package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/decide"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/findings"
	"github.com/felixapel/calibre-bookwarden/internal/store"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

// Store is what agent fixing keeps in the database; *store.AgentRepo
// implements it.
type Store interface {
	Mode(ctx context.Context) (string, error)
	SetMode(ctx context.Context, mode, by string) error
	Queue(ctx context.Context, runID int64, kind, mode, by string) (store.Review, bool, error)
	Next(ctx context.Context) (store.Review, bool, error)
	Start(ctx context.Context, id int64) error
	Finish(ctx context.Context, id int64, state, reason string) error
	Reviews(ctx context.Context, runID int64) ([]store.Review, error)
	AddVerdict(ctx context.Context, v store.Verdict) error
	Verdicts(ctx context.Context, runID int64, promptVersion string) ([]store.Verdict, error)
	AddOutcome(ctx context.Context, o store.Outcome) error
	Outcomes(ctx context.Context, runID int64) ([]store.Outcome, error)
	ReviewOutcomes(ctx context.Context, reviewID int64) ([]store.Outcome, error)
	Tokens(ctx context.Context, runID int64) (map[string]int64, error)
	ProposalBooks(ctx context.Context, runID int64) ([]int, error)
}

// Evidence reads sealed packages; *store.RunRepo implements it.
type Evidence interface {
	Evidence(ctx context.Context, runID int64, bookID int) ([]byte, string, error)
}

// Fixes reads decisions; *store.FixRepo implements it.
type Fixes interface {
	NewestEvidenceRun(ctx context.Context, libraryID string, bookID int, uuid string) (int64, error)
	Proposals(ctx context.Context, runID int64, bookID int) ([]store.ProposalRow, error)
}

// Options configure a Reviewer.
type Options struct {
	Store    Store
	Evidence Evidence
	Fixes    Fixes
	Seats    []Seat
	// Panel names every seat of the panel, configured or not; a seat
	// without an answer leaves the proposal. Default: the Seats' names.
	Panel     []string
	MaxTokens int64 // input plus output tokens per run, over all reviews
	Books     int   // books judged at once; default 3
	Logger    *slog.Logger
}

// Reviewer runs one review at a time.
type Reviewer struct {
	o     Options
	names []string
}

// NewReviewer returns a reviewer. A nil logger discards.
func NewReviewer(o Options) *Reviewer {
	if o.Books < 1 {
		o.Books = 3
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	names := o.Panel
	if len(names) == 0 {
		for _, s := range o.Seats {
			names = append(names, s.Name())
		}
	}
	return &Reviewer{o: o, names: names}
}

// Stop reasons a review records.
const (
	stopOff      = "agent fixing switched off"
	stopTokenCap = "token cap reached" //nolint:gosec // a stop reason, not a credential
)

// review is one review in progress.
type review struct {
	r    *Reviewer
	rv   store.Review
	done map[Key]bool // fields this review already settled, on resume
	mu   sync.Mutex
	stop string
}

func (v *review) stopped() string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.stop
}

func (v *review) stopWith(reason string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.stop == "" {
		v.stop = reason
	}
}

