package fix

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/decide"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/store"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

var (
	ErrBusy      = errors.New("fix: another write is running")
	ErrWritesOff = errors.New("fix: changes to Calibre are switched off")
	ErrPaused    = errors.New("fix: writes are paused until the open incident is acknowledged")
	// ErrRefused: Calibre refused a write (forbidden or bad credentials) and
	// the book is unchanged, so no incident opened.
	ErrRefused     = errors.New("fix: Calibre refused the write")
	ErrNoProposal  = errors.New("fix: no such proposal in the book's newest evidence")
	ErrNotUndoable = errors.New("fix: the operation can't be undone")
	ErrChanged     = errors.New("fix: the book changed since the operation")
	ErrBlocked     = errors.New("fix: Calibre can't store these values exactly")
	// ErrTextUnverified refuses a fix that rests on the book's printed text
	// when the sealed extraction no longer re-verifies (ADR-036 section 5).
	ErrTextUnverified = errors.New("fix: the book's printed text does not re-verify")
	// ErrTextOldVerifier is ErrTextUnverified for an extraction sealed under
	// another verifier or prompt version: nothing was altered, but this build
	// can't re-derive it, so the book needs a new check.
	ErrTextOldVerifier = fmt.Errorf("%w: the printed text was read by an older verifier; recheck the book", ErrTextUnverified)
)

// TextCheck re-verifies a book's sealed extractions from the stored excerpt
// and raw answers, the check verify-seals runs. It returns an error that
// matches ErrTextUnverified when they differ or can't be checked, and any
// other error when it couldn't run.
type TextCheck func(ctx context.Context, runID int64, bookID int) error

// UnwritableError is Approve's refusal of a field Calibre can't store
// exactly; it matches ErrBlocked.
type UnwritableError struct{ Field, Reason string }

func (e *UnwritableError) Error() string {
	return fmt.Sprintf("fix: %s (%s): %v", e.Field, e.Reason, ErrBlocked)
}

func (e *UnwritableError) Unwrap() error { return ErrBlocked }

// Calibre is what writing needs from the content server; *library.Client
// implements it.
type Calibre interface {
	BookInLibrary(ctx context.Context, lib string, id int) (library.Book, error)
	SetFields(ctx context.Context, lib string, id int, changes []library.Change) (library.Book, error)
	FieldNames(ctx context.Context, lib, field string) ([]string, error)
}

// Summary counts the approved proposals an Apply call settled.
type Summary struct{ Applied, Stale, Blocked, Failed int }

// Service approves proposals and writes them.
type Service struct {
	fixes  *store.FixRepo
	runs   *store.RunRepo
	lib    Calibre
	logger *slog.Logger
	// Each Calibre call has its own deadline, so a hung server can't hold
	// the writer lock.
	readTimeout, writeTimeout time.Duration

	textCheck TextCheck // nil refuses every fix that rests on the text

	// writer admits one writer session or recovery at a time inside the
	// process; the advisory lock (lockWriter) keeps out a second process,
	// but a Postgres restart drops it without anyone noticing.
	writer     sync.Mutex
	lockWriter func(context.Context) (func(), bool, error)

	requests chan string   // the queued background apply, by whom
	retryNow chan struct{} // a queued "Retry now" from the incidents page
	// recoverEvery is how often Serve retries unfinished operations, such as
	// a restore a Calibre outage stranded; tests shorten it.
	recoverEvery time.Duration
	undoing      atomic.Int32
	mu           sync.Mutex
	status       ApplyStatus
}

// Undoing is how many undos are running; the stop logs it.
func (s *Service) Undoing() int { return int(s.undoing.Load()) }

// New returns the writer. A nil logger discards.
func New(fixes *store.FixRepo, runs *store.RunRepo, lib Calibre, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Service{fixes: fixes, runs: runs, lib: lib, logger: logger, readTimeout: 30 * time.Second, writeTimeout: 2 * time.Minute,
		lockWriter: fixes.LockWriter, requests: make(chan string, 1), retryNow: make(chan struct{}, 1), recoverEvery: time.Minute}
}

// SetTextCheck sets the check that re-verifies the printed text before a fix
// that rests on it is approved or applied. Without one such fixes are refused.
func (s *Service) SetTextCheck(c TextCheck) { s.textCheck = c }

