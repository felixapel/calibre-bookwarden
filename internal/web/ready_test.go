package web

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"
)

func readyCheck(name string, err error) ReadyCheck {
	return ReadyCheck{Name: name, Check: func(context.Context) error { return err }}
}

func TestReadyz(t *testing.T) {
	secretErr := errors.New(`dial postgres://bookwarden:db-secret@postgresql18:5432/bookwarden: refused`)
	tests := []struct {
		name       string
		checks     []ReadyCheck
		wantStatus int
		wantBody   string
	}{
		{"nothing to check", nil, http.StatusOK, `{"status":"ok"}`},
		{"every check passes", []ReadyCheck{readyCheck("database", nil), readyCheck("migrations", nil), readyCheck("calibre", nil)}, http.StatusOK, `{"status":"ok"}`},
		{"calibre down", []ReadyCheck{readyCheck("database", nil), readyCheck("calibre", secretErr)}, http.StatusServiceUnavailable, `{"status":"unavailable","failing":["calibre"]}`},
		{"two fail, in declared order", []ReadyCheck{readyCheck("database", secretErr), readyCheck("migrations", nil), readyCheck("calibre", secretErr)}, http.StatusServiceUnavailable, `{"status":"unavailable","failing":["database","calibre"]}`},
		{"a check that panics fails", []ReadyCheck{{Name: "calibre", Check: func(context.Context) error { panic("boom") }}}, http.StatusServiceUnavailable, `{"status":"unavailable","failing":["calibre"]}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, func(o *Options) { o.Ready = tc.checks })
			r := e.do(t, http.MethodGet, "/readyz", nil, nil)
			if r.status != tc.wantStatus || strings.TrimSpace(r.body) != tc.wantBody {
				t.Fatalf("/readyz: %d %q, want %d %q", r.status, r.body, tc.wantStatus, tc.wantBody)
			}
			if got := r.header.Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
				t.Errorf("Content-Type = %q", got)
			}
			if got := r.header.Get("Cache-Control"); got != "no-store" {
				t.Errorf("Cache-Control = %q", got)
			}
			for _, leak := range []string{"db-secret", "postgres://", "refused"} {
				if strings.Contains(r.body, leak) {
					t.Errorf("body leaks %q: %s", leak, r.body)
				}
			}
		})
	}
}

// TestReadyzIsOpenLikeHealthz: no session, no setup redirect and no allowed
// host check, so a probe on any address works.
func TestReadyzIsOpenLikeHealthz(t *testing.T) {
	e := newEnv(t, func(o *Options) { o.Ready = []ReadyCheck{readyCheck("database", nil)} })
	// Setup isn't done, and the Host is not in AllowedHosts.
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, e.srv.URL+"/readyz", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "probe.invalid"
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/readyz with a foreign Host before setup: %d", resp.StatusCode)
	}
	// A page, by contrast, is refused for that Host.
	req, _ = http.NewRequestWithContext(context.Background(), http.MethodGet, e.srv.URL+"/login", nil)
	req.Host = "probe.invalid"
	resp2, err := e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusMisdirectedRequest {
		t.Fatalf("/login with a foreign Host: %d, want 421", resp2.StatusCode)
	}
}

func TestReadyzDoesNotChangeHealthz(t *testing.T) {
	e := newEnv(t, func(o *Options) { o.Ready = []ReadyCheck{readyCheck("calibre", errors.New("down"))} })
	if r := e.do(t, http.MethodGet, "/healthz", nil, nil); r.status != http.StatusOK || strings.TrimSpace(r.body) != "ok" {
		t.Fatalf("/healthz with Calibre down: %d %q, want liveness only", r.status, r.body)
	}
}

func TestReadyzBoundsEachCheck(t *testing.T) {
	// A check that ignores its context is given up on at the deadline.
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	slow := ReadyCheck{Name: "database", Check: func(context.Context) error {
		<-release
		return nil
	}}
	e := newEnv(t, func(o *Options) { o.Ready = []ReadyCheck{slow}; o.ReadyTimeout = 30 * time.Millisecond })
	start := time.Now()
	r := e.do(t, http.MethodGet, "/readyz", nil, nil)
	if r.status != http.StatusServiceUnavailable || !strings.Contains(r.body, `"database"`) {
		t.Fatalf("/readyz with a stuck check: %d %q", r.status, r.body)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("/readyz took %v", time.Since(start))
	}
}

func TestReadyzLogsTheCauseNotTheResponse(t *testing.T) {
	logs := &lockedBuffer{}
	e := newEnv(t, func(o *Options) {
		o.Ready = []ReadyCheck{readyCheck("calibre", errors.New("calibre: unreachable"))}
		o.Logger = slog.New(slog.NewJSONHandler(logs, nil))
	})
	e.do(t, http.MethodGet, "/readyz", nil, nil)
	if !strings.Contains(logs.String(), "calibre: unreachable") || !strings.Contains(logs.String(), `"check":"calibre"`) {
		t.Errorf("the failing check was not logged:\n%s", logs.String())
	}
}
