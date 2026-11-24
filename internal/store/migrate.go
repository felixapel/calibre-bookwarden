package store

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// migrationLockID is an arbitrary constant ("bookward" in ASCII) that
// serialises migrations between bookwarden instances.
const migrationLockID int64 = 0x626f6f6b77617264

type migration struct {
	version int
	name    string
	sql     string
}

func migrationVersion(file string) (int, error) {
	num, _, ok := strings.Cut(file, "_")
	v, err := strconv.Atoi(num)
	if !ok || err != nil || v <= 0 {
		return 0, fmt.Errorf("store: migration file %q must be named NNNN_name.sql", file)
	}
	return v, nil
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return nil, fmt.Errorf("store: read migrations: %w", err)
	}
	var out []migration
	for _, e := range entries {
		v, err := migrationVersion(e.Name())
		if err != nil {
			return nil, err
		}
		body, err := migrationFiles.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, fmt.Errorf("store: read %s: %w", e.Name(), err)
		}
		out = append(out, migration{version: v, name: e.Name(), sql: string(body)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	for i := 1; i < len(out); i++ {
		if out[i].version == out[i-1].version {
			return nil, fmt.Errorf("store: two migrations share version %d", out[i].version)
		}
	}
	return out, nil
}

const (
	defaultMigrateLockWait  = 2 * time.Minute
	defaultLockPoll         = time.Second
	defaultLockLogEvery     = 10 * time.Second
	defaultLockTimeout      = 10 * time.Second
	defaultLockRetryBackoff = 500 * time.Millisecond
	// lockRetries is how often a migration is retried after a lock timeout,
	// so it runs at most lockRetries+1 times.
	lockRetries = 3
	// sqlLockNotAvailable is the SQLSTATE lock_timeout raises.
	sqlLockNotAvailable = "55P03"
)

func orDefault(d, def time.Duration) time.Duration {
	if d <= 0 {
		return def
	}
	return d
}

func (s *Store) logger() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}

// Migrate applies pending migrations in order, each in its own transaction,
// while holding a session advisory lock so concurrent instances wait their
// turn, for at most MigrateLockWait. It refuses a database that a newer
// bookwarden has migrated.
func (s *Store) Migrate(ctx context.Context) error {
	ms, err := loadMigrations()
	if err != nil {
		return err
	}
	return s.applyMigrations(ctx, ms)
}

// pidList renders backend pids as "12,34", or "unknown" for none.
func pidList(pids []int) string {
	if len(pids) == 0 {
		return "unknown"
	}
	parts := make([]string, len(pids))
	for i, p := range pids {
		parts[i] = strconv.Itoa(p)
	}
	return strings.Join(parts, ",")
}

// lockHolders lists the backends holding the migration advisory lock.
func lockHolders(ctx context.Context, conn *pgxpool.Conn) ([]int, error) {
	rows, err := conn.Query(ctx, `SELECT pid FROM pg_locks
		WHERE locktype = 'advisory' AND granted AND objsubid = 1
		  AND database = (SELECT oid FROM pg_database WHERE datname = current_database())
		  AND classid = (($1::bigint >> 32) & 4294967295)::bigint::oid
		  AND objid = ($1::bigint & 4294967295)::bigint::oid
		ORDER BY pid`, migrationLockID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[int])
}

// takeMigrationLock polls pg_try_advisory_lock until it gets the lock, the
// wait is over or ctx ends. It logs the holders about every lockLogEvery.
func (s *Store) takeMigrationLock(ctx context.Context, conn *pgxpool.Conn) error {
	wait := orDefault(s.MigrateLockWait, defaultMigrateLockWait)
	poll := orDefault(s.lockPoll, defaultLockPoll)
	every := orDefault(s.lockLogEvery, defaultLockLogEvery)
	start := time.Now()
	lastLog := start
	for {
		var got bool
		if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", migrationLockID).Scan(&got); err != nil {
			return fmt.Errorf("store: take migration lock: %w", err)
		}
		if got {
			return nil
		}
		waited := time.Since(start)
		holders, herr := lockHolders(ctx, conn)
		if herr != nil {
			holders = nil // diagnostics only
		}
		if waited >= wait {
			return fmt.Errorf("store: migration lock still held by pid %s after %s; another bookwarden is migrating or a session holds it", pidList(holders), wait)
		}
		if time.Since(lastLog) >= every || lastLog.Equal(start) {
			s.logger().Info("waiting for the migration lock", "pid", pidList(holders), "waited", waited.Round(time.Millisecond).String())
			lastLog = time.Now()
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("store: take migration lock: %w", ctx.Err())
		case <-time.After(poll):
		}
	}
}

