package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/report"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

// TestRunHistorySurvivesPrune: the ledger keeps every run's row and seal
// counts after retention has deleted the runs.
func TestRunHistorySurvivesPrune(t *testing.T) {
	ctx := context.Background()
	r, s := runRepo(t)
	reports := NewReportRepo(s)
	for range 15 {
		run, err := r.CreateRun(ctx, verify.Scope{}, "library", "v1", refs(1))
		if err != nil {
			t.Fatal(err)
		}
		if err := r.SetStatus(ctx, run.ID, verify.RunCompleted, ""); err != nil {
			t.Fatal(err)
		}
		if err := reports.SaveSealCheck(ctx, report.SealCheck{RunID: run.ID, Books: 1, FindingsVersion: 6, DecisionVersion: 9}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.Prune(ctx, 1); err != nil {
		t.Fatal(err)
	}
	var runs, rows, sealed int
	if err := s.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM runs), count(*), count(*) FILTER (WHERE seal_books = 1 AND seal_broken = 0)
		FROM run_history`).Scan(&runs, &rows, &sealed); err != nil {
		t.Fatal(err)
	}
	if runs >= 15 || rows != 15 || sealed != 15 {
		t.Fatalf("runs left %d, ledger rows %d, with seal counts %d; want fewer than 15 runs and 15/15 rows", runs, rows, sealed)
	}
}

// TestRunHistoryRow: the row says what kind of run it was, how it ended and
// what its last seal check found.
func TestRunHistoryRow(t *testing.T) {
	ctx := context.Background()
	r, s := runRepo(t)
	slot := time.Date(2026, 11, 23, 2, 30, 0, 0, time.UTC)
	run, err := r.CreateRun(ctx, verify.Scope{Incremental: true, ScheduledFor: slot}, "library", "v9", refs(2))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.FinishBook(ctx, run.ID, refs(2)[0], sealed(t, 10, "uuid-1")); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM run_history`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("a running run is in the ledger: %d rows, %v", n, err)
	}
	if err := r.SetStatus(ctx, run.ID, verify.RunFailed, "boom"); err != nil {
		t.Fatal(err)
	}
	if err := NewReportRepo(s).SaveSealCheck(ctx, report.SealCheck{RunID: run.ID, Books: 1, Broken: 1, FirstBroken: 10, FindingsVersion: 6, DecisionVersion: 9}, nil); err != nil {
		t.Fatal(err)
	}
	var kind, status, version string
	var scheduled time.Time
	var books, broken int
	if err := s.Pool.QueryRow(ctx, `SELECT kind, scheduled_for, status, app_version, books, seal_broken FROM run_history WHERE run_id = $1`, run.ID).
		Scan(&kind, &scheduled, &status, &version, &books, &broken); err != nil {
		t.Fatal(err)
	}
	if kind != "incremental" || !scheduled.Equal(slot) || status != "failed" || version != "v9" || books != 2 || broken != 1 {
		t.Fatalf("row = %s %v %s %s books %d broken %d", kind, scheduled, status, version, books, broken)
	}
	one, err := r.CreateRun(ctx, verify.Scope{BookID: 10}, "library", "v9", refs(1))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.SetStatus(ctx, one.ID, verify.RunCompleted, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Pool.QueryRow(ctx, `SELECT kind FROM run_history WHERE run_id = $1`, one.ID).Scan(&kind); err != nil || kind != "book" {
		t.Fatalf("one-book run kind = %q, %v", kind, err)
	}
}

// ledgerRow inserts a ledger row directly.
func ledgerRow(t *testing.T, s *Store, id int64, kind string, scheduled bool, status string, start time.Time, dur time.Duration, broken *int) {
	t.Helper()
	var slot *time.Time
	if scheduled {
		slot = &start
	}
	var books *int
	if broken != nil {
		one := 1
		books = &one
	}
	if _, err := s.Pool.Exec(context.Background(), `INSERT INTO run_history (run_id, library_id, kind, scheduled_for, status, app_version, started_at, finished_at, seal_books, seal_broken)
		VALUES ($1, 'library', $2, $3, $4, 'v', $5, $6, $7, $8)`, id, kind, slot, status, start, start.Add(dur), books, broken); err != nil {
		t.Fatal(err)
	}
}

func TestBurnIn(t *testing.T) {
	ctx := context.Background()
	zero := 0
	one := 1
	now := time.Now()
	day := 24 * time.Hour
	t.Run("streak", func(t *testing.T) {
		r, s := runRepo(t)
		ledgerRow(t, s, 1, "incremental", true, "completed", now.Add(-9*day), time.Minute, &one) // broken: ends the streak
		ledgerRow(t, s, 2, "incremental", true, "completed", now.Add(-8*day), time.Minute, &zero)
		ledgerRow(t, s, 3, "full", false, "failed", now.Add(-7*day), time.Minute, nil) // manual: doesn't count
		ledgerRow(t, s, 4, "full", true, "completed", now.Add(-6*day), 15*time.Minute, &zero)
		ledgerRow(t, s, 5, "incremental", true, "completed", now.Add(-5*day), time.Minute, &zero)
		b, err := r.BurnIn(ctx)
		if err != nil || b.CleanStreak != 3 {
			t.Fatalf("streak = %d, %v; want 3", b.CleanStreak, err)
		}
		// The newest run's seal check is still running: it is pending, not a
		// break in the streak.
		ledgerRow(t, s, 6, "incremental", true, "completed", now.Add(-4*day), time.Minute, nil)
		if b, _ := r.BurnIn(ctx); b.CleanStreak != 3 || !b.SealPending {
			t.Fatalf("pending newest run: streak %d, pending %v; want 3, true", b.CleanStreak, b.SealPending)
		}
		// Once a newer run exists, a run that never got its seal check ends
		// the streak.
		ledgerRow(t, s, 7, "incremental", true, "completed", now.Add(-3*day), time.Minute, &zero)
		if b, _ := r.BurnIn(ctx); b.CleanStreak != 1 || b.SealPending {
			t.Fatalf("unchecked older run: streak %d, pending %v; want 1, false", b.CleanStreak, b.SealPending)
		}
	})
	t.Run("a failed scheduled run with clean seals ends the streak", func(t *testing.T) {
		r, s := runRepo(t)
		ledgerRow(t, s, 1, "incremental", true, "completed", now.Add(-2*day), time.Minute, &zero)
		ledgerRow(t, s, 2, "incremental", true, "failed", now.Add(-day), time.Minute, &zero)
		ledgerRow(t, s, 3, "incremental", true, "completed", now, time.Minute, &zero)
		if b, _ := r.BurnIn(ctx); b.CleanStreak != 1 {
			t.Fatalf("streak = %d, want 1", b.CleanStreak)
		}
	})
	t.Run("p95", func(t *testing.T) {
		for _, tc := range []struct {
			n    int
			want time.Duration
		}{{1, 1 * time.Minute}, {11, 11 * time.Minute}, {20, 19 * time.Minute}} {
			r, s := runRepo(t)
			for i := 1; i <= tc.n; i++ {
				ledgerRow(t, s, int64(i), "incremental", true, "completed", now.Add(-time.Duration(i)*time.Hour), time.Duration(i)*time.Minute, &zero)
			}
			ledgerRow(t, s, 100, "incremental", false, "completed", now, time.Hour, &zero)             // manual: ignored
			ledgerRow(t, s, 101, "incremental", true, "completed", now.Add(-40*day), time.Hour, &zero) // older than 30 days
			ledgerRow(t, s, 102, "incremental", true, "failed", now.Add(-day), time.Hour, &zero)       // failed: not a daily duration
			b, err := r.BurnIn(ctx)
			if err != nil || b.DailyP95 != tc.want || b.DailyRuns != tc.n {
				t.Fatalf("n=%d: p95 %v over %d runs, %v; want %v over %d", tc.n, b.DailyP95, b.DailyRuns, err, tc.want, tc.n)
			}
		}
	})
	t.Run("longest full and rows", func(t *testing.T) {
		r, s := runRepo(t)
		ledgerRow(t, s, 1, "full", true, "completed", now.Add(-8*day), 15*time.Minute, &zero)
		ledgerRow(t, s, 2, "full", true, "completed", now.Add(-day), 16*time.Minute, &zero)
		ledgerRow(t, s, 3, "full", false, "completed", now, 3*time.Hour, &zero)
		b, err := r.BurnIn(ctx)
		if err != nil || b.LongestFull != 16*time.Minute || len(b.Rows) != 3 || b.Rows[0].RunID != 3 {
			t.Fatalf("longest full %v, rows %d (newest %v), %v", b.LongestFull, len(b.Rows), b.Rows, err)
		}
		// A scheduled full run cancelled after 2:30 still counts: the target
		// is not met just because the run didn't finish.
		ledgerRow(t, s, 4, "full", true, "cancelled", now.Add(-2*day), 150*time.Minute, nil)
		if b, _ := r.BurnIn(ctx); b.LongestFull != 150*time.Minute {
			t.Fatalf("longest full = %v, want 2h30m", b.LongestFull)
		}
	})
	t.Run("empty", func(t *testing.T) {
		r, _ := runRepo(t)
		b, err := r.BurnIn(ctx)
		if err != nil || b.CleanStreak != 0 || b.DailyRuns != 0 || len(b.Rows) != 0 {
			t.Fatalf("empty ledger = %+v, %v", b, err)
		}
	})
}

// TestRunHistoryBackfill: migration 0024 fills the ledger from the runs that
// still exist and have ended, with their seal checks.
func TestRunHistoryBackfill(t *testing.T) {
	ctx := context.Background()
	r, s := runRepo(t)
	var ended []int64
	for range 3 {
		run, err := r.CreateRun(ctx, verify.Scope{}, "library", "v1", refs(2))
		if err != nil {
			t.Fatal(err)
		}
		if err := r.SetStatus(ctx, run.ID, verify.RunCompleted, ""); err != nil {
			t.Fatal(err)
		}
		ended = append(ended, run.ID)
	}
	if err := NewReportRepo(s).SaveSealCheck(ctx, report.SealCheck{RunID: ended[0], Books: 2, FindingsVersion: 6, DecisionVersion: 9}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CreateRun(ctx, verify.Scope{}, "library", "v1", refs(1)); err != nil { // active: not in the ledger
		t.Fatal(err)
	}
	// Put the schema back as it was before 0024 and run the migration again.
	if _, err := s.Pool.Exec(ctx, `DROP TABLE run_history`); err != nil {
		t.Fatal(err)
	}
	raw, err := migrationFiles.ReadFile("migrations/0024_run_history.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, string(raw)); err != nil {
		t.Fatal(err)
	}
	var rows, books, sealedRows int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*), coalesce(sum(books), 0), count(seal_books) FROM run_history`).Scan(&rows, &books, &sealedRows); err != nil {
		t.Fatal(err)
	}
	if rows != 3 || books != 6 || sealedRows != 1 {
		t.Fatalf("backfill: %d rows, %d books, %d with a seal check; want 3, 6, 1", rows, books, sealedRows)
	}
}

// TestRunStatsReachTheLedger: a run's stats are stored with it, copied to
// its ledger row when it ends, and a later save updates both; a re-check of
// the seal keeps them.
func TestRunStatsReachTheLedger(t *testing.T) {
	ctx := context.Background()
	r, s := runRepo(t)
	reports := NewReportRepo(s)
	run, err := r.CreateRun(ctx, verify.Scope{}, "library", "v1", refs(1))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := r.Run(ctx, run.ID); err != nil || !got.Stats.Empty() {
		t.Fatalf("a new run's stats = %+v, %v; want empty", got.Stats, err)
	}
	first := verify.Stats{
		StageSeconds: map[string]float64{verify.StageCalibre: 1.5, verify.StageStore: 2},
		Lookups:      map[string]map[string]int{"dnb": {"ok": 3, "stale_hit": 1}},
		Extractor:    verify.ExtractorCounts{Calls: 2, Cached: 1, Failed: 1},
	}
	if err := r.SaveRunStats(ctx, run.ID, first); err != nil {
		t.Fatal(err)
	}
	if got, err := r.Run(ctx, run.ID); err != nil || !reflect.DeepEqual(got.Stats, first) {
		t.Fatalf("stored stats = %+v, %v; want %+v", got.Stats, err, first)
	}
	if err := r.SetStatus(ctx, run.ID, verify.RunCompleted, ""); err != nil {
		t.Fatal(err)
	}
	ledger := func() verify.Stats {
		t.Helper()
		var st verify.Stats
		if err := s.Pool.QueryRow(ctx, `SELECT stats FROM run_history WHERE run_id = $1`, run.ID).Scan(&st); err != nil {
			t.Fatal(err)
		}
		return st
	}
	if got := ledger(); !reflect.DeepEqual(got, first) {
		t.Fatalf("ledger stats = %+v, want %+v", got, first)
	}
	second := first
	second.Extractor = verify.ExtractorCounts{Calls: 5}
	if err := r.SaveRunStats(ctx, run.ID, second); err != nil {
		t.Fatal(err)
	}
	if err := reports.SaveSealCheck(ctx, report.SealCheck{RunID: run.ID, Books: 1, FindingsVersion: 6, DecisionVersion: 9}, nil); err != nil {
		t.Fatal(err)
	}
	if got := ledger(); !reflect.DeepEqual(got, second) {
		t.Fatalf("ledger stats after a later save and a seal check = %+v, want %+v", got, second)
	}
	if err := r.SaveRunStats(ctx, 1<<40, second); !errors.Is(err, verify.ErrNoSuchRun) {
		t.Errorf("SaveRunStats of a missing run = %v, want ErrNoSuchRun", err)
	}
}
