package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/fix"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/report"
	"github.com/felixapel/calibre-bookwarden/internal/store"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

// fixCall is one call that changes something.
type fixCall struct {
	Method string
	RunID  int64
	BookID int
	Fields []string
	Basis  fix.Basis
	ID     int64
	On     bool
}

type fakeFixing struct {
	mu        sync.Mutex
	calls     []fixCall
	reveals   []store.RevealKey
	proposals []fix.ProposalRow
	approved  int
	decideErr map[int]error    // by book
	fieldErr  map[string]error // by "book:field", for one-field calls; before decideErr
	startErr  error
	status    fix.ApplyStatus
	ops       []fix.Operation
	events    map[int64][]fix.Event
	undoable  bool
	undoID    int64
	undoErr   error
	incidents []fix.Incident
	ackErr    error
	retryErr  error
	paused    bool
	writesOn  bool
	settings  []fix.SettingChange
	labels    []fix.TypeLabels
	mute      []fix.TypeMute
	muteErr   error
	unmutes   []unmuteCall
	unmuteErr error
}

// unmuteCall is one recorded unmute.
type unmuteCall struct {
	Library string
	Type    fix.MuteType
	By      string
}

func (f *fakeFixing) record(c fixCall) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, c)
}

func (f *fakeFixing) recorded() []fixCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func (f *fakeFixing) Approve(_ context.Context, runID int64, bookID int, fields []string, _ string, basis fix.Basis) error {
	f.record(fixCall{Method: "Approve", RunID: runID, BookID: bookID, Fields: fields, Basis: basis})
	if err := f.fieldErr[fmt.Sprintf("%d:%s", bookID, strings.Join(fields, ","))]; err != nil {
		return err
	}
	return f.decideErr[bookID]
}

func (f *fakeFixing) Reject(_ context.Context, runID int64, bookID int, fields []string, _ string, basis fix.Basis) error {
	f.record(fixCall{Method: "Reject", RunID: runID, BookID: bookID, Fields: fields, Basis: basis})
	return f.decideErr[bookID]
}

func (f *fakeFixing) RunProposals(context.Context, int64) ([]fix.ProposalRow, error) {
	return f.proposals, nil
}

func (f *fakeFixing) RecordReveals(_ context.Context, _ int64, keys []store.RevealKey) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reveals = append(f.reveals, keys...)
	return nil
}

func (f *fakeFixing) revealed() []store.RevealKey {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.reveals)
}

func (f *fakeFixing) Precision(context.Context) ([]fix.TypeLabels, error) { return f.labels, nil }

func (f *fakeFixing) MuteStats(context.Context) ([]fix.TypeMute, error) { return f.mute, f.muteErr }

func (f *fakeFixing) Unmute(_ context.Context, libraryID string, t fix.MuteType, by string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unmutes = append(f.unmutes, unmuteCall{libraryID, t, by})
	return f.unmuteErr
}

func (f *fakeFixing) ApprovedCount(context.Context) (int, error) { return f.approved, nil }

func (f *fakeFixing) StartApply(context.Context, string) error {
	f.record(fixCall{Method: "StartApply"})
	return f.startErr
}

func (f *fakeFixing) ApplyStatus() fix.ApplyStatus { return f.status }

func (f *fakeFixing) Operations(_ context.Context, before int64, limit int) ([]fix.Operation, error) {
	var out []fix.Operation
	for _, op := range f.ops {
		if (before <= 0 || op.ID < before) && len(out) < limit {
			out = append(out, op)
		}
	}
	return out, nil
}

func (f *fakeFixing) Operation(_ context.Context, id int64) (fix.Operation, error) {
	for _, op := range f.ops {
		if op.ID == id {
			return op, nil
		}
	}
	return fix.Operation{}, fix.ErrNoOperation
}

func (f *fakeFixing) Events(_ context.Context, id int64) ([]fix.Event, error) {
	return f.events[id], nil
}

func (f *fakeFixing) Undoable(context.Context, int64) (bool, error) { return f.undoable, nil }

func (f *fakeFixing) Undo(_ context.Context, id int64, _ string) (int64, error) {
	f.record(fixCall{Method: "Undo", ID: id})
	return f.undoID, f.undoErr
}

func (f *fakeFixing) Incidents(context.Context) ([]fix.Incident, error) { return f.incidents, nil }

