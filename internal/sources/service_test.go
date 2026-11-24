package sources_test

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/sources"
)

// fakeAdapter answers from outcomes, keyed by ISBN, and counts calls.
type fakeAdapter struct {
	name     string
	mu       sync.Mutex
	calls    []string
	outcomes map[string]string // isbn → outcome
	fallback string            // outcome for other ISBNs; "" means unavailable
}

func (a *fakeAdapter) Name() string { return a.name }

func (a *fakeAdapter) Lookup(_ context.Context, _ sources.Fetcher, isbn string) (string, []evidence.SourceRecord) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, isbn)
	o, ok := a.outcomes[isbn]
	if !ok {
		o = cmp.Or(a.fallback, evidence.SourceUnavailable)
	}
	if o == evidence.SourceOK {
		return o, []evidence.SourceRecord{{ID: a.name + "-" + isbn, ISBNs: []string{isbn}}}
	}
	return o, nil
}

func (a *fakeAdapter) callCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.calls)
}

type memCache struct {
	mu     sync.Mutex
	gets   int
	rows   map[string]cached
	puts   []string
	prunes int
}

type cached struct {
	outcome string
	records []evidence.SourceRecord
	ttl     time.Duration
	stale   bool
}

func newMemCache() *memCache { return &memCache{rows: map[string]cached{}} }

func (c *memCache) Get(_ context.Context, source, isbn string) (string, []evidence.SourceRecord, bool, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gets++
	r, ok := c.rows[source+"/"+isbn]
	return r.outcome, slices.Clone(r.records), ok, r.stale, nil
}

// putStale stores a row that is past its expiry.
func (c *memCache) putStale(source, isbn, outcome string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var recs []evidence.SourceRecord
	if outcome == evidence.SourceOK {
		recs = []evidence.SourceRecord{{ID: "cached-" + isbn, ISBNs: []string{isbn}}}
	}
	c.rows[source+"/"+isbn] = cached{outcome: outcome, records: recs, stale: true}
}

func (c *memCache) Put(_ context.Context, source, isbn, outcome string, records []evidence.SourceRecord, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rows[source+"/"+isbn] = cached{outcome: outcome, records: slices.Clone(records), ttl: ttl}
	c.puts = append(c.puts, fmt.Sprintf("%s/%s/%s/%v", source, isbn, outcome, ttl))
	return nil
}

func (c *memCache) Prune(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.prunes++
	return nil
}

func ok(isbns ...string) map[string]string {
	m := map[string]string{}
	for _, i := range isbns {
		m[i] = evidence.SourceOK
	}
	return m
}

func TestSeeds(t *testing.T) {
	p := evidence.Package{
		Calibre: evidence.Record{Identifiers: map[string]string{"ISBN": "9780306406157", "goodreads": "9783551551672"}},
		Formats: []evidence.FormatEvidence{
			{Format: "EPUB", State: evidence.StateOK, Metadata: &evidence.FileMetadata{ISBNs: []string{"0-306-40615-2", "9780306406158"}}},
			{Format: "MOBI", State: evidence.StateCorrupt, Metadata: &evidence.FileMetadata{ISBNs: []string{"9788478884452"}}},
			{Format: "PDF", State: evidence.StateOK, ISBNHints: &evidence.ISBNHints{Method: evidence.HintOCR, State: evidence.HintTimeout, ISBNs: []string{"9783551551672"}}},
			{Format: "EPUB", State: evidence.StateOK, ISBNHints: &evidence.ISBNHints{Method: evidence.HintEPUBText, State: evidence.HintOK, ISBNs: []string{"9780306406157", "9788478884452"}}},
		},
	}
	want := []sources.Seed{
		{ISBN: "9780306406157", Class: "file"}, {ISBN: "9780306406157", Class: "calibre"}, {ISBN: "9783551551672", Class: "ocr"},
		{ISBN: "9780306406157", Class: "epub_text"}, {ISBN: "9788478884452", Class: "epub_text"},
	}
	if got := sources.Seeds(p); !slices.Equal(got, want) {
		t.Fatalf("Seeds = %+v, want %+v", got, want)
	}
	dnb := &fakeAdapter{name: evidence.SourceDNB, outcomes: ok("9780306406157", "9783551551672")}
	s := sources.NewService(&fakeFetcher{}, newMemCache(), dnb)
	got := s.Lookup(t.Context(), 1, sources.Seeds(p))
	if len(got) != 3 || got[0].ISBN != "9780306406157" || !slices.Equal(got[0].Seeds, []string{"file", "calibre", "epub_text"}) ||
		got[1].ISBN != "9783551551672" || !slices.Equal(got[1].Seeds, []string{"ocr"}) || !slices.Equal(got[2].Seeds, []string{"epub_text"}) {
		t.Fatalf("lookups = %+v", got)
	}
	var many []sources.Seed
	for i := range 6 {
		many = append(many, sources.Seed{ISBN: fmt.Sprintf("isbn%d", i), Class: "file"})
	}
	if got := s.Lookup(t.Context(), 1, many); len(got) != 4 || got[3].ISBN != "isbn3" {
		t.Fatalf("cap: %+v", got)
	}
}

