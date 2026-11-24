package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testKey = "fake-key-for-tests"

// testServer answers each request with handler and returns a seat config
// pointing at it.
func testServer(t *testing.T, wire string, handler http.HandlerFunc) (SeatConfig, *http.Client) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	return SeatConfig{Name: "s", Wire: wire, URL: srv.URL + "/v1/x", Model: "m-1", Key: testKey,
			AllowCIDR: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}},
		NewHTTPClient([]string{u.Host}, true)
}

// bodyType guesses a test body's content type from its first byte.
func bodyType(body string) string {
	switch {
	case strings.HasPrefix(body, "{"):
		return "application/json; charset=utf-8"
	case strings.HasPrefix(body, "<"):
		return "text/html"
	}
	return "text/plain"
}

func noWait(t *testing.T) *[]time.Duration {
	t.Helper()
	var waits []time.Duration
	old := wait
	wait = func(ctx context.Context, d time.Duration) error { waits = append(waits, d); return ctx.Err() }
	t.Cleanup(func() { wait = old })
	return &waits
}

func TestAnthropicWire(t *testing.T) {
	var got map[string]any
	cfg, hc := testServer(t, "anthropic", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("x-api-key") != testKey || r.Header.Get("anthropic-version") != "2023-06-01" ||
			r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "bad headers", http.StatusBadRequest)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = io.WriteString(w, `{"model":"m-1-20261001","content":[{"type":"thinking","thinking":"hm"},{"type":"text","text":"{\"verdicts\":"},{"type":"text","text":"[]}"}],
			"usage":{"input_tokens":120,"output_tokens":30}}`)
	})
	cfg.Effort = "max"
	s, err := NewSeat(cfg, hc)
	if err != nil {
		t.Fatal(err)
	}
	reply, err := s.Judge(context.Background(), "BRIEF", "PACKET")
	if err != nil {
		t.Fatal(err)
	}
	if reply.Text != `{"verdicts":[]}` || reply.InputTokens != 120 || reply.OutputTokens != 30 || reply.Model != "m-1-20261001" {
		t.Fatalf("reply = %+v", reply)
	}
	msgs, _ := got["messages"].([]any)
	if got["model"] != "m-1" || got["system"] != "BRIEF" || got["max_tokens"] == nil || len(msgs) != 1 ||
		msgs[0].(map[string]any)["content"] != "PACKET" || msgs[0].(map[string]any)["role"] != "user" {
		t.Fatalf("request = %v", got)
	}
	if oc, _ := got["output_config"].(map[string]any); oc["effort"] != "max" {
		t.Fatalf("output_config = %v", got["output_config"])
	}
	if s.Name() != "s" || s.Model() != "m-1" {
		t.Fatalf("Name/Model = %s %s", s.Name(), s.Model())
	}
}

func TestOpenAIWire(t *testing.T) {
	for _, effort := range []string{"", "high"} {
		var got map[string]any
		cfg, hc := testServer(t, "openai", func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+testKey {
				http.Error(w, "no auth", http.StatusUnauthorized)
				return
			}
			_ = json.NewDecoder(r.Body).Decode(&got)
			_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ANSWER"}}],"usage":{"prompt_tokens":200,"completion_tokens":50}}`)
		})
		cfg.Effort = effort
		s, _ := NewSeat(cfg, hc)
		reply, err := s.Judge(context.Background(), "BRIEF", "PACKET")
		if err != nil || reply.Text != "ANSWER" || reply.InputTokens != 200 || reply.OutputTokens != 50 {
			t.Fatalf("Judge = %+v, %v", reply, err)
		}
		msgs, _ := got["messages"].([]any)
		if len(msgs) != 2 || msgs[0].(map[string]any)["role"] != "system" || msgs[0].(map[string]any)["content"] != "BRIEF" ||
			msgs[1].(map[string]any)["content"] != "PACKET" || got["model"] != "m-1" {
			t.Fatalf("request = %v", got)
		}
		if _, has := got["reasoning_effort"]; has != (effort != "") || (has && got["reasoning_effort"] != effort) {
			t.Fatalf("reasoning_effort with Effort %q = %v", effort, got["reasoning_effort"])
		}
	}
}