// restsOnText reports whether any proposal has the text among its roots.
func restsOnText(ps []evidence.Proposal) bool {
	return slices.ContainsFunc(ps, func(p evidence.Proposal) bool { return slices.Contains(p.Roots, evidence.RootText) })
}

// verifyText re-verifies the book's extraction when a proposal rests on the
// text. Every path that approves or applies goes through it.
func (s *Service) verifyText(ctx context.Context, runID int64, bookID int, ps []evidence.Proposal) error {
	if !restsOnText(ps) {
		return nil
	}
	if s.textCheck == nil {
		return fmt.Errorf("fix: run %d book %d: no text check is set: %w", runID, bookID, ErrTextUnverified)
	}
	return s.textCheck(ctx, runID, bookID)
}

func (s *Service) readBook(ctx context.Context, lib string, id int) (library.Book, error) {
	ctx, cancel := context.WithTimeout(ctx, s.readTimeout)
	defer cancel()
	return s.lib.BookInLibrary(ctx, lib, id)
}

func (s *Service) setFields(ctx context.Context, lib string, id int, changes []library.Change) (library.Book, error) {
	ctx, cancel := context.WithTimeout(ctx, s.writeTimeout)
	defer cancel()
	return s.lib.SetFields(ctx, lib, id, changes)
}

func (s *Service) fieldNames(ctx context.Context, lib, field string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, s.readTimeout)
	defer cancel()
	return s.lib.FieldNames(ctx, lib, field)
}

// Approve approves fields of a book's proposals, copying their values from
// the verified sealed decision.
func (s *Service) Approve(ctx context.Context, runID int64, bookID int, fields []string, by string, basis store.Basis) error {
	return s.decide(ctx, runID, bookID, fields, store.ProposalApproved, by, basis)
}

// Reject rejects fields of a book's proposals.
func (s *Service) Reject(ctx context.Context, runID int64, bookID int, fields []string, by string, basis store.Basis) error {
	return s.decide(ctx, runID, bookID, fields, store.ProposalRejected, by, basis)
}

func (s *Service) decide(ctx context.Context, runID int64, bookID int, fields []string, status, by string, basis store.Basis) error {
	ps, err := s.sealed(ctx, runID, bookID, fields, status)
	if err != nil {
		return err
	}
	return s.fixes.Decide(ctx, runID, bookID, ps, status, by, basis)
}

// sealed returns the verified sealed proposals for fields of a book's newest
// evidence; approving also checks Calibre can store them.
func (s *Service) sealed(ctx context.Context, runID int64, bookID int, fields []string, status string) ([]evidence.Proposal, error) {
	if len(fields) == 0 {
		return nil, fmt.Errorf("fix: no fields: %w", ErrNoProposal)
	}
	p, dec, err := s.open(ctx, runID, bookID)
	if errors.Is(err, verify.ErrNoSuchRun) {
		return nil, fmt.Errorf("fix: run %d book %d: %w", runID, bookID, ErrNoProposal)
	}
	if err != nil {
		return nil, err
	}
	newest, err := s.fixes.NewestEvidenceRun(ctx, p.LibraryID, p.BookID, p.UUID)
	if err != nil {
		return nil, err
	}
	if newest != runID {
		return nil, fmt.Errorf("fix: run %d book %d is superseded by run %d: %w", runID, bookID, newest, ErrNoProposal)
	}
	var ps []evidence.Proposal
	for _, f := range slices.Compact(slices.Sorted(slices.Values(fields))) {
		i := slices.IndexFunc(dec.Proposals, func(p evidence.Proposal) bool { return p.Field == f })
		if i < 0 {
			return nil, fmt.Errorf("fix: run %d book %d %s: %w", runID, bookID, f, ErrNoProposal)
		}
		if status == store.ProposalApproved {
			if reason := unwritable(dec.Proposals[i], p.Calibre); reason != "" {
				return nil, fmt.Errorf("fix: run %d book %d: %w", runID, bookID, &UnwritableError{Field: f, Reason: reason})
			}
		}
		ps = append(ps, dec.Proposals[i])
	}
	if status == store.ProposalApproved {
		if err := s.verifyText(ctx, runID, bookID, ps); err != nil {
			return nil, err
		}
	}
	return ps, nil
}

