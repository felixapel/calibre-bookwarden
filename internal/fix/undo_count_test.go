package fix

import (
	"context"
	"testing"
)

// TestUndoingCountsAnUndoInProgress: the stop's log names an undo that is
// still running, since Undo isn't a goroutine the stop tracks.
func TestUndoingCountsAnUndoInProgress(t *testing.T) {
	e := setup(t)
	orig := e.applied()
	e.cal.entered, e.cal.release = make(chan struct{}, 10), make(chan struct{})
	if n := e.svc.Undoing(); n != 0 {
		t.Fatalf("Undoing() = %d before any undo", n)
	}
	done := make(chan error, 1)
	go func() {
		_, err := e.svc.Undo(context.Background(), orig.ID, "admin")
		done <- err
	}()
	<-e.cal.entered
	if n := e.svc.Undoing(); n != 1 {
		t.Errorf("Undoing() = %d while an undo is in progress, want 1", n)
	}
	close(e.cal.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if n := e.svc.Undoing(); n != 0 {
		t.Errorf("Undoing() = %d after the undo, want 0", n)
	}
}
