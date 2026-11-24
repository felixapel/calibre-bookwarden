package web

import (
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/agent"
	"github.com/felixapel/calibre-bookwarden/internal/fix"
	"github.com/felixapel/calibre-bookwarden/internal/report"
	"github.com/felixapel/calibre-bookwarden/internal/store"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

func inboxEnv(t *testing.T, f *fakeFixing, rows ...report.FixRow) (*testEnv, *fakeReports) {
	t.Helper()
	rep := &fakeReports{fixes: report.FixPage{Rows: rows}}
	return fixingEnv(t, f, rep), rep
}

func TestFixesQueryAndSortReachTheReport(t *testing.T) {
	e, rep := inboxEnv(t, &fakeFixing{}, report.FixRow{RunID: 7, BookID: 2, Title: "Café", Tier: "A", Proposal: prop("title", "Old", "New")})
	for _, tc := range []struct {
		query string
		want  report.FixFilter
	}{
		{"q=caf%C3%A9", report.FixFilter{Query: "café"}},
		{"q=++Saramago+&sort=field", report.FixFilter{Query: "Saramago", Sort: "field"}},
		{"sort=tier", report.FixFilter{Sort: "tier"}},
		{"sort=decided", report.FixFilter{Sort: "decided"}},
		{"sort=book", report.FixFilter{Sort: "book"}},
		{"sort=nonsense", report.FixFilter{}}, // an unknown sort is the default
		{"q=", report.FixFilter{}},
	} {
		r := e.do(t, "GET", "/runs/7/fixes?"+tc.query, nil, nil)
		if r.status != 200 || !reflect.DeepEqual(rep.fixFilter, tc.want) {
			t.Errorf("%s: %d, filter %+v, want %+v", tc.query, r.status, rep.fixFilter, tc.want)
		}
	}
	long := strings.Repeat("a", report.MaxFixQuery+1)
	if r := e.do(t, "GET", "/runs/7/fixes?q="+long, nil, nil); r.status != 400 {
		t.Errorf("an over-long query = %d, want 400", r.status)
	}
	// The search and the sort survive the decide forms, the filter form and the next link.
	rep.fixes.Next = &report.Cursor{RunID: 7, BookID: 2, Ordinal: 0, Offset: 1}
	r := e.do(t, "GET", "/runs/7/fixes?q=caf%C3%A9&sort=field", nil, nil)
	for _, want := range []string{`name="q" value="café"`, `name="sort" value="field"`, `<option value="field" selected>`} {
		if !strings.Contains(r.body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if n := strings.Count(r.body, `name="sort" value="field"`); n < 3 {
		t.Errorf("the sort is kept by %d forms, want the decide forms too", n)
	}
	next := regexp.MustCompile(`href="([^"]*cursor=[^"]*)"`).FindStringSubmatch(r.body)
	if next == nil || !strings.Contains(next[1], "q=caf%C3%A9") || !strings.Contains(next[1], "sort=field") {
		t.Errorf("the next link drops the search or the sort: %v", next)
	}
}

// A search never shows the agents' verdicts, and neither does a sort.
func TestSearchKeepsVerdictsHidden(t *testing.T) {
	e, _, _ := agentFixes(t)
	for _, path := range []string{"/runs/5/fixes?q=two", "/runs/5/fixes?q=two&sort=decided", "/runs/5/fixes?sort=field"} {
		r := e.do(t, "GET", path, nil, nil)
		for _, hidden := range []string{"deepseek: unsure · editor?", "gemini: failed: timeout", "Agents: gemini failed: timeout"} {
			if strings.Contains(r.body, hidden) {
				t.Errorf("%s shows %q for an undecided row", path, hidden)
			}
		}
		if strings.Contains(r.body, `name="verdicts"`) {
			t.Errorf("%s asks the decide forms to reveal", path)
		}
	}
}

func TestFixesDisagreeFilterPassesTheLeftKeys(t *testing.T) {
	a := &fakeAgents{mode: store.ModeOn, outcomes: map[agent.Key]store.Outcome{
		{BookID: 1, Field: "title"}: {Outcome: store.OutcomeLeft, Reason: "agents disagree"},
		{BookID: 2, Field: "title"}: {Outcome: store.OutcomeLeft, Reason: "shadow: approved"},
		{BookID: 3, Field: "title"}: {Outcome: store.OutcomeApproved},
		{BookID: 4, Field: "isbn"}:  {Outcome: store.OutcomeLeft, Reason: "gemini failed: timeout"},
	}}
	rep := &fakeReports{}
	e := agentEnv(t, a, func(o *Options) { o.Reports = rep; o.Fixing = &fakeFixing{} })
	e.runs.add(verify.RunView{Run: verify.Run{ID: 5, Status: verify.RunCompleted}})
	if r := e.do(t, "GET", "/runs/5/fixes?decided=disagree", nil, nil); r.status != 200 {
		t.Fatalf("status %d", r.status)
	}
	got := rep.fixFilter.Keys
	want := []report.FixKey{{BookID: 1, Field: "title"}, {BookID: 4, Field: "isbn"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("keys = %v, want %v", got, want)
	}
}

func TestFixesRowsCarryTheAttributesTheKeysUse(t *testing.T) {
	rows := []report.FixRow{
		{RunID: 7, BookID: 2, Title: "Two", Tier: "A", Proposal: prop("title", "Old", "New")},
		{RunID: 7, BookID: 3, Title: "Three", Tier: "A", Proposal: prop("authors", "Old", "New")},
	}
	f := &fakeFixing{proposals: []fix.ProposalRow{{RunID: 7, BookID: 3, Proposal: rows[1].Proposal, Status: "applied", DecidedBy: "admin"}}}
	e, _ := inboxEnv(t, f, rows...)
	r := e.do(t, "GET", "/runs/7/fixes", nil, nil)
	// Each row has a stable id (book and field), so the script can find the
	// row after the one it acted on once the page has reloaded.
	for _, want := range []string{`<tr data-row="2-title" tabindex="0"`, `<tr data-row="3-authors" tabindex="0"`} {
		if !strings.Contains(r.body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if n := strings.Count(r.body, `<tr data-row="`); n != 2 {
		t.Errorf("rows with data-row: %d, want 2", n)
	}
	for _, want := range []string{
		`<button type="submit" form="one-2-title" name="action" value="approve" data-act="approve">`,
		`<button type="submit" form="one-2-title" name="action" value="reject" data-act="reject">`,
	} {
		if !strings.Contains(r.body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	// The applied row can't be decided, so it has no button for a key to press.
	if strings.Contains(r.body, `form="one-3-authors"`) || strings.Count(r.body, `data-act=`) != 2 {
		t.Error("a row that can't be decided has decision buttons")
	}
	// Exactly the individual form: the keys never reach the bulk ones.
	if !strings.Contains(r.body, `id="one-2-title"`) || !strings.Contains(r.body, `name="mode" value="individual"`) {
		t.Error("the per-row form is not the individual one")
	}
}

func TestFixesPageLoadsTheInboxScriptUnderTheSameCSP(t *testing.T) {
	e, _ := inboxEnv(t, &fakeFixing{}, report.FixRow{RunID: 7, BookID: 2, Title: "Two", Tier: "A", Proposal: prop("title", "Old", "New")})
	r := e.do(t, "GET", "/runs/7/fixes", nil, nil)
	if !strings.Contains(r.body, `<script src="/static/inbox.js" defer></script>`) {
		t.Error("the Fixes page does not load inbox.js")
	}
	if strings.Contains(r.body, "<script>") {
		t.Error("an inline script")
	}
	if got := r.header.Get("Content-Security-Policy"); got != contentSecurityPolicy {
		t.Errorf("CSP = %q", got)
	}
	if !strings.Contains(contentSecurityPolicy, "script-src 'self';") || strings.Contains(contentSecurityPolicy, "unsafe") {
		t.Errorf("the policy changed: %q", contentSecurityPolicy)
	}
	js := e.do(t, "GET", "/static/inbox.js", nil, nil)
	if js.status != 200 || !strings.Contains(js.body, "data-row") {
		t.Errorf("inbox.js = %d", js.status)
	}
	for _, path := range []string{"/runs", "/findings", "/precision", "/"} {
		if p := e.do(t, "GET", path, nil, nil); strings.Contains(p.body, "inbox.js") {
			t.Errorf("%s loads inbox.js", path)
		}
	}
	raw, err := os.ReadFile("static/inbox.js")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(raw), "\n"); n >= 150 {
		t.Errorf("inbox.js has %d lines, want under 150", n)
	}
}

// A carried decision, rejection or approval (ADR-034), names the run it
// was carried from and stays decidable.
func TestCarriedDecisionIsShownWithItsRun(t *testing.T) {
	for _, status := range []string{"rejected", "approved"} {
		row := report.FixRow{RunID: 7, BookID: 2, Title: "Two", Tier: "A", Proposal: prop("title", "Old", "New")}
		from := int64(11)
		f := &fakeFixing{proposals: []fix.ProposalRow{{RunID: 7, BookID: 2, Proposal: row.Proposal, Status: status, DecidedBy: "admin",
			DecisionMode: store.DecisionCarried, VerdictsShown: true, CarriedFrom: &from, CarriedFromRun: 5, CarriedFromBook: 20}}}
		e, _ := inboxEnv(t, f, row)
		r := e.do(t, "GET", "/runs/7/fixes", nil, nil)
		for _, want := range []string{strings.ToUpper(status[:1]) + status[1:], `carried from <a href="/runs/5/books/20">run 5</a>`} {
			if !strings.Contains(r.body, want) {
				t.Errorf("%s: page lacks %q", status, want)
			}
		}
		if !strings.Contains(r.body, `form="one-2-title"`) {
			t.Errorf("a carried %s fix can't be changed", status)
		}
	}
}

func TestSortIsDisabledForARunWithoutACheck(t *testing.T) {
	row := report.FixRow{RunID: 7, BookID: 2, Title: "Two", Tier: "A", Proposal: prop("title", "Old", "New")}
	e, rep := inboxEnv(t, &fakeFixing{}, row)
	r := e.do(t, "GET", "/runs/7/fixes", nil, nil)
	if strings.Contains(r.body, `<select id="sort" name="sort" disabled`) || strings.Contains(r.body, "sorting needs a checked run") {
		t.Error("a checked run's sort is disabled")
	}
	rep.fixes.Walked = true
	r = e.do(t, "GET", "/runs/7/fixes", nil, nil)
	for _, want := range []string{`<select id="sort" name="sort" disabled`, "sorting needs a checked run"} {
		if !strings.Contains(r.body, want) {
			t.Errorf("a walked page lacks %q", want)
		}
	}
}
