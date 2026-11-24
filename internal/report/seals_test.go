package report

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

// sealStore is a fake store of several finished runs that each hold the
// same books, and that keeps the checks it is given.
type sealStore struct {
	*fakeStore
	mu      sync.Mutex
	status  map[int64]verify.RunStatus
	checked map[int64]time.Time
	saved   map[int64]SealCheck
	books   map[int64][]BookProjection
	gone    int64
}

type idSnapshot struct {
	Snapshot
	run verify.Run
}

func (s idSnapshot) Run() verify.Run { return s.run }

func (s *sealStore) Snapshot(ctx context.Context, id int64) (Snapshot, error) {
	status, ok := s.status[id]
	if id == s.gone {
		return nil, verify.ErrNoSuchRun
	}
	if !ok {
		return nil, verify.ErrNoSuchRun
	}
	inner, err := s.fakeStore.Snapshot(ctx, 7)
	if err != nil {
		return nil, err
	}
	return idSnapshot{inner, verify.Run{ID: id, Status: status}}, nil
}

func (s *sealStore) SaveSealCheck(_ context.Context, c SealCheck, books []BookProjection) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saved[c.RunID], s.books[c.RunID] = c, books
	s.checked[c.RunID] = time.Now()
	return nil
}

func (s *sealStore) RunsToCheck(_ context.Context, before time.Time) ([]int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []int64
	for _, id := range []int64{1, 2, 3, 4} {
		at, ok := s.checked[id]
		if s.status[id].Terminal() && (!ok || at.Before(before)) {
			out = append(out, id)
		}
	}
	return out, nil
}

func (s *sealStore) FinishedRuns(context.Context) ([]int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []int64
	for _, id := range []int64{1, 2, 3, 4} {
		if s.status[id].Terminal() {
			out = append(out, id)
		}
	}
	return out, nil
}

func newSealStore(books ...Book) *sealStore {
	return &sealStore{
		fakeStore: &fakeStore{books: books},
		status:    map[int64]verify.RunStatus{1: verify.RunCompleted, 2: verify.RunCancelled, 3: verify.RunFailed, 4: verify.RunRunning},
		checked:   map[int64]time.Time{}, saved: map[int64]SealCheck{}, books: map[int64][]BookProjection{},
	}
}

