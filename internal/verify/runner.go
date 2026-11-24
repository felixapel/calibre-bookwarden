package verify

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/semaphore"

	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/sources"
)

// How often a runner without the lock tries again, and how often a running
// run looks for a cancel request made through another instance.
const lockRetry = 30 * time.Second

// maxInterruptions is how often Calibre may become unreachable while one
// book is in hand before that book fails. A format that crashes Calibre
// (an out-of-memory kill on a huge export) would otherwise crash it again
// on every retry, forever.
const maxInterruptions = 3

// ErrRunnerStopped: the runner can't execute runs, so Start refuses to
// create runs that would never finish.
var ErrRunnerStopped = errors.New("verify: the check runner has stopped")

var cancelPoll = 2 * time.Second

// Options configure a Runner. Zero values take the defaults.
type Options struct {
	Workers    int    // books checked in parallel; default 4
	Limits     Limits // default DefaultLimits
	ScratchDir string // default os.TempDir()/bookwarden-scratch
	AppVersion string // recorded on each run; default "dev"
	Logger     *slog.Logger
	Hinter     Hinter  // reads PDF ISBN hints; nil records none
	Sources    Sources // looks ISBNs up; nil seals no lookups
	// Extraction extracts EPUB excerpts (M4, shadow); nil seals none.
	Extraction Extraction
	// ToolVersions describes the native tools OCR and text reads depend on
	// (ocr.ToolVersions); it is part of the key incremental runs reuse
	// file evidence under (ADR-039).
	ToolVersions string
	// Now is the clock; default time.Now.
	Now func() time.Time
	// Backoff is the wait before the attempt-th Calibre probe while a run
	// waits; default 5 s doubling up to 60 s.
	Backoff func(attempt int) time.Duration
	// CheckEvery is how often an idle runner probes Calibre; default 5 min.
	CheckEvery time.Duration
	// Finished, when set, hears of every run that ends, after its status is
	// stored (agent fixing reviews completed runs, ADR-029; retention prunes
	// old runs, ADR-039). Retention is not the runner's business.
	Finished func(ctx context.Context, runID int64, status RunStatus)
}

func (o Options) withDefaults() Options {
	if o.Workers == 0 {
		o.Workers = 4
	}
	if o.Limits == (Limits{}) {
		o.Limits = DefaultLimits
	}
	if o.ScratchDir == "" {
		o.ScratchDir = filepath.Join(os.TempDir(), "bookwarden-scratch")
	}
	if o.AppVersion == "" {
		o.AppVersion = "dev"
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if o.Backoff == nil {
		o.Backoff = func(attempt int) time.Duration {
			return min(5*time.Second<<min(attempt, 4), 60*time.Second)
		}
	}
	if o.CheckEvery == 0 {
		o.CheckEvery = 5 * time.Minute
	}
	return o
}

// Runner starts, executes, cancels and resumes runs. Only the process that
// holds the store's runner lock executes runs; any process can start, cancel
// and list them.
type Runner struct {
	store  RunStore
	lib    Library
	opts   Options
	budget *semaphore.Weighted
	wake   chan struct{}
	// drain is closed by Drain: the runner takes no new book, lets the
	// books in hand finish, and Serve returns. Cancelling Serve's context is
	// the hard stop after it.
	drain     chan struct{}
	drainOnce sync.Once

	mu        sync.Mutex
	calibre   CalibreStatus
	activeID  int64
	cancelRun context.CancelFunc
	stopped   error // why Serve gave up, if it did

	readyMu  sync.Mutex // serialises CalibreReady's probes
	readyAt  time.Time
	readyErr error

	// recovered holds, for each run whose crashed books this process has
	// counted, the pending books with a crash; only Serve's goroutine
	// (execute) touches it.
	recovered map[int64]map[int]bool

	// stats adds up the executing run's Stats; only Serve's goroutine sets
	// it, and the run's workers add to it.
	stats *runStats
}

// statsSaveTimeout bounds storing an attempt's stats after its context
// ended.
const statsSaveTimeout = 5 * time.Second

// NewRunner returns a runner. It panics on options that could never work: no
// workers, or a file limit above the in-flight limit.
func NewRunner(store RunStore, lib Library, opts Options) *Runner {
	opts = opts.withDefaults()
	if opts.Workers < 1 {
		panic(fmt.Sprintf("verify: Workers must be at least 1, got %d", opts.Workers))
	}
	if opts.Limits.FileSize < 1 || opts.Limits.FileSize > opts.Limits.InFlight {
		panic(fmt.Sprintf("verify: need 0 < FileSize <= InFlight, got %d and %d", opts.Limits.FileSize, opts.Limits.InFlight))
	}
	return &Runner{
		recovered: map[int64]map[int]bool{},
		store:     store,
		lib:       lib,
		opts:      opts,
		budget:    semaphore.NewWeighted(opts.Limits.InFlight),
		wake:      make(chan struct{}, 1),
		drain:     make(chan struct{}),
		calibre:   CalibreStatus{State: "unknown"},
	}
}

// errDrained ends a wait for Calibre when Drain is called.
var errDrained = errors.New("verify: the runner is draining")

// Drain asks the runner to stop gracefully (ADR-022): it takes no new book,
// the books in hand finish, and Serve returns with the run left active for
// the next start. Start refuses new runs from then on. It is safe to call
// more than once.
func (r *Runner) Drain() {
	r.drainOnce.Do(func() { close(r.drain) })
}

func (r *Runner) draining() bool {
	select {
	case <-r.drain:
		return true
	default:
		return false
	}
}

// pause is sleep that also ends on Drain.
func (r *Runner) pause(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-r.drain:
		return false
	case <-t.C:
		return true
	}
}

