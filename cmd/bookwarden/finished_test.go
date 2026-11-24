package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

type fakeRunReader struct {
	runs map[int64]verify.Run
	err  error
}

func (f fakeRunReader) Run(_ context.Context, id int64) (verify.Run, error) {
	if f.err != nil {
		return verify.Run{}, f.err
	}
	r, ok := f.runs[id]
	if !ok {
		return verify.Run{}, verify.ErrNoSuchRun
	}
	return r, nil
}

// A run the scheduler started never queues an agent review by itself: nobody
// is there to look at what the panel would approve, and bookwarden stays out
// of unattended writing. A person's run does, as before (ADR-039).
func TestScheduledRunsQueueNoAgentReview(t *testing.T) {
	slot := time.Date(2026, 11, 2, 4, 0, 0, 0, time.UTC)
	runs := fakeRunReader{runs: map[int64]verify.Run{
		1: {ID: 1, Status: verify.RunCompleted},
		2: {ID: 2, Status: verify.RunCompleted, Scope: verify.Scope{ScheduledFor: slot}},
		3: {ID: 3, Status: verify.RunCompleted, Scope: verify.Scope{Incremental: true, ScheduledFor: slot}},
		4: {ID: 4, Status: verify.RunCompleted, Scope: verify.Scope{Incremental: true}}, // incremental by hand
	}}
	tests := []struct {
		name   string
		runID  int64
		reader fakeRunReader
		want   bool
		log    string
	}{
		{"a manual full run is reviewed", 1, runs, true, ""},
		{"a manual incremental run is reviewed", 4, runs, true, ""},
		{"a scheduled full run is not", 2, runs, false, "scheduled run"},
		{"a scheduled incremental run is not", 3, runs, false, "scheduled run"},
		{"a run that can't be read is not (fail closed)", 1, fakeRunReader{err: errors.New("db down")}, false, "db down"},
		{"a run that is gone is not", 9, runs, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var log bytes.Buffer
			var reviewed []int64
			hook := reviewUnlessScheduled(tt.reader, func(_ context.Context, id int64, _ verify.RunStatus) { reviewed = append(reviewed, id) },
				slog.New(slog.NewTextHandler(&log, nil)))
			hook(context.Background(), tt.runID, verify.RunCompleted)
			if got := len(reviewed) == 1 && reviewed[0] == tt.runID; got != tt.want {
				t.Errorf("reviewed = %v, want review %v", reviewed, tt.want)
			}
			if tt.log != "" && !strings.Contains(log.String(), tt.log) {
				t.Errorf("log %q lacks %q", log.String(), tt.log)
			}
		})
	}
}

type fakeAfterRun struct{ calls int }

func (f *fakeAfterRun) AfterRun(context.Context) { f.calls++ }

// Retention runs when a run has ended, and not while it is still going.
func TestFinishedHookPrunesOnlyAfterATerminalStatus(t *testing.T) {
	tests := []struct {
		status verify.RunStatus
		prune  bool
	}{
		{verify.RunCompleted, true},
		{verify.RunCancelled, true},
		{verify.RunFailed, true},
		{verify.RunRunning, false},
		{verify.RunWaiting, false},
	}
	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			retain := &fakeAfterRun{}
			var sealed []verify.RunStatus
			hook := finishedHook(func(_ context.Context, _ int64, s verify.RunStatus) { sealed = append(sealed, s) }, retain)
			hook(context.Background(), 5, tt.status)
			if (retain.calls == 1) != tt.prune || retain.calls > 1 {
				t.Errorf("AfterRun called %d times for %s, want prune=%v", retain.calls, tt.status, tt.prune)
			}
			if len(sealed) != 1 || sealed[0] != tt.status {
				t.Errorf("the seal hook heard %v", sealed)
			}
		})
	}
}
