package main

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/schedule"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

type fakeStarter struct {
	scopes []verify.Scope
	err    error
}

func (f *fakeStarter) Start(_ context.Context, s verify.Scope) (verify.Run, error) {
	f.scopes = append(f.scopes, s)
	return verify.Run{ID: 9, Scope: s}, f.err
}

func TestScheduledRunsStart(t *testing.T) {
	slot := time.Date(2026, 11, 2, 4, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		slot      schedule.Slot
		err       error
		wantScope verify.Scope
		wantSkip  bool
		wantErr   bool
	}{
		{name: "an incremental slot", slot: schedule.Slot{Kind: schedule.Incremental, At: slot}, wantScope: verify.Scope{Incremental: true, ScheduledFor: slot}},
		{name: "a full slot", slot: schedule.Slot{Kind: schedule.Full, At: slot}, wantScope: verify.Scope{ScheduledFor: slot}},
		{name: "a run is active", slot: schedule.Slot{Kind: schedule.Full, At: slot}, err: verify.ErrRunActive, wantScope: verify.Scope{ScheduledFor: slot}, wantSkip: true},
		{name: "Calibre is not ready", slot: schedule.Slot{Kind: schedule.Full, At: slot}, err: fmt.Errorf("%w: down", verify.ErrCalibreNotReady), wantScope: verify.Scope{ScheduledFor: slot}, wantSkip: true},
		{name: "the runner is stopping", slot: schedule.Slot{Kind: schedule.Full, At: slot}, err: fmt.Errorf("%w: stopping", verify.ErrRunnerStopped), wantScope: verify.Scope{ScheduledFor: slot}, wantSkip: true},
		{name: "anything else is an error, not a skip", slot: schedule.Slot{Kind: schedule.Full, At: slot}, err: errors.New("database is down"), wantScope: verify.Scope{ScheduledFor: slot}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeStarter{err: tt.err}
			err := scheduledRuns{f}.Start(context.Background(), tt.slot)
			if len(f.scopes) != 1 || f.scopes[0] != tt.wantScope {
				t.Errorf("started %+v, want %+v", f.scopes, tt.wantScope)
			}
			switch {
			case tt.wantSkip && !errors.Is(err, schedule.ErrSkipped):
				t.Errorf("err = %v, want a skip", err)
			case tt.wantErr && (err == nil || errors.Is(err, schedule.ErrSkipped)):
				t.Errorf("err = %v, want a real error", err)
			case !tt.wantSkip && !tt.wantErr && err != nil:
				t.Errorf("err = %v", err)
			}
		})
	}
}

type fakeLast struct {
	any, full *verify.Run
	lastFull  *verify.Run // the newest whole-library, non-incremental run
	err       error
}

func (f fakeLast) LastFullRun(context.Context) (verify.Run, bool, error) {
	if f.err != nil || f.lastFull == nil {
		return verify.Run{}, false, f.err
	}
	return *f.lastFull, true, nil
}

func (f fakeLast) LastScheduled(_ context.Context, fullOnly bool) (verify.Run, bool, error) {
	r := f.any
	if fullOnly {
		r = f.full
	}
	if f.err != nil || r == nil {
		return verify.Run{}, false, f.err
	}
	return *r, true, nil
}

