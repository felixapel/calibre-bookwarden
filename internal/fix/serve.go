package fix

import (
	"context"
	"errors"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/store"
)

// The ledger types the pages show.
type (
	Operation     = store.Operation
	Event         = store.Event
	Incident      = store.Incident
	ProposalRow   = store.ProposalRow
	SettingChange = store.SettingChange
	Basis         = store.Basis
	TypeLabels    = store.TypeLabels
	TypeMute      = store.TypeMute
	Unmute        = store.Unmute
	MuteType      = store.FixType
	RevealKey     = store.RevealKey
)

var (
	ErrClosed      = store.ErrClosed
	ErrDecided     = store.ErrDecided
	ErrNoIncident  = store.ErrNoIncident
	ErrNoOperation = store.ErrNoOperation
	ErrNotMuted    = store.ErrNotMuted
)

// ApplyStatus is the background apply's progress: running, or how the last
// one ended. Err is a short message without book data.
type ApplyStatus struct {
	Running           bool
	Started, Finished time.Time
	Summary           Summary
	Err               string
}

// Serve is the writer's goroutine (ADR-022): it recovers unfinished
// operations at start, then runs queued applies until ctx ends. Every
// recoverEvery, while writes are on, it retries operations still unfinished,
// such as a restore that couldn't reach Calibre; on this goroutine, so never
// during an apply. A stop lets the current book finish.
func (s *Service) Serve(ctx context.Context) error {
	if err := s.Recover(ctx); err != nil {
		s.logger.Warn("fix: recovery at start didn't finish; retrying every minute", "error", err)
	}
	tick := time.NewTicker(s.recoverEvery)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			s.retryUnfinished(ctx)
		case <-s.retryNow:
			s.retryUnfinished(ctx)
		case <-ctx.Done():
			select {
			case <-s.requests:
				s.finish(Summary{}, ctx.Err(), true)
			default:
			}
			return nil
		case by := <-s.requests:
			sum, err := s.Apply(ctx, by)
			if err != nil {
				s.logger.Warn("fix: apply stopped", "error", err)
			}
			s.finish(sum, err, ctx.Err() != nil)
		}
	}
}

// retryUnfinished runs recovery when an operation is unfinished and writes
// are on. With writes off a stranded restore waits: recovery would only
// record the steps again.
func (s *Service) retryUnfinished(ctx context.Context) {
	ops, err := s.fixes.Unfinished(ctx)
	if err != nil || len(ops) == 0 {
		return
	}
	if on, err := s.fixes.WritesEnabled(ctx); err != nil || !on {
		return
	}
	if err := s.Recover(ctx); err != nil && ctx.Err() == nil {
		s.logger.Warn("fix: retrying unfinished operations didn't finish; retrying in a minute", "error", err)
	}
}

// RetryRecovery queues an immediate retry of unfinished operations on the
// writer's goroutine, for the incidents page's "Retry now", and returns at
// once: a hung Calibre must not hold the request. ErrBusy while an apply is
// queued or running, ErrWritesOff while writes are off.
func (s *Service) RetryRecovery(ctx context.Context) error {
	if s.ApplyStatus().Running {
		return ErrBusy
	}
	on, err := s.fixes.WritesEnabled(ctx)
	if err != nil {
		return err
	}
	if !on {
		return ErrWritesOff
	}
	select {
	case s.retryNow <- struct{}{}:
	default: // one is queued already
	}
	return nil
}

