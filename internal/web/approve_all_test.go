package web

import (
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/fix"
	"github.com/felixapel/calibre-bookwarden/internal/report"
	"github.com/felixapel/calibre-bookwarden/internal/store"
)

// "Approve all open fixes" approves every open fix of the run, on every
// page, that Calibre can store; it leaves decided fixes and muted types
// alone and lists what it couldn't approve (Felix, 2026-11-24).
func TestApproveAllOpenFixes(t *testing.T) {
	pubdate := evidence.Proposal{Field: "pubdate", Current: []string{}, Proposed: []string{"2001-07-01"}, Roots: []string{"dnb", "file"}}
	first := []report.FixRow{
		{RunID: 7, BookID: 2, Title: "Two", Tier: "A", Proposal: prop("title", "Old", "New")},
		{RunID: 7, BookID: 2, Title: "Two", Tier: "A", Proposal: prop("authors", "Wrong", "Right")},
		{RunID: 7, BookID: 3, Title: "Three", Tier: "A", Proposal: prop("title", "Old", "New")},
		{RunID: 7, BookID: 4, Title: "Four", Tier: "A", Proposal: prop("title", "Old", "New")},
	}
	second := []report.FixRow{
		{RunID: 7, BookID: 5, Title: "Five", Tier: "B", Proposal: prop("isbn", "1", "2")},
		{RunID: 7, BookID: 6, Title: "Six", Tier: "B", Proposal: pubdate},
	}
	rep := &fakeReports{fixPages: map[int]report.FixPage{
		0: {Rows: first, Next: &report.Cursor{RunID: 7, BookID: 4}},
		4: {Rows: second},
	}}
	f := &fakeFixing{
		proposals: []fix.ProposalRow{
			{RunID: 7, BookID: 3, Proposal: first[2].Proposal, Status: "rejected"},
			{RunID: 7, BookID: 4, Proposal: first[3].Proposal, Status: "approved"},
		},
		mute:      []fix.TypeMute{{LibraryID: "library", FixType: store.FixType{Field: "publisher", Path: "text", Kind: "fill"}, Muted: true}, {LibraryID: "other", FixType: store.FixType{Field: "title", Path: "text", Kind: "fill"}, Muted: true}},
		decideErr: map[int]error{5: fmt.Errorf("x: %w", fix.ErrTextOldVerifier)},
	}
	e := fixingEnv(t, f, rep)
	token := e.csrfToken(t)

	r := e.do(t, "POST", "/runs/7/fixes/approve-all", url.Values{"csrf_token": {token}}, nil)
	if r.status != 200 {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	var got []string
	for _, c := range f.recorded() {
		if c.Basis != (fix.Basis{Mode: store.DecisionBulk}) || c.Method != "Approve" || c.RunID != 7 {
			t.Fatalf("call %+v, want a blind bulk approval in run 7", c)
		}
		got = append(got, fmt.Sprintf("%d:%s", c.BookID, strings.Join(c.Fields, ",")))
	}
	if want := []string{"2:authors,title", "5:isbn"}; !slices.Equal(got, want) {
		t.Fatalf("approved %v, want %v (open writable fixes on both pages, one call per book)", got, want)
	}
	if !slices.Equal(rep.fixFilter.MutedTypes, []report.FixType{{Field: "publisher", Path: "text", Kind: "fill"}}) || rep.fixFilter.Muted {
		t.Fatalf("filter %+v, want the run library's muted types left out", rep.fixFilter)
	}
	for _, want := range []string{"Approved 2 fixes. Left alone: 2 already decided, and the muted fix types; 2 not approved",
		"Book 5: the printed text was read by an older verifier", "Book 6: Calibre can’t store its pubdate exactly", `href="/runs/7/fixes"`} {
		if !strings.Contains(r.body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	// Refused: a running check, an unknown run, a stray key, a query string.
	e.runs.add(running(8))
	for name, tc := range map[string]struct {
		path   string
		form   url.Values
		status int
	}{
		"running":   {"/runs/8/fixes/approve-all", url.Values{"csrf_token": {token}}, 409},
		"unknown":   {"/runs/99/fixes/approve-all", url.Values{"csrf_token": {token}}, 404},
		"stray key": {"/runs/7/fixes/approve-all", url.Values{"csrf_token": {token}, "pick": {"2:title"}}, 400},
		"query":     {"/runs/7/fixes/approve-all?x=1", url.Values{"csrf_token": {token}}, 400},
		"no csrf":   {"/runs/7/fixes/approve-all", url.Values{}, 403},
	} {
		before := len(f.recorded())
		if r := e.do(t, "POST", tc.path, tc.form, nil); r.status != tc.status || len(f.recorded()) != before {
			t.Errorf("%s = %d (calls %d → %d), want %d", name, r.status, before, len(f.recorded()), tc.status)
		}
	}
}

// The inbox offers it next to Apply on a finished run.
func TestFixesPageOffersApproveAll(t *testing.T) {
	e := fixingEnv(t, &fakeFixing{approved: 2}, &fakeReports{fixes: report.FixPage{Rows: []report.FixRow{{RunID: 7, BookID: 2, Title: "Two", Tier: "A", Proposal: prop("title", "Old", "New")}}}})
	r := e.do(t, "GET", "/runs/7/fixes", nil, nil)
	for _, want := range []string{`action="/runs/7/fixes/approve-all"`, "Approve all open fixes", `action="/fixes/apply"`} {
		if !strings.Contains(r.body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
}

// An approval a newer check carried shows why it went stale (ADR-034).
func TestCarriedApprovalReason(t *testing.T) {
	if got := reasonText("title", "carried"); got != "a newer check carried this approval" {
		t.Fatalf("reasonText = %q", got)
	}
}