func (f *fakeFixing) Acknowledge(_ context.Context, id int64, _ string) error {
	f.record(fixCall{Method: "Acknowledge", ID: id})
	return f.ackErr
}

func (f *fakeFixing) RetryRecovery(context.Context) error {
	f.record(fixCall{Method: "RetryRecovery"})
	return f.retryErr
}

func (f *fakeFixing) Paused(context.Context) (bool, error) { return f.paused, nil }

func (f *fakeFixing) WritesEnabled(context.Context) (bool, error) { return f.writesOn, nil }

func (f *fakeFixing) SetWritesEnabled(_ context.Context, on bool, _ string) error {
	f.record(fixCall{Method: "SetWritesEnabled", On: on})
	return nil
}

func (f *fakeFixing) SettingChanges(_ context.Context, limit int) ([]fix.SettingChange, error) {
	return f.settings[:min(limit, len(f.settings))], nil
}

// fixingEnv is a signed-in env with fakes for Fixing and Reports and a
// completed run 7.
func fixingEnv(t *testing.T, f *fakeFixing, rep *fakeReports) *testEnv {
	t.Helper()
	if rep == nil {
		rep = &fakeReports{}
	}
	e := newEnv(t, func(o *Options) { o.Fixing, o.Reports = f, rep })
	e.setUpAdmin(t)
	e.logIn(t)
	e.runs.add(verify.RunView{Run: verify.Run{ID: 7, Status: verify.RunCompleted, LibraryID: "library"}})
	return e
}

func prop(field string, current, proposed string) evidence.Proposal {
	return evidence.Proposal{Field: field, Current: []string{current}, Proposed: []string{proposed}, Roots: []string{"dnb", "file"}}
}