// Review judges a run's open proposals and records the verdicts. It decides
// nothing: every proposal is left for a person (ADR-029). It returns the
// review as it ended. An error, or a cancelled ctx, leaves the review
// running, to resume where it stopped.
func (r *Reviewer) Review(ctx context.Context, rv store.Review) (store.Review, error) {
	if err := r.o.Store.Start(ctx, rv.ID); err != nil {
		return rv, err
	}
	books, err := r.o.Store.ProposalBooks(ctx, rv.RunID)
	if err != nil {
		return rv, err
	}
	// A resumed review keeps what it settled.
	settled, err := r.o.Store.ReviewOutcomes(ctx, rv.ID)
	if err != nil {
		return rv, err
	}
	v := &review{r: r, rv: rv, done: map[Key]bool{}}
	for _, o := range settled {
		v.done[Key{o.BookID, o.Field}] = true
	}
	work := make(chan int)
	errs := make(chan error, r.o.Books)
	var wg sync.WaitGroup
	for range r.o.Books {
		wg.Go(func() {
			for id := range work {
				if err := v.book(ctx, id); err != nil {
					errs <- err
					return
				}
			}
		})
	}
	var firstErr error
feed:
	for _, id := range books {
		select {
		case work <- id:
		case firstErr = <-errs:
			break feed
		case <-ctx.Done():
			break feed
		}
		if v.stopped() != "" {
			break
		}
	}
	close(work)
	wg.Wait()
	close(errs)
	if firstErr == nil {
		firstErr = <-errs
	}
	if firstErr == nil {
		firstErr = ctx.Err()
	}
	if firstErr != nil {
		r.o.Logger.Warn("agent: review interrupted", "review", rv.ID, "run", rv.RunID, "error", ErrorCode(firstErr))
		return rv, firstErr
	}
	rv.State, rv.StopReason = store.ReviewDone, v.stopped()
	if rv.StopReason != "" {
		rv.State = store.ReviewStopped
	}
	if err := r.o.Store.Finish(ctx, rv.ID, rv.State, rv.StopReason); err != nil {
		return rv, err
	}
	r.o.Logger.Info("agent: review ended", "review", rv.ID, "run", rv.RunID, "state", rv.State, "reason", rv.StopReason)
	return rv, nil
}

