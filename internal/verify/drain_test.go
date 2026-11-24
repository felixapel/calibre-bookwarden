package verify_test

import (
	"context"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/verify"
	"github.com/felixapel/calibre-bookwarden/internal/verify/verifytest"
)

// serveUntil runs r.Serve on ctx and returns a channel that gets its result.
func serveUntil(ctx context.Context, r *verify.Runner) <-chan error {
	done := make(chan error, 1)
	go func() { done <- r.Serve(ctx) }()
	return done
}

func waitDone(t *testing.T, done <-chan error, what string) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve() = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("Serve did not return: %s", what)
	}
}

// TestDrainFinishesTheBookInHand: a stop (ADR-022) lets the book in progress
// finish and take no new one; the run stays active for the next start.
func TestDrainFinishesTheBookInHand(t *testing.T) {
	store := verifytest.NewMemStore()
	lib := verifytest.NewLibrary("library", books(6, 4, "abcd")...)
	lib.SetExportDelay(200 * time.Millisecond)
	r := verify.NewRunner(store, lib, verify.Options{ScratchDir: t.TempDir(), Workers: 1, Backoff: func(int) time.Duration { return time.Millisecond }})
	// The context is never cancelled here: only the drain signal stops it.
	done := serveUntil(context.Background(), r)
	id := start(t, r, verify.Scope{}).ID
	waitFor(t, "book 1 to be in hand", func() bool { return lib.ExportCalls(1, "EPUB") >= 1 })

	r.Drain()
	waitDone(t, done, "after Drain with a book in hand")

	states := store.BookStates(id)
	if states[1] != verify.BookChecked {
		t.Errorf("the book in hand ended %q, want checked", states[1])
	}
	for b := 2; b <= 6; b++ {
		if n := lib.ExportCalls(b, "EPUB"); n != 0 {
			t.Errorf("book %d was started after the drain (%d exports)", b, n)
		}
	}
	v := view(t, r, id)
	if v.Run.Status.Terminal() || v.Progress.Checked != 1 || v.Progress.Pending != 5 {
		t.Fatalf("run is %s with %+v, want active with 1 checked and 5 pending", v.Run.Status, v.Progress)
	}

	// The next start resumes the run and checks no book twice.
	r2, _ := serve(t, store, lib)
	if v := waitTerminal(t, r2, id); v.Run.Status != verify.RunCompleted || v.Progress.Checked != 6 {
		t.Fatalf("resumed run: %s with %+v", v.Run.Status, v.Progress)
	}
	if n := lib.ExportCalls(1, "EPUB"); n != 1 {
		t.Errorf("book 1 was exported %d times", n)
	}
}

// TestHardCancelAfterDrainLeavesTheBookPending: past the grace the context is
// cancelled; the book in hand stays pending, the run stays active, and the
// next start checks that book to its end, once.
func TestHardCancelAfterDrainLeavesTheBookPending(t *testing.T) {
	store := verifytest.NewMemStore()
	lib := verifytest.NewLibrary("library", books(4, 4, "abcd")...)
	lib.SetExportDelay(time.Hour) // the book in hand outlasts any grace
	r := verify.NewRunner(store, lib, verify.Options{ScratchDir: t.TempDir(), Workers: 1, Backoff: func(int) time.Duration { return time.Millisecond }})
	ctx, hard := context.WithCancel(context.Background())
	done := serveUntil(ctx, r)
	id := start(t, r, verify.Scope{}).ID
	waitFor(t, "book 1 in hand", func() bool { return lib.ExportCalls(1, "EPUB") >= 1 })

	r.Drain()
	select {
	case <-done:
		t.Fatal("Serve returned before the book in hand finished or the hard cancel")
	case <-time.After(100 * time.Millisecond):
	}
	hard() // the grace is over
	waitDone(t, done, "after the hard cancel")

	if st := store.BookStates(id)[1]; st != verify.BookPending {
		t.Errorf("the abandoned book is %q, want pending", st)
	}
	if v := view(t, r, id); v.Run.Status.Terminal() || v.Progress.Checked != 0 || v.Progress.Pending != 4 {
		t.Fatalf("run is %s with %+v, want active with all 4 pending", v.Run.Status, v.Progress)
	}

	lib.SetExportDelay(0)
	r2, _ := serve(t, store, lib)
	v := waitTerminal(t, r2, id)
	if v.Run.Status != verify.RunCompleted || v.Progress.Checked != 4 {
		t.Fatalf("resumed run: %s with %+v", v.Run.Status, v.Progress)
	}
	if states := store.BookStates(id); states[1] != verify.BookChecked {
		t.Errorf("book 1 after the resume is %q", states[1])
	}
	// One abandoned attempt, one that finished; the others ran once.
	if n := lib.ExportCalls(1, "EPUB"); n != 2 {
		t.Errorf("book 1 was exported %d times, want 2 (abandoned, then finished)", n)
	}
	for b := 2; b <= 4; b++ {
		if n := lib.ExportCalls(b, "EPUB"); n != 1 {
			t.Errorf("book %d was exported %d times, want 1", b, n)
		}
	}
}

