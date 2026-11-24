package main

import (
	"context"
	"errors"
	"log/slog"

	"github.com/felixapel/calibre-bookwarden/internal/heartbeat"
	"github.com/felixapel/calibre-bookwarden/internal/report"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

// afterRunner is the retention policy as the finished hook needs it.
type afterRunner interface{ AfterRun(ctx context.Context) }

// finishedHook is the check runner's Finished hook: it hands the run to the
// seal check (and, through it, the agent review), then prunes once the run has
// ended.
func finishedHook(seal func(context.Context, int64, verify.RunStatus), retain afterRunner) func(context.Context, int64, verify.RunStatus) {
	return func(ctx context.Context, runID int64, status verify.RunStatus) {
		seal(ctx, runID, status)
		if status.Terminal() {
			retain.AfterRun(ctx)
		}
	}
}

// runReader reads one run; *store.RunRepo implements it.
type runReader interface {
	Run(ctx context.Context, id int64) (verify.Run, error)
}

// reviewUnlessScheduled queues the automatic agent review only for a run a
// person started. A scheduled run (ScheduledFor set) never queues one: nobody
// is there to look at what the panel approves, and bookwarden stays out of
// unattended writing (ADR-039). A person can still start a review by hand from
// the Fixes page. A run that can't be read is not reviewed either.
func reviewUnlessScheduled(runs runReader, review func(context.Context, int64, verify.RunStatus), logger *slog.Logger) func(context.Context, int64, verify.RunStatus) {
	return func(ctx context.Context, runID int64, status verify.RunStatus) {
		run, err := runs.Run(ctx, runID)
		switch {
		case errors.Is(err, verify.ErrNoSuchRun):
			return // pruned already
		case err != nil:
			logger.Warn("agent: the run could not be read; no automatic review", "run", runID, "error", err)
			return
		case !run.Scope.ScheduledFor.IsZero():
			logger.Info("agent: a scheduled run is not reviewed automatically; start a review from its Fixes page", "run", runID)
			return
		}
		review(ctx, runID, status)
	}
}

// callCounter counts a run's extractor calls; *store.RunRepo implements it.
type callCounter interface {
	ExtractorCalls(ctx context.Context, runID int64) (calls, failed int, err error)
}

// heartbeatRuns is what the heartbeat reads about a run; *store.RunRepo
// implements it.
type heartbeatRuns interface {
	runReader
	callCounter
}

// heartbeatAfterSeals pushes how a scheduled run ended, once its seal check
// has returned (ADR-041). A run a person started sends nothing, so it can't
// reset the monitor's missed-run interval; neither does a run that can't be
// read. A seal check that could not run is reported as such. If the extractor
// calls can't be counted the beat goes without a rate. A stop in progress
// sends nothing.
func heartbeatAfterSeals(send func(context.Context, heartbeat.Beat), runs heartbeatRuns, logger *slog.Logger) sealsChecked {
	return func(ctx context.Context, runID int64, status verify.RunStatus, check report.SealCheck, sealErr error) {
		if ctx.Err() != nil {
			return
		}
		run, err := runs.Run(ctx, runID)
		switch {
		case errors.Is(err, verify.ErrNoSuchRun):
			return // pruned already
		case err != nil:
			if ctx.Err() == nil {
				logger.Warn("heartbeat: the run could not be read; no beat", "run", runID, "error", err)
			}
			return
		case run.Scope.ScheduledFor.IsZero():
			return // started by hand
		}
		res := heartbeat.Result{RunID: runID, Status: status, SealsChecked: sealErr == nil, Books: check.Books, Broken: check.Broken}
		calls, failed, err := runs.ExtractorCalls(ctx, runID)
		if err != nil {
			if ctx.Err() != nil {
				return // a stop began while counting
			}
			logger.Warn("heartbeat: extractor calls not counted", "run", runID, "error", err)
		} else {
			res.Calls, res.Failed = calls, failed
		}
		send(ctx, res.Beat())
	}
}
