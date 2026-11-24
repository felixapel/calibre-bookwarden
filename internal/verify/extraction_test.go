package verify_test

import (
	"context"
	"sync"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
	"github.com/felixapel/calibre-bookwarden/internal/verify/verifytest"
)

// fakeExtraction seals a block naming one extractor, and remembers what it
// was asked. With entered set, it waits for its context instead.
type fakeExtraction struct {
	mu      sync.Mutex
	name    string
	runs    []int64
	bodies  []string
	entered chan struct{}
}

func (f *fakeExtraction) BeginRun(id int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs = append(f.runs, id)
}

func (f *fakeExtraction) Extract(ctx context.Context, ex evidence.Excerpt, body string) (*evidence.Extraction, []verify.RawOutput, error) {
	f.mu.Lock()
	f.bodies = append(f.bodies, body)
	name := f.name
	f.mu.Unlock()
	if f.entered != nil {
		f.entered <- struct{}{}
		<-ctx.Done()
		return nil, nil, ctx.Err()
	}
	return &evidence.Extraction{PromptVersion: "p", ExtractVersion: 1, ExcerptSHA256: ex.SHA256,
		Extractors: []evidence.ExtractorRun{{Name: name, State: evidence.ExtractUnavailable}}}, nil, nil
}

func (f *fakeExtraction) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.bodies)
}

func sealedFormats(t *testing.T, store *verifytest.MemStore, run int64, book int) []evidence.FormatEvidence {
	t.Helper()
	raw, seal, ok := store.Evidence(run, book)
	if !ok {
		t.Fatalf("no package for book %d in run %d", book, run)
	}
	p, err := evidence.Open(raw, seal)
	if err != nil {
		t.Fatal(err)
	}
	return p.Formats
}

func TestRunSealsExtraction(t *testing.T) {
	lib := verifytest.NewLibrary("library", fileBook(3, "EPUB", tidesEPUB))
	store := verifytest.NewMemStore()
	f := &fakeExtraction{name: "a"}
	v := runOnce(t, store, lib, verify.Options{Extraction: f}, verify.Scope{})
	formats := sealedFormats(t, store, v.Run.ID, 3)
	if formats[0].Excerpt == nil {
		t.Fatal("the test EPUB has no excerpt")
	}
	x := formats[0].Extraction
	if x == nil || x.ExcerptSHA256 != formats[0].Excerpt.SHA256 || x.Extractors[0].Name != "a" {
		t.Fatalf("extraction = %+v", x)
	}
	if len(f.runs) != 1 || f.runs[0] != v.Run.ID || f.calls() != 1 || f.bodies[0] == "" {
		t.Fatalf("runs %v, calls %d", f.runs, f.calls())
	}
}

func TestNoExtractionWithoutService(t *testing.T) {
	lib := verifytest.NewLibrary("library", fileBook(3, "EPUB", tidesEPUB))
	store := verifytest.NewMemStore()
	v := runOnce(t, store, lib, verify.Options{}, verify.Scope{})
	if x := sealedFormats(t, store, v.Run.ID, 3)[0].Extraction; x != nil {
		t.Fatalf("extraction without a service: %+v", x)
	}
}

func TestPDFTextLayerIsNotExtracted(t *testing.T) {
	ex := syntheticExcerpt(evidence.ExcerptTextLayer, "Synthetic copyright page")
	h := &excerptHinter{excerpt: ex}
	lib := verifytest.NewLibrary("library", fileBook(4, "PDF", paperPDF))
	store := verifytest.NewMemStore()
	f := &fakeExtraction{name: "a"}
	v := runOnce(t, store, lib, verify.Options{Extraction: f, Hinter: h}, verify.Scope{})
	formats := sealedFormats(t, store, v.Run.ID, 4)
	if formats[0].Excerpt == nil || formats[0].Extraction != nil || f.calls() != 0 {
		t.Fatalf("excerpt %v, extraction %+v, calls %d", formats[0].Excerpt != nil, formats[0].Extraction, f.calls())
	}
}

func TestReusedFormatsAreReextracted(t *testing.T) {
	lib := verifytest.NewLibrary("library", fileBook(3, "EPUB", tidesEPUB))
	store := verifytest.NewMemStore()
	f := &fakeExtraction{name: "first"}
	runOnce(t, store, lib, verify.Options{Extraction: f}, verify.Scope{})
	f.mu.Lock()
	f.name = "second"
	f.mu.Unlock()
	v := runOnce(t, store, lib, verify.Options{Extraction: f}, verify.Scope{Incremental: true})
	if v.Progress.Reused != 1 {
		t.Fatalf("progress = %+v; want the file evidence reused", v.Progress)
	}
	if x := sealedFormats(t, store, v.Run.ID, 3)[0].Extraction; x == nil || x.Extractors[0].Name != "second" || f.calls() != 2 {
		t.Fatalf("extraction = %+v after %d calls; want it made again", x, f.calls())
	}
	// Without a service, a reused package carries no old block either.
	v = runOnce(t, store, lib, verify.Options{}, verify.Scope{Incremental: true})
	if x := sealedFormats(t, store, v.Run.ID, 3)[0].Extraction; x != nil || v.Progress.Reused != 1 {
		t.Fatalf("extraction %+v, progress %+v", x, v.Progress)
	}
}

func TestCheckExtractionCancelled(t *testing.T) {
	lib := verifytest.NewLibrary("library", fileBook(3, "EPUB", tidesEPUB))
	store := verifytest.NewMemStore()
	f := &fakeExtraction{name: "a", entered: make(chan struct{}, 1)}
	r, stop := serveWith(t, store, lib, verify.Options{Extraction: f})
	defer stop()
	run := start(t, r, verify.Scope{})
	<-f.entered
	if err := r.Cancel(context.Background(), run.ID); err != nil {
		t.Fatal(err)
	}
	if v := waitTerminal(t, r, run.ID); v.Run.Status != verify.RunCancelled {
		t.Fatalf("status = %s", v.Run.Status)
	}
	if _, _, ok := store.Evidence(run.ID, 3); ok {
		t.Fatal("a book was sealed with its extraction cut short")
	}
}
