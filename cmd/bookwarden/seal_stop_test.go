package main

import (
	"bytes"
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/report"
	"github.com/felixapel/calibre-bookwarden/internal/store"
	"github.com/felixapel/calibre-bookwarden/internal/testutil/pgtest"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

// TestSealCheckStopsAtDrain: serve hands sealOnFinish the stop context, so a
// run that ends while the process drains starts no seal check and holds the
// stop for none.
func TestSealCheckStopsAtDrain(t *testing.T) {
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
	stopCtx, drain := context.WithCancel(ctx)
	drain()
	var wg sync.WaitGroup
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	sealOnFinish(stopCtx, &wg, report.New(store.NewReportRepo(st)), logger, nil)(ctx, r.ID, verify.RunCompleted)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the seal check held the stop")
	}
	var n int
	if err := st.Pool.QueryRow(ctx, `SELECT count(*) FROM seal_checks`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d seal checks after the drain, %v", n, err)
	}
}
