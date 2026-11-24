// Package metrics holds bookwarden's process counters and writes the
// Prometheus text exposition format by hand: no client library is needed for
// a few counters, gauges and one histogram.
package metrics

import (
	"fmt"
	"io"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Counters is a family of monotonic counters keyed by label values. It is
// safe for concurrent use.
type Counters struct {
	labels []string
	m      sync.Map // key (values joined with \x00) → *atomic.Int64
}

// NewCounters returns a family whose series carry the named labels.
func NewCounters(labels ...string) *Counters {
	return &Counters{labels: labels}
}

// Inc adds one to the series with these label values, in label order. It
// panics when the number of values is wrong, which is a programming error.
func (c *Counters) Inc(values ...string) {
	if len(values) != len(c.labels) {
		panic(fmt.Sprintf("metrics: %d label values for labels %v", len(values), c.labels))
	}
	key := strings.Join(values, "\x00")
	n, ok := c.m.Load(key)
	if !ok {
		n, _ = c.m.LoadOrStore(key, new(atomic.Int64))
	}
	n.(*atomic.Int64).Add(1)
}

// series is one counter's label values and count.
type series struct {
	values []string
	n      int64
}

func (c *Counters) snapshot() []series {
	var out []series
	c.m.Range(func(k, v any) bool {
		out = append(out, series{values: strings.Split(k.(string), "\x00"), n: v.(*atomic.Int64).Load()})
		return true
	})
	slices.SortFunc(out, func(a, b series) int { return slices.Compare(a.values, b.values) })
	return out
}

// requestBuckets are the upper bounds, in seconds, of the source request
// histogram's buckets.
var requestBuckets = []float64{0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30}

// Histogram is a family of histograms keyed by one label's value. It is safe
// for concurrent use.
type Histogram struct {
	label   string
	buckets []float64
	mu      sync.Mutex
	m       map[string]*histSeries
}

type histSeries struct {
	counts []int64 // per bucket, not cumulative
	sum    float64
	n      int64
}

// NewHistogram returns a family with the given bucket bounds, ascending.
func NewHistogram(label string, buckets ...float64) *Histogram {
	return &Histogram{label: label, buckets: buckets, m: map[string]*histSeries{}}
}

// Observe adds one observation of v to the series of value.
func (h *Histogram) Observe(value string, v float64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.m[value]
	if s == nil {
		s = &histSeries{counts: make([]int64, len(h.buckets))}
		h.m[value] = s
	}
	if i, _ := slices.BinarySearch(h.buckets, v); i < len(h.buckets) {
		s.counts[i]++
	}
	s.sum += v
	s.n++
}

// Process holds the counters that live and die with the process. A nil
// *Process records nothing, so a component can take one unconditionally.
type Process struct {
	sources  *Counters
	agent    *Counters
	requests *Histogram
}

// New returns empty process counters.
func New() *Process {
	return &Process{sources: NewCounters("source", "outcome"), agent: NewCounters("seat", "code"),
		requests: NewHistogram("source", requestBuckets...)}
}

// SourceRequest times one live source lookup.
func (p *Process) SourceRequest(source string, d time.Duration) {
	if p != nil {
		p.requests.Observe(source, d.Seconds())
	}
}

// SourceLookup counts one source lookup by its outcome.
func (p *Process) SourceLookup(source, outcome string) {
	if p != nil {
		p.sources.Inc(source, outcome)
	}
}

// AgentCall counts one call to an agent seat; code is "" on success, else
// the error code.
func (p *Process) AgentCall(seat, code string) {
	if p != nil {
		p.agent.Inc(seat, code)
	}
}

// WriteTo writes the process counters.
func (p *Process) WriteTo(w *Writer) {
	w.counters("bookwarden_source_lookups_total", "Source lookups by source and outcome since the process started.", p.sources)
	w.counters("bookwarden_agent_calls_total", "Calls to agent seats by seat and error code (empty for success) since the process started.", p.agent)
	w.histogram("bookwarden_source_request_seconds", "How long live source lookups took, by source, since the process started.", p.requests)
}

// Writer writes the text exposition format. The first write error is kept
// and later writes are skipped.
type Writer struct {
	w   io.Writer
	err error
}

// NewWriter returns a Writer over w.
func NewWriter(w io.Writer) *Writer { return &Writer{w: w} }

// Err returns the first write error.
func (w *Writer) Err() error { return w.err }

func (w *Writer) printf(format string, args ...any) {
	if w.err == nil {
		_, w.err = fmt.Fprintf(w.w, format, args...)
	}
}

// Family starts a metric family: its HELP and TYPE lines.
func (w *Writer) Family(name, help, typ string) {
	w.printf("# HELP %s %s\n# TYPE %s %s\n", name, escapeHelp(help), name, typ)
}

// Sample writes one series. kv is label name, value pairs.
func (w *Writer) Sample(name string, value float64, kv ...string) {
	var b strings.Builder
	b.WriteString(name)
	if len(kv) > 0 {
		b.WriteByte('{')
		for i := 0; i+1 < len(kv); i += 2 {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(kv[i])
			b.WriteString(`="`)
			b.WriteString(escapeLabel(kv[i+1]))
			b.WriteByte('"')
		}
		b.WriteByte('}')
	}
	w.printf("%s %s\n", b.String(), formatValue(value))
}

func (w *Writer) counters(name, help string, c *Counters) {
	w.Family(name, help, "counter")
	for _, s := range c.snapshot() {
		kv := make([]string, 0, 2*len(c.labels))
		for i, l := range c.labels {
			kv = append(kv, l, s.values[i])
		}
		w.Sample(name, float64(s.n), kv...)
	}
}

func (w *Writer) histogram(name, help string, h *Histogram) {
	w.Family(name, help, "histogram")
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, value := range slices.Sorted(maps.Keys(h.m)) {
		s := h.m[value]
		var cum int64
		for i, le := range h.buckets {
			cum += s.counts[i]
			w.Sample(name+"_bucket", float64(cum), h.label, value, "le", formatValue(le))
		}
		w.Sample(name+"_bucket", float64(s.n), h.label, value, "le", "+Inf")
		w.Sample(name+"_sum", s.sum, h.label, value)
		w.Sample(name+"_count", float64(s.n), h.label, value)
	}
}

var (
	helpEscaper  = strings.NewReplacer(`\`, `\\`, "\n", `\n`)
	labelEscaper = strings.NewReplacer(`\`, `\\`, "\n", `\n`, `"`, `\"`)
)

func escapeHelp(s string) string  { return helpEscaper.Replace(s) }
func escapeLabel(s string) string { return labelEscaper.Replace(s) }

func formatValue(v float64) string {
	if v == math.Trunc(v) && math.Abs(v) < 1e15 {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}