func TestFixesPageStatuses(t *testing.T) {
	rows := []report.FixRow{
		{RunID: 7, BookID: 2, Title: "Two", Tier: "A", Proposal: prop("title", "Old", "New")},
		{RunID: 7, BookID: 2, Title: "Two", Tier: "A", Proposal: prop("authors", "Wrong", "Right")},
		{RunID: 7, BookID: 3, Title: "Three", Tier: "A", Proposal: prop("title", "Old", "New")},
		{RunID: 7, BookID: 4, Title: "Four", Tier: "A", Proposal: prop("title", "Old", "New")},
		{RunID: 7, BookID: 5, Title: "Five", Tier: "A", Proposal: evidence.Proposal{Field: "pubdate", Current: []string{}, Proposed: []string{"2001-07-01"}, Roots: []string{"dnb", "file"}}},
		{RunID: 7, BookID: 6, Title: "Six", Tier: "A", Proposal: prop("title", "Old", "New")},
	}
	f := &fakeFixing{approved: 3, proposals: []fix.ProposalRow{
		{RunID: 7, BookID: 2, Proposal: rows[1].Proposal, Status: "approved"},
		{RunID: 7, BookID: 3, Proposal: rows[2].Proposal, Status: "applied"},
		{RunID: 7, BookID: 4, Proposal: rows[3].Proposal, Status: "stale", Reason: "book_changed"},
		{RunID: 7, BookID: 6, Proposal: rows[5].Proposal, Status: "rejected"},
	}}
	e := fixingEnv(t, f, &fakeReports{fixes: report.FixPage{Rows: rows}})
	r := e.do(t, "GET", "/runs/7/fixes?field=title", nil, nil)
	if r.status != 200 {
		t.Fatalf("status %d", r.status)
	}
	boxes := regexp.MustCompile(`<input type="checkbox" name="pick" value="([^"]+)"( checked)?`).FindAllStringSubmatch(r.body, -1)
	var got []string
	for _, b := range boxes {
		got = append(got, b[1]+b[2])
	}
	if want := []string{"2:title", "2:authors", "6:title"}; !slices.Equal(got, want) {
		t.Fatalf("checkboxes %v, want %v (none pre-ticked)", got, want)
	}
	if strings.Contains(r.body, "checked") {
		t.Fatal("a row is pre-ticked")
	}
	var all []string
	end := strings.Index(r.body, "Approve all shown")
	allShown := r.body[strings.LastIndex(r.body[:end], "<form"):end]
	for _, m := range regexp.MustCompile(`<input type="hidden" name="pick" value="([^"]+)">`).FindAllStringSubmatch(allShown, -1) {
		all = append(all, m[1])
	}
	if want := []string{"2:title", "2:authors"}; !slices.Equal(all, want) {
		t.Fatalf("approve all shown holds %v, want %v (never a rejected fix)", all, want)
	}
	for _, want := range []string{
		"Approved", "Applied", "Rejected", "Stale: the book changed in Calibre since the check",
		"Calibre can’t store this date exactly", "3 approved, waiting", `action="/fixes/apply"`,
		`action="/runs/7/fixes/decide"`, `name="field" value="title"`, "Approve all shown",
	} {
		if !strings.Contains(r.body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
}

func TestDecideForm(t *testing.T) {
	f := &fakeFixing{}
	e := fixingEnv(t, f, nil)
	token := e.csrfToken(t)
	post := func(form url.Values) response {
		t.Helper()
		form.Set("csrf_token", token)
		return e.do(t, "POST", "/runs/7/fixes/decide", form, nil)
	}

	r := post(url.Values{"action": {"approve"}, "pick": {"2:title", "2:pubdate"}, "field": {"title"}, "tier": {"B"}})
	if r.status != 303 || r.header.Get("Location") != "/runs/7/fixes?field=title&tier=B" {
		t.Fatalf("approve = %d → %q: %s", r.status, r.header.Get("Location"), r.body)
	}
	if got := f.recorded(); len(got) != 1 || fmt.Sprint(got[0]) != fmt.Sprint(fixCall{Method: "Approve", RunID: 7, BookID: 2, Fields: []string{"pubdate", "title"}, Basis: fix.Basis{Mode: store.DecisionBulk}}) {
		t.Fatalf("calls %+v", got)
	}
	r = post(url.Values{"action": {"reject"}, "pick": {"2:title"}})
	if r.status != 303 || r.header.Get("Location") != "/runs/7/fixes" || f.recorded()[1].Method != "Reject" {
		t.Fatalf("reject = %d → %q, calls %+v", r.status, r.header.Get("Location"), f.recorded())
	}

	var many []string
	for i := 1; i <= 51; i++ {
		many = append(many, fmt.Sprintf("%d:title", i))
	}
	for name, form := range map[string]url.Values{
		"51 picks":         {"action": {"approve"}, "pick": many},
		"no picks":         {"action": {"approve"}},
		"unknown field":    {"action": {"approve"}, "pick": {"2:series"}},
		"book 0":           {"action": {"approve"}, "pick": {"0:title"}},
		"11-digit book":    {"action": {"approve"}, "pick": {"99999999999:title"}},
		"book past int32":  {"action": {"approve"}, "pick": {"2147483648:title"}},
		"unknown action":   {"action": {"delete"}, "pick": {"2:title"}},
		"two actions":      {"action": {"approve", "reject"}, "pick": {"2:title"}},
		"markup in a pick": {"action": {"approve"}, "pick": {"<b>:title"}},
		"bad filter":       {"action": {"approve"}, "pick": {"2:title"}, "field": {"series"}},
		"unknown key":      {"action": {"approve"}, "pick": {"2:title"}, "next": {"https://evil.example"}},
	} {
		before := len(f.recorded())
		if r := post(form); r.status != 400 || len(f.recorded()) != before {
			t.Errorf("%s = %d, calls %d → %d", name, r.status, before, len(f.recorded()))
		}
	}
	if r := e.do(t, "POST", "/runs/7/fixes/decide?pick=2:title", url.Values{"csrf_token": {token}, "action": {"approve"}, "pick": {"2:title"}}, nil); r.status != 400 {
		t.Errorf("query string = %d", r.status)
	}

	e.runs.add(running(8))
	if r := e.do(t, "POST", "/runs/8/fixes/decide", url.Values{"csrf_token": {token}, "action": {"approve"}, "pick": {"2:title"}}, nil); r.status != 409 {
		t.Errorf("active run = %d", r.status)
	}
	if r := e.do(t, "POST", "/runs/99/fixes/decide", url.Values{"csrf_token": {token}, "action": {"approve"}, "pick": {"2:title"}}, nil); r.status != 404 {
		t.Errorf("unknown run = %d", r.status)
	}

	f.decideErr = map[int]error{2: fmt.Errorf("x: %w", fix.ErrNoProposal), 4: fix.ErrClosed, 5: fix.ErrBlocked,
		6: fmt.Errorf("x: %w", &fix.UnwritableError{Field: "pubdate", Reason: "calibre_alters_value"})}
	before := len(f.recorded())
	r = post(url.Values{"action": {"approve"}, "pick": {"3:title", "2:title", "4:title", "5:pubdate", "6:title", "6:pubdate"}})
	if r.status != 409 {
		t.Fatalf("partial = %d", r.status)
	}
	for _, want := range []string{"Book 2: superseded by a newer check", "Book 4: already final", "Book 5: Calibre can’t store it", "Book 6: Calibre can’t store its pubdate exactly; untick it", `href="/runs/7/fixes"`} {
		if !strings.Contains(r.body, want) {
			t.Errorf("409 body lacks %q", want)
		}
	}
	if strings.Contains(r.body, "Book 3") {
		t.Error("409 body names a book that was decided")
	}
	var books []int
	for _, c := range f.recorded()[before:] {
		books = append(books, c.BookID)
	}
	if !slices.Equal(books, []int{2, 3, 4, 5, 6}) {
		t.Fatalf("decided books %v", books)
	}
}

func TestApplyRefusals(t *testing.T) {
	f := &fakeFixing{}
	e := fixingEnv(t, f, nil)
	token := e.csrfToken(t)
	for _, tc := range []struct {
		err    error
		status int
		want   string
	}{
		{nil, 303, ""},
		{fix.ErrWritesOff, 409, `href="/settings"`},
		{fix.ErrPaused, 409, `href="/incidents"`},
		{fix.ErrBusy, 409, "already being applied"},
	} {
		f.startErr = tc.err
		r := e.do(t, "POST", "/fixes/apply", url.Values{"csrf_token": {token}}, nil)
		if r.status != tc.status || !strings.Contains(r.body, tc.want) {
			t.Errorf("%v: %d %q", tc.err, r.status, r.body)
		}
		if tc.status == 303 && r.header.Get("Location") != "/history" {
			t.Errorf("Location %q", r.header.Get("Location"))
		}
		if tc.status == 409 && !strings.Contains(r.body, `name="csrf_token" value="`+token+`"`) {
			t.Errorf("%v: the refusal page's Sign out has no CSRF token", tc.err)
		}
	}
	if r := e.do(t, "POST", "/fixes/apply", url.Values{"csrf_token": {token}, "x": {"1"}}, nil); r.status != 400 {
		t.Errorf("unknown key = %d", r.status)
	}
}

func op(id int64, state string, changes ...library.Change) fix.Operation {
	return fix.Operation{ID: id, Kind: "apply", LibraryID: "library", BookID: int(id) + 100, State: state, Changes: changes,
		RequestedBy: "admin", CreatedAt: time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)}
}

func change(field string, v any) library.Change {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return library.Change{Field: field, Value: raw}
}

func TestHistoryPage(t *testing.T) {
	f := &fakeFixing{}
	for id := int64(51); id >= 1; id-- {
		f.ops = append(f.ops, op(id, "completed", change("title", "New"), change("languages", []string{"eng"})))
	}
	f.status = fix.ApplyStatus{Running: true, Started: time.Now()}
	e := fixingEnv(t, f, nil)
	r := e.do(t, "GET", "/history", nil, nil)
	if r.status != 200 {
		t.Fatalf("status %d", r.status)
	}
	if n := strings.Count(r.body, `<tr><td><a href="/history/`); n != 50 {
		t.Errorf("%d rows, want 50", n)
	}
	for _, want := range []string{`href="/history?before=2"`, "Applying…", "Apply", "151", "completed", "title, languages", "2026-10-19"} {
		if !strings.Contains(r.body, want) {
			t.Errorf("history lacks %q", want)
		}
	}
	r = e.do(t, "GET", "/history?before=2", nil, nil)
	if n := strings.Count(r.body, `<tr><td><a href="/history/`); n != 1 || strings.Contains(r.body, "before=") {
		t.Errorf("older page: %d rows, or an older link", n)
	}
	f.status = fix.ApplyStatus{Finished: time.Now(), Summary: fix.Summary{Applied: 5, Blocked: 1}, Err: "stopped: an operation didn't complete; see Incidents"}
	r = e.do(t, "GET", "/history", nil, nil)
	for _, want := range []string{"Last apply: 5 applied, 0 stale, 1 blocked, 0 failed", "stopped: an operation didn&#39;t complete"} {
		if !strings.Contains(r.body, want) {
			t.Errorf("history lacks %q", want)
		}
	}
	for _, path := range []string{"/history?before=abc", "/history?before=0", "/history?x=1"} {
		if r := e.do(t, "GET", path, nil, nil); r.status != 400 {
			t.Errorf("%s = %d, want 400", path, r.status)
		}
	}
}

func TestOperationPage(t *testing.T) {
	done := op(3, "completed", change("title", "New Title"), change("languages", []string{"eng"}))
	done.Before = evidence.Record{Title: "Old Title", Languages: []string{"fra"}}
	done.After = &evidence.Record{Title: "New Title", Languages: []string{"eng"}}
	restored := op(4, "restored", change("title", "X"))
	restored.Before = evidence.Record{Title: "Old"}
	restored.After = &evidence.Record{Title: "Old"}
	f := &fakeFixing{ops: []fix.Operation{done, restored}, undoable: true,
		events:    map[int64][]fix.Event{3: {{Seq: 1, State: "requested"}, {Seq: 2, State: "writing"}, {Seq: 3, State: "verifying"}, {Seq: 4, State: "completed"}}},
		incidents: []fix.Incident{{ID: 9, OperationID: 4, Reason: "readback_mismatch", Detail: "readback_mismatch: title"}}}
	e := fixingEnv(t, f, nil)
	r := e.do(t, "GET", "/history/3", nil, nil)
	for _, want := range []string{
		"<tr><td>title</td><td>Old Title</td><td>New Title</td><td>New Title</td></tr>",
		"<tr><td>languages</td><td>fra</td><td>eng</td><td>eng</td></tr>",
		"requested", "writing", "verifying", `action="/history/3/undo"`,
	} {
		if !strings.Contains(r.body, want) {
			t.Errorf("operation page lacks %q", want)
		}
	}
	f.undoable = false
	r = e.do(t, "GET", "/history/4", nil, nil)
	if !strings.Contains(r.body, "Calibre stored something else") || !strings.Contains(r.body, `href="/incidents"`) || strings.Contains(r.body, "/undo") {
		t.Errorf("restored operation page: %s", r.body)
	}
	if r := e.do(t, "GET", "/history/99", nil, nil); r.status != 404 {
		t.Errorf("unknown = %d", r.status)
	}
	if r := e.do(t, "GET", "/history/abc", nil, nil); r.status != 400 {
		t.Errorf("bad id = %d", r.status)
	}
}

func TestOperationPageEscapes(t *testing.T) {
	long := op(5, "completed", change("title", strings.Repeat("x", 10000)))
	long.Before = evidence.Record{Title: "<script>alert(1)</script>"}
	odd := op(6, "failed", library.Change{Field: "title", Value: json.RawMessage(`{"x":1}`)})
	f := &fakeFixing{ops: []fix.Operation{long, odd}}
	e := fixingEnv(t, f, nil)
	r := e.do(t, "GET", "/history/5", nil, nil)
	if strings.Contains(r.body, "<script>alert") || !strings.Contains(r.body, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Error("the before value isn't escaped")
	}
	if !strings.Contains(r.body, strings.Repeat("x", 512)) || strings.Contains(r.body, strings.Repeat("x", 513)) {
		t.Error("the written value isn't capped at 512 runes")
	}
	r = e.do(t, "GET", "/history/6", nil, nil)
	if r.status != 200 || !strings.Contains(r.body, "(unreadable value)") {
		t.Errorf("odd value: %d", r.status)
	}
	if r := e.do(t, "GET", "/history", nil, nil); r.status != 200 || strings.Contains(r.body, "<script>alert") {
		t.Errorf("history: %d", r.status)
	}
}

func TestValueText(t *testing.T) {
	for raw, want := range map[string]string{
		`"a"`: "a", `["a","b"]`: "a; b", `{"isbn":"1","asin":"2"}`: "asin: 2; isbn: 1", `null`: "(unset)",
		`""`: "(empty)", `[]`: "(empty)", `{"x":1}`: "(unreadable value)", `7`: "(unreadable value)",
	} {
		if got := valueText(json.RawMessage(raw)); got != want {
			t.Errorf("valueText(%s) = %q, want %q", raw, got, want)
		}
	}
}

func TestUndoRefusals(t *testing.T) {
	f := &fakeFixing{undoID: 42}
	e := fixingEnv(t, f, nil)
	token := e.csrfToken(t)
	for _, tc := range []struct {
		err    error
		status int
		want   string
	}{
		{nil, 303, ""},
		{fmt.Errorf("x: %w", fix.ErrChanged), 409, "changed in Calibre since"},
		{fix.ErrNotUndoable, 409, "can’t be undone"},
		{fix.ErrBlocked, 409, "can’t store the old values"},
		{fix.ErrPaused, 409, `href="/incidents"`},
		{fix.ErrWritesOff, 409, `href="/settings"`},
		{fix.ErrBusy, 409, "Another write is running"},
		{fix.ErrRefused, 409, "needs write access"},
	} {
		f.undoErr = tc.err
		r := e.do(t, "POST", "/history/3/undo", url.Values{"csrf_token": {token}}, nil)
		if r.status != tc.status || !strings.Contains(r.body, tc.want) {
			t.Errorf("%v: %d %q", tc.err, r.status, r.body)
		}
		if tc.status == 303 && r.header.Get("Location") != "/history/42" {
			t.Errorf("Location %q", r.header.Get("Location"))
		}
	}
	if r := e.do(t, "POST", "/history/abc/undo", url.Values{"csrf_token": {token}}, nil); r.status != 400 {
		t.Errorf("bad id = %d", r.status)
	}
}

func TestIncidentsPage(t *testing.T) {
	at := time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)
	f := &fakeFixing{incidents: []fix.Incident{
		{ID: 5, OperationID: 12, Reason: "write_error", Detail: "write_error: timeout", CreatedAt: at, AcknowledgedBy: "admin", AcknowledgedAt: &at},
		{ID: 3, OperationID: 10, Reason: "readback_mismatch", Detail: "readback_mismatch: title", CreatedAt: at},
	}}
	e := fixingEnv(t, f, nil)
	r := e.do(t, "GET", "/incidents", nil, nil)
	open, acked := strings.Index(r.body, "Incident 3"), strings.Index(r.body, "Incident 5")
	if open < 0 || acked < 0 || open > acked {
		t.Fatalf("open incident not listed first: %d, %d", open, acked)
	}
	for _, want := range []string{"Calibre stored something else", "readback_mismatch: title", `href="/history/10"`, "2026-10-19 09:00 UTC", `action="/incidents/3/acknowledge"`, "Acknowledged"} {
		if !strings.Contains(r.body, want) {
			t.Errorf("incidents page lacks %q", want)
		}
	}
	if strings.Contains(r.body, "/incidents/5/acknowledge") {
		t.Error("an acknowledged incident offers Acknowledge")
	}
	token := e.csrfToken(t)
	r = e.do(t, "POST", "/incidents/3/acknowledge", url.Values{"csrf_token": {token}}, nil)
	if r.status != 303 || r.header.Get("Location") != "/incidents" || fmt.Sprint(f.recorded()) != fmt.Sprint([]fixCall{{Method: "Acknowledge", ID: 3}}) {
		t.Fatalf("acknowledge = %d → %q, calls %+v", r.status, r.header.Get("Location"), f.recorded())
	}
	f.ackErr = fmt.Errorf("x: %w", fix.ErrNoIncident)
	if r := e.do(t, "POST", "/incidents/3/acknowledge", url.Values{"csrf_token": {token}}, nil); r.status != 409 || !strings.Contains(r.body, "still being restored or already acknowledged") {
		t.Errorf("refused acknowledge = %d %q", r.status, r.body)
	}
	if r := e.do(t, "POST", "/incidents/x/acknowledge", url.Values{"csrf_token": {token}}, nil); r.status != 400 {
		t.Errorf("bad id = %d", r.status)
	}
}

