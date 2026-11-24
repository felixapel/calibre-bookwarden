package web

import (
	"bytes"
	"crypto/tls"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestHostAllowlist(t *testing.T) {
	// The last entry is IPv6 in long form, lower-cased as config stores it.
	allowed := []string{"192.0.2.122", "bookwarden.lan", "fd00::1", "2001:db8:0:0::5"}
	tests := []struct {
		name string
		host string
		path string
		want int
	}{
		{"allowed IP with port", "192.0.2.122:8399", "/login", http.StatusOK},
		{"allowed name in upper case", "BOOKWARDEN.LAN", "/login", http.StatusOK},
		{"allowed name with a trailing dot", "bookwarden.lan.:8399", "/login", http.StatusOK},
		{"allowed IPv6 literal", "[fd00::1]:8399", "/login", http.StatusOK},
		{"IPv6 entry written in long form", "[2001:db8::5]:8399", "/login", http.StatusOK},
		{"IPv6 Host header in long form", "[fd00:0:0::1]:8399", "/login", http.StatusOK},
		{"another IPv6 address", "[fd00::2]:8399", "/login", http.StatusMisdirectedRequest},
		{"unknown name", "evil.example", "/login", http.StatusMisdirectedRequest},
		{"rebinding to localhost", "localhost:8399", "/login", http.StatusMisdirectedRequest},
		{"health check from Docker", "127.0.0.1:8080", "/healthz", http.StatusOK},
	}
	h := requireAllowedHost(allowed, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.Host = tc.host
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("Host %q %s: status %d, want %d", tc.host, tc.path, rec.Code, tc.want)
			}
		})
	}
}

func TestResolveClient(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("172.18.0.0/16")}
	tests := []struct {
		name      string
		remote    string
		xff       string
		proto     string
		tls       bool
		wantIP    string
		wantHTTPS bool
	}{
		{"direct LAN client", "192.0.2.50:51000", "", "", false, "192.0.2.50", false},
		{"direct client over TLS", "192.0.2.50:51000", "", "", true, "192.0.2.50", true},
		{"untrusted peer spoofing headers", "192.0.2.50:51000", "10.9.9.9", "https", false, "192.0.2.50", false},
		{"trusted proxy", "172.18.0.2:40000", "192.0.2.77", "https", false, "192.0.2.77", true},
		{"trusted proxy chain", "172.18.0.2:40000", "203.0.113.9, 192.0.2.77, 172.18.0.3", "https", false, "192.0.2.77", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = tc.remote
			if tc.xff != "" {
				req.Header.Set("X-Forwarded-For", tc.xff)
			}
			if tc.proto != "" {
				req.Header.Set("X-Forwarded-Proto", tc.proto)
			}
			if tc.tls {
				req.TLS = &tls.ConnectionState{}
			}
			got := resolveClient(req, trusted)
			if got.IP.String() != tc.wantIP || got.HTTPS != tc.wantHTTPS {
				t.Fatalf("resolveClient = %s https=%v, want %s https=%v", got.IP, got.HTTPS, tc.wantIP, tc.wantHTTPS)
			}
		})
	}
}

func TestSecurityHeaders(t *testing.T) {
	rec := httptest.NewRecorder()
	securityHeaders(http.NotFoundHandler()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	want := map[string]string{
		"Content-Security-Policy": contentSecurityPolicy,
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "same-origin",
		"X-Frame-Options":         "DENY",
	}
	for k, v := range want {
		if got := rec.Header().Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
}

func TestRecoverPanicsAnswers500(t *testing.T) {
	h := recoverPanics(slog.New(slog.DiscardHandler), http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500", rec.Code)
	}
}

func TestLimitBody(t *testing.T) {
	h := limitBody(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	tests := []struct {
		name string
		size int
		want int
	}{
		{"small form", 1 << 10, http.StatusOK},
		{"form over 64 KiB", 65 << 10, http.StatusRequestEntityTooLarge},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("a="+strings.Repeat("x", tc.size)))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status %d, want %d", rec.Code, tc.want)
			}
		})
	}
}

func TestLogRequests(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	h := withClient(nil, logRequests(logger, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})))
	for _, target := range []string{"/login?token=secret-value", "/healthz"} {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.RemoteAddr = "192.0.2.50:51000"
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
	out := buf.String()
	for _, want := range []string{`"path":"/login"`, `"status":418`, `"client":"192.0.2.50"`} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %s: %s", want, out)
		}
	}
	for _, unwanted := range []string{"secret-value", "/healthz"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("log contains %q: %s", unwanted, out)
		}
	}
}
