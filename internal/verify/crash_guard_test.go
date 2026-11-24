package verify_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
	"github.com/felixapel/calibre-bookwarden/internal/verify/verifytest"
)

// TestABookThatCrashedTwiceIsGivenUp: a book that was in hand when the
// process died twice is failed with process_crashed at the next start, and
// the run completes with the other books checked.
func TestABookThatCrashedTwiceIsGivenUp(t *testing.T) {
	ctx := context.Background()
	store := verifytest.NewMemStore()
	lib := verifytest.NewLibrary("library", books(4, 4, "abcd")...)
	var refs []library.BookRef
	for i := 1; i <= 4; i++ {
		refs = append(refs, library.BookRef{ID: i, UUID: fmt.Sprintf("uuid-%d", i)})
	}
	run, err := store.CreateRun(ctx, verify.Scope{}, "library", "dev", refs)
	if err != nil {
		t.Fatal(err)
	}
	// The first process died with book 2 in hand; the second counted that,
	// took book 2 again and died too.
	if err := store.TakeBook(ctx, run.ID, 2); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.RecoverCrashed(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.TakeBook(ctx, run.ID, 2); err != nil {
		t.Fatal(err)
	}
	var exports exportCounter
	lib.SetExportError(func(id int, _ string) error { exports.add(id); return nil })
	r, _ := serve(t, store, lib) // the third start
	v := waitTerminal(t, r, run.ID)
	if v.Run.Status != verify.RunCompleted || v.Progress != (verify.Progress{Total: 4, Checked: 3, Failed: 1}) {
		t.Fatalf("run ended %s with %+v (%s)", v.Run.Status, v.Progress, v.Run.Error)
	}
	if st, reason := store.BookStates(run.ID)[2], store.BookReasons(run.ID)[2]; st != verify.BookFailed || reason != "process_crashed" {
		t.Errorf("book 2 = %s/%q, want failed/process_crashed", st, reason)
	}
	if n := exports.get(2); n != 0 {
		t.Errorf("book 2 was inspected again %d times", n)
	}
}

// TestABookInHandIsMarked: a worker marks the book it checks, and the mark
// is gone once the book's result is stored.
func TestABookInHandIsMarked(t *testing.T) {
	store := verifytest.NewMemStore()
	lib := verifytest.NewLibrary("library", books(1, 4, "abcd")...)
	held := make(chan bool, 1)
	var runID int64
	lib.SetExportError(func(int, string) error {
		held <- store.InHand(runID, 1)
		return nil
	})
	r, _ := serve(t, store, lib)
	runID = start(t, r, verify.Scope{}).ID
	v := waitTerminal(t, r, runID)
	if v.Run.Status != verify.RunCompleted {
		t.Fatalf("run ended %s", v.Run.Status)
	}
	if !<-held {
		t.Error("the book wasn't in hand while it was checked")
	}
	if store.InHand(runID, 1) {
		t.Error("the book is still in hand after its result was stored")
	}
}

// TestAnOrderlyInterruptionIsNotACrash: a Calibre outage that puts the books
// in hand back leaves no crash for the next start to count.
func TestAnOrderlyInterruptionIsNotACrash(t *testing.T) {
	store := verifytest.NewMemStore()
	lib := verifytest.NewLibrary("library", books(6, 4, "abcd")...)
	lib.SetExportDelay(20 * time.Millisecond)
	r, stop := serve(t, store, lib)
	id := start(t, r, verify.Scope{}).ID
	waitFor(t, "a book checked", func() bool { return view(t, r, id).Progress.Checked >= 1 })
	lib.SetDown(true)
	waitFor(t, "the run to wait", func() bool { return view(t, r, id).Run.Status == verify.RunWaiting })
	stop()
	for i := 1; i <= 6; i++ {
		if store.InHand(id, i) {
			t.Errorf("book %d is still in hand after an orderly interruption", i)
		}
	}
	lib.SetDown(false)
	r2, _ := serve(t, store, lib) // a restart
	if v := waitTerminal(t, r2, id); v.Run.Status != verify.RunCompleted {
		t.Fatalf("run ended %s", v.Run.Status)
	}
	for i := 1; i <= 6; i++ {
		if n := store.Crashes(id, i); n != 0 {
			t.Errorf("book %d has %d crashes", i, n)
		}
	}
}

// TestCrashedBooksAreRetriedOneAtATime: the books in hand when the process
// died all count a crash, but only one of them can be the culprit, so each
// is checked alone; a second death then takes down only the culprit.
func TestCrashedBooksAreRetriedOneAtATime(t *testing.T) {
	ctx := context.Background()
	store := verifytest.NewMemStore()
	lib := verifytest.NewLibrary("library", books(6, 4, "abcd")...)
	var refs []library.BookRef
	for i := 1; i <= 6; i++ {
		refs = append(refs, library.BookRef{ID: i, UUID: fmt.Sprintf("uuid-%d", i)})
	}
	run, err := store.CreateRun(ctx, verify.Scope{}, "library", "dev", refs)
	if err != nil {
		t.Fatal(err)
	}
	// The first process died with four workers' books in hand.
	for i := 1; i <= 4; i++ {
		if err := store.TakeBook(ctx, run.ID, i); err != nil {
			t.Fatal(err)
		}
	}
	var (
		mu      sync.Mutex
		maxHeld int
	)
	lib.SetExportError(func(id int, _ string) error {
		if id > 4 {
			return nil
		}
		held := 0
		for i := 1; i <= 6; i++ {
			if store.InHand(run.ID, i) {
				held++
			}
		}
		mu.Lock()
		maxHeld = max(maxHeld, held)
		mu.Unlock()
		return nil
	})
	lib.SetExportDelay(10 * time.Millisecond)
	r, _ := serve(t, store, lib) // the second start, four workers
	if v := waitTerminal(t, r, run.ID); v.Run.Status != verify.RunCompleted || v.Progress != (verify.Progress{Total: 6, Checked: 6}) {
		t.Fatalf("run ended %s with %+v (%s)", v.Run.Status, v.Progress, v.Run.Error)
	}
	if maxHeld != 1 {
		t.Errorf("a book that crashed was checked with %d books in hand, want 1", maxHeld)
	}
}

// outageHook is a MemStore whose FinishBook and ReleaseBooks fail once each,
// as if the database went away in the middle of a book.
type outageHook struct {
	*verifytest.MemStore
	finished, released atomic.Bool
}

func (s *outageHook) FinishBook(ctx context.Context, runID int64, ref library.BookRef, out verify.Outcome) error {
	if ref.ID == 2 && s.finished.CompareAndSwap(false, true) {
		return fmt.Errorf("fake: %w", verify.ErrStoreUnavailable)
	}
	return s.MemStore.FinishBook(ctx, runID, ref, out)
}

func (s *outageHook) ReleaseBooks(ctx context.Context, runID int64, ids []int) error {
	if s.released.CompareAndSwap(false, true) {
		return fmt.Errorf("fake: %w", verify.ErrStoreUnavailable)
	}
	return s.MemStore.ReleaseBooks(ctx, runID, ids)
}

// TestAStoreOutageInsideOneProcessIsNotACrash: a book whose mark survived a
// store outage is not counted as a crash when the same process resumes the
// run.
func TestAStoreOutageInsideOneProcessIsNotACrash(t *testing.T) {
	store := &outageHook{MemStore: verifytest.NewMemStore()}
	lib := verifytest.NewLibrary("library", books(4, 4, "abcd")...)
	r, _ := serve(t, store, lib)
	id := start(t, r, verify.Scope{}).ID
	if v := waitTerminal(t, r, id); v.Run.Status != verify.RunCompleted || v.Progress != (verify.Progress{Total: 4, Checked: 4}) {
		t.Fatalf("run ended %s with %+v (%s)", v.Run.Status, v.Progress, v.Run.Error)
	}
	if !store.released.Load() {
		t.Fatal("the outage never reached ReleaseBooks")
	}
	if n := store.Crashes(id, 2); n != 0 {
		t.Errorf("book 2 has %d crashes after an outage in the same process", n)
	}
}
