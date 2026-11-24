package verify_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
	"github.com/felixapel/calibre-bookwarden/internal/verify/verifytest"
)

func books(n int, size int64, content string) []verifytest.FakeBook {
	out := make([]verifytest.FakeBook, 0, n)
	for i := 1; i <= n; i++ {
		b := epubBook(i, size, content)
		b.Book.UUID = fmt.Sprintf("uuid-%d", i)
		out = append(out, b)
	}
	return out
}

// serve starts a runner on store and lib and returns it with a stop func
// that cancels Serve and waits for it to return.
func serve(t *testing.T, store verify.RunStore, lib verify.Library) (*verify.Runner, func()) {
	t.Helper()
	return serveWith(t, store, lib, verify.Options{})
}

// serveWith is serve with opts; ScratchDir is set for tests, and Backoff
// unless opts has one.
func serveWith(t *testing.T, store verify.RunStore, lib verify.Library, opts verify.Options) (*verify.Runner, func()) {
	t.Helper()
	opts.ScratchDir = t.TempDir()
	if opts.Backoff == nil {
		opts.Backoff = func(int) time.Duration { return time.Millisecond }
	}
	r := verify.NewRunner(store, lib, opts)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Serve(ctx) }()
	var stopped bool
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Serve() = %v", err)
		}
	}
	t.Cleanup(stop)
	return r, stop
}

