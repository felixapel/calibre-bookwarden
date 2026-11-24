package verify_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
	"github.com/felixapel/calibre-bookwarden/internal/verify/verifytest"
)

// finishHook is a MemStore whose FinishBook can be replaced per call.
type finishHook struct {
	*verifytest.MemStore
	finish func(ctx context.Context, ref library.BookRef, out verify.Outcome) error
}

func (s *finishHook) FinishBook(ctx context.Context, runID int64, ref library.BookRef, out verify.Outcome) error {
	if err := s.finish(ctx, ref, out); err != nil {
		return err
	}
	return s.MemStore.FinishBook(ctx, runID, ref, out)
}

// exportCounter counts each book's exports.
type exportCounter struct {
	mu sync.Mutex
	n  map[int]int
}

func (c *exportCounter) add(id int) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.n == nil {
		c.n = map[int]int{}
	}
	c.n[id]++
	return c.n[id]
}

func (c *exportCounter) get(id int) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n[id]
}

// TestAStoreErrorFailsTheBookNotTheRun: a result that can't be stored for a
// reason other than an outage fails that book alone, once, and the run ends.
func TestAStoreErrorFailsTheBookNotTheRun(t *testing.T) {
	store := &finishHook{MemStore: verifytest.NewMemStore()}
	store.finish = func(_ context.Context, ref library.BookRef, out verify.Outcome) error {
		if ref.ID == 2 && out.Reason != "store_error" {
			return errors.New("fake: evidence refused")
		}
		return nil
	}
	lib := verifytest.NewLibrary("library", books(5, 4, "abcd")...)
	var exports exportCounter
	lib.SetExportError(func(id int, _ string) error { exports.add(id); return nil })
	r, _ := serve(t, store, lib)
	id := start(t, r, verify.Scope{}).ID
	v := waitTerminal(t, r, id)
	if v.Run.Status != verify.RunCompleted || v.Progress != (verify.Progress{Total: 5, Checked: 4, Failed: 1}) {
		t.Fatalf("run ended %s with %+v (%s)", v.Run.Status, v.Progress, v.Run.Error)
	}
	if st, reason := store.BookStates(id)[2], store.BookReasons(id)[2]; st != verify.BookFailed || reason != "store_error" {
		t.Errorf("book 2 = %s/%q, want failed/store_error", st, reason)
	}
	if n := exports.get(2); n != 1 {
		t.Errorf("book 2 was exported %d times, want 1", n)
	}
}

// TestAStoreOutageStillInterruptsTheRun: an outage interrupts the run, which
// resumes and checks the book again.
func TestAStoreOutageStillInterruptsTheRun(t *testing.T) {
	store := &finishHook{MemStore: verifytest.NewMemStore()}
	var failed atomic.Bool
	store.finish = func(_ context.Context, ref library.BookRef, _ verify.Outcome) error {
		if ref.ID == 2 && failed.CompareAndSwap(false, true) {
			return fmt.Errorf("fake: %w", verify.ErrStoreUnavailable)
		}
		return nil
	}
	lib := verifytest.NewLibrary("library", books(5, 4, "abcd")...)
	var exports exportCounter
	lib.SetExportError(func(id int, _ string) error { exports.add(id); return nil })
	r, _ := serve(t, store, lib)
	id := start(t, r, verify.Scope{}).ID
	v := waitTerminal(t, r, id)
	if v.Run.Status != verify.RunCompleted || v.Progress != (verify.Progress{Total: 5, Checked: 5}) {
		t.Fatalf("run ended %s with %+v (%s)", v.Run.Status, v.Progress, v.Run.Error)
	}
	if n := exports.get(2); n != 2 {
		t.Errorf("book 2 was exported %d times, want 2 (interrupted, then resumed)", n)
	}
}

// TestAFinishedBookSurvivesASiblingOutage: a book that finished while another
// worker lost Calibre is stored, not thrown away and checked again.
func TestAFinishedBookSurvivesASiblingOutage(t *testing.T) {
	store := &finishHook{MemStore: verifytest.NewMemStore()}
	atFinish := make(chan struct{})
	lost := make(chan struct{})
	var once sync.Once
	store.finish = func(ctx context.Context, ref library.BookRef, _ verify.Outcome) error {
		if ref.ID != 1 {
			return nil
		}
		once.Do(func() {
			close(atFinish)
			<-lost
			// Give the sibling's failure time to cancel the batch; a store
			// call honours its context, as a database call would.
			select {
			case <-ctx.Done():
			case <-time.After(200 * time.Millisecond):
			}
		})
		return ctx.Err()
	}
	lib := verifytest.NewLibrary("library", books(2, 4, "abcd")...)
	var exports exportCounter
	lib.SetExportError(func(id int, _ string) error {
		if exports.add(id) == 1 && id == 2 {
			<-atFinish
			close(lost)
			return fmt.Errorf("fake: export: %w", library.ErrUnreachable)
		}
		return nil
	})
	r, _ := serveWith(t, store, lib, verify.Options{Workers: 2})
	id := start(t, r, verify.Scope{}).ID
	v := waitTerminal(t, r, id)
	if v.Run.Status != verify.RunCompleted || v.Progress != (verify.Progress{Total: 2, Checked: 2}) {
		t.Fatalf("run ended %s with %+v (%s)", v.Run.Status, v.Progress, v.Run.Error)
	}
	if n := exports.get(1); n != 1 {
		t.Errorf("book 1 was exported %d times, want 1: its finished result was thrown away", n)
	}
}

