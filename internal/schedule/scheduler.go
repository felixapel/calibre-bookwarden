package schedule

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"time"
)

// ErrSkipped is what Runs.Start returns, wrapped with the reason, when it
// declines a slot on purpose: a run is already active, Calibre isn't ready,
// or the runner is stopping. The scheduler logs it and moves on.
var ErrSkipped = errors.New("schedule: slot skipped")

// Runs starts the check for a slot. The implementation marks the run with the
// slot (so that History can tell it was served) and returns ErrSkipped, or an
// error wrapping it, when it declines.
type Runs interface {
	Start(ctx context.Context, s Slot) error
}

// LastRun is the run the scheduler most recently started.
type LastRun struct {
	RunID  int64
	Slot   Slot
	Status string
}

// History reads which slots runs were started for. It is the database, so
// that it survives a restart.
type History interface {
	// Served reports whether a scheduled run was started for s, or for a
	// later slot that covers it: a full run covers an incremental slot, an
	// incremental run never covers a full one.
	Served(ctx context.Context, s Slot) (bool, error)
	// Last returns the scheduled run with the latest slot.
	Last(ctx context.Context) (LastRun, bool, error)
}

// Skip is a slot the scheduler passed over, and why.
type Skip struct {
	Slot   Slot
	Reason string
	At     time.Time
}

// Status is what Settings shows.
type Status struct {
	Enabled         bool
	Incremental     string // as configured, "" when off
	Full            string
	Zone            string
	NextIncremental time.Time // zero when off
	NextFull        time.Time
	Last            *LastRun
	// Skipped is the latest slot passed over since this process started.
	Skipped *Skip
}

// Scheduler starts scheduled runs. Run is its worker: the caller runs it with
// the context that ends when a stop drains (ADR-022), so it returns then.
// Zero values take the defaults.
type Scheduler struct {
	Config  Config
	Runs    Runs
	History History
	// Busy returns why scheduled work must not start now ("" when nothing
	// stands in the way): changes to Calibre being written, an undo, an
	// agent review in progress.
	Busy func(ctx context.Context) string
	// Now and Sleep are the clock; tests inject theirs. Sleep returns false
	// when ctx ended.
	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration) bool
	// Poll is how often the scheduler looks at the clock; default 30 s.
	Poll time.Duration
	// CatchUp is how far back, at startup, a slot that nothing served still
	// counts; default 12 h. Older ones are dropped.
	CatchUp time.Duration
	Logger  *slog.Logger

	once    sync.Once
	mu      sync.Mutex
	skipped *Skip
}

