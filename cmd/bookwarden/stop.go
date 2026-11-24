package main

import (
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/store"
)

// lifecycle runs bookwarden's background workers and stops them in three
// steps, all inside one grace period (BOOKWARDEN_STOP_GRACE):
//
//  1. Begin tells the workers to stop taking work (the drain callbacks): the
//     runner takes no new book and the writer takes no new apply, while the
//     book in hand and the operation in progress finish (ADR-022).
//  2. Stop waits for the workers until the grace deadline.
//  3. Past it, Stop logs what is still running, runs the hard callbacks
//     (which cancel the work contexts) and waits 2 s longer, then gives
//     up and logs what it abandoned. The ledger and the run recover from that
//     as from a crash.
type lifecycle struct {
	grace    time.Duration
	hardWait time.Duration
	logger   *slog.Logger

	wg       sync.WaitGroup
	onDrain  []func()
	onHard   []func()
	describe func() []any

	beginOnce sync.Once
	deadline  time.Time
	stopOnce  sync.Once
	abandoned atomic.Bool
}

func newLifecycle(grace time.Duration, logger *slog.Logger) *lifecycle {
	return &lifecycle{grace: grace, hardWait: 2 * time.Second, logger: logger}
}

// Go runs f as a worker Stop waits for.
func (l *lifecycle) Go(f func()) { l.wg.Go(f) }

// OnDrain registers a callback for step 1. Register before Begin.
func (l *lifecycle) OnDrain(f func()) { l.onDrain = append(l.onDrain, f) }

// OnHard registers a callback for step 3. Register before Begin.
func (l *lifecycle) OnHard(f func()) { l.onHard = append(l.onHard, f) }

// Describe sets what the log says is still running when the grace expires.
func (l *lifecycle) Describe(f func() []any) { l.describe = f }

// Begin starts the stop: it fixes the deadline and drains the workers. It is
// safe to call more than once.
func (l *lifecycle) Begin() {
	l.beginOnce.Do(func() {
		l.deadline = time.Now().Add(l.grace)
		for _, f := range l.onDrain {
			f()
		}
	})
}

// Deadline is when the grace ends; it is set by Begin.
func (l *lifecycle) Deadline() time.Time {
	l.Begin()
	return l.deadline
}

// Stop waits for the workers within the grace, then cancels hard, then gives
// up. It begins the stop if that hasn't happened.
func (l *lifecycle) Stop() {
	l.stopOnce.Do(func() {
		l.Begin()
		done := make(chan struct{})
		go func() { l.wg.Wait(); close(done) }()
		timer := time.NewTimer(time.Until(l.deadline))
		defer timer.Stop()
		select {
		case <-done:
			return
		case <-timer.C:
		}
		var attrs []any
		if l.describe != nil {
			attrs = l.describe()
		}
		l.logger.Warn("stop grace expired; cancelling hard", append([]any{"grace", l.grace.String()}, attrs...)...)
		for _, f := range l.onHard {
			f()
		}
		wait := time.NewTimer(l.hardWait)
		defer wait.Stop()
		select {
		case <-done:
		case <-wait.C:
			l.abandoned.Store(true)
			l.logger.Error("workers abandoned at stop; the next start recovers them", attrs...)
		}
	})
}

// Abandoned reports whether Stop gave up on a worker. Such a worker may still
// hold a pooled connection, so the store must not be closed (pgxpool's Close
// waits for every connection).
func (l *lifecycle) Abandoned() bool { return l.abandoned.Load() }

// closeStore closes st, unless a worker was abandoned: then the store is left
// open for the process exit, which ends the connections.
func closeStore(l *lifecycle, st *store.Store, logger *slog.Logger) {
	if l.Abandoned() {
		logger.Warn("store left open for the process exit: a worker was abandoned")
		return
	}
	st.Close()
}