func TestServiceCacheRoundTrip(t *testing.T) {
	dnb := &fakeAdapter{name: evidence.SourceDNB, outcomes: ok("a")}
	bne := &fakeAdapter{name: evidence.SourceBNE, outcomes: map[string]string{"a": evidence.SourceNoMatch}}
	cache := newMemCache()
	s := sources.NewService(&fakeFetcher{}, cache, dnb, bne)
	sources.SetRand(s, func() float64 { return 0.5 }) // a TTL factor of exactly 1
	seeds := []sources.Seed{{ISBN: "a", Class: "file"}}
	first := s.Lookup(t.Context(), 1, seeds)
	second := s.Lookup(t.Context(), 1, seeds)
	if dnb.callCount() != 1 || bne.callCount() != 1 {
		t.Fatalf("calls dnb=%d bne=%d", dnb.callCount(), bne.callCount())
	}
	if !slices.Equal(cache.puts, []string{"dnb/a/ok/720h0m0s", "bne/a/no_match/168h0m0s"}) {
		t.Fatalf("puts = %v", cache.puts)
	}
	if fmt.Sprint(first) != fmt.Sprint(second) || first[0].Results[0].Source != evidence.SourceDNB || first[0].Results[1].Outcome != evidence.SourceNoMatch {
		t.Fatalf("first %+v\nsecond %+v", first, second)
	}
}

func TestServiceFailuresNotCached(t *testing.T) {
	dnb := &fakeAdapter{name: evidence.SourceDNB}
	gb := &fakeAdapter{name: evidence.SourceGoogleBooks, outcomes: map[string]string{"a": evidence.SourceNotConfigured}}
	cache := newMemCache()
	s := sources.NewService(&fakeFetcher{}, cache, dnb, gb)
	got := s.Lookup(t.Context(), 1, []sources.Seed{{ISBN: "a", Class: "file"}})
	if len(cache.puts) != 0 || got[0].Results[0].Outcome != evidence.SourceUnavailable || got[0].Results[1].Outcome != evidence.SourceNotConfigured {
		t.Fatalf("puts %v, results %+v", cache.puts, got)
	}
}

func TestServiceBreaker(t *testing.T) {
	dnb := &fakeAdapter{name: evidence.SourceDNB, outcomes: ok("good")}
	bne := &fakeAdapter{name: evidence.SourceBNE, fallback: evidence.SourceNoMatch}
	s := sources.NewService(&fakeFetcher{}, newMemCache(), dnb, bne)
	look := func(run int64, isbn string) []evidence.Lookup {
		return s.Lookup(t.Context(), run, []sources.Seed{{ISBN: isbn, Class: "file"}})
	}
	for i := range 4 {
		look(1, fmt.Sprintf("bad%d", i))
	}
	look(1, "good") // DNB ok resets its count
	for i := range 5 {
		look(1, fmt.Sprintf("worse%d", i))
	}
	before := dnb.callCount()
	got := look(1, "after")
	if dnb.callCount() != before || got[0].Results[0].Outcome != evidence.SourceUnavailable {
		t.Fatalf("breaker didn't open: calls %d→%d, %+v", before, dnb.callCount(), got)
	}
	if bne.callCount() != 11 || got[0].Results[1].Outcome != evidence.SourceNoMatch {
		t.Fatalf("BNE called %d times, want 11: DNB's breaker must not stop it", bne.callCount())
	}
	s.BeginRun(t.Context(), 2, sources.RefreshAll)
	look(2, "again")
	if dnb.callCount() != before+1 {
		t.Fatalf("a new run didn't close the breaker")
	}
}

