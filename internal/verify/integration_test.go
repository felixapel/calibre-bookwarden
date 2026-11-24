//go:build calibre

package verify_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/decide"
	"github.com/felixapel/calibre-bookwarden/internal/duplicates"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/findings"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/ocr"
	"github.com/felixapel/calibre-bookwarden/internal/report"
	"github.com/felixapel/calibre-bookwarden/internal/store"
	"github.com/felixapel/calibre-bookwarden/internal/testutil/pgtest"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

// TestReadOnlyRunOnRealCalibre runs a whole check against the disposable
// Calibre in test/calibre and proves it changed no file in the library.
// scripts/integration-test.sh runs it inside the Calibre container.
func TestReadOnlyRunOnRealCalibre(t *testing.T) {
	env := func(name string) string {
		v := os.Getenv(name)
		if v == "" {
			t.Skipf("%s is not set; run scripts/integration-test.sh", name)
		}
		return v
	}
	calibreURL, password, libDir := env("BW_IT_CALIBRE_URL"), env("BW_IT_READER_PASSWORD"), env("BW_IT_LIBRARY_DIR")
	before := hashTree(t, libDir)

	ctx := context.Background()
	st, err := store.Open(ctx, pgtest.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	base, err := url.Parse(calibreURL)
	if err != nil {
		t.Fatal(err)
	}
	repo := store.NewRunRepo(st)
	// Debian's tesseract keeps its models elsewhere than Alpine's.
	tools := ocr.DefaultTools
	tools.TessdataDir = "/usr/share/tesseract-ocr/5/tessdata"
	hinter := ocr.New(tools)
	if err := hinter.SelfCheck(ctx, t.TempDir()); err != nil {
		t.Fatalf("OCR self-check: %v", err)
	}
	runner := verify.NewRunner(repo, library.New(library.Config{BaseURL: base, User: "reader", Password: password}),
		verify.Options{ScratchDir: t.TempDir(), Hinter: hinter})
	sctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- runner.Serve(sctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	run, err := runner.Start(ctx, verify.Scope{})
	if err != nil {
		t.Fatal(err)
	}
	var view verify.RunView
	deadline := time.Now().Add(2 * time.Minute)
	for {
		view, err = runner.View(ctx, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if view.Run.Status.Terminal() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run still %s after 2 minutes: %+v", view.Run.Status, view.Progress)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if view.Run.Status != verify.RunCompleted || view.Progress != (verify.Progress{Total: 10, Checked: 10}) {
		t.Fatalf("run ended %s (%q) with %+v", view.Run.Status, view.Run.Error, view.Progress)
	}

	pkg := func(bookID int) evidence.Package {
		t.Helper()
		raw, seal, err := repo.Evidence(ctx, run.ID, bookID)
		if err != nil {
			t.Fatalf("book %d: %v", bookID, err)
		}
		p, err := findings.Open(ctx, raw, seal)
		if err != nil {
			t.Fatalf("book %d evidence: %v", bookID, err)
		}
		return p
	}
	reports := report.New(store.NewReportRepo(st))
	for id := 1; id <= 10; id++ {
		p := pkg(id)
		b, err := reports.Book(ctx, run.ID, id)
		if err != nil || b.Package == nil || p.FindingsVersion != findings.Version || b.FindingsVersion != findings.Version || !slices.Equal(b.Package.Findings, p.Findings) || !slices.Equal(b.Findings, p.Findings) {
			t.Fatalf("book %d verified report: %+v/%v", id, b, err)
		}
		// CI has no sources, so no book can reach Tier A or get a field
		// proposal; book 8's planted title sort gets Calibre's own (ADR-028).
		wantProposals := []evidence.Proposal(nil)
		if id == 8 {
			wantProposals = []evidence.Proposal{{Field: "title_sort", Current: []string{"Draft Ledger"}, Proposed: []string{"Scanned Ledger, The"}, Roots: []string{evidence.RootCalibreRule}}}
		}
		if p.DecisionVersion != decide.Version || (p.Tier != evidence.TierB && p.Tier != evidence.TierC) || !reflect.DeepEqual(p.Proposals, wantProposals) || b.Decision.Tier != p.Tier {
			t.Fatalf("book %d decision: version %d tier %q reasons %v proposals %+v", id, p.DecisionVersion, p.Tier, p.TierReasons, p.Proposals)
		}
	}
	hasFinding := func(id int, want evidence.Finding) bool { return slices.Contains(pkg(id).Findings, want) }
	for _, tt := range []struct {
		id      int
		finding evidence.Finding
	}{
		{2, evidence.Finding{Code: "title_mismatch", Format: "EPUB", Field: "title"}},
		{4, evidence.Finding{Code: "no_formats"}},
		{5, evidence.Finding{Code: "file_missing", Format: "EPUB", Reason: evidence.ReasonMissingOnDisk}},
	} {
		if !hasFinding(tt.id, tt.finding) {
			t.Errorf("book %d missing finding %+v: %+v", tt.id, tt.finding, pkg(tt.id).Findings)
		}
	}
	for _, f := range pkg(3).Findings {
		switch f.Code {
		case "isbn_invalid", "isbn_mismatch", "formats_isbn_conflict", "language_invalid", "language_mismatch", "formats_language_conflict":
			t.Errorf("book 3 false ISBN/language finding: %+v", f)
		}
	}
	var exported bytes.Buffer
	if err := reports.CSV(ctx, run.ID, report.Filter{Code: "title_mismatch"}, &exported, report.DefaultCSVLimits()); err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(&exported).ReadAll()
	// Book 8's PDF still carries the title Calibre wrote into it on adding,
	// from before the retitle.
	if err != nil || len(rows) != 3 || rows[1][1] != "2" || rows[1][3] != "title_mismatch" || rows[2][1] != "8" || rows[2][3] != "title_mismatch" {
		t.Fatalf("filtered CSV=%v/%v", rows, err)
	}
	// Every format read is the file on disk, byte for byte, and its
	// inspection reports what the file itself says.
	type want struct {
		detected, text, title, author, isbn string
	}
	tides := func(detected string) want {
		return want{detected, evidence.TextPresent, "Binary Tides", "Cy Placeholder", "9780000000026"}
	}
	books := map[int]map[string]want{
		1: {"EPUB": {"epub", evidence.TextPresent, "The Clockwork Garden", "Ada Example", "9780000000002"}},
		2: {"EPUB": {"epub", evidence.TextPresent, "Lighthouse Signals", "Bea Sample", "9780000000019"}},
		3: {"AZW3": tides("mobi"), "EPUB": tides("epub"), "MOBI": tides("mobi")},
		6: {"PDF": {"pdf", evidence.TextPresent, "Paper Trail", "Fay Sample", ""}},
		7: {"CBZ": {"zip", "", "", "", ""}},
	}
	for id, formats := range books {
		p := pkg(id)
		if len(p.Formats) != len(formats) {
			t.Errorf("book %d: %d formats, want %d", id, len(p.Formats), len(formats))
		}
		for _, f := range p.Formats {
			w, ok := formats[f.Format]
			if !ok || f.SHA256 != fileHash(t, before, id, f.Format) || f.State != evidence.StateOK || f.Reason != "" || f.Detected != w.detected || f.Text != w.text {
				t.Errorf("book %d %s: %+v, want ok, detected %q, text %q and the file's sha256", id, f.Format, f, w.detected, w.text)
				continue
			}
			m := f.Metadata
			switch {
			case w.title == "" && m != nil:
				t.Errorf("book %d %s: metadata %+v, want none", id, f.Format, m)
			case w.title != "" && (m == nil || m.Title != w.title || len(m.Authors) != 1 || m.Authors[0] != w.author ||
				w.isbn != "" && (len(m.ISBNs) != 1 || m.ISBNs[0] != w.isbn)):
				t.Errorf("book %d %s: metadata %+v, want title %q, author %q, ISBN %q", id, f.Format, m, w.title, w.author, w.isbn)
			}
		}
	}
	// PDF ISBN hints: book 6 from its text layer, book 8 by OCR.
	hints := func(id int) *evidence.ISBNHints {
		p := pkg(id)
		if len(p.Formats) != 1 || p.Formats[0].Format != "PDF" {
			t.Fatalf("book %d formats: %+v", id, p.Formats)
		}
		return p.Formats[0].ISBNHints
	}
	for _, tt := range []struct {
		id           int
		method, isbn string
	}{{6, evidence.HintTextLayer, "9780000000040"}, {8, evidence.HintOCR, "9780000000057"}} {
		h := hints(tt.id)
		if h == nil || h.Method != tt.method || h.State != evidence.HintOK || !slices.Equal(h.Pages, []int{1}) || !slices.Equal(h.ISBNs, []string{tt.isbn}) {
			t.Errorf("book %d hints: %+v, want %s ok [1] [%s]", tt.id, h, tt.method, tt.isbn)
		}
	}
	if f := pkg(8).Formats[0]; f.State != evidence.StateOK || f.Detected != "pdf" || f.Text != evidence.TextAbsent || f.SHA256 != fileHash(t, before, 8, "PDF") {
		t.Errorf("book 8 PDF: %+v, want ok, pdf, no text layer", f)
	}
	for id := 1; id <= 7; id++ {
		if id != 6 {
			for _, f := range pkg(id).Formats {
				// A readable EPUB has front-matter hints; the generated ones
				// print no ISBN. Nothing else has hints.
				if h := f.ISBNHints; f.Format == "EPUB" && f.State == evidence.StateOK {
					if h == nil || h.Method != evidence.HintEPUBText || h.State != evidence.HintOK || len(h.ISBNs) != 0 || len(h.Context) != 0 {
						t.Errorf("book %d EPUB hints: %+v, want ok epub_text with no ISBN", id, h)
					}
				} else if h != nil {
					t.Errorf("book %d %s has hints: %+v", id, f.Format, h)
				}
			}
		}
	}
	// Title sort: captured from Calibre; book 8 was retitled, and Calibre
	// kept its old sort.
	if p := pkg(1); p.Calibre.TitleSort == nil || *p.Calibre.TitleSort != "Clockwork Garden, The" {
		t.Errorf("book 1 title sort: %v", p.Calibre.TitleSort)
	}
	if p := pkg(8); p.Calibre.Title != "The Scanned Ledger" || p.Calibre.TitleSort == nil || *p.Calibre.TitleSort != "Draft Ledger" {
		t.Errorf("book 8 title %q, sort %v", p.Calibre.Title, p.Calibre.TitleSort)
	}
	for id := 1; id <= 10; id++ {
		for _, f := range pkg(id).Findings {
			if f.Field == "title_sort" && (id != 8 || f.Code != "title_sort_mismatch") {
				t.Errorf("book %d title-sort finding: %+v", id, f)
			}
		}
	}
	if !hasFinding(8, evidence.Finding{Code: "title_sort_mismatch", Field: "title_sort"}) {
		t.Errorf("book 8 lacks title_sort_mismatch: %+v", pkg(8).Findings)
	}
	// Covers (ADR-023): Calibre stores every cover as JPEG at the size given.
	// calibredb add makes a cover from a PDF's first page (books 6 and 8) and
	// from a comic's first image (book 7, a flat grey 40 × 60 page).
	coverCodes := func(id int) []string {
		var codes []string
		for _, f := range pkg(id).Findings {
			if strings.HasPrefix(f.Code, "cover_") {
				codes = append(codes, f.Code)
			}
		}
		return codes
	}
	for _, tt := range []struct {
		id, w, h int
		codes    []string
	}{
		{1, 800, 1200, nil},
		{2, 300, 450, []string{"cover_low_resolution"}},
		{3, 600, 900, []string{"cover_placeholder"}},
		{6, 1240, 1755, nil},
		{7, 40, 60, []string{"cover_low_resolution", "cover_placeholder"}},
		{8, 1240, 1755, nil},
		{9, 800, 1200, nil},
		{10, 400, 600, []string{"cover_low_resolution"}},
	} {
		c := pkg(tt.id).Cover
		if c == nil || c.State != evidence.StateOK || c.Detected != "jpeg" || c.Width != tt.w || c.Height != tt.h || len(c.SHA256) != 64 || len(c.DHash) != 16 {
			t.Errorf("book %d cover: %+v", tt.id, c)
		}
		if got := coverCodes(tt.id); !slices.Equal(got, tt.codes) {
			t.Errorf("book %d cover findings %v, want %v", tt.id, got, tt.codes)
		}
	}
	for id := 4; id <= 5; id++ {
		if c := pkg(id).Cover; c == nil || c.State != evidence.StateMissing || c.Reason != "" || !slices.Equal(coverCodes(id), []string{"cover_missing"}) {
			t.Errorf("book %d without a cover: %+v, findings %v", id, c, coverCodes(id))
		}
	}
	// Duplicates (ADR-024): book 9 is book 1's file added again, and book
	// 10's cover is book 9's at half the size. The gradient, small and flat
	// covers of books 1 to 3 have too little detail to compare.
	if a, b := pkg(1).Formats, pkg(9).Formats; len(a) != 1 || len(b) != 1 || a[0].SHA256 == "" || a[0].SHA256 != b[0].SHA256 || a[0].SHA256 != fileHash(t, before, 9, "EPUB") {
		t.Errorf("book 9's EPUB isn't book 1's: %+v / %+v", a, b)
	}
	dups, err := reports.Duplicates(ctx, run.ID, report.DuplicateFilter{Page: 1})
	if err != nil || dups.Unavailable != "" || dups.Partial {
		t.Fatalf("duplicates: %+v, %v", dups, err)
	}
	var groups []string
	for _, g := range dups.Groups {
		ids := make([]int, 0, len(g.Members))
		for _, m := range g.Members {
			ids = append(ids, m.BookID)
		}
		groups = append(groups, fmt.Sprintf("%s %s %v", g.Kind, g.Value, ids))
	}
	wantGroups := []string{
		"isbn 9780000000002 [1 9]",
		"file " + pkg(1).Formats[0].SHA256 + " [1 9]",
		"title_author " + dupsTitleAuthorKey(pkg(1)) + " [1 9]",
		"cover  [9 10]",
	}
	if !slices.Equal(groups, wantGroups) {
		t.Errorf("duplicate groups\n got %q\nwant %q", groups, wantGroups)
	}
	if h, compared, _ := duplicates.CoverHash(pkg(1).Cover); compared {
		t.Errorf("book 1's gradient cover (hash %016x) is compared", h)
	}
	if bd, err := reports.BookDuplicates(ctx, run.ID, 9); err != nil || len(bd.Groups) != 4 {
		t.Errorf("book 9's duplicates: %+v, %v", bd, err)
	}
	// Book 2's file says one thing and Calibre's record another.
	if p := pkg(2); p.Calibre.Title != "Incorrect Catalogue Title" {
		t.Errorf("book 2's Calibre title: %q", p.Calibre.Title)
	}
	if p := pkg(4); len(p.Formats) != 0 {
		t.Errorf("book 4 should have no formats: %+v", p.Formats)
	}
	if p := pkg(5); len(p.Formats) != 1 || p.Formats[0].State != evidence.StateMissing || p.Formats[0].Reason != evidence.ReasonMissingOnDisk {
		t.Errorf("book 5 (file gone from disk): %+v", p.Formats)
	}
	historical, err := reports.Book(ctx, run.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	recheck, err := runner.Start(ctx, verify.Scope{BookID: 2, ExpectedLibraryID: run.LibraryID, ExpectedUUID: historical.Ref.UUID})
	if err != nil {
		t.Fatal(err)
	}
	checked := waitTerminal(t, runner, recheck.ID)
	if checked.Run.Status != verify.RunCompleted || checked.Progress != (verify.Progress{Total: 1, Checked: 1}) {
		t.Fatalf("recheck=%+v", checked)
	}
	frozen, err := reports.Books(ctx, recheck.ID, 0)
	if err != nil || len(frozen.Rows) != 1 || frozen.Rows[0].Ref != historical.Ref || frozen.NextBookID != 0 {
		t.Fatalf("scoped frozen books=%+v/%v", frozen, err)
	}

	if after := hashTree(t, libDir); !maps.Equal(before, after) {
		t.Fatal("the run changed the library")
	}
}

// hashTree maps every regular file under dir, metadata.db included, to its
// SHA-256.
func hashTree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		out[rel] = hex.EncodeToString(h.Sum(nil))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := out["metadata.db"]; !ok {
		t.Fatalf("%s holds no metadata.db", dir)
	}
	return out
}

// fileHash finds the one file of book id's format, "…(<id>)/<name>.<ext>".
func fileHash(t *testing.T, tree map[string]string, id int, format string) string {
	t.Helper()
	var found []string
	for path := range tree {
		if strings.Contains(path, "("+strconv.Itoa(id)+")"+string(filepath.Separator)) && strings.HasSuffix(path, "."+strings.ToLower(format)) {
			found = append(found, path)
		}
	}
	if len(found) != 1 {
		t.Fatalf("book %d %s: %d files match: %v", id, format, len(found), found)
	}
	return tree[found[0]]
}

// dupsTitleAuthorKey is the title_author key of a package.
func dupsTitleAuthorKey(p evidence.Package) string {
	for _, k := range duplicates.Keys(p) {
		if k.Kind == duplicates.KindTitleAuthor {
			return k.Value
		}
	}
	return ""
}