// TestSettingsSourceNote: a source that failed its probe says so next to
// its state.
func TestSettingsSourceNote(t *testing.T) {
	e := newEnv(t, func(o *Options) {
		o.Fixing = &fakeFixing{}
		o.Sources = []SourceStatus{{"Open Library", "on"}, {"Google Books", "on"}}
		o.SourceNotes = func() map[string]string {
			return map[string]string{"Google Books": "skipped since 2026-10-28 21:58 UTC: its probe found no volume"}
		}
	})
	e.setUpAdmin(t)
	e.logIn(t)
	r := e.do(t, "GET", "/settings", nil, nil)
	for _, want := range []string{"<tr><td>Open Library</td><td>on</td></tr>", "<tr><td>Google Books</td><td>on; skipped since 2026-10-28 21:58 UTC: its probe found no volume</td></tr>"} {
		if !strings.Contains(r.body, want) {
			t.Errorf("settings page lacks %q", want)
		}
	}
}

func TestSettingsPage(t *testing.T) {
	at := time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)
	f := &fakeFixing{settings: []fix.SettingChange{{Value: false, ChangedBy: "admin", ChangedAt: at}, {Value: true, ChangedBy: "admin", ChangedAt: at.Add(-time.Hour)}}}
	e := newEnv(t, func(o *Options) {
		o.Fixing = f
		o.Sources = []SourceStatus{{"DNB", "on"}, {"BNE", "on"}, {"Open Library", "on"}, {"Google Books", "off: no BOOKWARDEN_GOOGLE_BOOKS_KEY"}}
	})
	e.setUpAdmin(t)
	e.logIn(t)
	r := e.do(t, "GET", "/settings", nil, nil)
	for _, want := range []string{
		"<strong>Off</strong>", `name="value" value="on"`, "Allow changes to Calibre", "read-write before turning this on, and read-only after",
		"<tr><td>Off</td><td>admin</td><td>2026-10-19 09:00 UTC</td></tr>", "<tr><td>On</td><td>admin</td><td>2026-10-19 08:00 UTC</td></tr>",
		"<tr><td>DNB</td><td>on</td></tr>", "<tr><td>BNE</td><td>on</td></tr>", "<tr><td>Open Library</td><td>on</td></tr>",
		"<tr><td>Google Books</td><td>off: no BOOKWARDEN_GOOGLE_BOOKS_KEY</td></tr>",
	} {
		if !strings.Contains(r.body, want) {
			t.Errorf("settings page lacks %q", want)
		}
	}
	token := e.csrfToken(t)
	r = e.do(t, "POST", "/settings/writes", url.Values{"csrf_token": {token}, "value": {"on"}}, nil)
	if r.status != 303 || r.header.Get("Location") != "/settings" || fmt.Sprint(f.recorded()) != fmt.Sprint([]fixCall{{Method: "SetWritesEnabled", On: true}}) {
		t.Fatalf("switch on = %d → %q, calls %+v", r.status, r.header.Get("Location"), f.recorded())
	}
	for _, form := range []url.Values{{"value": {"maybe"}}, {}, {"value": {"on", "off"}}} {
		form.Set("csrf_token", token)
		if r := e.do(t, "POST", "/settings/writes", form, nil); r.status != 400 || len(f.recorded()) != 1 {
			t.Errorf("%v = %d", form, r.status)
		}
	}
	f.writesOn = true
	if r := e.do(t, "GET", "/settings", nil, nil); !strings.Contains(r.body, "<strong>On</strong>") || !strings.Contains(r.body, `name="value" value="off"`) {
		t.Error("the page doesn't show the switch on")
	}
}