// failingPending is a MemStore whose PendingBooks fails a number of times.
type failingPending struct {
	*verifytest.MemStore
	fails atomic.Int32
}

func (s *failingPending) PendingBooks(ctx context.Context, runID int64) ([]library.BookRef, error) {
	if s.fails.Add(-1) >= 0 {
		return nil, errors.New("fake: pending books")
	}
	return s.MemStore.PendingBooks(ctx, runID)
}

// TestServeBacksOff: each failed attempt at the same run waits longer.
func TestServeBacksOff(t *testing.T) {
	store := &failingPending{MemStore: verifytest.NewMemStore()}
	var mu sync.Mutex
	var waits []int
	backoff := func(n int) time.Duration {
		mu.Lock()
		waits = append(waits, n)
		mu.Unlock()
		return time.Millisecond
	}
	lib := verifytest.NewLibrary("library", books(3, 4, "abcd")...)
	r, _ := serveWith(t, store, lib, verify.Options{Backoff: backoff})
	store.fails.Store(4)
	id := start(t, r, verify.Scope{}).ID
	if v := waitTerminal(t, r, id); v.Run.Status != verify.RunCompleted {
		t.Fatalf("run ended %s (%s)", v.Run.Status, v.Run.Error)
	}
	mu.Lock()
	defer mu.Unlock()
	if want := []int{0, 1, 2, 3}; fmt.Sprint(waits) != fmt.Sprint(want) {
		t.Errorf("waits = %v, want %v", waits, want)
	}
}

// TestAFailedStoreErrorWriteInterruptsTheRun: when even the store_error
// write fails, the batch stops and the run resumes later, as for an outage.
func TestAFailedStoreErrorWriteInterruptsTheRun(t *testing.T) {
	store := &finishHook{MemStore: verifytest.NewMemStore()}
	var calls atomic.Int32
	store.finish = func(_ context.Context, ref library.BookRef, _ verify.Outcome) error {
		if ref.ID == 2 && calls.Add(1) <= 2 { // the result and the store_error write
			return errors.New("fake: refused")
		}
		return nil
	}
	lib := verifytest.NewLibrary("library", books(3, 4, "abcd")...)
	var exports exportCounter
	lib.SetExportError(func(id int, _ string) error { exports.add(id); return nil })
	r, _ := serve(t, store, lib)
	id := start(t, r, verify.Scope{}).ID
	v := waitTerminal(t, r, id)
	if v.Run.Status != verify.RunCompleted || v.Progress != (verify.Progress{Total: 3, Checked: 3}) {
		t.Fatalf("run ended %s with %+v (%s)", v.Run.Status, v.Progress, v.Run.Error)
	}
	if n := exports.get(2); n != 2 {
		t.Errorf("book 2 was exported %d times, want 2 (interrupted, then resumed)", n)
	}
}

// failingActive is a MemStore whose ActiveRun fails a number of times.
type failingActive struct {
	*verifytest.MemStore
	fails atomic.Int32
}

func (s *failingActive) ActiveRun(ctx context.Context) (verify.Run, bool, error) {
	if s.fails.Add(-1) >= 0 {
		return verify.Run{}, false, errors.New("fake: active run")
	}
	return s.MemStore.ActiveRun(ctx)
}

// TestServeBacksOffReadingTheActiveRun: failed reads of the active run wait
// longer each time; once a read succeeds, runs go on as usual.
func TestServeBacksOffReadingTheActiveRun(t *testing.T) {
	store := &failingActive{MemStore: verifytest.NewMemStore()}
	store.fails.Store(3) // Serve's first three reads
	var mu sync.Mutex
	var waits []int
	backoff := func(n int) time.Duration {
		mu.Lock()
		waits = append(waits, n)
		mu.Unlock()
		return time.Millisecond
	}
	lib := verifytest.NewLibrary("library", books(1, 4, "abcd")...)
	r, _ := serveWith(t, store, lib, verify.Options{Backoff: backoff})
	waitFor(t, "the reads to succeed", func() bool { return store.fails.Load() < 0 })
	mu.Lock()
	got := fmt.Sprint(waits)
	mu.Unlock()
	if want := fmt.Sprint([]int{0, 1, 2}); got != want {
		t.Fatalf("waits = %v, want %v", got, want)
	}
	id := start(t, r, verify.Scope{}).ID
	if v := waitTerminal(t, r, id); v.Run.Status != verify.RunCompleted {
		t.Fatalf("run ended %s (%s)", v.Run.Status, v.Run.Error)
	}
}
