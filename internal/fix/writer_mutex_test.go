package fix

import (
	"context"
	"errors"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/store"
)

// grantAlways stands in for an advisory lock that Postgres lost (a restart):
// it grants every caller.
func grantAlways(context.Context) (func(), bool, error) { return func() {}, true, nil }

// TestOneWriterInsideTheProcess: even when the advisory lock grants
// everyone, an Undo or a recovery during a running Apply is refused, and the
// Apply's operation is left alone.
func TestOneWriterInsideTheProcess(t *testing.T) {
	ctx := context.Background()
	e := setup(t)
	run := e.checkBook(2)
	e.approve(run, 2, "title")
	e.svc.lockWriter = grantAlways
	e.cal.entered, e.cal.release = make(chan struct{}, 1), make(chan struct{})
	applied := make(chan error, 1)
	go func() {
		_, err := e.svc.Apply(ctx, "felix")
		applied <- err
	}()
	<-e.cal.entered // the Apply is writing
	ops := e.operationIDs()
	inFlight := ops[len(ops)-1]
	// The lock is checked before the operation is even read.
	if _, err := e.svc.Undo(ctx, inFlight, "felix"); !errors.Is(err, ErrBusy) {
		t.Errorf("Undo during an Apply = %v, want ErrBusy", err)
	}
	if err := e.svc.Recover(ctx); !errors.Is(err, ErrBusy) {
		t.Errorf("Recover during an Apply = %v, want ErrBusy", err)
	}
	if op, err := e.fixes.Operation(ctx, inFlight); err != nil || op.State != store.OpWriting {
		t.Errorf("the Apply's operation = %+v, %v; want writing, untouched", op, err)
	}
	close(e.cal.release)
	if err := <-applied; err != nil {
		t.Fatalf("Apply = %v", err)
	}
	if op, _ := e.fixes.Operation(ctx, inFlight); op.State != store.OpCompleted {
		t.Errorf("the Apply's operation ended %s, want completed", op.State)
	}
}
