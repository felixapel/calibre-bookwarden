package heartbeat

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

var loopback = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}

const token = "s3cret-push-token"

// recorder is a Kuma stand-in: it answers each request with the next status
// of codes (the last one repeats) and keeps what it was asked.
type recorder struct {
	mu    sync.Mutex
	codes []int
	got   []*http.Request
}

func (r *recorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, req)
	code := r.codes[min(len(r.got), len(r.codes))-1]
	w.WriteHeader(code)
}

func newSender(t *testing.T, rawURL string, allow []netip.Prefix) (*Sender, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	s, err := New(rawURL, allow, slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	s.retryWait = time.Millisecond
	return s, &logs
}

func TestBeatDecision(t *testing.T) {
	tests := []struct {
		name    string
		r       Result
		up      bool
		wantMsg string
	}{
		{"clean run", Result{RunID: 15, Status: verify.RunCompleted, SealsChecked: true, Books: 3207, Calls: 2950, Failed: 12}, true,
			"run 15 completed: 3207 books, 0 broken seals, extractor failures 0.41 % (12/2950 not answered)"},
		{"no extraction reports no rate", Result{RunID: 2, Status: verify.RunCompleted, SealsChecked: true, Books: 10}, true,
			"run 2 completed: 10 books, 0 broken seals"},
		{"exactly 5 % is still up", Result{RunID: 3, Status: verify.RunCompleted, SealsChecked: true, Books: 1, Calls: 100, Failed: 5}, true, "5.00 %"},
		{"just over 5 % shows as over", Result{RunID: 9, Status: verify.RunCompleted, SealsChecked: true, Books: 1, Calls: 2000, Failed: 101}, false, "5.05 % (101/2000 not answered)"},
		{"over 5 % is down", Result{RunID: 4, Status: verify.RunCompleted, SealsChecked: true, Books: 1, Calls: 207, Failed: 61}, false, "29.47 % (61/207 not answered)"},
		// The 5 % rule needs 20 calls: one unanswered call of a one-book
		// recheck isn't an outage.
		{"one of two calls failed is up", Result{RunID: 10, Status: verify.RunCompleted, SealsChecked: true, Books: 1, Calls: 2, Failed: 1}, true, "50.00 % (1/2 not answered)"},
		{"two of twenty is down", Result{RunID: 11, Status: verify.RunCompleted, SealsChecked: true, Books: 3, Calls: 20, Failed: 2}, false, "10.00 %"},
		{"one of twenty is up", Result{RunID: 12, Status: verify.RunCompleted, SealsChecked: true, Books: 3, Calls: 20, Failed: 1}, true, "5.00 %"},
		{"broken seals are down", Result{RunID: 5, Status: verify.RunCompleted, SealsChecked: true, Books: 9, Broken: 2}, false, "2 broken seals"},
		{"unchecked seals are down", Result{RunID: 6, Status: verify.RunCompleted}, false, "run 6 completed: seals not checked"},
		{"failed run is down", Result{RunID: 7, Status: verify.RunFailed, SealsChecked: true}, false, "run 7 failed"},
		{"cancelled run is down", Result{RunID: 8, Status: verify.RunCancelled, SealsChecked: true}, false, "run 8 cancelled"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := tc.r.Beat()
			if b.Up != tc.up {
				t.Errorf("Up = %v, want %v (%q)", b.Up, tc.up, b.Msg)
			}
			if !strings.Contains(b.Msg, tc.wantMsg) {
				t.Errorf("msg %q lacks %q", b.Msg, tc.wantMsg)
			}
			if len(b.Msg) > maxMessage {
				t.Errorf("msg is %d chars", len(b.Msg))
			}
		})
	}
}