// StartApply queues a background apply of every approved proposal. It
// returns at once: ErrWritesOff or ErrPaused when writes can't start,
// ErrBusy while an apply is queued or running.
func (s *Service) StartApply(ctx context.Context, by string) error {
	if err := s.ready(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status.Running {
		return ErrBusy
	}
	select {
	case s.requests <- by:
	default:
		return ErrBusy
	}
	s.status = ApplyStatus{Running: true, Started: time.Now()}
	return nil
}

// ApplyStatus returns the background apply's progress.
func (s *Service) ApplyStatus() ApplyStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

func (s *Service) finish(sum Summary, err error, stopping bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status.Running = false
	s.status.Finished = time.Now()
	s.status.Summary = sum
	s.status.Err = applyMessage(err, stopping)
}

// applyMessage says why an apply stopped, without book data.
func applyMessage(err error, stopping bool) string {
	switch {
	case err == nil:
		return ""
	case stopping:
		return "stopped: Bookwarden is shutting down; approved fixes not yet written stay approved"
	case errors.Is(err, ErrRefused):
		return "stopped: Calibre refused the write. The Calibre account needs write access (see the README, “Fixing”); the fixes not yet written stay approved"
	case errors.Is(err, ErrPaused):
		return "stopped: an operation didn't complete; see Incidents"
	case errors.Is(err, ErrWritesOff):
		return "stopped: changes to Calibre were switched off"
	case errors.Is(err, ErrBusy):
		return "not started: another write is running"
	}
	return "stopped: " + errorCode(err) + "; see the log"
}

// Undoable reports whether Undo can be tried: the operation completed and
// isn't undone. Whether the book is unchanged shows only when undoing.
func (s *Service) Undoable(ctx context.Context, id int64) (bool, error) {
	op, err := s.fixes.Operation(ctx, id)
	if err != nil {
		return false, err
	}
	if op.State != store.OpCompleted || op.After == nil {
		return false, nil
	}
	undone, err := s.fixes.HasUndo(ctx, id)
	return !undone, err
}

// Operations lists operations newest first, before an id (0: from the newest).
func (s *Service) Operations(ctx context.Context, before int64, limit int) ([]Operation, error) {
	return s.fixes.Operations(ctx, before, limit)
}

// Operation returns one operation; ErrNoOperation when there is none.
func (s *Service) Operation(ctx context.Context, id int64) (Operation, error) {
	return s.fixes.Operation(ctx, id)
}

// Events returns an operation's steps in order.
func (s *Service) Events(ctx context.Context, id int64) ([]Event, error) {
	return s.fixes.Events(ctx, id)
}

// RunProposals returns a run's decided proposals.
func (s *Service) RunProposals(ctx context.Context, runID int64) ([]ProposalRow, error) {
	return s.fixes.RunProposals(ctx, runID)
}

// RecordReveals remembers which undecided fixes had their verdicts shown
// (ADR-034).
func (s *Service) RecordReveals(ctx context.Context, runID int64, keys []store.RevealKey) error {
	return s.fixes.RecordReveals(ctx, runID, keys)
}

// Precision returns the blind individual labels per fix type (ADR-034).
func (s *Service) Precision(ctx context.Context) ([]TypeLabels, error) {
	return s.fixes.Precision(ctx)
}

// MuteStats returns the muting state of every fix type (v1 design section
// 6.3). It is review-layer only: nothing in fixing depends on it.
func (s *Service) MuteStats(ctx context.Context) ([]TypeMute, error) {
	return s.fixes.MuteStats(ctx)
}

// Unmute records that by showed a muted fix type of a library again.
func (s *Service) Unmute(ctx context.Context, libraryID string, t MuteType, by string) error {
	return s.fixes.Unmute(ctx, libraryID, t, by)
}

// ApprovedCount counts the proposals waiting to be applied.
func (s *Service) ApprovedCount(ctx context.Context) (int, error) {
	return s.fixes.ApprovedCount(ctx)
}

// Incidents lists every incident, newest first.
func (s *Service) Incidents(ctx context.Context) ([]Incident, error) {
	return s.fixes.Incidents(ctx)
}

// Acknowledge closes an incident; refused while its operation is unfinished.
func (s *Service) Acknowledge(ctx context.Context, id int64, by string) error {
	return s.fixes.Acknowledge(ctx, id, by)
}

// Paused reports whether an open incident pauses writes.
func (s *Service) Paused(ctx context.Context) (bool, error) {
	return s.fixes.Paused(ctx)
}

// WritesEnabled reads the "Allow changes to Calibre" switch.
func (s *Service) WritesEnabled(ctx context.Context) (bool, error) {
	return s.fixes.WritesEnabled(ctx)
}

// SetWritesEnabled sets the switch.
func (s *Service) SetWritesEnabled(ctx context.Context, on bool, by string) error {
	return s.fixes.SetWritesEnabled(ctx, on, by)
}

// SettingChanges lists the switch's changes, newest first.
func (s *Service) SettingChanges(ctx context.Context, limit int) ([]SettingChange, error) {
	return s.fixes.SettingChanges(ctx, limit)
}
