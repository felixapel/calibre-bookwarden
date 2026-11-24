//go:build calibre

package agent

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/fix"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/store"
	"github.com/felixapel/calibre-bookwarden/internal/testutil/pgtest"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

// TestAgentFixingOnRealCalibre: on the disposable Calibre in test/calibre,
// a check of book 8 completes, and a shadow review records verdicts, decides
// nothing and writes nothing, even though the whole panel approves. Mode on
// is refused. The judges are fakes; scripts/integration-test.sh runs it
// after the fix tests.
func TestAgentFixingOnRealCalibre(t *testing.T) {
	ctx := context.Background()
	get := func(name string) string {
		v := os.Getenv(name)
		if v == "" {
			t.Skipf("%s is not set; run scripts/integration-test.sh", name)
		}
		return v
	}
	base, err := url.Parse(get("BW_IT_CALIBRE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	w := library.New(library.Config{BaseURL: base, User: "writer", Password: get("BW_IT_WRITER_PASSWORD")})
	lib, err := w.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	read := func() library.Book {
		t.Helper()
		b, err := w.BookInLibrary(ctx, lib, 8)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	sortOf := func() string {
		if s := read().TitleSort; s != nil {
			return *s
		}
		return ""
	}
	before := sortOf()
	if before != "Draft Ledger" {
		t.Fatalf("book 8's planted sort is %q", before)
	}

	st, err := store.Open(ctx, pgtest.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	runs, fixes, agents := store.NewRunRepo(st), store.NewFixRepo(st), store.NewAgentRepo(st)
	fixer := fix.New(fixes, runs, w, nil)
	seats := panel(nil)
	ss := make([]Seat, len(seats))
	for i, s := range seats {
		ss[i] = s
	}
	reviewer := NewReviewer(Options{Store: agents, Evidence: runs, Fixes: fixes, Seats: ss, MaxTokens: 1_000_000})
	svc := NewService(ServiceOptions{Reviewer: reviewer, Store: agents, Decisions: fixes, Seats: allSeats, Retry: 50 * time.Millisecond})
	runner := verify.NewRunner(runs, w, verify.Options{ScratchDir: t.TempDir(), Finished: svc.RunFinished})
	sctx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	for _, serve := range []func(context.Context) error{runner.Serve, fixer.Serve, svc.Serve} {
		go func() { _ = serve(sctx) }()
	}
	waitFor := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Minute)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	check := func() int64 {
		t.Helper()
		run, err := runner.Start(ctx, verify.Scope{BookID: 8, ExpectedLibraryID: lib, ExpectedUUID: read().UUID})
		if err != nil {
			t.Fatal(err)
		}
		waitFor("the check", func() bool {
			v, err := runner.View(ctx, run.ID)
			return err == nil && v.Run.Status == verify.RunCompleted
		})
		return run.ID
	}
	reviewed := func(run int64) {
		t.Helper()
		waitFor("the review", func() bool {
			rs, err := agents.Reviews(ctx, run)
			return err == nil && len(rs) == 1 && rs[0].State == store.ReviewDone
		})
	}
	if err := fixes.SetWritesEnabled(ctx, true, "felix"); err != nil {
		t.Fatal(err)
	}

	// Shadow: verdicts, no decision, nothing written.
	if err := svc.SetMode(ctx, store.ModeShadow, "felix"); err != nil {
		t.Fatal(err)
	}
	shadow := check()
	reviewed(shadow)
	if err := svc.SetMode(ctx, store.ModeOn, "felix"); err == nil {
		t.Fatal("agent fixing turned on after a shadow review")
	}
	if rows, _ := fixes.Proposals(ctx, shadow, 8); len(rows) != 0 || sortOf() != before {
		t.Fatalf("shadow decided %+v or wrote %q", rows, sortOf())
	}

	for _, s := range seats {
		for _, p := range s.packets {
			if strings.Contains(p, `"text"`) || strings.Contains(p, "isbn_hints") {
				t.Fatalf("%s got file text or OCR hints", s.name)
			}
		}
	}
}