// ActiveRunID is the run being executed now, or 0.
func (r *Runner) ActiveRunID() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.activeID
}

// Calibre returns the result of the last Calibre probe.
func (r *Runner) Calibre() CalibreStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calibre
}

// calibreFresh is how long a successful probe counts as Calibre answering
// (/readyz).
var calibreFresh = 5 * time.Minute

// readyProbeEvery is how often /readyz may probe Calibre, whatever the
// result: a monitor or a retry storm must not turn into a probe per request.
var readyProbeEvery = 15 * time.Second

// CalibreReady returns nil when Calibre answered within the last five
// minutes, or answers now; else the probe's error. A run in progress doesn't
// refresh the last answer, so a stale one is probed rather than failed. Probes
// from here happen at most every 15 seconds, failures included, and
// concurrent callers share one.
func (r *Runner) CalibreReady(ctx context.Context) error {
	if st := r.Calibre(); st.State == "ok" && time.Since(st.CheckedAt) <= calibreFresh {
		return nil
	}
	r.readyMu.Lock()
	defer r.readyMu.Unlock()
	// Another caller may have probed, or the runner's own probe may have
	// succeeded, while this one waited.
	if st := r.Calibre(); st.State == "ok" && time.Since(st.CheckedAt) <= calibreFresh {
		return nil
	}
	if !r.readyAt.IsZero() && time.Since(r.readyAt) < readyProbeEvery {
		return r.readyErr
	}
	_, err := r.checkCalibre(ctx)
	if ctx.Err() != nil {
		return err // cut short by the caller: says nothing about Calibre
	}
	r.readyAt, r.readyErr = time.Now(), err
	return err
}

// checkCalibre probes Calibre and records the result. It returns the probe's
// error unchanged.
func (r *Runner) checkCalibre(ctx context.Context) (CalibreStatus, error) {
	return r.probeCalibre(ctx, "")
}

func (r *Runner) probeCalibre(ctx context.Context, libraryID string) (CalibreStatus, error) {
	var id string
	var err error
	if libraryID == "" {
		id, err = r.lib.Check(ctx)
	} else {
		id, err = r.lib.CheckInLibrary(ctx, libraryID)
	}
	if err != nil && ctx.Err() != nil {
		return r.Calibre(), err
	}
	if err != nil {
		return r.recordCalibre(err), err
	}
	st := CalibreStatus{State: "ok", LibraryID: id, CheckedAt: time.Now().UTC()}
	r.mu.Lock()
	r.calibre = st
	r.mu.Unlock()
	return st, nil
}

// recordCalibre records a failed probe, or a failure seen mid-run, as the
// current Calibre status.
func (r *Runner) recordCalibre(err error) CalibreStatus {
	st := CalibreStatus{State: calibreState(err), Message: err.Error(), CheckedAt: time.Now().UTC()}
	r.mu.Lock()
	r.calibre = st
	r.mu.Unlock()
	return st
}

