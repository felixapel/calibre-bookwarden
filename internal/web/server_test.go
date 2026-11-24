package web

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/auth"
	"github.com/felixapel/calibre-bookwarden/internal/auth/authtest"
)

type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }

func (c *testClock) advance(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

type testEnv struct {
	srv    *httptest.Server
	server *Server
	svc    *auth.Service
	clock  *testClock
	client *http.Client
	runs   *fakeRuns
}

type response struct {
	status int
	header http.Header
	body   string
	cookie *http.Cookie
}

const adminPassword = "correct horse battery"

func newEnv(t *testing.T, mutate ...func(*Options)) *testEnv {
	t.Helper()
	// Start at the real time: the cookie jar expires cookies by the real
	// clock, and a fixed date would make every test fail once it's 7 days old.
	clk := &testClock{t: time.Now()}
	svc := auth.NewService(authtest.NewMemRepo(), auth.Params{Memory: 64, Time: 1, Threads: 1, KeyLen: 32, SaltLen: 16}, clk.now)
	runs := newFakeRuns()
	opts := Options{
		AllowedHosts: []string{"127.0.0.1"},
		Auth:         svc,
		Health:       func(context.Context) error { return nil },
		Logger:       slog.New(slog.DiscardHandler),
		Runs:         runs,
		Reports:      &fakeReports{},
		Fixing:       &fakeFixing{},
	}
	for _, m := range mutate {
		m(&opts)
	}
	s, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return &testEnv{srv: ts, server: s, svc: svc, clock: clk, client: client, runs: runs}
}

func (e *testEnv) do(t *testing.T, method, path string, form url.Values, header http.Header) response {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(context.Background(), method, e.srv.URL+path, body)
	if err != nil {
		t.Fatal(err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
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
	r := response{status: resp.StatusCode, header: resp.Header, body: string(b)}
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookie {
			r.cookie = c
		}
	}
	return r
}

func (e *testEnv) setUpAdmin(t *testing.T) {
	t.Helper()
	code, ok, err := e.svc.PrepareSetup(context.Background())
	if err != nil || !ok {
		t.Fatalf("PrepareSetup: %v, %v", ok, err)
	}
	r := e.do(t, http.MethodPost, "/setup", url.Values{
		"code": {code}, "username": {"admin"}, "password": {adminPassword}, "confirm": {adminPassword},
	}, nil)
	if r.status != http.StatusSeeOther {
		t.Fatalf("setup: status %d: %s", r.status, r.body)
	}
}

func (e *testEnv) logIn(t *testing.T) {
	t.Helper()
	r := e.do(t, http.MethodPost, "/login", url.Values{"username": {"admin"}, "password": {adminPassword}}, nil)
	if r.status != http.StatusSeeOther || r.cookie == nil {
		t.Fatalf("login: status %d, cookie %v", r.status, r.cookie)
	}
}

var csrfField = regexp.MustCompile(`name="csrf_token" value="([^"]+)"`)

func (e *testEnv) csrfToken(t *testing.T) string {
	t.Helper()
	r := e.do(t, http.MethodGet, "/", nil, nil)
	m := csrfField.FindStringSubmatch(r.body)
	if r.status != http.StatusOK || m == nil {
		t.Fatalf("overview: status %d, no CSRF field in %q", r.status, r.body)
	}
	return m[1]
}

func TestSetupFlow(t *testing.T) {
	e := newEnv(t)
	if r := e.do(t, http.MethodGet, "/", nil, nil); r.status != http.StatusSeeOther || r.header.Get("Location") != "/setup" {
		t.Fatalf("GET / before setup: %d → %q, want 303 → /setup", r.status, r.header.Get("Location"))
	}
	code, _, err := e.svc.PrepareSetup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	steps := []struct {
		name       string
		form       url.Values
		wantStatus int
		wantText   string
	}{
		{"wrong code", url.Values{"code": {"AAAA-AAAA-AAAA-AAAA"}, "username": {"admin"}, "password": {adminPassword}, "confirm": {adminPassword}}, http.StatusBadRequest, "setup code is wrong"},
		{"passwords differ", url.Values{"code": {code}, "username": {"admin"}, "password": {adminPassword}, "confirm": {"something else!"}}, http.StatusBadRequest, "don&#39;t match"},
		{"short password", url.Values{"code": {code}, "username": {"admin"}, "password": {"short"}, "confirm": {"short"}}, http.StatusBadRequest, "at least 12 characters"},
		{"valid", url.Values{"code": {code}, "username": {"admin"}, "password": {adminPassword}, "confirm": {adminPassword}}, http.StatusSeeOther, ""},
	}
	for _, st := range steps {
		r := e.do(t, http.MethodPost, "/setup", st.form, nil)
		if r.status != st.wantStatus || !strings.Contains(r.body, st.wantText) {
			t.Fatalf("%s: status %d body %q; want %d containing %q", st.name, r.status, r.body, st.wantStatus, st.wantText)
		}
	}
	if r := e.do(t, http.MethodGet, "/setup", nil, nil); r.status != http.StatusNotFound {
		t.Fatalf("GET /setup after setup: %d, want 404", r.status)
	}
	if r := e.do(t, http.MethodGet, "/", nil, nil); r.header.Get("Location") != "/login" {
		t.Fatalf("GET / signed out: Location %q, want /login", r.header.Get("Location"))
	}
}

func TestLoginCookieSecureFlag(t *testing.T) {
	tests := []struct {
		name       string
		trusted    []netip.Prefix
		header     http.Header
		wantSecure bool
	}{
		{"plain HTTP on the LAN", nil, nil, false},
		{"trusted proxy that terminates HTTPS", []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}, http.Header{"X-Forwarded-Proto": {"https"}}, true},
		{"untrusted client claiming HTTPS", nil, http.Header{"X-Forwarded-Proto": {"https"}}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, func(o *Options) { o.TrustedProxies = tc.trusted })
			e.setUpAdmin(t)
			r := e.do(t, http.MethodPost, "/login", url.Values{"username": {"admin"}, "password": {adminPassword}}, tc.header)
			if r.status != http.StatusSeeOther || r.cookie == nil {
				t.Fatalf("login: status %d, cookie %v", r.status, r.cookie)
			}
			if r.cookie.Secure != tc.wantSecure || !r.cookie.HttpOnly || r.cookie.SameSite != http.SameSiteStrictMode {
				t.Fatalf("cookie Secure=%v HttpOnly=%v SameSite=%v; want Secure=%v, HttpOnly, Strict",
					r.cookie.Secure, r.cookie.HttpOnly, r.cookie.SameSite, tc.wantSecure)
			}
		})
	}
}

