package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/testutil/pgtest"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
	"github.com/felixapel/calibre-bookwarden/internal/verify/verifytest"
)

var _ verify.RunStore = (*RunRepo)(nil)

func runRepo(t *testing.T) (*RunRepo, *Store) {
	t.Helper()
	s := openTest(t)
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return NewRunRepo(s), s
}

func refs(n int) []library.BookRef {
	out := make([]library.BookRef, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, library.BookRef{ID: i * 10, UUID: fmt.Sprintf("uuid-%d", i)})
	}
	return out
}

func sealed(t *testing.T, bookID int, uuid string) verify.Outcome {
	t.Helper()
	raw, seal, err := evidence.Seal(evidence.Package{
		SchemaVersion: evidence.SchemaVersion, LibraryID: "library", BookID: bookID, UUID: uuid,
		Snapshot: "s", Calibre: evidence.RecordFrom(library.Book{Title: "Binary Tides"}),
		Formats: []evidence.FormatEvidence{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return verify.Outcome{State: verify.BookChecked, Evidence: raw, Seal: seal}
}

func TestCreateRunFreezesBooks(t *testing.T) {
	ctx := context.Background()
	r, _ := runRepo(t)
	books := []library.BookRef{{ID: 30, UUID: "c"}, {ID: 10, UUID: "a"}, {ID: 20, UUID: "b"}}
	run, err := r.CreateRun(ctx, verify.Scope{}, "library", "v1", books)
	if err != nil {
		t.Fatal(err)
	}
	if run.ID == 0 || run.Status != verify.RunRunning || run.LibraryID != "library" || run.AppVersion != "v1" || run.StartedAt.IsZero() {
		t.Errorf("CreateRun() = %+v", run)
	}
	pending, err := r.PendingBooks(ctx, run.ID)
	if err != nil || fmt.Sprint(pending) != fmt.Sprint(books) {
		t.Errorf("PendingBooks() = %v, %v; want %v in frozen order", pending, err, books)
	}
	if p, err := r.Progress(ctx, run.ID); err != nil || p != (verify.Progress{Total: 3, Pending: 3}) {
		t.Errorf("Progress() = %+v, %v", p, err)
	}
	got, err := r.Run(ctx, run.ID)
	if err != nil || got.Scope != (verify.Scope{}) || got.ID != run.ID {
		t.Errorf("Run() = %+v, %v", got, err)
	}
	active, ok, err := r.ActiveRun(ctx)
	if err != nil || !ok || active.ID != run.ID {
		t.Errorf("ActiveRun() = %+v, %v, %v", active, ok, err)
	}

	if err := r.SetStatus(ctx, run.ID, verify.RunCompleted, ""); err != nil {
		t.Fatal(err)
	}
	one, err := r.CreateRun(ctx, verify.Scope{BookID: 20}, "library", "v1", books[2:])
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := r.Run(ctx, one.ID); got.Scope.BookID != 20 {
		t.Errorf("scope = %+v, want book 20", got.Scope)
	}

	for name, err := range map[string]error{
		"Run":           func() error { _, err := r.Run(ctx, 999); return err }(),
		"PendingBooks":  func() error { _, err := r.PendingBooks(ctx, 999); return err }(),
		"Progress":      func() error { _, err := r.Progress(ctx, 999); return err }(),
		"SetStatus":     r.SetStatus(ctx, 999, verify.RunFailed, ""),
		"RequestCancel": r.RequestCancel(ctx, 999),
	} {
		if !errors.Is(err, verify.ErrNoSuchRun) {
			t.Errorf("%s(999) = %v, want ErrNoSuchRun", name, err)
		}
	}
}

func TestOneActiveRun(t *testing.T) {
	ctx := context.Background()
	r, _ := runRepo(t)
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		created []verify.Run
		refused int
	)
	for range 8 {
		wg.Go(func() {
			run, err := r.CreateRun(ctx, verify.Scope{}, "library", "v1", refs(50))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				created = append(created, run)
			case errors.Is(err, verify.ErrRunActive):
				refused++
			default:
				t.Errorf("CreateRun() = %v", err)
			}
		})
	}
	wg.Wait()
	if len(created) != 1 || refused != 7 {
		t.Fatalf("%d runs created, %d refused; want 1 and 7", len(created), refused)
	}
	if err := r.SetStatus(ctx, created[0].ID, verify.RunWaiting, "Calibre is unreachable"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CreateRun(ctx, verify.Scope{}, "library", "v1", nil); !errors.Is(err, verify.ErrRunActive) {
		t.Errorf("CreateRun() while a run waits = %v, want ErrRunActive", err)
	}
	if err := r.SetStatus(ctx, created[0].ID, verify.RunCompleted, ""); err != nil {
		t.Fatal(err)
	}
	got, err := r.Run(ctx, created[0].ID)
	if err != nil || got.FinishedAt.IsZero() || got.Error != "" {
		t.Errorf("completed run = %+v, %v", got, err)
	}
	if _, err := r.CreateRun(ctx, verify.Scope{}, "library", "v1", nil); err != nil {
		t.Errorf("CreateRun() after completion = %v", err)
	}
}

func TestFinishBook(t *testing.T) {
	ctx := context.Background()
	r, s := runRepo(t)
	books := refs(3)
	run, err := r.CreateRun(ctx, verify.Scope{}, "library", "v1", books)
	if err != nil {
		t.Fatal(err)
	}
	out := sealed(t, books[0].ID, books[0].UUID)
	if err := r.FinishBook(ctx, run.ID, books[0], out); err != nil {
		t.Fatal(err)
	}
	raw, seal, err := r.Evidence(ctx, run.ID, books[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if p, err := evidence.Open(raw, seal); err != nil || p.BookID != books[0].ID {
		t.Errorf("stored evidence opens as %+v, %v", p, err)
	}
	if err := r.FinishBook(ctx, run.ID, books[0], out); !errors.Is(err, verify.ErrBookFinished) {
		t.Errorf("second FinishBook() = %v, want ErrBookFinished", err)
	}
	if err := r.FinishBook(ctx, run.ID, books[1], verify.Outcome{State: verify.BookSourceChanged, Reason: "changed"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.Evidence(ctx, run.ID, books[1].ID); !errors.Is(err, verify.ErrNoSuchRun) {
		t.Errorf("a source_changed book has evidence: %v", err)
	}
	if err := r.FinishBook(ctx, run.ID, books[2], verify.Outcome{State: verify.BookFailed, Reason: "calibre_error"}); err != nil {
		t.Fatal(err)
	}
	var reason string
	if err := s.Pool.QueryRow(ctx, "SELECT reason FROM run_books WHERE run_id = $1 AND book_id = $2", run.ID, books[2].ID).Scan(&reason); err != nil || reason != "calibre_error" {
		t.Errorf("failed book reason = %q, %v", reason, err)
	}
	if p, err := r.Progress(ctx, run.ID); err != nil || p != (verify.Progress{Total: 3, Checked: 1, SourceChanged: 1, Failed: 1}) {
		t.Errorf("Progress() = %+v, %v", p, err)
	}
	if err := r.FinishBook(ctx, run.ID, library.BookRef{ID: 999}, out); !errors.Is(err, verify.ErrBookFinished) {
		t.Errorf("FinishBook() for a book outside the run = %v, want ErrBookFinished", err)
	}
}

func TestEvidenceTamperIsDetected(t *testing.T) {
	ctx := context.Background()
	r, s := runRepo(t)
	books := refs(1)
	run, err := r.CreateRun(ctx, verify.Scope{}, "library", "v1", books)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.FinishBook(ctx, run.ID, books[0], sealed(t, books[0].ID, books[0].UUID)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE evidence SET package = jsonb_set(package, '{calibre_record,title}', '"x"')`); err != nil {
		t.Fatal(err)
	}
	raw, seal, err := r.Evidence(ctx, run.ID, books[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := evidence.Open(raw, seal); !errors.Is(err, evidence.ErrSealBroken) {
		t.Errorf("Open() of tampered evidence = %v, want ErrSealBroken", err)
	}
}

func TestPruneRuns(t *testing.T) {
	ctx := context.Background()
	r, s := runRepo(t)
	var first int64
	for i := range 12 {
		run, err := r.CreateRun(ctx, verify.Scope{}, "library", "v1", refs(1))
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = run.ID
			if err := r.FinishBook(ctx, run.ID, refs(1)[0], sealed(t, 10, "uuid-1")); err != nil {
				t.Fatal(err)
			}
		}
		if err := r.SetStatus(ctx, run.ID, verify.RunCompleted, ""); err != nil {
			t.Fatal(err)
		}
	}
	active, err := r.CreateRun(ctx, verify.Scope{}, "library", "v1", refs(1))
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Prune(ctx, 10)
	n := res.Runs
	if err != nil || n != 3 {
		t.Fatalf("Prune(10) = %d, %v; want 3 (13 runs, the active one among the newest 10)", n, err)
	}
	runs, err := r.ListRuns(ctx, 100)
	if err != nil || len(runs) != 10 || runs[0].ID != active.ID {
		t.Fatalf("ListRuns() = %d runs, newest %v, %v", len(runs), runs, err)
	}
	if _, err := r.Run(ctx, first); !errors.Is(err, verify.ErrNoSuchRun) {
		t.Errorf("the oldest run survived: %v", err)
	}
	var left int
	if err := s.Pool.QueryRow(ctx, "SELECT count(*) FROM evidence WHERE run_id = $1", first).Scan(&left); err != nil || left != 0 {
		t.Errorf("pruned run left %d evidence rows, %v", left, err)
	}

	// The active run is never pruned, even when it isn't among the newest.
	if res, err := r.pruneAll(ctx); err != nil || res.Runs != 9 {
		t.Errorf("pruneAll() = %+v, %v; want 9", res, err)
	}
	if _, ok, err := r.ActiveRun(ctx); err != nil || !ok {
		t.Errorf("the active run was pruned: %v", err)
	}
}

func TestRequestCancel(t *testing.T) {
	ctx := context.Background()
	r, _ := runRepo(t)
	run, err := r.CreateRun(ctx, verify.Scope{}, "library", "v1", refs(1))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.RequestCancel(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.Run(ctx, run.ID); !got.CancelRequested {
		t.Error("cancel flag not set")
	}
	if err := r.SetStatus(ctx, run.ID, verify.RunCancelled, ""); err != nil {
		t.Fatal(err)
	}
	if err := r.RequestCancel(ctx, run.ID); err != nil {
		t.Errorf("RequestCancel() on an ended run = %v, want a no-op", err)
	}
}

func TestOnlyOneInstanceRunsChecks(t *testing.T) {
	ctx := context.Background()
	url := pgtest.URL(t)
	open := func() *RunRepo {
		s, err := Open(ctx, url)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(s.Close)
		if err := s.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		return NewRunRepo(s)
	}
	a, b := open(), open()
	release, ok, err := a.LockRunner(ctx)
	if err != nil || !ok {
		t.Fatalf("first LockRunner() = %v, %v", ok, err)
	}
	if _, ok, err := b.LockRunner(ctx); err != nil || ok {
		t.Fatalf("second LockRunner() while held = %v, %v; want false", ok, err)
	}
	release()
	release() // a second call is harmless
	releaseB, ok, err := b.LockRunner(ctx)
	if err != nil || !ok {
		t.Fatalf("LockRunner() after release = %v, %v", ok, err)
	}
	releaseB()
}

func TestRunnerResumesOnPostgres(t *testing.T) {
	ctx := context.Background()
	repo, s := runRepo(t)
	var fakes []verifytest.FakeBook
	for i := 1; i <= 12; i++ {
		fakes = append(fakes, verifytest.FakeBook{
			Book: library.Book{
				ID: i, UUID: fmt.Sprintf("uuid-%d", i), Title: "Book",
				Formats: []library.Format{{Name: "EPUB", Size: 4}},
			},
			Files: map[string][]byte{"EPUB": []byte("abcd")},
		})
	}
	lib := verifytest.NewLibrary("library", fakes...)
	lib.SetExportDelay(10 * time.Millisecond)

	serve := func() (*verify.Runner, func()) {
		r := verify.NewRunner(repo, lib, verify.Options{ScratchDir: t.TempDir(), Backoff: func(int) time.Duration { return time.Millisecond }})
		sctx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- r.Serve(sctx) }()
		var once sync.Once
		stop := func() {
			once.Do(func() {
				cancel()
				if err := <-done; err != nil {
					t.Errorf("Serve() = %v", err)
				}
			})
		}
		t.Cleanup(stop)
		return r, stop
	}
	waitFor := func(what string, cond func() bool) {
		deadline := time.Now().Add(20 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}

	r1, stop1 := serve()
	run, err := r1.Start(ctx, verify.Scope{})
	if err != nil {
		t.Fatal(err)
	}
	waitFor("4 books checked", func() bool {
		p, err := repo.Progress(ctx, run.ID)
		return err == nil && p.Checked >= 4
	})
	stop1()
	var finished []int
	rows, err := s.Pool.Query(ctx, "SELECT book_id FROM run_books WHERE run_id = $1 AND state = 'checked'", run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		finished = append(finished, id)
	}
	rows.Close()

	r2, _ := serve()
	waitFor("the resumed run to end", func() bool {
		v, err := r2.View(ctx, run.ID)
		return err == nil && v.Run.Status.Terminal()
	})
	v, err := r2.View(ctx, run.ID)
	if err != nil || v.Run.Status != verify.RunCompleted || v.Progress != (verify.Progress{Total: 12, Checked: 12}) {
		t.Fatalf("resumed run = %+v, %v", v, err)
	}
	for _, id := range finished {
		if n := lib.ExportCalls(id, "EPUB"); n != 1 {
			t.Errorf("book %d, checked before the restart, was exported %d times", id, n)
		}
	}
	var count int
	if err := s.Pool.QueryRow(ctx, "SELECT count(*) FROM evidence WHERE run_id = $1", run.ID).Scan(&count); err != nil || count != 12 {
		t.Errorf("evidence rows = %d, %v; want 12", count, err)
	}
}