func calibreState(err error) string {
	switch {
	case errors.Is(err, library.ErrUnreachable):
		return "unreachable"
	case errors.Is(err, library.ErrIncompatible):
		return "incompatible"
	case errors.Is(err, library.ErrAuth):
		return "auth_failed"
	case errors.Is(err, library.ErrForbidden):
		return "forbidden"
	}
	return "error"
}

// Start freezes the book list for scope and creates a run, which Serve then
// executes. It probes Calibre first and refuses with ErrCalibreNotReady when
// the probe fails, and with ErrRunActive while another run is active.
func (r *Runner) Start(ctx context.Context, scope Scope) (Run, error) {
	guarded := scope.ExpectedLibraryID != "" || scope.ExpectedUUID != ""
	if scope.BookID < 0 || (guarded && (scope.BookID <= 0 || scope.ExpectedLibraryID == "" || scope.ExpectedUUID == "")) {
		return Run{}, ErrInvalidScope
	}
	r.mu.Lock()
	stopped := r.stopped
	r.mu.Unlock()
	if r.draining() {
		return Run{}, fmt.Errorf("%w: stopping", ErrRunnerStopped)
	}
	if stopped != nil {
		return Run{}, fmt.Errorf("%w: %w", ErrRunnerStopped, stopped)
	}
	st, err := r.checkCalibre(ctx)
	if err != nil {
		return Run{}, fmt.Errorf("%w: %s", ErrCalibreNotReady, st.Message)
	}
	if guarded && st.LibraryID != scope.ExpectedLibraryID {
		return Run{}, ErrBookChanged
	}
	if _, active, err := r.store.ActiveRun(ctx); err != nil {
		return Run{}, err
	} else if active {
		return Run{}, ErrRunActive
	}
	var refs []library.BookRef
	if scope.BookID != 0 {
		b, err := r.lib.BookInLibrary(ctx, st.LibraryID, scope.BookID)
		if err != nil {
			return Run{}, fmt.Errorf("verify: start: %w", err)
		}
		if guarded && (b.ID != scope.BookID || b.UUID != scope.ExpectedUUID) {
			return Run{}, ErrBookChanged
		}
		refs = []library.BookRef{{ID: b.ID, UUID: b.UUID}}
	} else if refs, err = r.lib.ListIDsInLibrary(ctx, st.LibraryID); err != nil {
		return Run{}, fmt.Errorf("verify: start: %w", err)
	}
	run, err := r.store.CreateRun(ctx, scope, st.LibraryID, r.opts.AppVersion, refs)
	if err != nil {
		return Run{}, err
	}
	r.opts.Logger.Info("run started", "run", run.ID, "books", len(refs), "scope_book", scope.BookID)
	select {
	case r.wake <- struct{}{}:
	default:
	}
	return run, nil
}

// Cancel asks an active run to stop. Books in hand stay pending, except one
// whose result is already being stored.
func (r *Runner) Cancel(ctx context.Context, runID int64) error {
	if err := r.store.RequestCancel(ctx, runID); err != nil {
		return err
	}
	r.mu.Lock()
	if r.activeID == runID && r.cancelRun != nil {
		r.cancelRun()
	}
	r.mu.Unlock()
	return nil
}

// SetPinned pins a run, so that retention keeps it, or unpins it.
func (r *Runner) SetPinned(ctx context.Context, runID int64, pinned bool) error {
	return r.store.SetPinned(ctx, runID, pinned)
}

// View returns one run with its progress.
func (r *Runner) View(ctx context.Context, runID int64) (RunView, error) {
	run, err := r.store.Run(ctx, runID)
	if err != nil {
		return RunView{}, err
	}
	p, err := r.store.Progress(ctx, runID)
	if err != nil {
		return RunView{}, err
	}
	return RunView{Run: run, Progress: p}, nil
}

// List returns up to limit runs with their progress, newest first.
func (r *Runner) List(ctx context.Context, limit int) ([]RunView, error) {
	runs, err := r.store.ListRuns(ctx, limit)
	if err != nil {
		return nil, err
	}
	views := make([]RunView, 0, len(runs))
	for _, run := range runs {
		p, err := r.store.Progress(ctx, run.ID)
		if err != nil {
			return nil, err
		}
		views = append(views, RunView{Run: run, Progress: p})
	}
	return views, nil
}