// waitFor polls until cond holds, or fails the test after 10 s.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func view(t *testing.T, r *verify.Runner, id int64) verify.RunView {
	t.Helper()
	v, err := r.View(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func waitTerminal(t *testing.T, r *verify.Runner, id int64) verify.RunView {
	t.Helper()
	var v verify.RunView
	waitFor(t, fmt.Sprintf("run %d to end", id), func() bool {
		v = view(t, r, id)
		return v.Run.Status.Terminal()
	})
	return v
}

func start(t *testing.T, r *verify.Runner, scope verify.Scope) verify.Run {
	t.Helper()
	run, err := r.Start(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func TestRunChecksEveryBookOnce(t *testing.T) {
	lib := verifytest.NewLibrary("library", books(20, 4, "abcd")...)
	r, _ := serve(t, verifytest.NewMemStore(), lib)
	v := waitTerminal(t, r, start(t, r, verify.Scope{}).ID)
	if v.Run.Status != verify.RunCompleted || v.Progress != (verify.Progress{Total: 20, Checked: 20}) {
		t.Fatalf("run ended %s with %+v", v.Run.Status, v.Progress)
	}
	if v.Run.LibraryID != "library" || v.Run.AppVersion != "dev" || v.Run.FinishedAt.IsZero() {
		t.Errorf("run = %+v", v.Run)
	}
	for id := 1; id <= 20; id++ {
		if n := lib.ExportCalls(id, "EPUB"); n != 1 {
			t.Errorf("book %d exported %d times", id, n)
		}
	}
}

func TestStartRefused(t *testing.T) {
	lib := verifytest.NewLibrary("library", books(2, 4, "abcd")...)
	lib.SetExportDelay(time.Hour)
	r, _ := serve(t, verifytest.NewMemStore(), lib)
	run := start(t, r, verify.Scope{})
	if _, err := r.Start(context.Background(), verify.Scope{}); !errors.Is(err, verify.ErrRunActive) {
		t.Errorf("second Start() = %v, want ErrRunActive", err)
	}
	if err := r.Cancel(context.Background(), run.ID); err != nil {
		t.Fatal(err)
	}
	waitTerminal(t, r, run.ID)

	lib.SetCheckError(fmt.Errorf("probe: %w", library.ErrIncompatible))
	if _, err := r.Start(context.Background(), verify.Scope{}); !errors.Is(err, verify.ErrCalibreNotReady) {
		t.Errorf("Start() against an incompatible Calibre = %v, want ErrCalibreNotReady", err)
	}
	if st := r.Calibre(); st.State != "incompatible" || st.Message == "" {
		t.Errorf("Calibre() = %+v, want incompatible with a message", st)
	}
}

func TestEmptyLibrary(t *testing.T) {
	r, _ := serve(t, verifytest.NewMemStore(), verifytest.NewLibrary("library"))
	v := waitTerminal(t, r, start(t, r, verify.Scope{}).ID)
	if v.Run.Status != verify.RunCompleted || v.Progress.Total != 0 {
		t.Fatalf("run ended %s with %+v", v.Run.Status, v.Progress)
	}
}

func TestScopeOneBook(t *testing.T) {
	lib := verifytest.NewLibrary("library", books(5, 4, "abcd")...)
	r, _ := serve(t, verifytest.NewMemStore(), lib)
	v := waitTerminal(t, r, start(t, r, verify.Scope{BookID: 3}).ID)
	if v.Run.Status != verify.RunCompleted || v.Progress != (verify.Progress{Total: 1, Checked: 1}) || v.Run.Scope.BookID != 3 {
		t.Fatalf("run ended %s with %+v, scope %+v", v.Run.Status, v.Progress, v.Run.Scope)
	}
	for id := 1; id <= 5; id++ {
		want := 0
		if id == 3 {
			want = 1
		}
		if n := lib.ExportCalls(id, "EPUB"); n != want {
			t.Errorf("book %d exported %d times, want %d", id, n, want)
		}
	}
	if _, err := r.Start(context.Background(), verify.Scope{BookID: 99}); !errors.Is(err, library.ErrBookNotFound) {
		t.Errorf("Start() for a missing book = %v, want ErrBookNotFound", err)
	}
}

func TestRecheckIdentity(t *testing.T) {
	ctx := context.Background()
	for _, tt := range []struct {
		scope verify.Scope
		want  error
	}{
		{verify.Scope{BookID: 2, ExpectedLibraryID: "library", ExpectedUUID: "uuid-2"}, nil},
		{verify.Scope{BookID: 2, ExpectedLibraryID: "other", ExpectedUUID: "uuid-2"}, verify.ErrBookChanged},
		{verify.Scope{BookID: 2, ExpectedLibraryID: "library", ExpectedUUID: "reused"}, verify.ErrBookChanged},
		{verify.Scope{BookID: 99, ExpectedLibraryID: "library", ExpectedUUID: "removed"}, library.ErrBookNotFound},
		{verify.Scope{BookID: 2, ExpectedLibraryID: "library"}, verify.ErrInvalidScope},
		{verify.Scope{BookID: 2, ExpectedUUID: "uuid-2"}, verify.ErrInvalidScope},
		{verify.Scope{ExpectedLibraryID: "library", ExpectedUUID: "uuid-2"}, verify.ErrInvalidScope},
		{verify.Scope{BookID: -1}, verify.ErrInvalidScope},
	} {
		t.Run(fmt.Sprint(tt.scope), func(t *testing.T) {
			st := verifytest.NewMemStore()
			lib := verifytest.NewLibrary("library", books(3, 4, "abcd")...)
			r := verify.NewRunner(st, lib, verify.Options{ScratchDir: t.TempDir()})
			run, err := r.Start(ctx, tt.scope)
			if !errors.Is(err, tt.want) {
				t.Fatalf("Start=%v want %v", err, tt.want)
			}
			if tt.want != nil {
				runs, _ := st.ListRuns(ctx, 10)
				if len(runs) != 0 {
					t.Fatal("identity failure created run")
				}
			} else {
				pending, err := st.PendingBooks(ctx, run.ID)
				if err != nil || len(pending) != 1 || pending[0] != (library.BookRef{ID: 2, UUID: "uuid-2"}) {
					t.Fatalf("frozen=%v/%v", pending, err)
				}
			}
		})
	}
}

func TestInFlightStaysUnderLimit(t *testing.T) {
	lib := verifytest.NewLibrary("library", books(8, 400<<20, "abcd")...)
	lib.SetExportDelay(20 * time.Millisecond)
	r, _ := serve(t, verifytest.NewMemStore(), lib)
	v := waitTerminal(t, r, start(t, r, verify.Scope{}).ID)
	if v.Run.Status != verify.RunCompleted || v.Progress.Checked != 8 {
		t.Fatalf("run ended %s with %+v", v.Run.Status, v.Progress)
	}
	if got := lib.MaxConcurrentExportBytes(); got > 1<<30 || got < 800<<20 {
		t.Errorf("max bytes in flight = %d MiB, want 800..1024 MiB", got>>20)
	}
}

func TestBookLargerThanBudgetFinishes(t *testing.T) {
	b := epubBook(1, 500<<20, "abcd")
	b.Book.Formats = []library.Format{
		{Name: "AZW3", Size: 500 << 20, MTime: t0}, {Name: "EPUB", Size: 500 << 20, MTime: t0}, {Name: "PDF", Size: 500 << 20, MTime: t0},
	}
	lib := verifytest.NewLibrary("library", b)
	r, _ := serve(t, verifytest.NewMemStore(), lib)
	v := waitTerminal(t, r, start(t, r, verify.Scope{}).ID)
	if v.Run.Status != verify.RunCompleted || v.Progress.Checked != 1 {
		t.Fatalf("run ended %s with %+v", v.Run.Status, v.Progress)
	}
}

func TestRunWaitsForCalibre(t *testing.T) {
	lib := verifytest.NewLibrary("library", books(10, 4, "abcd")...)
	lib.SetExportDelay(10 * time.Millisecond)
	r, _ := serve(t, verifytest.NewMemStore(), lib)
	id := start(t, r, verify.Scope{}).ID
	waitFor(t, "3 books checked", func() bool { return view(t, r, id).Progress.Checked >= 3 })
	lib.SetDown(true)
	waitFor(t, "the run to wait", func() bool { return view(t, r, id).Run.Status == verify.RunWaiting })
	if st := r.Calibre(); st.State != "unreachable" {
		t.Errorf("Calibre() while down = %+v", st)
	}
	lib.SetDown(false)
	v := waitTerminal(t, r, id)
	if v.Run.Status != verify.RunCompleted || v.Progress != (verify.Progress{Total: 10, Checked: 10}) {
		t.Fatalf("run ended %s with %+v (%s)", v.Run.Status, v.Progress, v.Run.Error)
	}
}

func TestRunFailsWhenCalibreRefusesTheAccount(t *testing.T) {
	lib := verifytest.NewLibrary("library", books(3, 4, "abcd")...)
	lib.SetExportError(func(int, string) error { return fmt.Errorf("export: %w", library.ErrForbidden) })
	r, _ := serve(t, verifytest.NewMemStore(), lib)
	v := waitTerminal(t, r, start(t, r, verify.Scope{}).ID)
	if v.Run.Status != verify.RunFailed || v.Run.Error == "" || v.Progress.Pending != 3 {
		t.Fatalf("run ended %s (%q) with %+v", v.Run.Status, v.Run.Error, v.Progress)
	}
}

func TestCancelStopsTheRun(t *testing.T) {
	store := verifytest.NewMemStore()
	lib := verifytest.NewLibrary("library", books(50, 4, "abcd")...)
	lib.SetExportDelay(10 * time.Millisecond)
	r, stop := serve(t, store, lib)
	id := start(t, r, verify.Scope{}).ID
	waitFor(t, "5 books checked", func() bool { return view(t, r, id).Progress.Checked >= 5 })
	if err := r.Cancel(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	v := waitTerminal(t, r, id)
	if v.Run.Status != verify.RunCancelled || v.Progress.Pending == 0 {
		t.Fatalf("run ended %s with %+v", v.Run.Status, v.Progress)
	}
	stop()

	r2, _ := serve(t, store, lib)
	time.Sleep(50 * time.Millisecond)
	if v2 := view(t, r2, id); v2.Run.Status != verify.RunCancelled || v2.Progress != v.Progress {
		t.Errorf("a new runner touched the cancelled run: %s %+v", v2.Run.Status, v2.Progress)
	}
	if err := r2.Cancel(context.Background(), 999); !errors.Is(err, verify.ErrNoSuchRun) {
		t.Errorf("Cancel(999) = %v, want ErrNoSuchRun", err)
	}
}

func TestCancelThroughAnotherInstance(t *testing.T) {
	verify.SetCancelPoll(t, 10*time.Millisecond)
	store := verifytest.NewMemStore()
	lib := verifytest.NewLibrary("library", books(3, 4, "abcd")...)
	lib.SetExportDelay(time.Hour)
	r1, _ := serve(t, store, lib)
	id := start(t, r1, verify.Scope{}).ID
	// r2 serves the web UI of a second process: it never gets the lock.
	r2, _ := serve(t, store, lib)
	if err := r2.Cancel(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if v := waitTerminal(t, r1, id); v.Run.Status != verify.RunCancelled {
		t.Fatalf("run ended %s", v.Run.Status)
	}
}

func TestResumeAfterRestart(t *testing.T) {
	store := verifytest.NewMemStore()
	lib := verifytest.NewLibrary("library", books(12, 4, "abcd")...)
	lib.SetExportDelay(10 * time.Millisecond)
	r1, stop1 := serve(t, store, lib)
	id := start(t, r1, verify.Scope{}).ID
	waitFor(t, "4 books checked", func() bool { return view(t, r1, id).Progress.Checked >= 4 })
	stop1()
	var done []int
	for bookID, st := range store.BookStates(id) {
		if st == verify.BookChecked {
			done = append(done, bookID)
		}
	}
	if v := view(t, r1, id); v.Run.Status.Terminal() || v.Progress.Pending == 0 {
		t.Fatalf("after the crash the run is %s with %+v", v.Run.Status, v.Progress)
	}

	r2, _ := serve(t, store, lib)
	v := waitTerminal(t, r2, id)
	if v.Run.Status != verify.RunCompleted || v.Progress != (verify.Progress{Total: 12, Checked: 12}) {
		t.Fatalf("resumed run ended %s with %+v", v.Run.Status, v.Progress)
	}
	for _, bookID := range done {
		if n := lib.ExportCalls(bookID, "EPUB"); n != 1 {
			t.Errorf("book %d, finished before the crash, was exported %d times", bookID, n)
		}
	}
}

// Retention is the caller's business (ADR-039): the runner reports every run
// that ends through Finished and prunes nothing itself.
func TestRunnerLeavesRetentionToTheFinishedHook(t *testing.T) {
	lib := verifytest.NewLibrary("library", books(1, 4, "abcd")...)
	var (
		mu    sync.Mutex
		ended []int64
	)
	r, _ := serveWith(t, verifytest.NewMemStore(), lib, verify.Options{Finished: func(_ context.Context, id int64, _ verify.RunStatus) {
		mu.Lock()
		ended = append(ended, id)
		mu.Unlock()
	}})
	var last int64
	for range 12 {
		last = start(t, r, verify.Scope{}).ID
		waitTerminal(t, r, last)
	}
	runs, err := r.List(context.Background(), 100)
	if err != nil || len(runs) != 12 || runs[0].Run.ID != last || runs[0].Progress.Checked != 1 {
		t.Fatalf("List() = %d runs, %v; want all 12 kept", len(runs), err)
	}
	// The run turns terminal before Finished hears of it.
	waitFor(t, "12 hooks", func() bool { mu.Lock(); defer mu.Unlock(); return len(ended) >= 12 })
	mu.Lock()
	defer mu.Unlock()
	if len(ended) != 12 || ended[11] != last {
		t.Errorf("Finished heard of %v", ended)
	}
}

// A format that crashes Calibre (say, OOM on a huge export) must not make
// the run crash it again forever: after the third interruption with that
// book in hand, the book fails and the run moves on.
func TestBookThatBreaksCalibreFails(t *testing.T) {
	store := verifytest.NewMemStore()
	lib := verifytest.NewLibrary("library", books(10, 4, "abcd")...)
	var attempts atomic.Int32
	lib.SetExportError(func(id int, _ string) error {
		if id == 3 {
			attempts.Add(1)
			return fmt.Errorf("fake: export: %w", library.ErrUnreachable)
		}
		return nil
	})
	r, _ := serve(t, store, lib)
	id := start(t, r, verify.Scope{}).ID
	v := waitTerminal(t, r, id)
	if v.Run.Status != verify.RunCompleted || v.Progress != (verify.Progress{Total: 10, Checked: 9, Failed: 1}) {
		t.Fatalf("run ended %s with %+v", v.Run.Status, v.Progress)
	}
	if st, reason := store.BookStates(id)[3], store.BookReasons(id)[3]; st != verify.BookFailed || reason != "calibre_unreachable" {
		t.Errorf("book 3 = %s/%q, want failed/calibre_unreachable", st, reason)
	}
	if n := attempts.Load(); n != 3 {
		t.Errorf("book 3 was exported %d times, want 3", n)
	}
}

// A runner whose scratch directory can't be used must say so at startup,
// and never create runs that nothing will execute.
func TestRunnerWithoutScratchRefusesToRun(t *testing.T) {
	ctx := context.Background()
	file := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	store := verifytest.NewMemStore()
	r := verify.NewRunner(store, verifytest.NewLibrary("library", books(1, 4, "abcd")...),
		verify.Options{ScratchDir: filepath.Join(file, "scratch")})
	if err := r.Prepare(); err == nil {
		t.Fatal("Prepare() with an unusable scratch directory succeeded")
	}
	if err := r.Serve(ctx); err == nil {
		t.Fatal("Serve() with an unusable scratch directory returned nil")
	}
	if _, err := r.Start(ctx, verify.Scope{}); !errors.Is(err, verify.ErrRunnerStopped) {
		t.Errorf("Start() after Serve failed = %v, want ErrRunnerStopped", err)
	}
	if _, ok, _ := store.ActiveRun(ctx); ok {
		t.Error("a run was created that nothing will execute")
	}
}

func TestPrepareCreatesTheScratchDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "scratch")
	r := verify.NewRunner(verifytest.NewMemStore(), verifytest.NewLibrary("library"), verify.Options{ScratchDir: dir})
	if err := r.Prepare(); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Fatalf("scratch directory: %v, %v", fi, err)
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Errorf("Prepare() left %d entries behind", len(left))
	}
}

func TestServeWarnsWhenCalibreIsNotReady(t *testing.T) {
	var logs syncBuffer
	lib := verifytest.NewLibrary("library")
	lib.SetCheckError(fmt.Errorf("probe: %w", library.ErrUnreachable))
	r := verify.NewRunner(verifytest.NewMemStore(), lib, verify.Options{
		ScratchDir: t.TempDir(),
		Logger:     slog.New(slog.NewJSONHandler(&logs, nil)),
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Serve(ctx) }()
	waitFor(t, "the startup probe", func() bool { return r.Calibre().State != "unknown" })
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	out := logs.String()
	if !strings.Contains(out, `"level":"WARN"`) || !strings.Contains(out, `"state":"unreachable"`) {
		t.Errorf("no WARN with the Calibre state in:\n%s", out)
	}
}

// syncBuffer is a bytes.Buffer safe for a logger and a reader.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestNewRunnerRejectsBadOptions(t *testing.T) {
	for _, opts := range []verify.Options{
		{Workers: -1},
		{Limits: verify.Limits{FileSize: 2 << 30, InFlight: 1 << 30}},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("NewRunner(%+v) didn't panic", opts)
				}
			}()
			verify.NewRunner(verifytest.NewMemStore(), verifytest.NewLibrary("library"), opts)
		}()
	}
}

