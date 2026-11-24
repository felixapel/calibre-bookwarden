package eval_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/decide"
	"github.com/felixapel/calibre-bookwarden/internal/eval"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/findings"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/store"
	"github.com/felixapel/calibre-bookwarden/internal/testutil/pgtest"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

const (
	isbnA      = "9780306406157"
	workTitle  = "Qwxzy Chronicle"
	workAuthor = "Pvlmk Hartwell"
	otherTitle = "Jqvbn Almanac"
	otherName  = "Rtgfd Lowell"
)

// v6Pkg is a book whose file and one DNB record agree on workTitle by
// workAuthor (the record naming recordAuthors), and whose Calibre record says
// calibreTitle by calibreAuthor.
func v6Pkg(b library.BookRef, calibreTitle, calibreAuthor string, recordAuthors []string) evidence.Package {
	return evidence.Package{
		SchemaVersion: evidence.SchemaVersionV1, LibraryID: "library", BookID: b.ID, UUID: b.UUID,
		Calibre: evidence.Record{Title: calibreTitle, Authors: []string{calibreAuthor}, Languages: []string{"deu"}, Identifiers: map[string]string{"isbn": isbnA}},
		Formats: []evidence.FormatEvidence{{Format: "EPUB", State: evidence.StateOK, Detected: "epub", Metadata: &evidence.FileMetadata{
			Title: workTitle, Authors: []string{workAuthor}, Languages: []string{"de"}, ISBNs: []string{isbnA}}}},
		Lookups: []evidence.Lookup{{ISBN: isbnA, Seeds: []string{evidence.SeedFile}, Results: []evidence.SourceResult{{
			Source: evidence.SourceDNB, Outcome: evidence.SourceOK,
			Records: []evidence.SourceRecord{{ID: "1", Title: workTitle, Authors: recordAuthors, Languages: []string{"deu"}, ISBNs: []string{isbnA}}},
		}}}},
	}
}

// sealAt seals p the way a build of decision version v would have.
func sealAt(ctx context.Context, t *testing.T, p evidence.Package, v int) ([]byte, string, error) {
	t.Helper()
	fs, err := findings.Derive(ctx, p)
	if err != nil {
		return nil, "", err
	}
	d, err := decide.AtVersion(ctx, p, v)
	if err != nil {
		return nil, "", err
	}
	p.FindingsVersion, p.Findings = findings.Version, fs
	p.DecisionVersion, p.Tier, p.TierReasons, p.Proposals = v, d.Tier, d.Reasons, d.Proposals
	return evidence.Seal(p)
}

// TestReplayV5ToV6 replays the rules of ADR-035 part 2: a book that becomes
// another work, a book that loses its ISBN proposal because the record's
// authors differ, and a book nothing changes for.
func TestReplayV5ToV6(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, pgtest.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	runs := store.NewRunRepo(st)
	books := []library.BookRef{{ID: 1, UUID: "u1"}, {ID: 2, UUID: "u2"}, {ID: 3, UUID: "u3"}}
	r, err := runs.CreateRun(ctx, verify.Scope{}, "library", "v1", books)
	if err != nil {
		t.Fatal(err)
	}
	pkgs := map[int]evidence.Package{
		// Calibre holds another work than the files and the record.
		1: v6Pkg(books[0], otherTitle, otherName, []string{workAuthor}),
		// Calibre has the work, but the record names another author.
		2: v6Pkg(books[1], workTitle, workAuthor, []string{otherName}),
		// Everything agrees.
		3: v6Pkg(books[2], workTitle, workAuthor, []string{workAuthor}),
	}
	pkgs[2].Calibre.Identifiers["isbn"] = "9780596520687"
	for _, b := range books {
		raw, seal, err := sealAt(ctx, t, pkgs[b.ID], 5)
		if err != nil {
			t.Fatal(err)
		}
		if err := runs.FinishBook(ctx, r.ID, b, verify.Outcome{State: verify.BookChecked, Evidence: raw, Seal: seal}); err != nil {
			t.Fatal(err)
		}
	}
	if err := runs.SetStatus(ctx, r.ID, verify.RunCompleted, ""); err != nil {
		t.Fatal(err)
	}
	res, err := eval.Replay(ctx, store.NewReportRepo(st), eval.Options{RunID: r.ID, Version: 6})
	if err != nil {
		t.Fatal(err)
	}
	if res.Compared != 3 || res.Broken != 0 {
		t.Fatalf("compared %d, broken %d", res.Compared, res.Broken)
	}
	var changed []int
	for _, b := range res.Books {
		changed = append(changed, b.Book)
	}
	if fmt.Sprint(changed) != "[1 2]" {
		t.Fatalf("changed books %v, want [1 2]", changed)
	}
	other := res.Books[0]
	if other.To != evidence.TierC || other.From == evidence.TierC || len(other.Removed) == 0 || len(other.Gained) != 1 || other.Gained[0] != "other_work" {
		t.Fatalf("other work book: %+v", other)
	}
	if got := res.Matrix[other.From][evidence.TierC]; got != 1 {
		t.Fatalf("matrix %s->C = %d: %v", other.From, got, res.Matrix)
	}
	if rd := res.Reasons["other_work"]; rd.Gained != 1 || rd.Lost != 0 {
		t.Fatalf("other_work: %+v", rd)
	}
	isbn := res.Books[1]
	if isbn.From != isbn.To || len(isbn.Removed) != 1 || isbn.Removed[0] != "isbn" || len(isbn.Added)+len(isbn.Changed) != 0 {
		t.Fatalf("isbn book: %+v", isbn)
	}
	if d := res.Fields["isbn"]; d.Removed != 1 || d.Baseline != 1 || d.Version != 0 {
		t.Fatalf("isbn field: %+v", d)
	}
	var text strings.Builder
	res.WriteText(&text, true)
	out := text.String()
	for _, want := range []string{"book 1: ", "book 2: "} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in %s", want, out)
		}
	}
	if strings.Contains(out, "book 3: ") {
		t.Fatalf("the unchanged book is listed: %s", out)
	}
	for _, leak := range []string{workTitle, workAuthor, otherTitle, otherName, "Qwxzy", "Jqvbn", "Hartwell", "Lowell"} {
		if strings.Contains(out, leak) {
			t.Fatalf("output leaks %q", leak)
		}
	}
}