func TestPausedBanner(t *testing.T) {
	f := &fakeFixing{}
	e := fixingEnv(t, f, nil)
	const banner = "Writes are paused until an incident is acknowledged."
	pages := []string{"/runs/7/fixes", "/history", "/incidents", "/settings"}
	for _, p := range pages {
		if r := e.do(t, "GET", p, nil, nil); r.status != 200 || strings.Contains(r.body, banner) {
			t.Errorf("%s = %d, banner shown while not paused", p, r.status)
		}
	}
	f.paused = true
	for _, p := range pages {
		r := e.do(t, "GET", p, nil, nil)
		if i := strings.Index(r.body, banner); i < 0 || !strings.Contains(r.body[i:], `<a href="/incidents">`) {
			t.Errorf("%s lacks the banner with its link", p)
		}
	}
}

func TestFixingPostsNeedCSRF(t *testing.T) {
	f := &fakeFixing{undoID: 2}
	e := fixingEnv(t, f, nil)
	token := e.csrfToken(t)
	for path, form := range map[string]url.Values{
		"/runs/7/fixes/decide":     {"action": {"approve"}, "pick": {"2:title"}},
		"/fixes/apply":             {},
		"/history/1/undo":          {},
		"/incidents/1/acknowledge": {},
		"/settings/writes":         {"value": {"on"}},
	} {
		if r := e.do(t, "POST", path, form, nil); r.status != 403 {
			t.Errorf("%s without a token = %d", path, r.status)
		}
		withToken := url.Values{"csrf_token": {token}}
		for k, v := range form {
			withToken[k] = v
		}
		if r := e.do(t, "POST", path, withToken, http.Header{"Origin": {"https://evil.example"}}); r.status != 403 {
			t.Errorf("%s from a foreign origin = %d", path, r.status)
		}
		if r := e.do(t, "POST", path, withToken, nil); r.status != 303 {
			t.Errorf("%s with a token = %d: %s", path, r.status, r.body)
		}
	}
	if got := f.recorded(); len(got) != 5 {
		t.Fatalf("calls %+v, want only the five with a token", got)
	}
}

