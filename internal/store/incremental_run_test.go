package store

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/inspect/inspecttest"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/sources"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
	"github.com/felixapel/calibre-bookwarden/internal/verify/verifytest"
)

// runToEnd runs one whole run on a runner over r and waits for it.
func runToEnd(t *testing.T, r *RunRepo, lib verify.Library, scope verify.Scope, opts verify.Options) verify.RunView {
	t.Helper()
	opts.ScratchDir = t.TempDir()
	opts.Backoff = func(int) time.Duration { return time.Millisecond }
	runner := verify.NewRunner(r, lib, opts)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Serve(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Serve() = %v", err)
		}
	}()
	run, err := runner.Start(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		v, err := runner.View(ctx, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if v.Run.Status.Terminal() {
			return v
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %d did not end", run.ID)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// fakeHinter stands in for the OCR engine: a text layer yields hints and an
// excerpt, a scan yields OCR hints.
type fakeHinter struct{}

func (fakeHinter) PDFHints(ctx context.Context, path string, pages int, textLayer bool, languages []string, left *time.Duration) *evidence.ISBNHints {
	h, _ := fakeHinter{}.PDFHintsExcerpt(ctx, path, pages, textLayer, languages, left)
	return h
}

func (fakeHinter) PDFHintsExcerpt(_ context.Context, _ string, _ int, textLayer bool, _ []string, _ *time.Duration) (*evidence.ISBNHints, *evidence.Excerpt) {
	if !textLayer {
		return &evidence.ISBNHints{Method: evidence.HintOCR, State: evidence.HintOK, Pages: []int{1}, ISBNs: []string{"9780000000040"},
			Context: []evidence.ISBNContext{{ISBN: "9780000000040"}}, Lang: "eng"}, nil
	}
	b := evidence.NewExcerptBuilder(evidence.ExcerptTextLayer)
	b.Add(1, "", "Paper Trail. Print ISBN 978-0-00-000004-0. A tiny generated book.")
	return &evidence.ISBNHints{Method: evidence.HintTextLayer, State: evidence.HintOK, Pages: []int{1}, ISBNs: []string{"9780000000040"},
		Context: []evidence.ISBNContext{{ISBN: "9780000000040", Qualifier: evidence.QualifierPrint}}}, b.Excerpt()
}

// fakeSources answers every seed with one record, the same way each time.
type fakeSources struct{}

func (fakeSources) BeginRun(context.Context, int64, int) {}

func (fakeSources) Lookup(_ context.Context, _ int64, seeds []sources.Seed) []evidence.Lookup {
	var out []evidence.Lookup
	for _, sd := range seeds {
		out = append(out, evidence.Lookup{ISBN: sd.ISBN, Seeds: []string{sd.Class}, Results: []evidence.SourceResult{
			{Source: evidence.SourceDNB, Outcome: evidence.SourceOK, Records: []evidence.SourceRecord{{ID: "dnb-" + sd.ISBN, Title: "Binary Tides", Authors: []string{"Cy Placeholder"}, ISBNs: []string{sd.ISBN}}}},
		}})
	}
	return out
}

// An incremental run on the same files seals exactly the packages a full run
// seals (ADR-039). The only differences are outside the seal: the run id the
// rows belong to, their timestamps, the reused flag and the key of the
// inspector build. The package holds no run id, so the sealed bytes and the
// seal are compared whole.
func TestIncrementalPackagesAreByteIdenticalToFullOnes(t *testing.T) {
	ctx := context.Background()
	r, s := runRepo(t)
	page := func(body string) string {
		return `<?xml version="1.0" encoding="utf-8"?>
<html xmlns="http://www.w3.org/1999/xhtml"><head><title>T</title></head><body>` + body + `</body></html>`
	}
	// An EPUB 3 whose title page and copyright page carry ISBNs with a landmark
	// and a qualifier each (ADR-032).
	epub := inspecttest.EPUB(inspecttest.EPUBOptions{
		Title: "Binary Tides", Authors: []string{"Cy Placeholder"}, Language: "en", ISBN: "9780000000026", EPUB3: true,
		ContentDoc: page(`<p>Binary Tides, Print ISBN 978-0-00-000006-4</p>`),
		Extra:      []string{page(`<p>Copyright. eBook ISBN 978-0-00-000004-0.</p>`), page(`<p>Front matter that becomes a stored excerpt.</p>`)},
		Nav: `<?xml version="1.0" encoding="utf-8"?>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops"><head><title>Nav</title></head><body>
<nav epub:type="landmarks"><ol><li><a epub:type="titlepage" href="text/chapter%201.xhtml">x</a></li><li><a epub:type="copyright-page" href="text/extra0.xhtml">x</a></li></ol></nav></body></html>`,
	})
	pdf := inspecttest.PDF(inspecttest.PDFOptions{Info: map[string]string{"Title": "Paper Trail"}, Text: "A tiny generated book."})
	mk := func(id int, formats map[string][]byte) verifytest.FakeBook {
		b := verifytest.FakeBook{Book: library.Book{ID: id, UUID: "uuid-" + string(rune('a'+id)), Title: "Binary Tides", Authors: []string{"Cy Placeholder"},
			Languages: []string{"eng"}, Identifiers: map[string]string{"isbn": "9780000000026"}, LastModified: time.Date(2026, 10, 12, 21, 5, 55, 0, time.UTC)}, Files: formats}
		for name, content := range formats {
			b.Book.Formats = append(b.Book.Formats, library.Format{Name: name, Size: int64(len(content)), MTime: b.Book.LastModified})
		}
		if id%2 == 1 { // odd books have a cover
			b.Book.HasCover = true
			b.Cover = inspecttest.JPEG(48, 64)
		}
		return b
	}
	lib := verifytest.NewLibrary("library",
		mk(1, map[string][]byte{"EPUB": epub}),
		mk(2, map[string][]byte{"EPUB": epub, "PDF": pdf}),
		mk(3, map[string][]byte{"PDF": pdf}),
		mk(4, map[string][]byte{}),
	)

	opts := verify.Options{Hinter: fakeHinter{}, Sources: fakeSources{}, ToolVersions: "pdftotext 1;tesseract 1"}
	full := runToEnd(t, r, lib, verify.Scope{}, opts)
	if full.Progress.Checked != 4 || full.Progress.Reused != 0 {
		t.Fatalf("full run = %+v", full.Progress)
	}
	incremental := runToEnd(t, r, lib, verify.Scope{Incremental: true}, opts)
	if incremental.Progress.Checked != 4 || incremental.Progress.Reused != 4 {
		t.Fatalf("incremental run = %+v; every book is unchanged and should be reused", incremental.Progress)
	}
	for _, id := range []int{1, 2, 3, 4} {
		a, sealA, err := r.Evidence(ctx, full.Run.ID, id)
		if err != nil {
			t.Fatal(err)
		}
		b, sealB, err := r.Evidence(ctx, incremental.Run.ID, id)
		if err != nil {
			t.Fatal(err)
		}
		pa, err := evidence.Open(a, sealA)
		if err != nil {
			t.Fatal(err)
		}
		pb, err := evidence.Open(b, sealB)
		if err != nil {
			t.Fatal(err)
		}
		ca, _ := evidence.Canonical(pa)
		cb, _ := evidence.Canonical(pb)
		if sealA != sealB || !bytes.Equal(ca, cb) {
			t.Errorf("book %d: the incremental package differs from the full one\nfull %s\nincr %s", id, ca, cb)
		}
	}
	// The test must exercise what it claims to: a cover, an EPUB ISBN with
	// landmark and qualifier, a PDF text-layer excerpt and source lookups, all
	// of it in the package read back from jsonb.
	for _, id := range []int{1, 2, 3} {
		raw, seal, err := r.Evidence(ctx, incremental.Run.ID, id)
		if err != nil {
			t.Fatal(err)
		}
		p, err := evidence.Open(raw, seal)
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Lookups) == 0 {
			t.Errorf("book %d: no source lookups in the package", id)
		}
		if has := p.Cover != nil && p.Cover.State == evidence.StateOK && p.Cover.DHash != ""; (id%2 == 1) != has {
			t.Errorf("book %d: cover = %v", id, p.Cover)
		}
		for _, f := range p.Formats {
			switch {
			case f.Format == "EPUB" && (f.ISBNHints == nil || len(f.ISBNHints.Context) == 0 || f.ISBNHints.Context[0].Landmark == "" || f.ISBNHints.Context[0].Qualifier == ""):
				t.Errorf("book %d: EPUB hints lack landmark and qualifier: %+v", id, f.ISBNHints)
			case f.Format == "PDF" && (f.Excerpt == nil || f.Excerpt.Method != evidence.ExcerptTextLayer):
				t.Errorf("book %d: PDF has no text-layer excerpt: %+v", id, f.Excerpt)
			}
		}
	}
	// Outside the seal: the reuse is recorded, and so is the key.
	var keys, reusedBooks int
	if err := s.Pool.QueryRow(ctx, `SELECT count(DISTINCT e.inspect_version), count(*) FILTER (WHERE b.reused)
		FROM evidence e JOIN run_books b USING (run_id, book_id) WHERE e.inspect_version <> ''`).Scan(&keys, &reusedBooks); err != nil || keys != 1 || reusedBooks != 4 {
		t.Errorf("%d inspector keys, %d reused books, %v; want one key and 4 reused", keys, reusedBooks, err)
	}

	// The reused packages hold their excerpts too: pruning the full run
	// leaves them readable.
	if n := excerptCount(t, s); n < 1 {
		t.Fatal("the test EPUB left no excerpt, so it proves nothing about reusing them")
	}
	if res, err := r.Prune(ctx, 1); err != nil || res.Runs != 1 || res.Excerpts != 0 {
		t.Fatalf("Prune() = %+v, %v; want the full run gone and every excerpt still referenced", res, err)
	}
	if _, ok, err := r.PriorEvidence(ctx, "library", library.BookRef{ID: 2, UUID: "uuid-c"}); !ok || err != nil {
		t.Errorf("after the prune the newest package of book 2 is unusable: %v, %v", ok, err)
	}
}
