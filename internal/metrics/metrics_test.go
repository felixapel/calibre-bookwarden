package metrics_test

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/metrics"
)

func TestProcessCountersAreKeyedByLabelSet(t *testing.T) {
	p := metrics.New()
	p.SourceLookup("dnb", "ok")
	p.SourceLookup("dnb", "ok")
	p.SourceLookup("dnb", "unavailable")
	p.SourceLookup("bne", "ok")
	p.AgentCall("haiku", "")
	p.AgentCall("haiku", "timeout")
	p.AgentCall("haiku", "timeout")

	var sb strings.Builder
	p.WriteTo(metrics.NewWriter(&sb))
	want := []string{
		`bookwarden_source_lookups_total{source="bne",outcome="ok"} 1`,
		`bookwarden_source_lookups_total{source="dnb",outcome="ok"} 2`,
		`bookwarden_source_lookups_total{source="dnb",outcome="unavailable"} 1`,
		`bookwarden_agent_calls_total{seat="haiku",code=""} 1`,
		`bookwarden_agent_calls_total{seat="haiku",code="timeout"} 2`,
		"# TYPE bookwarden_source_lookups_total counter",
		"# TYPE bookwarden_agent_calls_total counter",
	}
	for _, line := range want {
		if !strings.Contains(sb.String(), line+"\n") {
			t.Errorf("missing %q in:\n%s", line, sb.String())
		}
	}
}

func TestCountersAreSafeForConcurrentUse(t *testing.T) {
	p := metrics.New()
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			for range 100 {
				p.SourceLookup("dnb", "ok")
			}
		})
	}
	wg.Wait()
	var sb strings.Builder
	p.WriteTo(metrics.NewWriter(&sb))
	if !strings.Contains(sb.String(), `bookwarden_source_lookups_total{source="dnb",outcome="ok"} 5000`+"\n") {
		t.Errorf("lost increments:\n%s", sb.String())
	}
}

func TestWriterEscapesLabelValues(t *testing.T) {
	var sb strings.Builder
	w := metrics.NewWriter(&sb)
	w.Family("bookwarden_x", "A help with a \\ and\na newline.", "gauge")
	w.Sample("bookwarden_x", 3, "a", "quote \" back \\ line \n end")
	w.Sample("bookwarden_x", 1.5)
	want := "# HELP bookwarden_x A help with a \\\\ and\\na newline.\n" +
		"# TYPE bookwarden_x gauge\n" +
		`bookwarden_x{a="quote \" back \\ line \n end"} 3` + "\n" +
		"bookwarden_x 1.5\n"
	if sb.String() != want {
		t.Errorf("got:\n%s\nwant:\n%s", sb.String(), want)
	}
}

func TestIncWithTheWrongNumberOfLabelsPanics(t *testing.T) {
	c := metrics.NewCounters("a", "b")
	defer func() {
		if recover() == nil {
			t.Error("Inc with one value did not panic")
		}
	}()
	c.Inc("only")
}

func TestNilProcessIsAUsableRecorder(_ *testing.T) {
	var p *metrics.Process
	p.SourceLookup("dnb", "ok")
	p.AgentCall("haiku", "")
}

// TestSourceRequestsAreAHistogram: live lookups are timed per source into
// cumulative buckets, with their sum and count.
func TestSourceRequestsAreAHistogram(t *testing.T) {
	p := metrics.New()
	p.SourceRequest("dnb", 300*time.Millisecond)
	p.SourceRequest("dnb", 4*time.Second)
	p.SourceRequest("bne", 50*time.Millisecond)

	var sb strings.Builder
	p.WriteTo(metrics.NewWriter(&sb))
	want := []string{
		"# TYPE bookwarden_source_request_seconds histogram",
		`bookwarden_source_request_seconds_bucket{source="bne",le="0.1"} 1`,
		`bookwarden_source_request_seconds_bucket{source="dnb",le="0.25"} 0`,
		`bookwarden_source_request_seconds_bucket{source="dnb",le="0.5"} 1`,
		`bookwarden_source_request_seconds_bucket{source="dnb",le="5"} 2`,
		`bookwarden_source_request_seconds_bucket{source="dnb",le="+Inf"} 2`,
		`bookwarden_source_request_seconds_sum{source="dnb"} 4.3`,
		`bookwarden_source_request_seconds_count{source="dnb"} 2`,
		`bookwarden_source_request_seconds_count{source="bne"} 1`,
	}
	for _, line := range want {
		if !strings.Contains(sb.String(), line+"\n") {
			t.Errorf("missing %q in:\n%s", line, sb.String())
		}
	}
}
