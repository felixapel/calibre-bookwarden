package store

import (
	"context"
	"maps"
	"reflect"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/metrics"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

func TestMetricsSnapshotOnAnEmptyDatabase(t *testing.T) {
	ctx := context.Background()
	_, s := runRepo(t)
	snap, err := NewMetricsRepo(s).Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snap.LastRun != nil || len(snap.RunsByStatus) != 0 || len(snap.ProposalsOpen) != 0 || snap.FixesApplied != 0 ||
		snap.FixesUndone != 0 || snap.IncidentsOpen != 0 || snap.SealChecksBroken != 0 || len(snap.Labels) != 0 {
		t.Errorf("snapshot of an empty database = %+v", snap)
	}
}

func TestMetricsSnapshot(t *testing.T) {
	ctx := context.Background()
	f, r, s := fixRepo(t)
	books := refs(3)
	run := checkedRun(t, r, books)
	// A second run is still going: it counts as a run, but is not "the last
	// finished run".
	if _, err := r.CreateRun(ctx, verify.Scope{}, "library", "v1", books); err != nil {
		t.Fatal(err)
	}

	// The last finished run proposes two titles and an ISBN; one title is decided.
	for _, b := range books {
		if _, err := s.Pool.Exec(ctx, `INSERT INTO book_projection (run_id, book_id, title, tier, findings_count, proposals_count) VALUES ($1,$2,'T','B',0,1)`, run, b.ID); err != nil {
			t.Fatal(err)
		}
	}
	for i, field := range []string{"title", "title", "isbn"} {
		if _, err := s.Pool.Exec(ctx, `INSERT INTO fix_projection (run_id, book_id, ordinal, field, proposal) VALUES ($1,$2,0,$3,'{}')`, run, books[i].ID, field); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Decide(ctx, run, books[0].ID, []evidence.Proposal{titleProposal("New")}, ProposalApproved, "felix", Basis{Mode: DecisionIndividual}); err != nil {
		t.Fatal(err)
	}

	// The ledger: two completed applies, one completed undo, one failed
	// apply that opened an incident.
	done := completedOp(t, f, run, books[0].ID)
	completedOp(t, f, run, books[1].ID)
	undo := applyOp(run, books[0].ID)
	undo.Kind, undo.RunID, undo.Undoes = OpUndo, nil, &done
	u, err := f.StartOperation(ctx, undo, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range []string{OpWriting, OpVerifying, OpCompleted} {
		if err := f.Advance(ctx, u, st, &evidence.Record{Title: "Old"}, ""); err != nil {
			t.Fatal(err)
		}
	}
	bad, err := f.StartOperation(ctx, applyOp(run, books[2].ID), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range []string{OpWriting, OpRestoring} {
		if err := f.Advance(ctx, bad, st, nil, "write_error: forbidden"); err != nil {
			t.Fatal(err)
		}
	}

	// A broken seal check.
	if _, err := s.Pool.Exec(ctx, `INSERT INTO seal_checks (run_id, checked_at, books, broken, first_broken_book, findings_version, decision_version) VALUES ($1, now(), 3, 1, $2, 1, 1)`, run, books[1].ID); err != nil {
		t.Fatal(err)
	}

	snap, err := NewMetricsRepo(s).Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]int{"completed": 1, "running": 1}; !maps.Equal(snap.RunsByStatus, want) {
		t.Errorf("RunsByStatus = %v, want %v", snap.RunsByStatus, want)
	}
	if snap.LastRun == nil || snap.LastRun.DurationSeconds < 0 || snap.LastRun.Kind != "full" || !maps.Equal(snap.LastRun.Books, map[string]int{"checked": 3}) {
		t.Errorf("LastRun = %+v, want the completed full run's 3 checked books", snap.LastRun)
	}
	if want := map[string]int{"title": 1, "isbn": 1}; !maps.Equal(snap.ProposalsOpen, want) {
		t.Errorf("ProposalsOpen = %v, want %v (the decided title is not open)", snap.ProposalsOpen, want)
	}
	if snap.FixesApplied != 2 || snap.FixesUndone != 1 {
		t.Errorf("FixesApplied = %d, FixesUndone = %d, want 2 and 1", snap.FixesApplied, snap.FixesUndone)
	}
	if snap.IncidentsOpen != 1 {
		t.Errorf("IncidentsOpen = %d, want 1", snap.IncidentsOpen)
	}
	if snap.SealChecksBroken != 1 {
		t.Errorf("SealChecksBroken = %d, want 1", snap.SealChecksBroken)
	}
	if want := []metrics.LabelCount{{Field: "title", Kind: "replace", Labels: 1}}; !reflect.DeepEqual(snap.Labels, want) {
		t.Errorf("Labels = %+v, want %+v", snap.Labels, want)
	}
}
