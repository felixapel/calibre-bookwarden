package store

import (
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/library"
)

// A proposal sealed with "roots": null has no path; checking its run must not
// fail, and it is the same fix as itself.
func TestCarryToleratesNullRoots(t *testing.T) {
	e := newCarryEnv(t)
	books := []library.BookRef{{ID: 10, UUID: "u-1"}}
	p := evidence.Proposal{Field: "title", Current: []string{"Old"}, Proposed: []string{"New"}}
	run1 := e.project("lib", books, []evidence.Proposal{p})
	e.decide(run1, 10, p, ProposalRejected, blind)
	run2 := e.project("lib", books, []evidence.Proposal{p})
	if r, ok := e.row(run2, 10, "title"); !ok || r.DecisionMode != DecisionCarried {
		t.Fatalf("run 2 = %+v, %v", r, ok)
	}
}
