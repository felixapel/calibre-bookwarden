package sources

import (
	"context"
	"io"
	"log/slog"
	"maps"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/findings"
)

// Lookup policy (ADR-019, ADR-044).
const (
	maxLookupISBNs = 4
	okTTL          = 30 * 24 * time.Hour
	noMatchTTL     = 7 * 24 * time.Hour
	breakerTrip    = 5 // consecutive unavailable outcomes per source per run
)

// Refresh budgets for BeginRun: how many cached answers past their expiry a
// run may ask a source again; the rest are served as cached (ADR-044).
// Lookups of ISBNs that are not cached at all never count.
const (
	// RefreshAll refreshes every expired answer: a full run, a one-book
	// recheck.
	RefreshAll = -1
	// RefreshIncremental is the budget per source of an incremental run.
	RefreshIncremental = 100
)

// allSources is the order of a Lookup's results.
var allSources = []string{evidence.SourceDNB, evidence.SourceBNE, evidence.SourceOpenLibrary, evidence.SourceGoogleBooks,
	evidence.SourceWikidata, evidence.SourceHardcover}

// Seed is one canonical ISBN-13 and where it came from: evidence.SeedFile,
// evidence.SeedCalibre, or a hint method.
type Seed struct {
	ISBN  string
	Class string
}

