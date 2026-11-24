package store

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/testutil/pgtest"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), pgtest.URL(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

func TestMigrateAppliesEveryMigrationOnce(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	for run := 1; run <= 2; run++ {
		if err := s.Migrate(ctx); err != nil {
			t.Fatalf("Migrate run %d: %v", run, err)
		}
	}
	ms, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	var applied int
	if err := s.Pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != len(ms) {
		t.Fatalf("schema_migrations has %d rows, want %d", applied, len(ms))
	}
	for _, table := range []string{"users", "sessions", "setup"} {
		var exists bool
		if err := s.Pool.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", table).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Errorf("table %s is missing", table)
		}
	}
}

func TestConcurrentMigrateIsSerialised(t *testing.T) {
	ctx := context.Background()
	dbURL := pgtest.URL(t)
	stores := make([]*Store, 4)
	for i := range stores {
		s, err := Open(ctx, dbURL)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(s.Close)
		stores[i] = s
	}
	errs := make(chan error, len(stores))
	for _, s := range stores {
		go func() { errs <- s.Migrate(ctx) }()
	}
	for range stores {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent Migrate: %v", err)
		}
	}
	var duplicates int
	err := stores[0].Pool.QueryRow(ctx,
		"SELECT count(*) FROM (SELECT version FROM schema_migrations GROUP BY version HAVING count(*) > 1) d",
	).Scan(&duplicates)
	if err != nil {
		t.Fatal(err)
	}
	if duplicates != 0 {
		t.Fatalf("%d migrations were applied more than once", duplicates)
	}
}

func TestOpenWithRetry(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		want    error
		maxTime time.Duration
	}{
		{"database never answers", "postgres://nobody:s3cret-pw@127.0.0.1:1/none?connect_timeout=1", context.DeadlineExceeded, 5 * time.Second},
		{"malformed URL fails at once", "postgres://nobody:s3cret-pw@127.0.0.1:1/none?sslmode=bogus", errInvalidURL, time.Second},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
			defer cancel()
			start := time.Now()
			_, err := OpenWithRetry(ctx, tc.url, slog.New(slog.DiscardHandler))
			if !errors.Is(err, tc.want) {
				t.Fatalf("OpenWithRetry = %v, want %v", err, tc.want)
			}
			if strings.Contains(err.Error(), "s3cret-pw") {
				t.Fatalf("error leaks the password: %v", err)
			}
			if elapsed := time.Since(start); elapsed > tc.maxTime {
				t.Fatalf("OpenWithRetry took %s, want at most %s", elapsed, tc.maxTime)
			}
		})
	}
}

func TestLoadMigrationsRejectsBadNames(t *testing.T) {
	tests := []struct {
		name    string
		file    string
		wantErr bool
	}{
		{"numbered", "0002_runs.sql", false},
		{"missing number", "runs.sql", true},
		{"zero", "0000_x.sql", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := migrationVersion(tc.file)
			if (err != nil) != tc.wantErr {
				t.Fatalf("migrationVersion(%q) error = %v, wantErr %v", tc.file, err, tc.wantErr)
			}
		})
	}
}
