package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/puddle/v2"

	"github.com/felixapel/calibre-bookwarden/internal/decide"
	"github.com/felixapel/calibre-bookwarden/internal/duplicates"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/findings"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

// runnerLockID is an arbitrary constant ("bkwdrun!" in ASCII): the session
// advisory lock held by the one process that executes runs.
const runnerLockID int64 = 0x626b776472756e21

// RunRepo implements verify.RunStore on Postgres.
type RunRepo struct {
	pool *pgxpool.Pool
}

// NewRunRepo returns the run repository for s.
func NewRunRepo(s *Store) *RunRepo { return &RunRepo{pool: s.Pool} }

const runColumns = `id, scope_book_id, status, library_id, app_version, error, cancel_requested, started_at, finished_at, duplicates_version, incremental, scheduled_for, pinned, stats`

func scanRun(row pgx.Row) (verify.Run, error) {
	var (
		run      verify.Run
		scope    *int32
		status   string
		finished *time.Time
		slot     *time.Time
	)
	if err := row.Scan(&run.ID, &scope, &status, &run.LibraryID, &run.AppVersion, &run.Error, &run.CancelRequested, &run.StartedAt, &finished, &run.DuplicatesVersion, &run.Scope.Incremental, &slot, &run.Pinned, &run.Stats); err != nil {
		return verify.Run{}, err
	}
	if scope != nil {
		run.Scope.BookID = int(*scope)
	}
	run.Status = verify.RunStatus(status)
	run.StartedAt = run.StartedAt.UTC()
	if finished != nil {
		run.FinishedAt = finished.UTC()
	}
	if slot != nil {
		run.Scope.ScheduledFor = slot.UTC()
	}
	return run, nil
}

