package retention

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakePrune struct {
	mu    sync.Mutex
	keeps []int
	res   Result
	err   error
}

func (f *fakePrune) prune(_ context.Context, keep int) (Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keeps = append(f.keeps, keep)
	return f.res, f.err
}

func (f *fakePrune) calls() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int(nil), f.keeps...)
}

func policy(keep int, f *fakePrune, log *bytes.Buffer) *Policy {
	return &Policy{Keep: keep, Prune: f.prune, Logger: slog.New(slog.NewTextHandler(log, &slog.HandlerOptions{Level: slog.LevelDebug}))}
}

func TestAfterRunPrunesToKeep(t *testing.T) {
	var log bytes.Buffer
	f := &fakePrune{res: Result{Runs: 2, Excerpts: 31}}
	policy(10, f, &log).AfterRun(context.Background())
	if got := f.calls(); len(got) != 1 || got[0] != 10 {
		t.Fatalf("Prune calls = %v, want [10]", got)
	}
	// Counts only.
	line := log.String()
	if !strings.Contains(line, "runs=2") || !strings.Contains(line, "excerpts=31") {
		t.Errorf("the log does not say what was pruned: %s", line)
	}
}

func TestKeepZeroKeepsEverything(t *testing.T) {
	var log bytes.Buffer
	f := &fakePrune{}
	p := policy(0, f, &log)
	p.AfterRun(context.Background())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p.Run(ctx)
	if got := f.calls(); len(got) != 0 {
		t.Errorf("Prune was called with keep 0: %v", got)
	}
}

func TestNothingPrunedIsQuiet(t *testing.T) {
	var log bytes.Buffer
	policy(10, &fakePrune{}, &log).AfterRun(context.Background())
	if strings.Contains(log.String(), "level=INFO") {
		t.Errorf("an empty prune logged at info: %s", log.String())
	}
}

func TestPruneErrorIsLoggedNotFatal(t *testing.T) {
	var log bytes.Buffer
	f := &fakePrune{err: errors.New("database is down")}
	policy(10, f, &log).AfterRun(context.Background())
	if !strings.Contains(log.String(), "level=ERROR") || !strings.Contains(log.String(), "database is down") {
		t.Errorf("log: %s", log.String())
	}
}

func TestRunPrunesAtStartAndOnceADay(t *testing.T) {
	var log bytes.Buffer
	f := &fakePrune{}
	p := policy(10, f, &log)
	ctx, cancel := context.WithCancel(context.Background())
	var slept []time.Duration
	p.Sleep = func(ctx context.Context, d time.Duration) bool {
		slept = append(slept, d)
		if len(slept) == 3 {
			cancel()
		}
		return ctx.Err() == nil
	}
	p.Run(ctx)
	if got := f.calls(); len(got) != 3 {
		t.Errorf("%d prunes in three sleeps, want 3 (at start, then one per sleep)", len(got))
	}
	for _, d := range slept {
		if d != 24*time.Hour {
			t.Errorf("slept %v, want a day", d)
		}
	}
}

func TestPrunesDoNotOverlap(t *testing.T) {
	var log bytes.Buffer
	inside, maxInside := 0, 0
	var mu sync.Mutex
	p := &Policy{Keep: 10, Logger: slog.New(slog.NewTextHandler(&log, nil)), Prune: func(context.Context, int) (Result, error) {
		mu.Lock()
		inside++
		maxInside = max(maxInside, inside)
		mu.Unlock()
		time.Sleep(5 * time.Millisecond)
		mu.Lock()
		inside--
		mu.Unlock()
		return Result{}, nil
	}}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() { p.AfterRun(context.Background()) })
	}
	wg.Wait()
	if maxInside != 1 {
		t.Errorf("%d prunes ran at once", maxInside)
	}
}
