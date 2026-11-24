package auth

import (
	"net/netip"
	"sync"
	"time"
)

const (
	freeFailures = 5
	baseWait     = time.Second
	maxWait      = 15 * time.Minute
	forgetAfter  = 24 * time.Hour

	// The global budget: past globalFailures failed logins within
	// globalWindow, from any clients, every attempt waits globalDelay.
	globalFailures = 50
	globalWindow   = time.Hour
	globalDelay    = 2 * time.Second
)

// LoginLimiter slows down repeated failures per client: the first five are
// free, then each further failure doubles the wait, up to 15 minutes. A
// success clears the record. A client is one IPv4 address or one IPv6 /64,
// since a single host can use a whole /64; at most MaxClients are remembered,
// and the one whose last failure is oldest is forgotten first.
//
// A global budget covers clients that change address on every attempt (a
// forged X-Forwarded-For behind a trusted proxy): after 50 failures within
// an hour, from anywhere, every attempt waits 2 seconds before it is checked.
// It slows, never locks out.
type LoginLimiter struct {
	mu       sync.Mutex
	failures map[netip.Prefix]*failureRecord
	recent   []time.Time // the newest global failures, at most globalFailures
}

// client is the key ip is counted under.
func client(ip netip.Addr) netip.Prefix {
	ip = ip.Unmap()
	if ip.Is6() {
		p, _ := ip.Prefix(64)
		return p
	}
	return netip.PrefixFrom(ip, ip.BitLen())
}

type failureRecord struct {
	count        int
	lastFailure  time.Time
	blockedUntil time.Time
}

// NewLoginLimiter returns an empty limiter.
func NewLoginLimiter() *LoginLimiter {
	return &LoginLimiter{failures: make(map[netip.Prefix]*failureRecord)}
}

// Check returns how long ip must still wait; zero means go ahead.
func (l *LoginLimiter) Check(ip netip.Addr, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	r, ok := l.failures[client(ip)]
	if !ok || !now.Before(r.blockedUntil) {
		return 0
	}
	return r.blockedUntil.Sub(now)
}

// Delay is how long any attempt waits before it is checked: globalDelay
// while globalFailures failures fall within the last globalWindow, else zero.
func (l *LoginLimiter) Delay(now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.recent) >= globalFailures && now.Sub(l.recent[0]) < globalWindow {
		return globalDelay
	}
	return 0
}

// Fail records a failed attempt from ip.
func (l *LoginLimiter) Fail(ip netip.Addr, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.recent) == globalFailures {
		l.recent = l.recent[1:]
	}
	l.recent = append(l.recent, now)
	key := client(ip)
	r := l.failures[key]
	if r != nil && now.Sub(r.lastFailure) > forgetAfter {
		*r = failureRecord{} // a day without failures starts over
	}
	if r == nil {
		if len(l.failures) >= MaxClients {
			l.forget(now)
		}
		r = &failureRecord{}
		l.failures[key] = r
	}
	r.count++
	r.lastFailure = now
	if r.count > freeFailures {
		wait := baseWait << min(r.count-freeFailures-1, 20)
		r.blockedUntil = now.Add(min(wait, maxWait))
	}
}

// Succeed forgets ip's failures.
func (l *LoginLimiter) Succeed(ip netip.Addr) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, client(ip))
}

// forget makes room in a full table: it drops every client without a
// failure for a day, or else the one whose last failure is oldest.
func (l *LoginLimiter) forget(now time.Time) {
	var oldest netip.Prefix
	var at time.Time
	for k, r := range l.failures {
		if now.Sub(r.lastFailure) > forgetAfter {
			delete(l.failures, k)
			continue
		}
		if at.IsZero() || r.lastFailure.Before(at) {
			oldest, at = k, r.lastFailure
		}
	}
	if len(l.failures) >= MaxClients {
		delete(l.failures, oldest)
	}
}

// MaxClients bounds how many clients the limiter remembers.
const MaxClients = 10000

// Len returns how many clients the limiter remembers.
func (l *LoginLimiter) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.failures)
}