// probingAdapter is a fakeAdapter whose probe passes or fails.
type probingAdapter struct {
	*fakeAdapter
	pass   bool
	probes int
}

func (a *probingAdapter) Probe(context.Context, sources.Fetcher) bool { a.probes++; return a.pass }

// TestServiceProbeOpensBreaker: a source that fails its probe at the start
// of a run is unavailable for the whole run: never asked, nothing cached,
// cached answers still served. Other sources are asked as usual.
func TestServiceProbeOpensBreaker(t *testing.T) {
	gb := &probingAdapter{fakeAdapter: &fakeAdapter{name: evidence.SourceGoogleBooks, fallback: evidence.SourceNoMatch}}
	ol := &fakeAdapter{name: evidence.SourceOpenLibrary, fallback: evidence.SourceNoMatch}
	c := newMemCache()
	s := sources.NewService(&fakeFetcher{}, c, ol, gb)
	if err := c.Put(t.Context(), evidence.SourceGoogleBooks, "cached", evidence.SourceOK, []evidence.SourceRecord{{ID: "c", ISBNs: []string{"cached"}}}, time.Hour); err != nil {
		t.Fatal(err)
	}
	// A no_match cached while Google was already broken is no answer either.
	if err := c.Put(t.Context(), evidence.SourceGoogleBooks, "stale", evidence.SourceNoMatch, nil, time.Hour); err != nil {
		t.Fatal(err)
	}
	s.BeginRun(t.Context(), 1, sources.RefreshAll)
	got := s.Lookup(t.Context(), 1, []sources.Seed{{ISBN: "fresh", Class: "file"}, {ISBN: "cached", Class: "file"}, {ISBN: "stale", Class: "file"}})
	if gb.probes != 1 || gb.callCount() != 0 {
		t.Fatalf("probes %d, lookups %d; want 1 probe and no lookup", gb.probes, gb.callCount())
	}
	if got[0].Results[1].Outcome != evidence.SourceUnavailable || got[1].Results[1].Outcome != evidence.SourceOK || got[2].Results[1].Outcome != evidence.SourceUnavailable {
		t.Fatalf("Google Books results = %+v / %+v / %+v", got[0].Results[1], got[1].Results[1], got[2].Results[1])
	}
	if at, failed := s.ProbeFailed(evidence.SourceGoogleBooks); !failed || at.IsZero() {
		t.Fatalf("ProbeFailed = %v, %v; want the failed probe's time", at, failed)
	}
	if got[0].Results[0].Outcome != evidence.SourceNoMatch || ol.callCount() != 3 {
		t.Fatalf("Open Library: %+v, %d calls", got[0].Results[0], ol.callCount())
	}
	if _, _, hit, _, _ := c.Get(t.Context(), evidence.SourceGoogleBooks, "fresh"); hit {
		t.Fatal("a skipped lookup reached the cache")
	}
	gb.pass = true
	s.BeginRun(t.Context(), 2, sources.RefreshAll)
	s.Lookup(t.Context(), 2, []sources.Seed{{ISBN: "fresh", Class: "file"}})
	if gb.callCount() != 1 {
		t.Fatalf("a passing probe didn't reopen the source: %d lookups", gb.callCount())
	}
	if _, failed := s.ProbeFailed(evidence.SourceGoogleBooks); failed {
		t.Fatal("ProbeFailed still true after a passing probe")
	}
}

// TestServiceProbeCancelled: a probe cut short by a cancelled run is no
// verdict on the source.
func TestServiceProbeCancelled(t *testing.T) {
	gb := &probingAdapter{fakeAdapter: &fakeAdapter{name: evidence.SourceGoogleBooks}}
	s := sources.NewService(&fakeFetcher{}, newMemCache(), gb)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	s.BeginRun(ctx, 1, sources.RefreshAll)
	if _, failed := s.ProbeFailed(evidence.SourceGoogleBooks); failed {
		t.Fatal("a cancelled probe was recorded as failed")
	}
}

