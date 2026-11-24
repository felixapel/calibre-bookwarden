package verify

import (
	"maps"
	"sync"
	"time"
)

// The stages of Stats.StageSeconds (B5).
const (
	StageCalibre       = "calibre"        // reading Calibre records
	StageExportInspect = "export_inspect" // downloading and inspecting files
	StageOCR           = "ocr"            // ISBN-hint tools on PDFs
	StageLookups       = "lookups"        // catalogue lookups
	StageExtraction    = "extraction"     // model extraction of excerpts
	StageStore         = "store"          // storing books' results
)

// Stages lists the stages in the order the run page shows them.
var Stages = []string{StageCalibre, StageExportInspect, StageOCR, StageLookups, StageExtraction, StageStore}

// Stats is where a run's time went: the seconds of each stage summed over
// the workers (so they can exceed the run's wall time), the catalogue
// lookups by source and outcome, and the extractor calls. A run's stats are
// stored with it, added to by each attempt, and copied to its ledger row
// when it ends. No book data.
type Stats struct {
	StageSeconds map[string]float64        `json:"stage_seconds,omitempty"`
	Lookups      map[string]map[string]int `json:"lookups,omitempty"`
	Extractor    ExtractorCounts           `json:"extractor,omitzero"`
}

// ExtractorCounts counts a run's extractor calls: Calls asked a model,
// Cached were answered by the cache, and Failed were calls without a usable
// answer.
type ExtractorCounts struct {
	Calls  int `json:"calls"`
	Cached int `json:"cached"`
	Failed int `json:"failed"`
}

// Empty reports whether nothing was counted: no stage took time, no lookup
// and no extractor call happened, as for a run cancelled before its first
// book.
func (s Stats) Empty() bool {
	for _, sec := range s.StageSeconds {
		if sec != 0 {
			return false
		}
	}
	for _, byOutcome := range s.Lookups {
		for _, n := range byOutcome {
			if n != 0 {
				return false
			}
		}
	}
	return s.Extractor == ExtractorCounts{}
}

// LookupCounter is implemented by a Sources that counts its lookups for the
// run stats: TakeLookupCounts returns the counts by source and outcome since
// it was last called, and starts again from zero.
type LookupCounter interface {
	TakeLookupCounts() map[string]map[string]int
}

// ExtractorCounter is the same for an Extraction's calls.
type ExtractorCounter interface {
	TakeExtractorCounts() ExtractorCounts
}

// runStats adds up a run's Stats while it executes. It is safe for
// concurrent use.
type runStats struct {
	mu sync.Mutex
	s  Stats
}

// newRunStats starts from what earlier attempts stored.
func newRunStats(earlier Stats) *runStats {
	s := Stats{StageSeconds: map[string]float64{}, Lookups: map[string]map[string]int{}, Extractor: earlier.Extractor}
	for _, st := range Stages {
		s.StageSeconds[st] = 0
	}
	maps.Copy(s.StageSeconds, earlier.StageSeconds)
	for src, n := range earlier.Lookups {
		s.Lookups[src] = maps.Clone(n)
	}
	return &runStats{s: s}
}

// stage adds d to a stage; a nil runStats (a checker outside a run) counts
// nothing.
func (a *runStats) stage(name string, d time.Duration) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.s.StageSeconds[name] += d.Seconds()
}

// take adds what the run's sources and extraction counted since the last
// take.
func (a *runStats) take(src Sources, ex Extraction) {
	var lookups map[string]map[string]int
	if c, ok := src.(LookupCounter); ok {
		lookups = c.TakeLookupCounts()
	}
	var calls ExtractorCounts
	if c, ok := ex.(ExtractorCounter); ok {
		calls = c.TakeExtractorCounts()
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for source, byOutcome := range lookups {
		if a.s.Lookups[source] == nil {
			a.s.Lookups[source] = map[string]int{}
		}
		for outcome, n := range byOutcome {
			a.s.Lookups[source][outcome] += n
		}
	}
	a.s.Extractor.Calls += calls.Calls
	a.s.Extractor.Cached += calls.Cached
	a.s.Extractor.Failed += calls.Failed
}

// snapshot returns a copy of the stats so far.
func (a *runStats) snapshot() Stats {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := Stats{StageSeconds: maps.Clone(a.s.StageSeconds), Lookups: map[string]map[string]int{}, Extractor: a.s.Extractor}
	for src, n := range a.s.Lookups {
		out.Lookups[src] = maps.Clone(n)
	}
	if len(out.Lookups) == 0 {
		out.Lookups = nil
	}
	return out
}