// Serve executes runs until ctx ends: it empties the scratch directory,
// takes the runner lock (waiting while another instance holds it), resumes
// an active run, then executes runs as they are started and probes Calibre
// while idle. It returns nil when ctx ends.
func (r *Runner) Serve(ctx context.Context) error {
	if err := r.Prepare(); err != nil {
		r.mu.Lock()
		r.stopped = err
		r.mu.Unlock()
		return err
	}
	if err := r.resetScratch(); err != nil {
		r.opts.Logger.Warn("emptying the scratch directory failed", "error", err)
	}
	// Calibre's status shows in the UI and never fails the container; the
	// log says why checks can't start yet.
	if st, err := r.checkCalibre(ctx); err != nil && ctx.Err() == nil {
		r.opts.Logger.Warn("calibre is not ready; checks can't run until it is", "state", st.State, "message", st.Message)
	}
	release, err := r.lock(ctx)
	if err != nil || release == nil {
		return nil // ctx ended while waiting for the lock
	}
	defer release()

	idle := time.NewTicker(r.opts.CheckEvery)
	defer idle.Stop()
	// Consecutive failures wait longer each time: reading the active run,
	// and executing the same run. Success resets the count.
	var readFailures, runFailures int
	var failedRun int64
	for {
		if r.draining() {
			return nil
		}
		run, active, err := r.store.ActiveRun(ctx)
		switch {
		case ctx.Err() != nil:
			return nil
		case err != nil:
			r.opts.Logger.Error("reading the active run failed", "error", err)
			if !r.pause(ctx, r.opts.Backoff(readFailures)) {
				return nil
			}
			readFailures++
			continue
		case active:
			readFailures = 0
			if run.ID != failedRun {
				failedRun, runFailures = run.ID, 0
			}
			if err := r.execute(ctx, run); err != nil && ctx.Err() == nil {
				r.opts.Logger.Error("run interrupted; retrying", "run", run.ID, "error", err)
				if !r.pause(ctx, r.opts.Backoff(runFailures)) {
					return nil
				}
				runFailures++
			} else {
				runFailures = 0
			}
			continue
		}
		readFailures = 0
		select {
		case <-ctx.Done():
			return nil
		case <-r.drain:
			return nil
		case <-r.wake:
		case <-idle.C:
			_, _ = r.checkCalibre(ctx)
		}
	}
}

func (r *Runner) lock(ctx context.Context) (func(), error) {
	for {
		release, ok, err := r.store.LockRunner(ctx)
		if err == nil && ok {
			return release, nil
		}
		if err != nil {
			r.opts.Logger.Error("taking the runner lock failed", "error", err)
		} else {
			r.opts.Logger.Info("another bookwarden runs checks on this database; waiting")
		}
		if !r.pause(ctx, lockRetry) {
			return nil, ctx.Err()
		}
	}
}

// Prepare creates the scratch directory and checks that files can be
// written there. serve calls it before listening, so a missing tmpfs stops
// startup instead of leaving runs that never finish; Serve calls it again.
func (r *Runner) Prepare() error {
	if err := os.MkdirAll(r.opts.ScratchDir, 0o700); err != nil {
		return fmt.Errorf("verify: scratch directory: %w", err)
	}
	probe, err := os.CreateTemp(r.opts.ScratchDir, ".probe-")
	if err != nil {
		return fmt.Errorf("verify: scratch directory isn't writable: %w", err)
	}
	_ = probe.Close()
	if err := os.Remove(probe.Name()); err != nil {
		return fmt.Errorf("verify: scratch directory: %w", err)
	}
	return nil
}

// resetScratch removes what a previous process left in the scratch
// directory.
func (r *Runner) resetScratch() error {
	entries, err := os.ReadDir(r.opts.ScratchDir)
	if err != nil {
		return fmt.Errorf("verify: scratch directory: %w", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "book-") {
			if err := os.RemoveAll(filepath.Join(r.opts.ScratchDir, e.Name())); err != nil {
				return fmt.Errorf("verify: empty the scratch directory: %w", err)
			}
		}
	}
	return nil
}