func TestSendQuery(t *testing.T) {
	tests := []struct {
		name       string
		beat       Beat
		wantStatus string
	}{
		{"up", Beat{Up: true, Msg: "run 1 completed: 3 books"}, "up"},
		{"down", Beat{Msg: "run 2 failed"}, "down"},
		{"down for broken seals", Result{RunID: 3, Status: verify.RunCompleted, SealsChecked: true, Books: 4, Broken: 1}.Beat(), "down"},
		{"down for extractor failures", Result{RunID: 4, Status: verify.RunCompleted, SealsChecked: true, Books: 4, Calls: 20, Failed: 3}.Beat(), "down"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{codes: []int{200}}
			srv := httptest.NewServer(rec)
			defer srv.Close()
			// The push URL's own query is kept.
			s, _ := newSender(t, srv.URL+"/api/push/"+token+"?keep=1", loopback)
			s.Send(context.Background(), tc.beat)
			if len(rec.got) != 1 {
				t.Fatalf("%d requests, want 1", len(rec.got))
			}
			req := rec.got[0]
			q := req.URL.Query()
			if req.Method != http.MethodGet || req.URL.Path != "/api/push/"+token {
				t.Errorf("%s %s", req.Method, req.URL.Path)
			}
			if q.Get("status") != tc.wantStatus || q.Get("msg") != tc.beat.Msg || !q.Has("ping") || q.Get("keep") != "1" {
				t.Errorf("query = %v", q)
			}
		})
	}
}

func TestSendRetriesOnceThenGivesUp(t *testing.T) {
	tests := []struct {
		name         string
		codes        []int
		wantRequests int
		wantWarning  bool
	}{
		{"first try works", []int{200}, 1, false},
		{"retry works", []int{500, 200}, 2, false},
		{"gives up after one retry", []int{500, 404}, 2, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{codes: tc.codes}
			srv := httptest.NewServer(rec)
			defer srv.Close()
			s, logs := newSender(t, srv.URL+"/api/push/"+token, loopback)
			s.Send(context.Background(), Beat{Up: true, Msg: "m"})
			if len(rec.got) != tc.wantRequests {
				t.Errorf("%d requests, want %d", len(rec.got), tc.wantRequests)
			}
			if got := strings.Contains(logs.String(), "heartbeat not delivered"); got != tc.wantWarning {
				t.Errorf("warning logged = %v, want %v: %s", got, tc.wantWarning, logs.String())
			}
		})
	}
}

func TestLogsNeverCarryThePushPath(t *testing.T) {
	rec := &recorder{codes: []int{503}}
	srv := httptest.NewServer(rec)
	// A refusing server, a closed one and one the dial check forbids.
	closed := httptest.NewServer(rec)
	closedURL := closed.URL
	closed.Close()
	cases := map[string]struct {
		url   string
		allow []netip.Prefix
	}{
		"http error":       {srv.URL, loopback},
		"connection error": {closedURL, loopback},
		"refused address":  {srv.URL, []netip.Prefix{netip.MustParsePrefix("10.9.9.9/32")}},
	}
	defer srv.Close()
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			s, logs := newSender(t, c.url+"/api/push/"+token+"?x=1", c.allow)
			s.Send(context.Background(), Beat{Up: true, Msg: "m"})
			out := logs.String()
			if !strings.Contains(out, "heartbeat not delivered") {
				t.Fatalf("no warning: %s", out)
			}
			if strings.Contains(out, token) || strings.Contains(out, "/api/push") || strings.Contains(out, "x=1") {
				t.Errorf("log leaks the push URL: %s", out)
			}
			if !strings.Contains(out, "host=http://127.0.0.1:") {
				t.Errorf("log lacks the host: %s", out)
			}
		})
	}
}

func TestDialsOnlyAllowedAddresses(t *testing.T) {
	rec := &recorder{codes: []int{200}}
	srv := httptest.NewServer(rec)
	defer srv.Close()
	// Loopback is private: without an allowance it is refused at dial time.
	s, err := New(strings.Replace(srv.URL, "http://", "https://", 1)+"/api/push/"+token, nil, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	s.retryWait = time.Millisecond
	if err := s.once(context.Background(), Beat{Up: true}); err == nil || !strings.Contains(err.Error(), "refused host") {
		t.Errorf("err = %v, want a refused host", err)
	}
	if _, err := New(srv.URL+"/x", nil, slog.Default()); err == nil {
		t.Error("plain http without an allowance was accepted")
	}
	if len(rec.got) != 0 {
		t.Errorf("%d requests reached a forbidden address", len(rec.got))
	}
}

func TestSendStopsWhenContextEnds(t *testing.T) {
	rec := &recorder{codes: []int{500}}
	srv := httptest.NewServer(rec)
	defer srv.Close()
	s, _ := newSender(t, srv.URL+"/api/push/"+token, loopback)
	s.retryWait = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Send(ctx, Beat{Up: true}); close(done) }()
	for range 200 { // wait for the first attempt
		rec.mu.Lock()
		n := len(rec.got)
		rec.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Send did not return after its context ended")
	}
}