func TestServiceNoSeeds(t *testing.T) {
	s := sources.NewService(&fakeFetcher{}, newMemCache(), &fakeAdapter{name: evidence.SourceDNB}, &fakeAdapter{name: evidence.SourceBNE},
		&fakeAdapter{name: evidence.SourceOpenLibrary}, &fakeAdapter{name: evidence.SourceGoogleBooks},
		&fakeAdapter{name: evidence.SourceWikidata}, &fakeAdapter{name: evidence.SourceHardcover})
	got := s.Lookup(t.Context(), 1, nil)
	if len(got) != 1 || got[0].ISBN != "" || got[0].Seeds == nil || len(got[0].Results) != 6 {
		t.Fatalf("%+v", got)
	}
	order := []string{evidence.SourceDNB, evidence.SourceBNE, evidence.SourceOpenLibrary, evidence.SourceGoogleBooks,
		evidence.SourceWikidata, evidence.SourceHardcover}
	for i, r := range got[0].Results {
		if r.Outcome != evidence.SourceNotQueried || r.Source != order[i] {
			t.Fatalf("%+v", r)
		}
	}
}

func TestServiceBeginRunPrunes(t *testing.T) {
	cache := newMemCache()
	sources.NewService(&fakeFetcher{}, cache).BeginRun(t.Context(), 7, sources.RefreshAll)
	if cache.prunes != 1 {
		t.Fatalf("prunes = %d", cache.prunes)
	}
}

// TestServiceCancelDoesNotTripBreaker: when a run's context ends (Calibre
// went away and the batch was cancelled), the lookups that fail with it are
// not source failures, and the run's breaker stays closed.
func TestServiceCancelDoesNotTripBreaker(t *testing.T) {
	dnb := &fakeAdapter{name: evidence.SourceDNB, outcomes: ok("live")}
	cache := newMemCache()
	s := sources.NewService(&fakeFetcher{}, cache, dnb)
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	var seeds []sources.Seed
	for i := range 4 {
		seeds = append(seeds, sources.Seed{ISBN: fmt.Sprintf("bad%d", i), Class: "file"})
	}
	for range 3 {
		s.Lookup(cancelled, 1, seeds)
	}
	// A cancellation that arrives while the adapter runs.
	cancelling := &cancellingAdapter{fakeAdapter: dnb}
	s2 := sources.NewService(&fakeFetcher{}, cache, cancelling)
	for range 6 {
		mid, stop := context.WithCancel(t.Context())
		cancelling.cancel = stop
		s2.Lookup(mid, 1, seeds[:1])
		if cancelling.cancel != nil {
			t.Fatal("the adapter wasn't reached")
		}
	}
	before := dnb.callCount()
	for _, svc := range []*sources.Service{s, s2} {
		got := svc.Lookup(t.Context(), 1, []sources.Seed{{ISBN: "live", Class: "file"}})
		if got[0].Results[0].Outcome != evidence.SourceOK {
			t.Fatalf("after cancelled lookups the breaker is open: %+v", got)
		}
		cache.rows = map[string]cached{}
	}
	if dnb.callCount() != before+2 || len(cache.puts) != 2 {
		t.Fatalf("calls %d→%d, puts %v", before, dnb.callCount(), cache.puts)
	}
}

// cancellingAdapter, when armed with cancel, cancels the caller's context
// during the call and fails, as a request cut off by cancellation does.
// Unarmed, it answers as its fakeAdapter.
type cancellingAdapter struct {
	*fakeAdapter
	cancel context.CancelFunc
}

func (a *cancellingAdapter) Lookup(ctx context.Context, f sources.Fetcher, isbn string) (string, []evidence.SourceRecord) {
	if a.cancel == nil {
		return a.fakeAdapter.Lookup(ctx, f, isbn)
	}
	a.cancel()
	a.cancel = nil
	return evidence.SourceUnavailable, nil
}

