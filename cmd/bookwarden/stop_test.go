package main

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type syncLog struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncLog) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncLog) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func newTestLifecycle(grace time.Duration) (*lifecycle, *syncLog) {
	logs := &syncLog{}
	l := newLifecycle(grace, slog.New(slog.NewJSONHandler(logs, nil)))
	l.hardWait = 100 * time.Millisecond
	return l, logs
}

// TestStopWaitsForWorkersThatFinish: the drain signal goes out first, the
// workers finish in their own time within the grace, and nothing is
// cancelled hard.
func TestStopWaitsForWorkersThatFinish(t *testing.T) {
	l, logs := newTestLifecycle(5 * time.Second)
	var drained, hard atomic.Bool
	l.OnDrain(func() { drained.Store(true) })
	l.OnHard(func() { hard.Store(true) })
	l.Go(func() {
		for !drained.Load() {
			time.Sleep(time.Millisecond)
		}
		time.Sleep(50 * time.Millisecond) // the book in hand
	})
	start := time.Now()
	l.Begin()
	l.Stop()
	if took := time.Since(start); took < 50*time.Millisecond || took > 2*time.Second {
		t.Errorf("Stop took %v, want about the book in hand", took)
	}
	if hard.Load() || strings.Contains(logs.String(), "grace") {
		t.Errorf("hard stop although the worker finished: hard=%v log=%s", hard.Load(), logs.String())
	}
}

// TestStopCancelsHardAfterTheGrace: a worker that outlasts the grace is
// cancelled, and the log says what was abandoned.
func TestStopCancelsHardAfterTheGrace(t *testing.T) {
	l, logs := newTestLifecycle(50 * time.Millisecond)
	hardCh := make(chan struct{})
	l.OnHard(func() { close(hardCh) })
	l.Describe(func() []any { return []any{"run", 7, "apply_running", true} })
	l.Go(func() { <-hardCh })
	start := time.Now()
	l.Begin()
	l.Stop()
	if took := time.Since(start); took < 50*time.Millisecond || took > 2*time.Second {
		t.Errorf("Stop took %v, want about the 50ms grace", took)
	}
	for _, want := range []string{"stop grace expired", `"run":7`, `"apply_running":true`} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log lacks %s:\n%s", want, logs.String())
		}
	}
	if strings.Contains(logs.String(), "abandoned") {
		t.Errorf("the worker stopped on the hard cancel, nothing was abandoned:\n%s", logs.String())
	}
}

// TestStopIsBoundedByAWorkerThatIgnoresEverything: the old unbounded
// wg.Wait is gone; Stop returns after grace plus the short hard wait.
func TestStopIsBoundedByAWorkerThatIgnoresEverything(t *testing.T) {
	l, logs := newTestLifecycle(50 * time.Millisecond)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	l.Go(func() { <-release })
	start := time.Now()
	l.Begin()
	l.Stop()
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("Stop took %v with a stuck worker", took)
	}
	if !strings.Contains(logs.String(), "abandoned") {
		t.Errorf("the stuck worker was not reported:\n%s", logs.String())
	}
}

func TestStopWithoutBeginAndTwice(t *testing.T) {
	l, _ := newTestLifecycle(time.Second)
	var drains atomic.Int32
	l.OnDrain(func() { drains.Add(1) })
	l.Go(func() {})
	l.Stop() // begins by itself, e.g. on an error path
	l.Stop()
	l.Begin()
	if drains.Load() != 1 {
		t.Errorf("drain ran %d times, want once", drains.Load())
	}
}