// Seeds lists the ISBNs worth looking up, in order: readable files'
// metadata, Calibre's isbn identifier, then text-layer and OCR hints.
// Invalid ISBNs are dropped; each (ISBN, class) pair appears once.
func Seeds(p evidence.Package) []Seed {
	var out []Seed
	add := func(raw, class string) {
		isbn, ok := findings.CanonicalISBN(raw)
		if !ok {
			return
		}
		s := Seed{ISBN: isbn, Class: class}
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	for _, f := range p.Formats {
		if f.State == evidence.StateOK && f.Metadata != nil {
			for _, raw := range f.Metadata.ISBNs {
				add(raw, evidence.SeedFile)
			}
		}
	}
	for _, key := range slices.Sorted(maps.Keys(p.Calibre.Identifiers)) {
		if strings.EqualFold(key, "isbn") {
			add(p.Calibre.Identifiers[key], evidence.SeedCalibre)
		}
	}
	// Hints are interleaved across formats, the first ISBN of each, then the
	// second, so that one format's many ISBNs can't crowd the others' out of
	// the lookup cap. Front-matter ISBNs from the back of a book are often
	// ads for other books: they stay sealed as evidence but aren't looked up.
	for k := range findings.MaxISBNs {
		for _, f := range p.Formats {
			h := f.ISBNHints
			if h == nil || k >= len(h.ISBNs) {
				continue
			}
			if h.Method == evidence.HintEPUBText && k < len(h.Context) && h.Context[k].Landmark == evidence.LandmarkBack {
				continue
			}
			add(h.ISBNs[k], h.Method)
		}
	}
	return out
}

// Cache stores definite outcomes per source and ISBN. Records come back as
// they were put.
// Get reports an answer past its expiry as a hit with stale set.
type Cache interface {
	Get(ctx context.Context, source, isbn string) (outcome string, records []evidence.SourceRecord, hit, stale bool, err error)
	Put(ctx context.Context, source, isbn, outcome string, records []evidence.SourceRecord, ttl time.Duration) error
	Prune(ctx context.Context) error
}

// Recorder counts lookups for the metrics endpoint. outcome is a source outcome
// from the evidence package, or cache_hit when the cache answered, stale_hit
// when an expired cached answer was served (ADR-044), and breaker_open when
// the source was skipped for the run.
type Recorder interface {
	SourceLookup(source, outcome string)
	// SourceRequest times one live lookup.
	SourceRequest(source string, d time.Duration)
}

// Service looks ISBNs up in every source, through the cache and a per-run
// circuit breaker. It is safe for concurrent use.
type Service struct {
	Logger *slog.Logger
	// Metrics, when set, counts every lookup by source and outcome.
	Metrics Recorder

	fetch    Fetcher
	cache    Cache
	adapters []Adapter

	// rand scales each cache TTL by 0.75 + 0.5·rand(), so answers written in
	// one run don't all expire together.
	rand func() float64

	mu      sync.Mutex
	fails   map[int64]map[string]int  // run → source → consecutive unavailable
	probed  map[string]time.Time      // source → when its last probe failed
	budget  map[int64]int             // run → refresh budget per source
	refresh map[int64]map[string]int  // run → source → refreshes used
	counts  map[string]map[string]int // source → outcome → lookups since the last take
}

// NewService returns a Service over the adapters, in result order.
func NewService(f Fetcher, c Cache, adapters ...Adapter) *Service {
	return &Service{
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		fetch:    f,
		cache:    c,
		adapters: adapters,
		rand:     rand.Float64,
		fails:    map[int64]map[string]int{},
		probed:   map[string]time.Time{},
		budget:   map[int64]int{},
		refresh:  map[int64]map[string]int{},
	}
}

// BeginRun closes every breaker for a new run, opens it again for a source
// that fails its probe, sets the run's refresh budget per source (RefreshAll
// or a count), and prunes cache rows long past their expiry. A prune failure
// is logged and ignored. A resumed run gets its budget again.
func (s *Service) BeginRun(ctx context.Context, runID int64, refresh int) {
	fails, probed := map[string]int{}, map[string]time.Time{}
	for _, a := range s.adapters {
		p, ok := a.(Prober)
		if !ok || p.Probe(ctx, s.fetch) {
			continue
		}
		fails[a.Name()] = breakerTrip
		if ctx.Err() == nil { // a cancelled probe is no verdict on the source
			probed[a.Name()] = time.Now()
			s.Logger.Warn("source failed its probe; skipped for this run", "run", runID, "source", a.Name())
		}
	}
	s.mu.Lock()
	s.fails = map[int64]map[string]int{runID: fails}
	s.probed = probed
	s.budget = map[int64]int{runID: refresh}
	s.refresh = map[int64]map[string]int{}
	s.mu.Unlock()
	if err := s.cache.Prune(ctx); err != nil {
		s.Logger.Warn("source cache prune failed", "run", runID, "err", err)
	}
}

// Lookup queries the first four distinct ISBNs in seeds. Without seeds it
// returns one Lookup whose sources are all not_queried.
func (s *Service) Lookup(ctx context.Context, runID int64, seeds []Seed) []evidence.Lookup {
	var out []evidence.Lookup
	for _, seed := range seeds {
		i := slices.IndexFunc(out, func(l evidence.Lookup) bool { return l.ISBN == seed.ISBN })
		switch {
		case i >= 0:
			if !slices.Contains(out[i].Seeds, seed.Class) {
				out[i].Seeds = append(out[i].Seeds, seed.Class)
			}
		case len(out) < maxLookupISBNs:
			out = append(out, evidence.Lookup{ISBN: seed.ISBN, Seeds: []string{seed.Class}})
		}
	}
	if len(out) == 0 {
		l := evidence.Lookup{ISBN: "", Seeds: []string{}}
		for _, src := range allSources {
			l.Results = append(l.Results, evidence.SourceResult{Source: src, Outcome: evidence.SourceNotQueried})
		}
		return []evidence.Lookup{l}
	}
	for i := range out {
		for _, a := range s.adapters {
			out[i].Results = append(out[i].Results, s.query(ctx, runID, a, out[i].ISBN))
		}
	}
	return out
}

func (s *Service) query(ctx context.Context, runID int64, a Adapter, isbn string) evidence.SourceResult {
	src := a.Name()
	// A cancelled run isn't a source failure: the book won't seal, so the
	// answer only must not trip the breaker or reach the cache.
	if ctx.Err() != nil {
		return evidence.SourceResult{Source: src, Outcome: evidence.SourceUnavailable}
	}
	if c, ok := a.(Configurer); ok && !c.Configured() {
		s.count(src, evidence.SourceNotConfigured)
		return evidence.SourceResult{Source: src, Outcome: evidence.SourceNotConfigured}
	}
	outcome, records, hit, stale, err := s.cache.Get(ctx, src, isbn)
	if err != nil {
		s.Logger.Warn("source cache read failed", "source", src, "err", err)
		hit = false
	}
	// A no_match cached for a source that now fails its probe may be the same
	// empty answer, so it isn't served, fresh or stale; a cached record is.
	usable := hit && (outcome != evidence.SourceNoMatch || !s.failedProbe(src))
	if usable && !stale {
		s.count(src, "cache_hit")
		return evidence.SourceResult{Source: src, Outcome: outcome, Records: records}
	}
	cachedResult := evidence.SourceResult{Source: src, Outcome: outcome, Records: records}
	if s.open(runID, src) {
		if usable && outcome == evidence.SourceOK {
			s.count(src, "stale_hit")
			return cachedResult
		}
		s.count(src, "breaker_open")
		return evidence.SourceResult{Source: src, Outcome: evidence.SourceUnavailable}
	}
	// An expired answer is asked again only within the run's budget
	// (ADR-044); past it, the answer is served as cached.
	if usable && !s.takeRefresh(runID, src) {
		s.count(src, "stale_hit")
		return cachedResult
	}
	asked := time.Now()
	fresh, freshRecords := a.Lookup(ctx, s.fetch, isbn)
	if ctx.Err() != nil {
		return evidence.SourceResult{Source: src, Outcome: evidence.SourceUnavailable}
	}
	// Timed only when it wasn't cut short: a stop says nothing about the
	// source.
	if s.Metrics != nil {
		s.Metrics.SourceRequest(src, time.Since(asked))
	}
	s.record(runID, src, fresh)
	if fresh == evidence.SourceUnavailable && usable && outcome == evidence.SourceOK {
		s.count(src, "stale_hit") // one count per lookup: the answer served
		return cachedResult
	}
	s.count(src, fresh)
	var ttl time.Duration
	switch fresh {
	case evidence.SourceOK:
		ttl = okTTL
	case evidence.SourceNoMatch:
		ttl = noMatchTTL
	}
	if ttl > 0 {
		ttl = time.Duration(float64(ttl) * (0.75 + 0.5*s.rand()))
		if err := s.cache.Put(ctx, src, isbn, fresh, freshRecords, ttl); err != nil {
			s.Logger.Warn("source cache write failed", "source", src, "err", err)
		}
	}
	return evidence.SourceResult{Source: src, Outcome: fresh, Records: freshRecords}
}

// takeRefresh uses one unit of runID's refresh budget for src, if one is
// left. A run without a budget (no BeginRun) has no limit.
func (s *Service) takeRefresh(runID int64, src string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	limit, ok := s.budget[runID]
	if !ok || limit < 0 {
		return true
	}
	used := s.refresh[runID]
	if used == nil {
		used = map[string]int{}
		s.refresh[runID] = used
	}
	if used[src] >= limit {
		return false
	}
	used[src]++
	return true
}

// ProbeFailed reports when src last failed its probe, if its last probe
// failed.
func (s *Service) ProbeFailed(src string) (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	at, ok := s.probed[src]
	return at, ok
}

func (s *Service) failedProbe(src string) bool {
	_, ok := s.ProbeFailed(src)
	return ok
}

func (s *Service) open(runID int64, src string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fails[runID][src] >= breakerTrip
}

func (s *Service) record(runID int64, src, outcome string) {
	// Neither says anything about the source's health.
	if outcome == evidence.SourceNotQueried || outcome == evidence.SourceNotConfigured {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	run := s.fails[runID]
	if run == nil {
		run = map[string]int{}
		s.fails[runID] = run
	}
	if outcome == evidence.SourceUnavailable {
		run[src]++
	} else {
		run[src] = 0
	}
}

func (s *Service) count(source, outcome string) {
	if s.Metrics != nil {
		s.Metrics.SourceLookup(source, outcome)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.counts == nil {
		s.counts = map[string]map[string]int{}
	}
	if s.counts[source] == nil {
		s.counts[source] = map[string]int{}
	}
	s.counts[source][outcome]++
}

// TakeLookupCounts returns the lookups counted since it was last called, by
// source and outcome as for Recorder, and starts again from zero; the runner
// adds them to the run's stats (B5). One run executes at a time.
func (s *Service) TakeLookupCounts() map[string]map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.counts
	s.counts = nil
	return out
}
