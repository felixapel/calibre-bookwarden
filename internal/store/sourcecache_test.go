package store

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

func sourceCache(t *testing.T) (*SourceCache, *Store) {
	t.Helper()
	s := openTest(t)
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return NewSourceCache(s, 1), s
}

// TestSourceCacheMappingVersion: a row mapped by other rules is a miss and
// is deleted, so a parser change never replays old records.
func TestSourceCacheMappingVersion(t *testing.T) {
	ctx := context.Background()
	old, s := sourceCache(t)
	const isbn = "9783551551672"
	recs := []evidence.SourceRecord{{ID: "1", Title: "T", ISBNs: []string{isbn}}}
	if err := old.Put(ctx, evidence.SourceDNB, isbn, evidence.SourceOK, recs, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, _, hit, _, err := old.Get(ctx, evidence.SourceDNB, isbn); err != nil || !hit {
		t.Fatalf("same mapping: hit=%v err=%v", hit, err)
	}
	if _, _, hit, _, err := NewSourceCache(s, 2).Get(ctx, evidence.SourceDNB, isbn); err != nil || hit {
		t.Fatalf("newer mapping: hit=%v err=%v", hit, err)
	}
	if n := countRows(t, s); n != 0 {
		t.Fatalf("the old row was kept: %d rows", n)
	}
}

func TestSourceCache(t *testing.T) {
	ctx := context.Background()
	c, s := sourceCache(t)
	const isbn = "9783551551672"
	recs := []evidence.SourceRecord{{ID: "958577617", Title: "Harry Potter", Authors: []string{"J. K. Rowling"}, ISBNs: []string{isbn}}}

	if _, _, hit, _, err := c.Get(ctx, evidence.SourceDNB, isbn); err != nil || hit {
		t.Fatalf("empty cache: hit=%v err=%v", hit, err)
	}
	if err := c.Put(ctx, evidence.SourceDNB, isbn, evidence.SourceOK, recs, 720*time.Hour); err != nil {
		t.Fatal(err)
	}
	outcome, got, hit, stale, err := c.Get(ctx, evidence.SourceDNB, isbn)
	if err != nil || !hit || stale || outcome != evidence.SourceOK || !reflect.DeepEqual(got, recs) {
		t.Fatalf("Get = %q %+v %v %v", outcome, got, hit, err)
	}
	if _, _, hit, _, _ := c.Get(ctx, evidence.SourceBNE, isbn); hit {
		t.Fatal("another source hit")
	}

	if err := c.Put(ctx, evidence.SourceDNB, isbn, evidence.SourceNoMatch, nil, 168*time.Hour); err != nil {
		t.Fatal(err)
	}
	if outcome, got, hit, _, err := c.Get(ctx, evidence.SourceDNB, isbn); err != nil || !hit || outcome != evidence.SourceNoMatch || len(got) != 0 {
		t.Fatalf("after upsert: %q %+v %v %v", outcome, got, hit, err)
	}

	if _, err := s.Pool.Exec(ctx, `UPDATE source_cache SET expires_at = now() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	// An expired row is still an answer, marked stale (ADR-044), and Prune
	// keeps it until it is staleMax past its expiry.
	if outcome, _, hit, stale, err := c.Get(ctx, evidence.SourceDNB, isbn); err != nil || !hit || !stale || outcome != evidence.SourceNoMatch {
		t.Fatalf("expired row: %q hit=%v stale=%v err=%v", outcome, hit, stale, err)
	}
	if err := c.Prune(ctx); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, s); n != 1 {
		t.Fatalf("Prune dropped a recently expired row: %d rows", n)
	}
	if _, err := s.Pool.Exec(ctx, `DELETE FROM source_cache`); err != nil {
		t.Fatal(err)
	}

	for name, corrupt := range map[string]string{
		"not_records":  `{"a":1}`,
		"unknown_key":  `[{"id":"1","isbns":["9783551551672"],"extra":true}]`,
		"other_isbn":   `[{"id":"1","isbns":["9780306406157"]}]`,
		"ok_but_empty": `[]`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := c.Put(ctx, evidence.SourceDNB, isbn, evidence.SourceOK, recs, time.Hour); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Pool.Exec(ctx, `UPDATE source_cache SET records = $1::jsonb`, corrupt); err != nil {
				t.Fatal(err)
			}
			if _, _, hit, _, err := c.Get(ctx, evidence.SourceDNB, isbn); err != nil || hit {
				t.Fatalf("corrupt row: hit=%v err=%v", hit, err)
			}
			if n := countRows(t, s); n != 0 {
				t.Fatalf("corrupt row kept: %d rows", n)
			}
		})
	}
}

func TestSourceCacheRejectsFailures(t *testing.T) {
	ctx := context.Background()
	c, s := sourceCache(t)
	for _, o := range []string{evidence.SourceUnavailable, evidence.SourceNotConfigured, evidence.SourceNotQueried} {
		if err := c.Put(ctx, evidence.SourceDNB, "9783551551672", o, nil, time.Hour); err == nil {
			t.Fatalf("Put(%s) succeeded", o)
		}
	}
	if _, err := s.Pool.Exec(ctx, `INSERT INTO source_cache (source, isbn, outcome, records, fetched_at, expires_at)
		VALUES ('dnb', '9783551551672', 'unavailable', '[]', now(), now() + interval '1 hour')`); err == nil {
		t.Fatal("the CHECK let unavailable in")
	}
	if n := countRows(t, s); n != 0 {
		t.Fatalf("%d rows", n)
	}
}

// TestSourceCachePruneKeepsRecentlyExpiredRows: Prune deletes only rows
// expired for longer than staleMax (60 days).
func TestSourceCachePruneKeepsRecentlyExpiredRows(t *testing.T) {
	ctx := context.Background()
	c, s := sourceCache(t)
	for _, isbn := range []string{"9783551551672", "9780306406157"} {
		if err := c.Put(ctx, evidence.SourceDNB, isbn, evidence.SourceNoMatch, nil, time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE source_cache SET expires_at = now() - CASE isbn
		WHEN '9783551551672' THEN interval '59 days' ELSE interval '61 days' END`); err != nil {
		t.Fatal(err)
	}
	if err := c.Prune(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, hit, stale, err := c.Get(ctx, evidence.SourceDNB, "9783551551672"); err != nil || !hit || !stale {
		t.Fatalf("59 days past expiry: hit=%v stale=%v err=%v; want a stale hit", hit, stale, err)
	}
	if _, _, hit, _, err := c.Get(ctx, evidence.SourceDNB, "9780306406157"); err != nil || hit {
		t.Fatalf("61 days past expiry: hit=%v err=%v; want it pruned", hit, err)
	}
}

// TestSourceCacheValidatesStaleRows: a stale row from another mapping
// version, or one that no longer validates, is a miss and is deleted, like a
// fresh one; a row past staleMax is a miss even if Prune didn't run.
func TestSourceCacheValidatesStaleRows(t *testing.T) {
	ctx := context.Background()
	old, s := sourceCache(t)
	const isbn = "9783551551672"
	recs := []evidence.SourceRecord{{ID: "1", Title: "T", ISBNs: []string{isbn}}}
	expire := func(age string) {
		t.Helper()
		if _, err := s.Pool.Exec(ctx, `UPDATE source_cache SET expires_at = now() - $1::interval`, age); err != nil {
			t.Fatal(err)
		}
	}
	if err := old.Put(ctx, evidence.SourceDNB, isbn, evidence.SourceOK, recs, time.Hour); err != nil {
		t.Fatal(err)
	}
	expire("1 day")
	if _, _, hit, _, err := NewSourceCache(s, 2).Get(ctx, evidence.SourceDNB, isbn); err != nil || hit {
		t.Fatalf("stale row of another mapping: hit=%v err=%v", hit, err)
	}
	if n := countRows(t, s); n != 0 {
		t.Fatalf("stale row of another mapping kept: %d rows", n)
	}
	if err := old.Put(ctx, evidence.SourceDNB, isbn, evidence.SourceOK, recs, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE source_cache SET records = '[{"id":"1","isbns":["9780306406157"]}]'::jsonb`); err != nil {
		t.Fatal(err)
	}
	expire("1 day")
	if _, _, hit, _, err := old.Get(ctx, evidence.SourceDNB, isbn); err != nil || hit {
		t.Fatalf("stale corrupt row: hit=%v err=%v", hit, err)
	}
	if err := old.Put(ctx, evidence.SourceDNB, isbn, evidence.SourceOK, recs, time.Hour); err != nil {
		t.Fatal(err)
	}
	expire("61 days")
	if _, _, hit, _, err := old.Get(ctx, evidence.SourceDNB, isbn); err != nil || hit {
		t.Fatalf("row past staleMax: hit=%v err=%v", hit, err)
	}
}

func countRows(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.Pool.QueryRow(context.Background(), `SELECT count(*) FROM source_cache`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