// open loads a book's evidence and its verified decision.
func (s *Service) open(ctx context.Context, runID int64, bookID int) (evidence.Package, decide.Decision, error) {
	raw, seal, err := s.runs.Evidence(ctx, runID, bookID)
	if err != nil {
		return evidence.Package{}, decide.Decision{}, err
	}
	return decide.Open(ctx, raw, seal)
}

// session runs fn under the writer lock, after finishing unfinished
// operations, with writes on and not paused. Recovery isn't cancelled with
// ctx; fn decides where ctx may stop it.
func (s *Service) session(ctx context.Context, fn func(context.Context) error) error {
	release, err := s.lock(ctx)
	if err != nil {
		return err
	}
	defer release()
	if err := s.recover(context.WithoutCancel(ctx)); err != nil {
		return err
	}
	if err := s.ready(ctx); err != nil {
		return err
	}
	return fn(ctx)
}

// lock takes the in-process writer mutex, then the advisory lock; ErrBusy
// when either is held.
func (s *Service) lock(ctx context.Context) (func(), error) {
	if !s.writer.TryLock() {
		return nil, ErrBusy
	}
	release, ok, err := s.lockWriter(ctx)
	if err != nil || !ok {
		s.writer.Unlock()
		if err != nil {
			return nil, err
		}
		return nil, ErrBusy
	}
	return func() {
		defer s.writer.Unlock()
		release()
	}, nil
}

// ready returns ErrWritesOff or ErrPaused when writes can't start.
func (s *Service) ready(ctx context.Context) error {
	on, err := s.fixes.WritesEnabled(ctx)
	if err != nil {
		return err
	}
	if !on {
		return ErrWritesOff
	}
	paused, err := s.fixes.Paused(ctx)
	if err != nil {
		return err
	}
	if paused {
		return ErrPaused
	}
	return nil
}

type bookWork struct {
	runID  int64
	bookID int
	rows   []store.ProposalRow
}

// Apply writes every approved proposal, one book at a time. It stops at the
// first operation that doesn't complete, with ErrPaused, or with ErrRefused
// when Calibre refused the write and the book is unchanged, and leaves
// proposals approved when Calibre can't be read.
func (s *Service) Apply(ctx context.Context, by string) (Summary, error) {
	var sum Summary
	err := s.session(ctx, func(ctx context.Context) error {
		rows, err := s.fixes.Approved(ctx)
		if err != nil {
			return err
		}
		var work []bookWork
		for _, r := range rows {
			if n := len(work); n > 0 && work[n-1].runID == r.RunID && work[n-1].bookID == r.BookID {
				work[n-1].rows = append(work[n-1].rows, r)
				continue
			}
			work = append(work, bookWork{runID: r.RunID, bookID: r.BookID, rows: []store.ProposalRow{r}})
		}
		for i, w := range work {
			if err := ctx.Err(); err != nil {
				return err
			}
			if i > 0 {
				if err := s.ready(ctx); err != nil {
					return err
				}
			}
			// A book once started finishes, bounded by the call deadlines.
			err := s.applyBook(context.WithoutCancel(ctx), w, by, &sum)
			if errors.Is(err, store.ErrClosed) {
				// A proposal was decided again since the apply read it,
				// before anything was sent; the book waits for the next
				// apply.
				s.logger.Info("fix: decision changed; book skipped", "run", w.runID, "book", w.bookID)
				continue
			}
			if err != nil {
				return err
			}
		}
		return nil
	})
	return sum, err
}

func ids(rows []store.ProposalRow) []int64 {
	out := make([]int64, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.ID)
	}
	return out
}

// sameRecord reports whether Calibre's record now holds what was recorded.
// A record from before bookwarden read the authors table (ADR-028) is
// compared without it, and one without a series without the live series.
func sameRecord(now, recorded evidence.Record) bool {
	if recorded.AuthorSortMap == nil {
		now.AuthorSortMap = nil
	}
	// A record from before the series was read (schema 4) has none.
	if recorded.Series == "" {
		now.Series, now.SeriesIndex = "", ""
	}
	a, b := now, recorded
	x, err1 := evidence.Canonical(a)
	y, err2 := evidence.Canonical(b)
	return err1 == nil && err2 == nil && bytes.Equal(x, y)
}

