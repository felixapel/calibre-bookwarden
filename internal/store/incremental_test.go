package store

import (
	"context"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

func mustRun(t *testing.T, r *RunRepo, id int64) verify.Run {
	t.Helper()
	run, err := r.Run(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func TestRunKeepsIncrementalAndScheduledSlot(t *testing.T) {
	ctx := context.Background()
	r, _ := runRepo(t)
	slot := time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC)
	run, err := r.CreateRun(ctx, verify.Scope{Incremental: true, ScheduledFor: slot}, "library", "v1", refs(1))
	if err != nil {
		t.Fatal(err)
	}
	for _, got := range []verify.Run{run, mustRun(t, r, run.ID)} {
		if !got.Scope.Incremental || !got.Scope.ScheduledFor.Equal(slot) {
			t.Errorf("scope = %+v, want incremental for slot %v", got.Scope, slot)
		}
	}
	if err := r.SetStatus(ctx, run.ID, verify.RunCompleted, ""); err != nil {
		t.Fatal(err)
	}
	manual, err := r.CreateRun(ctx, verify.Scope{}, "library", "v1", refs(1))
	if err != nil {
		t.Fatal(err)
	}
	if manual.Scope.Incremental || !manual.Scope.ScheduledFor.IsZero() {
		t.Errorf("a manual full run reads back as %+v", manual.Scope)
	}
}

func TestFinishBookRecordsReuseAndInspectKey(t *testing.T) {
	ctx := context.Background()
	r, s := runRepo(t)
	books := refs(2)
	run, err := r.CreateRun(ctx, verify.Scope{Incremental: true}, "library", "v1", books)
	if err != nil {
		t.Fatal(err)
	}
	reused := sealed(t, books[0].ID, books[0].UUID)
	reused.Reused, reused.InspectKey = true, "key-1"
	fresh := sealed(t, books[1].ID, books[1].UUID)
	fresh.InspectKey = "key-1"
	for i, out := range []verify.Outcome{reused, fresh} {
		if err := r.FinishBook(ctx, run.ID, books[i], out); err != nil {
			t.Fatal(err)
		}
	}
	p, err := r.Progress(ctx, run.ID)
	if err != nil || p.Checked != 2 || p.Reused != 1 {
		t.Fatalf("Progress() = %+v, %v; want 2 checked, 1 reused", p, err)
	}
	var key string
	if err := s.Pool.QueryRow(ctx, `SELECT inspect_version FROM evidence WHERE run_id=$1 AND book_id=$2`, run.ID, books[0].ID).Scan(&key); err != nil || key != "key-1" {
		t.Errorf("inspect_version = %q, %v", key, err)
	}
}

func TestPriorEvidence(t *testing.T) {
	ctx := context.Background()
	ref := refs(1)[0]
	// finish stores out as ref's package in a new completed run.
	finish := func(t *testing.T, r *RunRepo, libraryID, key string, out verify.Outcome) {
		t.Helper()
		run, err := r.CreateRun(ctx, verify.Scope{}, libraryID, "v1", []library.BookRef{ref})
		if err != nil {
			t.Fatal(err)
		}
		out.InspectKey = key
		if err := r.FinishBook(ctx, run.ID, ref, out); err != nil {
			t.Fatal(err)
		}
		if err := r.SetStatus(ctx, run.ID, verify.RunCompleted, ""); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("newest package wins, with its excerpts", func(t *testing.T) {
		r, _ := runRepo(t)
		old, _ := excerptOutcome(t, ref, "old front matter")
		finish(t, r, "library", "k1", old)
		newest, exs := excerptOutcome(t, ref, "newest front matter")
		finish(t, r, "library", "k2", newest)
		prior, ok, err := r.PriorEvidence(ctx, "library", ref)
		if err != nil || !ok {
			t.Fatalf("PriorEvidence() = %v, %v", ok, err)
		}
		if prior.InspectKey != "k2" || prior.Package.Formats[0].Excerpt.SHA256 != exs[0].SHA256 || prior.Excerpts[exs[0].SHA256] != "newest front matter" {
			t.Errorf("prior = key %q, formats %+v, bodies %d", prior.InspectKey, prior.Package.Formats, len(prior.Excerpts))
		}
	})
	t.Run("a book never checked", func(t *testing.T) {
		r, _ := runRepo(t)
		if _, ok, err := r.PriorEvidence(ctx, "library", ref); ok || err != nil {
			t.Errorf("PriorEvidence() = %v, %v; want none", ok, err)
		}
	})
	t.Run("another library or another uuid", func(t *testing.T) {
		r, _ := runRepo(t)
		finish(t, r, "library", "k", sealed(t, ref.ID, ref.UUID))
		if _, ok, _ := r.PriorEvidence(ctx, "elsewhere", ref); ok {
			t.Error("a package of another library was offered")
		}
		if _, ok, _ := r.PriorEvidence(ctx, "library", library.BookRef{ID: ref.ID, UUID: "replaced"}); ok {
			t.Error("a package of another book with the same id was offered")
		}
	})
	t.Run("a missing or corrupt excerpt falls back", func(t *testing.T) {
		for _, damage := range []string{`DELETE FROM excerpts`, `UPDATE excerpts SET body = body || 'x'`} {
			r, s := runRepo(t)
			out, _ := excerptOutcome(t, ref, "front matter")
			finish(t, r, "library", "k", out)
			if _, ok, err := r.PriorEvidence(ctx, "library", ref); !ok || err != nil {
				t.Fatalf("undamaged: %v, %v", ok, err)
			}
			if _, err := s.Pool.Exec(ctx, damage); err != nil {
				t.Fatal(err)
			}
			if _, ok, err := r.PriorEvidence(ctx, "library", ref); ok || err != nil {
				t.Errorf("%s: PriorEvidence() = %v, %v; want none and no error", damage, ok, err)
			}
		}
	})
	t.Run("a package whose seal no longer holds", func(t *testing.T) {
		r, s := runRepo(t)
		finish(t, r, "library", "k", sealed(t, ref.ID, ref.UUID))
		if _, err := s.Pool.Exec(ctx, `UPDATE evidence SET package = jsonb_set(package, '{calibre_record,title}', '"Edited"')`); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := r.PriorEvidence(ctx, "library", ref); ok || err != nil {
			t.Errorf("PriorEvidence() = %v, %v; want none and no error", ok, err)
		}
	})
}

func TestLastScheduled(t *testing.T) {
	ctx := context.Background()
	r, _ := runRepo(t)
	if _, ok, err := r.LastScheduled(ctx, false); ok || err != nil {
		t.Fatalf("empty database: %v, %v", ok, err)
	}
	slot := func(day int) time.Time { return time.Date(2026, 11, day, 3, 0, 0, 0, time.UTC) }
	start := func(scope verify.Scope) {
		run, err := r.CreateRun(ctx, scope, "library", "v1", refs(1))
		if err != nil {
			t.Fatal(err)
		}
		if err := r.SetStatus(ctx, run.ID, verify.RunCompleted, ""); err != nil {
			t.Fatal(err)
		}
	}
	start(verify.Scope{ScheduledFor: slot(1)})                    // full
	start(verify.Scope{Incremental: true, ScheduledFor: slot(2)}) // incremental
	start(verify.Scope{})                                         // by hand: never counts
	latest, ok, err := r.LastScheduled(ctx, false)
	if err != nil || !ok || !latest.Scope.ScheduledFor.Equal(slot(2)) || !latest.Scope.Incremental {
		t.Errorf("LastScheduled(any) = %+v, %v, %v; want the incremental of day 2", latest.Scope, ok, err)
	}
	full, ok, err := r.LastScheduled(ctx, true)
	if err != nil || !ok || !full.Scope.ScheduledFor.Equal(slot(1)) || full.Scope.Incremental {
		t.Errorf("LastScheduled(full) = %+v, %v, %v; want the full of day 1", full.Scope, ok, err)
	}
}

func TestMigration0019IsRepeatable(t *testing.T) {
	_, s := runRepo(t)
	raw, err := migrationFiles.ReadFile("migrations/0019_incremental_retention.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(context.Background(), string(raw)); err != nil {
		t.Fatalf("a second run of 0019: %v", err)
	}
}