// book judges one book's open proposals and records what the panel did.
func (v *review) book(ctx context.Context, bookID int) error {
	r, rv := v.r, v.rv
	if v.stopped() != "" || ctx.Err() != nil {
		return nil
	}
	calibration := rv.Kind == store.ReviewCalibration
	raw, seal, err := r.o.Evidence.Evidence(ctx, rv.RunID, bookID)
	if errors.Is(err, verify.ErrNoSuchRun) {
		return v.leaveBook(ctx, bookID, nil, err)
	}
	if err != nil {
		return err
	}
	pkg, dec, err := decide.Open(ctx, raw, seal)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return v.leaveBook(ctx, bookID, raw, err)
	}
	if !calibration {
		newest, err := r.o.Fixes.NewestEvidenceRun(ctx, pkg.LibraryID, bookID, pkg.UUID)
		if err != nil {
			return err
		}
		if newest != rv.RunID {
			return nil
		}
	}
	open := dec.Proposals
	if !calibration {
		rows, err := r.o.Fixes.Proposals(ctx, rv.RunID, bookID)
		if err != nil {
			return err
		}
		open = slices.DeleteFunc(slices.Clone(open), func(p evidence.Proposal) bool {
			return slices.ContainsFunc(rows, func(row store.ProposalRow) bool { return row.Proposal.Field == p.Field })
		})
	}
	open = slices.DeleteFunc(slices.Clone(open), func(p evidence.Proposal) bool { return v.done[Key{bookID, p.Field}] })
	if len(open) == 0 {
		return nil
	}
	// Fields in the packet's order, so the token cap cuts in a stable place.
	open = slices.SortedFunc(slices.Values(open), func(a, b evidence.Proposal) int { return strings.Compare(a.Field, b.Field) })
	hashes := proposalHashes(open)
	newest, asked, err := v.verdicts(ctx, bookID, hashes)
	if err != nil {
		return err
	}
	// A seat is asked when it has no answer yet, unless this review already
	// asked it: a failure is retried by a later review, not on resume.
	var need []Seat
	for _, s := range r.o.Seats {
		for _, p := range open {
			if vd, ok := newest[p.Field][s.Name()]; (!ok || vd.ErrorCode != "") && !asked[p.Field][s.Name()] {
				need = append(need, s)
				break
			}
		}
	}
	if len(need) > 0 {
		// A review queued before the switch went off must not call a seat.
		mode, err := r.o.Store.Mode(ctx)
		if err != nil {
			return err
		}
		if mode == store.ModeOff {
			v.stopWith(stopOff)
			return nil
		}
		toks, err := r.o.Store.Tokens(ctx, rv.RunID)
		if err != nil {
			return err
		}
		var total int64
		for _, n := range toks {
			total += n
		}
		// Checked before the seats are asked, so only the books already in
		// flight can cross the cap: at most Books of them, one call per seat.
		if total >= r.o.MaxTokens {
			v.stopWith(stopTokenCap)
			return nil
		}
		if err := v.judge(ctx, pkg, bookID, open, need); err != nil {
			var be *bookError
			if errors.As(err, &be) {
				return v.leaveBook(ctx, bookID, nil, be.err, open...)
			}
			return err
		}
		if newest, _, err = v.verdicts(ctx, bookID, hashes); err != nil {
			return err
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	mode, err := r.o.Store.Mode(ctx)
	if err != nil {
		return err
	}
	if mode == store.ModeOff {
		v.stopWith(stopOff)
		return nil
	}
	for _, p := range open {
		var rs []SeatResult
		for seat, vd := range newest[p.Field] {
			rs = append(rs, SeatResult{Seat: seat, Answer: vd.Answer, Err: vd.ErrorCode})
		}
		outcome, reason := Combine(rs, r.names)
		if outcome != store.OutcomeLeft {
			outcome, reason = store.OutcomeLeft, "shadow: "+outcome
		}
		if err := r.o.Store.AddOutcome(ctx, store.Outcome{ReviewID: rv.ID, RunID: rv.RunID, BookID: bookID, Field: p.Field, Outcome: outcome, Reason: reason}); err != nil {
			return err
		}
	}
	return nil
}

// bookError is a failure that belongs to one book, not to the store or the
// context: the review records it and goes on.
type bookError struct{ err error }

func (e *bookError) Error() string { return e.err.Error() }
func (e *bookError) Unwrap() error { return e.err }

// leaveBook records that a book couldn't be judged because its evidence is
// missing, doesn't open or can't be packed: each of its open fields is left
// for a person, with the reason. open, when given, are the book's still-open
// proposals. Otherwise raw, which is not trusted, only names the fields,
// and only names that are proposal fields count; without any name one
// book-level outcome with an empty field is recorded. Like a judged book, a
// book of a superseded run and a field somebody decided get no outcome.
// Only a store error is returned.
func (v *review) leaveBook(ctx context.Context, bookID int, raw []byte, cause error, open ...evidence.Proposal) error {
	var fields []string
	var ident struct {
		LibraryID string `json:"library_id"`
		UUID      string `json:"uuid"`
		Proposals []struct {
			Field string `json:"field"`
		} `json:"proposals"`
	}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &ident) // best effort: names only, never values
	}
	if len(open) > 0 {
		for _, p := range open {
			fields = append(fields, p.Field)
		}
	} else {
		known := decide.Fields()
		for _, p := range ident.Proposals {
			if slices.Contains(known, p.Field) {
				fields = append(fields, p.Field)
			}
		}
	}
	slices.Sort(fields)
	fields = slices.Compact(fields)
	if v.rv.Kind != store.ReviewCalibration {
		if ident.UUID != "" {
			newest, err := v.r.o.Fixes.NewestEvidenceRun(ctx, ident.LibraryID, bookID, ident.UUID)
			if err != nil {
				return err
			}
			if newest != v.rv.RunID {
				return nil
			}
		}
		if len(fields) > 0 {
			rows, err := v.r.o.Fixes.Proposals(ctx, v.rv.RunID, bookID)
			if err != nil {
				return err
			}
			fields = slices.DeleteFunc(fields, func(f string) bool {
				return slices.ContainsFunc(rows, func(row store.ProposalRow) bool { return row.Proposal.Field == f })
			})
			if len(fields) == 0 {
				return nil // every field is decided already
			}
		}
	}
	if len(fields) == 0 {
		fields = []string{""}
	}
	reason := "evidence unavailable: " + evidenceCode(cause)
	v.r.o.Logger.Warn("agent: book left for a person", "review", v.rv.ID, "run", v.rv.RunID, "book", bookID, "reason", reason)
	for _, f := range fields {
		if v.done[Key{bookID, f}] {
			continue
		}
		if err := v.r.o.Store.AddOutcome(ctx, store.Outcome{ReviewID: v.rv.ID, RunID: v.rv.RunID, BookID: bookID, Field: f, Outcome: store.OutcomeLeft, Reason: reason}); err != nil {
			return err
		}
	}
	return nil
}