// TestRunnerFinishedHook: Finished hears of every run that ends, once, with
// how it ended.
func TestRunnerFinishedHook(t *testing.T) {
	var mu sync.Mutex
	ended := map[int64][]verify.RunStatus{}
	hook := func(_ context.Context, id int64, status verify.RunStatus) {
		mu.Lock()
		defer mu.Unlock()
		ended[id] = append(ended[id], status)
	}
	lib := verifytest.NewLibrary("library", books(30, 4, "abcd")...)
	r, _ := serveWith(t, verifytest.NewMemStore(), lib, verify.Options{Finished: hook})
	done := start(t, r, verify.Scope{}).ID
	waitTerminal(t, r, done)
	lib.SetExportDelay(10 * time.Millisecond)
	cancelled := start(t, r, verify.Scope{}).ID
	waitFor(t, "a book checked", func() bool { return view(t, r, cancelled).Progress.Checked >= 1 })
	if err := r.Cancel(context.Background(), cancelled); err != nil {
		t.Fatal(err)
	}
	waitTerminal(t, r, cancelled)
	waitFor(t, "both hooks", func() bool { mu.Lock(); defer mu.Unlock(); return len(ended) == 2 })
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(ended[done], []verify.RunStatus{verify.RunCompleted}) || !slices.Equal(ended[cancelled], []verify.RunStatus{verify.RunCancelled}) {
		t.Fatalf("hook calls = %v", ended)
	}
}
