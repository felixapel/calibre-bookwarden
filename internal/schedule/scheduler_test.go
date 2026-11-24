package schedule

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// world is the runs a scheduler started, which is also its history: a slot is
// served when a run for it or for a later one was started, as the Postgres
// history reads runs.scheduled_for.
type world struct {
	mu       sync.Mutex
	started  []Slot
	startFn  func(Slot) error // nil: start
	servedFn func(Slot) error // nil: read the history
}

func (w *world) Start(_ context.Context, s Slot) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.startFn != nil {
		if err := w.startFn(s); err != nil {
			return err
		}
	}
	w.started = append(w.started, s)
	return nil
}

func (w *world) Served(_ context.Context, s Slot) (bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.servedFn != nil {
		if err := w.servedFn(s); err != nil {
			return false, err
		}
	}
	for _, st := range w.started {
		if !st.At.Before(s.At) && (s.Kind == Incremental || st.Kind == Full) {
			return true, nil
		}
	}
	return false, nil
}

func (w *world) Last(context.Context) (LastRun, bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.started) == 0 {
		return LastRun{}, false, nil
	}
	last := w.started[len(w.started)-1]
	return LastRun{RunID: int64(len(w.started)), Slot: last, Status: "completed"}, true, nil
}

func (w *world) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.started)
}

// clock is an injected clock: Sleep moves it and, after limit sleeps, stops
// the scheduler the way a drain does.
type clock struct {
	now    time.Time
	sleeps int
	limit  int
	cancel context.CancelFunc
}

func (c *clock) Now() time.Time { return c.now }

func (c *clock) Sleep(ctx context.Context, d time.Duration) bool {
	c.now = c.now.Add(d)
	c.sleeps++
	if c.sleeps >= c.limit {
		c.cancel()
	}
	return ctx.Err() == nil
}

func newScheduler(t *testing.T, cfg Config, w *world, now time.Time, limit int) (*Scheduler, *clock, context.Context, *bytes.Buffer) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	clk := &clock{now: now, limit: limit, cancel: cancel}
	var log bytes.Buffer
	s := &Scheduler{
		Config: cfg, Runs: w, History: w, Now: clk.Now, Sleep: clk.Sleep,
		Logger: slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
	return s, clk, ctx, &log
}

func parse(t *testing.T, inc, full string, loc *time.Location) Config {
	t.Helper()
	c, err := parseLoose(inc, full, loc)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestSchedulerStartsADueSlot(t *testing.T) {
	loc := berlin(t)
	w := &world{}
	cfg := parse(t, "04:00", "sun 04:00", loc)
	// Monday 2026-11-02, 03:59:40, then 30-second steps.
	s, _, ctx, _ := newScheduler(t, cfg, w, local(loc, 2026, 11, 2, 3, 59), 6)
	s.CatchUp = time.Minute // no catch-up of the previous day's slot
	s.Run(ctx)
	if len(w.started) != 1 || w.started[0].Kind != Incremental || !w.started[0].At.Equal(local(loc, 2026, 11, 2, 4, 0)) {
		t.Fatalf("started %+v; want one incremental run for Monday 04:00", w.started)
	}
}

func TestSchedulerFullWinsOnItsDay(t *testing.T) {
	loc := berlin(t)
	w := &world{}
	cfg := parse(t, "04:00", "sun 04:00", loc)
	s, _, ctx, _ := newScheduler(t, cfg, w, local(loc, 2026, 11, 1, 3, 59), 6)
	s.CatchUp = time.Minute
	s.Run(ctx)
	if len(w.started) != 1 || w.started[0].Kind != Full {
		t.Fatalf("started %+v; want one full run", w.started)
	}
}

func TestSchedulerSkipsAndDoesNotRetry(t *testing.T) {
	loc := berlin(t)
	tests := []struct {
		name      string
		busy      func(context.Context) string
		startErr  error
		wantStart int // calls that reached Runs.Start
		wantLog   string
	}{
		{name: "a run is active", startErr: fmt.Errorf("%w: a check is running", ErrSkipped), wantStart: 1, wantLog: "a check is running"},
		{name: "writes are in progress", busy: func(context.Context) string { return "changes to Calibre are being written" }, wantStart: 0, wantLog: "changes to Calibre are being written"},
		{name: "the agent is reviewing", busy: func(context.Context) string { return "the agent is reviewing a run" }, wantStart: 0, wantLog: "the agent is reviewing a run"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			w := &world{startFn: func(Slot) error { calls++; return tt.startErr }}
			cfg := parse(t, "04:00", "", loc)
			s, _, ctx, log := newScheduler(t, cfg, w, local(loc, 2026, 11, 2, 3, 59), 40)
			s.CatchUp = time.Minute
			s.Busy = tt.busy
			s.Run(ctx)
			if w.count() != 0 {
				t.Errorf("a run was started: %+v", w.started)
			}
			if calls != tt.wantStart {
				t.Errorf("Start was called %d times, want %d: a skipped slot is not retried on the next tick", calls, tt.wantStart)
			}
			if !strings.Contains(log.String(), "skipping") || !strings.Contains(log.String(), tt.wantLog) {
				t.Errorf("the skip was not logged with its reason:\n%s", log.String())
			}
			st, err := s.Status(ctx)
			if err != nil || st.Skipped == nil || !strings.Contains(st.Skipped.Reason, tt.wantLog) {
				t.Errorf("Status().Skipped = %+v, %v", st.Skipped, err)
			}
		})
	}
}

