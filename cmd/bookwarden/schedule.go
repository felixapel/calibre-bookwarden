package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/felixapel/calibre-bookwarden/internal/retention"
	"github.com/felixapel/calibre-bookwarden/internal/schedule"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

// starter is what the scheduler needs of the check runner.
type starter interface {
	Start(ctx context.Context, scope verify.Scope) (verify.Run, error)
}

// scheduledRuns starts the run a slot asks for. A run that is already active,
// a Calibre that isn't ready and a runner that is stopping are skips, not
// errors: the slot is dropped and the next one runs as usual (ADR-039).
type scheduledRuns struct{ runner starter }

func (a scheduledRuns) Start(ctx context.Context, s schedule.Slot) error {
	_, err := a.runner.Start(ctx, verify.Scope{Incremental: s.Kind == schedule.Incremental, ScheduledFor: s.At})
	switch {
	case err == nil:
		return nil
	case errors.Is(err, verify.ErrRunActive):
		return fmt.Errorf("%w: a check is already running", schedule.ErrSkipped)
	case errors.Is(err, verify.ErrCalibreNotReady):
		return fmt.Errorf("%w: Calibre isn't ready", schedule.ErrSkipped)
	case errors.Is(err, verify.ErrRunnerStopped):
		return fmt.Errorf("%w: bookwarden is stopping", schedule.ErrSkipped)
	}
	return err
}

// lastScheduled is the history of scheduled runs; *store.RunRepo implements it.
type lastScheduled interface {
	LastScheduled(ctx context.Context, fullOnly bool) (verify.Run, bool, error)
	LastFullRun(ctx context.Context) (verify.Run, bool, error)
}

// scheduleHistory answers from the database which slots runs were started
// for, so that a restart neither repeats a slot nor loses one.
type scheduleHistory struct{ runs lastScheduled }

// Served reports whether a run covers s. A full run of the whole library that
// started at or after the slot covers it whoever started it, because it
// measured everything (ADR-039). Scheduled runs also record the slot they
// were started for, so a later slot of the right kind covers an earlier one: a
// full run covers an incremental slot, but an incremental run never a full one.
func (h scheduleHistory) Served(ctx context.Context, s schedule.Slot) (bool, error) {
	if full, ok, err := h.runs.LastFullRun(ctx); err != nil {
		return false, err
	} else if ok && !full.StartedAt.Before(s.At) {
		return true, nil
	}
	run, ok, err := h.runs.LastScheduled(ctx, s.Kind == schedule.Full)
	if err != nil || !ok {
		return false, err
	}
	return !run.Scope.ScheduledFor.Before(s.At), nil
}

func (h scheduleHistory) Last(ctx context.Context) (schedule.LastRun, bool, error) {
	run, ok, err := h.runs.LastScheduled(ctx, false)
	if err != nil || !ok {
		return schedule.LastRun{}, false, err
	}
	kind := schedule.Full
	if run.Scope.Incremental {
		kind = schedule.Incremental
	}
	return schedule.LastRun{RunID: run.ID, Slot: schedule.Slot{Kind: kind, At: run.Scope.ScheduledFor}, Status: string(run.Status)}, true, nil
}

// startBackground starts the retention sweep and, when a schedule is
// configured, the scheduler, as lifecycle workers. Both end when stopCtx does,
// which a drain cancels (ADR-022), so a stop does not wait for them.
func startBackground(stopCtx context.Context, life *lifecycle, sched *schedule.Scheduler, retain *retention.Policy, logger *slog.Logger) {
	life.Go(func() { retain.Run(stopCtx) })
	if !sched.Config.Enabled() {
		return
	}
	logger.Info("scheduled checks are on", "incremental", sched.Config.IncrementalText(), "full", sched.Config.FullText(), "zone", sched.Config.Loc.String())
	life.Go(func() { sched.Run(stopCtx) })
}

// busyReason is the scheduler's Busy check: the reason scheduled work must
// wait, or "". Changes being written to Calibre, an undo and an agent review
// in progress all stand in its way. If the review state can't be read it
// waits too: a busy slot is dropped, not retried, so an unreadable review
// state costs the slot rather than risking a run beside a review.
func busyReason(applying func() bool, undoing func() int, reviewing func(context.Context) (bool, error)) func(context.Context) string {
	return func(ctx context.Context) string {
		switch {
		case applying():
			return "changes to Calibre are being written"
		case undoing() > 0:
			return "an undo is writing to Calibre"
		}
		if reviewing != nil {
			busy, err := reviewing(ctx)
			switch {
			case err != nil:
				return "the agent review state could not be read"
			case busy:
				return "the agents are reviewing a run"
			}
		}
		return ""
	}
}
