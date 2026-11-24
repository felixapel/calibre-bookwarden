package fix

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/store"
)

var errAway = fmt.Errorf("fake: %w", library.ErrUnreachable)

// strandRestore applies the title of book 2 while Calibre goes away: the
// write lands but answers unreachable, and the restore can't even read the
// book. The operation is left restoring with its incident open.
func (e *env) strandRestore() int64 {
	e.t.Helper()
	ctx := context.Background()
	run := e.checkBook(2)
	e.approve(run, 2, "title")
	e.cal.errAfter = errAway
	e.cal.afterSet = func() {
		e.cal.mu.Lock()
		e.cal.failRead = errAway
		e.cal.mu.Unlock()
	}
	if _, err := e.svc.Apply(ctx, "felix"); err == nil {
		e.t.Fatal("Apply succeeded while Calibre was away")
	}
	ops := e.operationIDs()
	if op, err := e.fixes.Operation(ctx, ops[0]); err != nil || op.State != store.OpRestoring {
		e.t.Fatalf("operation = %+v, %v; want restoring", op, err)
	}
	return ops[0]
}

func (e *env) healCalibre() {
	e.cal.mu.Lock()
	e.cal.errAfter, e.cal.failRead, e.cal.afterSet = nil, nil, nil
	e.cal.mu.Unlock()
}

