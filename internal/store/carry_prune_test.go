package store

import (
	"context"
	"reflect"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/report"
)

func (e *carryEnv) runIDs() []int64 {
	e.t.Helper()
	rows, err := e.s.Pool.Query(context.Background(), `SELECT id FROM runs ORDER BY id`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			e.t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

// A carried row is a copy, so it neither keeps its run from being pruned nor
// outlives it; the run holding the original rejection is kept, so the carry
// goes on.
func TestPruneIgnoresCarriedRows(t *testing.T) {
	ctx := context.Background()
	e := newCarryEnv(t)
	books := []library.BookRef{{ID: 10, UUID: "u-1"}}
	p := titleProposal("New")
	run1 := e.project("lib", books, []evidence.Proposal{p})
	e.decide(run1, 10, p, ProposalRejected, blind)
	var last int64
	for range 5 {
		last = e.project("lib", books, []evidence.Proposal{p})
	}
	if _, err := e.r.Prune(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if left := e.runIDs(); !reflect.DeepEqual(left, []int64{run1, last}) {
		t.Fatalf("runs left = %v, want the original rejection's run %d and the newest %d", left, run1, last)
	}
	next := e.project("lib", books, []evidence.Proposal{p})
	if r, ok := e.row(next, 10, "title"); !ok || r.DecisionMode != DecisionCarried || r.CarriedFromRun != run1 {
		t.Fatalf("after the prune: %+v, %v", r, ok)
	}
}

// A run whose carried row a person changed holds a real decision and stays.
func TestPruneKeepsARunWhoseCarriedRowWasDecided(t *testing.T) {
	ctx := context.Background()
	e := newCarryEnv(t)
	books := []library.BookRef{{ID: 10, UUID: "u-1"}}
	p := titleProposal("New")
	run1 := e.project("lib", books, []evidence.Proposal{p})
	e.decide(run1, 10, p, ProposalRejected, blind)
	run2 := e.project("lib", books, []evidence.Proposal{p})
	e.decide(run2, 10, p, ProposalApproved, Basis{Mode: DecisionIndividual, VerdictsShown: true})
	e.project("lib", books, []evidence.Proposal{p})
	e.project("lib", books, []evidence.Proposal{p})
	if _, err := e.r.Prune(ctx, 1); err != nil {
		t.Fatal(err)
	}
	// The approval carried on (Felix, 2026-11-24), so run 2's row is retired
	// as stale; it is still a person's decision, and its run stays.
	if r, ok := e.row(run2, 10, "title"); !ok || r.Status != ProposalStale || r.Reason != "carried" || r.DecisionMode != DecisionIndividual {
		t.Fatalf("a decided run was pruned: %+v, %v", r, ok)
	}
}

// Only a run's first seal check carries rejections into it. A re-check, such
// as the nightly sweep's, never back-fills a run, so a run checked before a
// rejection existed doesn't get one, and one a person decided since keeps it.
func TestOnlyTheFirstCheckCarries(t *testing.T) {
	e := newCarryEnv(t)
	books := []library.BookRef{{ID: 10, UUID: "u-1"}}
	p := titleProposal("New")
	run1 := e.project("lib", books, []evidence.Proposal{p})
	run2 := e.project("lib", books, []evidence.Proposal{p}) // checked before any decision
	e.decide(run1, 10, p, ProposalRejected, blind)
	run3 := e.project("lib", books, []evidence.Proposal{p}) // carried
	e.decide(run3, 10, p, ProposalApproved, Basis{Mode: DecisionIndividual, VerdictsShown: true})
	e.check(run2, []report.BookProjection{{BookID: 10, Title: "T", Tier: evidence.TierA, Proposals: []evidence.Proposal{p}}})
	if r, ok := e.row(run2, 10, "title"); ok {
		t.Fatalf("a re-check back-filled run 2: %+v", r)
	}
	if r, _ := e.row(run3, 10, "title"); r.Status != ProposalApproved {
		t.Fatalf("run 3 = %+v", r)
	}
}
