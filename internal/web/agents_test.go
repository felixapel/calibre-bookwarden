package web

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/agent"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/fix"
	"github.com/felixapel/calibre-bookwarden/internal/report"
	"github.com/felixapel/calibre-bookwarden/internal/store"
	"github.com/felixapel/calibre-bookwarden/internal/testutil/pgtest"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

type fakeAgents struct {
	mu        sync.Mutex
	mode      string
	setErr    error
	reqErr    error
	modes     []string
	requested []int64
	summary   agent.RunSummary
	verdicts  map[agent.Key][]store.Verdict
	outcomes  map[agent.Key]store.Outcome
}

func (f *fakeAgents) Mode(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.mode == "" {
		return store.ModeOff, nil
	}
	return f.mode, nil
}

func (f *fakeAgents) SetMode(_ context.Context, mode, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.setErr != nil {
		return f.setErr
	}
	f.modes = append(f.modes, mode)
	f.mode = mode
	return nil
}

func (f *fakeAgents) Seats() []agent.SeatStatus {
	return []agent.SeatStatus{{Name: "haiku", Model: "claude-haiku-5-5", Family: "anthropic", Configured: true},
		{Name: "deepseek", Model: "deepseek-v4.1-flash", Family: "deepseek", Configured: true,
			QuotaAt: time.Date(2026, 10, 29, 14, 5, 0, 0, time.UTC), QuotaRetryAfter: 90 * time.Second},
		{Name: "gemini"}}
}

func (f *fakeAgents) MaxTokens() int64 { return 20_000_000 }

func (f *fakeAgents) Request(_ context.Context, runID int64, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.reqErr != nil {
		return f.reqErr
	}
	f.requested = append(f.requested, runID)
	return nil
}

func (f *fakeAgents) RunSummary(context.Context, int64) (agent.RunSummary, error) {
	return f.summary, nil
}
func (f *fakeAgents) Verdicts(context.Context, int64) (map[agent.Key][]store.Verdict, error) {
	return f.verdicts, nil
}
func (f *fakeAgents) Outcomes(context.Context, int64) (map[agent.Key]store.Outcome, error) {
	return f.outcomes, nil
}

func agentEnv(t *testing.T, a *fakeAgents, mutate ...func(*Options)) *testEnv {
	t.Helper()
	e := newEnv(t, append([]func(*Options){func(o *Options) { o.Agents = a }}, mutate...)...)
	e.setUpAdmin(t)
	e.logIn(t)
	return e
}