func (e *env) waitState(opID int64, want string) {
	e.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		op, err := e.fixes.Operation(context.Background(), opID)
		if err != nil {
			e.t.Fatal(err)
		}
		if op.State == want {
			return
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("operation %d is %s, want %s", opID, op.State, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestARestoreThatCannotReachCalibreIsRetried: a restore stranded by a
// Calibre outage is retried by the writer once Calibre is back, so writes
// don't stay paused until a restart.
func TestARestoreThatCannotReachCalibreIsRetried(t *testing.T) {
	ctx := context.Background()
	e := setup(t)
	before := e.cal.book(2).Title
	opID := e.strandRestore()
	inc, err := e.fixes.Incidents(ctx)
	if err != nil || len(inc) != 1 || inc[0].AcknowledgedAt != nil {
		t.Fatalf("incidents = %+v, %v; want one open", inc, err)
	}
	if err := e.svc.StartApply(ctx, "felix"); !errors.Is(err, ErrPaused) {
		t.Fatalf("StartApply = %v, want ErrPaused", err)
	}
	if err := e.svc.Acknowledge(ctx, inc[0].ID, "felix"); !errors.Is(err, ErrNoIncident) {
		t.Fatalf("Acknowledge while restoring = %v, want ErrNoIncident", err)
	}
	e.svc.recoverEvery = 10 * time.Millisecond
	e.serve()
	time.Sleep(50 * time.Millisecond) // ticks while Calibre is still away
	e.waitState(opID, store.OpRestoring)
	e.healCalibre()
	e.waitState(opID, store.OpRestored)
	if got := e.cal.book(2).Title; got != before {
		t.Errorf("title = %q, want the old %q back", got, before)
	}
	if err := e.svc.Acknowledge(ctx, inc[0].ID, "felix"); err != nil {
		t.Errorf("Acknowledge after the restore = %v", err)
	}
}

// TestRecoveryWaitsForWritesOn: with writes off the stranded restore stays
// put through the ticks; once writes are on it is restored.
func TestRecoveryWaitsForWritesOn(t *testing.T) {
	ctx := context.Background()
	e := setup(t)
	opID := e.strandRestore()
	if err := e.fixes.SetWritesEnabled(ctx, false, "felix"); err != nil {
		t.Fatal(err)
	}
	e.healCalibre()
	e.svc.recoverEvery = 10 * time.Millisecond
	e.serve()
	sets := e.cal.setCount()
	time.Sleep(60 * time.Millisecond)
	e.waitState(opID, store.OpRestoring)
	if n := e.cal.setCount(); n != sets {
		t.Fatalf("set-fields calls with writes off: %d, want %d", n, sets)
	}
	if err := e.fixes.SetWritesEnabled(ctx, true, "felix"); err != nil {
		t.Fatal(err)
	}
	e.waitState(opID, store.OpRestored)
}

// TestNoRetryWithoutUnfinishedOperations: the ticker makes no Calibre call
// when nothing is unfinished.
func TestNoRetryWithoutUnfinishedOperations(t *testing.T) {
	e := setup(t)
	e.svc.recoverEvery = 5 * time.Millisecond
	e.serve()
	time.Sleep(20 * time.Millisecond) // the start-up recovery is done
	e.cal.mu.Lock()
	reads, sets := e.cal.reads, e.cal.sets
	e.cal.mu.Unlock()
	time.Sleep(60 * time.Millisecond)
	e.cal.mu.Lock()
	defer e.cal.mu.Unlock()
	if e.cal.reads != reads || e.cal.sets != sets {
		t.Errorf("Calibre calls during idle ticks: reads %d→%d, sets %d→%d", reads, e.cal.reads, sets, e.cal.sets)
	}
}

// TestRetryDoesNotRunDuringAnApply: a manual retry is refused while an apply
// runs (the ticker shares the writer's goroutine, so it can't run then
// either).
func TestRetryDoesNotRunDuringAnApply(t *testing.T) {
	ctx := context.Background()
	e := setup(t)
	run := e.checkBook(2)
	e.approve(run, 2, "title")
	e.cal.entered, e.cal.release = make(chan struct{}, 1), make(chan struct{})
	e.svc.recoverEvery = 5 * time.Millisecond
	e.serve()
	if err := e.svc.StartApply(ctx, "felix"); err != nil {
		t.Fatal(err)
	}
	<-e.cal.entered
	if err := e.svc.RetryRecovery(ctx); !errors.Is(err, ErrBusy) {
		t.Errorf("RetryRecovery during an apply = %v, want ErrBusy", err)
	}
	close(e.cal.release)
	if st := e.waitApplied(); st.Err != "" {
		t.Fatalf("apply = %+v", st)
	}
}

// TestManualRetry: "Retry now" queues an immediate retry on the writer's
// goroutine, refuses with writes off, and never waits for Calibre.
func TestManualRetry(t *testing.T) {
	ctx := context.Background()
	e := setup(t)
	opID := e.strandRestore()
	e.svc.recoverEvery = time.Hour // only the manual retry can restore it
	e.serve()
	if err := e.svc.RetryRecovery(ctx); err != nil {
		t.Fatalf("RetryRecovery while Calibre is away = %v, want queued", err)
	}
	time.Sleep(30 * time.Millisecond)
	e.waitState(opID, store.OpRestoring)
	if err := e.fixes.SetWritesEnabled(ctx, false, "felix"); err != nil {
		t.Fatal(err)
	}
	e.healCalibre()
	if err := e.svc.RetryRecovery(ctx); !errors.Is(err, ErrWritesOff) {
		t.Fatalf("RetryRecovery with writes off = %v, want ErrWritesOff", err)
	}
	if err := e.fixes.SetWritesEnabled(ctx, true, "felix"); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RetryRecovery(ctx); err != nil {
		t.Fatalf("RetryRecovery = %v", err)
	}
	e.waitState(opID, store.OpRestored)
}

// TestFailingRetriesDontGrowTheLedger: while Calibre answers reads but
// refuses every write, each retry finds the book as the last one did, and
// the ledger gains no event per tick.
func TestFailingRetriesDontGrowTheLedger(t *testing.T) {
	ctx := context.Background()
	e := setup(t)
	run := e.checkBook(2)
	e.approve(run, 2, "title")
	e.cal.errAfter = errAway // the write lands, the answer is lost
	e.cal.failSet, e.cal.failSkip = errAway, 1
	if _, err := e.svc.Apply(ctx, "felix"); err == nil {
		t.Fatal("Apply succeeded")
	}
	opID := e.operationIDs()[0]
	e.waitState(opID, store.OpRestoring)
	e.svc.recoverEvery = 5 * time.Millisecond
	e.serve()
	waitSets := func(n int) {
		deadline := time.Now().Add(5 * time.Second)
		for e.cal.setCount() < n {
			if time.Now().After(deadline) {
				t.Fatalf("only %d set-fields calls", e.cal.setCount())
			}
			time.Sleep(time.Millisecond)
		}
	}
	waitSets(e.cal.setCount() + 2) // two retries
	first := len(e.events(opID))
	waitSets(e.cal.setCount() + 5) // five more
	if n := len(e.events(opID)); n != first {
		t.Fatalf("events grew from %d to %d over five failed retries", first, n)
	}
	if inc, _ := e.fixes.Incidents(ctx); len(inc) != 1 {
		t.Fatalf("incidents = %d, want 1", len(inc))
	}
	e.cal.mu.Lock()
	e.cal.failSet, e.cal.errAfter = nil, nil
	e.cal.mu.Unlock()
	e.waitState(opID, store.OpRestored)
}
