package main

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/retention"
	"github.com/felixapel/calibre-bookwarden/internal/schedule"
)

type fakeSlotRuns struct{}

func (*fakeSlotRuns) Start(context.Context, schedule.Slot) error { return nil }

// A drain ends the scheduler and the retention sweep with every other worker
// (ADR-022): Stop returns at once, not after the grace, and nothing is
// cancelled hard.
func TestDrainStopsTheSchedulerAndRetention(t *testing.T) {
	l, logs := newTestLifecycle(5 * time.Second)
	stopCtx, stopAccepting := context.WithCancel(context.Background())
	defer stopAccepting()
	l.OnDrain(stopAccepting)
	var hard bool
	l.OnHard(func() { hard = true })

	cfg, err := schedule.Parse("04:00", "sun 05:00", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	pruned := make(chan struct{}, 1)
	sched := &schedule.Scheduler{Config: cfg, Runs: &fakeSlotRuns{}, History: scheduleHistory{fakeLast{}}}
	retain := &retention.Policy{Keep: 10, Prune: func(context.Context, int) (retention.Result, error) {
		select {
		case pruned <- struct{}{}:
		default:
		}
		return retention.Result{}, nil
	}}
	startBackground(stopCtx, l, sched, retain, slog.New(slog.DiscardHandler))
	select {
	case <-pruned:
	case <-time.After(5 * time.Second):
		t.Fatal("the retention sweep did not run at start")
	}
	start := time.Now()
	l.Begin()
	l.Stop()
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("Stop took %v: the workers did not end with the drain", took)
	}
	if hard || strings.Contains(logs.String(), "grace") {
		t.Errorf("hard stop: %v %s", hard, logs.String())
	}
}

// Nothing is started for a schedule that is off, and keeping every run starts
// no sweep.
func TestBackgroundWithEverythingOff(t *testing.T) {
	l, _ := newTestLifecycle(time.Second)
	stopCtx, stopAccepting := context.WithCancel(context.Background())
	defer stopAccepting()
	l.OnDrain(stopAccepting)
	cfg, err := schedule.Parse("", "", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	sched := &schedule.Scheduler{Config: cfg, Runs: &fakeSlotRuns{}, History: scheduleHistory{fakeLast{}}}
	startBackground(stopCtx, l, sched, &retention.Policy{Keep: 0}, slog.New(slog.DiscardHandler))
	l.Begin()
	l.Stop()
}
