package main

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/store"
	"github.com/felixapel/calibre-bookwarden/internal/testutil/pgtest"
)

func TestLifecycleDefaults(t *testing.T) {
	l := newLifecycle(25*time.Second, nil)
	if l.hardWait != 2*time.Second {
		t.Errorf("hardWait = %v, want 2s: 25s grace + hardWait must stay clear of Docker's 30s", l.hardWait)
	}
}

// TestStopIsBoundedWhileAWorkerHoldsAConnection: a worker that ignores the
// hard cancel and holds a pooled connection would block pgxpool's Close for
// as long as it lives (the writer's write timeout is 2 minutes). The stop
// must still return within grace + hardWait, and leave the store open.
func TestStopIsBoundedWhileAWorkerHoldsAConnection(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, pgtest.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	held := make(chan struct{})
	t.Cleanup(func() { close(release); st.Close() })

	l, logs := newTestLifecycle(100 * time.Millisecond)
	l.hardWait = 100 * time.Millisecond
	l.Go(func() {
		conn, err := st.Pool.Acquire(ctx)
		if err != nil {
			return
		}
		defer conn.Release()
		close(held)
		<-release // ignores every cancellation
	})
	<-held

	start := time.Now()
	done := make(chan struct{})
	go func() {
		l.Stop()
		closeStore(l, st, slogOf(logs))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("stop plus store close did not return within grace + hardWait + margin")
	}
	if took := time.Since(start); took > time.Second {
		t.Errorf("took %v", took)
	}
	if !l.Abandoned() {
		t.Error("Abandoned() = false after giving up on a worker")
	}
	if !strings.Contains(logs.String(), "store left open") {
		t.Errorf("the open store was not logged:\n%s", logs.String())
	}
}

func TestCloseStoreClosesWhenNothingWasAbandoned(t *testing.T) {
	st, err := store.Open(context.Background(), pgtest.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	l, logs := newTestLifecycle(time.Second)
	l.Go(func() {})
	l.Stop()
	closeStore(l, st, slogOf(logs))
	if l.Abandoned() || strings.Contains(logs.String(), "store left open") {
		t.Errorf("abandoned although the workers finished:\n%s", logs.String())
	}
	if err := st.Ping(context.Background()); err == nil {
		t.Error("the store is still open")
	}
}

func slogOf(w *syncLog) *slog.Logger { return slog.New(slog.NewJSONHandler(w, nil)) }
