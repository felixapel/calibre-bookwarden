// Package store owns bookwarden's Postgres connection, schema and
// repositories.
package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Store wraps the connection pool every repository shares.
type Store struct {
	Pool *pgxpool.Pool

	// MigrateLockWait bounds how long Migrate waits for the migration lock;
	// zero means two minutes.
	MigrateLockWait time.Duration
	// Logger receives migration progress; nil means slog.Default.
	Logger           *slog.Logger
	lockPoll         time.Duration
	lockLogEvery     time.Duration
	lockTimeout      time.Duration
	lockRetryBackoff time.Duration
}

// errInvalidURL replaces pgx's parse errors, which may quote the URL and with
// it the password.
var errInvalidURL = errors.New("store: invalid database URL")

// Open connects to Postgres and pings it once.
func Open(ctx context.Context, databaseURL string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, errInvalidURL
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("store: create pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: ping: %w", err)
	}
	return &Store{Pool: pool}, nil
}

// OpenWithRetry keeps trying until Postgres answers or ctx ends. On Unraid the
// database container may start after bookwarden. A malformed URL won't get
// better by waiting, so it fails at once.
func OpenWithRetry(ctx context.Context, databaseURL string, logger *slog.Logger) (*Store, error) {
	delay := time.Second
	for {
		s, err := Open(ctx, databaseURL)
		if err == nil {
			s.Logger = logger
			return s, nil
		}
		if errors.Is(err, errInvalidURL) {
			return nil, err
		}
		logger.Warn("database not reachable yet; retrying", "error", err, "retry_in", delay.String())
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, fmt.Errorf("store: giving up on the database: %w", ctx.Err())
		case <-timer.C:
		}
		if delay < 30*time.Second {
			delay *= 2
		}
	}
}

// Close releases every connection.
func (s *Store) Close() { s.Pool.Close() }

// Ping checks that Postgres answers; /healthz uses it.
func (s *Store) Ping(ctx context.Context) error {
	if err := s.Pool.Ping(ctx); err != nil {
		return fmt.Errorf("store: ping: %w", err)
	}
	return nil
}

// OpenReadOnly opens a pool whose every transaction is read-only, for
// commands that must never write, such as eval. Postgres rejects a write with
// SQLSTATE 25006.
func OpenReadOnly(ctx context.Context, databaseURL string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, errInvalidURL
	}
	cfg.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("store: create pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: ping: %w", err)
	}
	return &Store{Pool: pool}, nil
}
