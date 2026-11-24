package verify

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"golang.org/x/sync/semaphore"

	"github.com/felixapel/calibre-bookwarden/internal/decide"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/sources"
)

// Reasons a book ends source_changed or failed.
const (
	reasonDeleted      = "deleted"
	reasonUUIDChanged  = "uuid_changed"
	reasonChanged      = "changed"
	reasonUnsealable   = "unsealable"
	reasonCalibreError = "calibre_error"
	reasonScratchError = "scratch_error"
	// reasonStoreError: Bookwarden could not store this book's result.
	reasonStoreError = "store_error"
)

// bookFailed ends one book as failed without stopping the run.
type bookFailed struct {
	reason string
	err    error
}

func (e *bookFailed) Error() string { return e.reason + ": " + e.err.Error() }
func (e *bookFailed) Unwrap() error { return e.err }

// checker holds what checking a book needs from its run. sources may be nil.
type checker struct {
	lib       Library
	libraryID string
	scratch   string
	lim       Limits
	budget    *semaphore.Weighted
	logger    *slog.Logger
	hinter    Hinter
	sources   Sources
	// extraction extracts EPUB excerpts (M4, shadow); nil seals none.
	extraction Extraction
	runID      int64
	// incremental lets a book reuse its file evidence from priors (ADR-039).
	incremental bool
	priors      PriorSource
	// tools describes the native tools (ocr.ToolVersions); now is the clock.
	tools string
	now   func() time.Time
	// stats adds up where the run's time goes.
	stats *runStats
}

// clock returns the checker's clock.
func (c checker) clock() func() time.Time {
	if c.now != nil {
		return c.now
	}
	return time.Now
}

// timed runs f and adds its time to stage.
func (c checker) timed(stage string, f func()) {
	now := c.clock()
	start := now()
	f()
	c.stats.stage(stage, now().Sub(start))
}

// check checks one book: it reads the record, streams each format to
// scratch one at a time under the byte budget, looks its ISBNs up, re-reads
// the record, and seals the evidence if nothing changed meanwhile. It returns
// an error only for problems that concern the whole run (Calibre
// unreachable, refusing the account or incompatible, or ctx ending); every
// other problem is an Outcome.
func (c checker) check(ctx context.Context, ref library.BookRef) (Outcome, error) {
	out, err := c.checkOnce(ctx, ref)
	if err == nil {
		return out, nil
	}
	if runLevel(ctx, err) {
		return Outcome{}, err
	}
	var failed *bookFailed
	if errors.As(err, &failed) {
		return Outcome{State: BookFailed, Reason: failed.reason}, nil
	}
	return Outcome{State: BookFailed, Reason: reasonCalibreError}, nil
}

// runLevel reports whether err stops or pauses the whole run.
func runLevel(ctx context.Context, err error) bool {
	return ctx.Err() != nil ||
		errors.Is(err, library.ErrUnreachable) || errors.Is(err, library.ErrAuth) ||
		errors.Is(err, library.ErrForbidden) || errors.Is(err, library.ErrIncompatible)
}