// reloaded returns cur with the recorded last_modified when Calibre reports
// that instant in whole seconds, for comparing only. Calibre 9.16 keeps a
// write's microseconds in its cache, and its loader (calibre.db.tables
// c_parse) truncates them when it reloads the library, so this is the same
// instant, not a change. Anything else is compared as read.
func reloaded(cur library.Book, recorded string) library.Book {
	rec, err := time.Parse(time.RFC3339Nano, recorded)
	if err != nil || rec.Nanosecond() == 0 || cur.LastModified.Nanosecond() != 0 {
		return cur
	}
	if rec.Truncate(time.Second).Equal(cur.LastModified) {
		cur.LastModified = rec.UTC()
	}
	return cur
}

func sameProposal(a, b evidence.Proposal) bool {
	x, err1 := evidence.Canonical(a)
	y, err2 := evidence.Canonical(b)
	return err1 == nil && err2 == nil && bytes.Equal(x, y)
}

// applyBook runs the stale check, the writability checks and one operation
// for a book's approved proposals (ADR-021).
func (s *Service) applyBook(ctx context.Context, w bookWork, by string, sum *Summary) error {
	p, dec, err := s.open(ctx, w.runID, w.bookID)
	if err != nil {
		return err
	}
	stale := func(reason string) error {
		s.logger.Info("fix: stale", "run", w.runID, "book", w.bookID, "reason", reason)
		if err := s.fixes.Close(ctx, ids(w.rows), store.ProposalStale, reason); err != nil {
			return err
		}
		sum.Stale += len(w.rows)
		return nil
	}
	for _, r := range w.rows {
		i := slices.IndexFunc(dec.Proposals, func(p evidence.Proposal) bool { return p.Field == r.Proposal.Field })
		if i < 0 || !sameProposal(dec.Proposals[i], r.Proposal) {
			return stale("proposal_changed")
		}
	}
	newest, err := s.fixes.NewestEvidenceRun(ctx, p.LibraryID, p.BookID, p.UUID)
	if err != nil {
		return err
	}
	if newest != w.runID {
		return stale("superseded")
	}
	proposals := make([]evidence.Proposal, 0, len(w.rows))
	for _, r := range w.rows {
		proposals = append(proposals, r.Proposal)
	}
	if err := s.verifyText(ctx, w.runID, w.bookID, proposals); err != nil {
		if !errors.Is(err, ErrTextUnverified) {
			return err
		}
		s.logger.Warn("fix: text does not re-verify", "run", w.runID, "book", w.bookID, "error", err)
		// Only the fields that rest on the text are blocked; the book's
		// other approved fields are independent of it and still apply.
		code := "text_unverified"
		if errors.Is(err, ErrTextOldVerifier) {
			code = "text_old_verifier"
		}
		var blocked, rest []store.ProposalRow
		for _, r := range w.rows {
			if restsOnText([]evidence.Proposal{r.Proposal}) {
				blocked = append(blocked, r)
			} else {
				rest = append(rest, r)
			}
		}
		if err := s.fixes.Close(ctx, ids(blocked), store.ProposalBlocked, code); err != nil {
			return err
		}
		sum.Blocked += len(blocked)
		if len(rest) == 0 {
			return nil
		}
		w.rows = rest
	}
	cur, err := s.readBook(ctx, p.LibraryID, p.BookID)
	if errors.Is(err, library.ErrBookNotFound) {
		return stale("book_gone")
	}
	if err != nil {
		return err
	}
	before := evidence.RecordFrom(cur)
	seen := reloaded(cur, p.Calibre.LastModified)
	snap, err := evidence.Snapshot(seen)
	if err != nil {
		return err
	}
	if cur.UUID != p.UUID || snap != p.Snapshot || !sameRecord(evidence.RecordFrom(seen), p.Calibre) {
		return stale("book_changed")
	}

	var changes []library.Change
	var take []int64
	for _, r := range w.rows {
		c, err := proposalChange(r.Proposal, before)
		if err != nil {
			return err
		}
		reason, err := s.blocked(ctx, p.LibraryID, c, before)
		if err != nil {
			return err
		}
		if reason != "" {
			s.logger.Info("fix: blocked", "run", w.runID, "book", w.bookID, "field", r.Proposal.Field, "reason", reason)
			if err := s.fixes.Close(ctx, []int64{r.ID}, store.ProposalBlocked, reason); err != nil {
				return err
			}
			sum.Blocked++
			continue
		}
		changes = append(changes, c)
		take = append(take, r.ID)
	}
	if len(changes) == 0 {
		return nil
	}
	runID := w.runID
	op := store.Operation{
		Kind: store.OpApply, RunID: &runID, LibraryID: p.LibraryID, BookID: p.BookID, UUID: p.UUID,
		Changes: changes, Before: before, RequestedBy: by,
	}
	if op.ID, err = s.fixes.StartOperation(ctx, op, take); err != nil {
		return err
	}
	completed, err := s.execute(ctx, op)
	if errors.Is(err, ErrRefused) {
		sum.Failed += len(take)
	}
	if err != nil {
		return err
	}
	if !completed {
		sum.Failed += len(take)
		return fmt.Errorf("fix: operation %d on book %d didn't complete: %w", op.ID, op.BookID, ErrPaused)
	}
	sum.Applied += len(take)
	return nil
}

