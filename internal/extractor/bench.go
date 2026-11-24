package extractor

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/agent"
	"github.com/felixapel/calibre-bookwarden/internal/extract"
)

// The benchmark of design section 4a: one cell is one extractor at one
// effort over a set of samples. Synthetic samples have truth; real samples
// are scored against the deterministic parsers. Nothing here keeps text: the
// stats are counts, rates and durations.

// Sample is one excerpt to extract from.
type Sample struct {
	Name    string
	Excerpt extract.Excerpt
	// Truth and traps of a synthetic sample; nil for a real one.
	Truth []Fact
	Traps []string
	real  bool
	// parser holds the keys the parsers ground in a real sample.
	parser map[string]bool
}

// SyntheticSample wraps a synthetic case.
func SyntheticSample(c Case) Sample {
	return Sample{Name: c.Name, Excerpt: c.Excerpt.Prepared(), Truth: c.Truth, Traps: c.Traps}
}

// RealSample wraps a stored excerpt; the parsers run once here.
func RealSample(name string, x extract.Excerpt) Sample {
	x = x.Prepared()
	s := Sample{Name: name, Excerpt: x, real: true, parser: map[string]bool{}}
	for _, c := range extract.Parse(x) {
		if v := extract.Verify(x, c); v.Status == extract.StatusGrounded {
			s.parser[key(v)] = true
		}
	}
	return s
}

func key(v extract.Verified) string { return v.Field + "\x00" + v.Value + "\x00" + v.Role + v.Label }

// patterned reports whether a grounded value is of a field the parsers also
// read (design section 7).
func patterned(v extract.Verified) bool {
	switch v.Field {
	case extract.FieldISBN, extract.FieldYear, extract.FieldSeriesIndex:
		return true
	case extract.FieldContributor:
		return v.Role == extract.RoleTranslator
	}
	return false
}

// Budget bounds the calls of a whole benchmark.
type Budget struct{ left atomic.Int64 }

// NewBudget returns a budget of n calls.
func NewBudget(n int64) *Budget {
	b := &Budget{}
	b.left.Store(n)
	return b
}

func (b *Budget) take() bool { return b.left.Add(-1) >= 0 }

// CellStats is what one cell measured.
type CellStats struct {
	Extractor, Effort string
	Calls, Skipped    int
	Errors            map[string]int // by error code; invalid_output for an unparsable answer, model_mismatch for another model's
	Latencies         []time.Duration
	InputTokens       int64
	OutputTokens      int64
	ReasoningTokens   int64
	Models            map[string]int // models the provider reported

	Claims, Grounded int
	Status           map[string]int // verifier outcome per claim
	FieldGrounded    map[string]int

	// Synthetic samples.
	TP, FP, FN, Extra, TrapHits int
	FieldFP                     map[string]int

	// Real samples: grounded patterned values, how many the parsers also
	// ground, and how many parser values the extractor found.
	ParserPatterned, ParserAgree, ParserTotal, ParserCovered int
	RealSamples, RealWithClaims                              int

	grounded map[string]map[string]bool // sample → grounded keys, for Agreement
}

// Precision is TP/(TP+FP) on the synthetic samples.
func (s CellStats) Precision() float64 { return ratio(s.TP, s.TP+s.FP) }

// Recall is TP/(TP+FN) on the synthetic samples.
func (s CellStats) Recall() float64 { return ratio(s.TP, s.TP+s.FN) }

// GroundedShare is the share of claims the verifier grounded.
func (s CellStats) GroundedShare() float64 { return ratio(s.Grounded, s.Claims) }

// Percentile returns the p-th percentile latency (0 < p ≤ 100).
func (s CellStats) Percentile(p float64) time.Duration {
	if len(s.Latencies) == 0 {
		return 0
	}
	l := slices.Clone(s.Latencies)
	slices.Sort(l)
	i := int(float64(len(l))*p/100+0.5) - 1
	return l[min(max(i, 0), len(l)-1)]
}

func ratio(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b)
}

// Agreement is the share of grounded values two cells have in common over the
// samples both answered: |A∩B| / |A∪B|, summed over samples.
func Agreement(a, b CellStats) float64 {
	shared, union := 0, 0
	for name, ka := range a.grounded {
		kb, ok := b.grounded[name]
		if !ok {
			continue
		}
		for k := range ka {
			if kb[k] {
				shared++
			}
		}
		union += len(ka) + len(kb)
	}
	return ratio(shared, union-shared)
}

// RunOptions bound a cell: calls at a time, the benchmark's call budget, and
// the time one call may take (0: no limit).
type RunOptions struct {
	Concurrency int
	Budget      *Budget
	Timeout     time.Duration
}