// TestDrainWithManyWorkers: every book in hand finishes, none starts.
func TestDrainWithManyWorkers(t *testing.T) {
	store := verifytest.NewMemStore()
	lib := verifytest.NewLibrary("library", books(20, 4, "abcd")...)
	lib.SetExportDelay(150 * time.Millisecond)
	r := verify.NewRunner(store, lib, verify.Options{ScratchDir: t.TempDir(), Workers: 3, Backoff: func(int) time.Duration { return time.Millisecond }})
	done := serveUntil(context.Background(), r)
	id := start(t, r, verify.Scope{}).ID
	waitFor(t, "three books in hand", func() bool {
		n := 0
		for b := 1; b <= 20; b++ {
			n += lib.ExportCalls(b, "EPUB")
		}
		return n >= 3
	})
	r.Drain()
	waitDone(t, done, "after Drain with three books in hand")
	started := 0
	for b := 1; b <= 20; b++ {
		started += lib.ExportCalls(b, "EPUB")
	}
	checked := 0
	for _, st := range store.BookStates(id) {
		if st == verify.BookChecked {
			checked++
		}
	}
	if checked != started || checked < 3 || checked > 4 {
		t.Errorf("%d books started and %d checked; every book in hand must finish and few more may start", started, checked)
	}
}

func TestDrainWhenIdleOrNotStarted(t *testing.T) {
	lib := verifytest.NewLibrary("library", books(1, 4, "abcd")...)
	r := verify.NewRunner(verifytest.NewMemStore(), lib, verify.Options{ScratchDir: t.TempDir()})
	r.Drain()
	r.Drain() // twice is fine
	waitDone(t, serveUntil(context.Background(), r), "Serve after Drain")

	idle := verify.NewRunner(verifytest.NewMemStore(), lib, verify.Options{ScratchDir: t.TempDir()})
	done := serveUntil(context.Background(), idle)
	time.Sleep(20 * time.Millisecond)
	idle.Drain()
	waitDone(t, done, "an idle runner after Drain")
	if _, err := idle.Start(context.Background(), verify.Scope{}); err == nil {
		t.Error("Start after Drain created a run no runner will execute")
	}
}

// TestDrainWhileWaitingForCalibre: a run that waits for Calibre stops waiting.
func TestDrainWhileWaitingForCalibre(t *testing.T) {
	store := verifytest.NewMemStore()
	lib := verifytest.NewLibrary("library", books(10, 4, "abcd")...)
	lib.SetExportDelay(10 * time.Millisecond)
	r := verify.NewRunner(store, lib, verify.Options{ScratchDir: t.TempDir(), Backoff: func(int) time.Duration { return time.Millisecond }})
	done := serveUntil(context.Background(), r)
	id := start(t, r, verify.Scope{}).ID
	waitFor(t, "3 books checked", func() bool { return view(t, r, id).Progress.Checked >= 3 })
	lib.SetDown(true)
	waitFor(t, "the run to wait", func() bool { return view(t, r, id).Run.Status == verify.RunWaiting })
	r.Drain()
	waitDone(t, done, "a run waiting for Calibre after Drain")
	if st := view(t, r, id).Run.Status; st != verify.RunWaiting {
		t.Errorf("the run is %s, want it left waiting for the next start", st)
	}
}