func TestSeedsFromHintsAreInterleavedAndSkipBackMatter(t *testing.T) {
	const x1, x2, x3, x4, x5 = "9780306406157", "9783551551672", "9788478884452", "9780804429573", "9780000000026"
	p := evidence.Package{Formats: []evidence.FormatEvidence{
		{Format: "EPUB", State: evidence.StateOK, ISBNHints: &evidence.ISBNHints{
			Method: evidence.HintEPUBText, State: evidence.HintOK, ISBNs: []string{x1, x2, x3, x5},
			Context: []evidence.ISBNContext{
				{ISBN: x1, Landmark: evidence.LandmarkCopyright}, {ISBN: x2, Landmark: evidence.LandmarkFront},
				{ISBN: x3, Landmark: evidence.LandmarkTitle}, {ISBN: x5, Landmark: evidence.LandmarkBack},
			},
		}},
		{Format: "PDF", State: evidence.StateOK, ISBNHints: &evidence.ISBNHints{Method: evidence.HintTextLayer, State: evidence.HintOK, ISBNs: []string{x4}}},
	}}
	want := []sources.Seed{
		{ISBN: x1, Class: "epub_text"}, {ISBN: x4, Class: "text_layer"}, {ISBN: x2, Class: "epub_text"}, {ISBN: x3, Class: "epub_text"},
	}
	if got := sources.Seeds(p); !slices.Equal(got, want) {
		t.Fatalf("Seeds = %+v, want %+v", got, want)
	}
	s := sources.NewService(&fakeFetcher{}, newMemCache(), &fakeAdapter{name: evidence.SourceDNB, outcomes: ok(x1, x2, x3, x4, x5)})
	var looked []string
	for _, l := range s.Lookup(t.Context(), 1, sources.Seeds(p)) {
		looked = append(looked, l.ISBN)
	}
	if !slices.Equal(looked, []string{x1, x4, x2, x3}) {
		t.Fatalf("looked up %v, want the PDF's hint among the first 4 and no back-matter ISBN", looked)
	}
}

// staleSeeds puts n stale rows for src into c and returns their seeds.
func staleSeeds(c *memCache, src, outcome, prefix string, n int) [][]sources.Seed {
	var out [][]sources.Seed
	for i := range n {
		isbn := fmt.Sprintf("%s%04d", prefix, i)
		c.putStale(src, isbn, outcome)
		out = append(out, []sources.Seed{{ISBN: isbn, Class: "file"}})
	}
	return out
}

// TestStaleRowsAreRefreshedWithinTheBudget: past the budget, a stale row is
// served as it was cached, so a wave of expired rows can't stretch a daily
// run (ADR-044).
func TestStaleRowsAreRefreshedWithinTheBudget(t *testing.T) {
	for _, tc := range []struct {
		name      string
		budget    int
		wantCalls int
	}{
		{"incremental budget", 100, 100},
		{"refresh all", sources.RefreshAll, 300},
		{"zero budget", 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newMemCache()
			dnb := &fakeAdapter{name: evidence.SourceDNB, fallback: evidence.SourceOK}
			s := sources.NewService(&fakeFetcher{}, c, dnb)
			books := staleSeeds(c, evidence.SourceDNB, evidence.SourceNoMatch, "s", 300)
			s.BeginRun(t.Context(), 1, tc.budget)
			served := 0
			for _, seeds := range books {
				got := s.Lookup(t.Context(), 1, seeds)[0].Results[0]
				if got.Outcome == evidence.SourceNoMatch {
					served++
				} else if got.Outcome != evidence.SourceOK {
					t.Fatalf("unexpected outcome %+v", got)
				}
			}
			if dnb.callCount() != tc.wantCalls || served != 300-tc.wantCalls {
				t.Fatalf("calls %d, served stale %d; want %d calls, %d stale", dnb.callCount(), served, tc.wantCalls, 300-tc.wantCalls)
			}
		})
	}
}

// TestAMissNeverUsesTheBudget: a book never looked up is always asked.
func TestAMissNeverUsesTheBudget(t *testing.T) {
	dnb := &fakeAdapter{name: evidence.SourceDNB, fallback: evidence.SourceNoMatch}
	s := sources.NewService(&fakeFetcher{}, newMemCache(), dnb)
	s.BeginRun(t.Context(), 1, 0)
	for i := range 5 {
		s.Lookup(t.Context(), 1, []sources.Seed{{ISBN: fmt.Sprintf("new%d", i), Class: "file"}})
	}
	if dnb.callCount() != 5 {
		t.Fatalf("calls %d, want 5", dnb.callCount())
	}
}

