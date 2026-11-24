//go:build live

package agent_test

import (
	"context"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/agent"
	"github.com/felixapel/calibre-bookwarden/internal/config"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

// TestLiveSeats sends one synthetic packet to every seat whose key is set
// (BOOKWARDEN_AGENT_*_KEY) and checks the answer parses. It pins each
// endpoint, model id and effort setting against the real API.
func TestLiveSeats(t *testing.T) {
	env := map[string]string{
		"BOOKWARDEN_DATABASE_URL": "postgres://u@localhost/db", "BOOKWARDEN_CALIBRE_URL": "http://calibre:8081",
		"BOOKWARDEN_CALIBRE_USER": "u", "BOOKWARDEN_CALIBRE_PASSWORD": "p", "BOOKWARDEN_ALLOWED_HOSTS": "localhost",
	}
	cfg, err := config.FromEnv(func(k string) string {
		if v, ok := env[k]; ok {
			return v
		}
		return os.Getenv(k)
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Agent.Seats) == 0 {
		t.Skip("no BOOKWARDEN_AGENT_*_KEY set")
	}
	sort := "Captive Mind, The"
	pkg := evidence.Package{Calibre: evidence.Record{Title: "The Captive Mind", Authors: []string{"Czesław Miłosz"},
		Languages: []string{"eng"}, Publisher: "Vintage", TitleSort: &sort},
		Formats: []evidence.FormatEvidence{{Format: "EPUB", Metadata: &evidence.FileMetadata{Title: "The Captive Mind", Publisher: "Vintage International"}}}}
	p, err := agent.BuildPacket(pkg, []evidence.Proposal{{Field: "publisher", Current: []string{"Vintage"}, Proposed: []string{"Vintage International"}, Roots: []string{"file"}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, sc := range cfg.Agent.Seats {
		t.Run(sc.Name, func(t *testing.T) {
			hc, err := agent.NewSeatClient(sc.URL, sc.AllowCIDR)
			if err != nil {
				t.Fatal(err)
			}
			s, err := agent.NewSeat(sc, hc)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			reply, err := s.Judge(ctx, agent.Brief(), p.Text)
			if err != nil {
				t.Fatalf("Judge: %s", agent.ErrorCode(err))
			}
			got, err := agent.ParseAnswer(reply.Text, p.Fields)
			if err != nil || reply.InputTokens == 0 || reply.OutputTokens == 0 {
				t.Fatalf("answer %v, tokens %d/%d", err, reply.InputTokens, reply.OutputTokens)
			}
			t.Logf("%s: %s (%d in, %d out)", sc.Model, got["publisher"].Answer, reply.InputTokens, reply.OutputTokens)
		})
	}
}

// TestLiveProxy probes an OpenAI-compatible proxy on the LAN (ADR-033): it
// lists /v1/models and sends one tiny synthetic chat completion through a
// seat built with an exact-CIDR allowance for the proxy's address.
// BOOKWARDEN_LIVE_PROXY_URL is the proxy's base URL (an IP address, such as
// http://192.0.2.122:8766), BOOKWARDEN_LIVE_PROXY_KEY its key, and
// BOOKWARDEN_LIVE_PROXY_MODEL the model to ask (default gemini-3.8-flash-high).
func TestLiveProxy(t *testing.T) {
	base, key := strings.TrimRight(os.Getenv("BOOKWARDEN_LIVE_PROXY_URL"), "/"), os.Getenv("BOOKWARDEN_LIVE_PROXY_KEY")
	if base == "" || key == "" {
		t.Skip("BOOKWARDEN_LIVE_PROXY_URL and BOOKWARDEN_LIVE_PROXY_KEY not set")
	}
	base = strings.TrimSuffix(strings.TrimSuffix(base, "/chat/completions"), "/v1")
	model := os.Getenv("BOOKWARDEN_LIVE_PROXY_MODEL")
	if model == "" {
		model = "gemini-3.8-flash-high"
	}
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	addr, err := netip.ParseAddr(u.Hostname())
	if err != nil {
		t.Fatalf("BOOKWARDEN_LIVE_PROXY_URL must name the proxy by IP address: %v", err)
	}
	allow := []netip.Prefix{netip.PrefixFrom(addr, addr.BitLen())}
	chat := base + "/v1/chat/completions"
	hc, err := agent.NewSeatClient(chat, allow)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/models", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatalf("GET /v1/models: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/models: status %d", resp.StatusCode)
	}

	s, err := agent.NewSeat(agent.SeatConfig{Name: "proxy", Wire: "openai", URL: chat, Model: model, Key: key, AllowCIDR: allow}, hc)
	if err != nil {
		t.Fatal(err)
	}
	reply, err := s.Judge(ctx, "Answer with the single word: ok.", "ping")
	if err != nil {
		t.Fatalf("Judge: %s", agent.ErrorCode(err))
	}
	t.Logf("%s: %d chars (%d in, %d out)", model, len(reply.Text), reply.InputTokens, reply.OutputTokens)
}