// TestASlotSurvivesAHistoryError: a database that is away for a minute at
// slot time doesn't cost the slot: the scheduler asks again on each tick.
func TestASlotSurvivesAHistoryError(t *testing.T) {
	loc := berlin(t)
	fails := 0
	w := &world{servedFn: func(Slot) error {
		if fails < 2 {
			fails++
			return errors.New("database is down")
		}
		return nil
	}}
	s, _, ctx, log := newScheduler(t, parse(t, "04:00", "", loc), w, local(loc, 2026, 11, 2, 3, 59), 40)
	s.CatchUp = time.Minute
	s.Run(ctx)
	if w.count() != 1 || !w.started[0].At.Equal(local(loc, 2026, 11, 2, 4, 0)) {
		t.Fatalf("started %+v; want the 04:00 run once, after two failed reads", w.started)
	}
	if !strings.Contains(log.String(), "database is down") || strings.Contains(log.String(), "slot dropped") {
		t.Errorf("log:\n%s", log.String())
	}
}

// TestASlotIsDroppedAfterFifteenMinutes: a start that keeps failing is tried
// on each tick for 15 minutes, then the slot is dropped, logged once.
func TestASlotIsDroppedAfterFifteenMinutes(t *testing.T) {
	loc := berlin(t)
	var tries []time.Time
	var clk *clock
	w := &world{startFn: func(Slot) error {
		tries = append(tries, clk.now)
		return errors.New("database is down")
	}}
	s, c, ctx, log := newScheduler(t, parse(t, "04:00", "", loc), w, local(loc, 2026, 11, 2, 3, 59), 60) // 30 minutes
	clk = c
	s.CatchUp = time.Minute
	s.Run(ctx)
	if w.count() != 0 || len(tries) < 2 {
		t.Fatalf("started %+v after %d tries", w.started, len(tries))
	}
	if first, last := tries[0], tries[len(tries)-1]; last.Sub(first) < 14*time.Minute || last.Sub(first) > 15*time.Minute {
		t.Errorf("tried from %s to %s; want about 15 minutes of tries", first.Format(time.TimeOnly), last.Format(time.TimeOnly))
	}
	if n := strings.Count(log.String(), "slot dropped"); n != 1 {
		t.Errorf("%d \"slot dropped\" lines, want 1:\n%s", n, log.String())
	}
	st, err := s.Status(ctx)
	if err != nil || st.Skipped == nil || !st.Skipped.Slot.At.Equal(local(loc, 2026, 11, 2, 4, 0)) || !strings.Contains(st.Skipped.Reason, "database is down") {
		t.Errorf("Status().Skipped = %+v, %v; want the dropped slot with its last error", st.Skipped, err)
	}
}

// TestASkipDuringARetryIsNotRetried: a slot being retried after a database
// error that is then skipped (a run is active) is dropped at once.
func TestASkipDuringARetryIsNotRetried(t *testing.T) {
	loc := berlin(t)
	calls := 0
	w := &world{startFn: func(Slot) error {
		calls++
		if calls == 1 {
			return errors.New("database is down")
		}
		return fmt.Errorf("%w: a check is running", ErrSkipped)
	}}
	s, _, ctx, log := newScheduler(t, parse(t, "04:00", "", loc), w, local(loc, 2026, 11, 2, 3, 59), 40)
	s.CatchUp = time.Minute
	s.Run(ctx)
	if calls != 2 || strings.Count(log.String(), "skipping") != 1 || strings.Contains(log.String(), "slot dropped") {
		t.Errorf("Start called %d times, log:\n%s", calls, log.String())
	}
}

// After a restart, a slot missed while the container was down runs once, if it
// is recent; older ones are dropped (ADR-039).
func TestSchedulerRestart(t *testing.T) {
	loc := berlin(t)
	cfg := parse(t, "04:00", "", loc)
	tests := []struct {
		name    string
		now     time.Time
		history []Slot
		want    int
	}{
		{"missed 20 minutes ago", local(loc, 2026, 11, 2, 4, 20), nil, 1},
		{"missed 11 hours ago", local(loc, 2026, 11, 2, 15, 0), nil, 1},
		{"missed 13 hours ago", local(loc, 2026, 11, 2, 17, 0), nil, 0},
		{"down for three days: one run, not three", local(loc, 2026, 11, 5, 5, 0), nil, 1},
		{"already served before the restart", local(loc, 2026, 11, 2, 4, 20), []Slot{{Incremental, local(loc, 2026, 11, 2, 4, 0)}}, 0},
		{"a later slot was served", local(loc, 2026, 11, 2, 4, 20), []Slot{{Full, local(loc, 2026, 11, 2, 4, 5)}}, 0},
		{"only an earlier slot was served", local(loc, 2026, 11, 2, 4, 20), []Slot{{Incremental, local(loc, 2026, 11, 1, 4, 0)}}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := &world{started: tt.history}
			s, _, ctx, _ := newScheduler(t, cfg, w, tt.now, 100)
			s.Run(ctx)
			if got := w.count() - len(tt.history); got != tt.want {
				t.Errorf("started %d runs, want %d: %+v", got, tt.want, w.started)
			}
		})
	}
}

