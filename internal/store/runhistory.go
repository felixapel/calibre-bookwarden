package store

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
)

// historyUpsert writes run $1's ledger row from runs, run_books and
// seal_checks (migrations 0024 and 0027). It is run when a run ends and when
// its seal check is written, inside the caller's transaction; a run that
// hasn't ended gets no row.
const historyUpsert = `INSERT INTO run_history (run_id, library_id, kind, scheduled_for, status, app_version, started_at, finished_at,
    books, reused, seal_books, seal_broken, seal_checked_at, stats)
SELECT r.id, r.library_id,
    CASE WHEN r.scope_book_id IS NOT NULL THEN 'book' WHEN r.incremental THEN 'incremental' ELSE 'full' END,
    r.scheduled_for, r.status, r.app_version, r.started_at, r.finished_at,
    (SELECT count(*) FROM run_books b WHERE b.run_id = r.id),
    (SELECT count(*) FROM run_books b WHERE b.run_id = r.id AND b.state = 'checked' AND b.reused),
    s.books, s.broken, s.checked_at, r.stats
FROM runs r LEFT JOIN seal_checks s ON s.run_id = r.id
WHERE r.id = $1 AND r.status IN ('completed', 'cancelled', 'failed')
ON CONFLICT (run_id) DO UPDATE SET status = EXCLUDED.status, finished_at = EXCLUDED.finished_at,
    books = EXCLUDED.books, reused = EXCLUDED.reused, seal_books = EXCLUDED.seal_books,
    seal_broken = EXCLUDED.seal_broken, seal_checked_at = EXCLUDED.seal_checked_at, stats = EXCLUDED.stats`

// LedgerRow is one run in the ledger. SealBroken is nil while the run has
// no seal check.
type LedgerRow struct {
	RunID         int64
	Kind          string // full, incremental or book
	Scheduled     bool
	Status        string
	StartedAt     time.Time
	Duration      time.Duration // zero while FinishedAt is unknown
	Books, Reused int
	SealBroken    *int
}

// BurnIn answers the v1.0 burn-in questions from the ledger (spec §3.1 and
// §3.4). Rows are the newest ledger rows, newest first.
type BurnIn struct {
	// CleanStreak counts scheduled runs in a row, newest first, that
	// completed with a seal check and no broken seal. The newest scheduled
	// run, if it completed and has no seal check yet, is skipped and
	// SealPending set: its check is still running (or waits for the hourly
	// sweep). Any older run without a seal check ends the streak.
	CleanStreak int
	SealPending bool
	// DailyP95 is the nearest-rank 95th percentile of the duration of the
	// scheduled incremental runs started in the last 30 days, DailyRuns of
	// them.
	DailyP95  time.Duration
	DailyRuns int
	// LongestFull is the longest scheduled full run started in the last 30
	// days, whether it completed or not: a full run cancelled past the
	// target still missed it.
	LongestFull time.Duration
	Rows        []LedgerRow
}

// burnInRows is how many ledger rows BurnIn returns.
const burnInRows = 30

// BurnIn reads the ledger.
func (r *RunRepo) BurnIn(ctx context.Context) (BurnIn, error) {
	rows, err := r.pool.Query(ctx, `SELECT run_id, kind, scheduled_for IS NOT NULL, status, started_at,
		coalesce(finished_at - started_at, interval '0'), books, reused, seal_broken
		FROM run_history ORDER BY run_id DESC`)
	if err != nil {
		return BurnIn{}, fmt.Errorf("store: read run ledger: %w", err)
	}
	all, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (LedgerRow, error) {
		var l LedgerRow
		err := row.Scan(&l.RunID, &l.Kind, &l.Scheduled, &l.Status, &l.StartedAt, &l.Duration, &l.Books, &l.Reused, &l.SealBroken)
		return l, err
	})
	if err != nil {
		return BurnIn{}, fmt.Errorf("store: read run ledger: %w", err)
	}
	var b BurnIn
	b.Rows = all[:min(len(all), burnInRows)]
	streak := true
	since := time.Now().Add(-30 * 24 * time.Hour)
	var daily []time.Duration
	newest := true
	for _, l := range all {
		if !l.Scheduled {
			continue
		}
		switch {
		case !streak:
		case newest && l.Status == "completed" && l.SealBroken == nil:
			b.SealPending = true
		case l.Status == "completed" && l.SealBroken != nil && *l.SealBroken == 0:
			b.CleanStreak++
		default:
			streak = false
		}
		newest = false
		if l.StartedAt.Before(since) {
			continue
		}
		switch {
		case l.Kind == "incremental" && l.Status == "completed":
			daily = append(daily, l.Duration)
		case l.Kind == "full":
			b.LongestFull = max(b.LongestFull, l.Duration)
		}
	}
	if b.DailyRuns = len(daily); b.DailyRuns > 0 {
		slices.Sort(daily)
		rank := (95*len(daily) + 99) / 100 // ceil(0.95·n)
		b.DailyP95 = daily[rank-1]
	}
	return b, nil
}
