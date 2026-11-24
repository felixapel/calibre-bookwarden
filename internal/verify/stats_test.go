package verify_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"maps"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/sources"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
	"github.com/felixapel/calibre-bookwarden/internal/verify/verifytest"
)

// stepClock is a clock that moves only when a fake says so.
type stepClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *stepClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *stepClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// clockLib takes 1 s per record read and 2 s per format export.
type clockLib struct {
	*verifytest.FakeLibrary
	clk *stepClock
}

func (l clockLib) BookInLibrary(ctx context.Context, libraryID string, id int) (library.Book, error) {
	l.clk.advance(time.Second)
	return l.FakeLibrary.BookInLibrary(ctx, libraryID, id)
}

func (l clockLib) ExportFormatInLibrary(ctx context.Context, libraryID string, id int, format string, w io.Writer, want int64) (library.ExportResult, error) {
	l.clk.advance(2 * time.Second)
	return l.FakeLibrary.ExportFormatInLibrary(ctx, libraryID, id, format, w, want)
}

// clockSources takes 4 s per book's lookup and reports two lookups and a
// stale answer for the run, once: a take starts again from zero.
type clockSources struct {
	clk   *stepClock
	taken *atomic.Bool
}

func (clockSources) BeginRun(context.Context, int64, int) {}

func (s clockSources) Lookup(context.Context, int64, []sources.Seed) []evidence.Lookup {
	s.clk.advance(4 * time.Second)
	return nil
}

func (s clockSources) TakeLookupCounts() map[string]map[string]int {
	if s.taken.Swap(true) {
		return nil
	}
	return map[string]map[string]int{"dnb": {"ok": 2, "stale_hit": 1}}
}

// clockExtraction takes 8 s per excerpt and reports its calls once.
type clockExtraction struct {
	*fakeExtraction
	clk   *stepClock
	taken *atomic.Bool
}

func (e clockExtraction) Extract(ctx context.Context, ex evidence.Excerpt, body string) (*evidence.Extraction, []verify.RawOutput, error) {
	e.clk.advance(8 * time.Second)
	return e.fakeExtraction.Extract(ctx, ex, body)
}

func (e clockExtraction) TakeExtractorCounts() verify.ExtractorCounts {
	if e.taken.Swap(true) {
		return verify.ExtractorCounts{}
	}
	return verify.ExtractorCounts{Calls: 2, Cached: 1}
}

// clockHinter takes 16 s of tool time per PDF.
type clockHinter struct{ clk *stepClock }

func (h clockHinter) PDFHints(context.Context, string, int, bool, []string, *time.Duration) *evidence.ISBNHints {
	h.clk.advance(16 * time.Second)
	return nil
}

// logLines returns the JSON log lines in buf whose message is msg.
func logLines(t *testing.T, buf *syncBuffer, msg string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for line := range strings.SplitSeq(buf.String(), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		if m["msg"] == msg {
			out = append(out, m)
		}
	}
	return out
}

// slowStore takes 32 s to store each book's result.
type slowStore struct {
	*verifytest.MemStore
	clk *stepClock
}

func (s slowStore) FinishBook(ctx context.Context, runID int64, ref library.BookRef, out verify.Outcome) error {
	s.clk.advance(32 * time.Second)
	return s.MemStore.FinishBook(ctx, runID, ref, out)
}

