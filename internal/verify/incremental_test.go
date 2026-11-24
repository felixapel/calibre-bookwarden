package verify_test

import (
	"bytes"
	"context"
	"slices"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/findings"
	"github.com/felixapel/calibre-bookwarden/internal/inspect"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
	"github.com/felixapel/calibre-bookwarden/internal/verify/verifytest"
)

// incrementalLibrary has a book with a cover, one without, and one with two
// formats, all with ISBNs the fake sources are asked about.
func incrementalLibrary(t *testing.T) *verifytest.FakeLibrary {
	t.Helper()
	withCover := coverBook(1, coverPNG(t, 40, 60))
	withCover.Book.Identifiers = map[string]string{"isbn": "0-00-000002-7"}
	plain := coverBook(2, nil)
	two := fileBook(3, "EPUB", tidesEPUB)
	two.Book.Formats = append(two.Book.Formats, library.Format{Name: "PDF", Size: int64(len(paperPDF)), MTime: t0})
	two.Files["PDF"] = paperPDF
	return verifytest.NewLibrary("library", withCover, plain, two)
}

// runOnce starts a run with scope on a runner over store and waits for it.
func runOnce(t *testing.T, store verify.RunStore, lib verify.Library, opts verify.Options, scope verify.Scope) verify.RunView {
	t.Helper()
	r, stop := serveWith(t, store, lib, opts)
	defer stop()
	return waitTerminal(t, r, start(t, r, scope).ID)
}

func TestIncrementalRunReusesFileEvidence(t *testing.T) {
	lib := incrementalLibrary(t)
	store := verifytest.NewMemStore()
	src := &fakeSources{}
	opts := verify.Options{Sources: src}

	full := runOnce(t, store, lib, opts, verify.Scope{})
	if full.Progress.Checked != 3 || full.Progress.Reused != 0 {
		t.Fatalf("full run = %+v", full.Progress)
	}
	incremental := runOnce(t, store, lib, opts, verify.Scope{Incremental: true})
	if incremental.Run.Status != verify.RunCompleted || incremental.Progress.Checked != 3 || incremental.Progress.Reused != 3 {
		t.Fatalf("incremental run = %s %+v; want 3 checked, all reused", incremental.Run.Status, incremental.Progress)
	}
	if !incremental.Run.Scope.Incremental {
		t.Error("the run does not say it is incremental")
	}

	// Nothing was downloaded again.
	for _, c := range []struct {
		id     int
		format string
	}{{1, "EPUB"}, {1, "cover"}, {2, "EPUB"}, {3, "EPUB"}, {3, "PDF"}} {
		if n := lib.ExportCalls(c.id, c.format); n != 1 {
			t.Errorf("book %d %s exported %d times, want 1", c.id, c.format, n)
		}
	}
	// The lookups ran again, in the new run.
	src.mu.Lock()
	var inNew int
	for _, id := range src.runs {
		if id == incremental.Run.ID {
			inNew++
		}
	}
	src.mu.Unlock()
	if inNew != 3 {
		t.Errorf("%d lookups in the incremental run, want 3 (the source cache makes them cheap, they are not skipped)", inNew)
	}
	// Determinism: the same files and the same source answers seal the same
	// bytes, in the new run (ADR-039).
	for id := 1; id <= 3; id++ {
		a, sealA, _ := store.Evidence(full.Run.ID, id)
		b, sealB, _ := store.Evidence(incremental.Run.ID, id)
		if !bytes.Equal(a, b) || sealA != sealB || sealA == "" {
			t.Errorf("book %d: the incremental package differs from the full one\nfull %s\nincr %s", id, a, b)
		}
	}
	// Every reused package is sealed at the current versions.
	for id := 1; id <= 3; id++ {
		raw, seal, _ := store.Evidence(incremental.Run.ID, id)
		p, err := findings.Open(context.Background(), raw, seal)
		if err != nil || p.FindingsVersion != findings.Version {
			t.Errorf("book %d: findings version %d, %v", id, p.FindingsVersion, err)
		}
	}
}

func TestFullRunNeverReuses(t *testing.T) {
	lib := incrementalLibrary(t)
	store := verifytest.NewMemStore()
	runOnce(t, store, lib, verify.Options{}, verify.Scope{})
	again := runOnce(t, store, lib, verify.Options{}, verify.Scope{})
	if again.Progress.Reused != 0 || lib.ExportCalls(1, "EPUB") != 2 {
		t.Errorf("a full run reused %d books; EPUB exports of book 1 = %d", again.Progress.Reused, lib.ExportCalls(1, "EPUB"))
	}
}

func TestIncrementalRunWithNothingBeforeChecksInFull(t *testing.T) {
	lib := incrementalLibrary(t)
	v := runOnce(t, verifytest.NewMemStore(), lib, verify.Options{}, verify.Scope{Incremental: true})
	if v.Progress.Checked != 3 || v.Progress.Reused != 0 || lib.ExportCalls(1, "EPUB") != 1 {
		t.Errorf("first run, incremental: %+v", v.Progress)
	}
}

