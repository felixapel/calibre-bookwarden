package main

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/agent"
	"github.com/felixapel/calibre-bookwarden/internal/extractor"
)

// echoSeat claims the first line of the excerpt as the title, and counts calls.
type echoSeat struct {
	calls *atomic.Int64
	model string
}

func (echoSeat) Name() string    { return "x" }
func (s echoSeat) Model() string { return s.model }
func (s echoSeat) Judge(_ context.Context, _, packet string) (agent.Reply, error) {
	s.calls.Add(1)
	lines := strings.Split(packet, "\n")
	title := lines[2] // after the fence sentence and the opening marker
	return agent.Reply{Text: `{"claims":[{"field":"title","value":"` + title + `","quote":"` + title + `"}]}`, InputTokens: 10, OutputTokens: 5, Model: s.model}, nil
}

func TestExtractBenchSynthetic(t *testing.T) {
	var calls atomic.Int64
	newBenchExtractor = func(cfg agent.SeatConfig, _ *http.Client) (extractor.Extractor, error) {
		return extractor.NewWithSeat(cfg.Name, cfg.Family, cfg.Model, cfg.Effort, echoSeat{&calls, cfg.Model}), nil
	}
	t.Cleanup(func() { newBenchExtractor = defaultBenchExtractor })
	env := map[string]string{"BOOKWARDEN_EXTRACTOR_KEY": "k"}
	var out, errb bytes.Buffer
	code := run(context.Background(), []string{"extract-bench", "--synthetic", "6", "--extractors", "luna,glm", "--efforts", "low,high", "--effort", "glm=none", "--max-calls", "100"},
		func(k string) string { return env[k] }, nil, &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	// luna at low and high, glm at none: three cells of six calls.
	if n := calls.Load(); n != 18 {
		t.Errorf("calls = %d", n)
	}
	for _, want := range []string{"luna/low", "luna/high", "glm/none", "suggested efforts:", "luna=low", "glm=none"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	// Counts only: no excerpt text reaches the output.
	if strings.Contains(out.String(), "Copyright") || strings.Contains(out.String(), "ISBN") {
		t.Errorf("output carries excerpt text:\n%s", out.String())
	}
}

func TestExtractBenchUsage(t *testing.T) {
	var out, errb bytes.Buffer
	env := map[string]string{"BOOKWARDEN_EXTRACTOR_KEY": "k"}
	get := func(k string) string { return env[k] }
	for _, args := range [][]string{
		{"extract-bench", "--real", "5"}, // real samples need --run
		{"extract-bench", "--effort", "nope"},
		{"extract-bench", "--effort", "unknown=low"},
		{"extract-bench", "--max-calls", "0"},
		{"extract-bench", "--candidate", "luna"},
		{"extract-bench", "--candidate", "luna=opencode-go/gpt-6-luna"},
		{"extract-bench", "--candidate", "luna=:openai"},
		{"extract-bench", "--candidate", "a=m:x", "--candidate", "a=n:y"},
	} {
		if code := run(context.Background(), args, get, nil, &out, &errb); code != 2 {
			t.Errorf("%v: exit %d", args, code)
		}
	}
	if code := run(context.Background(), []string{"extract-bench"}, func(string) string { return "" }, nil, &out, &errb); code != 1 {
		t.Errorf("no keys: exit %d", code)
	}
}

func TestExtractBenchCandidates(t *testing.T) {
	var calls atomic.Int64
	var cfgs []agent.SeatConfig
	newBenchExtractor = func(cfg agent.SeatConfig, _ *http.Client) (extractor.Extractor, error) {
		cfgs = append(cfgs, cfg)
		return extractor.NewWithSeat(cfg.Name, cfg.Family, cfg.Model, cfg.Effort, echoSeat{&calls, cfg.Model}), nil
	}
	t.Cleanup(func() { newBenchExtractor = defaultBenchExtractor })
	env := map[string]string{"BOOKWARDEN_EXTRACTOR_BIFROST_URL": "http://127.0.0.1:8082", "BOOKWARDEN_EXTRACTOR_ALLOW_CIDR": "127.0.0.1/32"}
	var out, errb bytes.Buffer
	code := run(context.Background(), []string{"extract-bench", "--synthetic", "2",
		"--candidate", "luna=opencode-go/gpt-6-luna:openai", "--candidate", "mimo=opencode-go/mimo-v2.6-flash:xiaomi", "--effort", "luna=low"},
		func(k string) string { return env[k] }, nil, &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	// Only the candidates run, on the gateway, with their model ids as given.
	if len(cfgs) != 2 || calls.Load() != 4 {
		t.Fatalf("configs = %+v, calls = %d", cfgs, calls.Load())
	}
	for i, w := range []struct{ name, model, family string }{{"luna", "opencode-go/gpt-6-luna", "openai"}, {"mimo", "opencode-go/mimo-v2.6-flash", "xiaomi"}} {
		c := cfgs[i]
		if c.Name != w.name || c.Model != w.model || c.Family != w.family || c.URL != "http://127.0.0.1:8082/v1/chat/completions" ||
			!c.Keyless || c.SessionHeader != "x-bf-eh-x-opencode-session" || len(c.AllowCIDR) != 1 {
			t.Errorf("candidate %d = %+v", i, c)
		}
	}
	// Agreement is shown for every pair of extractors, whatever their efforts.
	if !strings.Contains(out.String(), "luna/low") || !strings.Contains(out.String(), "agreement luna/low ~ mimo/default: 100.0%") {
		t.Errorf("output:\n%s", out.String())
	}
}

func TestExtractBenchWarnsWhenTheBudgetRunsOut(t *testing.T) {
	var calls atomic.Int64
	newBenchExtractor = func(cfg agent.SeatConfig, _ *http.Client) (extractor.Extractor, error) {
		return extractor.NewWithSeat(cfg.Name, cfg.Family, cfg.Model, cfg.Effort, echoSeat{&calls, cfg.Model}), nil
	}
	t.Cleanup(func() { newBenchExtractor = defaultBenchExtractor })
	env := map[string]string{"BOOKWARDEN_EXTRACTOR_KEY": "k"}
	var out, errb bytes.Buffer
	if code := run(context.Background(), []string{"extract-bench", "--synthetic", "4", "--extractors", "luna,glm", "--max-calls", "6"},
		func(k string) string { return env[k] }, nil, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "warning: the call budget ran out: 2 calls skipped") {
		t.Errorf("output:\n%s", out.String())
	}
}
