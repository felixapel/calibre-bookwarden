package verifytest

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

type memBook struct {
	ref      library.BookRef
	state    verify.BookState
	reason   string
	evidence []byte
	seal     string
	excerpts map[string]string
	reused   bool
	key      string
	measured time.Time
	inHand   bool
	crashes  int
}

type memRun struct {
	run   verify.Run
	books []*memBook // frozen order
}

// MemStore is an in-memory verify.RunStore with the same semantics as
// store.RunRepo. It is safe for concurrent use.
type MemStore struct {
	mu     sync.Mutex
	nextID int64
	runs   map[int64]*memRun
	locked bool
}

var _ verify.RunStore = (*MemStore)(nil)

func NewMemStore() *MemStore {
	return &MemStore{runs: map[int64]*memRun{}}
}

func (s *MemStore) CreateRun(_ context.Context, scope verify.Scope, libraryID, appVersion string, books []library.BookRef) (verify.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.runs {
		if !r.run.Status.Terminal() {
			return verify.Run{}, verify.ErrRunActive
		}
	}
	s.nextID++
	r := &memRun{run: verify.Run{
		ID: s.nextID, Scope: scope, Status: verify.RunRunning,
		LibraryID: libraryID, AppVersion: appVersion, StartedAt: time.Now().UTC(),
	}}
	for _, ref := range books {
		r.books = append(r.books, &memBook{ref: ref, state: verify.BookPending})
	}
	s.runs[r.run.ID] = r
	return r.run, nil
}

func (s *MemStore) ActiveRun(context.Context) (verify.Run, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.runs {
		if !r.run.Status.Terminal() {
			return r.run, true, nil
		}
	}
	return verify.Run{}, false, nil
}

func (s *MemStore) get(id int64) (*memRun, error) {
	r, ok := s.runs[id]
	if !ok {
		return nil, fmt.Errorf("memstore: run %d: %w", id, verify.ErrNoSuchRun)
	}
	return r, nil
}

func (s *MemStore) Run(_ context.Context, id int64) (verify.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.get(id)
	if err != nil {
		return verify.Run{}, err
	}
	return r.run, nil
}

func (s *MemStore) PendingBooks(_ context.Context, runID int64) ([]library.BookRef, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.get(runID)
	if err != nil {
		return nil, err
	}
	var refs []library.BookRef
	for _, b := range r.books {
		if b.state == verify.BookPending {
			refs = append(refs, b.ref)
		}
	}
	return refs, nil
}

func (s *MemStore) FinishBook(_ context.Context, runID int64, ref library.BookRef, out verify.Outcome) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.get(runID)
	if err != nil {
		return err
	}
	for _, b := range r.books {
		if b.ref.ID != ref.ID || b.state != verify.BookPending {
			continue
		}
		b.state, b.reason, b.inHand = out.State, out.Reason, false
		if out.State == verify.BookChecked {
			b.evidence, b.seal = slices.Clone(out.Evidence), out.Seal
			b.excerpts, b.reused, b.key, b.measured = maps.Clone(out.Excerpts), out.Reused, out.InspectKey, out.MeasuredAt
		}
		return nil
	}
	return fmt.Errorf("memstore: run %d book %d: %w", runID, ref.ID, verify.ErrBookFinished)
}

func (s *MemStore) TakeBook(_ context.Context, runID int64, bookID int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.get(runID)
	if err != nil {
		return err
	}
	for _, b := range r.books {
		if b.ref.ID == bookID && b.state == verify.BookPending {
			b.inHand = true
		}
	}
	return nil
}

func (s *MemStore) ReleaseBooks(_ context.Context, runID int64, bookIDs []int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.get(runID)
	if err != nil {
		return err
	}
	for _, b := range r.books {
		if slices.Contains(bookIDs, b.ref.ID) {
			b.inHand = false
		}
	}
	return nil
}

func (s *MemStore) RecoverCrashed(_ context.Context, runID int64) (int, []int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.get(runID)
	if err != nil {
		return 0, nil, err
	}
	gaveUp := 0
	var crashed []int
	for _, b := range r.books {
		if b.state != verify.BookPending {
			continue
		}
		if b.inHand {
			b.crashes++
			b.inHand = false
		}
		switch {
		case b.crashes >= verify.MaxCrashes:
			b.state, b.reason = verify.BookFailed, "process_crashed"
			gaveUp++
		case b.crashes > 0:
			crashed = append(crashed, b.ref.ID)
		}
	}
	return gaveUp, crashed, nil
}

// InHand reports whether a book is marked in hand.
func (s *MemStore) InHand(runID int64, bookID int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.runs[runID]; ok {
		for _, b := range r.books {
			if b.ref.ID == bookID {
				return b.inHand
			}
		}
	}
	return false
}

// Crashes returns how many crashes were counted for a book.
func (s *MemStore) Crashes(runID int64, bookID int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.runs[runID]; ok {
		for _, b := range r.books {
			if b.ref.ID == bookID {
				return b.crashes
			}
		}
	}
	return 0
}

