package store

import (
	"context"
	"strings"
	"testing"
)

func TestReadyChecks(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)

	if err := s.SelectOne(ctx); err != nil {
		t.Fatalf("SelectOne: %v", err)
	}
	// Before Migrate, the schema table doesn't exist.
	if err := s.MigrationsCurrent(ctx); err == nil || !strings.Contains(err.Error(), "migrations") {
		t.Fatalf("MigrationsCurrent on an empty database = %v, want a migrations error", err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.MigrationsCurrent(ctx); err != nil {
		t.Fatalf("MigrationsCurrent after Migrate: %v", err)
	}
	// Behind: the newest migration is missing.
	ms, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	newest := ms[len(ms)-1].version
	if _, err := s.Pool.Exec(ctx, "DELETE FROM schema_migrations WHERE version = $1", newest); err != nil {
		t.Fatal(err)
	}
	err = s.MigrationsCurrent(ctx)
	if err == nil || !strings.Contains(err.Error(), "behind") {
		t.Fatalf("MigrationsCurrent one migration behind = %v, want a behind error", err)
	}
	// Ahead: a newer binary migrated the database.
	if _, err := s.Pool.Exec(ctx, "INSERT INTO schema_migrations (version, name) VALUES ($1, 'current'), ($2, 'later')", newest, newest+1); err != nil {
		t.Fatal(err)
	}
	err = s.MigrationsCurrent(ctx)
	if err == nil || !strings.Contains(err.Error(), "ahead") {
		t.Fatalf("MigrationsCurrent with a newer schema = %v, want an ahead error", err)
	}
}
