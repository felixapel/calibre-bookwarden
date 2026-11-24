package web

import (
	"net/url"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/fix"
	"github.com/felixapel/calibre-bookwarden/internal/report"
	"github.com/felixapel/calibre-bookwarden/internal/store"
)

// TestDecisionModes: how a request decides is recorded as the fix's basis
// (ADR-034): the per-row buttons are individual, everything ticked or "all
// shown" is bulk, and a reveal or an earlier decision means verdicts shown.
func TestDecisionModes(t *testing.T) {
	rows := []report.FixRow{
		{RunID: 7, BookID: 2, Title: "Two", Tier: "A", Proposal: prop("title", "Old", "New")},
		{RunID: 7, BookID: 3, Title: "Three", Tier: "A", Proposal: prop("title", "Old", "New")},
	}
	f := &fakeFixing{proposals: []fix.ProposalRow{{RunID: 7, BookID: 3, Proposal: rows[1].Proposal, Status: "approved", DecidedBy: "agent"}}}
	e := fixingEnv(t, f, &fakeReports{fixes: report.FixPage{Rows: rows}})
	token := e.csrfToken(t)
	post := func(form url.Values) response {
		t.Helper()
		form.Set("csrf_token", token)
		return e.do(t, "POST", "/runs/7/fixes/decide", form, nil)
	}
	last := func() fixCall {
		t.Helper()
		calls := f.recorded()
		if len(calls) == 0 {
			t.Fatal("no call")
		}
		return calls[len(calls)-1]
	}

	page := e.do(t, "GET", "/runs/7/fixes", nil, nil)
	for _, want := range []string{`form="one-2-title"`, `form="one-3-title"`, `id="one-2-title"`, `name="mode" value="individual"`} {
		if !strings.Contains(page.body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if n := strings.Count(page.body, `value="approve"`); n < 4 { // two rows, ticked, all shown
		t.Errorf("approve buttons: %d", n)
	}

	for _, tc := range []struct {
		name string
		form url.Values
		want fix.Basis
		call string
	}{
		{"row approve, blind", url.Values{"action": {"approve"}, "pick": {"2:title"}, "mode": {"individual"}}, fix.Basis{Mode: store.DecisionIndividual}, "Approve"},
		{"row reject, blind", url.Values{"action": {"reject"}, "pick": {"2:title"}, "mode": {"individual"}}, fix.Basis{Mode: store.DecisionIndividual}, "Reject"},
		{"row approve, revealed", url.Values{"action": {"approve"}, "pick": {"2:title"}, "mode": {"individual"}, "verdicts": {"1"}}, fix.Basis{Mode: store.DecisionIndividual, VerdictsShown: true}, "Approve"},
		{"row on a decided fix saw its verdicts", url.Values{"action": {"reject"}, "pick": {"3:title"}, "mode": {"individual"}}, fix.Basis{Mode: store.DecisionIndividual, VerdictsShown: true}, "Reject"},
		{"row under the disagree filter saw verdicts", url.Values{"action": {"approve"}, "pick": {"2:title"}, "mode": {"individual"}, "decided": {"disagree"}}, fix.Basis{Mode: store.DecisionIndividual, VerdictsShown: true}, "Approve"},
		{"ticked", url.Values{"action": {"approve"}, "pick": {"2:title"}}, fix.Basis{Mode: store.DecisionBulk}, "Approve"},
		{"ticked, revealed", url.Values{"action": {"reject"}, "pick": {"2:title"}, "verdicts": {"1"}}, fix.Basis{Mode: store.DecisionBulk, VerdictsShown: true}, "Reject"},
	} {
		before := len(f.recorded())
		if r := post(tc.form); r.status != 303 {
			t.Fatalf("%s = %d: %s", tc.name, r.status, r.body)
		}
		if len(f.recorded()) != before+1 {
			t.Fatalf("%s: %d calls", tc.name, len(f.recorded())-before)
		}
		if c := last(); c.Method != tc.call || c.Basis != tc.want {
			t.Errorf("%s: %+v, want %s with %+v", tc.name, c, tc.call, tc.want)
		}
	}

	// A ticked batch mixing a decided and an undecided fix of one book is
	// two calls, so only the decided one counts as shown.
	f.proposals = append(f.proposals, fix.ProposalRow{RunID: 7, BookID: 2, Proposal: prop("authors", "A", "B"), Status: "rejected", DecidedBy: "admin"})
	before := len(f.recorded())
	if r := post(url.Values{"action": {"approve"}, "pick": {"2:title", "2:authors"}}); r.status != 303 {
		t.Fatalf("mixed = %d", r.status)
	}
	got := f.recorded()[before:]
	if len(got) != 2 || got[0].Basis.VerdictsShown == got[1].Basis.VerdictsShown {
		t.Errorf("mixed calls %+v", got)
	}

	for name, form := range map[string]url.Values{
		"individual with two picks": {"action": {"approve"}, "pick": {"2:title", "3:title"}, "mode": {"individual"}},
		"unknown mode":              {"action": {"approve"}, "pick": {"2:title"}, "mode": {"agent"}},
		"two modes":                 {"action": {"approve"}, "pick": {"2:title"}, "mode": {"individual", "bulk"}},
		"bad verdicts flag":         {"action": {"approve"}, "pick": {"2:title"}, "verdicts": {"yes"}},
	} {
		before := len(f.recorded())
		if r := post(form); r.status != 400 || len(f.recorded()) != before {
			t.Errorf("%s = %d, calls %d", name, r.status, len(f.recorded())-before)
		}
	}

	// A decision from a revealed page comes back to the revealed page.
	r := post(url.Values{"action": {"approve"}, "pick": {"2:title"}, "mode": {"individual"}, "verdicts": {"1"}})
	if r.header.Get("Location") != "/runs/7/fixes?verdicts=1" {
		t.Errorf("Location %q", r.header.Get("Location"))
	}
}

func TestPrecisionPage(t *testing.T) {
	f := &fakeFixing{labels: []fix.TypeLabels{
		{Field: "publisher", Path: "dnb+file", Kind: "fill", Labels: 60, Misses: 0},
		{Field: "title", Path: "calibre_rule", Kind: "replace", Labels: 299, Misses: 0},
		{Field: "authors", Path: "dnb", Kind: "replace", Labels: 100, Misses: 1},
		{Field: "isbn", Path: "file", Kind: "fill", Labels: 0, Misses: 0},
		{Field: "title_sort", Path: "calibre_rule", Kind: "replace", Labels: 700, Misses: 0},
	}}
	e := fixingEnv(t, f, nil)
	r := e.do(t, "GET", "/precision", nil, nil)
	if r.status != 200 || r.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("precision = %d", r.status)
	}
	for _, want := range []string{
		"dnb&#43;file", "fill", "95.13", "60 / 598 with zero misses",
		"299 / 598 with zero misses", "99.00", "95.34", "no labels yet",
		"700 with zero misses: target reached",
	} {
		if !strings.Contains(r.body, want) {
			t.Errorf("precision page lacks %q", want)
		}
	}
	if strings.Contains(r.body, "100 / 598") {
		t.Error("a type with a miss shows zero-miss progress")
	}
	if r := e.do(t, "GET", "/precision?x=1", nil, nil); r.status != 400 {
		t.Errorf("query = %d", r.status)
	}
	empty := fixingEnv(t, &fakeFixing{}, nil)
	if r := empty.do(t, "GET", "/precision", nil, nil); r.status != 200 || !strings.Contains(r.body, "No labels yet") {
		t.Errorf("empty = %d", r.status)
	}
}