func (s *Scheduler) setDefaults() {
	if s.Poll <= 0 {
		s.Poll = 30 * time.Second
	}
	if s.CatchUp <= 0 {
		s.CatchUp = 12 * time.Hour
	}
	if s.Now == nil {
		s.Now = time.Now
	}
	if s.Sleep == nil {
		s.Sleep = func(ctx context.Context, d time.Duration) bool {
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
	if s.Logger == nil {
		s.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
}

// slotRetry is how long a slot whose history read or start failed is tried
// again, from its first failure.
const slotRetry = 15 * time.Minute

// Run looks at the clock every Poll until ctx ends. A slot is due once: in
// normal running when the clock crosses it, and, right after a start, when it
// lies within CatchUp behind and nothing served it. A slot that cannot start
// now (a run is active, writes or the agent are busy) is logged and dropped,
// never retried, so there is no catch-up storm. A slot whose history read or
// start failed (the database is away) is tried again on each tick for
// slotRetry, then dropped.
func (s *Scheduler) Run(ctx context.Context) {
	s.once.Do(s.setDefaults)
	if !s.Config.Enabled() {
		return
	}
	last := s.Now().Add(-s.CatchUp)
	var (
		failed Slot // the slot being retried, if until is set
		until  time.Time
	)
	for ctx.Err() == nil {
		now := s.Now()
		slot, err := s.tick(ctx, last, now)
		switch {
		case err == nil:
			until = time.Time{}
			last = now
		case until.IsZero() || slot.Kind != failed.Kind || !slot.At.Equal(failed.At):
			failed, until = slot, now.Add(slotRetry)
			fallthrough
		case now.Before(until):
			// The window stays open just before the slot, so it is due again.
			last = slot.At.Add(-time.Nanosecond)
		default:
			s.Logger.Error("schedule: slot dropped after failing for 15 minutes", "kind", string(slot.Kind), "slot", slot.At, "error", err)
			s.skip(slot, now, "failed for 15 minutes: "+err.Error())
			until = time.Time{}
			last = now
		}
		if !s.Sleep(ctx, s.Poll) {
			return
		}
	}
}

// tick starts the run for the latest slot in (after, now], unless something
// stands in the way. It returns the error of a slot whose history read or
// start failed for a reason other than a skip or the end of ctx, which is
// tried again.
func (s *Scheduler) tick(ctx context.Context, after, now time.Time) (Slot, error) {
	slot, ok := s.Config.Due(after, now)
	if !ok {
		return slot, nil
	}
	served, err := s.History.Served(ctx, slot)
	if err != nil {
		if ctx.Err() != nil {
			return slot, nil
		}
		s.Logger.Warn("schedule: reading the run history failed; the slot is tried again", "kind", string(slot.Kind), "slot", slot.At, "error", err)
		return slot, err
	}
	if served {
		s.Logger.Debug("schedule: slot already served", "kind", string(slot.Kind), "slot", slot.At)
		return slot, nil
	}
	if s.Busy != nil {
		if reason := s.Busy(ctx); reason != "" {
			s.skip(slot, now, reason)
			return slot, nil
		}
	}
	err = s.Runs.Start(ctx, slot)
	switch {
	case err == nil:
		s.Logger.Info("schedule: run started", "kind", string(slot.Kind), "slot", slot.At)
	case errors.Is(err, ErrSkipped):
		s.skip(slot, now, err.Error())
	case ctx.Err() != nil:
	default:
		s.Logger.Warn("schedule: starting the run failed; the slot is tried again", "kind", string(slot.Kind), "slot", slot.At, "error", err)
		return slot, err
	}
	return slot, nil
}

func (s *Scheduler) skip(slot Slot, now time.Time, reason string) {
	s.mu.Lock()
	s.skipped = &Skip{Slot: slot, Reason: reason, At: now}
	s.mu.Unlock()
	s.Logger.Info("schedule: skipping the slot", "kind", string(slot.Kind), "slot", slot.At, "reason", reason)
}

// zoneName is the zone's name, or, for the process's local zone, which Go
// calls "Local", its abbreviation at now.
func zoneName(loc *time.Location, now time.Time) string {
	if loc == nil {
		return "UTC"
	}
	if name := loc.String(); name != "Local" {
		return name
	}
	abbr, _ := now.In(loc).Zone()
	return abbr
}

// Status reports the schedule, the next slots and the last scheduled run.
func (s *Scheduler) Status(ctx context.Context) (Status, error) {
	s.once.Do(s.setDefaults)
	st := Status{Enabled: s.Config.Enabled(), Incremental: s.Config.IncrementalText(), Full: s.Config.FullText()}
	if !st.Enabled {
		return st, nil
	}
	now := s.Now()
	loc := s.Config.Loc
	st.Zone = zoneName(loc, now)
	st.NextIncremental, st.NextFull = s.Config.Next(now)
	if s.History != nil {
		last, ok, err := s.History.Last(ctx)
		if err != nil {
			return st, err
		}
		if ok {
			last.Slot.At = last.Slot.At.In(loc) // the database gives UTC
			st.Last = &last
		}
	}
	s.mu.Lock()
	if s.skipped != nil {
		skip := *s.skipped
		skip.Slot.At, skip.At = skip.Slot.At.In(loc), skip.At.In(loc)
		st.Skipped = &skip
	}
	s.mu.Unlock()
	return st, nil
}