func TestCheckSealsCountsAndContinues(t *testing.T) {
	ctx := context.Background()
	st := newSealStore(sampleBook(1, 2), sampleBook(2, 1), sampleBook(3, 0))
	st.corrupt = 2
	check, err := New(st).CheckSeals(ctx, 1)
	if err != nil || check.Books != 3 || check.Broken != 1 || check.FirstBroken != 2 || check.RunID != 1 {
		t.Fatalf("CheckSeals = %+v, %v", check, err)
	}
	if got := st.saved[1]; !reflect.DeepEqual(got, check) || st.books[1] != nil {
		t.Fatalf("saved %+v with %d books", got, len(st.books[1]))
	}
	st.corrupt = 0
	check, err = New(st).CheckSeals(ctx, 1)
	if err != nil || check.Broken != 0 || check.FirstBroken != 0 || len(st.books[1]) != 3 {
		t.Fatalf("clean CheckSeals = %+v, %v, %d books", check, err, len(st.books[1]))
	}
	if b := st.books[1][0]; b.BookID != 1 || len(b.Findings) != 2 || len([]rune(b.Title)) != 512 {
		t.Fatalf("projected book %+v", b)
	}
	if _, err := New(st).CheckSeals(ctx, 4); !errors.Is(err, ErrRunNotFinished) {
		t.Fatalf("a running run: %v", err)
	}
	if _, err := New(st).CheckSeals(ctx, 9); !errors.Is(err, verify.ErrNoSuchRun) {
		t.Fatalf("a missing run: %v", err)
	}
	if _, err := New(&fakeStore{}).CheckSeals(ctx, 7); !errors.Is(err, ErrNoSealStore) {
		t.Fatalf("a store that can't check: %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := New(st).CheckSeals(cancelled, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
}

func TestSweepOnceChecksOnlyStaleOrMissing(t *testing.T) {
	ctx := context.Background()
	st := newSealStore(sampleBook(1, 1))
	now := time.Now()
	st.checked[1] = now.Add(-time.Hour)      // fresh
	st.checked[2] = now.Add(-25 * time.Hour) // stale
	// run 3 has no check; run 4 is still running
	got, err := New(st).SweepOnce(ctx, 24*time.Hour, func() time.Time { return now }, nil)
	if err != nil || !reflect.DeepEqual(got, []int64{2, 3}) {
		t.Fatalf("SweepOnce = %v, %v", got, err)
	}
	if _, ok := st.saved[1]; ok {
		t.Fatal("the fresh run was checked again")
	}
	if again, err := New(st).SweepOnce(ctx, 24*time.Hour, func() time.Time { return now }, nil); err != nil || len(again) != 0 {
		t.Fatalf("second sweep = %v, %v", again, err)
	}
}

func TestSweepSchedule(t *testing.T) {
	st := newSealStore(sampleBook(1, 1))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		New(st).Sweep(ctx, SweepOptions{First: time.Millisecond, Every: time.Millisecond, MaxAge: time.Nanosecond})
		close(done)
	}()
	// MaxAge of a nanosecond makes every run stale again on every tick, so
	// a second round proves the ticker repeats.
	deadline := time.After(5 * time.Second)
	for {
		st.mu.Lock()
		n := len(st.saved)
		at := st.checked[1]
		st.mu.Unlock()
		if n == 3 && time.Since(at) < time.Second && !at.IsZero() {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("sweep checked %d runs", n)
		case <-time.After(time.Millisecond):
		}
	}
	first := func() time.Time { st.mu.Lock(); defer st.mu.Unlock(); return st.checked[1] }
	seen := first()
	for first().Equal(seen) {
		select {
		case <-deadline:
			t.Fatal("the sweep never ran a second time")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the sweep ignored cancellation")
	}
}

func TestSweepStopsBeforeTheFirstRun(t *testing.T) {
	st := newSealStore(sampleBook(1, 1))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() {
		New(st).Sweep(ctx, SweepOptions{First: time.Hour, Every: time.Hour, MaxAge: time.Hour})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the sweep ignored cancellation while waiting")
	}
	if len(st.saved) != 0 {
		t.Fatal("a cancelled sweep checked a run")
	}
}

// TestTamperWindowIsBounded pins ADR-038: a check made just after a sweep is
// refreshed by the sweep no later than MaxAge + Every after it was made.
func TestTamperWindowIsBounded(t *testing.T) {
	ctx := context.Background()
	st := newSealStore(sampleBook(1, 1))
	o := DefaultSweep
	start := time.Now()
	st.checked[1] = start.Add(time.Millisecond) // made just after the sweep at start
	st.checked[2], st.checked[3] = start.Add(time.Hour*24*365), start.Add(time.Hour*24*365)
	refreshed := time.Duration(-1)
	for at := o.Every; at <= 3*(o.MaxAge+o.Every); at += o.Every {
		now := start.Add(at)
		done, err := New(st).SweepOnce(ctx, o.MaxAge, func() time.Time { return now }, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(done) > 0 {
			refreshed = at
			break
		}
	}
	if refreshed < 0 || refreshed > o.MaxAge+o.Every {
		t.Fatalf("the check was refreshed after %v, want within %v", refreshed, o.MaxAge+o.Every)
	}
	if bound := o.MaxAge + o.Every; bound > 25*time.Hour {
		t.Fatalf("the bound is %v, ADR-038 says about 25 h", bound)
	}
}

func TestSweepOnceLogsAPrunedRunQuietly(t *testing.T) {
	st := newSealStore(sampleBook(1, 1))
	st.gone = 2 // listed, then pruned before its check
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	done, err := New(st).SweepOnce(context.Background(), time.Hour, nil, logger)
	if err != nil || len(done) != 2 {
		t.Fatalf("SweepOnce = %v, %v", done, err)
	}
	if strings.Contains(buf.String(), "level=WARN") || !strings.Contains(buf.String(), "level=DEBUG") {
		t.Fatalf("a pruned run should be logged at debug only:\n%s", buf.String())
	}
}
