package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/felixapel/calibre-bookwarden/internal/metrics"
)

// metricsBudget bounds one scrape's queries together.
const metricsBudget = 5 * time.Second

// MetricsRepo reads the numbers behind /metrics.
type MetricsRepo struct {
	store *Store
	fixes *FixRepo
}

// NewMetricsRepo returns a MetricsRepo over st.
func NewMetricsRepo(st *Store) *MetricsRepo {
	return &MetricsRepo{store: st, fixes: NewFixRepo(st)}
}

// Snapshot reads every database-backed metric, one query per family, within a
// budget of five seconds in all.
func (m *MetricsRepo) Snapshot(ctx context.Context) (metrics.Snapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, metricsBudget)
	defer cancel()
	var s metrics.Snapshot
	var err error
	if s.RunsByStatus, err = m.counts(ctx, "runs by status", `SELECT status, count(*)::int FROM runs GROUP BY status`); err != nil {
		return s, err
	}
	if s.LastRun, err = m.lastRun(ctx); err != nil {
		return s, err
	}
	// Open means no decision row: the last finished run's projected
	// proposals that the proposals table has nothing on.
	if s.ProposalsOpen, err = m.counts(ctx, "open proposals", `SELECT x.field, count(*)::int
		FROM fix_projection x
		LEFT JOIN proposals p ON p.run_id = x.run_id AND p.book_id = x.book_id AND p.field = x.field
		WHERE x.run_id = (SELECT id FROM runs WHERE finished_at IS NOT NULL ORDER BY id DESC LIMIT 1) AND p.id IS NULL
		GROUP BY x.field`); err != nil {
		return s, err
	}
	ops, err := m.counts(ctx, "completed operations", `SELECT o.kind, count(*)::int
		FROM operations o
		JOIN LATERAL (SELECT state FROM operation_events e WHERE e.operation_id = o.id ORDER BY e.seq DESC LIMIT 1) last ON true
		WHERE last.state = 'completed' GROUP BY o.kind`)
	if err != nil {
		return s, err
	}
	s.FixesApplied, s.FixesUndone = ops[OpApply], ops[OpUndo]
	if s.IncidentsOpen, err = m.one(ctx, "open incidents", `SELECT count(*)::int FROM incidents WHERE acknowledged_at IS NULL`); err != nil {
		return s, err
	}
	if s.SealChecksBroken, err = m.one(ctx, "broken seal checks", `SELECT count(*)::int FROM seal_checks WHERE broken > 0`); err != nil {
		return s, err
	}
	types, err := m.fixes.Precision(ctx)
	if err != nil {
		return s, err
	}
	s.Labels = labelCounts(types)
	return s, nil
}

// labelCounts sums the labels of each fix type over its evidence paths.
func labelCounts(types []TypeLabels) []metrics.LabelCount {
	var out []metrics.LabelCount
	for _, t := range types {
		i := slices.IndexFunc(out, func(l metrics.LabelCount) bool { return l.Field == t.Field && l.Kind == t.Kind })
		if i < 0 {
			out = append(out, metrics.LabelCount{Field: t.Field, Kind: t.Kind})
			i = len(out) - 1
		}
		out[i].Labels += t.Labels
		out[i].Misses += t.Misses
	}
	return out
}

func (m *MetricsRepo) one(ctx context.Context, what, query string) (int, error) {
	var n int
	if err := m.store.Pool.QueryRow(ctx, query).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: metrics %s: %w", what, err)
	}
	return n, nil
}

// counts runs a query of (key text, n int) rows.
func (m *MetricsRepo) counts(ctx context.Context, what, query string, args ...any) (map[string]int, error) {
	rows, err := m.store.Pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: metrics %s: %w", what, err)
	}
	type kv struct {
		K string
		N int
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[kv])
	if err != nil {
		return nil, fmt.Errorf("store: metrics %s: %w", what, err)
	}
	out := make(map[string]int, len(list))
	for _, e := range list {
		out[e.K] = e.N
	}
	return out, nil
}

// lastRun reads the duration, kind and book states of the last finished run,
// or nil when none has finished.
func (m *MetricsRepo) lastRun(ctx context.Context) (*metrics.LastRun, error) {
	var id int64
	var seconds float64
	var kind string
	err := m.store.Pool.QueryRow(ctx, `SELECT id, extract(epoch FROM finished_at - started_at)::float8,
		CASE WHEN scope_book_id IS NOT NULL THEN 'book' WHEN incremental THEN 'incremental' ELSE 'full' END
		FROM runs WHERE finished_at IS NOT NULL ORDER BY id DESC LIMIT 1`).Scan(&id, &seconds, &kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: metrics last run: %w", err)
	}
	books, err := m.counts(ctx, "last run books", `SELECT state, count(*)::int FROM run_books WHERE run_id = $1 GROUP BY state`, id)
	if err != nil {
		return nil, err
	}
	return &metrics.LastRun{DurationSeconds: seconds, Kind: kind, Books: books}, nil
}
