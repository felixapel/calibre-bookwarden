package web

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/metrics"
)

const metricsToken = "scrape-token-for-tests"

func metricsOptions(o *Options) {
	o.MetricsToken = metricsToken
	o.Metrics = func(_ context.Context, w *metrics.Writer) error {
		w.Family("bookwarden_test", "A test metric.", "gauge")
		w.Sample("bookwarden_test", 7)
		return nil
	}
}

func scrape(t *testing.T, e *testEnv, auth, host string) response {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, e.srv.URL+"/metrics", nil)
	if err != nil {
		t.Fatal(err)
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	if host != "" {
		req.Host = host
	}
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response{status: resp.StatusCode, header: resp.Header, body: string(b)}
}

func TestMetricsIsOffWithoutAToken(t *testing.T) {
	e := newEnv(t)
	for _, auth := range []string{"", "Bearer " + metricsToken, "Bearer "} {
		if r := scrape(t, e, auth, ""); r.status != http.StatusNotFound {
			t.Errorf("/metrics with no token configured, Authorization %q: %d, want 404", auth, r.status)
		}
	}
}

func TestMetricsNeedsTheBearerToken(t *testing.T) {
	e := newEnv(t, metricsOptions)
	tests := []struct {
		name string
		auth string
		want int
	}{
		{"no header", "", http.StatusUnauthorized},
		{"wrong token", "Bearer nope", http.StatusUnauthorized},
		{"a prefix of the token", "Bearer " + metricsToken[:5], http.StatusUnauthorized},
		{"the token and more", "Bearer " + metricsToken + "x", http.StatusUnauthorized},
		{"empty token", "Bearer ", http.StatusUnauthorized},
		{"basic auth with the token", "Basic " + metricsToken, http.StatusUnauthorized},
		{"the token without a scheme", metricsToken, http.StatusUnauthorized},
		{"the token", "Bearer " + metricsToken, http.StatusOK},
		{"scheme in any case", "bearer " + metricsToken, http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := scrape(t, e, tc.auth, "")
			if r.status != tc.want {
				t.Fatalf("status %d, want %d", r.status, tc.want)
			}
			if tc.want == http.StatusUnauthorized {
				if r.header.Get("WWW-Authenticate") == "" || strings.Contains(r.body, "bookwarden_test") {
					t.Errorf("a refusal must challenge and show no metrics: %v %q", r.header, r.body)
				}
				return
			}
			if got := r.header.Get("Content-Type"); got != "text/plain; version=0.0.4; charset=utf-8" {
				t.Errorf("Content-Type = %q", got)
			}
			if !strings.Contains(r.body, "bookwarden_test 7\n") || !strings.Contains(r.body, "# TYPE bookwarden_test gauge\n") {
				t.Errorf("body = %q", r.body)
			}
		})
	}
}

// A scraper reaches the container by any name, and no session is needed.
func TestMetricsSkipsHostAndSetupChecks(t *testing.T) {
	e := newEnv(t, metricsOptions)
	if r := scrape(t, e, "Bearer "+metricsToken, "bookwarden:8080"); r.status != http.StatusOK {
		t.Fatalf("/metrics before setup, foreign Host: %d", r.status)
	}
}

// TestMetricsRefusalsLeaveATrace: a wrong token is logged like any failed
// request (never the token); a good scrape every 15 s is not.
func TestMetricsRefusalsLeaveATrace(t *testing.T) {
	logs := &lockedBuffer{}
	e := newEnv(t, metricsOptions, func(o *Options) { o.Logger = slog.New(slog.NewJSONHandler(logs, nil)) })
	scrape(t, e, "Bearer "+metricsToken, "")
	if strings.Contains(logs.String(), `"path":"/metrics"`) {
		t.Errorf("a successful scrape was logged:\n%s", logs.String())
	}
	scrape(t, e, "Bearer wrong-token-value", "")
	scrape(t, e, "", "")
	got := logs.String()
	if strings.Count(got, `"path":"/metrics"`) != 2 || !strings.Contains(got, `"status":401`) {
		t.Errorf("the two refusals were not logged:\n%s", got)
	}
	for _, secret := range []string{metricsToken, "wrong-token-value"} {
		if strings.Contains(got, secret) {
			t.Errorf("the log carries a token:\n%s", got)
		}
	}
	// The liveness and readiness probes stay quiet, failures or not.
	e.do(t, http.MethodGet, "/healthz", nil, nil)
	e.do(t, http.MethodGet, "/readyz", nil, nil)
	if strings.Contains(logs.String(), `/healthz`) || strings.Contains(logs.String(), `/readyz`) {
		t.Errorf("a probe was logged:\n%s", logs.String())
	}
}

func TestMetricsFailureIsGenericAndLogged(t *testing.T) {
	logs := &lockedBuffer{}
	e := newEnv(t, metricsOptions, func(o *Options) {
		o.Logger = slog.New(slog.NewJSONHandler(logs, nil))
		o.Metrics = func(_ context.Context, w *metrics.Writer) error {
			w.Sample("bookwarden_partial", 1)
			return errors.New("store: metrics runs by status: connection refused")
		}
	})
	r := scrape(t, e, "Bearer "+metricsToken, "")
	if r.status != http.StatusInternalServerError || strings.Contains(r.body, "bookwarden_partial") || strings.Contains(r.body, "refused") {
		t.Fatalf("failed scrape: %d %q", r.status, r.body)
	}
	if !strings.Contains(logs.String(), "connection refused") || strings.Contains(logs.String(), metricsToken) {
		t.Errorf("log = %s", logs.String())
	}
}

func TestMetricsNeedsAWriterWithTheToken(t *testing.T) {
	_, err := New(Options{
		AllowedHosts: []string{"127.0.0.1"}, Auth: newEnv(t).svc, Health: func(context.Context) error { return nil },
		Logger: slog.New(slog.DiscardHandler), Runs: newFakeRuns(), Reports: &fakeReports{}, Fixing: &fakeFixing{},
		MetricsToken: metricsToken,
	})
	if err == nil {
		t.Fatal("New accepted a metrics token without a Metrics writer")
	}
}
