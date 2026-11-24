package store

import (
	"context"
	"slices"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

func inHand(t *testing.T, s *Store, runID int64, bookID int) (bool, int) {
	t.Helper()
	var held bool
	var crashes int
	if err := s.Pool.QueryRow(context.Background(), `SELECT in_hand_since IS NOT NULL, crashes FROM run_books WHERE run_id = $1 AND book_id = $2`, runID, bookID).Scan(&held, &crashes); err != nil {
		t.Fatal(err)
	}
	return held, crashes
}

// TestBookInHand: taking a book marks it in hand, finishing or releasing it
// clears the mark.
func TestBookInHand(t *testing.T) {
	ctx := context.Background()
	r, s := runRepo(t)
	books := refs(3)
	run, err := r.CreateRun(ctx, verify.Scope{}, "library", "v1", books)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range books {
		if err := r.TakeBook(ctx, run.ID, b.ID); err != nil {
			t.Fatal(err)
		}
		if held, _ := inHand(t, s, run.ID, b.ID); !held {
			t.Fatalf("book %d isn't in hand after TakeBook", b.ID)
		}
	}
	if err := r.FinishBook(ctx, run.ID, books[0], verify.Outcome{State: verify.BookFailed, Reason: "x"}); err != nil {
		t.Fatal(err)
	}
	if held, _ := inHand(t, s, run.ID, books[0].ID); held {
		t.Error("a finished book is still in hand")
	}
	if err := r.ReleaseBooks(ctx, run.ID, []int{books[1].ID}); err != nil {
		t.Fatal(err)
	}
	if held, crashes := inHand(t, s, run.ID, books[1].ID); held || crashes != 0 {
		t.Errorf("a released book: in hand %v, crashes %d; want false, 0", held, crashes)
	}
	if held, _ := inHand(t, s, run.ID, books[2].ID); !held {
		t.Error("releasing one book released another")
	}
}

// TestCrashedBooksAreCountedOnce: recovery counts a crash for each book in
// hand, once, and gives up a book at its second crash.
func TestCrashedBooksAreCountedOnce(t *testing.T) {
	ctx := context.Background()
	r, s := runRepo(t)
	books := refs(2)
	run, err := r.CreateRun(ctx, verify.Scope{}, "library", "v1", books)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.TakeBook(ctx, run.ID, books[0].ID); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if gaveUp, crashed, err := r.RecoverCrashed(ctx, run.ID); err != nil || gaveUp != 0 || !slices.Equal(crashed, []int{books[0].ID}) {
			t.Fatalf("RecoverCrashed = %d, %v, %v; want 0, [%d]", gaveUp, crashed, err, books[0].ID)
		}
	}
	if held, crashes := inHand(t, s, run.ID, books[0].ID); held || crashes != 1 {
		t.Fatalf("after one crash and two recoveries: in hand %v, crashes %d; want false, 1", held, crashes)
	}
	if _, crashes := inHand(t, s, run.ID, books[1].ID); crashes != 0 {
		t.Fatalf("a book never in hand has %d crashes", crashes)
	}
	if err := r.TakeBook(ctx, run.ID, books[0].ID); err != nil {
		t.Fatal(err)
	}
	if gaveUp, crashed, err := r.RecoverCrashed(ctx, run.ID); err != nil || gaveUp != 1 || len(crashed) != 0 {
		t.Fatalf("RecoverCrashed after the second crash = %d, %v, %v; want 1, none", gaveUp, crashed, err)
	}
	pending, err := r.PendingBooks(ctx, run.ID)
	if err != nil || len(pending) != 1 || pending[0].ID != books[1].ID {
		t.Fatalf("pending = %v, %v; want only book %d", pending, err, books[1].ID)
	}
	var state, reason string
	if err := s.Pool.QueryRow(ctx, `SELECT state, reason FROM run_books WHERE run_id = $1 AND book_id = $2`, run.ID, books[0].ID).Scan(&state, &reason); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || reason != "process_crashed" {
		t.Errorf("the given-up book = %s/%s, want failed/process_crashed", state, reason)
	}
}
