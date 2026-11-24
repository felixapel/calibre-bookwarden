package auth_test

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/auth"
	"github.com/felixapel/calibre-bookwarden/internal/auth/authtest"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }

func (c *clock) advance(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

var client = netip.MustParseAddr("192.0.2.50")

const goodPassword = "correct horse battery"

func newService(t *testing.T) (*auth.Service, *clock) {
	t.Helper()
	c := &clock{t: time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)}
	return auth.NewService(authtest.NewMemRepo(), cheap, c.now), c
}

func setUpAdmin(t *testing.T, svc *auth.Service) {
	t.Helper()
	ctx := context.Background()
	code, ok, err := svc.PrepareSetup(ctx)
	if err != nil || !ok {
		t.Fatalf("PrepareSetup: ok=%v err=%v", ok, err)
	}
	if err := svc.CompleteSetup(ctx, client, code, "admin", goodPassword); err != nil {
		t.Fatalf("CompleteSetup: %v", err)
	}
}

func TestCompleteSetup(t *testing.T) {
	tests := []struct {
		name     string
		code     func(printed string) string
		username string
		password string
		want     error
	}{
		{"code as printed", func(c string) string { return c }, "admin", goodPassword, nil},
		{"code in lower case with spaces", func(c string) string { return strings.ToLower(strings.ReplaceAll(c, "-", " ")) }, "admin", goodPassword, nil},
		{"wrong code", func(string) string { return "AAAA-AAAA-AAAA-AAAA" }, "admin", goodPassword, auth.ErrSetupCodeInvalid},
		{"short password", func(c string) string { return c }, "admin", "short", auth.ErrWeakPassword},
		{"empty username", func(c string) string { return c }, "   ", goodPassword, auth.ErrUsernameRequired},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := newService(t)
			code, ok, err := svc.PrepareSetup(context.Background())
			if err != nil || !ok {
				t.Fatalf("PrepareSetup: ok=%v err=%v", ok, err)
			}
			err = svc.CompleteSetup(context.Background(), client, tc.code(code), tc.username, tc.password)
			if !errors.Is(err, tc.want) {
				t.Fatalf("CompleteSetup = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestSetupHappensOnce(t *testing.T) {
	ctx := context.Background()
	svc, _ := newService(t)
	setUpAdmin(t, svc)
	if _, ok, err := svc.PrepareSetup(ctx); err != nil || ok {
		t.Fatalf("PrepareSetup after setup: ok=%v err=%v, want no new code", ok, err)
	}
	if needed, _ := svc.SetupNeeded(ctx); needed {
		t.Fatal("SetupNeeded after setup = true")
	}
	if err := svc.CompleteSetup(ctx, client, "ANY", "second", goodPassword); !errors.Is(err, auth.ErrAdminExists) {
		t.Fatalf("second CompleteSetup = %v, want ErrAdminExists", err)
	}
}

func TestCompleteSetupAfterSetupSkipsPasswordWork(t *testing.T) {
	svc, _ := newService(t)
	setUpAdmin(t, svc)
	// "short" fails the length check, so ErrAdminExists shows that the admin
	// check runs before any password validation or hashing.
	if err := svc.CompleteSetup(context.Background(), client, "ANY", "second", "short"); !errors.Is(err, auth.ErrAdminExists) {
		t.Fatalf("CompleteSetup after setup = %v, want ErrAdminExists", err)
	}
}

func TestLogin(t *testing.T) {
	tests := []struct {
		name     string
		username string
		password string
		want     error
	}{
		{"right credentials", "admin", goodPassword, nil},
		{"username with surrounding spaces", "  admin ", goodPassword, nil},
		{"wrong password", "admin", "wrong password!", auth.ErrInvalidCredentials},
		{"unknown user", "mallory", goodPassword, auth.ErrInvalidCredentials},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			svc, _ := newService(t)
			setUpAdmin(t, svc)
			token, sess, err := svc.Login(ctx, client, tc.username, tc.password)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Login = %v, want %v", err, tc.want)
			}
			if tc.want != nil {
				return
			}
			if token == "" || sess.CSRFToken == "" {
				t.Fatal("Login returned an empty token")
			}
			if _, err := svc.Authenticate(ctx, token); err != nil {
				t.Fatalf("Authenticate fresh session: %v", err)
			}
		})
	}
}