// execute works on run until it ends, or ctx ends (the run then stays active
// and resumes on the next Serve). It returns store errors.
func (r *Runner) execute(ctx context.Context, run Run) error {
	runCtx, cancel := context.WithCancel(ctx)
	var watcher sync.WaitGroup
	defer func() {
		cancel()
		watcher.Wait()
	}()
	r.mu.Lock()
	r.activeID, r.cancelRun = run.ID, cancel
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.activeID, r.cancelRun = 0, nil
		r.mu.Unlock()
	}()
	poll := cancelPoll
	watcher.Go(func() { r.watchCancel(runCtx, cancel, run.ID, poll) })
	// What this attempt spent is stored even when the run stays active, so
	// the attempt that resumes it adds to it.
	r.stats = newRunStats(run.Stats)
	defer func() {
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), statsSaveTimeout)
		defer cancel()
		if err := r.saveStats(sctx, run.ID); err != nil {
			r.opts.Logger.Warn("the run's stats could not be stored", "run", run.ID, "err", err)
		}
		r.stats = nil
	}()

	if run.CancelRequested {
		return r.finish(ctx, run.ID, RunCancelled, "")
	}
	// Books still in hand from before this process started were in hand
	// when it died: each counts a crash, and one that crashed it twice is
	// failed instead of inspected again, so a book that kills the process
	// can't keep the run from ending. Only one of the books in hand can have
	// been the culprit, so the others with a crash are checked one at a time
	// and a second death takes down only the culprit.
	crashed, ok := r.recovered[run.ID]
	if !ok {
		n, ids, err := r.store.RecoverCrashed(ctx, run.ID)
		if err != nil {
			return err
		}
		if n > 0 {
			r.opts.Logger.Warn("books that stopped the process twice were failed", "run", run.ID, "books", n)
		}
		crashed = map[int]bool{}
		for _, id := range ids {
			crashed[id] = true
		}
		r.recovered[run.ID] = crashed
	}
	if r.opts.Sources != nil {
		r.opts.Sources.BeginRun(runCtx, run.ID, refreshBudget(run.Scope))
	}
	if r.opts.Extraction != nil {
		r.opts.Extraction.BeginRun(run.ID)
	}
	status := run.Status
	// interruptions counts, per book, how often Calibre went away while the
	// book was in hand. Books with a count are retried one at a time, so an
	// innocent book that was merely in hand alongside the culprit isn't
	// blamed again.
	interruptions := map[int]int{}
	for {
		// A stop leaves the run active; the next start resumes it.
		if r.draining() {
			return nil
		}
		if status == RunWaiting {
			if err := r.waitForCalibre(runCtx, run.LibraryID); err != nil {
				if errors.Is(err, errDrained) {
					return nil
				}
				return r.interrupted(ctx, runCtx, run.ID, err)
			}
			if err := r.store.SetStatus(ctx, run.ID, RunRunning, ""); err != nil {
				return err
			}
			status = RunRunning
			r.opts.Logger.Info("calibre is back; run continues", "run", run.ID)
		}
		pending, err := r.store.PendingBooks(runCtx, run.ID)
		if err != nil {
			return r.interrupted(ctx, runCtx, run.ID, err)
		}
		if len(pending) == 0 {
			return r.finish(ctx, run.ID, RunCompleted, "")
		}
		batch, workers := pending, r.opts.Workers
		if suspects := slices.DeleteFunc(slices.Clone(pending), func(ref library.BookRef) bool { return interruptions[ref.ID] == 0 && !crashed[ref.ID] }); len(suspects) > 0 {
			batch, workers = suspects, 1
		}
		inHand, err := r.work(runCtx, ctx, run, batch, workers)
		r.release(run.ID, inHand)
		switch {
		case err == nil:
			continue
		case errors.Is(err, library.ErrUnreachable) && runCtx.Err() == nil:
			for _, ref := range inHand {
				interruptions[ref.ID]++
				if interruptions[ref.ID] < maxInterruptions {
					continue
				}
				r.opts.Logger.Warn("calibre went away every time this book was read; marking it failed", "run", run.ID, "book", ref.ID)
				if err := r.finishBook(ctx, run.ID, ref, Outcome{State: BookFailed, Reason: "calibre_unreachable"}); err != nil {
					return err
				}
			}
			r.recordCalibre(err)
			r.opts.Logger.Warn("calibre is unreachable; run waits", "run", run.ID)
			if err := r.store.SetStatus(ctx, run.ID, RunWaiting, "Calibre is unreachable"); err != nil {
				return err
			}
			status = RunWaiting
		default:
			return r.interrupted(ctx, runCtx, run.ID, err)
		}
	}
}