func TestSchedulerFullIsCaughtUpButNotTheIncrementalItReplaces(t *testing.T) {
	loc := berlin(t)
	cfg := parse(t, "04:00", "sun 04:00", loc)
	w := &world{}
	// Sunday 04:20: the full run of 04:00 was missed.
	s, _, ctx, _ := newScheduler(t, cfg, w, local(loc, 2026, 11, 1, 4, 20), 100)
	s.Run(ctx)
	if len(w.started) != 1 || w.started[0].Kind != Full {
		t.Errorf("started %+v; want exactly the full run", w.started)
	}
}

func TestSchedulerStopsWhenTheContextEnds(t *testing.T) {
	loc := berlin(t)
	w := &world{}
	s, clk, ctx, _ := newScheduler(t, parse(t, "04:00", "", loc), w, local(loc, 2026, 11, 2, 12, 0), 3)
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its context ended")
	}
	if clk.sleeps != 3 {
		t.Errorf("slept %d times, want 3", clk.sleeps)
	}
}

// Over the days Europe/Berlin changes its clocks, a daily run starts exactly
// once per local day (no double run, no skipped day).
func TestSchedulerAcrossDST(t *testing.T) {
	loc := berlin(t)
	tests := []struct {
		name  string
		slot  string
		start time.Time
	}{
		{"spring forward, slot in the gap", "02:30", local(loc, 2027, 3, 26, 12, 0)},
		{"spring forward, slot after the gap", "03:30", local(loc, 2027, 3, 26, 12, 0)},
		{"fall back, slot in the repeated hour", "02:30", local(loc, 2026, 10, 23, 12, 0)},
		{"fall back, slot after it", "04:00", local(loc, 2026, 10, 23, 12, 0)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := &world{}
			// Four days of 30-second ticks.
			s, _, ctx, _ := newScheduler(t, parse(t, tt.slot, "", loc), w, tt.start, 4*24*120)
			s.CatchUp = time.Minute
			s.Run(ctx)
			perDay := map[string]int{}
			for _, st := range w.started {
				perDay[st.At.In(loc).Format("2006-01-02")]++
			}
			// The walk starts at noon on day 0 and runs 96 hours: the slots
			// of days 1, 2, 3 and 4 fall inside it.
			for i := 1; i <= 4; i++ {
				day := tt.start.AddDate(0, 0, i).Format("2006-01-02")
				if perDay[day] != 1 {
					t.Errorf("%s: %d runs, want 1 (all: %v)", day, perDay[day], perDay)
				}
			}
			if len(w.started) != 4 {
				t.Errorf("%d runs in four days, want 4", len(w.started))
			}
		})
	}
}

func TestStatus(t *testing.T) {
	loc := berlin(t)
	w := &world{}
	cfg := parse(t, "04:00", "sun 05:00", loc)
	s, _, ctx, _ := newScheduler(t, cfg, w, local(loc, 2026, 10, 31, 12, 0), 1)
	st, err := s.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Enabled || st.Incremental != "04:00" || st.Full != "sun 05:00" || st.Zone != "Europe/Berlin" || st.Last != nil {
		t.Errorf("Status() = %+v", st)
	}
	if !st.NextIncremental.Equal(local(loc, 2026, 11, 2, 4, 0)) || !st.NextFull.Equal(local(loc, 2026, 11, 1, 5, 0)) {
		t.Errorf("next = %v and %v", st.NextIncremental, st.NextFull)
	}
	_ = w.Start(ctx, Slot{Full, local(loc, 2026, 11, 1, 5, 0)})
	if st, _ := s.Status(ctx); st.Last == nil || st.Last.Slot.Kind != Full {
		t.Errorf("Status().Last = %+v", st.Last)
	}
	off := &Scheduler{Config: parse(t, "", "", loc), Now: func() time.Time { return time.Time{} }}
	if st, _ := off.Status(ctx); st.Enabled || !st.NextIncremental.IsZero() {
		t.Errorf("Status() with the scheduler off = %+v", st)
	}
}

func TestSchedulerDefaults(t *testing.T) {
	s := &Scheduler{Config: parse(t, "04:00", "", time.UTC), Runs: &world{}, History: &world{}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s.setDefaults()
	if s.Poll != 30*time.Second || s.CatchUp != 12*time.Hour || s.Now == nil || s.Sleep == nil {
		t.Errorf("defaults = %v %v", s.Poll, s.CatchUp)
	}
}
