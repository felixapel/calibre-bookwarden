package fix

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/store"
)

func TestUnwritable(t *testing.T) {
	for _, tc := range []struct {
		p    evidence.Proposal
		want string
	}{
		{evidence.Proposal{Field: "pubdate", Current: []string{}, Proposed: []string{"2001-07-01"}}, "calibre_alters_value"},
		{evidence.Proposal{Field: "pubdate", Current: []string{"2001-07-31T22:00:00+00:00"}, Proposed: []string{"2001-07-26"}}, "calibre_cannot_restore"},
		{evidence.Proposal{Field: "authors", Current: []string{"Wrong Author"}, Proposed: []string{"A & B"}}, "calibre_alters_value"},
		{evidence.Proposal{Field: "title", Current: []string{"Old"}, Proposed: []string{"New"}}, ""},
		{evidence.Proposal{Field: "isbn", Current: []string{"9780000000026"}, Proposed: []string{isbn}}, ""},
		{evidence.Proposal{Field: "languages", Current: []string{}, Proposed: []string{"eng"}}, ""},
		{evidence.Proposal{Field: "series", Proposed: []string{"x"}}, "calibre_alters_value"},
		{evidence.Proposal{Field: "title_sort", Current: []string{"Innocent Captive"}, Proposed: []string{"Captive Mind, The"}}, ""},
		{evidence.Proposal{Field: "title_sort", Current: []string{"Innocent Captive"}, Proposed: []string{"Captive  Mind, The"}}, "calibre_alters_value"},
		{evidence.Proposal{Field: "title_sort", Current: []string{"Innocent Captive"}, Proposed: []string{""}}, "calibre_alters_value"},
		{evidence.Proposal{Field: "title_sort", Current: []string{}, Proposed: []string{"Captive Mind, The"}}, "calibre_cannot_restore"},
		{evidence.Proposal{Field: "author_sort", Current: []string{"Road, The"}, Proposed: []string{"McCarthy, Cormac"}}, ""},
		{evidence.Proposal{Field: "author_sort", Current: []string{}, Proposed: []string{"McCarthy, Cormac"}}, ""},
	} {
		if got := Unwritable(tc.p); got != tc.want {
			t.Errorf("Unwritable(%s %v → %v) = %q, want %q", tc.p.Field, tc.p.Current, tc.p.Proposed, got, tc.want)
		}
	}
}

func TestApproveRefusesUnwritable(t *testing.T) {
	ctx := context.Background()
	e := setup(t)
	run := e.checkV9(pkgFor(t, e.cal.book(2), sourceSays{date: "2001-07-01"})) // v10 no longer proposes it
	if err := e.svc.Approve(ctx, run, 2, []string{"pubdate"}, "admin", store.Basis{Mode: store.DecisionBulk}); !errors.Is(err, ErrBlocked) {
		t.Fatalf("approving an unwritable date = %v", err)
	}
	if rows, _ := e.fixes.Proposals(ctx, run, 2); len(rows) != 0 {
		t.Fatalf("stored %+v", rows)
	}
	e.approve(run, 2, "title")
	if err := e.svc.Reject(ctx, run, 2, []string{"pubdate"}, "admin", store.Basis{Mode: store.DecisionBulk}); err != nil {
		t.Fatalf("rejecting an unwritable date = %v", err)
	}
}

// serve runs the writer until the test ends.
func (e *env) serve() context.CancelFunc {
	e.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = e.svc.Serve(ctx)
		close(done)
	}()
	e.t.Cleanup(func() {
		cancel()
		<-done
	})
	return cancel
}