// The provider's reported model and reasoning tokens reach the reply, for
// sealing (model_reported) and for the extraction benchmark.
func TestOpenAIWireReportsModelAndReasoning(t *testing.T) {
	cfg, hc := testServer(t, "openai", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"model":"deepseek-v4.1-flash-2026-10","choices":[{"message":{"content":"A"}}],`+
			`"usage":{"prompt_tokens":10,"completion_tokens":40,"completion_tokens_details":{"reasoning_tokens":30}}}`)
	})
	s, _ := NewSeat(cfg, hc)
	reply, err := s.Judge(context.Background(), "B", "P")
	if err != nil || reply.Model != "deepseek-v4.1-flash-2026-10" || reply.ReasoningTokens != 30 {
		t.Fatalf("Judge = %+v, %v", reply, err)
	}
}

func TestSeatErrors(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		header    string
		body      string
		ctxCancel bool
		deadline  time.Duration // from now; zero for none
		code      string
		calls     int32
		waits     []time.Duration
	}{
		{"rate limited", 429, "2", "", false, 0, "quota_exhausted", 4, []time.Duration{2 * time.Second, 2 * time.Second, 2 * time.Second}},
		// A usage limit that resets later than a retry would wait: give up now.
		{"rate limited, long Retry-After", 429, "600", "", false, 0, "quota_exhausted", 1, nil},
		{"rate limited, Retry-After at the cap", 429, "60", "", false, 0, "quota_exhausted", 4, []time.Duration{time.Minute, time.Minute, time.Minute}},
		// A wait that would outlast the call's deadline isn't started.
		{"rate limited, wait past the deadline", 429, "30", "", false, 20 * time.Second, "quota_exhausted", 1, nil},
		{"rate limited, wait before the deadline", 429, "2", "", false, time.Minute, "quota_exhausted", 4, []time.Duration{2 * time.Second, 2 * time.Second, 2 * time.Second}},
		{"server error", 500, "", "", false, 0, "http_5xx", 4, []time.Duration{5 * time.Second, 5 * time.Second, 5 * time.Second}},
		{"server error, wait past the deadline", 500, "", "", false, 3 * time.Second, "http_5xx", 1, nil},
		{"unauthorized", 401, "", "", false, 0, "http_4xx", 1, nil},
		{"credits exhausted", 402, "", `{"error":{"message":"Insufficient CREDITS"}}`, false, 0, "quota_exhausted", 1, nil},
		{"billing limit in error.code", 403, "30", `{"error":{"code":"billing_hard_limit"}}`, false, 0, "quota_exhausted", 1, nil},
		{"quota in a top-level message", 403, "", `{"message":"quota exceeded for this project"}`, false, 0, "quota_exhausted", 1, nil},
		{"rate limit in error.type", 403, "", `{"error":{"type":"Rate Limit"}}`, false, 0, "quota_exhausted", 1, nil},
		{"quota in plain text", 403, "", `quota exceeded for this project`, false, 0, "http_4xx", 1, nil},
		{"billing in an HTML page", 403, "", `<html><body>Billing portal: access denied</body></html>`, false, 0, "http_4xx", 1, nil},
		{"quota word in an unrelated JSON field", 403, "", `{"detail":"quota","error":{"message":"denied"}}`, false, 0, "http_4xx", 1, nil},
		{"error as a bare string is not read", 403, "", `{"error":"quota"}`, false, 0, "http_4xx", 1, nil},
		{"JSON type with a broken body", 403, "", `{"error":{"message":"quota"`, false, 0, "http_4xx", 1, nil},
		{"payment required, no keyword", 402, "", `pay up`, false, 0, "http_4xx", 1, nil},
		{"plain forbidden", 403, "", `forbidden`, false, 0, "http_4xx", 1, nil},
		{"quota words in a 401", 401, "", `quota`, false, 0, "http_4xx", 1, nil},
		{"quota words in a 400", 400, "", `quota`, false, 0, "http_4xx", 1, nil},
		{"huge body", 200, "", strings.Repeat("x", 1<<20+1), false, 0, "bad_answer", 1, nil},
		{"not JSON", 200, "", "<html>", false, 0, "bad_answer", 1, nil},
		{"deadline", 200, "", "", true, 0, "timeout", 0, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			waits := noWait(t)
			var calls atomic.Int32
			cfg, hc := testServer(t, "openai", func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				if tc.header != "" {
					w.Header().Set("Retry-After", tc.header)
				}
				w.Header().Set("Content-Type", bodyType(tc.body))
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			})
			s, _ := NewSeat(cfg, hc)
			ctx, cancel := context.WithCancel(context.Background())
			if tc.ctxCancel {
				ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			}
			if tc.deadline > 0 {
				ctx, cancel = context.WithTimeout(context.Background(), tc.deadline)
			}
			defer cancel()
			_, err := s.Judge(ctx, "B", "P")
			if got := ErrorCode(err); got != tc.code {
				t.Fatalf("ErrorCode(%v) = %q, want %q", err, got, tc.code)
			}
			if strings.Contains(err.Error(), testKey) {
				t.Fatalf("error carries the key: %v", err)
			}
			if calls.Load() != tc.calls {
				t.Errorf("calls = %d, want %d", calls.Load(), tc.calls)
			}
			if len(*waits) != len(tc.waits) {
				t.Fatalf("waits = %v, want %v", *waits, tc.waits)
			}
			for i := range tc.waits {
				if (*waits)[i] != tc.waits[i] {
					t.Fatalf("waits = %v, want %v", *waits, tc.waits)
				}
			}
		})
	}
}

func TestHTTPClientRefuses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "{}") }))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	cfg := SeatConfig{Name: "s", Wire: "openai", URL: srv.URL, Model: "m", Key: testKey, AllowCIDR: prefixes(t, "127.0.0.1/32")}
	for _, tc := range []struct {
		name string
		hc   *http.Client
	}{
		{"host not allowed", NewHTTPClient([]string{"api.example.com:443"}, true)},
		{"private address", NewHTTPClient([]string{u.Host}, false)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := NewSeat(cfg, tc.hc)
			_, err := s.Judge(context.Background(), "B", "P")
			if ErrorCode(err) != "refused_host" {
				t.Fatalf("ErrorCode(%v) = %q", err, ErrorCode(err))
			}
		})
	}
}

func TestNewSeatRejects(t *testing.T) {
	hc := NewHTTPClient(nil, false)
	for _, c := range []SeatConfig{
		{Name: "s", Wire: "grpc", URL: "https://x/v1", Model: "m", Key: "k"},
		{Name: "s", Wire: "openai", URL: "", Model: "m", Key: "k"},
		{Name: "s", Wire: "openai", URL: "https://x/v1", Model: "", Key: "k"},
		{Name: "s", Wire: "openai", URL: "https://x/v1", Model: "m", Key: ""},
	} {
		if _, err := NewSeat(c, hc); err == nil {
			t.Errorf("NewSeat(%+v) accepted it", c)
		}
	}
}

func TestErrorCodeOther(t *testing.T) {
	if ErrorCode(nil) != "" || ErrorCode(errors.New("x")) != "transport" || ErrorCode(ErrBadAnswer) != "bad_answer" ||
		ErrorCode(context.DeadlineExceeded) != "timeout" {
		t.Fatal("ErrorCode mapping")
	}
}

func TestRetryCaps(t *testing.T) {
	for _, v := range []string{"99999999999", "9223372036854775807", "9223372036854775808", "600", "-1", "x", ""} {
		if got := retryAfter(v); got < 0 || got > maxWait {
			t.Errorf("retryAfter(%q) = %v, want within 0..%v", v, got, maxWait)
		}
	}
	for _, v := range []string{"99999999999", "9223372036854775807", "9223372036854775808", "-1", "x", ""} {
		if got := reportedRetry(v); got < 0 || got > maxReportedRetry {
			t.Errorf("reportedRetry(%q) = %v, want within 0..%v", v, got, maxReportedRetry)
		}
	}
	if retryAfter("99999999999") != maxWait || reportedRetry("99999999999") != maxReportedRetry {
		t.Errorf("huge values should hit the caps: %v %v", retryAfter("99999999999"), reportedRetry("99999999999"))
	}
}

func TestNewSeatRefusesPlainHTTPWithoutAllowance(t *testing.T) {
	hc := NewHTTPClient(nil, true)
	c := SeatConfig{Name: "s", Wire: "openai", URL: "http://10.0.0.122:8766/v1", Model: "m", Key: "k"}
	if _, err := NewSeat(c, hc); err == nil {
		t.Fatal("NewSeat accepted plain http without an allowance")
	}
	c.AllowCIDR = []netip.Prefix{netip.MustParsePrefix("10.0.0.122/32")}
	if _, err := NewSeat(c, hc); err != nil {
		t.Fatalf("with an allowance: %v", err)
	}
	c.URL = "https://api.example.com/v1"
	c.AllowCIDR = nil
	if _, err := NewSeat(c, hc); err != nil {
		t.Fatalf("https: %v", err)
	}
}

func TestSessionHeader(t *testing.T) {
	var got []string
	cfg, hc := testServer(t, "openai", func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get("X-Bf-Eh-X-Opencode-Session"))
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"A"}}]}`)
	})
	cfg.SessionHeader = "x-bf-eh-x-opencode-session"
	a, err := NewSeat(cfg, hc)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewSeat(cfg, hc)
	for _, s := range []Seat{a, a, b} {
		if _, err := s.Judge(context.Background(), "B", "P"); err != nil {
			t.Fatal(err)
		}
	}
	if len(got) != 3 || got[0] == "" || got[0] != got[1] || got[0] == got[2] || strings.Contains(got[0], testKey) {
		t.Fatalf("session headers = %q; want one stable id per seat", got)
	}
}