func TestSettingsAgent(t *testing.T) {
	a := &fakeAgents{}
	e := agentEnv(t, a)
	r := e.do(t, "GET", "/settings", nil, nil)
	for _, want := range []string{
		"<h2>Agent fixing</h2>", "Agent fixing: <strong>Off</strong>", `action="/settings/agent"`,
		"<tr><td>haiku</td><td>claude-haiku-5-5</td><td>anthropic</td><td>configured</td></tr>",
		"<tr><td>deepseek</td><td>deepseek-v4.1-flash</td><td>deepseek</td><td>configured; out of quota at 2026-10-29 14:05 UTC, retry after 1m30s</td></tr>",
		"<tr><td>gemini</td><td></td><td></td><td>missing: no key</td></tr>",
		"20000000 tokens per check",
	} {
		if !strings.Contains(r.body, want) {
			t.Errorf("settings page lacks %q", want)
		}
	}
	for _, gone := range []string{"approvals per review", `value="shadow">Shadow</button> <button`, "On approves"} {
		if strings.Contains(r.body, gone) {
			t.Errorf("settings page still has %q", gone)
		}
	}
	token := e.csrfToken(t)
	for _, mode := range []string{"shadow", "off"} {
		if r := e.do(t, "POST", "/settings/agent", url.Values{"csrf_token": {token}, "value": {mode}}, nil); r.status != 303 || r.header.Get("Location") != "/settings" {
			t.Fatalf("%s = %d", mode, r.status)
		}
	}
	if fmt.Sprint(a.modes) != "[shadow off]" {
		t.Fatalf("modes = %v", a.modes)
	}
	a.setErr = fmt.Errorf("%w: gemini", agent.ErrSeatsMissing)
	if r := e.do(t, "POST", "/settings/agent", url.Values{"csrf_token": {token}, "value": {"shadow"}}, nil); r.status != 409 || !strings.Contains(r.body, "A judge isn’t configured: gemini") {
		t.Errorf("missing seat = %d, body lacks the message", r.status)
	}
	a.setErr = nil
	// Mode on is retired: refused with a plain message, never passed on.
	if r := e.do(t, "POST", "/settings/agent", url.Values{"csrf_token": {token}, "value": {"on"}}, nil); r.status != 400 || !strings.Contains(r.body, "can no longer be turned on") {
		t.Errorf("on = %d, want 400 with the message", r.status)
	}
	for _, form := range []url.Values{{"value": {"auto"}}, {}, {"value": {"on", "off"}}} {
		form.Set("csrf_token", token)
		if r := e.do(t, "POST", "/settings/agent", form, nil); r.status != 400 {
			t.Errorf("%v = %d, want 400", form, r.status)
		}
	}
	if r := e.do(t, "POST", "/settings/agent", url.Values{"value": {"on"}}, nil); r.status != 403 {
		t.Errorf("without CSRF = %d, want 403", r.status)
	}
	if fmt.Sprint(a.modes) != "[shadow off]" {
		t.Fatalf("modes = %v", a.modes)
	}
}

func TestSettingsWithoutAgents(t *testing.T) {
	e := newEnv(t)
	e.setUpAdmin(t)
	e.logIn(t)
	if r := e.do(t, "GET", "/settings", nil, nil); strings.Contains(r.body, "Agent fixing") {
		t.Fatal("agent section without an agent service")
	}
	if r := e.do(t, "POST", "/settings/agent", url.Values{"csrf_token": {e.csrfToken(t)}, "value": {"on"}}, nil); r.status != 404 {
		t.Fatalf("POST without an agent service = %d", r.status)
	}
}

func TestReviewButton(t *testing.T) {
	a := &fakeAgents{mode: store.ModeShadow}
	proposal := evidence.Proposal{Field: "title", Current: []string{"Old"}, Proposed: []string{"New"}, Roots: []string{"dnb", "file"}}
	f := &fakeReports{fixes: report.FixPage{Rows: []report.FixRow{{RunID: 5, BookID: 2, Title: "T", Tier: "A", Proposal: proposal}}}}
	e := agentEnv(t, a, func(o *Options) { o.Reports = f })
	e.runs.add(verify.RunView{Run: verify.Run{ID: 5, Status: verify.RunCompleted}})
	e.runs.add(running(7))
	if r := e.do(t, "GET", "/runs/5/fixes", nil, nil); !strings.Contains(r.body, `action="/runs/5/fixes/review"`) || !strings.Contains(r.body, "Review with agents") {
		t.Fatal("no review button on a finished run")
	}
	if r := e.do(t, "GET", "/runs/7/fixes", nil, nil); strings.Contains(r.body, "Review with agents") {
		t.Fatal("review button on a running run")
	}
	token := e.csrfToken(t)
	if r := e.do(t, "POST", "/runs/5/fixes/review", url.Values{"csrf_token": {token}}, nil); r.status != 303 || r.header.Get("Location") != "/runs/5/fixes" {
		t.Fatalf("review = %d → %s", r.status, r.header.Get("Location"))
	}
	if fmt.Sprint(a.requested) != "[5]" {
		t.Fatalf("requested = %v", a.requested)
	}
	if r := e.do(t, "POST", "/runs/7/fixes/review", url.Values{"csrf_token": {token}}, nil); r.status != 409 || !strings.Contains(r.body, "still running") {
		t.Fatalf("running run = %d", r.status)
	}
	for _, tc := range []struct {
		err  error
		want string
	}{
		{agent.ErrOff, "Agent fixing is off"},
		{agent.ErrQueued, "already waiting"},
		{fmt.Errorf("%w: gemini", agent.ErrSeatsMissing), "A judge isn’t configured: gemini"},
	} {
		a.reqErr = tc.err
		if r := e.do(t, "POST", "/runs/5/fixes/review", url.Values{"csrf_token": {token}}, nil); r.status != 409 || !strings.Contains(r.body, tc.want) {
			t.Errorf("%v = %d, body lacks %q", tc.err, r.status, tc.want)
		}
	}
	if r := e.do(t, "POST", "/runs/99/fixes/review", url.Values{"csrf_token": {token}}, nil); r.status != 404 {
		t.Errorf("unknown run = %d", r.status)
	}
}

