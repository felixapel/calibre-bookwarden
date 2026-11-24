package store

import (
	"context"
	"testing"
)

func TestReviewing(t *testing.T) {
	ctx := context.Background()
	a, _, r := agentRepo(t)
	run := checkedRun(t, r, refs(1))
	if busy, err := a.Reviewing(ctx); err != nil || busy {
		t.Fatalf("no review yet: Reviewing() = %v, %v", busy, err)
	}
	rv, ok, err := a.Queue(ctx, run, ReviewManual, ModeShadow, "tester")
	if err != nil || !ok {
		t.Fatalf("Queue() = %v, %v", ok, err)
	}
	// Queued is not mid-operation: a review that waits for a judge to be
	// configured again must not hold the scheduler back forever.
	if busy, err := a.Reviewing(ctx); err != nil || busy {
		t.Errorf("queued review: Reviewing() = %v, %v; want false", busy, err)
	}
	if err := a.Start(ctx, rv.ID); err != nil {
		t.Fatal(err)
	}
	if busy, err := a.Reviewing(ctx); err != nil || !busy {
		t.Errorf("running review: Reviewing() = %v, %v; want true", busy, err)
	}
	if err := a.Finish(ctx, rv.ID, ReviewDone, ""); err != nil {
		t.Fatal(err)
	}
	if busy, err := a.Reviewing(ctx); err != nil || busy {
		t.Errorf("finished review: Reviewing() = %v, %v; want false", busy, err)
	}
}