func TestDecodePickSortFields(t *testing.T) {
	for _, field := range []string{"title_sort", "author_sort"} {
		if id, f, err := decodePick("2:" + field); err != nil || id != 2 || f != field {
			t.Errorf("decodePick(2:%s) = %d %q %v", field, id, f, err)
		}
	}
	if _, _, err := decodePick("2:sort"); err == nil {
		t.Error("decodePick accepted Calibre's own field name")
	}
}

// TestIncidentsPageRestoring: an incident whose operation is still restoring
// says why and offers "Retry now" instead of Acknowledge; a restored one
// doesn't. The retry needs a session and a CSRF token.
func TestIncidentsPageRestoring(t *testing.T) {
	at := time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)
	f := &fakeFixing{writesOn: true, incidents: []fix.Incident{
		{ID: 7, OperationID: 14, Reason: "write_error", Detail: "write_error: unreachable", CreatedAt: at, OperationState: "restoring"},
		{ID: 6, OperationID: 13, Reason: "write_error", Detail: "write_error: unreachable", CreatedAt: at, OperationState: "restored"},
		{ID: 4, OperationID: 11, Reason: "interrupted", Detail: "interrupted: writing", CreatedAt: at, OperationState: "restoring"},
	}}
	e := fixingEnv(t, f, nil)
	r := e.do(t, "GET", "/incidents", nil, nil)
	for _, want := range []string{"Calibre could not be reached while restoring this book. Bookwarden retries every minute.", `action="/incidents/7/retry"`, "Retry now", `action="/incidents/6/acknowledge"`, "The write was interrupted before the old values were back."} {
		if !strings.Contains(r.body, want) {
			t.Errorf("incidents page lacks %q", want)
		}
	}
	for _, unwanted := range []string{`action="/incidents/7/acknowledge"`, `action="/incidents/6/retry"`} {
		if strings.Contains(r.body, unwanted) {
			t.Errorf("incidents page has %q", unwanted)
		}
	}
	f.writesOn = false
	if r := e.do(t, "GET", "/incidents", nil, nil); !strings.Contains(r.body, "Restoring continues once writes are on") {
		t.Errorf("with writes off the page lacks the writes-off text")
	}
	if r := e.do(t, "POST", "/incidents/7/retry", url.Values{}, nil); r.status != 403 {
		t.Errorf("retry without a CSRF token = %d, want 403", r.status)
	}
	token := e.csrfToken(t)
	r = e.do(t, "POST", "/incidents/7/retry", url.Values{"csrf_token": {token}}, nil)
	if r.status != 303 || r.header.Get("Location") != "/incidents" || fmt.Sprint(f.recorded()) != fmt.Sprint([]fixCall{{Method: "RetryRecovery"}}) {
		t.Fatalf("retry = %d → %q, calls %+v", r.status, r.header.Get("Location"), f.recorded())
	}
	for _, tc := range []struct {
		err  error
		want string
	}{
		{fix.ErrBusy, "while an apply is running"},
		{fix.ErrWritesOff, "Writes are off"},
	} {
		f.retryErr = tc.err
		if r := e.do(t, "POST", "/incidents/7/retry", url.Values{"csrf_token": {token}}, nil); r.status != 409 || !strings.Contains(r.body, tc.want) {
			t.Errorf("retry refused with %v = %d, body lacks %q", tc.err, r.status, tc.want)
		}
	}
}
