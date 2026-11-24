package store

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

// finishedRun stores one package per out (book i+1) in a new run and ends it
// with status.
func finishedRun(t *testing.T, r *RunRepo, status verify.RunStatus, outs ...verify.Outcome) int64 {
	t.Helper()
	ctx := context.Background()
	books := refs(len(outs))
	run, err := r.CreateRun(ctx, verify.Scope{}, "library", "v1", books)
	if err != nil {
		t.Fatal(err)
	}
	for i, out := range outs {
		if err := r.FinishBook(ctx, run.ID, books[i], out); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.SetStatus(ctx, run.ID, status, ""); err != nil {
		t.Fatal(err)
	}
	return run.ID
}

func runIDs(t *testing.T, r *RunRepo) map[int64]bool {
	t.Helper()
	runs, err := r.ListRuns(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[int64]bool{}
	for _, run := range runs {
		ids[run.ID] = true
	}
	return ids
}

func TestFinishBookLinksItsExcerpts(t *testing.T) {
	ctx := context.Background()
	r, s := runRepo(t)
	out, exs := excerptOutcome(t, refs(1)[0], "first format", "second format")
	finishedRun(t, r, verify.RunCompleted, out)
	var n int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM evidence_excerpts WHERE sha256 = ANY($1)`, []string{exs[0].SHA256, exs[1].SHA256}).Scan(&n); err != nil || n != 2 {
		t.Fatalf("%d links, %v; want 2", n, err)
	}
}

func TestPruneDropsExcerptsNoPackageRefersTo(t *testing.T) {
	ctx := context.Background()
	r, s := runRepo(t)
	book := refs(1)[0]
	oldOnly, _ := excerptOutcome(t, book, "only the oldest run has this text", "shared by two runs")
	finishedRun(t, r, verify.RunCompleted, oldOnly)
	shared, _ := excerptOutcome(t, book, "shared by two runs")
	finishedRun(t, r, verify.RunCompleted, shared)
	finishedRun(t, r, verify.RunCompleted, sealed(t, book.ID, book.UUID))
	if n := excerptCount(t, s); n != 2 {
		t.Fatalf("%d excerpts before the prune, want 2", n)
	}
	// An excerpt nobody ever referred to goes too: a failed write can leave one.
	if _, err := s.Pool.Exec(ctx, `INSERT INTO excerpts (sha256, body, chars) VALUES ($1, 'stray', 5)`, strings.Repeat("ab", 32)); err != nil {
		t.Fatal(err)
	}

	res, err := r.Prune(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if res.Runs != 1 || res.Excerpts != 2 {
		t.Errorf("Prune() = %+v; want 1 run and 2 excerpts (the oldest run's own text and the stray one)", res)
	}
	if n := excerptCount(t, s); n != 1 {
		t.Errorf("%d excerpts left, want the one the second run refers to", n)
	}
	if again, err := r.Prune(ctx, 2); err != nil || again != (PruneResult{}) {
		t.Errorf("a second Prune() = %+v, %v; want nothing", again, err)
	}
}

func TestPruneNeverDropsAnExcerptARunInProgressRefersTo(t *testing.T) {
	ctx := context.Background()
	r, s := runRepo(t)
	book := refs(1)[0]
	finishedRun(t, r, verify.RunCompleted, sealed(t, book.ID, book.UUID))
	finishedRun(t, r, verify.RunCompleted, sealed(t, book.ID, book.UUID))
	active, err := r.CreateRun(ctx, verify.Scope{}, "library", "v1", []library.BookRef{book})
	if err != nil {
		t.Fatal(err)
	}
	out, _ := excerptOutcome(t, book, "written by the run in progress")
	if err := r.FinishBook(ctx, active.ID, book, out); err != nil {
		t.Fatal(err)
	}
	if res, err := r.Prune(ctx, 1); err != nil || res.Excerpts != 0 {
		t.Fatalf("Prune() = %+v, %v; the run in progress refers to the excerpt", res, err)
	}
	if n := excerptCount(t, s); n != 1 {
		t.Errorf("%d excerpts, want 1", n)
	}
}

func TestPruneKeepsTheNewestCompletedRunAndEverythingNewer(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name     string
		statuses []verify.RunStatus // oldest first
		keep     int
		wantLeft []int // indexes into statuses
	}{
		{"keep counts every run", []verify.RunStatus{"completed", "completed", "completed", "completed"}, 2, []int{2, 3}},
		{"a failed run on top keeps the completed one under it", []verify.RunStatus{"completed", "completed", "failed"}, 1, []int{1, 2}},
		{"runs newer than the newest completed stay", []verify.RunStatus{"completed", "completed", "failed", "cancelled"}, 1, []int{1, 2, 3}},
		{"older failures go", []verify.RunStatus{"failed", "cancelled", "completed", "completed"}, 1, []int{3}},
		{"no completed run: nothing goes", []verify.RunStatus{"failed", "failed", "cancelled"}, 1, []int{0, 1, 2}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, _ := runRepo(t)
			ids := make([]int64, len(tt.statuses))
			for i, st := range tt.statuses {
				ids[i] = finishedRun(t, r, st, sealed(t, 10, "uuid-1"))
			}
			if _, err := r.Prune(ctx, tt.keep); err != nil {
				t.Fatal(err)
			}
			left := runIDs(t, r)
			for i, id := range ids {
				want := false
				for _, w := range tt.wantLeft {
					want = want || w == i
				}
				if left[id] != want {
					t.Errorf("run %d (%s): kept=%v, want %v", i, tt.statuses[i], left[id], want)
				}
			}
		})
	}
}

func TestBackfillLinksExistingPackages(t *testing.T) {
	ctx := context.Background()
	r, s := runRepo(t)
	book := refs(1)[0]
	out, exs := excerptOutcome(t, book, "legacy first", "legacy second")
	finishedRun(t, r, verify.RunCompleted, out)
	// Put the schema back as it was before 0019 wrote links.
	if _, err := s.Pool.Exec(ctx, `DELETE FROM evidence_excerpts`); err != nil {
		t.Fatal(err)
	}
	raw, err := migrationFiles.ReadFile("migrations/0019_incremental_retention.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, string(raw)); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM evidence_excerpts WHERE sha256 = ANY($1)`, []string{exs[0].SHA256, exs[1].SHA256}).Scan(&n); err != nil || n != 2 {
		t.Fatalf("backfill linked %d excerpts, %v; want 2", n, err)
	}
	// And a prune right after the backfill removes nothing a package needs.
	finishedRun(t, r, verify.RunCompleted, sealed(t, book.ID, book.UUID))
	if res, err := r.Prune(ctx, 2); err != nil || res.Excerpts != 0 {
		t.Errorf("Prune() = %+v, %v", res, err)
	}
}

// A prune that runs while a book is being stored must not leave the book's
// package pointing at an excerpt the prune just deleted: here the old run is
// the only one that refers to the text, and the new package reuses it.
func TestPruneRacesFinishBookSafely(t *testing.T) {
	ctx := context.Background()
	r, s := runRepo(t)
	rep := NewReportRepo(s)
	book := refs(1)[0]
	for i := range 15 {
		out, exs := excerptOutcome(t, book, "raced text "+string(rune('a'+i)))
		finishedRun(t, r, verify.RunCompleted, out)
		finishedRun(t, r, verify.RunCompleted, sealed(t, book.ID, book.UUID))
		run, err := r.CreateRun(ctx, verify.Scope{Incremental: true}, "library", "v1", []library.BookRef{book})
		if err != nil {
			t.Fatal(err)
		}
		var (
			wg                  sync.WaitGroup
			finishErr, pruneErr error
		)
		wg.Go(func() { finishErr = r.FinishBook(ctx, run.ID, book, out) })
		wg.Go(func() { _, pruneErr = r.Prune(ctx, 1) })
		wg.Wait()
		if finishErr != nil || pruneErr != nil {
			t.Fatalf("round %d: finish %v, prune %v", i, finishErr, pruneErr)
		}
		if _, err := rep.Excerpt(ctx, exs[0].SHA256); err != nil {
			t.Fatalf("round %d: the new package's excerpt is gone: %v", i, err)
		}
		if err := r.SetStatus(ctx, run.ID, verify.RunCompleted, ""); err != nil {
			t.Fatal(err)
		}
	}
}