func (r *RunRepo) CreateRun(ctx context.Context, scope verify.Scope, libraryID, appVersion string, books []library.BookRef) (verify.Run, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return verify.Run{}, fmt.Errorf("store: begin run: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var scopeBook *int
	if scope.BookID != 0 {
		scopeBook = &scope.BookID
	}
	var slot *time.Time
	if !scope.ScheduledFor.IsZero() {
		slot = &scope.ScheduledFor
	}
	run, err := scanRun(tx.QueryRow(ctx, `INSERT INTO runs (scope_book_id, status, library_id, app_version, duplicates_version, incremental, scheduled_for)
		VALUES ($1, 'running', $2, $3, $4, $5, $6) RETURNING `+runColumns, scopeBook, libraryID, appVersion, duplicates.Version, scope.Incremental, slot))
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.ConstraintName == "runs_one_active" {
		return verify.Run{}, verify.ErrRunActive
	}
	if err != nil {
		return verify.Run{}, fmt.Errorf("store: create run: %w", err)
	}
	rows := make([][]any, 0, len(books))
	for i, b := range books {
		rows = append(rows, []any{run.ID, b.ID, b.UUID, i})
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"run_books"}, []string{"run_id", "book_id", "uuid", "position"}, pgx.CopyFromRows(rows)); err != nil {
		return verify.Run{}, fmt.Errorf("store: freeze the book list: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return verify.Run{}, fmt.Errorf("store: commit run: %w", err)
	}
	return run, nil
}

func (r *RunRepo) ActiveRun(ctx context.Context) (verify.Run, bool, error) {
	run, err := scanRun(r.pool.QueryRow(ctx, `SELECT `+runColumns+` FROM runs WHERE status IN ('running', 'waiting')`))
	if errors.Is(err, pgx.ErrNoRows) {
		return verify.Run{}, false, nil
	}
	if err != nil {
		return verify.Run{}, false, fmt.Errorf("store: active run: %w", err)
	}
	return run, true, nil
}

func (r *RunRepo) Run(ctx context.Context, id int64) (verify.Run, error) {
	run, err := scanRun(r.pool.QueryRow(ctx, `SELECT `+runColumns+` FROM runs WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return verify.Run{}, fmt.Errorf("store: run %d: %w", id, verify.ErrNoSuchRun)
	}
	if err != nil {
		return verify.Run{}, fmt.Errorf("store: run %d: %w", id, err)
	}
	return run, nil
}

// exists turns "no rows" from a query about a run into ErrNoSuchRun.
func (r *RunRepo) exists(ctx context.Context, id int64) error {
	var ok bool
	if err := r.pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM runs WHERE id = $1)", id).Scan(&ok); err != nil {
		return fmt.Errorf("store: run %d: %w", id, err)
	}
	if !ok {
		return fmt.Errorf("store: run %d: %w", id, verify.ErrNoSuchRun)
	}
	return nil
}

func (r *RunRepo) PendingBooks(ctx context.Context, runID int64) ([]library.BookRef, error) {
	rows, err := r.pool.Query(ctx, `SELECT book_id, uuid FROM run_books
		WHERE run_id = $1 AND state = 'pending' ORDER BY position`, runID)
	if err != nil {
		return nil, fmt.Errorf("store: pending books: %w", err)
	}
	refs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (library.BookRef, error) {
		var ref library.BookRef
		err := row.Scan(&ref.ID, &ref.UUID)
		return ref, err
	})
	if err != nil {
		return nil, fmt.Errorf("store: pending books: %w", err)
	}
	if len(refs) == 0 {
		return nil, r.exists(ctx, runID)
	}
	return refs, nil
}

// TakeBook marks a pending book as in hand (B4's crash-loop guard).
func (r *RunRepo) TakeBook(ctx context.Context, runID int64, bookID int) error {
	if _, err := r.pool.Exec(ctx, `UPDATE run_books SET in_hand_since = now() WHERE run_id = $1 AND book_id = $2 AND state = 'pending'`, runID, bookID); err != nil {
		return fmt.Errorf("store: take book %d: %w", bookID, err)
	}
	return nil
}

// ReleaseBooks clears the in-hand mark of books an orderly interruption put
// back.
func (r *RunRepo) ReleaseBooks(ctx context.Context, runID int64, bookIDs []int) error {
	if len(bookIDs) == 0 {
		return nil
	}
	if _, err := r.pool.Exec(ctx, `UPDATE run_books SET in_hand_since = NULL WHERE run_id = $1 AND book_id = ANY($2)`, runID, bookIDs); err != nil {
		return fmt.Errorf("store: release books: %w", err)
	}
	return nil
}

// RecoverCrashed counts a crash for every pending book of the run still in
// hand, clears the mark, and fails as process_crashed each one with two or
// more crashes. It returns how many it failed and the pending books left
// with a crash.
func (r *RunRepo) RecoverCrashed(ctx context.Context, runID int64) (int, []int, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, nil, fmt.Errorf("store: begin crash recovery: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `UPDATE run_books SET crashes = crashes + 1, in_hand_since = NULL
		WHERE run_id = $1 AND state = 'pending' AND in_hand_since IS NOT NULL`, runID); err != nil {
		return 0, nil, fmt.Errorf("store: count crashes: %w", err)
	}
	tag, err := tx.Exec(ctx, `UPDATE run_books SET state = 'failed', reason = 'process_crashed', finished_at = now()
		WHERE run_id = $1 AND state = 'pending' AND crashes >= $2`, runID, verify.MaxCrashes)
	if err != nil {
		return 0, nil, fmt.Errorf("store: give up crashed books: %w", err)
	}
	rows, err := tx.Query(ctx, `SELECT book_id FROM run_books WHERE run_id = $1 AND state = 'pending' AND crashes > 0 ORDER BY position`, runID)
	if err != nil {
		return 0, nil, fmt.Errorf("store: list crashed books: %w", err)
	}
	crashed, err := pgx.CollectRows(rows, pgx.RowTo[int])
	if err != nil {
		return 0, nil, fmt.Errorf("store: list crashed books: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, nil, fmt.Errorf("store: commit crash recovery: %w", err)
	}
	return int(tag.RowsAffected()), crashed, nil
}

func (r *RunRepo) FinishBook(ctx context.Context, runID int64, ref library.BookRef, out verify.Outcome) error {
	err := r.finishBook(ctx, runID, ref, out)
	if err != nil && storeUnavailable(err) {
		return fmt.Errorf("%w: %w", verify.ErrStoreUnavailable, err)
	}
	return err
}

// storeUnavailable reports whether err means the database is away or busy,
// rather than refusing this book's result: the pool is closed, the
// connection or network failed or was dropped (an unexpected EOF, a closed
// connection, anything pgconn calls safe to retry or a timeout), the context
// ended, or Postgres answered with
// a connection (08), resource (53) or operator (57) error, a serialization
// failure or a deadlock.
func storeUnavailable(err error) bool {
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok {
		switch {
		case strings.HasPrefix(pgErr.Code, "08"), strings.HasPrefix(pgErr.Code, "53"), strings.HasPrefix(pgErr.Code, "57"),
			pgErr.Code == "40001", pgErr.Code == "40P01":
			return true
		}
		return false
	}
	if _, ok := errors.AsType[*pgconn.ConnectError](err); ok {
		return true
	}
	if _, ok := errors.AsType[net.Error](err); ok {
		return true
	}
	return errors.Is(err, puddle.ErrClosedPool) || errors.Is(err, pgconn.ErrConnClosed) || errors.Is(err, io.ErrUnexpectedEOF) ||
		pgconn.SafeToRetry(err) || pgconn.Timeout(err) ||
		errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func (r *RunRepo) finishBook(ctx context.Context, runID int64, ref library.BookRef, out verify.Outcome) error {
	if out.State != verify.BookChecked && out.State != verify.BookFailed && out.State != verify.BookSourceChanged {
		return fmt.Errorf("store: invalid book outcome: %w", findings.ErrInvalidEvidence)
	}
	var pkg evidence.Package
	var current []evidence.Finding
	if out.State == verify.BookChecked {
		var err error
		pkg, _, err = decide.Open(ctx, out.Evidence, out.Seal)
		if err != nil {
			return fmt.Errorf("store: verify book evidence: %w", err)
		}
		if err := checkExcerptBodies(pkg, out.Excerpts); err != nil {
			return err
		}
		if err := checkRaws(pkg, out.Raws); err != nil {
			return err
		}
		current = pkg.Findings
		if pkg.FindingsVersion != 0 && pkg.FindingsVersion < findings.Version {
			current, err = findings.Derive(ctx, pkg)
			if err != nil {
				return fmt.Errorf("store: derive current findings: %w", err)
			}
		}
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: begin book result: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var uuid, state, libraryID string
	err = tx.QueryRow(ctx, `SELECT b.uuid, b.state, r.library_id FROM run_books b JOIN runs r ON r.id=b.run_id
		WHERE b.run_id=$1 AND b.book_id=$2 FOR UPDATE OF b`, runID, ref.ID).Scan(&uuid, &state, &libraryID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && state != string(verify.BookPending)) {
		return fmt.Errorf("store: run %d book %d: %w", runID, ref.ID, verify.ErrBookFinished)
	}
	if err != nil {
		return fmt.Errorf("store: lock book result: %w", err)
	}
	if ref.UUID != uuid || (out.State == verify.BookChecked && (pkg.LibraryID != libraryID || pkg.BookID != ref.ID || pkg.UUID != uuid)) {
		return fmt.Errorf("store: book identity mismatch: %w", findings.ErrInvalidEvidence)
	}
	if _, err := tx.Exec(ctx, `UPDATE run_books SET state=$3, reason=$4, finished_at=now(), reused=$5, in_hand_since=NULL WHERE run_id=$1 AND book_id=$2`, runID, ref.ID, string(out.State), out.Reason, out.Reused && out.State == verify.BookChecked); err != nil {
		return fmt.Errorf("store: book %d result: %w", ref.ID, err)
	}
	if out.State == verify.BookChecked {
		var measured *time.Time
		if !out.MeasuredAt.IsZero() {
			measured = &out.MeasuredAt
		}
		if _, err := tx.Exec(ctx, `INSERT INTO evidence (run_id, book_id, uuid, schema_version, package, seal, projection_version, inspect_version, measured_at)
			VALUES ($1,$2,$3,$4,$5::jsonb,$6,$7,$8,$9)`, runID, ref.ID, uuid, pkg.SchemaVersion, string(out.Evidence), out.Seal, findings.Version, out.InspectKey, measured); err != nil {
			return fmt.Errorf("store: book %d evidence: %w", ref.ID, err)
		}
		// The links come before the bodies: they take the table lock that
		// makes a retention sweep wait for this transaction (see Prune).
		if err := insertExcerptLinks(ctx, tx, runID, ref.ID, out.Excerpts); err != nil {
			return fmt.Errorf("store: book %d: %w", ref.ID, err)
		}
		if err := insertExcerpts(ctx, tx, out.Excerpts); err != nil {
			return fmt.Errorf("store: book %d: %w", ref.ID, err)
		}
		if err := insertRawLinks(ctx, tx, runID, ref.ID, out.Raws); err != nil {
			return fmt.Errorf("store: book %d: %w", ref.ID, err)
		}
		if err := insertRaws(ctx, tx, out.Raws); err != nil {
			return fmt.Errorf("store: book %d: %w", ref.ID, err)
		}
		rows := make([][]any, 0, len(current))
		for i, f := range current {
			rows = append(rows, []any{runID, ref.ID, i, f.Code, f.Format, f.Field, f.Reason})
		}
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{"findings"}, []string{"run_id", "book_id", "ordinal", "code", "format", "field", "reason"}, pgx.CopyFromRows(rows)); err != nil {
			return fmt.Errorf("store: book %d findings: %w", ref.ID, err)
		}
		if c := pkg.Cover; c != nil && c.State == evidence.StateOK {
			h, compared, ok := duplicates.CoverHash(c)
			if !ok {
				return fmt.Errorf("store: book %d cover hash: %w", ref.ID, findings.ErrInvalidEvidence)
			}
			// The hash's bits, stored as a signed bigint.
			if _, err := tx.Exec(ctx, `INSERT INTO covers (run_id, book_id, sha256, dhash, width, height, comparable) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
				runID, ref.ID, c.SHA256, int64(h), c.Width, c.Height, compared); err != nil { //nolint:gosec // bit-preserving conversion
				return fmt.Errorf("store: book %d cover: %w", ref.ID, err)
			}
		}
		keys := duplicates.Keys(pkg)
		rows = rows[:0]
		for _, k := range keys {
			rows = append(rows, []any{runID, ref.ID, k.Kind, k.Value, k.Detail})
		}
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{"duplicate_keys"}, []string{"run_id", "book_id", "kind", "value", "detail"}, pgx.CopyFromRows(rows)); err != nil {
			return fmt.Errorf("store: book %d duplicate keys: %w", ref.ID, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: commit book %d result: %w", ref.ID, err)
	}
	return nil
}

// SetStatus sets a run's status; a run that ends gets its ledger row in the
// same transaction (migration 0024).
func (r *RunRepo) SetStatus(ctx context.Context, runID int64, status verify.RunStatus, message string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: run %d status: %w", runID, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `UPDATE runs SET status = $2, error = $3,
		finished_at = CASE WHEN $4 THEN now() END WHERE id = $1`, runID, string(status), message, status.Terminal())
	if err != nil {
		return fmt.Errorf("store: run %d status: %w", runID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("store: run %d: %w", runID, verify.ErrNoSuchRun)
	}
	if status.Terminal() {
		if _, err := tx.Exec(ctx, historyUpsert, runID); err != nil {
			return fmt.Errorf("store: run %d ledger: %w", runID, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: run %d status: %w", runID, err)
	}
	return nil
}

// SaveRunStats replaces the run's stats and, once the run has ended, its
// ledger row's (B5).
func (r *RunRepo) SaveRunStats(ctx context.Context, runID int64, stats verify.Stats) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: run %d stats: %w", runID, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `UPDATE runs SET stats = $2 WHERE id = $1`, runID, stats)
	if err != nil {
		return fmt.Errorf("store: run %d stats: %w", runID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("store: run %d: %w", runID, verify.ErrNoSuchRun)
	}
	if _, err := tx.Exec(ctx, `UPDATE run_history SET stats = $2 WHERE run_id = $1`, runID, stats); err != nil {
		return fmt.Errorf("store: run %d ledger stats: %w", runID, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: run %d stats: %w", runID, err)
	}
	return nil
}

func (r *RunRepo) RequestCancel(ctx context.Context, runID int64) error {
	tag, err := r.pool.Exec(ctx, `UPDATE runs SET cancel_requested = true
		WHERE id = $1 AND status IN ('running', 'waiting')`, runID)
	if err != nil {
		return fmt.Errorf("store: cancel run %d: %w", runID, err)
	}
	if tag.RowsAffected() == 0 {
		return r.exists(ctx, runID)
	}
	return nil
}

func (r *RunRepo) Progress(ctx context.Context, runID int64) (verify.Progress, error) {
	var (
		p  verify.Progress
		ok bool
	)
	err := r.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM runs WHERE id = $1),
		count(*), count(*) FILTER (WHERE state = 'pending'), count(*) FILTER (WHERE state = 'checked'),
		count(*) FILTER (WHERE state = 'source_changed'), count(*) FILTER (WHERE state = 'failed'),
		count(*) FILTER (WHERE state = 'checked' AND reused)
		FROM run_books WHERE run_id = $1`, runID).Scan(&ok, &p.Total, &p.Pending, &p.Checked, &p.SourceChanged, &p.Failed, &p.Reused)
	if err != nil {
		return verify.Progress{}, fmt.Errorf("store: run %d progress: %w", runID, err)
	}
	if !ok {
		return verify.Progress{}, fmt.Errorf("store: run %d: %w", runID, verify.ErrNoSuchRun)
	}
	return p, nil
}

