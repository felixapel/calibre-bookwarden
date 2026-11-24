package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/store"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

var (
	ErrOff          = errors.New("agent: agent fixing is off")
	ErrSeatsMissing = errors.New("agent: a judge isn't configured")
	ErrQueued       = errors.New("agent: a review of this run is already waiting")
)

// SeatStatus is a seat as Settings shows it: never its key.
type SeatStatus struct {
	Name, Model string
	Family      string
	Configured  bool
	// QuotaAt and QuotaRetryAfter are the seat's last quota exhaustion, from
	// memory; QuotaAt is zero if there was none since start.
	QuotaAt         time.Time
	QuotaRetryAfter time.Duration
}

// ServiceOptions configure a Service.
type ServiceOptions struct {
	Reviewer  *Reviewer
	Store     Store
	Decisions Decisions
	Seats     []SeatStatus // the whole panel, configured or not
	Quotas    *Quotas      // quota exhaustions the seats recorded; may be nil
	Logger    *slog.Logger
	// Retry is how often Serve looks for queued reviews; default 5 minutes.
	Retry time.Duration
}

// Service queues, starts and resumes reviews (ADR-029). It never decides or
// applies anything.
type Service struct {
	o        ServiceOptions
	wake     chan struct{}
	failures map[int64]int // consecutive failed attempts per review
}

// maxReviewFailures is how often a review may fail in a row before it is
// stopped, so one broken book can't hold the queue.
const maxReviewFailures = 3

// NewService returns the agent service.
func NewService(o ServiceOptions) *Service {
	if o.Logger == nil {
		o.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if o.Retry <= 0 {
		o.Retry = 5 * time.Minute
	}
	return &Service{o: o, wake: make(chan struct{}, 1), failures: map[int64]int{}}
}

func (s *Service) poke() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// missing names the seats without a key.
func (s *Service) missing() []string {
	var out []string
	for _, st := range s.o.Seats {
		if !st.Configured {
			out = append(out, st.Name)
		}
	}
	if len(s.o.Seats) == 0 {
		out = append(out, "every seat")
	}
	return out
}

// Seats returns the panel's status.
func (s *Service) Seats() []SeatStatus {
	out := make([]SeatStatus, len(s.o.Seats))
	for i, st := range s.o.Seats {
		if q, ok := s.o.Quotas.State(st.Name); ok {
			st.QuotaAt, st.QuotaRetryAfter = q.At, q.RetryAfter
		}
		out[i] = st
	}
	return out
}

// MaxTokens returns the token cap per run.
func (s *Service) MaxTokens() int64 {
	if s.o.Reviewer == nil {
		return 0
	}
	return s.o.Reviewer.o.MaxTokens
}

// Mode reads the agent fixing mode.
func (s *Service) Mode(ctx context.Context) (string, error) { return s.o.Store.Mode(ctx) }

// SetMode changes the mode: off, or shadow, which needs every seat
// configured. Mode on was retired (ADR-029).
func (s *Service) SetMode(ctx context.Context, mode, by string) error {
	switch mode {
	case store.ModeOff:
	case store.ModeShadow:
		if m := s.missing(); len(m) > 0 {
			return fmt.Errorf("%w: %s", ErrSeatsMissing, strings.Join(m, ", "))
		}
	default:
		return fmt.Errorf("agent: invalid mode %q", mode)
	}
	return s.o.Store.SetMode(ctx, mode, by)
}

// RunFinished queues the automatic review of a run that completed while
// agent fixing is in shadow. It is the check runner's Finished hook.
func (s *Service) RunFinished(ctx context.Context, runID int64, status verify.RunStatus) {
	if status != verify.RunCompleted {
		return
	}
	mode, err := s.o.Store.Mode(ctx)
	if err != nil {
		s.o.Logger.Error("agent: reading the mode failed; run not reviewed", "run", runID, "error", err)
		return
	}
	if mode == store.ModeOff {
		return
	}
	if m := s.missing(); len(m) > 0 {
		s.o.Logger.Warn("agent: a judge isn't configured; run not reviewed", "run", runID, "missing", strings.Join(m, ","))
		return
	}
	if _, _, err := s.o.Store.Queue(ctx, runID, store.ReviewAuto, mode, store.AgentActor); err != nil {
		s.o.Logger.Error("agent: queueing the review failed", "run", runID, "error", err)
		return
	}
	s.poke()
}

// Request queues a review of a run that a person asked for.
func (s *Service) Request(ctx context.Context, runID int64, by string) error {
	mode, err := s.o.Store.Mode(ctx)
	if err != nil {
		return err
	}
	if mode == store.ModeOff {
		return ErrOff
	}
	if m := s.missing(); len(m) > 0 {
		return fmt.Errorf("%w: %s", ErrSeatsMissing, strings.Join(m, ", "))
	}
	_, ok, err := s.o.Store.Queue(ctx, runID, store.ReviewManual, mode, by)
	if err != nil {
		return err
	}
	if !ok {
		return ErrQueued
	}
	s.poke()
	return nil
}

// Serve runs reviews one at a time, the interrupted one first, until ctx
// ends, and looks for queued reviews on every retry tick.
func (s *Service) Serve(ctx context.Context) error {
	tick := time.NewTicker(s.o.Retry)
	defer tick.Stop()
	for {
		s.runReviews(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-s.wake:
		case <-tick.C:
		}
	}
}

func (s *Service) runReviews(ctx context.Context) {
	if m := s.missing(); len(m) > 0 {
		return // reviews wait until every judge is configured again
	}
	for ctx.Err() == nil {
		rv, ok, err := s.o.Store.Next(ctx)
		if err != nil {
			if ctx.Err() == nil {
				s.o.Logger.Error("agent: reading the review queue failed", "error", err)
			}
			return
		}
		if !ok {
			return
		}
		if _, err := s.o.Reviewer.Review(ctx, rv); err != nil {
			if ctx.Err() != nil {
				return
			}
			s.failures[rv.ID]++
			if s.failures[rv.ID] < maxReviewFailures {
				s.o.Logger.Error("agent: review interrupted; retrying later", "review", rv.ID, "error", err)
				return
			}
			delete(s.failures, rv.ID)
			s.o.Logger.Error("agent: review failed repeatedly; stopped", "review", rv.ID, "error", err)
			if ferr := s.o.Store.Finish(ctx, rv.ID, store.ReviewStopped, "failed repeatedly: "+ErrorCode(err)); ferr != nil {
				return
			}
			continue
		}
		delete(s.failures, rv.ID)
	}
}
