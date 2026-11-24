package main

import (
	"bytes"
	"context"
	"log/slog"
	"sync"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/report"
	"github.com/felixapel/calibre-bookwarden/internal/store"
	"github.com/felixapel/calibre-bookwarden/internal/testutil/pgtest"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

// TestReviewIsQueuedAfterTheSealCheck: the check carries a person's earlier
// rejections into the run, so the agent panel's review is queued only after it
// has returned (ADR-034, carried decisions). Otherwise the panel could decide
// a fix a person already rejected. A failed check doesn't hold the review back.
func TestReviewIsQueuedAfterTheSealCheck(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, pgtest.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	runs := store.NewRunRepo(st)
	r, err := runs.CreateRun(ctx, verify.Scope{}, "library", "v1", []library.BookRef{{ID: 1, UUID: "u1"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := runs.SetStatus(ctx, r.ID, verify.RunCompleted, ""); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	checked := func(run int64) bool {
		var n int
		if err := st.Pool.QueryRow(ctx, `SELECT count(*) FROM seal_checks WHERE run_id = $1`, run).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n == 1
	}
	for _, tc := range []struct {
		name        string
		run         int64
		status      verify.RunStatus
		wantChecked bool // the check had finished when the review was queued
		wantReview  bool
	}{
		{"completed", r.ID, verify.RunCompleted, true, true},
		{"the check fails (pruned run)", 99999, verify.RunCompleted, false, true},
		{"still going", r.ID, verify.RunWaiting, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := st.Pool.Exec(ctx, `DELETE FROM seal_checks`); err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			var reviewed []bool
			review := func(_ context.Context, run int64, status verify.RunStatus) {
				mu.Lock()
				defer mu.Unlock()
				if status != tc.status || run != tc.run {
					t.Errorf("review of run %d status %v", run, status)
				}
				reviewed = append(reviewed, checked(run))
			}
			var wg sync.WaitGroup
			sealOnFinish(ctx, &wg, report.New(store.NewReportRepo(st)), logger, review)(ctx, tc.run, tc.status)
			wg.Wait()
			if tc.wantReview != (len(reviewed) == 1) {
				t.Fatalf("reviews = %v, want a review: %v", reviewed, tc.wantReview)
			}
			if tc.wantReview && reviewed[0] != tc.wantChecked {
				t.Fatalf("the check had finished when the review was queued: %v, want %v", reviewed[0], tc.wantChecked)
			}
		})
	}
}
