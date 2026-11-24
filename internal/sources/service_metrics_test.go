package sources_test

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/sources"
)

type recorder struct {
	mu    sync.Mutex
	got   []string
	timed []string
}

func (r *recorder) SourceRequest(source string, _ time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.timed = append(r.timed, source)
}

func (r *recorder) requests() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.timed)
}

func (r *recorder) SourceLookup(source, outcome string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, source+"/"+outcome)
}

func (r *recorder) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.got)
}

// TestServiceCountsLookups: every lookup is counted once by its outcome: a
// live answer by the source's outcome, a cache read as cache_hit, and a
// source whose breaker is open as breaker_open.
func TestServiceCountsLookups(t *testing.T) {
	a := &fakeAdapter{name: evidence.SourceDNB, outcomes: map[string]string{
		"9780306406157": evidence.SourceOK,
		"9780306406158": evidence.SourceNoMatch,
	}}
	rec := &recorder{}
	s := sources.NewService(&fakeFetcher{}, newMemCache(), a)
	s.Metrics = rec
	look := func(isbn string) {
		s.Lookup(t.Context(), 1, []sources.Seed{{ISBN: isbn, Class: "file"}})
	}
	look("9780306406157")
	look("9780306406157") // cached
	look("9780306406158")
	for i := range 5 {
		look(fmt.Sprintf("bad%d", i)) // unavailable, five times: the breaker opens
	}
	look("after")

	want := []string{
		"dnb/ok", "dnb/cache_hit", "dnb/no_match",
		"dnb/unavailable", "dnb/unavailable", "dnb/unavailable", "dnb/unavailable", "dnb/unavailable",
		"dnb/breaker_open",
	}
	if got := rec.seen(); !slices.Equal(got, want) {
		t.Errorf("counted %v, want %v", got, want)
	}
}

func TestServiceWithoutMetricsStillLooksUp(t *testing.T) {
	a := &fakeAdapter{name: evidence.SourceDNB, fallback: evidence.SourceOK}
	s := sources.NewService(&fakeFetcher{}, newMemCache(), a)
	if got := s.Lookup(t.Context(), 1, []sources.Seed{{ISBN: "9780306406157", Class: "file"}}); len(got) != 1 {
		t.Fatalf("lookups = %v", got)
	}
}

// TestServiceCountsStaleAnswersOnce: a stale answer served is counted once,
// as stale_hit, whether the budget was used up, the refresh failed or the
// breaker was open; a refresh that answered is counted by its outcome.
func TestServiceCountsStaleAnswersOnce(t *testing.T) {
	c := newMemCache()
	a := &fakeAdapter{name: evidence.SourceDNB, outcomes: map[string]string{"fresh": evidence.SourceNoMatch}} // others: unavailable
	rec := &recorder{}
	s := sources.NewService(&fakeFetcher{}, c, a)
	s.Metrics = rec
	for _, isbn := range []string{"fresh", "failed", "over"} {
		c.putStale(evidence.SourceDNB, isbn, evidence.SourceOK)
	}
	s.BeginRun(t.Context(), 1, 2)
	for _, isbn := range []string{"fresh", "failed", "over"} {
		s.Lookup(t.Context(), 1, []sources.Seed{{ISBN: isbn, Class: "file"}})
	}
	want := []string{"dnb/no_match", "dnb/stale_hit", "dnb/stale_hit"}
	if got := rec.seen(); !slices.Equal(got, want) {
		t.Errorf("counted %v, want %v", got, want)
	}
}

// TestServiceCountsLookupsForTheRun: the run's counts, by source and
// outcome, cover fresh hits, stale hits and live answers; a take starts
// again from zero.
func TestServiceCountsLookupsForTheRun(t *testing.T) {
	c := newMemCache()
	a := &fakeAdapter{name: evidence.SourceDNB, outcomes: map[string]string{
		"9780306406157": evidence.SourceOK,
		"9780306406158": evidence.SourceNoMatch,
	}}
	rec := &recorder{}
	s := sources.NewService(&fakeFetcher{}, c, a)
	s.Metrics = rec
	c.putStale(evidence.SourceDNB, "stale", evidence.SourceOK)
	s.BeginRun(t.Context(), 1, 0) // no refresh budget: the stale row is served
	for _, isbn := range []string{"9780306406157", "9780306406157", "9780306406158", "stale"} {
		s.Lookup(t.Context(), 1, []sources.Seed{{ISBN: isbn, Class: "file"}})
	}
	want := map[string]map[string]int{evidence.SourceDNB: {"ok": 1, "cache_hit": 1, "no_match": 1, "stale_hit": 1}}
	if got := s.TakeLookupCounts(); !reflect.DeepEqual(got, want) {
		t.Errorf("run counts = %v, want %v", got, want)
	}
	if got := s.TakeLookupCounts(); len(got) != 0 {
		t.Errorf("a second take = %v, want nothing", got)
	}
	// Only the two live lookups were timed.
	if n := rec.requests(); n != 2 {
		t.Errorf("%d timed requests, want 2", n)
	}
}

// TestACancelledLookupIsNotTimed: a lookup cut short by a stop says nothing
// about how fast the source answers.
func TestACancelledLookupIsNotTimed(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	rec := &recorder{}
	s := sources.NewService(&fakeFetcher{}, newMemCache(), &cancellingAdapter{fakeAdapter: &fakeAdapter{name: evidence.SourceDNB}, cancel: cancel})
	s.Metrics = rec
	s.Lookup(ctx, 1, []sources.Seed{{ISBN: "9780306406157", Class: "file"}})
	if n := rec.requests(); n != 0 {
		t.Errorf("%d timed requests for a cancelled lookup, want 0", n)
	}
}