// NoContentLog asks Bifrost not to keep the excerpt and the answer in its log
// store; a seat without it sends no such header.
func TestNoContentLogHeader(t *testing.T) {
	for _, on := range []bool{true, false} {
		var got []string
		cfg, hc := testServer(t, "openai", func(w http.ResponseWriter, r *http.Request) {
			got = r.Header.Values("X-Bf-Disable-Content-Logging")
			_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"A"}}]}`)
		})
		cfg.NoContentLog = on
		s, _ := NewSeat(cfg, hc)
		if _, err := s.Judge(context.Background(), "B", "P"); err != nil {
			t.Fatal(err)
		}
		if on && (len(got) != 1 || got[0] != "true") || !on && len(got) != 0 {
			t.Errorf("NoContentLog %v: header = %q", on, got)
		}
	}
}

func TestKeylessSeat(t *testing.T) {
	var auth []string
	cfg, hc := testServer(t, "openai", func(w http.ResponseWriter, r *http.Request) {
		auth = append(auth, r.Header.Get("Authorization"))
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"A"}}]}`)
	})
	cfg.Key, cfg.Keyless = "", true
	s, err := NewSeat(cfg, hc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Judge(context.Background(), "B", "P"); err != nil {
		t.Fatal(err)
	}
	cfg.Key = testKey // a gateway key, sent when set
	s, _ = NewSeat(cfg, hc)
	if _, err := s.Judge(context.Background(), "B", "P"); err != nil {
		t.Fatal(err)
	}
	if len(auth) != 2 || auth[0] != "" || auth[1] != "Bearer "+testKey {
		t.Fatalf("Authorization = %q", auth)
	}
}

func TestNewSeatRejectsSessionHeader(t *testing.T) {
	hc := NewHTTPClient(nil, false)
	for _, h := range []string{"Authorization", "content-type", "X-Api-Key", "x bad", "x:y", "x\n", "Proxy-Authorization", "cookie", "te", "Upgrade", "x-"} {
		c := SeatConfig{Name: "s", Wire: "openai", URL: "https://x/v1", Model: "m", Key: "k", SessionHeader: h}
		if _, err := NewSeat(c, hc); err == nil {
			t.Errorf("NewSeat accepted session header %q", h)
		}
	}
}