func (r *RunRepo) ListRuns(ctx context.Context, limit int) ([]verify.Run, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+runColumns+` FROM runs ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list runs: %w", err)
	}
	runs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (verify.Run, error) { return scanRun(row) })
	if err != nil {
		return nil, fmt.Errorf("store: list runs: %w", err)
	}
	return runs, nil
}

// PruneResult counts what a prune deleted.
type PruneResult struct{ Runs, Excerpts, Raws int64 }

// SetPinned pins a run, so that Prune never removes it, or unpins it.
func (r *RunRepo) SetPinned(ctx context.Context, runID int64, pinned bool) error {
	tag, err := r.pool.Exec(ctx, `UPDATE runs SET pinned = $2 WHERE id = $1`, runID, pinned)
	if err != nil {
		return fmt.Errorf("store: pin run %d: %w", runID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("store: run %d: %w", runID, verify.ErrNoSuchRun)
	}
	return nil
}

// Prune deletes the ended runs beyond the newest keep, with their books and
// evidence, and then every excerpt no package refers to (ADR-039). A keep of 0
// keeps everything, as BOOKWARDEN_RETAIN_RUNS=0 does; no call of this API wipes
// the history. It keeps:
//   - pinned runs;
//   - the runs a decided proposal or an operation points to (spec section 6);
//   - runs that are running or waiting, which are never ended;
//   - the newest completed run of the whole library and every run newer than
//     it, so that failed or cancelled runs, or a string of one-book runs, can't
//     push out the last good one. With no completed whole-library run nothing
//     is pruned.
func (r *RunRepo) Prune(ctx context.Context, keep int) (PruneResult, error) {
	if keep < 0 {
		return PruneResult{}, fmt.Errorf("store: prune runs: keep %d is negative", keep)
	}
	if keep == 0 {
		return PruneResult{}, nil
	}
	return r.prune(ctx, keep, false)
}

// prune is Prune; wipe, which tests use, drops the keep count and the guard
// of the newest completed run, but still keeps pinned, decided and
// operation-referenced runs.
//
// A carried rejection is a copy of a decision that lives in an older run, so
// it doesn't keep its own run: the copies go first, in the same statement.
// They are matched again as carried when deleted, so a person who changed one
// at the same moment makes the foreign key fail the prune instead of losing
// the decision.
//
// The excerpts are found through evidence_excerpts, one indexed anti-join,
// instead of by opening every jsonb package. The prune holds a lock that
// conflicts with the row locks FinishBook takes on that table, so a package
// being stored either commits its links before the sweep looks or waits until
// the sweep is done and writes its bodies again.
func (r *RunRepo) prune(ctx context.Context, keep int, wipe bool) (PruneResult, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return PruneResult{}, fmt.Errorf("store: begin prune: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `LOCK TABLE evidence_excerpts IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return PruneResult{}, fmt.Errorf("store: prune runs: lock links: %w", err)
	}
	if _, err := tx.Exec(ctx, `LOCK TABLE evidence_extraction_raw IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return PruneResult{}, fmt.Errorf("store: prune runs: lock raw links: %w", err)
	}
	var res PruneResult
	tag, err := tx.Exec(ctx, `WITH doomed AS (
			SELECT id FROM runs WHERE status IN ('completed', 'cancelled', 'failed') AND NOT pinned
			AND id IN (SELECT id FROM runs ORDER BY id DESC OFFSET $1::bigint)
			AND ($2 OR id < (SELECT max(id) FROM runs WHERE status = 'completed' AND scope_book_id IS NULL))
			AND NOT EXISTS (SELECT 1 FROM proposals p WHERE p.run_id = runs.id AND p.decision_mode <> 'carried')
			AND NOT EXISTS (SELECT 1 FROM operations o WHERE o.run_id = runs.id)),
		gone AS (DELETE FROM proposals p USING doomed d WHERE p.run_id = d.id AND p.decision_mode = 'carried')
		DELETE FROM runs USING doomed d WHERE runs.id = d.id`, keep, wipe)
	if err != nil {
		return PruneResult{}, fmt.Errorf("store: prune runs: %w", err)
	}
	res.Runs = tag.RowsAffected()
	tag, err = tx.Exec(ctx, `DELETE FROM excerpts e WHERE NOT EXISTS (SELECT 1 FROM evidence_excerpts x WHERE x.sha256 = e.sha256)`)
	if err != nil {
		return PruneResult{}, fmt.Errorf("store: prune excerpts: %w", err)
	}
	res.Excerpts = tag.RowsAffected()
	// The cache rows of a swept answer go by cascade.
	tag, err = tx.Exec(ctx, `DELETE FROM extraction_raw r WHERE NOT EXISTS (SELECT 1 FROM evidence_extraction_raw x WHERE x.sha256 = r.sha256)`)
	if err != nil {
		return PruneResult{}, fmt.Errorf("store: prune raw outputs: %w", err)
	}
	res.Raws = tag.RowsAffected()
	if err := tx.Commit(ctx); err != nil {
		return PruneResult{}, fmt.Errorf("store: commit prune: %w", err)
	}
	return res, nil
}

// PriorEvidence returns the newest sealed package of the book in this
// library, if its seal holds and every excerpt it refers to is stored and
// matches its hash. The newest package is the only candidate: if it is no
// good, an older one is not offered.
func (r *RunRepo) PriorEvidence(ctx context.Context, libraryID string, ref library.BookRef) (verify.Prior, bool, error) {
	var raw, seal, key, uuid string
	var measured *time.Time
	err := r.pool.QueryRow(ctx, `SELECT e.package::text, e.seal, e.inspect_version, e.uuid, e.measured_at FROM evidence e JOIN runs r ON r.id = e.run_id
		WHERE e.book_id = $1 AND r.library_id = $2 ORDER BY e.run_id DESC LIMIT 1`, ref.ID, libraryID).Scan(&raw, &seal, &key, &uuid, &measured)
	if errors.Is(err, pgx.ErrNoRows) {
		return verify.Prior{}, false, nil
	}
	if err != nil {
		return verify.Prior{}, false, fmt.Errorf("store: prior evidence of book %d: %w", ref.ID, err)
	}
	if uuid != ref.UUID {
		return verify.Prior{}, false, nil
	}
	p, err := evidence.Open([]byte(raw), seal)
	if err != nil {
		return verify.Prior{}, false, nil
	}
	refs, err := evidence.ExcerptRefs(p)
	if err != nil {
		return verify.Prior{}, false, nil
	}
	bodies, ok, err := r.readExcerpts(ctx, refs)
	if err != nil || !ok {
		return verify.Prior{}, false, err
	}
	prior := verify.Prior{Package: p, InspectKey: key, Excerpts: bodies}
	if measured != nil {
		prior.MeasuredAt = measured.UTC()
	}
	return prior, true, nil
}

// readExcerpts reads the excerpts with the given hashes and lengths. ok is
// false when one is absent or doesn't match its hash and length.
func (r *RunRepo) readExcerpts(ctx context.Context, refs map[string]int) (map[string]string, bool, error) {
	bodies := make(map[string]string, len(refs))
	if len(refs) == 0 {
		return bodies, true, nil
	}
	rows, err := r.pool.Query(ctx, `SELECT sha256, body, chars FROM excerpts WHERE sha256 = ANY($1)`, slices.Collect(maps.Keys(refs)))
	if err != nil {
		return nil, false, fmt.Errorf("store: read excerpts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var hash, body string
		var chars int
		if err := rows.Scan(&hash, &body, &chars); err != nil {
			return nil, false, fmt.Errorf("store: read excerpts: %w", err)
		}
		if want, ok := refs[hash]; !ok || chars != want || !validExcerptBody(hash, want, body) {
			return nil, false, nil
		}
		bodies[hash] = body
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("store: read excerpts: %w", err)
	}
	return bodies, len(bodies) == len(refs), nil
}

// LastScheduled returns the scheduled run with the latest slot: a run the
// scheduler started, never one started by hand. With fullOnly it skips
// incremental runs.
func (r *RunRepo) LastScheduled(ctx context.Context, fullOnly bool) (verify.Run, bool, error) {
	run, err := scanRun(r.pool.QueryRow(ctx, `SELECT `+runColumns+` FROM runs
		WHERE scheduled_for IS NOT NULL AND (NOT $1 OR NOT incremental)
		ORDER BY scheduled_for DESC, id DESC LIMIT 1`, fullOnly))
	if errors.Is(err, pgx.ErrNoRows) {
		return verify.Run{}, false, nil
	}
	if err != nil {
		return verify.Run{}, false, fmt.Errorf("store: last scheduled run: %w", err)
	}
	return run, true, nil
}

// LastFullRun returns the newest run of the whole library that was not
// incremental, however it was started and however it ended. A scheduled slot
// that such a run started at or after is served by it (ADR-039).
func (r *RunRepo) LastFullRun(ctx context.Context) (verify.Run, bool, error) {
	run, err := scanRun(r.pool.QueryRow(ctx, `SELECT `+runColumns+` FROM runs
		WHERE scope_book_id IS NULL AND NOT incremental ORDER BY started_at DESC, id DESC LIMIT 1`))
	if errors.Is(err, pgx.ErrNoRows) {
		return verify.Run{}, false, nil
	}
	if err != nil {
		return verify.Run{}, false, fmt.Errorf("store: last full run: %w", err)
	}
	return run, true, nil
}

// LockRunner takes a session advisory lock on a connection of its own, which
// stays out of the pool until release.
func (r *RunRepo) LockRunner(ctx context.Context) (func(), bool, error) {
	return tryAdvisoryLock(ctx, r.pool, runnerLockID, "runner")
}

// tryAdvisoryLock takes a session advisory lock without waiting. The
// connection that holds it stays out of the pool until release.
func tryAdvisoryLock(ctx context.Context, pool *pgxpool.Pool, id int64, name string) (func(), bool, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("store: acquire the %s lock connection: %w", name, err)
	}
	var ok bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", id).Scan(&ok); err != nil {
		conn.Release()
		return nil, false, fmt.Errorf("store: take the %s lock: %w", name, err)
	}
	if !ok {
		conn.Release()
		return nil, false, nil
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := conn.Exec(ctx, "SELECT pg_advisory_unlock($1)", id); err != nil {
				// Without the unlock the lock would live as long as this
				// connection, so close it instead of returning it to the pool.
				_ = conn.Conn().Close(ctx)
			}
			conn.Release()
		})
	}, true, nil
}

// Evidence returns a checked book's sealed package as stored. Callers check
// it with evidence.Open. ErrNoSuchRun when there is none.
func (r *RunRepo) Evidence(ctx context.Context, runID int64, bookID int) ([]byte, string, error) {
	var (
		raw  string
		seal string
	)
	err := r.pool.QueryRow(ctx, "SELECT package::text, seal FROM evidence WHERE run_id = $1 AND book_id = $2", runID, bookID).Scan(&raw, &seal)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", fmt.Errorf("store: run %d book %d evidence: %w", runID, bookID, verify.ErrNoSuchRun)
	}
	if err != nil {
		return nil, "", fmt.Errorf("store: run %d book %d evidence: %w", runID, bookID, err)
	}
	return []byte(raw), seal, nil
}
