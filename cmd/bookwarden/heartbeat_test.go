package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/heartbeat"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/report"
	"github.com/felixapel/calibre-bookwarden/internal/store"
	"github.com/felixapel/calibre-bookwarden/internal/testutil/pgtest"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

type fakeCalls struct {
	calls, failed int
	err           error
	// manual makes the run one a person started; runErr fails reading it.
	manual bool
	runErr error
}

func (f fakeCalls) ExtractorCalls(context.Context, int64) (int, int, error) {
	return f.calls, f.failed, f.err
}

func (f fakeCalls) Run(_ context.Context, id int64) (verify.Run, error) {
	if f.runErr != nil {
		return verify.Run{}, f.runErr
	}
	r := verify.Run{ID: id}
	if !f.manual {
		r.Scope.ScheduledFor = time.Date(2026, 11, 23, 2, 30, 0, 0, time.UTC)
	}
	return r, nil
}

func TestHeartbeatAfterSeals(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name     string
		ctx      context.Context
		status   verify.RunStatus
		check    report.SealCheck
		sealErr  error
		calls    fakeCalls
		wantSent bool
		wantUp   bool
		// wantWarns is how many warnings the hook logs.
		wantWarns int
	}{
		{name: "clean completed run", ctx: context.Background(), status: verify.RunCompleted, check: report.SealCheck{Books: 5}, calls: fakeCalls{calls: 100, failed: 5}, wantSent: true, wantUp: true},
		{name: "broken seals", ctx: context.Background(), status: verify.RunCompleted, check: report.SealCheck{Books: 5, Broken: 1}, wantSent: true},
		{name: "seal check could not run", ctx: context.Background(), status: verify.RunCompleted, sealErr: errors.New("boom"), wantSent: true},
		{name: "too many extractor failures", ctx: context.Background(), status: verify.RunCompleted, check: report.SealCheck{Books: 5}, calls: fakeCalls{calls: 100, failed: 6}, wantSent: true},
		{name: "failed run", ctx: context.Background(), status: verify.RunFailed, check: report.SealCheck{Books: 5}, wantSent: true},
		{name: "calls not countable still beats", ctx: context.Background(), status: verify.RunCompleted, check: report.SealCheck{Books: 5}, calls: fakeCalls{err: errors.New("db")}, wantSent: true, wantUp: true, wantWarns: 1},
		{name: "a stop in progress sends nothing", ctx: cancelled, status: verify.RunCompleted, check: report.SealCheck{Books: 5}},
		// Only a scheduled run beats (ADR-041 amendment): a run started by
		// hand must not reset Kuma's missed-run interval.
		{name: "a manual full run sends nothing", ctx: context.Background(), status: verify.RunCompleted, check: report.SealCheck{Books: 5}, calls: fakeCalls{manual: true}},
		{name: "a cancelled manual recheck sends nothing", ctx: context.Background(), status: verify.RunCancelled, calls: fakeCalls{manual: true}},
		{name: "a failed scheduled run is down", ctx: context.Background(), status: verify.RunFailed, wantSent: true},
		{name: "a run that can't be read sends nothing", ctx: context.Background(), status: verify.RunCompleted, check: report.SealCheck{Books: 5}, calls: fakeCalls{runErr: errors.New("db")}, wantWarns: 1},
		{name: "a pruned run sends nothing", ctx: context.Background(), status: verify.RunCompleted, check: report.SealCheck{Books: 5}, calls: fakeCalls{runErr: verify.ErrNoSuchRun}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got []heartbeat.Beat
			var logs bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logs, nil))
			send := func(_ context.Context, b heartbeat.Beat) { got = append(got, b) }
			heartbeatAfterSeals(send, tc.calls, logger)(tc.ctx, 15, tc.status, tc.check, tc.sealErr)
			if n := strings.Count(logs.String(), "level=WARN"); n != tc.wantWarns {
				t.Errorf("warnings = %d, want %d:\n%s", n, tc.wantWarns, logs.String())
			}
			if (len(got) == 1) != tc.wantSent {
				t.Fatalf("beats = %v, want sent: %v", got, tc.wantSent)
			}
			if tc.wantSent && got[0].Up != tc.wantUp {
				t.Errorf("Up = %v, want %v (%q)", got[0].Up, tc.wantUp, got[0].Msg)
			}
		})
	}
}

// stopDuringRun is a run reader that sees a stop begin while it reads.
type stopDuringRun struct {
	fakeCalls
	cancel context.CancelFunc
}

func (s stopDuringRun) Run(ctx context.Context, _ int64) (verify.Run, error) {
	s.cancel()
	return verify.Run{}, ctx.Err()
}

// TestHeartbeatStopWhileReadingTheRun: a stop that begins while the run is
// read sends nothing and warns about nothing.
func TestHeartbeatStopWhileReadingTheRun(t *testing.T) {
	var logs bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sent := false
	send := func(context.Context, heartbeat.Beat) { sent = true }
	heartbeatAfterSeals(send, stopDuringRun{cancel: cancel}, slog.New(slog.NewTextHandler(&logs, nil)))(ctx, 15, verify.RunCompleted, report.SealCheck{Books: 5}, nil)
	if sent || strings.Contains(logs.String(), "level=WARN") {
		t.Errorf("sent %v, logs %q; want nothing", sent, logs.String())
	}
}

// TestHeartbeatFollowsTheSealCheckWithoutDelayingTheReview: the follower is
// called once the check has returned and its result is known, and a follower
// that hangs does not hold the agent review back.
func TestHeartbeatFollowsTheSealCheckWithoutDelayingTheReview(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, pgtest.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	runs := store.NewRunRepo(st)
	r, err := runs.CreateRun(ctx, verify.Scope{}, "library", "v1", []library.BookRef{{ID: 1, UUID: "u1"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := runs.SetStatus(ctx, r.ID, verify.RunCompleted, ""); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	reviewed := make(chan struct{})
	release := make(chan struct{})
	var mu sync.Mutex
	var seen report.SealCheck
	var seenErr error
	var calls int
	hang := func(_ context.Context, _ int64, _ verify.RunStatus, check report.SealCheck, err error) {
		mu.Lock()
		seen, seenErr = check, err
		calls++
		mu.Unlock()
		<-release // a slow Kuma
	}
	review := func(context.Context, int64, verify.RunStatus) { close(reviewed) }
	var wg sync.WaitGroup
	sealOnFinish(ctx, &wg, report.New(store.NewReportRepo(st)), logger, review, hang)(ctx, r.ID, verify.RunCompleted)
	<-reviewed // returns although the follower is still blocked
	close(release)
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 || seenErr != nil || seen.Books != 1 || seen.RunID != r.ID {
		t.Errorf("follower called %d times with check %+v, err %v", calls, seen, seenErr)
	}
}