func TestRunPageAgents(t *testing.T) {
	a := &fakeAgents{}
	e := agentEnv(t, a)
	e.runs.add(verify.RunView{Run: verify.Run{ID: 5, Status: verify.RunCompleted}})
	if r := e.do(t, "GET", "/runs/5", nil, nil); strings.Contains(r.body, "Agents:") {
		t.Fatal("agent summary before any review")
	}
	a.summary = agent.RunSummary{Reviewed: true, Approved: 3, Applied: 2, Rejected: 2, ForYou: 1, Tokens: map[string]int64{"haiku": 1200, "gemini": 3400}}
	r := e.do(t, "GET", "/runs/5", nil, nil)
	for _, want := range []string{"Agents: 3 approved (2 applied), 2 rejected, 1 for you", "gemini 3400", "haiku 1200"} {
		if !strings.Contains(r.body, want) {
			t.Errorf("run page lacks %q", want)
		}
	}
}

// A run the agents decided nothing on shows no approved or rejected counts.
func TestRunPageAgentsWithoutDecisions(t *testing.T) {
	a := &fakeAgents{summary: agent.RunSummary{Reviewed: true, ForYou: 4}}
	e := agentEnv(t, a)
	e.runs.add(verify.RunView{Run: verify.Run{ID: 5, Status: verify.RunCompleted}})
	r := e.do(t, "GET", "/runs/5", nil, nil)
	if !strings.Contains(r.body, "Agents: 4 for you") || strings.Contains(r.body, "approved") || strings.Contains(r.body, "rejected,") {
		t.Errorf("run page = %q", r.body)
	}
}

// A stored mode "on" shows as Shadow on the settings page, read through the
// real repository.
func TestSettingsStoredOnShowsShadow(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, pgtest.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool.Exec(ctx, `INSERT INTO settings (name, value, changed_by) VALUES ('agent_fixing', '"on"'::jsonb, 'felix')`); err != nil {
		t.Fatal(err)
	}
	svc := agent.NewService(agent.ServiceOptions{Store: store.NewAgentRepo(st), Seats: []agent.SeatStatus{{Name: "haiku", Configured: true}}})
	e := agentEnv(t, nil, func(o *Options) { o.Agents = svc })
	r := e.do(t, "GET", "/settings", nil, nil)
	if !strings.Contains(r.body, "Agent fixing: <strong>Shadow</strong>") {
		t.Errorf("settings page does not show Shadow for a stored on")
	}
}