func TestLoginBackoff(t *testing.T) {
	ctx := context.Background()
	svc, clk := newService(t)
	setUpAdmin(t, svc)
	for i := 1; i <= 6; i++ {
		if _, _, err := svc.Login(ctx, client, "admin", "wrong password!"); !errors.Is(err, auth.ErrInvalidCredentials) {
			t.Fatalf("attempt %d: %v, want ErrInvalidCredentials", i, err)
		}
	}
	var limited *auth.RateLimitedError
	if _, _, err := svc.Login(ctx, client, "admin", goodPassword); !errors.As(err, &limited) || limited.RetryAfter != time.Second {
		t.Fatalf("attempt 7 = %v, want a 1s RateLimitedError", err)
	}
	clk.advance(time.Second)
	if _, _, err := svc.Login(ctx, client, "admin", goodPassword); err != nil {
		t.Fatalf("login after the wait: %v", err)
	}
}

func TestSessionTimeouts(t *testing.T) {
	tests := []struct {
		name  string
		every time.Duration // advance the clock this much, then authenticate,
		times int           // this many times; every use but the last must succeed
		want  error         // the result of the last use
	}{
		{"used within the idle window", 11 * time.Hour, 1, nil},
		{"idle for 12 hours", 12 * time.Hour, 1, auth.ErrNoSession},
		{"used every 11 hours, until past 7 days", 11 * time.Hour, 16, auth.ErrNoSession},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			svc, clk := newService(t)
			setUpAdmin(t, svc)
			token, _, err := svc.Login(ctx, client, "admin", goodPassword)
			if err != nil {
				t.Fatal(err)
			}
			var last error
			for i := 1; i <= tc.times; i++ {
				clk.advance(tc.every)
				_, last = svc.Authenticate(ctx, token)
				if i < tc.times && last != nil {
					t.Fatalf("use %d of %d: %v", i, tc.times, last)
				}
			}
			if !errors.Is(last, tc.want) {
				t.Fatalf("last use = %v, want %v", last, tc.want)
			}
		})
	}
}

func TestLogoutEndsTheSession(t *testing.T) {
	ctx := context.Background()
	svc, _ := newService(t)
	setUpAdmin(t, svc)
	token, _, err := svc.Login(ctx, client, "admin", goodPassword)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Logout(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(ctx, token); !errors.Is(err, auth.ErrNoSession) {
		t.Fatalf("Authenticate after logout = %v, want ErrNoSession", err)
	}
}

func TestPurgeExpiredSessions(t *testing.T) {
	ctx := context.Background()
	svc, clk := newService(t)
	setUpAdmin(t, svc)
	if _, _, err := svc.Login(ctx, client, "admin", goodPassword); err != nil {
		t.Fatal(err)
	}
	clk.advance(6 * time.Hour)
	fresh, _, err := svc.Login(ctx, client, "admin", goodPassword)
	if err != nil {
		t.Fatal(err)
	}
	clk.advance(7 * time.Hour) // the first session is now idle for 13 hours
	n, err := svc.PurgeExpiredSessions(ctx)
	if err != nil || n != 1 {
		t.Fatalf("PurgeExpiredSessions = %d, %v; want 1, nil", n, err)
	}
	if _, err := svc.Authenticate(ctx, fresh); err != nil {
		t.Fatalf("the fresh session was purged: %v", err)
	}
}

// TestLoginDelay: the service reports the global budget's wait, which the
// web layer applies before it takes a hashing slot.
func TestLoginDelay(t *testing.T) {
	ctx := context.Background()
	svc, clk := newService(t)
	setUpAdmin(t, svc)
	if d := svc.LoginDelay(); d != 0 {
		t.Fatalf("LoginDelay with no failures = %v", d)
	}
	for i := range 50 {
		_, _, _ = svc.Login(ctx, netip.AddrFrom4([4]byte{198, 51, 100, byte(i)}), "admin", "wrong password!!")
	}
	if d := svc.LoginDelay(); d != 2*time.Second {
		t.Fatalf("LoginDelay after 50 failures = %v, want 2s", d)
	}
	if _, _, err := svc.Login(ctx, netip.MustParseAddr("203.0.113.9"), "admin", goodPassword); err != nil {
		t.Fatalf("Login past the budget = %v; it must never lock out", err)
	}
	clk.advance(time.Hour + time.Second)
	if d := svc.LoginDelay(); d != 0 {
		t.Fatalf("LoginDelay an hour later = %v", d)
	}
}