func (c checker) checkOnce(ctx context.Context, ref library.BookRef) (Outcome, error) {
	var b library.Book
	var err error
	c.timed(StageCalibre, func() { b, err = c.lib.BookInLibrary(ctx, c.libraryID, ref.ID) })
	if errors.Is(err, library.ErrBookNotFound) {
		return Outcome{State: BookSourceChanged, Reason: reasonDeleted}, nil
	}
	if err != nil {
		return Outcome{}, err
	}
	if b.UUID != ref.UUID {
		return Outcome{State: BookSourceChanged, Reason: reasonUUIDChanged}, nil
	}
	snapshot, err := evidence.Snapshot(b)
	if err != nil {
		return Outcome{}, &bookFailed{reasonUnsealable, err}
	}

	key := inspectKey(c.lim, c.hinter != nil, c.tools)
	measured := c.clock()()
	formats, cover, bodies, priorAt, reused := c.priorEvidence(ctx, ref, b, snapshot, key, measured)
	if reused {
		measured = priorAt
	} else {
		formats, cover, bodies, err = c.fileEvidence(ctx, ref, b)
		if err != nil {
			return Outcome{}, err
		}
	}

	p := evidence.Package{
		Cover:         cover,
		SchemaVersion: evidence.SchemaVersion,
		LibraryID:     c.libraryID,
		BookID:        b.ID,
		UUID:          b.UUID,
		Snapshot:      snapshot,
		Calibre:       evidence.RecordFrom(b),
		Formats:       formats,
	}
	if c.sources != nil {
		c.timed(StageLookups, func() { p.Lookups = c.sources.Lookup(ctx, c.runID, sources.Seeds(p)) })
		if err := ctx.Err(); err != nil {
			return Outcome{}, err
		}
	}

	var raws []RawOutput
	c.timed(StageExtraction, func() { raws, err = c.extract(ctx, p.Formats, bodies) })
	if err != nil {
		return Outcome{}, err
	}

	var again library.Book
	c.timed(StageCalibre, func() { again, err = c.lib.BookInLibrary(ctx, c.libraryID, ref.ID) })
	if errors.Is(err, library.ErrBookNotFound) {
		return Outcome{State: BookSourceChanged, Reason: reasonDeleted}, nil
	}
	if err != nil {
		return Outcome{}, err
	}
	if s, err := evidence.Snapshot(again); err != nil || s != snapshot {
		return Outcome{State: BookSourceChanged, Reason: reasonChanged}, nil
	}

	raw, seal, err := decide.Seal(ctx, p)
	if err != nil {
		return Outcome{}, &bookFailed{reasonUnsealable, err}
	}
	return Outcome{State: BookChecked, Evidence: raw, Seal: seal, Excerpts: bodies, Reused: reused, InspectKey: key, MeasuredAt: measured, Raws: raws}, nil
}

// extract sets the extraction of every format with an EPUB excerpt, made
// afresh even when the file evidence is reused (the cache makes an unchanged
// one free), and clears any other. bodies holds the excerpt bodies by hash.
func (c checker) extract(ctx context.Context, formats []evidence.FormatEvidence, bodies map[string]string) ([]RawOutput, error) {
	var raws []RawOutput
	for i := range formats {
		formats[i].Extraction = nil
		ex := formats[i].Excerpt
		if c.extraction == nil || ex == nil || ex.Method != evidence.ExcerptEPUBText {
			continue
		}
		body, ok := bodies[ex.SHA256]
		if !ok {
			continue
		}
		x, rs, err := c.extraction.Extract(ctx, *ex, body)
		if err != nil {
			return nil, err
		}
		formats[i].Extraction = x
		raws = append(raws, rs...)
	}
	return raws, nil
}

// priorEvidence returns the file evidence of the book's newest sealed package
// when this run is incremental and nothing it depends on has changed
// (ADR-039). Anything else, including a store that fails, means a full check:
// reuse is only ever an optimisation.
func (c checker) priorEvidence(ctx context.Context, ref library.BookRef, b library.Book, snapshot, key string, now time.Time) ([]evidence.FormatEvidence, *evidence.CoverEvidence, map[string]string, time.Time, bool) {
	if !c.incremental || c.priors == nil {
		return nil, nil, nil, time.Time{}, false
	}
	prior, ok, err := c.priors.PriorEvidence(ctx, c.libraryID, ref)
	if err != nil {
		if ctx.Err() == nil {
			c.logger.Warn("the earlier evidence of a book could not be read; checking it in full", "book", ref.ID)
		}
		return nil, nil, nil, time.Time{}, false
	}
	if !ok || !reusable(prior, c.libraryID, b, snapshot, key, now) {
		return nil, nil, nil, time.Time{}, false
	}
	return prior.Package.Formats, prior.Package.Cover, prior.Excerpts, prior.MeasuredAt, true
}