// RunCell extracts every sample with e, as RunOptions allow, and scores the
// answers.
func RunCell(ctx context.Context, e Extractor, samples []Sample, o RunOptions) CellStats {
	st := CellStats{
		Extractor: e.Name, Effort: e.Effort, Errors: map[string]int{}, Models: map[string]int{},
		Status: map[string]int{}, FieldGrounded: map[string]int{}, FieldFP: map[string]int{},
		grounded: map[string]map[string]bool{},
	}
	var mu sync.Mutex
	sem := make(chan struct{}, max(o.Concurrency, 1))
	var wg sync.WaitGroup
	for _, s := range samples {
		if !o.Budget.take() {
			st.Skipped++
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(s Sample) {
			defer func() { <-sem; wg.Done() }()
			cctx, cancel := ctx, context.CancelFunc(func() {})
			if o.Timeout > 0 {
				cctx, cancel = context.WithTimeout(ctx, o.Timeout)
			}
			r, err := e.Extract(cctx, s.Excerpt)
			cancel()
			mu.Lock()
			defer mu.Unlock()
			st.record(s, r, err)
		}(s)
	}
	wg.Wait()
	return st
}

func (s *CellStats) record(smp Sample, r Result, err error) {
	s.Calls++
	s.Latencies = append(s.Latencies, r.Elapsed)
	s.InputTokens += r.Reply.InputTokens
	s.OutputTokens += r.Reply.OutputTokens
	s.ReasoningTokens += r.Reply.ReasoningTokens
	if r.Reply.Model != "" {
		s.Models[r.Reply.Model]++
	}
	if smp.real {
		s.RealSamples++
		s.ParserTotal += len(smp.parser)
	}
	switch {
	case errors.Is(err, ErrInvalidOutput):
		s.Errors["invalid_output"]++
	case errors.Is(err, ErrModelMismatch):
		s.Errors["model_mismatch"]++
	case err != nil:
		s.Errors[agent.ErrorCode(err)]++
	}
	if err != nil {
		s.FN += len(smp.Truth)
		return
	}
	got := map[string]extract.Verified{}
	for _, c := range r.Claims {
		v := extract.Verify(smp.Excerpt, c)
		s.Claims++
		s.Status[v.Status]++
		if v.Status == extract.StatusGrounded {
			s.Grounded++
			got[key(v)] = v
		}
	}
	keys := map[string]bool{}
	for k, v := range got {
		keys[k] = true
		s.FieldGrounded[v.Field]++
	}
	s.grounded[smp.Name] = keys
	if smp.real {
		if len(r.Claims) > 0 {
			s.RealWithClaims++
		}
		for k, v := range got {
			if patterned(v) {
				s.ParserPatterned++
				if smp.parser[k] {
					s.ParserAgree++
				}
			}
		}
		for k := range smp.parser {
			if keys[k] {
				s.ParserCovered++
			}
		}
		return
	}
	s.scoreSynthetic(smp, got)
}

func (s *CellStats) scoreSynthetic(smp Sample, got map[string]extract.Verified) {
	truth := map[string]bool{}
	fields := map[string]bool{}
	for _, f := range smp.Truth {
		truth[f.Key()] = true
		fields[f.Field] = true
	}
	for k, v := range got {
		trap := slices.Contains(smp.Traps, v.Value) || slices.Contains(smp.Traps, v.Value+"/"+v.Role)
		switch {
		case truth[k]:
			s.TP++
		case trap || fields[v.Field]:
			s.FP++
			s.FieldFP[v.Field]++
		default:
			s.Extra++
		}
		if trap {
			s.TrapHits++
		}
	}
	for k := range truth {
		if _, ok := got[k]; !ok {
			s.FN++
		}
	}
}

// effortRank orders efforts from cheapest; "default" (the provider's own)
// comes last, so a named effort is preferred when it scores the same.
var effortRank = map[string]int{"none": 0, "minimal": 1, "low": 2, "medium": 3, "high": 4, "xhigh": 5, "max": 6, "default": 7}

// minAnswered is the share of a cell's calls that must end without an error
// for the cell to count: a provider that refuses an effort errs on every
// call, and one that mostly fails measures too few samples.
const minAnswered = 0.95

// Usable reports whether a cell answered: at least minAnswered of its calls
// ended without an error.
func (s CellStats) Usable() bool {
	failed := 0
	for _, n := range s.Errors {
		failed += n
	}
	return s.Calls > 0 && float64(s.Calls-failed) >= minAnswered*float64(s.Calls)
}

// SuggestEfforts applies design section 4a: per extractor, among usable cells
// with the best synthetic precision, those whose grounded share is within 2
// points of the best grounded share among them; of those, the cheapest
// effort.
func SuggestEfforts(cells []CellStats) map[string]string {
	by := map[string][]CellStats{}
	for _, c := range cells {
		if c.Usable() {
			by[c.Extractor] = append(by[c.Extractor], c)
		}
	}
	out := map[string]string{}
	for name, cs := range by {
		bestP, bestG := 0.0, 0.0
		for _, c := range cs {
			bestP = max(bestP, c.Precision())
		}
		for _, c := range cs {
			if c.Precision() == bestP {
				bestG = max(bestG, c.GroundedShare())
			}
		}
		pick := ""
		for _, c := range cs {
			if c.Precision() < bestP || c.GroundedShare() < bestG-0.02 {
				continue
			}
			if pick == "" || rank(c.Effort) < rank(pick) {
				pick = c.Effort
			}
		}
		out[name] = pick
	}
	return out
}

func rank(effort string) int {
	if r, ok := effortRank[effort]; ok {
		return r
	}
	return len(effortRank)
}