// TestTheBudgetIsPerSource: one source's refreshes don't use another's.
func TestTheBudgetIsPerSource(t *testing.T) {
	c := newMemCache()
	dnb := &fakeAdapter{name: evidence.SourceDNB, fallback: evidence.SourceNoMatch}
	bne := &fakeAdapter{name: evidence.SourceBNE, fallback: evidence.SourceNoMatch}
	s := sources.NewService(&fakeFetcher{}, c, dnb, bne)
	books := staleSeeds(c, evidence.SourceDNB, evidence.SourceNoMatch, "x", 150)
	for _, seeds := range books {
		c.putStale(evidence.SourceBNE, seeds[0].ISBN, evidence.SourceNoMatch)
	}
	s.BeginRun(t.Context(), 1, 100)
	for _, seeds := range books {
		s.Lookup(t.Context(), 1, seeds)
	}
	if dnb.callCount() != 100 || bne.callCount() != 100 {
		t.Fatalf("calls dnb=%d bne=%d, want 100 each", dnb.callCount(), bne.callCount())
	}
}

// TestTheBudgetIsPerRun: a new run gets a fresh budget.
func TestTheBudgetIsPerRun(t *testing.T) {
	c := newMemCache()
	dnb := &fakeAdapter{name: evidence.SourceDNB, fallback: evidence.SourceNoMatch}
	s := sources.NewService(&fakeFetcher{}, c, dnb)
	books := staleSeeds(c, evidence.SourceDNB, evidence.SourceOK, "r", 3)
	s.BeginRun(t.Context(), 1, 1)
	for _, seeds := range books {
		s.Lookup(t.Context(), 1, seeds)
	}
	// The refreshed row is fresh now; the other two are still stale.
	s.BeginRun(t.Context(), 2, 1)
	for _, seeds := range books {
		s.Lookup(t.Context(), 2, seeds)
	}
	if dnb.callCount() != 2 {
		t.Fatalf("calls %d, want 2 (one per run)", dnb.callCount())
	}
}

// TestAFailedRefreshFallsBackToAStaleRecord: a source that fails now still
// has its old records served; an old "no match" from a failing source isn't
// trusted.
func TestAFailedRefreshFallsBackToAStaleRecord(t *testing.T) {
	c := newMemCache()
	dnb := &fakeAdapter{name: evidence.SourceDNB} // every lookup: unavailable
	s := sources.NewService(&fakeFetcher{}, c, dnb)
	c.putStale(evidence.SourceDNB, "rec", evidence.SourceOK)
	c.putStale(evidence.SourceDNB, "none", evidence.SourceNoMatch)
	s.BeginRun(t.Context(), 1, sources.RefreshAll)
	got := s.Lookup(t.Context(), 1, []sources.Seed{{ISBN: "rec", Class: "file"}, {ISBN: "none", Class: "file"}})
	if r := got[0].Results[0]; r.Outcome != evidence.SourceOK || len(r.Records) != 1 || r.Records[0].ID != "cached-rec" {
		t.Fatalf("stale ok after a failed refresh = %+v", r)
	}
	if r := got[1].Results[0]; r.Outcome != evidence.SourceUnavailable {
		t.Fatalf("stale no_match after a failed refresh = %+v, want unavailable", r)
	}
	if dnb.callCount() != 2 {
		t.Fatalf("calls %d, want 2", dnb.callCount())
	}
}

// TestAStaleRowIsServedWhileTheBreakerIsOpen: an open breaker serves old
// records and refuses old "no match" answers, without asking the source.
func TestAStaleRowIsServedWhileTheBreakerIsOpen(t *testing.T) {
	c := newMemCache()
	gb := &probingAdapter{fakeAdapter: &fakeAdapter{name: evidence.SourceGoogleBooks, fallback: evidence.SourceOK}}
	s := sources.NewService(&fakeFetcher{}, c, gb)
	c.putStale(evidence.SourceGoogleBooks, "rec", evidence.SourceOK)
	c.putStale(evidence.SourceGoogleBooks, "none", evidence.SourceNoMatch)
	s.BeginRun(t.Context(), 1, sources.RefreshAll) // the probe fails: breaker open
	got := s.Lookup(t.Context(), 1, []sources.Seed{{ISBN: "rec", Class: "file"}, {ISBN: "none", Class: "file"}})
	if gb.callCount() != 0 {
		t.Fatalf("calls %d, want none", gb.callCount())
	}
	if got[0].Results[0].Outcome != evidence.SourceOK || got[1].Results[0].Outcome != evidence.SourceUnavailable {
		t.Fatalf("results %+v / %+v", got[0].Results[0], got[1].Results[0])
	}
}

