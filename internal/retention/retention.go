// Package retention applies the run retention policy (ADR-039): after each
// run ends and once a day, it prunes old runs and the excerpts no package
// refers to. What may be pruned is decided by the store; this package only
// decides when, and says what happened in counts.
package retention

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"time"
)

// Result counts what one prune deleted.
type Result struct{ Runs, Excerpts, Raws int64 }

// PruneFunc deletes the ended runs beyond the newest keep that nothing needs,
// and then the excerpts and raw answers no package refers to;
// store.RunRepo.Prune is one.
type PruneFunc func(ctx context.Context, keep int) (Result, error)

// Policy is the retention policy.
type Policy struct {
	// Keep is how many of the newest runs are kept, besides the ones the
	// store always keeps. 0 keeps every run and every excerpt.
	Keep   int
	Prune  PruneFunc
	Logger *slog.Logger
	// Every is the sweep interval of Run; default one day.
	Every time.Duration
	// Sleep is the clock; tests inject theirs. It returns false when ctx ended.
	Sleep func(ctx context.Context, d time.Duration) bool

	mu sync.Mutex // one prune at a time
}

func (p *Policy) logger() *slog.Logger {
	if p.Logger == nil {
		return slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return p.Logger
}

// AfterRun prunes now; it is called when a run ends. An error is logged and
// the next prune tries again.
func (p *Policy) AfterRun(ctx context.Context) {
	if p.Keep <= 0 || p.Prune == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	res, err := p.Prune(ctx, p.Keep)
	switch {
	case err != nil && ctx.Err() == nil:
		p.logger().Error("retention: pruning failed", "error", err)
	case err != nil:
	case res.Runs > 0 || res.Excerpts > 0 || res.Raws > 0:
		p.logger().Info("retention: pruned", "runs", res.Runs, "excerpts", res.Excerpts, "raw_answers", res.Raws, "keep", p.Keep)
	default:
		p.logger().Debug("retention: nothing to prune", "keep", p.Keep)
	}
}

// Run prunes at start and then every day until ctx ends. The daily sweep
// covers what a run's end missed: a failed prune, or a process that was down.
func (p *Policy) Run(ctx context.Context) {
	if p.Keep <= 0 {
		p.logger().Info("retention: keeping every run (BOOKWARDEN_RETAIN_RUNS=0)")
		return
	}
	every, sleep := p.Every, p.Sleep
	if every <= 0 {
		every = 24 * time.Hour
	}
	if sleep == nil {
		sleep = func(ctx context.Context, d time.Duration) bool {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return false
			case <-t.C:
				return true
			}
		}
	}
	for {
		p.AfterRun(ctx)
		if !sleep(ctx, every) {
			return
		}
	}
}