// blocked names why c can't be written (ADR-021), or "" when it can.
func (s *Service) blocked(ctx context.Context, lib string, c library.Change, before evidence.Record) (string, error) {
	if reason := unstorable(c, before); reason != "" {
		return reason, nil
	}
	collides, err := s.renames(ctx, lib, []library.Change{c})
	if err != nil {
		return "", err
	}
	if collides {
		return "renames_shared_name", nil
	}
	return "", nil
}

// renames reports whether writing changes would rename an author or
// publisher that other books share.
func (s *Service) renames(ctx context.Context, lib string, changes []library.Change) (bool, error) {
	for _, c := range changes {
		var names []string
		switch c.Field {
		case fAuthors:
			names, _ = decode[[]string](c.Value)
		case fPublisher:
			if n, _ := decode[string](c.Value); n != "" {
				names = []string{n}
			}
		}
		if len(names) == 0 {
			continue
		}
		existing, err := s.fieldNames(ctx, lib, c.Field)
		if err != nil {
			return false, err
		}
		if caseCollision(names, existing) {
			return true, nil
		}
	}
	return false, nil
}

// errorCode names a write error without its text, which may carry book data.
func errorCode(err error) string {
	switch {
	case errors.Is(err, library.ErrForbidden):
		return "forbidden"
	case errors.Is(err, library.ErrAuth):
		return "auth"
	case errors.Is(err, library.ErrUnreachable):
		return "unreachable"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, library.ErrBookNotFound):
		return "book_gone"
	}
	return "error"
}

// execute writes a recorded operation and reads it back. It reports whether
// the operation completed; otherwise it went through the mismatch path.
// ErrRefused means Calibre refused the write and the book is unchanged: the
// operation failed with no incident. Any other error means the ledger or
// Calibre failed mid-way, and the next writer session finishes the
// operation.
func (s *Service) execute(ctx context.Context, op store.Operation) (bool, error) {
	if err := s.fixes.Advance(ctx, op.ID, store.OpWriting, nil, ""); err != nil {
		return false, err
	}
	answer, err := s.setFields(ctx, op.LibraryID, op.BookID, op.Changes)
	if err != nil {
		if (errors.Is(err, library.ErrForbidden) || errors.Is(err, library.ErrAuth)) && s.unchanged(ctx, op) {
			// A read-only account: nothing was written, so there is nothing
			// to restore and no incident; the apply stops with a message.
			s.logger.Warn("fix: Calibre refused the write; the book is unchanged", "operation", op.ID, "book", op.BookID, "error", errorCode(err))
			if err := s.fixes.Advance(ctx, op.ID, store.OpFailed, nil, "calibre_refused_write"); err != nil {
				return false, err
			}
			return false, ErrRefused
		}
		return false, s.mismatch(ctx, op, "write_error: "+errorCode(err))
	}
	if err := s.fixes.Advance(ctx, op.ID, store.OpVerifying, nil, ""); err != nil {
		return false, err
	}
	fresh, err := s.readBook(ctx, op.LibraryID, op.BookID)
	if err != nil {
		return false, s.mismatch(ctx, op, "readback_mismatch: read "+errorCode(err), answer)
	}
	var bad []string
	for _, c := range op.Changes {
		if !same(c, answer) || !same(c, fresh) || fresh.UUID != op.UUID {
			bad = append(bad, c.Field)
		}
	}
	if len(bad) > 0 {
		return false, s.mismatch(ctx, op, "readback_mismatch: "+strings.Join(bad, ", "), answer, fresh)
	}
	after := evidence.RecordFrom(fresh)
	if err := s.fixes.Advance(ctx, op.ID, store.OpCompleted, &after, ""); err != nil {
		return false, err
	}
	s.logger.Info("fix: completed", "operation", op.ID, "kind", op.Kind, "book", op.BookID)
	return true, nil
}