func TestIncrementalRunChecksChangedBooksInFull(t *testing.T) {
	tests := []struct {
		name   string
		change func(*verifytest.FakeBook)
	}{
		{"last_modified", func(b *verifytest.FakeBook) { b.Book.LastModified = b.Book.LastModified.Add(time.Minute) }},
		{"format mtime", func(b *verifytest.FakeBook) { b.Book.Formats[0].MTime = b.Book.Formats[0].MTime.Add(time.Second) }},
		{"format size", func(b *verifytest.FakeBook) {
			b.Files["EPUB"] = append(slices.Clone(b.Files["EPUB"]), 0)
			b.Book.Formats[0].Size++
		}},
		{"a format added", func(b *verifytest.FakeBook) {
			b.Book.Formats = append(b.Book.Formats, library.Format{Name: "PDF", Size: int64(len(paperPDF)), MTime: t0})
			b.Files["PDF"] = paperPDF
		}},
		{"identifiers", func(b *verifytest.FakeBook) { b.Book.Identifiers = map[string]string{"isbn": "9780000000026"} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lib := incrementalLibrary(t)
			store := verifytest.NewMemStore()
			runOnce(t, store, lib, verify.Options{}, verify.Scope{})
			lib.Edit(2, tt.change)
			v := runOnce(t, store, lib, verify.Options{}, verify.Scope{Incremental: true})
			if v.Progress.Checked != 3 || v.Progress.Reused != 2 {
				t.Fatalf("incremental run = %+v; want book 2 checked in full, the other two reused", v.Progress)
			}
			if lib.ExportCalls(2, "EPUB") != 2 || lib.ExportCalls(1, "EPUB") != 1 || lib.ExportCalls(3, "EPUB") != 1 {
				t.Errorf("EPUB exports: book 1 %d, book 2 %d, book 3 %d", lib.ExportCalls(1, "EPUB"), lib.ExportCalls(2, "EPUB"), lib.ExportCalls(3, "EPUB"))
			}
		})
	}
}

// A different build of the inspectors, or other limits, must not inherit the
// file evidence of an earlier one.
func TestIncrementalRunChecksInFullUnderOtherInspectorSettings(t *testing.T) {
	smaller := verify.DefaultLimits
	smaller.Inspect.MaxEntries = 500
	tests := []struct {
		name string
		opts verify.Options
	}{
		{"other inspect limits", verify.Options{Limits: smaller}},
		{"other file limit", verify.Options{Limits: verify.Limits{FileSize: 1 << 20, InFlight: 1 << 30, Inspect: inspect.DefaultLimits, HintTime: verify.DefaultLimits.HintTime}}},
		{"other hint time", verify.Options{Limits: verify.Limits{FileSize: verify.DefaultLimits.FileSize, InFlight: 1 << 30, Inspect: inspect.DefaultLimits, HintTime: time.Second}}},
		{"a hinter appears", verify.Options{Hinter: noHints{}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lib := incrementalLibrary(t)
			store := verifytest.NewMemStore()
			runOnce(t, store, lib, verify.Options{}, verify.Scope{})
			v := runOnce(t, store, lib, tt.opts, verify.Scope{Incremental: true})
			if v.Progress.Checked != 3 || v.Progress.Reused != 0 || lib.ExportCalls(1, "EPUB") != 2 {
				t.Errorf("incremental run = %+v, EPUB exports of book 1 = %d; want a full check", v.Progress, lib.ExportCalls(1, "EPUB"))
			}
		})
	}
}

// noHints is a Hinter that finds nothing, so only its presence matters.
type noHints struct{}

func (noHints) PDFHints(context.Context, string, int, bool, []string, *time.Duration) *evidence.ISBNHints {
	return nil
}

// dropsBodies is a store whose prior packages come without their excerpt
// bodies, as if the store had checked them badly.
type dropsBodies struct{ *verifytest.MemStore }

func (s dropsBodies) PriorEvidence(ctx context.Context, libraryID string, ref library.BookRef) (verify.Prior, bool, error) {
	p, ok, err := s.MemStore.PriorEvidence(ctx, libraryID, ref)
	p.Excerpts = nil
	return p, ok, err
}

func TestIncrementalRunFallsBackWhenAnExcerptBodyIsMissing(t *testing.T) {
	lib := incrementalLibrary(t)
	mem := verifytest.NewMemStore()
	runOnce(t, mem, lib, verify.Options{}, verify.Scope{})
	v := runOnce(t, dropsBodies{mem}, lib, verify.Options{}, verify.Scope{Incremental: true})
	if v.Progress.Checked != 3 || v.Progress.Reused != 0 {
		t.Errorf("incremental run = %+v; every package refers to an excerpt, so none can be reused", v.Progress)
	}
}