// interrupted ends execute for err: a cancel request cancels the run, the
// end of ctx leaves it active, Calibre refusing the account or being
// incompatible fails it, and anything else (the store) is returned.
func (r *Runner) interrupted(ctx, runCtx context.Context, runID int64, err error) error {
	switch {
	case ctx.Err() != nil:
		return nil
	case runCtx.Err() != nil:
		return r.finish(ctx, runID, RunCancelled, "")
	case errors.Is(err, library.ErrAuth), errors.Is(err, library.ErrForbidden), errors.Is(err, library.ErrIncompatible):
		return r.finish(ctx, runID, RunFailed, err.Error())
	}
	return err
}

func (r *Runner) finish(ctx context.Context, runID int64, status RunStatus, message string) error {
	// Stored first, so the ledger row written with the status copies them.
	if err := r.saveStats(ctx, runID); err != nil {
		return err
	}
	if err := r.store.SetStatus(ctx, runID, status, message); err != nil {
		return err
	}
	r.opts.Logger.Info("run ended", "run", runID, "status", string(status))
	r.logSummary(ctx, runID)
	if r.opts.Finished != nil {
		r.opts.Finished(ctx, runID, status)
	}
	return nil
}

// saveStats adds what the sources and the extraction counted to the run's
// stats and stores them.
func (r *Runner) saveStats(ctx context.Context, runID int64) error {
	r.stats.take(r.opts.Sources, r.opts.Extraction)
	return r.store.SaveRunStats(ctx, runID, r.stats.snapshot())
}

// logSummary logs one line with where an ended run's time went, as stored.
// No book data.
func (r *Runner) logSummary(ctx context.Context, runID int64) {
	run, err := r.store.Run(ctx, runID)
	if err != nil {
		r.opts.Logger.Warn("the run summary could not be read", "run", runID, "err", err)
		return
	}
	p, err := r.store.Progress(ctx, runID)
	if err != nil {
		r.opts.Logger.Warn("the run summary could not be read", "run", runID, "err", err)
		return
	}
	st := run.Stats
	args := []any{"run", runID, "kind", run.Scope.kind(), "status", string(run.Status),
		"duration_seconds", run.FinishedAt.Sub(run.StartedAt).Seconds(), "books", p.Total, "reinspected", p.Checked - p.Reused}
	for _, stage := range Stages {
		args = append(args, stage+"_seconds", st.StageSeconds[stage])
	}
	for _, src := range slices.Sorted(maps.Keys(st.Lookups)) {
		for _, outcome := range slices.Sorted(maps.Keys(st.Lookups[src])) {
			args = append(args, "lookups_"+src+"_"+outcome, st.Lookups[src][outcome])
		}
	}
	args = append(args, "extractor_calls", st.Extractor.Calls, "extractor_cached", st.Extractor.Cached, "extractor_failed", st.Extractor.Failed)
	r.opts.Logger.Info("run summary", args...)
}

// now is the runner's clock.
func (r *Runner) now() time.Time {
	if r.opts.Now != nil {
		return r.opts.Now()
	}
	return time.Now()
}

// watchCancel cancels the run when another instance flags it.
func (r *Runner) watchCancel(runCtx context.Context, cancel context.CancelFunc, runID int64, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-runCtx.Done():
			return
		case <-t.C:
			if run, err := r.store.Run(runCtx, runID); err == nil && run.CancelRequested {
				cancel()
				return
			}
		}
	}
}

// waitForCalibre probes Calibre with backoff until it answers. A Calibre
// that refuses the account or is incompatible ends the wait with an error.
func (r *Runner) waitForCalibre(ctx context.Context, libraryID string) error {
	for attempt := 0; ; attempt++ {
		if !r.pause(ctx, r.opts.Backoff(attempt)) {
			if ctx.Err() == nil {
				return errDrained
			}
			return ctx.Err()
		}
		_, err := r.probeCalibre(ctx, libraryID)
		switch {
		case err == nil:
			return nil
		case ctx.Err() != nil:
			return ctx.Err()
		case errors.Is(err, library.ErrAuth), errors.Is(err, library.ErrForbidden), errors.Is(err, library.ErrIncompatible):
			return err
		}
	}
}