// agentFixes is a run with three proposals: one the agents approved, one a
// person rejected, one the agents left.
func agentFixes(t *testing.T) (*testEnv, *fakeAgents, *fakeFixing) {
	t.Helper()
	p := func(f string) evidence.Proposal {
		return evidence.Proposal{Field: f, Current: []string{"Old"}, Proposed: []string{"New"}, Roots: []string{"calibre_rule"}}
	}
	reports := &fakeReports{fixes: report.FixPage{Rows: []report.FixRow{
		{RunID: 5, BookID: 1, Title: "One", Tier: "A", Proposal: p("title_sort")},
		{RunID: 5, BookID: 2, Title: "Two", Tier: "A", Proposal: p("title_sort")},
		{RunID: 5, BookID: 3, Title: "Three", Tier: "A", Proposal: p("author_sort")},
	}}}
	fixing := &fakeFixing{proposals: []fix.ProposalRow{
		{ID: 1, RunID: 5, BookID: 1, Proposal: p("title_sort"), Status: store.ProposalApproved, DecidedBy: store.AgentActor},
		{ID: 2, RunID: 5, BookID: 2, Proposal: p("title_sort"), Status: store.ProposalRejected, DecidedBy: "admin"},
	}}
	v := func(seat, answer, reason, code string) store.Verdict {
		return store.Verdict{Seat: seat, Answer: answer, Reason: reason, ErrorCode: code}
	}
	a := &fakeAgents{
		mode: store.ModeOn,
		verdicts: map[agent.Key][]store.Verdict{
			{BookID: 1, Field: "title_sort"}:  {v("haiku", "approve", "Calibre’s rule", ""), v("deepseek", "approve", "ok", ""), v("gemini", "approve", "ok", "")},
			{BookID: 3, Field: "author_sort"}: {v("haiku", "approve", "fine", ""), v("deepseek", "unsure", "editor?", ""), v("gemini", "", "", "timeout")},
		},
		outcomes: map[agent.Key]store.Outcome{
			{BookID: 1, Field: "title_sort"}:  {Outcome: store.OutcomeApproved, Reason: "all three judges approve"},
			{BookID: 3, Field: "author_sort"}: {Outcome: store.OutcomeLeft, Reason: "gemini failed: timeout"},
		},
	}
	e := agentEnv(t, a, func(o *Options) { o.Reports = reports; o.Fixing = fixing })
	e.runs.add(verify.RunView{Run: verify.Run{ID: 5, Status: verify.RunCompleted}})
	return e, a, fixing
}

// TestFixesVerdicts: the agents' verdicts and note stay hidden for an
// undecided row until the page is asked to show them; a decided row always
// shows its verdicts (ADR-034).
func TestFixesVerdicts(t *testing.T) {
	e, _, _ := agentFixes(t)
	blind := e.do(t, "GET", "/runs/5/fixes", nil, nil)
	for _, want := range []string{"by agent", "haiku: approve · Calibre’s rule", "Show agents’ verdicts", `href="/runs/5/fixes?verdicts=1"`} {
		if !strings.Contains(blind.body, want) {
			t.Errorf("blind page lacks %q", want)
		}
	}
	for _, hidden := range []string{"deepseek: unsure · editor?", "gemini: failed: timeout", "Agents: gemini failed: timeout"} {
		if strings.Contains(blind.body, hidden) {
			t.Errorf("blind page shows %q for an undecided row", hidden)
		}
	}
	r := e.do(t, "GET", "/runs/5/fixes?verdicts=1", nil, nil)
	for _, want := range []string{
		"by agent", "haiku: approve · Calibre’s rule", "deepseek: unsure · editor?", "gemini: failed: timeout",
		"Agents: gemini failed: timeout", `name="verdicts" value="1"`, "Hide agents’ verdicts",
	} {
		if !strings.Contains(r.body, want) {
			t.Errorf("revealed page lacks %q", want)
		}
	}
	if r := e.do(t, "GET", "/runs/5/fixes?verdicts=0", nil, nil); r.status != 400 {
		t.Errorf("verdicts=0 = %d, want 400", r.status)
	}
	// Filtering by what the panel left for lack of unanimity is itself a
	// look at the verdicts.
	d := e.do(t, "GET", "/runs/5/fixes?decided=disagree", nil, nil)
	if !strings.Contains(d.body, "Agents: gemini failed: timeout") || !strings.Contains(d.body, `name="verdicts" value="1"`) {
		t.Error("the disagree filter doesn't reveal the verdicts")
	}
}

