package report

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/decide"
	"github.com/felixapel/calibre-bookwarden/internal/findings"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

// open opens the run's snapshot for a page. The reader it returns is non-nil
// only when the page may be served from the projection: the run has a check
// that passed and is newer than the run's end (ADR-038). Without a check the
// caller walks and verifies the run, as before. A damaged run fails the page
// with the integrity error that names its first broken book, except that a
// page for one book (bookID > 0) walks, which verifies only that book.
func (s *Service) open(ctx context.Context, runID int64, bookID int) (Snapshot, ProjectionReader, error) {
	snap, err := s.store.Snapshot(ctx, runID)
	if err != nil {
		return nil, nil, err
	}
	pr, ok := snap.(ProjectionReader)
	if !ok {
		return snap, nil, nil
	}
	check, found, err := pr.SealCheck(ctx)
	if err != nil {
		_ = snap.Close(ctx)
		return nil, nil, err
	}
	switch {
	case !found:
		return snap, nil, nil
	case check.Broken > 0 && bookID > 0:
		return snap, nil, nil
	case check.Broken > 0:
		_ = snap.Close(ctx)
		return nil, nil, fmt.Errorf("book %d: %w", check.FirstBroken, ErrIntegrity)
	}
	return snap, pr, nil
}

// checkProjectedCursor applies the walk's cursor rules to the projection: a
// filtered book must be frozen in the run, and the cursor must name a checked
// book and an ordinal that book has.
func checkProjectedCursor(ctx context.Context, pr ProjectionReader, bookID int, cursor *Cursor, items func(ProjectedBook) int) error {
	if bookID > 0 {
		b, err := pr.ProjectedBook(ctx, bookID)
		if err != nil {
			return err
		}
		if !b.InRun {
			return ErrNoSuchBook
		}
	}
	if cursor == nil {
		return nil
	}
	b, err := pr.ProjectedBook(ctx, cursor.BookID)
	if err != nil {
		return err
	}
	if !b.Checked || cursor.Ordinal >= items(b) {
		return ErrInvalidCursor
	}
	return nil
}

// CheckSeals verifies the whole finished run once, exactly as a page walk
// verifies each book, and keeps the result and the projection the pages read
// (ADR-038). A damaged book is counted and the walk continues; only a
// failure to read ends it.
func (s *Service) CheckSeals(ctx context.Context, runID int64) (SealCheck, error) {
	ss, ok := s.store.(SealStore)
	if !ok {
		return SealCheck{}, ErrNoSealStore
	}
	snap, err := s.store.Snapshot(ctx, runID)
	if err != nil {
		return SealCheck{}, err
	}
	defer func() { _ = snap.Close(ctx) }()
	if !snap.Run().Status.Terminal() {
		return SealCheck{}, fmt.Errorf("run %d: %w", runID, ErrRunNotFinished)
	}
	check := SealCheck{RunID: runID, FindingsVersion: findings.Version, DecisionVersion: decide.Version}
	var books []BookProjection
	for after := 0; ; {
		if err := ctx.Err(); err != nil {
			return SealCheck{}, err
		}
		b, found, err := snap.NextBook(ctx, after)
		if !found {
			if err != nil {
				return SealCheck{}, err
			}
			break
		}
		if err != nil {
			if !errors.Is(err, ErrIntegrity) || b.Ref.ID <= after {
				return SealCheck{}, err
			}
			check.Books++
			check.Broken++
			if check.FirstBroken == 0 {
				check.FirstBroken = b.Ref.ID
				check.Detail = Display(err.Error())
			}
			after = b.Ref.ID
			continue
		}
		check.Books++
		after = b.Ref.ID
		if b.Package == nil {
			continue
		}
		books = append(books, BookProjection{BookID: b.Ref.ID, Title: Display(b.Package.Calibre.Title), Tier: b.Decision.Tier, Findings: b.Findings, Proposals: b.Decision.Proposals,
			SearchText: SearchText(b.Package.Calibre.Title, b.Package.Calibre.Authors)})
	}
	if check.Broken > 0 {
		books = nil
	}
	if err := ss.SaveSealCheck(ctx, check, books); err != nil {
		return SealCheck{}, err
	}
	return check, nil
}

// SweepOptions tune the sweep: it first runs after First, then every Every,
// and checks the runs whose check is older than MaxAge or missing. Now is
// the clock; nil means time.Now.
type SweepOptions struct {
	First, Every, MaxAge time.Duration
	Now                  func() time.Time
	Logger               *slog.Logger
}

// DefaultSweep is the sweep of ADR-038: five minutes after startup, then every
// hour, over the checks older than 24 hours. A check is therefore refreshed
// at most MaxAge + Every (25 hours) after it was made.
var DefaultSweep = SweepOptions{First: 5 * time.Minute, Every: time.Hour, MaxAge: 24 * time.Hour}

// SweepOnce checks every finished run whose check is missing or older than
// maxAge, and returns the ids it checked. A run that can't be checked is
// logged and skipped; the others still are.
func (s *Service) SweepOnce(ctx context.Context, maxAge time.Duration, now func() time.Time, logger *slog.Logger) ([]int64, error) {
	ss, ok := s.store.(SealStore)
	if !ok {
		return nil, ErrNoSealStore
	}
	if now == nil {
		now = time.Now
	}
	ids, err := ss.RunsToCheck(ctx, now().Add(-maxAge))
	if err != nil {
		return nil, err
	}
	var done []int64
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return done, err
		}
		check, err := s.CheckSeals(ctx, id)
		if err != nil {
			if logger != nil {
				if errors.Is(err, verify.ErrNoSuchRun) {
					// Pruned between the listing and the check.
					logger.Debug("run is gone, not checked", "run", id)
				} else {
					logger.Warn("seal check failed", "run", id, "error", err)
				}
			}
			continue
		}
		done = append(done, id)
		if logger != nil {
			logger.Info("seals checked", "run", id, "books", check.Books, "broken", check.Broken, "first_broken", check.FirstBroken)
		}
	}
	return done, nil
}

// Sweep runs SweepOnce on o's schedule until ctx ends.
func (s *Service) Sweep(ctx context.Context, o SweepOptions) {
	timer := time.NewTimer(o.First)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if _, err := s.SweepOnce(ctx, o.MaxAge, o.Now, o.Logger); err != nil && ctx.Err() == nil && o.Logger != nil {
			o.Logger.Warn("seal sweep failed", "error", err)
		}
		timer.Reset(o.Every)
	}
}
