package store

import (
	"context"
	"errors"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

// pruneAll deletes every ended run that nothing keeps. Only tests may wipe
// like this; the retention policy cannot (Prune(0) keeps everything).
func (r *RunRepo) pruneAll(ctx context.Context) (PruneResult, error) {
	return r.prune(ctx, 0, true)
}

func TestPinnedRunSurvivesPrune(t *testing.T) {
	ctx := context.Background()
	r, _ := runRepo(t)
	baseline := finishedRun(t, r, verify.RunCompleted, sealed(t, 10, "uuid-1"))
	other := finishedRun(t, r, verify.RunCompleted, sealed(t, 10, "uuid-1"))
	newest := finishedRun(t, r, verify.RunCompleted, sealed(t, 10, "uuid-1"))
	if err := r.SetPinned(ctx, baseline, true); err != nil {
		t.Fatal(err)
	}
	if !mustRun(t, r, baseline).Pinned || mustRun(t, r, other).Pinned {
		t.Fatal("the pin did not stick to the right run")
	}
	if res, err := r.Prune(ctx, 1); err != nil || res.Runs != 1 {
		t.Fatalf("Prune() = %+v, %v; want only the unpinned old run gone", res, err)
	}
	if left := runIDs(t, r); !left[baseline] || left[other] || !left[newest] {
		t.Errorf("runs left = %v; want the pinned baseline and the newest", left)
	}
	// A wipe leaves it too.
	if _, err := r.pruneAll(ctx); err != nil {
		t.Fatal(err)
	}
	if !runIDs(t, r)[baseline] {
		t.Error("a wipe removed the pinned run")
	}
	// Unpinned, it goes at the next prune.
	finishedRun(t, r, verify.RunCompleted, sealed(t, 10, "uuid-1"))
	if err := r.SetPinned(ctx, baseline, false); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Prune(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if runIDs(t, r)[baseline] {
		t.Error("an unpinned run was kept past retention")
	}
}

func TestSetPinnedUnknownRun(t *testing.T) {
	r, _ := runRepo(t)
	if err := r.SetPinned(context.Background(), 999, true); !errors.Is(err, verify.ErrNoSuchRun) {
		t.Errorf("SetPinned(999) = %v, want ErrNoSuchRun", err)
	}
}

// Runs of one book never count as "the newest completed run": ten of them must
// not push the last full run out.
func TestOneBookRunsDoNotPushOutTheLastFullRun(t *testing.T) {
	ctx := context.Background()
	r, _ := runRepo(t)
	oldFull := finishedRun(t, r, verify.RunCompleted, sealed(t, 10, "uuid-1"))
	full := finishedRun(t, r, verify.RunCompleted, sealed(t, 10, "uuid-1"))
	book := library.BookRef{ID: 10, UUID: "uuid-1"}
	for range 12 {
		run, err := r.CreateRun(ctx, verify.Scope{BookID: book.ID}, "library", "v1", []library.BookRef{book})
		if err != nil {
			t.Fatal(err)
		}
		if err := r.FinishBook(ctx, run.ID, book, sealed(t, book.ID, book.UUID)); err != nil {
			t.Fatal(err)
		}
		if err := r.SetStatus(ctx, run.ID, verify.RunCompleted, ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.Prune(ctx, 10); err != nil {
		t.Fatal(err)
	}
	left := runIDs(t, r)
	if !left[full] || left[oldFull] {
		t.Errorf("full runs left: last %v, older %v; want the last kept and the older gone", left[full], left[oldFull])
	}
}

func TestPruneZeroKeepsEverything(t *testing.T) {
	ctx := context.Background()
	r, _ := runRepo(t)
	for range 3 {
		finishedRun(t, r, verify.RunCompleted, sealed(t, 10, "uuid-1"))
	}
	if res, err := r.Prune(ctx, 0); err != nil || res != (PruneResult{}) {
		t.Errorf("Prune(0) = %+v, %v; 0 keeps every run, as BOOKWARDEN_RETAIN_RUNS=0 does", res, err)
	}
	if n := len(runIDs(t, r)); n != 3 {
		t.Errorf("%d runs left, want 3", n)
	}
	if _, err := r.Prune(ctx, -1); err == nil {
		t.Error("Prune(-1) was accepted")
	}
}