func (s *MemStore) SetStatus(_ context.Context, runID int64, status verify.RunStatus, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.get(runID)
	if err != nil {
		return err
	}
	r.run.Status, r.run.Error = status, message
	if status.Terminal() {
		r.run.FinishedAt = time.Now().UTC()
	}
	return nil
}

func (s *MemStore) RequestCancel(_ context.Context, runID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.get(runID)
	if err != nil {
		return err
	}
	if !r.run.Status.Terminal() {
		r.run.CancelRequested = true
	}
	return nil
}

func (s *MemStore) Progress(_ context.Context, runID int64) (verify.Progress, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.get(runID)
	if err != nil {
		return verify.Progress{}, err
	}
	p := verify.Progress{Total: len(r.books)}
	for _, b := range r.books {
		switch b.state {
		case verify.BookPending:
			p.Pending++
		case verify.BookChecked:
			p.Checked++
			if b.reused {
				p.Reused++
			}
		case verify.BookSourceChanged:
			p.SourceChanged++
		case verify.BookFailed:
			p.Failed++
		}
	}
	return p, nil
}

func (s *MemStore) newestFirst() []*memRun {
	runs := make([]*memRun, 0, len(s.runs))
	for _, r := range s.runs {
		runs = append(runs, r)
	}
	slices.SortFunc(runs, func(a, b *memRun) int { return int(b.run.ID - a.run.ID) })
	return runs
}

func (s *MemStore) ListRuns(_ context.Context, limit int) ([]verify.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []verify.Run
	for _, r := range s.newestFirst() {
		if len(out) == limit {
			break
		}
		out = append(out, r.run)
	}
	return out, nil
}

// PriorEvidence returns the newest sealed package of the book, as the
// Postgres store does: it must open, and every excerpt it refers to must be
// kept. The run being checked has no package of the book yet.
func (s *MemStore) PriorEvidence(_ context.Context, libraryID string, ref library.BookRef) (verify.Prior, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.newestFirst() {
		if r.run.LibraryID != libraryID {
			continue
		}
		for _, b := range r.books {
			if b.ref.ID != ref.ID || b.evidence == nil {
				continue
			}
			if b.ref.UUID != ref.UUID {
				return verify.Prior{}, false, nil
			}
			p, err := evidence.Open(b.evidence, b.seal)
			if err != nil {
				return verify.Prior{}, false, nil
			}
			refs, err := evidence.ExcerptRefs(p)
			if err != nil {
				return verify.Prior{}, false, nil
			}
			bodies := map[string]string{}
			for hash, chars := range refs {
				body, ok := b.excerpts[hash]
				if !ok || len([]rune(body)) != chars || evidence.ExcerptHash(body) != hash {
					return verify.Prior{}, false, nil
				}
				bodies[hash] = body
			}
			return verify.Prior{Package: p, InspectKey: b.key, MeasuredAt: b.measured, Excerpts: bodies}, true, nil
		}
	}
	return verify.Prior{}, false, nil
}

func (s *MemStore) SetPinned(_ context.Context, runID int64, pinned bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.get(runID)
	if err != nil {
		return err
	}
	r.run.Pinned = pinned
	return nil
}

// SaveRunStats keeps stats as given; callers pass a copy.
func (s *MemStore) SaveRunStats(_ context.Context, runID int64, stats verify.Stats) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.get(runID)
	if err != nil {
		return err
	}
	r.run.Stats = stats
	return nil
}

func (s *MemStore) LockRunner(context.Context) (func(), bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.locked {
		return nil, false, nil
	}
	s.locked = true
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			s.locked = false
			s.mu.Unlock()
		})
	}, true, nil
}

// BookStates returns each book's state in a run, by book id.
func (s *MemStore) BookStates(runID int64) map[int]verify.BookState {
	s.mu.Lock()
	defer s.mu.Unlock()
	states := map[int]verify.BookState{}
	if r, ok := s.runs[runID]; ok {
		for _, b := range r.books {
			states[b.ref.ID] = b.state
		}
	}
	return states
}

// BookReasons returns the reason of each finished book in a run, by book id.
func (s *MemStore) BookReasons(runID int64) map[int]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	reasons := map[int]string{}
	if r, ok := s.runs[runID]; ok {
		for _, b := range r.books {
			reasons[b.ref.ID] = b.reason
		}
	}
	return reasons
}

// Evidence returns a checked book's evidence and seal.
func (s *MemStore) Evidence(runID int64, bookID int) ([]byte, string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.runs[runID]; ok {
		for _, b := range r.books {
			if b.ref.ID == bookID && b.evidence != nil {
				return slices.Clone(b.evidence), b.seal, true
			}
		}
	}
	return nil, "", false
}

// MeasuredAt returns when a checked book's file evidence was measured.
func (s *MemStore) MeasuredAt(runID int64, bookID int) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.runs[runID]; ok {
		for _, b := range r.books {
			if b.ref.ID == bookID {
				return b.measured
			}
		}
	}
	return time.Time{}
}