func TestLoginFailuresAreRateLimited(t *testing.T) {
	e := newEnv(t)
	e.setUpAdmin(t)
	wrong := url.Values{"username": {"admin"}, "password": {"wrong password!"}}
	for i := 1; i <= 6; i++ {
		if r := e.do(t, http.MethodPost, "/login", wrong, nil); r.status != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status %d, want 401", i, r.status)
		}
	}
	r := e.do(t, http.MethodPost, "/login", wrong, nil)
	if r.status != http.StatusTooManyRequests || r.header.Get("Retry-After") != "1" {
		t.Fatalf("attempt 7: status %d Retry-After %q, want 429 and 1", r.status, r.header.Get("Retry-After"))
	}
}

func TestSignInWorkIsCapped(t *testing.T) {
	e := newEnv(t, func(o *Options) { o.AuthWait = 50 * time.Millisecond })
	e.setUpAdmin(t)
	for range cap(e.server.authSlots) {
		e.server.authSlots <- struct{}{} // every hashing slot is busy
	}
	creds := url.Values{"username": {"admin"}, "password": {adminPassword}}
	if r := e.do(t, http.MethodPost, "/login", creds, nil); r.status != http.StatusServiceUnavailable || r.header.Get("Retry-After") == "" {
		t.Fatalf("login with every slot busy: status %d, want 503 with Retry-After", r.status)
	}
	<-e.server.authSlots
	if r := e.do(t, http.MethodPost, "/login", creds, nil); r.status != http.StatusSeeOther {
		t.Fatalf("login with a free slot: status %d, want 303", r.status)
	}
}

// TestGlobalLoginWaitHoldsNoSlot: past the global budget a login waits 2 s
// before it takes a hashing slot, so waiting attempts can't crowd the admin
// out of the slots.
func TestGlobalLoginWaitHoldsNoSlot(t *testing.T) {
	e := newEnv(t)
	e.setUpAdmin(t)
	for i := range 50 {
		_, _, _ = e.svc.Login(context.Background(), netip.AddrFrom4([4]byte{198, 51, 100, byte(i)}), "admin", "wrong password!!")
	}
	creds := url.Values{"username": {"admin"}, "password": {adminPassword}}
	start := time.Now()
	done := make(chan int, 1)
	go func() { done <- e.do(t, http.MethodPost, "/login", creds, nil).status }()
	time.Sleep(500 * time.Millisecond)
	if n := len(e.server.authSlots); n != 0 {
		t.Errorf("%d hashing slots held while the login waits", n)
	}
	if st := <-done; st != http.StatusSeeOther {
		t.Fatalf("login past the budget = %d, want 303", st)
	}
	if took := time.Since(start); took < 2*time.Second {
		t.Errorf("login took %v, want the 2 s wait", took)
	}
}

