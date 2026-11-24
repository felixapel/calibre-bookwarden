package auth_test

import (
	"net/netip"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/auth"
)

func TestLoginLimiter(t *testing.T) {
	ip := netip.MustParseAddr("192.0.2.50")
	other := netip.MustParseAddr("192.0.2.51")
	t0 := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		failures int
		after    time.Duration
		addr     netip.Addr
		want     time.Duration
	}{
		{"five failures are free", 5, 0, ip, 0},
		{"the sixth blocks for a second", 6, 0, ip, time.Second},
		{"the seventh doubles the wait", 7, 0, ip, 2 * time.Second},
		{"the wait is capped at 15 minutes", 40, 0, ip, 15 * time.Minute},
		{"the block ends on time", 6, time.Second, ip, 0},
		{"other clients are unaffected", 10, 0, other, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l := auth.NewLoginLimiter()
			for range tc.failures {
				l.Fail(ip, t0)
			}
			if got := l.Check(tc.addr, t0.Add(tc.after)); got != tc.want {
				t.Fatalf("Check = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestLoginLimiterSuccessClearsFailures(t *testing.T) {
	ip := netip.MustParseAddr("192.0.2.50")
	t0 := time.Now()
	l := auth.NewLoginLimiter()
	for range 8 {
		l.Fail(ip, t0)
	}
	l.Succeed(ip)
	if got := l.Check(ip, t0); got != 0 {
		t.Fatalf("Check after success = %s, want 0", got)
	}
}

// TestLoginLimiterIPv6Prefix: one IPv6 /64 is one client, so rotating
// addresses inside it doesn't reset the count.
func TestLoginLimiterIPv6Prefix(t *testing.T) {
	l := auth.NewLoginLimiter()
	t0 := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	for i := range 6 {
		l.Fail(netip.AddrFrom16([16]byte{0x20, 0x01, 0x0d, 0xb8, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, byte(i + 1)}), t0)
	}
	if got := l.Check(netip.MustParseAddr("2001:db8:0:1::ffff"), t0); got != time.Second {
		t.Errorf("same /64 waits %v, want 1s", got)
	}
	if got := l.Check(netip.MustParseAddr("2001:db8:0:2::1"), t0); got != 0 {
		t.Errorf("another /64 waits %v, want 0", got)
	}
	if got := l.Check(netip.MustParseAddr("::ffff:192.0.2.50"), t0); got != 0 {
		t.Errorf("an IPv4-mapped address waits %v, want 0", got)
	}
}

// TestLoginLimiterBounded: many clients never grow the table past its cap;
// the oldest are forgotten first.
func TestLoginLimiterBounded(t *testing.T) {
	l := auth.NewLoginLimiter()
	t0 := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	for i := range auth.MaxClients + 100 {
		l.Fail(netip.AddrFrom4([4]byte{10, byte(i >> 16), byte(i >> 8), byte(i)}), t0.Add(time.Duration(i)*time.Millisecond))
	}
	if n := l.Len(); n != auth.MaxClients {
		t.Fatalf("limiter holds %d clients, want %d", n, auth.MaxClients)
	}
	last := auth.MaxClients + 99
	newest := netip.AddrFrom4([4]byte{10, byte(last >> 16), byte(last >> 8), byte(last)})
	for range 5 {
		l.Fail(newest, t0.Add(time.Hour))
	}
	if got := l.Check(newest, t0.Add(time.Hour)); got != time.Second {
		t.Errorf("the newest client was forgotten: wait %v", got)
	}
}

// TestLoginLimiterForgetsAfterADay: failures more than a day old don't count.
func TestLoginLimiterForgetsAfterADay(t *testing.T) {
	l := auth.NewLoginLimiter()
	ip := netip.MustParseAddr("192.0.2.50")
	t0 := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	for range 5 {
		l.Fail(ip, t0)
	}
	later := t0.Add(25 * time.Hour)
	l.Fail(ip, later)
	if got := l.Check(ip, later); got != 0 {
		t.Errorf("a failure a day later waits %v, want 0", got)
	}
}

// TestLoginLimiterGlobalBudget: 50 failures within an hour, from anywhere,
// make every further attempt wait 2 s before it is checked, so a forged
// address per attempt can't brute-force at full speed. It never locks out,
// and an hour later the delay is gone.
func TestLoginLimiterGlobalBudget(t *testing.T) {
	l := auth.NewLoginLimiter()
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	for i := range 49 {
		l.Fail(netip.AddrFrom4([4]byte{198, 51, 100, byte(i)}), now)
	}
	if d := l.Delay(now); d != 0 {
		t.Fatalf("Delay after 49 failures = %v, want 0", d)
	}
	l.Fail(netip.AddrFrom4([4]byte{198, 51, 100, 49}), now.Add(time.Minute))
	if d := l.Delay(now.Add(time.Minute)); d != 2*time.Second {
		t.Fatalf("Delay after 50 failures = %v, want 2s", d)
	}
	if w := l.Check(netip.MustParseAddr("203.0.113.9"), now.Add(time.Minute)); w != 0 {
		t.Fatalf("a new address is locked out: %v", w)
	}
	if d := l.Delay(now.Add(time.Hour + 30*time.Second)); d != 0 {
		t.Fatalf("Delay an hour later = %v, want 0", d)
	}
}

// TestLoginLimiterGlobalBudgetCountsTheNewest: the window is the newest 50
// failures, not the first 50 ever.
func TestLoginLimiterGlobalBudgetCountsTheNewest(t *testing.T) {
	l := auth.NewLoginLimiter()
	t0 := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	for i := range 100 {
		at := t0
		if i >= 50 {
			at = t0.Add(2 * time.Hour)
		}
		l.Fail(netip.AddrFrom4([4]byte{198, 51, byte(i / 250), byte(i)}), at)
	}
	if d := l.Delay(t0.Add(2*time.Hour + time.Minute)); d != 2*time.Second {
		t.Fatalf("Delay after 50 fresh failures = %v, want 2s", d)
	}
}