// unchanged reports whether a fresh read shows the operation's book with
// every field it was to change still at its before value. A failed read is
// not proof of anything.
func (s *Service) unchanged(ctx context.Context, op store.Operation) bool {
	fresh, err := s.readBook(ctx, op.LibraryID, op.BookID)
	if err != nil || fresh.UUID != op.UUID {
		return false
	}
	for _, c := range op.Changes {
		if v, ok := beforeChange(c.Field, op.Before); !ok || !same(v, fresh) {
			return false
		}
	}
	return true
}

// mismatch opens the incident and restores the before values, over what the
// operation wrote or Calibre answered with (observed) and nothing else.
func (s *Service) mismatch(ctx context.Context, op store.Operation, detail string, observed ...library.Book) error {
	s.logger.Warn("fix: mismatch", "operation", op.ID, "book", op.BookID, "detail", detail)
	if err := s.fixes.Advance(ctx, op.ID, store.OpRestoring, nil, detail); err != nil {
		return err
	}
	return s.restore(ctx, op, false, observed)
}

// holdsKnown reports whether every field of a restore group holds a value
// this operation knows of: the before value, one it wrote, or one Calibre
// answered with. The one exception: a sort or author sort the operation
// didn't write itself, that no observed book carries, is accepted while its
// title or authors hold a known value other than the before one, since
// Calibre recomputed it from them. If an observed book carries the field,
// the value must equal a known one. Anything else was edited since, and a
// restore after a mismatch leaves it alone.
func holdsKnown(group, wrote []library.Change, observed []library.Book, cur library.Book) bool {
	holds := func(c library.Change) bool {
		if same(c, cur) {
			return true
		}
		for _, w := range wrote {
			if w.Field == c.Field && same(w, cur) {
				return true
			}
		}
		for _, o := range observed {
			if v, ok := beforeChange(c.Field, evidence.RecordFrom(o)); ok && same(v, cur) {
				return true
			}
		}
		return false
	}
	for i, c := range group {
		if holds(c) {
			continue
		}
		derived := i > 0 && !slices.ContainsFunc(wrote, func(w library.Change) bool { return w.Field == c.Field }) &&
			!slices.ContainsFunc(observed, func(o library.Book) bool {
				_, ok := beforeChange(c.Field, evidence.RecordFrom(o))
				return ok
			})
		if !derived || same(group[0], cur) || !holds(group[0]) {
			return false
		}
	}
	return true
}

// recordRestoreWrite records, before a restore writes, the book as found and
// the fields it writes. A retry that finds the book as the last attempt did
// and writes the same fields adds nothing: the newest event already says
// so, and a Calibre that keeps failing must not grow the append-only ledger
// by one event a minute.
func (s *Service) recordRestoreWrite(ctx context.Context, opID int64, found *evidence.Record, detail string) error {
	events, err := s.fixes.Events(ctx, opID)
	if err != nil {
		return err
	}
	if n := len(events); n > 0 {
		last := events[n-1]
		if last.State == store.OpRestoring && last.Detail == detail && last.After != nil && found != nil && reflect.DeepEqual(*last.After, *found) {
			return nil
		}
	}
	return s.fixes.Advance(ctx, opID, store.OpRestoring, found, detail)
}

