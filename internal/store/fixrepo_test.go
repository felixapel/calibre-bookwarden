package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/decide"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

func fixRepo(t *testing.T) (*FixRepo, *RunRepo, *Store) {
	t.Helper()
	r, s := runRepo(t)
	return NewFixRepo(s), r, s
}

// checkedRun stores a completed run with checked evidence for books.
func checkedRun(t *testing.T, r *RunRepo, books []library.BookRef) int64 {
	t.Helper()
	ctx := context.Background()
	run, err := r.CreateRun(ctx, verify.Scope{}, "library", "v1", books)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range books {
		raw, seal, err := decide.Seal(ctx, cleanEvidence(b))
		if err != nil {
			t.Fatal(err)
		}
		if err := r.FinishBook(ctx, run.ID, b, verify.Outcome{State: verify.BookChecked, Evidence: raw, Seal: seal}); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.SetStatus(ctx, run.ID, verify.RunCompleted, ""); err != nil {
		t.Fatal(err)
	}
	return run.ID
}

func titleProposal(v string) evidence.Proposal {
	return evidence.Proposal{Field: "title", Current: []string{"Old"}, Proposed: []string{v}, Roots: []string{"dnb", "file"}}
}

func applyOp(runID int64, bookID int) Operation {
	return Operation{
		Kind: OpApply, RunID: &runID, LibraryID: "library", BookID: bookID, UUID: "uuid-1",
		Changes:     []library.Change{{Field: "title", Value: json.RawMessage(`"New"`)}},
		Before:      evidence.Record{Title: "Old", Authors: []string{"A"}, Languages: []string{}, Identifiers: map[string]string{}},
		RequestedBy: "felix",
	}
}

func TestWritesSwitch(t *testing.T) {
	ctx := context.Background()
	f, _, s := fixRepo(t)
	if on, err := f.WritesEnabled(ctx); err != nil || on {
		t.Fatalf("fresh database: %v, %v", on, err)
	}
	for _, on := range []bool{true, false, true} {
		if err := f.SetWritesEnabled(ctx, on, "felix"); err != nil {
			t.Fatal(err)
		}
	}
	if on, err := f.WritesEnabled(ctx); err != nil || !on {
		t.Fatalf("after on, off, on: %v, %v", on, err)
	}
	rows, err := s.Pool.Query(ctx, `SELECT value::text, changed_by FROM settings ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	var log []string
	for rows.Next() {
		var v, by string
		if err := rows.Scan(&v, &by); err != nil {
			t.Fatal(err)
		}
		log = append(log, v+" "+by)
	}
	if want := []string{"true felix", "false felix", "true felix"}; !reflect.DeepEqual(log, want) {
		t.Fatalf("log = %q", log)
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE settings SET value = 'false'`); err == nil {
		t.Fatal("settings accepted an UPDATE")
	}
}

func TestDecide(t *testing.T) {
	ctx := context.Background()
	f, r, _ := fixRepo(t)
	books := refs(2)
	run := checkedRun(t, r, books)
	p := titleProposal("New")
	for _, status := range []string{ProposalApproved, ProposalRejected, ProposalApproved} {
		if err := f.Decide(ctx, run, books[0].ID, []evidence.Proposal{p}, status, "felix", Basis{Mode: DecisionBulk}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := f.Proposals(ctx, run, books[0].ID)
	if err != nil || len(rows) != 1 || rows[0].Status != ProposalApproved || rows[0].DecidedBy != "felix" || !reflect.DeepEqual(rows[0].Proposal, p) {
		t.Fatalf("Proposals = %+v, %v", rows, err)
	}
	if err := f.Decide(ctx, run, books[0].ID, []evidence.Proposal{p}, ProposalApplied, "felix", Basis{Mode: DecisionBulk}); err == nil {
		t.Fatal("Decide accepted a final status")
	}
	if err := f.Close(ctx, []int64{rows[0].ID}, ProposalStale, "book_changed"); err != nil {
		t.Fatal(err)
	}
	if err := f.Decide(ctx, run, books[0].ID, []evidence.Proposal{p}, ProposalRejected, "felix", Basis{Mode: DecisionBulk}); !errors.Is(err, ErrClosed) {
		t.Fatalf("deciding a stale proposal: %v", err)
	}
	if err := f.Close(ctx, []int64{rows[0].ID}, ProposalBlocked, "x"); !errors.Is(err, ErrClosed) {
		t.Fatalf("closing a closed proposal: %v", err)
	}
	if err := f.Decide(ctx, run, 999, []evidence.Proposal{p}, ProposalApproved, "felix", Basis{Mode: DecisionBulk}); err == nil {
		t.Fatal("Decide accepted a book without evidence")
	}
	authors := evidence.Proposal{Field: "authors", Proposed: []string{"B"}, Roots: []string{"dnb", "file"}}
	if err := f.Decide(ctx, run, books[1].ID, []evidence.Proposal{titleProposal("T2"), authors}, ProposalApproved, "felix", Basis{Mode: DecisionBulk}); err != nil {
		t.Fatal(err)
	}
	approved, err := f.Approved(ctx)
	if err != nil || len(approved) != 2 || approved[0].BookID != books[1].ID || approved[0].Proposal.Field != "authors" || approved[1].Proposal.Field != "title" {
		t.Fatalf("Approved = %+v, %v", approved, err)
	}
	if _, err := f.StartOperation(ctx, applyOp(run, books[1].ID), []int64{approved[1].ID}); err != nil {
		t.Fatal(err)
	}
	if approved, err = f.Approved(ctx); err != nil || len(approved) != 1 || approved[0].Proposal.Field != "authors" {
		t.Fatalf("Approved after linking = %+v, %v", approved, err)
	}
	if err := f.Decide(ctx, run, books[1].ID, []evidence.Proposal{titleProposal("T2")}, ProposalRejected, "felix", Basis{Mode: DecisionBulk}); !errors.Is(err, ErrClosed) {
		t.Fatalf("deciding a proposal in flight: %v", err)
	}
}

func TestOperationStates(t *testing.T) {
	ctx := context.Background()
	f, r, s := fixRepo(t)
	books := refs(2)
	run := checkedRun(t, r, books)
	if err := f.Decide(ctx, run, books[0].ID, []evidence.Proposal{titleProposal("New")}, ProposalApproved, "felix", Basis{Mode: DecisionBulk}); err != nil {
		t.Fatal(err)
	}
	rows, _ := f.Proposals(ctx, run, books[0].ID)
	id, err := f.StartOperation(ctx, applyOp(run, books[0].ID), []int64{rows[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Advance(ctx, id, OpCompleted, nil, ""); !errors.Is(err, ErrBadTransition) {
		t.Fatalf("requested → completed: %v", err)
	}
	after := evidence.Record{Title: "New", Authors: []string{"A"}, Languages: []string{}, Identifiers: map[string]string{}}
	for _, st := range []string{OpWriting, OpVerifying, OpCompleted} {
		var a *evidence.Record
		if st == OpCompleted {
			a = &after
		}
		if err := f.Advance(ctx, id, st, a, ""); err != nil {
			t.Fatalf("→ %s: %v", st, err)
		}
	}
	op, err := f.Operation(ctx, id)
	if err != nil || op.State != OpCompleted || op.After == nil || !reflect.DeepEqual(*op.After, after) ||
		op.Before.Title != "Old" || len(op.Changes) != 1 || op.Changes[0].Field != "title" || op.RequestedBy != "felix" {
		t.Fatalf("Operation = %+v, %v", op, err)
	}
	if rows, _ = f.Proposals(ctx, run, books[0].ID); rows[0].Status != ProposalApplied || rows[0].OperationID == nil || *rows[0].OperationID != id {
		t.Fatalf("proposal after completed = %+v", rows[0])
	}
	if err := f.Advance(ctx, id, OpRestoring, nil, "readback_mismatch"); !errors.Is(err, ErrBadTransition) {
		t.Fatalf("completed → restoring: %v", err)
	}
	if err := f.Advance(ctx, 9999, OpWriting, nil, ""); !errors.Is(err, ErrNoOperation) {
		t.Fatalf("unknown operation: %v", err)
	}

	// A mismatch opens one incident and pauses writes; the restore closes the
	// proposals as failed.
	if err := f.Decide(ctx, run, books[1].ID, []evidence.Proposal{titleProposal("New")}, ProposalApproved, "felix", Basis{Mode: DecisionBulk}); err != nil {
		t.Fatal(err)
	}
	rows, _ = f.Proposals(ctx, run, books[1].ID)
	id2, err := f.StartOperation(ctx, applyOp(run, books[1].ID), []int64{rows[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range []string{OpWriting, OpVerifying, OpRestoring} {
		if err := f.Advance(ctx, id2, st, nil, map[bool]string{true: "readback_mismatch: title"}[st == OpRestoring]); err != nil {
			t.Fatalf("→ %s: %v", st, err)
		}
	}
	if paused, err := f.Paused(ctx); err != nil || !paused {
		t.Fatalf("Paused after restoring = %v, %v", paused, err)
	}
	if err := f.Advance(ctx, id2, OpRestored, &evidence.Record{Title: "Old"}, ""); err != nil {
		t.Fatal(err)
	}
	inc, err := f.Incidents(ctx)
	if err != nil || len(inc) != 1 || inc[0].OperationID != id2 || inc[0].Reason != "readback_mismatch" || inc[0].Detail != "readback_mismatch: title" {
		t.Fatalf("Incidents = %+v, %v", inc, err)
	}
	if rows, _ = f.Proposals(ctx, run, books[1].ID); rows[0].Status != ProposalFailed {
		t.Fatalf("proposal after restored = %+v", rows[0])
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE operations SET requested_by = 'x'`); err == nil {
		t.Fatal("operations accepted an UPDATE")
	}
	if _, err := s.Pool.Exec(ctx, `DELETE FROM operation_events`); err == nil {
		t.Fatal("operation_events accepted a DELETE")
	}
}

func TestUnfinishedAndUndo(t *testing.T) {
	ctx := context.Background()
	f, r, _ := fixRepo(t)
	books := refs(1)
	run := checkedRun(t, r, books)
	done, err := f.StartOperation(ctx, applyOp(run, books[0].ID), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range []string{OpWriting, OpVerifying, OpCompleted} {
		if err := f.Advance(ctx, done, st, &evidence.Record{Title: "New"}, ""); err != nil {
			t.Fatal(err)
		}
	}
	open, err := f.StartOperation(ctx, applyOp(run, books[0].ID), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Advance(ctx, open, OpWriting, nil, ""); err != nil {
		t.Fatal(err)
	}
	un, err := f.Unfinished(ctx)
	if err != nil || len(un) != 1 || un[0].ID != open || un[0].State != OpWriting {
		t.Fatalf("Unfinished = %+v, %v", un, err)
	}
	if has, err := f.HasUndo(ctx, done); err != nil || has {
		t.Fatalf("HasUndo before any undo = %v, %v", has, err)
	}
	undo := applyOp(run, books[0].ID)
	undo.Kind, undo.RunID, undo.Undoes = OpUndo, nil, &done
	u1, err := f.StartOperation(ctx, undo, nil)
	if err != nil {
		t.Fatal(err)
	}
	if has, _ := f.HasUndo(ctx, done); !has {
		t.Fatal("an unfinished undo doesn't count")
	}
	for _, st := range []string{OpFailed} {
		if err := f.Advance(ctx, u1, st, nil, "interrupted"); err != nil {
			t.Fatal(err)
		}
	}
	if has, _ := f.HasUndo(ctx, done); has {
		t.Fatal("a failed undo counts")
	}
	u2, err := f.StartOperation(ctx, undo, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range []string{OpWriting, OpVerifying, OpCompleted} {
		if err := f.Advance(ctx, u2, st, &evidence.Record{Title: "Old"}, ""); err != nil {
			t.Fatal(err)
		}
	}
	if has, _ := f.HasUndo(ctx, done); !has {
		t.Fatal("a completed undo doesn't count")
	}
	bad := applyOp(run, books[0].ID)
	bad.RunID = nil
	if _, err := f.StartOperation(ctx, bad, nil); err == nil {
		t.Fatal("an apply without a run was stored")
	}
}

func TestAcknowledge(t *testing.T) {
	ctx := context.Background()
	f, r, _ := fixRepo(t)
	books := refs(1)
	run := checkedRun(t, r, books)
	id, err := f.StartOperation(ctx, applyOp(run, books[0].ID), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range []string{OpWriting, OpRestoring} {
		if err := f.Advance(ctx, id, st, nil, "write_error: forbidden"); err != nil {
			t.Fatal(err)
		}
	}
	inc, _ := f.Incidents(ctx)
	if len(inc) != 1 || inc[0].OperationState != OpRestoring {
		t.Fatalf("incidents = %+v; want one, its operation restoring", inc)
	}
	if err := f.Acknowledge(ctx, inc[0].ID, "felix"); !errors.Is(err, ErrNoIncident) {
		t.Fatalf("acknowledging while the operation is unfinished: %v", err)
	}
	if err := f.Advance(ctx, id, OpRestored, &evidence.Record{}, ""); err != nil {
		t.Fatal(err)
	}
	if err := f.Acknowledge(ctx, inc[0].ID, "felix"); err != nil {
		t.Fatal(err)
	}
	if paused, err := f.Paused(ctx); err != nil || paused {
		t.Fatalf("Paused after acknowledging = %v, %v", paused, err)
	}
	if inc, _ = f.Incidents(ctx); inc[0].AcknowledgedBy != "felix" || inc[0].AcknowledgedAt == nil || inc[0].OperationState != OpRestored {
		t.Fatalf("incident = %+v", inc[0])
	}
	if err := f.Acknowledge(ctx, inc[0].ID, "felix"); !errors.Is(err, ErrNoIncident) {
		t.Fatalf("acknowledging twice: %v", err)
	}
}

func TestFixWriterLock(t *testing.T) {
	ctx := context.Background()
	f, _, _ := fixRepo(t)
	release, ok, err := f.LockWriter(ctx)
	if err != nil || !ok {
		t.Fatalf("first lock: %v, %v", ok, err)
	}
	if _, ok, err := f.LockWriter(ctx); err != nil || ok {
		t.Fatalf("second lock while held: %v, %v", ok, err)
	}
	release()
	release2, ok, err := f.LockWriter(ctx)
	if err != nil || !ok {
		t.Fatalf("lock after release: %v, %v", ok, err)
	}
	release2()
}

func TestPruneKeepsReferencedRuns(t *testing.T) {
	ctx := context.Background()
	f, r, s := fixRepo(t)
	books := refs(1)
	withProposal := checkedRun(t, r, books)
	withOperation := checkedRun(t, r, books)
	plain := checkedRun(t, r, books)
	if err := f.Decide(ctx, withProposal, books[0].ID, []evidence.Proposal{titleProposal("New")}, ProposalRejected, "felix", Basis{Mode: DecisionBulk}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.StartOperation(ctx, applyOp(withOperation, books[0].ID), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := r.pruneAll(ctx); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[int64]bool{withProposal: true, withOperation: true, plain: false} {
		var n int
		if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM runs WHERE id=$1`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if (n == 1) != want {
			t.Errorf("run %d kept = %v, want %v", id, n == 1, want)
		}
	}
	if newest, err := f.NewestEvidenceRun(ctx, "library", books[0].ID, books[0].UUID); err != nil || newest != withOperation {
		t.Fatalf("NewestEvidenceRun = %d, %v; want %d", newest, err, withOperation)
	}
	if newest, err := f.NewestEvidenceRun(ctx, "library", books[0].ID, "other"); err != nil || newest != 0 {
		t.Fatalf("NewestEvidenceRun for another uuid = %d, %v", newest, err)
	}
}

// TestFailedAfterRestoring: a restore that ends failed keeps the incident
// opened by restoring, whatever its own detail says.
func TestFailedAfterRestoring(t *testing.T) {
	ctx := context.Background()
	f, r, _ := fixRepo(t)
	books := refs(1)
	run := checkedRun(t, r, books)
	id, err := f.StartOperation(ctx, applyOp(run, books[0].ID), nil)
	if err != nil {
		t.Fatal(err)
	}
	steps := []struct{ state, detail string }{
		{OpWriting, ""}, {OpVerifying, ""}, {OpRestoring, "readback_mismatch: title"}, {OpFailed, "restore_incomplete: title"},
	}
	for _, st := range steps {
		if err := f.Advance(ctx, id, st.state, nil, st.detail); err != nil {
			t.Fatalf("→ %s: %v", st.state, err)
		}
	}
	inc, err := f.Incidents(ctx)
	if err != nil || len(inc) != 1 || inc[0].Reason != "readback_mismatch" {
		t.Fatalf("incidents = %+v, %v", inc, err)
	}
	if op, _ := f.Operation(ctx, id); op.State != OpFailed || op.Detail != "restore_incomplete: title" {
		t.Fatalf("operation = %+v", op)
	}
}

// TestRestoreWriteStep: a restore records the book it found before writing
// back, as a second restoring event that opens no second incident.
func TestRestoreWriteStep(t *testing.T) {
	ctx := context.Background()
	f, r, _ := fixRepo(t)
	books := refs(1)
	run := checkedRun(t, r, books)
	id, err := f.StartOperation(ctx, applyOp(run, books[0].ID), nil)
	if err != nil {
		t.Fatal(err)
	}
	found := evidence.Record{Title: "Found"}
	steps := []struct {
		state, detail string
		after         *evidence.Record
	}{
		{OpWriting, "", nil}, {OpRestoring, "interrupted: writing", nil}, {OpRestoring, "restore_write: title, sort", &found},
	}
	for _, st := range steps {
		if err := f.Advance(ctx, id, st.state, st.after, st.detail); err != nil {
			t.Fatalf("→ %s %q: %v", st.state, st.detail, err)
		}
	}
	op, err := f.Operation(ctx, id)
	if err != nil || op.State != OpRestoring || op.After == nil || op.After.Title != "Found" {
		t.Fatalf("operation = %+v, %v", op, err)
	}
	if inc, _ := f.Incidents(ctx); len(inc) != 1 || inc[0].Reason != "interrupted" {
		t.Fatalf("incidents = %+v", inc)
	}
}

// TestProposalFieldMatchesRow: a row's JSON can't name another field than
// the row, so a tampered approval can't move to a field Felix didn't approve.
func TestProposalFieldMatchesRow(t *testing.T) {
	ctx := context.Background()
	f, r, s := fixRepo(t)
	books := refs(1)
	run := checkedRun(t, r, books)
	if err := f.Decide(ctx, run, books[0].ID, []evidence.Proposal{titleProposal("New")}, ProposalApproved, "felix", Basis{Mode: DecisionBulk}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE proposals SET proposal = jsonb_set(proposal, '{field}', '"publisher"')`); err == nil {
		t.Fatal("a proposal moved to another field")
	}
}

// completedOp records an apply on the book that went all the way to completed.
func completedOp(t *testing.T, f *FixRepo, run int64, bookID int) int64 {
	t.Helper()
	ctx := context.Background()
	id, err := f.StartOperation(ctx, applyOp(run, bookID), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range []string{OpWriting, OpVerifying, OpCompleted} {
		var a *evidence.Record
		if st == OpCompleted {
			a = &evidence.Record{Title: "New"}
		}
		if err := f.Advance(ctx, id, st, a, ""); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func TestOperationsPage(t *testing.T) {
	ctx := context.Background()
	f, r, _ := fixRepo(t)
	books := refs(1)
	run := checkedRun(t, r, books)
	ids := []int64{completedOp(t, f, run, books[0].ID), completedOp(t, f, run, books[0].ID), completedOp(t, f, run, books[0].ID)}
	page, err := f.Operations(ctx, 0, 2)
	if err != nil || len(page) != 2 || page[0].ID != ids[2] || page[1].ID != ids[1] || page[0].State != OpCompleted {
		t.Fatalf("first page = %+v, %v", page, err)
	}
	page, err = f.Operations(ctx, ids[1], 2)
	if err != nil || len(page) != 1 || page[0].ID != ids[0] {
		t.Fatalf("second page = %+v, %v", page, err)
	}
}

func TestEvents(t *testing.T) {
	ctx := context.Background()
	f, r, _ := fixRepo(t)
	books := refs(1)
	id := completedOp(t, f, checkedRun(t, r, books), books[0].ID)
	ev, err := f.Events(ctx, id)
	if err != nil || len(ev) != 4 {
		t.Fatalf("Events = %+v, %v", ev, err)
	}
	for i, want := range []string{OpRequested, OpWriting, OpVerifying, OpCompleted} {
		if ev[i].Seq != i+1 || ev[i].State != want || (ev[i].After != nil) != (want == OpCompleted) || ev[i].At.IsZero() {
			t.Errorf("event %d = %+v", i, ev[i])
		}
	}
}

func TestRunProposals(t *testing.T) {
	ctx := context.Background()
	f, r, _ := fixRepo(t)
	books := refs(2)
	run := checkedRun(t, r, books)
	other := checkedRun(t, r, books)
	authors := evidence.Proposal{Field: "authors", Proposed: []string{"B"}}
	if err := f.Decide(ctx, run, books[1].ID, []evidence.Proposal{titleProposal("T"), authors}, ProposalApproved, "felix", Basis{Mode: DecisionBulk}); err != nil {
		t.Fatal(err)
	}
	if err := f.Decide(ctx, run, books[0].ID, []evidence.Proposal{titleProposal("T")}, ProposalRejected, "felix", Basis{Mode: DecisionBulk}); err != nil {
		t.Fatal(err)
	}
	if err := f.Decide(ctx, other, books[0].ID, []evidence.Proposal{titleProposal("T")}, ProposalApproved, "felix", Basis{Mode: DecisionBulk}); err != nil {
		t.Fatal(err)
	}
	rows, err := f.RunProposals(ctx, run)
	if err != nil || len(rows) != 3 || rows[0].BookID != books[0].ID || rows[1].Proposal.Field != "authors" || rows[2].Proposal.Field != "title" {
		t.Fatalf("RunProposals = %+v, %v", rows, err)
	}
}

func TestSettingChanges(t *testing.T) {
	ctx := context.Background()
	f, _, _ := fixRepo(t)
	for _, on := range []bool{true, false} {
		if err := f.SetWritesEnabled(ctx, on, "felix"); err != nil {
			t.Fatal(err)
		}
	}
	log, err := f.SettingChanges(ctx, 20)
	if err != nil || len(log) != 2 || log[0].Value || !log[1].Value || log[0].ChangedBy != "felix" || log[0].ChangedAt.IsZero() {
		t.Fatalf("SettingChanges = %+v, %v", log, err)
	}
	if log, _ = f.SettingChanges(ctx, 1); len(log) != 1 {
		t.Fatalf("limit 1 = %+v", log)
	}
}

func TestApprovedCount(t *testing.T) {
	ctx := context.Background()
	f, r, _ := fixRepo(t)
	books := refs(3)
	run := checkedRun(t, r, books)
	authors := evidence.Proposal{Field: "authors", Proposed: []string{"B"}}
	if err := f.Decide(ctx, run, books[0].ID, []evidence.Proposal{titleProposal("T"), authors}, ProposalApproved, "felix", Basis{Mode: DecisionBulk}); err != nil {
		t.Fatal(err)
	}
	if err := f.Decide(ctx, run, books[1].ID, []evidence.Proposal{titleProposal("T")}, ProposalRejected, "felix", Basis{Mode: DecisionBulk}); err != nil {
		t.Fatal(err)
	}
	if err := f.Decide(ctx, run, books[2].ID, []evidence.Proposal{titleProposal("T")}, ProposalApproved, "felix", Basis{Mode: DecisionBulk}); err != nil {
		t.Fatal(err)
	}
	rows, _ := f.Proposals(ctx, run, books[2].ID)
	if _, err := f.StartOperation(ctx, applyOp(run, books[2].ID), []int64{rows[0].ID}); err != nil {
		t.Fatal(err)
	}
	if n, err := f.ApprovedCount(ctx); err != nil || n != 2 {
		t.Fatalf("ApprovedCount = %d, %v", n, err)
	}
}

// TestRestoredReason: proposals of a restored operation are failed with the
// incident's reason.
func TestRestoredReason(t *testing.T) {
	ctx := context.Background()
	f, r, _ := fixRepo(t)
	books := refs(1)
	run := checkedRun(t, r, books)
	if err := f.Decide(ctx, run, books[0].ID, []evidence.Proposal{titleProposal("T")}, ProposalApproved, "felix", Basis{Mode: DecisionBulk}); err != nil {
		t.Fatal(err)
	}
	rows, _ := f.Proposals(ctx, run, books[0].ID)
	id, err := f.StartOperation(ctx, applyOp(run, books[0].ID), []int64{rows[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range []struct{ state, detail string }{{OpWriting, ""}, {OpVerifying, ""}, {OpRestoring, "readback_mismatch: title"}, {OpRestored, ""}} {
		if err := f.Advance(ctx, id, st.state, &evidence.Record{}, st.detail); err != nil {
			t.Fatal(err)
		}
	}
	if rows, _ = f.Proposals(ctx, run, books[0].ID); rows[0].Status != ProposalFailed || rows[0].Reason != "readback_mismatch" {
		t.Fatalf("proposal = %+v", rows[0])
	}
}

// An approval the agent left from before mode on was retired is never
// applied by a person's Apply: it is neither selected nor counted (ADR-029).
func TestAgentApprovalIsNeverApplied(t *testing.T) {
	ctx := context.Background()
	f, r, _ := fixRepo(t)
	books := refs(2)
	run := checkedRun(t, r, books)
	if ok, err := f.DecideOpen(ctx, run, books[0].ID, titleProposal("T"), ProposalApproved, AgentActor); err != nil || !ok {
		t.Fatalf("DecideOpen = %v, %v", ok, err)
	}
	if err := f.Decide(ctx, run, books[1].ID, []evidence.Proposal{titleProposal("T")}, ProposalApproved, "felix", Basis{Mode: DecisionBulk}); err != nil {
		t.Fatal(err)
	}
	rows, err := f.Approved(ctx)
	if err != nil || len(rows) != 1 || rows[0].BookID != books[1].ID || rows[0].DecidedBy != "felix" {
		t.Fatalf("Approved = %+v, %v; want only the person's approval", rows, err)
	}
	if n, err := f.ApprovedCount(ctx); err != nil || n != 1 {
		t.Fatalf("ApprovedCount = %d, %v; want 1", n, err)
	}
}

// TestOneLiveUndoPerOperation: while an undo of an operation is unfinished or
// completed, a second one is refused, even when two start at once; after a
// failed undo a new one may start.
func TestOneLiveUndoPerOperation(t *testing.T) {
	ctx := context.Background()
	f, r, _ := fixRepo(t)
	books := refs(1)
	run := checkedRun(t, r, books)
	done, err := f.StartOperation(ctx, applyOp(run, books[0].ID), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range []string{OpWriting, OpVerifying, OpCompleted} {
		if err := f.Advance(ctx, done, st, &evidence.Record{Title: "New"}, ""); err != nil {
			t.Fatal(err)
		}
	}
	undo := applyOp(run, books[0].ID)
	undo.Kind, undo.RunID, undo.Undoes = OpUndo, nil, &done
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			_, err := f.StartOperation(ctx, undo, nil)
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	var ok, refused int
	for err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrUndoExists):
			refused++
		default:
			t.Fatalf("StartOperation = %v", err)
		}
	}
	if ok != 1 || refused != 1 {
		t.Fatalf("two undos at once: %d stored, %d refused; want 1 and 1", ok, refused)
	}
	un, err := f.Unfinished(ctx)
	if err != nil || len(un) != 1 {
		t.Fatalf("Unfinished = %+v, %v", un, err)
	}
	if err := f.Advance(ctx, un[0].ID, OpFailed, nil, "interrupted"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.StartOperation(ctx, undo, nil); err != nil {
		t.Fatalf("a new undo after a failed one = %v", err)
	}
}

// TestUndoWaitsForAConcurrentUndo: an undo that starts while another
// transaction holds the original operation and is inserting its own undo
// waits, then sees that undo and is refused. (One statement that both locks
// and checks would keep its old snapshot after the wait and let both in.)
func TestUndoWaitsForAConcurrentUndo(t *testing.T) {
	ctx := context.Background()
	f, r, s := fixRepo(t)
	books := refs(1)
	run := checkedRun(t, r, books)
	done, err := f.StartOperation(ctx, applyOp(run, books[0].ID), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range []string{OpWriting, OpVerifying, OpCompleted} {
		if err := f.Advance(ctx, done, st, &evidence.Record{Title: "New"}, ""); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT 1 FROM operations WHERE id = $1 FOR UPDATE`, done); err != nil {
		t.Fatal(err)
	}
	undo := applyOp(run, books[0].ID)
	undo.Kind, undo.RunID, undo.Undoes = OpUndo, nil, &done
	result := make(chan error, 1)
	go func() {
		_, err := f.StartOperation(ctx, undo, nil)
		result <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting int
		if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock' AND datname = current_database()`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the second undo never waited for the lock")
		}
		time.Sleep(5 * time.Millisecond)
	}
	var id int64
	if err := tx.QueryRow(ctx, `INSERT INTO operations (kind, undoes, library_id, book_id, uuid, changes, before, requested_by)
		VALUES ('undo', $1, 'library', $2, 'uuid-1', '[]'::jsonb, '{}'::jsonb, 'felix') RETURNING id`, done, books[0].ID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO operation_events (operation_id, seq, state) VALUES ($1, 1, 'requested')`, id); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, ErrUndoExists) {
		t.Fatalf("the waiting undo = %v, want ErrUndoExists", err)
	}
}
