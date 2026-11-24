package main

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/agent"
	"github.com/felixapel/calibre-bookwarden/internal/config"
)

// Each seat gets its own client, and all seats record into one Quotas that
// the Service reads (ADR-033).
func TestBuildSeats(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	cfg := config.AgentConfig{Seats: []agent.SeatConfig{
		// Allowed to reach loopback (standing in for the LAN) over http.
		{Name: "haiku", Wire: "openai", URL: srv.URL, Model: "m", Key: "k1", Family: "anthropic", AllowCIDR: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}},
		// Same host and port, but no allowance: the dial must be refused.
		{Name: "deepseek", Wire: "openai", URL: "https://" + u.Host + "/v1", Model: "m", Key: "k2", Family: "deepseek"},
		// Plain http without an allowance: not usable.
		{Name: "gemini", Wire: "openai", URL: "http://10.0.0.122:8766/v1", Model: "m", Key: "k3", Family: "google"},
	}}
	var logs bytes.Buffer
	seats, status, quotas := buildSeats(cfg, slog.New(slog.NewTextHandler(&logs, nil)))
	if len(seats) != 2 || len(status) != 3 || status[2].Configured || status[0].Family != "anthropic" {
		t.Fatalf("seats = %d, status = %+v", len(seats), status)
	}
	if !strings.Contains(logs.String(), "seat=haiku") || !strings.Contains(logs.String(), "plain http") || strings.Contains(logs.String(), "k1") {
		t.Fatalf("log = %s", logs.String())
	}
	if _, err := seats[1].Judge(context.Background(), "B", "P"); agent.ErrorCode(err) != "refused_host" {
		t.Fatalf("the deepseek seat reached the host the haiku seat is allowed to: %v", err)
	}
	if hits.Load() != 0 {
		t.Fatal("the seat without an allowance reached the server")
	}
	if _, err := seats[0].Judge(context.Background(), "B", "P"); agent.ErrorCode(err) != "quota_exhausted" {
		t.Fatalf("haiku: %v", err)
	}
	// The service reads the quota the seat recorded.
	got := agent.NewService(agent.ServiceOptions{Seats: status, Quotas: quotas}).Seats()
	if got[0].QuotaAt.IsZero() || !got[1].QuotaAt.IsZero() {
		t.Fatalf("quota on Settings: %+v", got)
	}
}
