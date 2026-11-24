package verify_test

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/decide"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/sources"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
	"github.com/felixapel/calibre-bookwarden/internal/verify/verifytest"
)

// fakeSources records its calls and answers every ISBN with DNB no_match.
type fakeSources struct {
	mu      sync.Mutex
	begun   []int64
	budgets []int
	seeds   [][]sources.Seed
	runs    []int64
	order   *[]string
	during  func()
}

func (f *fakeSources) BeginRun(_ context.Context, runID int64, refresh int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.begun = append(f.begun, runID)
	f.budgets = append(f.budgets, refresh)
}

func (f *fakeSources) Lookup(_ context.Context, runID int64, seeds []sources.Seed) []evidence.Lookup {
	f.mu.Lock()
	f.seeds = append(f.seeds, seeds)
	f.runs = append(f.runs, runID)
	if f.order != nil {
		*f.order = append(*f.order, "lookup")
	}
	during := f.during
	f.mu.Unlock()
	if during != nil {
		during()
	}
	var out []evidence.Lookup
	for _, s := range seeds {
		if len(out) == 0 || out[len(out)-1].ISBN != s.ISBN {
			out = append(out, evidence.Lookup{ISBN: s.ISBN, Results: []evidence.SourceResult{{Source: evidence.SourceDNB, Outcome: evidence.SourceNoMatch}}})
		}
		out[len(out)-1].Seeds = append(out[len(out)-1].Seeds, s.Class)
	}
	return out
}

func (f *fakeSources) begins() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.begun)
}

// isbnBook is an EPUB carrying ISBN 9780000000026 whose Calibre record
// carries the same ISBN in its 10-digit form.
func isbnBook() verifytest.FakeBook {
	b := fileBook(4, "EPUB", tidesEPUB)
	b.Book.Identifiers = map[string]string{"isbn": "0-00-000002-7"}
	return b
}

func TestCheckBookLookups(t *testing.T) {
	b := isbnBook()
	lib := verifytest.NewLibrary("library", b)
	var order []string
	lib.MutateOnRead(b.Book.ID, 2, func(*library.Book) bool { order = append(order, "reread"); return false })
	src := &fakeSources{order: &order}
	out, err := verify.CheckBookSources(t.Context(), lib, library.BookRef{ID: b.Book.ID, UUID: b.Book.UUID}, t.TempDir(), src, 42)
	if err != nil || out.State != verify.BookChecked {
		t.Fatalf("CheckBook = %+v, %v", out, err)
	}
	want := []sources.Seed{{ISBN: "9780000000026", Class: evidence.SeedFile}, {ISBN: "9780000000026", Class: evidence.SeedCalibre}}
	if len(src.seeds) != 1 || !slices.Equal(src.seeds[0], want) || src.runs[0] != 42 {
		t.Fatalf("Lookup calls: seeds %+v runs %v", src.seeds, src.runs)
	}
	if !slices.Equal(order, []string{"lookup", "reread"}) {
		t.Fatalf("order = %v, want the lookup before the re-read", order)
	}
	p, err := evidence.Open(out.Evidence, out.Seal)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Lookups) != 1 || p.Lookups[0].ISBN != "9780000000026" || !slices.Equal(p.Lookups[0].Seeds, []string{"file", "calibre"}) ||
		p.Lookups[0].Results[0].Outcome != evidence.SourceNoMatch {
		t.Fatalf("sealed lookups = %+v", p.Lookups)
	}
	if p.DecisionVersion != decide.Version || p.Tier == "" {
		t.Fatalf("sealed without a decision: version %d, tier %q", p.DecisionVersion, p.Tier)
	}
}

func TestCheckBookNoSources(t *testing.T) {
	b := isbnBook()
	out, err := verify.CheckBookSources(t.Context(), verifytest.NewLibrary("library", b), library.BookRef{ID: b.Book.ID, UUID: b.Book.UUID}, t.TempDir(), nil, 1)
	if err != nil || out.State != verify.BookChecked {
		t.Fatalf("CheckBook = %+v, %v", out, err)
	}
	if strings.Contains(string(out.Evidence), `"lookups"`) {
		t.Fatalf("evidence without sources has lookups: %s", out.Evidence)
	}
	p, err := evidence.Open(out.Evidence, out.Seal)
	if err != nil || p.Lookups != nil {
		t.Fatalf("Lookups = %+v, %v", p.Lookups, err)
	}
}

func TestCheckBookLookupCancelled(t *testing.T) {
	b := isbnBook()
	ctx, cancel := context.WithCancel(t.Context())
	src := &fakeSources{during: cancel}
	out, err := verify.CheckBookSources(ctx, verifytest.NewLibrary("library", b), library.BookRef{ID: b.Book.ID, UUID: b.Book.UUID}, t.TempDir(), src, 1)
	if err == nil || out.Evidence != nil {
		t.Fatalf("cancelled during lookups: %+v, %v", out, err)
	}
}

func TestRunBeginsSources(t *testing.T) {
	store := verifytest.NewMemStore()
	lib := verifytest.NewLibrary("library", books(12, 4, "abcd")...)
	lib.SetExportDelay(10 * time.Millisecond)
	src := &fakeSources{}
	r1, stop1 := serveWith(t, store, lib, verify.Options{Sources: src})
	id := start(t, r1, verify.Scope{}).ID
	waitFor(t, "4 books checked", func() bool { return view(t, r1, id).Progress.Checked >= 4 })
	stop1()
	if got := src.begins(); !slices.Equal(got, []int64{id}) {
		t.Fatalf("BeginRun calls after start = %v, want [%d]", got, id)
	}
	r2, _ := serveWith(t, store, lib, verify.Options{Sources: src})
	if v := waitTerminal(t, r2, id); v.Run.Status != verify.RunCompleted {
		t.Fatalf("resumed run: %s", v.Run.Status)
	}
	if got := src.begins(); !slices.Equal(got, []int64{id, id}) {
		t.Fatalf("BeginRun calls after resume = %v, want [%d %d]", got, id, id)
	}
}

// TestRunRefreshBudget: an incremental run of the whole library refreshes a
// bounded number of expired cache answers per source; a full run and a
// one-book recheck refresh all of them (ADR-044).
func TestRunRefreshBudget(t *testing.T) {
	lib := incrementalLibrary(t)
	store := verifytest.NewMemStore()
	src := &fakeSources{}
	opts := verify.Options{Sources: src}
	for _, scope := range []verify.Scope{{}, {Incremental: true}, {BookID: 3, Incremental: true}} {
		if v := runOnce(t, store, lib, opts, scope); v.Run.Status != verify.RunCompleted {
			t.Fatalf("%+v: %s", scope, v.Run.Status)
		}
	}
	src.mu.Lock()
	got := slices.Clone(src.budgets)
	src.mu.Unlock()
	if want := []int{sources.RefreshAll, sources.RefreshIncremental, sources.RefreshAll}; !slices.Equal(got, want) {
		t.Fatalf("budgets = %v, want %v", got, want)
	}
}