// TestNoBeginRunMeansNoLimit: a lookup outside any run's budget (tools,
// tests) refreshes every stale row.
func TestNoBeginRunMeansNoLimit(t *testing.T) {
	c := newMemCache()
	dnb := &fakeAdapter{name: evidence.SourceDNB, fallback: evidence.SourceNoMatch}
	s := sources.NewService(&fakeFetcher{}, c, dnb)
	for _, seeds := range staleSeeds(c, evidence.SourceDNB, evidence.SourceNoMatch, "n", 3) {
		s.Lookup(t.Context(), 9, seeds)
	}
	if dnb.callCount() != 3 {
		t.Fatalf("calls %d, want 3", dnb.callCount())
	}
}

// TestTTLJitter: each answer is cached for 75 % to 125 % of its TTL, so rows
// written in one run don't all expire at the same hour.
func TestTTLJitter(t *testing.T) {
	const okTTL, noneTTL = 720 * time.Hour, 168 * time.Hour
	for _, tc := range []struct {
		r        float64
		min, max float64 // bounds of the factor, inclusive
	}{
		{0, 0.75, 0.75},
		{0.5, 1, 1},
		{0.999, 1.2494, 1.2496},
	} {
		cache := newMemCache()
		dnb := &fakeAdapter{name: evidence.SourceDNB, outcomes: map[string]string{"a": evidence.SourceOK, "b": evidence.SourceNoMatch}}
		s := sources.NewService(&fakeFetcher{}, cache, dnb)
		sources.SetRand(s, func() float64 { return tc.r })
		s.Lookup(t.Context(), 1, []sources.Seed{{ISBN: "a", Class: "file"}, {ISBN: "b", Class: "file"}})
		for key, base := range map[string]time.Duration{"dnb/a": okTTL, "dnb/b": noneTTL} {
			got := cache.rows[key].ttl
			lo, hi := time.Duration(tc.min*float64(base)), time.Duration(tc.max*float64(base))
			if got < lo || got > hi {
				t.Fatalf("r=%v %s: ttl %v, want %v..%v", tc.r, key, got, lo, hi)
			}
		}
	}
}

// configurableAdapter answers not_configured until it has a key.
type configurableAdapter struct {
	*fakeAdapter
	configured bool
}

func (a *configurableAdapter) Configured() bool { return a.configured }

func (a *configurableAdapter) Lookup(ctx context.Context, f sources.Fetcher, isbn string) (string, []evidence.SourceRecord) {
	if !a.configured {
		return evidence.SourceNotConfigured, nil
	}
	return a.fakeAdapter.Lookup(ctx, f, isbn)
}

// TestAnUnconfiguredSourceUsesNoBudget: a source whose key was removed
// answers not_configured; refreshing its stale rows uses no budget, so the
// other sources and books aren't starved, and its old records aren't sealed.
func TestAnUnconfiguredSourceUsesNoBudget(t *testing.T) {
	c := newMemCache()
	gb := &configurableAdapter{fakeAdapter: &fakeAdapter{name: evidence.SourceGoogleBooks, fallback: evidence.SourceOK}}
	s := sources.NewService(&fakeFetcher{}, c, gb)
	books := staleSeeds(c, evidence.SourceGoogleBooks, evidence.SourceOK, "g", 5)
	s.BeginRun(t.Context(), 1, 2)
	for _, seeds := range books {
		if r := s.Lookup(t.Context(), 1, seeds)[0].Results[0]; r.Outcome != evidence.SourceNotConfigured {
			t.Fatalf("unconfigured source answered %+v", r)
		}
	}
	gb.configured = true
	for _, seeds := range books {
		s.Lookup(t.Context(), 1, seeds)
	}
	if gb.callCount() != 2 {
		t.Fatalf("calls after the key came back = %d, want the full budget of 2", gb.callCount())
	}
}