// TestRevealIsRemembered: showing the verdicts records the undecided fixes
// on screen, so hiding them again can't make a later decision blind.
func TestRevealIsRemembered(t *testing.T) {
	e, _, fixing := agentFixes(t)
	e.do(t, "GET", "/runs/5/fixes", nil, nil)
	if got := fixing.revealed(); len(got) != 0 {
		t.Fatalf("a blind page recorded reveals %+v", got)
	}
	want := []store.RevealKey{{BookID: 3, Field: "author_sort"}}
	for _, q := range []string{"verdicts=1", "decided=disagree"} {
		fixing.mu.Lock()
		fixing.reveals = nil
		fixing.mu.Unlock()
		if r := e.do(t, "GET", "/runs/5/fixes?"+q, nil, nil); r.status != 200 {
			t.Fatalf("%s = %d", q, r.status)
		}
		if got := fixing.revealed(); !slices.Equal(got, want) {
			t.Errorf("%s recorded %+v, want %+v (only the undecided rows)", q, got, want)
		}
	}
}

// TestRevealedDecisionRecordsVerdictsShown: a decision from the revealed page
// is not a blind label.
func TestRevealedDecisionRecordsVerdictsShown(t *testing.T) {
	e, _, fixing := agentFixes(t)
	token := e.csrfToken(t)
	for _, tc := range []struct {
		verdicts string
		want     bool
	}{{"", false}, {"1", true}} {
		form := url.Values{"csrf_token": {token}, "action": {"approve"}, "pick": {"3:author_sort"}, "mode": {"individual"}}
		if tc.verdicts != "" {
			form.Set("verdicts", tc.verdicts)
		}
		if r := e.do(t, "POST", "/runs/5/fixes/decide", form, nil); r.status != 303 {
			t.Fatalf("decide = %d: %s", r.status, r.body)
		}
		calls := fixing.recorded()
		if c := calls[len(calls)-1]; c.Basis.Mode != store.DecisionIndividual || c.Basis.VerdictsShown != tc.want {
			t.Errorf("verdicts=%q recorded %+v", tc.verdicts, c.Basis)
		}
	}
}

func TestFixesFilterDecided(t *testing.T) {
	e, _, _ := agentFixes(t)
	for _, tc := range []struct {
		decided    string
		has, lacks []string
	}{
		{"agent", []string{"1 · One"}, []string{"2 · Two", "3 · Three"}},
		{"person", []string{"2 · Two"}, []string{"1 · One", "3 · Three"}},
		{"disagree", []string{"3 · Three"}, []string{"1 · One", "2 · Two"}},
		{"", []string{"1 · One", "2 · Two", "3 · Three"}, nil},
	} {
		r := e.do(t, "GET", "/runs/5/fixes?decided="+tc.decided, nil, nil)
		if r.status != 200 {
			t.Fatalf("decided=%s: %d", tc.decided, r.status)
		}
		for _, s := range tc.has {
			if !strings.Contains(r.body, s) {
				t.Errorf("decided=%s lacks %q", tc.decided, s)
			}
		}
		for _, s := range tc.lacks {
			if strings.Contains(r.body, s) {
				t.Errorf("decided=%s shows %q", tc.decided, s)
			}
		}
		if tc.decided != "" && !strings.Contains(r.body, `name="decided" value="`+tc.decided+`"`) {
			t.Errorf("decided=%s: the decide forms don't keep the filter", tc.decided)
		}
	}
	if r := e.do(t, "GET", "/runs/5/fixes?decided=robot", nil, nil); r.status != 400 {
		t.Errorf("invalid decided = %d, want 400", r.status)
	}
	if r := e.do(t, "GET", "/runs/5/fixes?decided=agent", nil, nil); !strings.Contains(r.body, "2 rows on this page are hidden by the filter") {
		t.Error("no note about hidden rows")
	}
}