func TestSetupAfterSetupTakesNoHashingSlot(t *testing.T) {
	e := newEnv(t, func(o *Options) { o.AuthWait = 50 * time.Millisecond })
	e.setUpAdmin(t)
	// A second server over the same accounts stands in for a restart: it
	// hasn't seen setup finish.
	restarted, err := New(Options{
		AllowedHosts: []string{"127.0.0.1"},
		Auth:         e.svc,
		Health:       func(context.Context) error { return nil },
		Logger:       slog.New(slog.DiscardHandler),
		Runs:         newFakeRuns(),
		Reports:      &fakeReports{},
		Fixing:       &fakeFixing{},
		AuthWait:     50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(restarted.Handler())
	t.Cleanup(ts.Close)
	tests := []struct {
		name string
		env  *testEnv
	}{
		{"the server that ran setup", e},
		{"after a restart", &testEnv{srv: ts, server: restarted, svc: e.svc, clock: e.clock, client: e.client}},
	}
	form := url.Values{"code": {"AAAA-AAAA-AAAA-AAAA"}, "username": {"second"}, "password": {adminPassword}, "confirm": {adminPassword}}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for range cap(tc.env.server.authSlots) {
				tc.env.server.authSlots <- struct{}{} // every hashing slot is busy
			}
			r := tc.env.do(t, http.MethodPost, "/setup", form, nil)
			if r.status != http.StatusSeeOther || r.header.Get("Location") != "/login" {
				t.Fatalf("POST /setup after setup: %d → %q, want 303 → /login without waiting for a slot", r.status, r.header.Get("Location"))
			}
		})
	}
}

func TestCrossOriginPostIsRejected(t *testing.T) {
	e := newEnv(t)
	e.setUpAdmin(t)
	r := e.do(t, http.MethodPost, "/login", url.Values{"username": {"admin"}, "password": {adminPassword}},
		http.Header{"Origin": {"http://evil.example"}})
	if r.status != http.StatusForbidden || r.cookie != nil {
		t.Fatalf("cross-origin login: status %d, cookie %v; want 403 and no cookie", r.status, r.cookie)
	}
}

func TestLogoutRequiresTheFormToken(t *testing.T) {
	e := newEnv(t)
	e.setUpAdmin(t)
	e.logIn(t)
	token := e.csrfToken(t)
	if r := e.do(t, http.MethodPost, "/logout", url.Values{}, nil); r.status != http.StatusForbidden {
		t.Fatalf("logout without token: %d, want 403", r.status)
	}
	if r := e.do(t, http.MethodPost, "/logout", url.Values{"csrf_token": {token}}, nil); r.status != http.StatusSeeOther {
		t.Fatalf("logout with token: %d, want 303", r.status)
	}
	if r := e.do(t, http.MethodGet, "/", nil, nil); r.header.Get("Location") != "/login" {
		t.Fatalf("GET / after logout: Location %q, want /login", r.header.Get("Location"))
	}
}

func TestIdleSessionExpires(t *testing.T) {
	e := newEnv(t)
	e.setUpAdmin(t)
	e.logIn(t)
	e.clock.advance(auth.SessionIdleTimeout)
	if r := e.do(t, http.MethodGet, "/", nil, nil); r.header.Get("Location") != "/login" {
		t.Fatalf("GET / after 12 idle hours: %d → %q, want /login", r.status, r.header.Get("Location"))
	}
}

func TestHealthz(t *testing.T) {
	tests := []struct {
		name   string
		health error
		want   int
	}{
		{"database answers", nil, http.StatusOK},
		{"database down", errors.New("connection refused"), http.StatusServiceUnavailable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, func(o *Options) { o.Health = func(context.Context) error { return tc.health } })
			if r := e.do(t, http.MethodGet, "/healthz", nil, nil); r.status != tc.want {
				t.Fatalf("/healthz: %d, want %d", r.status, tc.want)
			}
		})
	}
}

func TestPagesCarryTheSecurityPolicy(t *testing.T) {
	e := newEnv(t)
	e.setUpAdmin(t)
	r := e.do(t, http.MethodGet, "/login", nil, nil)
	if r.header.Get("Content-Security-Policy") != contentSecurityPolicy || r.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("headers: %v", r.header)
	}
	if strings.Contains(r.body, "<script") || strings.Contains(r.body, "style=") {
		t.Fatal("page contains inline script or style")
	}
}