// TestABreakerOpenedDuringTheRunRefusesStaleNoMatch: five failed refreshes
// open the breaker; after that a stale no_match isn't served, budget or not.
func TestABreakerOpenedDuringTheRunRefusesStaleNoMatch(t *testing.T) {
	c := newMemCache()
	dnb := &fakeAdapter{name: evidence.SourceDNB} // unavailable
	s := sources.NewService(&fakeFetcher{}, c, dnb)
	failing := staleSeeds(c, evidence.SourceDNB, evidence.SourceOK, "f", 5)
	c.putStale(evidence.SourceDNB, "none", evidence.SourceNoMatch)
	s.BeginRun(t.Context(), 1, sources.RefreshAll)
	for _, seeds := range failing {
		if r := s.Lookup(t.Context(), 1, seeds)[0].Results[0]; r.Outcome != evidence.SourceOK {
			t.Fatalf("stale ok after a failed refresh = %+v", r)
		}
	}
	before := dnb.callCount()
	r := s.Lookup(t.Context(), 1, []sources.Seed{{ISBN: "none", Class: "file"}})[0].Results[0]
	if r.Outcome != evidence.SourceUnavailable || dnb.callCount() != before {
		t.Fatalf("stale no_match with the breaker open = %+v, calls %d→%d", r, before, dnb.callCount())
	}
}

// failingCache fails every read.
type failingCache struct{ *memCache }

func (failingCache) Get(context.Context, string, string) (string, []evidence.SourceRecord, bool, bool, error) {
	return "", nil, false, false, fmt.Errorf("database gone")
}

// TestACacheReadErrorIsAMiss: the source is asked and no budget is used.
func TestACacheReadErrorIsAMiss(t *testing.T) {
	dnb := &fakeAdapter{name: evidence.SourceDNB, fallback: evidence.SourceNoMatch}
	s := sources.NewService(&fakeFetcher{}, failingCache{newMemCache()}, dnb)
	s.BeginRun(t.Context(), 1, 0)
	for i := range 3 {
		s.Lookup(t.Context(), 1, []sources.Seed{{ISBN: fmt.Sprintf("e%d", i), Class: "file"}})
	}
	if dnb.callCount() != 3 {
		t.Fatalf("calls %d, want 3", dnb.callCount())
	}
}

// TestTheBudgetHoldsUnderConcurrentLookups: four workers can't overshoot it.
func TestTheBudgetHoldsUnderConcurrentLookups(t *testing.T) {
	c := newMemCache()
	dnb := &fakeAdapter{name: evidence.SourceDNB, fallback: evidence.SourceNoMatch}
	s := sources.NewService(&fakeFetcher{}, c, dnb)
	books := staleSeeds(c, evidence.SourceDNB, evidence.SourceNoMatch, "c", 200)
	s.BeginRun(t.Context(), 1, 30)
	var wg sync.WaitGroup
	for w := range 4 {
		wg.Go(func() {
			for i := w; i < len(books); i += 4 {
				s.Lookup(t.Context(), 1, books[i])
			}
		})
	}
	wg.Wait()
	if dnb.callCount() != 30 {
		t.Fatalf("calls %d, want exactly the budget of 30", dnb.callCount())
	}
}

// TestAnUnconfiguredSourceCostsNoCacheQuery: a source without its key
// answers not_configured before the cache is read.
func TestAnUnconfiguredSourceCostsNoCacheQuery(t *testing.T) {
	c := newMemCache()
	gb := &configurableAdapter{fakeAdapter: &fakeAdapter{name: evidence.SourceGoogleBooks, fallback: evidence.SourceOK}}
	s := sources.NewService(&fakeFetcher{}, c, gb)
	for _, isbn := range []string{"9780306406157", "9780306406158"} {
		if r := s.Lookup(t.Context(), 1, []sources.Seed{{ISBN: isbn, Class: "file"}})[0].Results[0]; r.Outcome != evidence.SourceNotConfigured {
			t.Fatalf("unconfigured source answered %+v", r)
		}
	}
	if c.gets != 0 {
		t.Errorf("%d cache reads for an unconfigured source, want 0", c.gets)
	}
}