// pairKey groups the fields a restore writes together.
func pairKey(field string) string {
	switch field {
	case fSort:
		return fTitle
	case fAuthorSort:
		return fAuthors
	}
	return field
}

// transient reports whether a Calibre error may clear by itself.
func transient(err error) bool {
	switch errorCode(err) {
	case "unreachable", "timeout", "cancelled":
		return true
	}
	return false
}

// restore writes the before values of an operation in state restoring and
// reads them back: restored when all are back, failed otherwise. With
// onlyOurs, used after a crash, a pair is written back only while it still
// holds the value the operation wrote, so an edit made since stays; a sort
// changed along with a title or author it wrote goes back with it, since
// Calibre recomputes the sort. After a mismatch a pair is written back only
// while it holds a value the operation knows (holdsKnown); a third value is
// left and named. Before writing, the book as found is recorded
// in a restoring event. A transient Calibre error leaves the operation
// restoring for the next writer session; any other ends it failed, so its
// incident can be acknowledged.
func (s *Service) restore(ctx context.Context, op store.Operation, onlyOurs bool, observed []library.Book) error {
	var found *evidence.Record
	giveUp := func(err error) error {
		if transient(err) {
			return err
		}
		return s.fixes.Advance(ctx, op.ID, store.OpFailed, found, "restore_incomplete: "+errorCode(err))
	}
	cur, err := s.readBook(ctx, op.LibraryID, op.BookID)
	if errors.Is(err, library.ErrBookNotFound) || (err == nil && cur.UUID != op.UUID) {
		return s.fixes.Advance(ctx, op.ID, store.OpFailed, nil, "restore_incomplete: book_gone")
	}
	if err != nil {
		return giveUp(err)
	}
	rec := evidence.RecordFrom(cur)
	found = &rec
	var writes []library.Change
	// Whatever the book holds, nothing can put a missing field back, so the
	// restore fails and names it.
	all, left := restoreChanges(op.Changes, op.Before)
	for len(all) > 0 {
		key := pairKey(all[0].Field)
		n := 1
		for n < len(all) && pairKey(all[n].Field) == key {
			n++
		}
		group := all[:n]
		all = all[n:]
		if !slices.ContainsFunc(group, func(c library.Change) bool { return !same(c, cur) }) {
			continue // already back
		}
		if onlyOurs && slices.ContainsFunc(op.Changes, func(c library.Change) bool { return pairKey(c.Field) == key && !same(c, cur) }) {
			left = append(left, key)
			continue
		}
		if !onlyOurs && !holdsKnown(group, op.Changes, observed, cur) {
			left = append(left, key)
			continue
		}
		writes = append(writes, group...)
	}
	fresh := cur
	if len(writes) > 0 {
		var fields []string
		unsafe := false
		for _, c := range writes {
			fields = append(fields, c.Field)
			unsafe = unsafe || !storable(c)
		}
		collides, err := s.renames(ctx, op.LibraryID, writes)
		if err != nil {
			return giveUp(err)
		}
		if unsafe || collides {
			return s.fixes.Advance(ctx, op.ID, store.OpFailed, found, "restore_incomplete: unsafe to write back")
		}
		if err := s.recordRestoreWrite(ctx, op.ID, found, "restore_write: "+strings.Join(fields, ", ")); err != nil {
			return err
		}
		if _, err := s.setFields(ctx, op.LibraryID, op.BookID, writes); err != nil {
			return giveUp(err)
		}
		if fresh, err = s.readBook(ctx, op.LibraryID, op.BookID); err != nil {
			return giveUp(err)
		}
		for _, c := range writes {
			if !same(c, fresh) {
				left = append(left, c.Field)
			}
		}
	}
	after := evidence.RecordFrom(fresh)
	if len(left) > 0 {
		return s.fixes.Advance(ctx, op.ID, store.OpFailed, &after, "restore_incomplete: "+strings.Join(left, ", "))
	}
	s.logger.Info("fix: restored", "operation", op.ID, "book", op.BookID)
	return s.fixes.Advance(ctx, op.ID, store.OpRestored, &after, "")
}
