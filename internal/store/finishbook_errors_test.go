package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

// TestFinishBookClassifiesErrors: an error that means the database is away
// or busy wraps verify.ErrStoreUnavailable, so the runner waits and resumes;
// any other error is the book's own and is returned as it is.
func TestFinishBookClassifiesErrors(t *testing.T) {
	ctx := context.Background()
	t.Run("a cancelled context", func(t *testing.T) {
		r, _ := runRepo(t)
		run, err := r.CreateRun(ctx, verify.Scope{}, "library", "v1", refs(1))
		if err != nil {
			t.Fatal(err)
		}
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		err = r.FinishBook(cctx, run.ID, refs(1)[0], verify.Outcome{State: verify.BookFailed, Reason: "x"})
		if !errors.Is(err, verify.ErrStoreUnavailable) {
			t.Errorf("FinishBook() = %v, want ErrStoreUnavailable", err)
		}
	})
	t.Run("a closed pool", func(t *testing.T) {
		r, s := runRepo(t)
		run, err := r.CreateRun(ctx, verify.Scope{}, "library", "v1", refs(1))
		if err != nil {
			t.Fatal(err)
		}
		s.Pool.Close()
		err = r.FinishBook(ctx, run.ID, refs(1)[0], verify.Outcome{State: verify.BookFailed, Reason: "x"})
		if !errors.Is(err, verify.ErrStoreUnavailable) {
			t.Errorf("FinishBook() = %v, want ErrStoreUnavailable", err)
		}
	})
	t.Run("a CHECK violation", func(t *testing.T) {
		r, s := runRepo(t)
		run, err := r.CreateRun(ctx, verify.Scope{}, "library", "v1", refs(1))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Pool.Exec(ctx, `ALTER TABLE run_books ADD CONSTRAINT test_reason CHECK (reason <> 'refused')`); err != nil {
			t.Fatal(err)
		}
		err = r.FinishBook(ctx, run.ID, refs(1)[0], verify.Outcome{State: verify.BookFailed, Reason: "refused"})
		if err == nil || errors.Is(err, verify.ErrStoreUnavailable) {
			t.Errorf("FinishBook() = %v, want a plain error", err)
		}
	})
}

func TestStoreUnavailable(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"connection failure", &pgconn.PgError{Code: "08006"}, true},
		{"too many connections", &pgconn.PgError{Code: "53300"}, true},
		{"admin shutdown", &pgconn.PgError{Code: "57P01"}, true},
		{"serialization failure", &pgconn.PgError{Code: "40001"}, true},
		{"deadlock", &pgconn.PgError{Code: "40P01"}, true},
		{"check violation", &pgconn.PgError{Code: "23514"}, false},
		{"unique violation", &pgconn.PgError{Code: "23505"}, false},
		{"connect error", &pgconn.ConnectError{}, true},
		{"network error", fmt.Errorf("wrapped: %w", &net.OpError{Op: "read", Err: errors.New("reset")}), true},
		{"deadline", fmt.Errorf("wrapped: %w", context.DeadlineExceeded), true},
		{"dropped connection", fmt.Errorf("store: lock book result: %w", io.ErrUnexpectedEOF), true},
		{"closed connection", fmt.Errorf("wrapped: %w", pgconn.ErrConnClosed), true},
		{"plain", errors.New("bad evidence"), false},
		{"book finished", fmt.Errorf("store: %w", verify.ErrBookFinished), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := storeUnavailable(tt.err); got != tt.want {
				t.Errorf("storeUnavailable(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
