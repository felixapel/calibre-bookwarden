package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

// SourceCache keeps validated source records in Postgres. It implements
// sources.Cache.
type SourceCache struct {
	pool    *pgxpool.Pool
	mapping int
}

// NewSourceCache returns the cache over s for records built by the given
// mapping version; rows from other versions are misses.
func NewSourceCache(s *Store, mapping int) *SourceCache {
	return &SourceCache{pool: s.Pool, mapping: mapping}
}

// staleMax is how long past its expiry a row is kept as a stale answer
// before Prune deletes it (ADR-044).
const staleMax = 60 * 24 * time.Hour

// Get returns the cached answer; stale reports that it is past its expiry.
// A row more than staleMax past its expiry is a miss even before Prune runs. A
// row that no longer validates, or was built by another mapping version, is
// deleted and reported as a miss.
func (c *SourceCache) Get(ctx context.Context, source, isbn string) (outcome string, records []evidence.SourceRecord, hit, stale bool, err error) {
	var raw []byte
	var mapping int
	err = c.pool.QueryRow(ctx, `SELECT outcome, records, mapping_version, expires_at <= now() FROM source_cache
		WHERE source = $1 AND isbn = $2 AND expires_at > now() - $3::interval`, source, isbn, staleMax).Scan(&outcome, &raw, &mapping, &stale)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, false, false, nil
	}
	if err != nil {
		return "", nil, false, false, fmt.Errorf("store: source cache get: %w", err)
	}
	records, ok := cachedRecords(outcome, raw, isbn)
	if !ok || mapping != c.mapping {
		if _, err := c.pool.Exec(ctx, `DELETE FROM source_cache WHERE source = $1 AND isbn = $2`, source, isbn); err != nil {
			return "", nil, false, false, fmt.Errorf("store: source cache drop invalid row: %w", err)
		}
		return "", nil, false, false, nil
	}
	return outcome, records, true, stale, nil
}

// cachedRecords re-validates a row: ok carries records, no_match none, and
// every record names the queried ISBN.
func cachedRecords(outcome string, raw []byte, isbn string) ([]evidence.SourceRecord, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var records []evidence.SourceRecord
	if err := dec.Decode(&records); err != nil {
		return nil, false
	}
	if (outcome == evidence.SourceOK) != (len(records) > 0) {
		return nil, false
	}
	for _, r := range records {
		if !slices.Contains(r.ISBNs, isbn) {
			return nil, false
		}
	}
	return records, true
}

// Put stores an ok or no_match answer for ttl, replacing any earlier one.
func (c *SourceCache) Put(ctx context.Context, source, isbn, outcome string, records []evidence.SourceRecord, ttl time.Duration) error {
	if outcome != evidence.SourceOK && outcome != evidence.SourceNoMatch {
		return fmt.Errorf("store: source cache: outcome %q is not cacheable", outcome)
	}
	if records == nil {
		records = []evidence.SourceRecord{} // [] rather than null
	}
	raw, err := json.Marshal(records)
	if err != nil {
		return fmt.Errorf("store: source cache encode: %w", err)
	}
	_, err = c.pool.Exec(ctx, `INSERT INTO source_cache (source, isbn, outcome, records, fetched_at, expires_at, mapping_version)
		VALUES ($1, $2, $3, $4, now(), now() + $5::interval, $6)
		ON CONFLICT (source, isbn) DO UPDATE SET outcome = EXCLUDED.outcome, records = EXCLUDED.records,
			fetched_at = EXCLUDED.fetched_at, expires_at = EXCLUDED.expires_at, mapping_version = EXCLUDED.mapping_version`,
		source, isbn, outcome, raw, ttl, c.mapping)
	if err != nil {
		return fmt.Errorf("store: source cache put: %w", err)
	}
	return nil
}

// Prune deletes rows expired for longer than staleMax.
func (c *SourceCache) Prune(ctx context.Context) error {
	if _, err := c.pool.Exec(ctx, `DELETE FROM source_cache WHERE expires_at <= now() - $1::interval`, staleMax); err != nil {
		return fmt.Errorf("store: source cache prune: %w", err)
	}
	return nil
}
