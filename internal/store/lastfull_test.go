package store

import (
	"context"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

// LastFullRun is the newest full-library, non-incremental run, however it was
// started and however it ended.
func TestLastFullRun(t *testing.T) {
	ctx := context.Background()
	r, _ := runRepo(t)
	if _, ok, err := r.LastFullRun(ctx); ok || err != nil {
		t.Fatalf("empty database: %v, %v", ok, err)
	}
	start := func(scope verify.Scope, status verify.RunStatus) int64 {
		run, err := r.CreateRun(ctx, scope, "library", "v1", []library.BookRef{{ID: 10, UUID: "uuid-1"}})
		if err != nil {
			t.Fatal(err)
		}
		if err := r.SetStatus(ctx, run.ID, status, ""); err != nil {
			t.Fatal(err)
		}
		return run.ID
	}
	manualFull := start(verify.Scope{}, verify.RunFailed)
	start(verify.Scope{Incremental: true}, verify.RunCompleted)
	start(verify.Scope{BookID: 10}, verify.RunCompleted)
	got, ok, err := r.LastFullRun(ctx)
	if err != nil || !ok || got.ID != manualFull || got.StartedAt.IsZero() {
		t.Errorf("LastFullRun() = %+v, %v, %v; want run %d (incremental and one-book runs don't count)", got, ok, err, manualFull)
	}
	newer := start(verify.Scope{}, verify.RunCompleted)
	if got, _, _ := r.LastFullRun(ctx); got.ID != newer {
		t.Errorf("LastFullRun() = run %d, want the newer %d", got.ID, newer)
	}
}
