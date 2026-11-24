package store

import (
	"context"
	"fmt"
)

// SelectOne runs SELECT 1, the database probe of /readyz.
func (s *Store) SelectOne(ctx context.Context) error {
	var one int
	if err := s.Pool.QueryRow(ctx, "SELECT 1").Scan(&one); err != nil {
		return fmt.Errorf("store: select 1: %w", err)
	}
	return nil
}

// MigrationsCurrent returns an error unless the database is at this binary's
// latest migration. Behind means Migrate hasn't finished; ahead means a newer
// binary migrated the database.
func (s *Store) MigrationsCurrent(ctx context.Context) error {
	ms, err := loadMigrations()
	if err != nil {
		return err
	}
	var applied *int
	if err := s.Pool.QueryRow(ctx, "SELECT max(version) FROM schema_migrations").Scan(&applied); err != nil {
		return fmt.Errorf("store: read migrations: %w", err)
	}
	want := ms[len(ms)-1].version
	have := 0
	if applied != nil {
		have = *applied
	}
	switch {
	case have < want:
		return fmt.Errorf("store: migrations behind: at %d, binary expects %d", have, want)
	case have > want:
		return fmt.Errorf("store: migrations ahead: at %d, binary expects %d", have, want)
	}
	return nil
}