func (s *Store) applyMigrations(ctx context.Context, ms []migration) error {
	conn, err := s.Pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("store: acquire connection: %w", err)
	}
	defer conn.Release()
	if err := s.takeMigrationLock(ctx, conn); err != nil {
		return err
	}
	defer func() {
		// Unlock even if ctx was cancelled: the lock would otherwise live as
		// long as this pooled connection.
		unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, _ = conn.Exec(unlockCtx, "SELECT pg_advisory_unlock($1)", migrationLockID)
	}()
	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    integer PRIMARY KEY,
		name       text NOT NULL,
		applied_at timestamptz NOT NULL DEFAULT now()
	)`); err != nil {
		return fmt.Errorf("store: create schema_migrations: %w", err)
	}
	rows, err := conn.Query(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return fmt.Errorf("store: read applied migrations: %w", err)
	}
	applied := map[int]bool{}
	newest := 0
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return fmt.Errorf("store: scan applied migration: %w", err)
		}
		applied[v] = true
		newest = max(newest, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: read applied migrations: %w", err)
	}
	if len(ms) > 0 && newest > ms[len(ms)-1].version {
		return fmt.Errorf("store: database schema is at version %d but this binary knows up to %d; a newer bookwarden migrated this database and an older binary must not run against it",
			newest, ms[len(ms)-1].version)
	}
	for _, m := range ms {
		if applied[m.version] {
			continue
		}
		if err := s.applyOne(ctx, conn, m); err != nil {
			return err
		}
	}
	return nil
}

// applyOne runs one migration with a lock_timeout, so a migration that needs a
// strong table lock fails fast instead of queueing behind a long reader and
// blocking every later query. A lock timeout is retried a few times.
func (s *Store) applyOne(ctx context.Context, conn *pgxpool.Conn, m migration) error {
	timeout := orDefault(s.lockTimeout, defaultLockTimeout)
	backoff := orDefault(s.lockRetryBackoff, defaultLockRetryBackoff)
	for attempt := 0; ; attempt++ {
		err := s.tryMigration(ctx, conn, m, timeout)
		var pgErr *pgconn.PgError
		if err == nil || !errors.As(err, &pgErr) || pgErr.Code != sqlLockNotAvailable || attempt >= lockRetries {
			return err
		}
		s.logger().Warn("migration hit a lock timeout; retrying", "migration", m.name,
			"attempt", attempt+1, "of", lockRetries+1, "lock_timeout", timeout.String())
		select {
		case <-ctx.Done():
			return fmt.Errorf("store: apply %s: %w", m.name, ctx.Err())
		case <-time.After(backoff * time.Duration(attempt+1)):
		}
	}
}

func (s *Store) tryMigration(ctx context.Context, conn *pgxpool.Conn, m migration, timeout time.Duration) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: begin %s: %w", m.name, err)
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL lock_timeout = %d", timeout.Milliseconds())); err != nil {
		_ = tx.Rollback(ctx)
		return fmt.Errorf("store: set lock_timeout for %s: %w", m.name, err)
	}
	if _, err := tx.Exec(ctx, m.sql); err != nil {
		_ = tx.Rollback(ctx)
		return fmt.Errorf("store: apply %s: %w", m.name, err)
	}
	if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations (version, name) VALUES ($1, $2)", m.version, m.name); err != nil {
		_ = tx.Rollback(ctx)
		return fmt.Errorf("store: record %s: %w", m.name, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: commit %s: %w", m.name, err)
	}
	return nil
}