func TestScheduleHistory(t *testing.T) {
	at := func(h int) time.Time { return time.Date(2026, 11, 2, h, 0, 0, 0, time.UTC) }
	incr := func(h int) *verify.Run {
		return &verify.Run{ID: 4, Status: verify.RunCompleted, Scope: verify.Scope{Incremental: true, ScheduledFor: at(h)}}
	}
	full := func(h int) *verify.Run {
		return &verify.Run{ID: 3, Status: verify.RunRunning, Scope: verify.Scope{ScheduledFor: at(h)}}
	}
	// started is a full run somebody started at hour h, not for any slot.
	started := func(h int) *verify.Run { return &verify.Run{ID: 8, Status: verify.RunCompleted, StartedAt: at(h)} }
	tests := []struct {
		name string
		last fakeLast
		slot schedule.Slot
		want bool
	}{
		{"nothing scheduled yet", fakeLast{}, schedule.Slot{Kind: schedule.Incremental, At: at(4)}, false},
		{"the same incremental slot", fakeLast{any: incr(4)}, schedule.Slot{Kind: schedule.Incremental, At: at(4)}, true},
		{"an earlier incremental slot", fakeLast{any: incr(3)}, schedule.Slot{Kind: schedule.Incremental, At: at(4)}, false},
		{"a later full run covers an incremental slot", fakeLast{any: full(5)}, schedule.Slot{Kind: schedule.Incremental, At: at(4)}, true},
		{"an incremental run never covers a full slot", fakeLast{any: incr(5), full: nil}, schedule.Slot{Kind: schedule.Full, At: at(4)}, false},
		{"an older full run does not either", fakeLast{any: incr(5), full: full(3)}, schedule.Slot{Kind: schedule.Full, At: at(4)}, false},
		{"the same full slot", fakeLast{any: full(4), full: full(4)}, schedule.Slot{Kind: schedule.Full, At: at(4)}, true},
		{"a manual full run after the slot serves a full slot", fakeLast{lastFull: started(5)}, schedule.Slot{Kind: schedule.Full, At: at(4)}, true},
		{"and an incremental slot", fakeLast{lastFull: started(5)}, schedule.Slot{Kind: schedule.Incremental, At: at(4)}, true},
		{"a manual full run exactly at the slot", fakeLast{lastFull: started(4)}, schedule.Slot{Kind: schedule.Full, At: at(4)}, true},
		{"a manual full run before the slot serves nothing", fakeLast{lastFull: started(3)}, schedule.Slot{Kind: schedule.Full, At: at(4)}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := scheduleHistory{tt.last}.Served(context.Background(), tt.slot)
			if err != nil || got != tt.want {
				t.Errorf("Served() = %v, %v; want %v", got, err, tt.want)
			}
		})
	}
	t.Run("Last reads the kind and status", func(t *testing.T) {
		got, ok, err := scheduleHistory{fakeLast{any: incr(4)}}.Last(context.Background())
		if err != nil || !ok || got.RunID != 4 || got.Slot.Kind != schedule.Incremental || !got.Slot.At.Equal(at(4)) || got.Status != "completed" {
			t.Errorf("Last() = %+v, %v, %v", got, ok, err)
		}
		if _, ok, _ := (scheduleHistory{fakeLast{}}).Last(context.Background()); ok {
			t.Error("Last() found a run in an empty history")
		}
	})
	t.Run("errors pass through", func(t *testing.T) {
		boom := errors.New("boom")
		if _, err := (scheduleHistory{fakeLast{err: boom}}).Served(context.Background(), schedule.Slot{Kind: schedule.Full, At: at(4)}); !errors.Is(err, boom) {
			t.Errorf("Served() = %v", err)
		}
	})
}

func TestBusyReason(t *testing.T) {
	tests := []struct {
		name      string
		applying  bool
		undoing   int
		reviewing bool
		reviewErr error
		want      string
	}{
		{"nothing is going on", false, 0, false, nil, ""},
		{"an apply is running", true, 0, false, nil, "changes to Calibre are being written"},
		{"an undo is running", false, 1, false, nil, "an undo is writing to Calibre"},
		{"the agents are reviewing", false, 0, true, nil, "the agents are reviewing a run"},
		{"the review state can't be read: wait", false, 0, false, errors.New("db"), "the agent review state could not be read"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			busy := busyReason(func() bool { return tt.applying }, func() int { return tt.undoing },
				func(context.Context) (bool, error) { return tt.reviewing, tt.reviewErr })
			if got := busy(context.Background()); got != tt.want {
				t.Errorf("busy = %q, want %q", got, tt.want)
			}
		})
	}
	if got := busyReason(func() bool { return false }, func() int { return 0 }, nil)(context.Background()); got != "" {
		t.Errorf("without a review source: %q", got)
	}
}