// evidenceCode names why a book's evidence was unusable, without the
// error's text, which can carry book data.
func evidenceCode(err error) string {
	switch {
	case errors.Is(err, verify.ErrNoSuchRun):
		return "missing"
	case errors.Is(err, errMarker):
		return "marker"
	case errors.Is(err, evidence.ErrSealBroken):
		return "seal_broken"
	case errors.Is(err, decide.ErrMismatch), errors.Is(err, findings.ErrMismatch):
		return "mismatch"
	case errors.Is(err, findings.ErrInvalidEvidence):
		return "invalid"
	}
	return "unreadable"
}

// verdicts returns the newest verdict per field and seat for a book, from
// every review of the run under this prompt version whose stored proposal
// hash is the one in hashes for that field, and which seats this review
// asked about which fields. The packet hash isn't compared: a packet holds
// whichever fields were open when it was built, so it varies legitimately.
func (v *review) verdicts(ctx context.Context, bookID int, hashes map[string]string) (newest map[string]map[string]store.Verdict, asked map[string]map[string]bool, err error) {
	all, err := v.r.o.Store.Verdicts(ctx, v.rv.RunID, PromptVersion)
	if err != nil {
		return nil, nil, err
	}
	newest, asked = map[string]map[string]store.Verdict{}, map[string]map[string]bool{}
	for _, vd := range all {
		if vd.BookID != bookID || !slices.Contains(v.r.names, vd.Seat) {
			continue
		}
		// A verdict on another proposal (or one without a hash) is no answer
		// to this one: the seat is asked again.
		if want, ok := hashes[vd.Field]; !ok || want == "" || vd.ProposalHash != want {
			continue
		}
		if newest[vd.Field] == nil {
			newest[vd.Field], asked[vd.Field] = map[string]store.Verdict{}, map[string]bool{}
		}
		newest[vd.Field][vd.Seat] = vd // oldest first, so the newest wins
		if vd.ReviewID == v.rv.ID {
			asked[vd.Field][vd.Seat] = true
		}
	}
	return newest, asked, nil
}

// proposalHashes maps each proposal's field to its ProposalHash.
func proposalHashes(open []evidence.Proposal) map[string]string {
	hashes := make(map[string]string, len(open))
	for _, p := range open {
		hashes[p.Field] = ProposalHash(p)
	}
	return hashes
}

// judge asks the seats in need about a book's open proposals, at once, and
// records every verdict, or the error, per field.
func (v *review) judge(ctx context.Context, pkg evidence.Package, bookID int, open []evidence.Proposal, need []Seat) error {
	packet, err := BuildPacket(pkg, open)
	if err != nil {
		return &bookError{err}
	}
	hashes := proposalHashes(open)
	errs := make([]error, len(need))
	var wg sync.WaitGroup
	for i, s := range need {
		wg.Go(func() {
			start := time.Now()
			reply, err := s.Judge(ctx, Brief(), packet.Text)
			var js map[string]Judgement
			if err == nil {
				js, err = ParseAnswer(reply.Text, packet.Fields)
			}
			if ctx.Err() != nil {
				return // interrupted: the resumed review asks again
			}
			for j, f := range packet.Fields {
				vd := store.Verdict{ReviewID: v.rv.ID, RunID: v.rv.RunID, BookID: bookID, Field: f, Seat: s.Name(), PromptVersion: PromptVersion,
					ProposalHash: hashes[f], PacketHash: packet.Hash, Model: s.Model(), Millis: time.Since(start).Milliseconds()}
				if j == 0 { // one call's tokens, counted once
					vd.InputTokens, vd.OutputTokens = reply.InputTokens, reply.OutputTokens
				}
				if err != nil {
					vd.ErrorCode = ErrorCode(err)
				} else {
					vd.Answer, vd.Reason = js[f].Answer, js[f].Reason
				}
				if aerr := v.r.o.Store.AddVerdict(ctx, vd); aerr != nil {
					errs[i] = fmt.Errorf("agent: record verdict: %w", aerr)
					return
				}
			}
		})
	}
	wg.Wait()
	return errors.Join(errs...)
}