func TestOperationShowsAgent(t *testing.T) {
	e, _, fixing := agentFixes(t)
	run, op := int64(5), int64(9)
	fixing.proposals[0].OperationID = &op
	fixing.proposals = append(fixing.proposals, fix.ProposalRow{ID: 3, RunID: 5, BookID: 1, OperationID: &op, Status: store.ProposalApplied, DecidedBy: "admin",
		Proposal: evidence.Proposal{Field: "publisher", Proposed: []string{"P"}}})
	fixing.ops = []fix.Operation{{ID: op, Kind: store.OpApply, RunID: &run, BookID: 1, LibraryID: "library", State: store.OpCompleted, RequestedBy: store.AgentActor}}
	r := e.do(t, "GET", "/history/9", nil, nil)
	for _, want := range []string{"title_sort: approved by agent", "publisher: approved by admin", `href="/runs/5/fixes?book_id=1"`} {
		if !strings.Contains(r.body, want) {
			t.Errorf("operation page lacks %q", want)
		}
	}
}

// TestAgentUIHiddenWhenOff: with agent fixing off, the Fixes page offers
// neither the review button nor the agent filter, as before agent fixing.
func TestAgentUIHiddenWhenOff(t *testing.T) {
	e, a, _ := agentFixes(t)
	a.mode = store.ModeOff
	r := e.do(t, "GET", "/runs/5/fixes", nil, nil)
	for _, unwanted := range []string{"Review with agents", `name="decided"`} {
		if strings.Contains(r.body, unwanted) {
			t.Errorf("fixes page shows %q while agent fixing is off", unwanted)
		}
	}
}

// The Decided: disagree filter lists only what the panel left for lack of
// unanimity, not shadow, ceiling, paused or refused leftovers.
func TestFixesFilterDisagree(t *testing.T) {
	prop := func(f string) evidence.Proposal {
		return evidence.Proposal{Field: f, Current: []string{"Old"}, Proposed: []string{"New"}, Roots: []string{"calibre_rule"}}
	}
	titles := []string{"Split", "Unsure", "Failed", "Silent", "Shadow", "Ceiling", "Paused", "Refused", "Person"}
	reasons := []string{"agents disagree", "agents unsure", "gemini failed: timeout", "deepseek didn't answer",
		"shadow: approved", "agent ceiling reached", "writes paused by an incident", "approve refused: no longer sealed", "decided by a person"}
	var rows []report.FixRow
	outcomes := map[agent.Key]store.Outcome{}
	for i, title := range titles {
		id := i + 1
		rows = append(rows, report.FixRow{RunID: 5, BookID: id, Title: title, Tier: "A", Proposal: prop("title_sort")})
		outcomes[agent.Key{BookID: id, Field: "title_sort"}] = store.Outcome{Outcome: store.OutcomeLeft, Reason: reasons[i]}
	}
	a := &fakeAgents{mode: store.ModeOn, outcomes: outcomes}
	e := agentEnv(t, a, func(o *Options) {
		o.Reports = &fakeReports{fixes: report.FixPage{Rows: rows}}
		o.Fixing = &fakeFixing{}
	})
	e.runs.add(verify.RunView{Run: verify.Run{ID: 5, Status: verify.RunCompleted}})
	r := e.do(t, "GET", "/runs/5/fixes?decided=disagree", nil, nil)
	if r.status != 200 {
		t.Fatalf("status %d", r.status)
	}
	for i, title := range titles {
		shown := strings.Contains(r.body, fmt.Sprintf("%d · %s", i+1, title))
		if want := i < 4; shown != want {
			t.Errorf("%s (%q) shown = %v, want %v", title, reasons[i], shown, want)
		}
	}
}