// release clears the in-hand mark of books an orderly interruption put back,
// so the next start doesn't count them as crashes. It runs even during a
// stop; a store that is away leaves the marks, and a restart then counts one
// crash too many, which only matters at two.
func (r *Runner) release(runID int64, refs []library.BookRef) {
	if len(refs) == 0 {
		return
	}
	ids := make([]int, len(refs))
	for i, ref := range refs {
		ids[i] = ref.ID
	}
	ctx, cancel := context.WithTimeout(context.Background(), finishTimeout)
	defer cancel()
	if err := r.store.ReleaseBooks(ctx, runID, ids); err != nil {
		r.opts.Logger.Warn("books in hand could not be put back", "run", runID, "books", len(ids), "error", err)
	}
}

// finishTimeout bounds storing one book's result.
const finishTimeout = 30 * time.Second

// finishBook stores a book's result under ctx, which work passes as Serve's
// context: a result is kept when a sibling stops the batch or a cancel request
// lands, and only the hard stop (past the stop grace) abandons the write. An outage
// (ErrStoreUnavailable) is returned so the run resumes later. Any other error
// is the book's own: the book fails with store_error, so one result that can't
// be stored never makes the run retry it forever. Only if that write fails too
// is the error returned.
func (r *Runner) finishBook(ctx context.Context, runID int64, ref library.BookRef, out Outcome) error {
	store := func(out Outcome) error {
		fctx, cancel := context.WithTimeout(ctx, finishTimeout)
		defer cancel()
		if err := r.store.FinishBook(fctx, runID, ref, out); err != nil && !errors.Is(err, ErrBookFinished) {
			return err
		}
		return nil
	}
	err := store(out)
	if err == nil || errors.Is(err, ErrStoreUnavailable) {
		return err
	}
	// Only a class is logged: a SQLSTATE tells a refused row from evidence the
	// store would not accept, and neither carries book text.
	class := "refused"
	if e, ok := errors.AsType[interface {
		error
		SQLState() string
	}](err); ok {
		class = "sqlstate " + e.SQLState()
	}
	r.opts.Logger.Error("a book's result could not be stored; the book fails", "run", runID, "book", ref.ID, "class", class)
	return store(Outcome{State: BookFailed, Reason: reasonStoreError})
}

// work checks pending books with up to workers at a time. It returns the
// first run-level error, after every worker has stopped, with the books
// that were in hand; they stay pending.
func (r *Runner) work(ctx, persist context.Context, run Run, pending []library.BookRef, workers int) ([]library.BookRef, error) {
	wctx, stop := context.WithCancelCause(ctx)
	defer stop(nil)
	refs := make(chan library.BookRef)
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		inHand []library.BookRef
	)
	c := checker{
		lib: r.lib, libraryID: run.LibraryID, scratch: r.opts.ScratchDir, lim: r.opts.Limits, budget: r.budget,
		logger: r.opts.Logger, hinter: r.opts.Hinter, sources: r.opts.Sources, extraction: r.opts.Extraction, runID: run.ID,
		incremental: run.Scope.Incremental, priors: r.store, tools: r.opts.ToolVersions, now: r.opts.Now, stats: r.stats,
	}
	for range min(workers, len(pending)) {
		wg.Go(func() {
			for ref := range refs {
				if err := r.store.TakeBook(wctx, run.ID, ref.ID); err != nil {
					stop(err)
					return
				}
				out, err := c.check(wctx, ref)
				if err != nil {
					mu.Lock()
					inHand = append(inHand, ref)
					mu.Unlock()
					stop(err)
					return
				}
				if out.State != BookChecked {
					r.opts.Logger.Info("book not checked", "run", run.ID, "book", ref.ID, "state", string(out.State), "reason", out.Reason)
				}
				stored := r.now()
				err = r.finishBook(persist, run.ID, ref, out)
				r.stats.stage(StageStore, r.now().Sub(stored))
				if err != nil {
					mu.Lock()
					inHand = append(inHand, ref)
					mu.Unlock()
					stop(err)
					return
				}
			}
		})
	}
feed:
	for _, ref := range pending {
		if r.draining() {
			break feed
		}
		select {
		case refs <- ref:
		case <-wctx.Done():
			break feed
		case <-r.drain:
			break feed
		}
	}
	close(refs)
	wg.Wait()
	return inHand, context.Cause(wctx)
}

// refreshBudget is how many expired cache answers per source a run may ask
// again (ADR-044): a bounded number for an incremental run of the whole
// library, every one for a full run and a one-book recheck.
func refreshBudget(scope Scope) int {
	if scope.Incremental && scope.BookID == 0 {
		return sources.RefreshIncremental
	}
	return sources.RefreshAll
}
