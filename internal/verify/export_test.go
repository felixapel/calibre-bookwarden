package verify

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"golang.org/x/sync/semaphore"

	"github.com/felixapel/calibre-bookwarden/internal/inspect"
	"github.com/felixapel/calibre-bookwarden/internal/library"
)

// CheckBook exposes checkBook to the verify_test package, which can import
// verifytest without an import cycle.
func CheckBook(ctx context.Context, lib Library, libraryID string, ref library.BookRef, scratch string, lim Limits, budget *semaphore.Weighted, logger *slog.Logger, hinter Hinter) (Outcome, error) {
	return checker{lib: lib, libraryID: libraryID, scratch: scratch, lim: lim, budget: budget, logger: logger, hinter: hinter}.check(ctx, ref)
}

// CheckBookSources checks one book of library "library" with src as the
// run's sources.
func CheckBookSources(ctx context.Context, lib Library, ref library.BookRef, scratch string, src Sources, runID int64) (Outcome, error) {
	return checker{
		lib: lib, libraryID: "library", scratch: scratch, lim: DefaultLimits, budget: semaphore.NewWeighted(DefaultLimits.InFlight),
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)), sources: src, runID: runID,
	}.check(ctx, ref)
}

// SetFormatTimeout shortens the per-format deadline for one test.
func SetFormatTimeout(t testing.TB, d time.Duration) {
	old := formatTimeout
	formatTimeout = d
	t.Cleanup(func() { formatTimeout = old })
}

// SetCancelPoll shortens how often a run looks for a cancel request made
// through another instance.
func SetCancelPoll(t testing.TB, d time.Duration) {
	old := cancelPoll
	cancelPoll = d
	t.Cleanup(func() { cancelPoll = old })
}

// SetCalibreFresh changes how long an answer from Calibre counts as recent.
func SetCalibreFresh(t testing.TB, d time.Duration) {
	old := calibreFresh
	calibreFresh = d
	t.Cleanup(func() { calibreFresh = old })
}

// SetReadyProbeEvery changes how often CalibreReady may probe Calibre.
func SetReadyProbeEvery(t testing.TB, d time.Duration) {
	old := readyProbeEvery
	readyProbeEvery = d
	t.Cleanup(func() { readyProbeEvery = old })
}

// Reusable exposes reusable to the verify_test package.
func Reusable(p Prior, libraryID string, b library.Book, snapshot, key string, now time.Time) bool {
	return reusable(p, libraryID, b, snapshot, key, now)
}

// InspectKey exposes inspectKey to the verify_test package.
func InspectKey(lim Limits, hasHinter bool, tools string) string {
	return inspectKey(lim, hasHinter, tools)
}

// BuildStamp exposes the real build stamp.
func BuildStamp() string { return defaultBuildStamp() }

// SetBuildStamp makes the key's build stamp s until the returned func is
// called or the test ends.
func SetBuildStamp(t testing.TB, s string) func() {
	old := buildStamp
	buildStamp = func() string { return s }
	restore := func() { buildStamp = old }
	t.Cleanup(restore)
	return restore
}

// SetInspectFile replaces the file inspector for one test.
func SetInspectFile(t testing.TB, f func(context.Context, string, string, inspect.Limits) (inspect.Result, error)) {
	old := inspectFile
	inspectFile = f
	t.Cleanup(func() { inspectFile = old })
}