// TestRunStatsSayWhereTheTimeWent: the seconds each stage took, summed over
// the books, and the run's lookup and extractor counts are stored with the
// run, added to what an earlier attempt stored, and logged once in the run
// summary.
func TestRunStatsSayWhereTheTimeWent(t *testing.T) {
	ctx := context.Background()
	clk := &stepClock{t: time.Date(2026, 11, 22, 3, 30, 0, 0, time.UTC)}
	lib := clockLib{verifytest.NewLibrary("library", fileBook(1, "EPUB", tidesEPUB), fileBook(2, "EPUB", tidesEPUB), fileBook(3, "PDF", paperPDF)), clk}
	lib.Edit(2, func(b *verifytest.FakeBook) { b.Book.UUID = "uuid-two" })
	store := slowStore{verifytest.NewMemStore(), clk}
	// An earlier attempt of the run stored what it had spent.
	var refs []library.BookRef
	for _, id := range []int{1, 2, 3} {
		b, err := lib.FakeLibrary.BookInLibrary(ctx, "library", id)
		if err != nil {
			t.Fatal(err)
		}
		refs = append(refs, library.BookRef{ID: id, UUID: b.UUID})
	}
	run, err := store.CreateRun(ctx, verify.Scope{}, "library", "dev", refs)
	if err != nil {
		t.Fatal(err)
	}
	earlier := verify.Stats{
		StageSeconds: map[string]float64{verify.StageStore: 100},
		Lookups:      map[string]map[string]int{"dnb": {"ok": 1}},
		Extractor:    verify.ExtractorCounts{Calls: 1},
	}
	if err := store.SaveRunStats(ctx, run.ID, earlier); err != nil {
		t.Fatal(err)
	}
	var logs syncBuffer
	// The run turns terminal before its summary is logged; Finished runs
	// after the summary, so the test waits for it before reading the log.
	finished := make(chan struct{})
	r, _ := serveWith(t, store, lib, verify.Options{
		Finished: func(context.Context, int64, verify.RunStatus) { close(finished) },
		Workers:  1, Now: clk.now, Logger: slog.New(slog.NewJSONHandler(&logs, nil)),
		Sources: clockSources{clk, new(atomic.Bool)}, Extraction: clockExtraction{&fakeExtraction{name: "a"}, clk, new(atomic.Bool)}, Hinter: clockHinter{clk},
	})
	if v := waitTerminal(t, r, run.ID); v.Run.Status != verify.RunCompleted || v.Progress.Checked != 3 {
		t.Fatalf("run ended %s with %+v (%s)", v.Run.Status, v.Progress, v.Run.Error)
	}

	want := verify.Stats{
		StageSeconds: map[string]float64{
			verify.StageCalibre:       6,  // 3 books × 2 reads × 1 s
			verify.StageExportInspect: 6,  // 3 formats × 2 s
			verify.StageOCR:           16, // the PDF
			verify.StageLookups:       12, // 3 books × 4 s
			verify.StageExtraction:    16, // 2 EPUB excerpts × 8 s
			verify.StageStore:         196,
		},
		Lookups:   map[string]map[string]int{"dnb": {"ok": 3, "stale_hit": 1}},
		Extractor: verify.ExtractorCounts{Calls: 3, Cached: 1},
	}
	got, err := store.Run(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Stats, want) {
		t.Errorf("stored stats = %+v\nwant %+v", got.Stats, want)
	}

	select {
	case <-finished:
	case <-time.After(10 * time.Second):
		t.Fatal("the run never finished")
	}
	summaries := logLines(t, &logs, "run summary")
	if len(summaries) != 1 {
		t.Fatalf("%d run summary lines, want 1", len(summaries))
	}
	s := summaries[0]
	wantFields := map[string]any{
		"run": float64(run.ID), "kind": "full", "status": "completed", "books": float64(3), "reinspected": float64(3),
		"calibre_seconds": float64(6), "export_inspect_seconds": float64(6), "ocr_seconds": float64(16),
		"lookups_seconds": float64(12), "extraction_seconds": float64(16), "store_seconds": float64(196),
		"lookups_dnb_ok": float64(3), "lookups_dnb_stale_hit": float64(1),
		"extractor_calls": float64(3), "extractor_cached": float64(1), "extractor_failed": float64(0),
	}
	for k, v := range wantFields {
		if s[k] != v {
			t.Errorf("summary %s = %v, want %v", k, s[k], v)
		}
	}
	if _, ok := s["duration_seconds"]; !ok {
		t.Error("the summary has no duration_seconds")
	}
	for k := range maps.Keys(s) {
		if k == "title" || k == "uuid" || k == "book" {
			t.Errorf("the summary carries book data: %s", k)
		}
	}
}

// TestStatsAreEmptyUntilSomethingIsCounted: a run without stats says so, and
// the JSON of empty stats is the column's default.
func TestStatsAreEmptyUntilSomethingIsCounted(t *testing.T) {
	if !(verify.Stats{}).Empty() {
		t.Error("zero Stats aren't empty")
	}
	zeros := verify.Stats{StageSeconds: map[string]float64{}, Lookups: map[string]map[string]int{"dnb": {"ok": 0}}}
	for _, st := range verify.Stages {
		zeros.StageSeconds[st] = 0
	}
	if !zeros.Empty() {
		t.Error("Stats of zeros, as a run cancelled before its first book stores, aren't empty")
	}
	if (verify.Stats{StageSeconds: map[string]float64{verify.StageStore: 0.2}}).Empty() {
		t.Error("Stats with time in one stage are empty")
	}
	if (verify.Stats{Extractor: verify.ExtractorCounts{Failed: 1}}).Empty() {
		t.Error("Stats with a failed extractor call are empty")
	}
	raw, err := json.Marshal(verify.Stats{})
	if err != nil || string(raw) != "{}" {
		t.Errorf("empty Stats marshal to %s, %v; want {}", raw, err)
	}
}
