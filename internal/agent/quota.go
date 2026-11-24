package agent

import (
	"sync"
	"time"
)

// QuotaState is a seat's last quota exhaustion (ADR-033).
type QuotaState struct {
	At         time.Time
	RetryAfter time.Duration // the provider's Retry-After; zero if none came
}

// Quotas remembers each seat's last quota exhaustion for Settings. It lives
// in memory and starts empty after a restart. A nil *Quotas ignores records.
type Quotas struct {
	mu  sync.Mutex
	m   map[string]QuotaState
	now func() time.Time
}

// NewQuotas returns an empty Quotas.
func NewQuotas() *Quotas { return &Quotas{m: map[string]QuotaState{}, now: time.Now} }

// Record notes that seat ran out of quota. A zero at means now.
func (q *Quotas) Record(seat string, at time.Time, retryAfter time.Duration) {
	if q == nil {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if at.IsZero() {
		at = q.now()
	}
	q.m[seat] = QuotaState{At: at, RetryAfter: retryAfter}
}

// Clear forgets seat's exhaustion: it answered again.
func (q *Quotas) Clear(seat string) {
	if q == nil {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.m, seat)
}

// State returns seat's last quota exhaustion, if it had one.
func (q *Quotas) State(seat string) (QuotaState, bool) {
	if q == nil {
		return QuotaState{}, false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	st, ok := q.m[seat]
	return st, ok
}
