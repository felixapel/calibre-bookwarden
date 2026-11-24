package eval

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/felixapel/calibre-bookwarden/internal/store"
)

// ErrInvalidAnswer means an answer isn't one of the three, names no field of
// the sample, or carries a typed value that isn't a plain, short text.
var ErrInvalidAnswer = errors.New("eval: invalid confirmation")

// maxTyped bounds a typed value, in bytes.
const maxTyped = 512

// RecallService draws recall samples, records the confirmations and
// computes the recall. It reads the verified projection through the store and
// never writes a label or a decision.
type RecallService struct{ repo *store.RecallRepo }

// NewRecallService returns a RecallService over repo.
func NewRecallService(repo *store.RecallRepo) *RecallService { return &RecallService{repo: repo} }

// Start draws a sample of SampleSize books from the latest checked run. A nil
// seed draws a random one; either way the seed is recorded.
func (s *RecallService) Start(ctx context.Context, seed *int64, by string) (int64, error) {
	run, err := s.repo.LatestCheckedRun(ctx)
	if err != nil {
		return 0, err
	}
	cands, err := s.repo.Candidates(ctx, run.ID)
	if err != nil {
		return 0, err
	}
	if len(cands) == 0 {
		return 0, store.ErrNoCheckedRun
	}
	var sd int64
	if seed != nil {
		sd = *seed
	} else {
		var b [8]byte
		if _, err := rand.Read(b[:]); err != nil {
			return 0, fmt.Errorf("eval: random seed: %w", err)
		}
		sd = int64(binary.BigEndian.Uint64(b[:]) >> 1)
	}
	d := DrawSample(cands, sd, SampleSize)
	return s.repo.CreateSample(ctx, store.RecallSample{
		LibraryID: run.LibraryID, RunID: run.ID, Seed: sd, Size: len(d.Books), Strata: d.Strata, CreatedBy: by,
	}, d.Books)
}

// Latest is the newest sample; store.ErrNoSuchSample when there is none.
func (s *RecallService) Latest(ctx context.Context) (store.RecallSample, error) {
	return s.repo.LatestSample(ctx)
}

// Sample is a sample by id.
func (s *RecallService) Sample(ctx context.Context, id int64) (store.RecallSample, error) {
	return s.repo.Sample(ctx, id)
}

// BookAt is the book at a position (from 1) of a sample.
func (s *RecallService) BookAt(ctx context.Context, id int64, position int) (store.SampleBook, error) {
	return s.repo.SampleBookAt(ctx, id, position)
}

// Answers returns the newest answer of each field of the book at a position,
// by field, for showing what was answered before.
func (s *RecallService) Answers(ctx context.Context, id int64, position int) (map[string]store.Confirmation, error) {
	b, err := s.repo.SampleBookAt(ctx, id, position)
	if err != nil {
		return nil, err
	}
	all, err := s.repo.LatestConfirmations(ctx, id)
	if err != nil {
		return nil, err
	}
	out := map[string]store.Confirmation{}
	for _, c := range all {
		if c.BookID == b.BookID {
			out[c.Field] = c
		}
	}
	return out, nil
}

// cleanAnswers checks answers and sets who gave them: each names one of the
// sample's fields once and one of the three answers. A typed value is plain
// text of up to maxTyped bytes, trimmed (an empty one is none), and makes the
// answer "wrong".
func cleanAnswers(answers []store.Confirmation, by string) ([]store.Confirmation, error) {
	clean := make([]store.Confirmation, 0, len(answers))
	seen := map[string]bool{}
	for _, a := range answers {
		if !slices.Contains(store.RecallFields, a.Field) || seen[a.Field] ||
			!slices.Contains([]string{store.AnswerRight, store.AnswerWrong, store.AnswerSkip}, a.Answer) {
			return nil, fmt.Errorf("%w: %s %q", ErrInvalidAnswer, a.Field, a.Answer)
		}
		seen[a.Field] = true
		a.By = by
		if a.Typed != nil {
			t := strings.TrimSpace(*a.Typed)
			switch {
			case t == "":
				a.Typed = nil
			case len(t) > maxTyped, !utf8.ValidString(t), strings.ContainsFunc(t, func(r rune) bool { return r < ' ' && r != '\n' }):
				return nil, fmt.Errorf("%w: typed value for %s", ErrInvalidAnswer, a.Field)
			default:
				// A typed value says the Calibre value is wrong, whichever
				// radio was left on.
				a.Typed, a.Answer = &t, store.AnswerWrong
			}
		}
		clean = append(clean, a)
	}
	return clean, nil
}

// Confirm records the answers for the book at a position, all or none.
func (s *RecallService) Confirm(ctx context.Context, id int64, position int, answers []store.Confirmation, by string) error {
	clean, err := cleanAnswers(answers, by)
	if err != nil {
		return err
	}
	return s.repo.Confirm(ctx, id, position, clean)
}

// SampleStatus is a sample as the pages and the CLI show it. Result is nil
// until the results may be shown: the sample is complete and was closed.
type SampleStatus struct {
	Sample   store.RecallSample
	Answered int
	// Next is the position of the first book with no answer, 0 when every
	// book has one.
	Next   int
	Result *RecallResult
}

// Status reads a sample's progress and, when allowed, its recall. The recall
// is Bookwarden's opinion of the books Felix is answering blind, so it is
// withheld until every book has an answer. With reveal, a complete sample is
// closed on the spot (the time is recorded once, in its own append-only table)
// and its results returned; from then on answers are refused. Without reveal
// the results come only from a sample that is already closed.
func (s *RecallService) Status(ctx context.Context, id int64, reveal bool) (SampleStatus, error) {
	sample, err := s.repo.Sample(ctx, id)
	if err != nil {
		return SampleStatus{}, err
	}
	books, err := s.repo.SampleBooks(ctx, id)
	if err != nil {
		return SampleStatus{}, err
	}
	answers, err := s.repo.LatestConfirmations(ctx, id)
	if err != nil {
		return SampleStatus{}, err
	}
	answered := map[int]bool{}
	for _, a := range answers {
		answered[a.BookID] = true
	}
	st := SampleStatus{Sample: sample, Answered: len(answered)}
	for _, b := range books {
		if !answered[b.BookID] {
			st.Next = b.Position
			break
		}
	}
	if st.Next != 0 {
		return st, nil
	}
	if sample.ClosedAt == nil {
		if !reveal {
			return st, nil
		}
		at, err := s.repo.Close(ctx, id)
		if err != nil {
			return SampleStatus{}, err
		}
		st.Sample.ClosedAt = &at
	}
	facts, err := s.repo.SampleFacts(ctx, id)
	if err != nil {
		return SampleStatus{}, err
	}
	res, err := Recall(RecallInput{Sample: st.Sample, Books: books, Answers: answers, Facts: facts})
	if err != nil {
		return SampleStatus{}, err
	}
	st.Result = &res
	return st, nil
}
