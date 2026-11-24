package web

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// lockedBuffer is a log sink the server's goroutines and the test can share.
type lockedBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// TestReportErrorLogsTheCause: an unexpected report failure shows the user
// a generic page, and the log names the underlying error, the path and the
// status, and never request values.
func TestReportErrorLogsTheCause(t *testing.T) {
	logs := &lockedBuffer{}
	f := &fakeReports{err: errors.New("store: reading projection: connection reset by peer")}
	e := newEnv(t, func(o *Options) {
		o.Reports = f
		o.Logger = slog.New(slog.NewJSONHandler(logs, nil))
	})
	e.setUpAdmin(t)
	e.logIn(t)
	e.runs.add(running(7))
	r := e.do(t, http.MethodGet, "/runs/7/books/2", nil, nil)
	if r.status != http.StatusInternalServerError || !strings.Contains(r.body, "Report unavailable") {
		t.Fatalf("status %d, body %q", r.status, r.body)
	}
	if strings.Contains(r.body, "connection reset") {
		t.Errorf("the page shows the cause: %q", r.body)
	}
	got := logs.String()
	for _, want := range []string{`"msg":"report unavailable"`, "connection reset by peer", `"path":"/runs/7/books/2"`, `"status":500`} {
		if !strings.Contains(got, want) {
			t.Errorf("log lacks %s:\n%s", want, got)
		}
	}
}

// TestReportErrorNeverLogsRequestValues: a query value, an unknown query
// parameter and a form value reach reportError's callers; none reaches the
// log, which carries the method and the path only.
func TestReportErrorNeverLogsRequestValues(t *testing.T) {
	logs := &lockedBuffer{}
	f := &fakeReports{err: errors.New("store: down")}
	e := newEnv(t, func(o *Options) {
		o.Reports = f
		o.Logger = slog.New(slog.NewJSONHandler(logs, nil))
	})
	e.setUpAdmin(t)
	e.logIn(t)
	e.runs.add(running(7))
	token := e.csrfToken(t)

	// A valid query value on a route that fails with a 500.
	if r := e.do(t, http.MethodGet, "/runs/7/findings?book_id=424242", nil, nil); r.status != http.StatusInternalServerError {
		t.Fatalf("GET with a query: %d", r.status)
	}
	// An unknown parameter is refused before any report call.
	if r := e.do(t, http.MethodGet, "/runs/7/books/2?f=secret", nil, nil); r.status != http.StatusBadRequest {
		t.Fatalf("GET with ?f=secret: %d", r.status)
	}
	// A POST form value on a route that fails with a 500.
	form := url.Values{"csrf_token": {token}, "note": {"form-secret-value"}}
	if r := e.do(t, http.MethodPost, "/runs/7/books/2/recheck", form, nil); r.status != http.StatusInternalServerError {
		t.Fatalf("POST with a form: %d", r.status)
	}
	got := logs.String()
	if !strings.Contains(got, `"method":"POST"`) || !strings.Contains(got, `"path":"/runs/7/books/2/recheck"`) {
		t.Errorf("the POST failure was not logged with method and path:\n%s", got)
	}
	for _, secret := range []string{"424242", "secret", "form-secret-value", token} {
		if strings.Contains(got, secret) {
			t.Errorf("the log carries %q:\n%s", secret, got)
		}
	}
}