// waitApplied waits for the background apply to finish.
func (e *env) waitApplied() ApplyStatus {
	e.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if st := e.svc.ApplyStatus(); !st.Running && !st.Finished.IsZero() {
			return st
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.t.Fatal("the background apply didn't finish")
	return ApplyStatus{}
}

func TestStartApply(t *testing.T) {
	ctx := context.Background()
	e := setup(t)
	e.cal.entered, e.cal.release = make(chan struct{}, 10), make(chan struct{})
	e.serve()
	run := e.checkBook(2)
	e.approve(run, 2, allFields...)
	if err := e.svc.StartApply(ctx, "admin"); err != nil {
		t.Fatal(err)
	}
	<-e.cal.entered
	if st := e.svc.ApplyStatus(); !st.Running || st.Started.IsZero() {
		t.Fatalf("status while applying = %+v", st)
	}
	if err := e.svc.StartApply(ctx, "admin"); !errors.Is(err, ErrBusy) {
		t.Fatalf("a second StartApply = %v", err)
	}
	close(e.cal.release)
	if st := e.waitApplied(); st.Summary != (Summary{Applied: 5}) || st.Err != "" {
		t.Fatalf("status = %+v", st)
	}

	if err := e.fixes.SetWritesEnabled(ctx, false, "admin"); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.StartApply(ctx, "admin"); !errors.Is(err, ErrWritesOff) {
		t.Fatalf("StartApply with writes off = %v", err)
	}
	if err := e.fixes.SetWritesEnabled(ctx, true, "admin"); err != nil {
		t.Fatal(err)
	}
	op, err := e.fixes.Operation(ctx, e.operationIDs()[0])
	if err != nil {
		t.Fatal(err)
	}
	other, err := e.fixes.StartOperation(ctx, op, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.fixes.Advance(ctx, other, store.OpFailed, nil, "interrupted: nothing was sent"); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.StartApply(ctx, "admin"); !errors.Is(err, ErrPaused) {
		t.Fatalf("StartApply while paused = %v", err)
	}
}

func TestServeStopsBetweenBooks(t *testing.T) {
	ctx := context.Background()
	b3 := book2()
	b3.ID, b3.UUID = 3, "u-3"
	e := setup(t, book2(), b3)
	e.cal.entered, e.cal.release = make(chan struct{}, 10), make(chan struct{})
	run2, run3 := e.checkBook(2), e.checkBook(3)
	e.approve(run2, 2, allFields...)
	e.approve(run3, 3, allFields...)
	stop := e.serve()
	if err := e.svc.StartApply(ctx, "admin"); err != nil {
		t.Fatal(err)
	}
	<-e.cal.entered
	stop()
	close(e.cal.release)
	st := e.waitApplied()
	if !strings.Contains(st.Err, "shutting down") || st.Summary != (Summary{Applied: 5}) {
		t.Fatalf("status = %+v", st)
	}
	op, err := e.fixes.Operation(ctx, e.operationIDs()[0])
	if err != nil || op.State != store.OpCompleted || op.BookID != 2 {
		t.Fatalf("book 2's operation = %+v, %v", op, err)
	}
	if e.cal.setCount() != 1 || e.statuses(run3, 3)["title"] != store.ProposalApproved {
		t.Fatalf("book 3: set-fields calls %d, statuses %v", e.cal.setCount(), e.statuses(run3, 3))
	}
}

func TestServeRecoversAtStart(t *testing.T) {
	ctx := context.Background()
	e := setup(t)
	op := e.crashed(store.OpWriting, true, allFields...)
	e.serve()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, err := e.fixes.Operation(ctx, op.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.State == store.OpRestored {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("state = %s", got.State)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestUndoSurvivesCancel(t *testing.T) {
	e := setup(t)
	orig := e.applied()
	e.cal.entered, e.cal.release = make(chan struct{}, 10), make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	type result struct {
		id  int64
		err error
	}
	done := make(chan result, 1)
	go func() {
		id, err := e.svc.Undo(ctx, orig.ID, "admin")
		done <- result{id, err}
	}()
	<-e.cal.entered
	cancel()
	close(e.cal.release)
	r := <-done
	if r.err != nil {
		t.Fatalf("Undo = %v", r.err)
	}
	if op, err := e.fixes.Operation(context.Background(), r.id); err != nil || op.State != store.OpCompleted {
		t.Fatalf("undo = %+v, %v", op, err)
	}
}

func TestUndoable(t *testing.T) {
	ctx := context.Background()
	e := setup(t)
	orig := e.applied()
	if ok, err := e.svc.Undoable(ctx, orig.ID); err != nil || !ok {
		t.Fatalf("completed apply undoable = %v, %v", ok, err)
	}
	if _, err := e.svc.Undo(ctx, orig.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := e.svc.Undoable(ctx, orig.ID); ok {
		t.Fatal("an undone operation is undoable")
	}

	e2 := setup(t)
	run := e2.checkBook(2)
	e2.approve(run, 2, "title")
	e2.cal.alterOnce = true
	e2.cal.alter = func(c library.Change) library.Change {
		c.Value = jsonValue("Altered")
		return c
	}
	if _, err := e2.svc.Apply(ctx, "admin"); !errors.Is(err, ErrPaused) {
		t.Fatal(err)
	}
	if ok, _ := e2.svc.Undoable(ctx, e2.operationIDs()[0]); ok {
		t.Fatal("a restored operation is undoable")
	}
}

// A fix rejected while a background apply runs is skipped, and the apply
// goes on.
func TestApplySkipsRejectedMidway(t *testing.T) {
	ctx := context.Background()
	b3, b4 := book2(), book2()
	b3.ID, b3.UUID = 3, "u-3"
	b4.ID, b4.UUID = 4, "u-4"
	e := setup(t, book2(), b3, b4)
	e.cal.entered, e.cal.release = make(chan struct{}, 10), make(chan struct{})
	run2, run3, run4 := e.checkBook(2), e.checkBook(3), e.checkBook(4)
	e.approve(run2, 2, allFields...)
	e.approve(run3, 3, allFields...)
	e.approve(run4, 4, allFields...)
	e.serve()
	if err := e.svc.StartApply(ctx, "admin"); err != nil {
		t.Fatal(err)
	}
	<-e.cal.entered
	if err := e.svc.Reject(ctx, run3, 3, []string{"title"}, "admin", store.Basis{Mode: store.DecisionBulk}); err != nil {
		t.Fatal(err)
	}
	close(e.cal.release)
	if st := e.waitApplied(); st.Err != "" || st.Summary != (Summary{Applied: 10}) {
		t.Fatalf("status = %+v", st)
	}
	if got := e.cal.book(3); got.Title != book2().Title {
		t.Fatalf("book 3 was written: %q", got.Title)
	}
	if st := e.statuses(run3, 3); st["title"] != store.ProposalRejected || st["authors"] != store.ProposalApproved {
		t.Fatalf("book 3 statuses %v", st)
	}
	if e.cal.setCount() != 2 {
		t.Fatalf("set-fields calls %d, want books 2 and 4", e.cal.setCount())
	}
}

// TestDecisionBasisPassesThrough: Approve and Reject record how the person
// decided.
func TestDecisionBasisPassesThrough(t *testing.T) {
	ctx := context.Background()
	e := setup(t)
	run := e.checkBook(2)
	steps := []struct {
		field string
		do    func() error
		mode  string
		shown bool
	}{
		{"title", func() error {
			return e.svc.Approve(ctx, run, 2, []string{"title"}, "admin", store.Basis{Mode: store.DecisionIndividual})
		}, store.DecisionIndividual, false},
		{"authors", func() error {
			return e.svc.Reject(ctx, run, 2, []string{"authors"}, "admin", store.Basis{Mode: store.DecisionIndividual, VerdictsShown: true})
		}, store.DecisionIndividual, true},
		{"languages", func() error {
			return e.svc.Reject(ctx, run, 2, []string{"languages"}, "admin", store.Basis{Mode: store.DecisionBulk})
		}, store.DecisionBulk, false},
	}
	for _, st := range steps {
		if err := st.do(); err != nil {
			t.Fatal(st.field, err)
		}
	}
	rows, err := e.fixes.Proposals(ctx, run, 2)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]store.ProposalRow{}
	for _, r := range rows {
		got[r.Proposal.Field] = r
	}
	for _, st := range steps {
		r, ok := got[st.field]
		if !ok || r.DecisionMode != st.mode || r.VerdictsShown != st.shown {
			t.Errorf("%s = %+v, want mode %s shown %v", st.field, r, st.mode, st.shown)
		}
	}
}
