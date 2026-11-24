package web

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

const htmxSHA256 = "d6fdc75f204e6bdefa99b69bf1e6d4ac69b8a364f77929f45c13476b4000f717"

// fakeRuns stands in for the verify.Runner.
type fakeRuns struct {
	mu        sync.Mutex
	startErr  error
	started   []verify.Scope
	cancelled []int64
	pins      map[int64]bool
	runs      map[int64]verify.RunView
	calibre   verify.CalibreStatus
}

func newFakeRuns() *fakeRuns {
	return &fakeRuns{
		runs:    map[int64]verify.RunView{},
		calibre: verify.CalibreStatus{State: "ok", LibraryID: "library", CheckedAt: time.Now()},
	}
}

func (f *fakeRuns) add(v verify.RunView) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs[v.Run.ID] = v
}

func (f *fakeRuns) Start(_ context.Context, scope verify.Scope) (verify.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = append(f.started, scope)
	if f.startErr != nil {
		return verify.Run{}, f.startErr
	}
	return verify.Run{ID: 7, Scope: scope, Status: verify.RunRunning}, nil
}

func (f *fakeRuns) Cancel(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.runs[id]; !ok {
		return fmt.Errorf("fake: %w", verify.ErrNoSuchRun)
	}
	f.cancelled = append(f.cancelled, id)
	return nil
}

func (f *fakeRuns) SetPinned(_ context.Context, id int64, pinned bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.runs[id]; !ok {
		return fmt.Errorf("fake: %w", verify.ErrNoSuchRun)
	}
	if f.pins == nil {
		f.pins = map[int64]bool{}
	}
	f.pins[id] = pinned
	return nil
}

func (f *fakeRuns) List(_ context.Context, limit int) ([]verify.RunView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []verify.RunView
	for _, v := range f.runs {
		out = append(out, v)
	}
	slices.SortFunc(out, func(a, b verify.RunView) int { return cmp.Compare(b.Run.ID, a.Run.ID) })
	return out[:min(limit, len(out))], nil
}

func (f *fakeRuns) View(_ context.Context, id int64) (verify.RunView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.runs[id]
	if !ok {
		return verify.RunView{}, fmt.Errorf("fake: %w", verify.ErrNoSuchRun)
	}
	return v, nil
}

func (f *fakeRuns) Calibre() verify.CalibreStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calibre
}

func signedIn(t *testing.T, mutate ...func(*Options)) *testEnv {
	t.Helper()
	e := newEnv(t, mutate...)
	e.setUpAdmin(t)
	e.logIn(t)
	return e
}

func running(id int64) verify.RunView {
	return verify.RunView{
		Run:      verify.Run{ID: id, Status: verify.RunRunning, LibraryID: "library", StartedAt: time.Now()},
		Progress: verify.Progress{Total: 20, Pending: 12, Checked: 7, SourceChanged: 1},
	}
}

func TestRunsPagesNeedSignIn(t *testing.T) {
	e := newEnv(t)
	e.setUpAdmin(t)
	e.runs.add(running(7))
	for _, path := range []string{"/runs", "/runs/7", "/runs/7/progress"} {
		if r := e.do(t, http.MethodGet, path, nil, nil); r.status != http.StatusSeeOther || r.header.Get("Location") != "/login" {
			t.Errorf("GET %s signed out: %d → %q, want 303 → /login", path, r.status, r.header.Get("Location"))
		}
	}
	if r := e.do(t, http.MethodPost, "/runs", url.Values{}, nil); r.status != http.StatusUnauthorized {
		t.Errorf("POST /runs signed out: %d, want 401", r.status)
	}
}

func TestStartRun(t *testing.T) {
	tests := []struct {
		name      string
		form      func(token string) url.Values
		startErr  error
		calibre   verify.CalibreStatus
		active    bool
		want      int
		location  string
		scope     *verify.Scope
		bodyHas   []string
		bodyLacks []string
	}{
		{
			name: "whole library", form: func(tok string) url.Values { return url.Values{"csrf_token": {tok}} },
			want: http.StatusSeeOther, location: "/runs/7", scope: &verify.Scope{},
		},
		{
			name: "one book", form: func(tok string) url.Values { return url.Values{"csrf_token": {tok}, "book_id": {"3"}} },
			want: http.StatusSeeOther, location: "/runs/7", scope: &verify.Scope{BookID: 3},
		},
		{
			name: "book id not a number", form: func(tok string) url.Values { return url.Values{"csrf_token": {tok}, "book_id": {"abc"}} },
			want: http.StatusBadRequest,
		},
		{
			name: "book id zero", form: func(tok string) url.Values { return url.Values{"csrf_token": {tok}, "book_id": {"0"}} },
			want: http.StatusBadRequest,
		},
		{
			name: "no form token", form: func(string) url.Values { return url.Values{} },
			want: http.StatusForbidden,
		},
		{
			name: "a run is active", form: func(tok string) url.Values { return url.Values{"csrf_token": {tok}} },
			startErr: verify.ErrRunActive, active: true,
			want: http.StatusConflict, bodyHas: []string{"A check is already running", `href="/runs/5"`},
		},
		{
			name: "Calibre not ready", form: func(tok string) url.Values { return url.Values{"csrf_token": {tok}} },
			startErr:  fmt.Errorf("%w: probe", verify.ErrCalibreNotReady),
			calibre:   verify.CalibreStatus{State: "incompatible", Message: "The module list is not available in version: 1.<b>"},
			want:      http.StatusServiceUnavailable,
			bodyHas:   []string{"The module list is not available in version: 1.&lt;b&gt;"},
			bodyLacks: []string{"1.<b>"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := signedIn(t)
			token := e.csrfToken(t)
			e.runs.startErr = tt.startErr
			if tt.calibre.State != "" {
				e.runs.calibre = tt.calibre
			}
			if tt.active {
				e.runs.add(verify.RunView{Run: verify.Run{ID: 4, Status: verify.RunCompleted}})
				e.runs.add(verify.RunView{Run: verify.Run{ID: 5, Status: verify.RunRunning}})
			}
			r := e.do(t, http.MethodPost, "/runs", tt.form(token), nil)
			if r.status != tt.want {
				t.Fatalf("POST /runs: %d, want %d: %s", r.status, tt.want, r.body)
			}
			if tt.location != "" && r.header.Get("Location") != tt.location {
				t.Errorf("Location = %q, want %q", r.header.Get("Location"), tt.location)
			}
			if tt.scope != nil && (len(e.runs.started) != 1 || e.runs.started[0] != *tt.scope) {
				t.Errorf("started %v, want [%v]", e.runs.started, *tt.scope)
			}
			for _, s := range tt.bodyHas {
				if !strings.Contains(r.body, s) {
					t.Errorf("body lacks %q", s)
				}
			}
			for _, s := range tt.bodyLacks {
				if strings.Contains(r.body, s) {
					t.Errorf("body contains %q", s)
				}
			}
		})
	}
}

func TestProgressFragment(t *testing.T) {
	e := signedIn(t)
	e.runs.add(running(7))
	r := e.do(t, http.MethodGet, "/runs/7/progress", nil, http.Header{"HX-Request": {"true"}})
	if r.status != http.StatusOK {
		t.Fatalf("progress: %d", r.status)
	}
	for _, s := range []string{"Running", ">20<", ">7<", ">12<"} {
		if !strings.Contains(r.body, s) {
			t.Errorf("running fragment lacks %q:\n%s", s, r.body)
		}
	}
	// Only the counts are swapped: the Cancel button, and the focus on it,
	// stay put.
	for _, s := range []string{"<html", "<form", "<button", "hx-get"} {
		if strings.Contains(r.body, s) {
			t.Errorf("the fragment carries %q:\n%s", s, r.body)
		}
	}

	done := running(8)
	done.Run.Status = verify.RunCompleted
	done.Progress = verify.Progress{Total: 20, Checked: 19, Failed: 1}
	e.runs.add(done)
	r = e.do(t, http.MethodGet, "/runs/8/progress", nil, nil)
	// 286 tells htmx to stop polling; the Cancel form goes out of band.
	if r.status != 286 || !strings.Contains(r.body, "Completed") || !strings.Contains(r.body, `id="cancel-form" hx-swap-oob="true"`) || strings.Contains(r.body, "<button") {
		t.Errorf("completed fragment: %d\n%s", r.status, r.body)
	}

	waiting := running(9)
	waiting.Run.Status = verify.RunWaiting
	e.runs.add(waiting)
	waiting.Run.Error = "Calibre is unreachable"
	e.runs.add(waiting)
	// The live region reads the error; an alert in it would interrupt every
	// 2 s for as long as Calibre is away.
	if r := e.do(t, http.MethodGet, "/runs/9/progress", nil, nil); r.status != http.StatusOK || !strings.Contains(r.body, "Waiting for Calibre") ||
		!strings.Contains(r.body, "Calibre is unreachable") || strings.Contains(r.body, `role="alert"`) {
		t.Errorf("waiting fragment: %d\n%s", r.status, r.body)
	}
	if r := e.do(t, http.MethodGet, "/runs/99/progress", nil, nil); r.status != http.StatusNotFound {
		t.Errorf("progress of an unknown run: %d, want 404", r.status)
	}
}

// TestRunPageProgressIsAccessible: the run page polls a live region that
// holds only the counts; the Cancel button has an id and sits outside it,
// and without JavaScript a Refresh link stands in.
func TestRunPageProgressIsAccessible(t *testing.T) {
	e := signedIn(t)
	e.runs.add(running(7))
	r := e.do(t, http.MethodGet, "/runs/7", nil, nil)
	region := `<div id="progress" aria-live="polite" aria-atomic="true" hx-get="/runs/7/progress" hx-trigger="every 2s" hx-swap="innerHTML">`
	start, button := strings.Index(r.body, region), strings.Index(r.body, `<button type="submit" id="cancel-run">Cancel</button>`)
	if start < 0 || button < 0 {
		t.Fatalf("no live region or no Cancel button with its id:\n%s", r.body)
	}
	// Every div the region opens is closed before the button: it is outside.
	if button < start {
		t.Fatal("the Cancel button comes before the live region")
	}
	if between := r.body[start:button]; strings.Count(between, "<div") != strings.Count(between, "</div>") {
		t.Errorf("the Cancel button is inside the swapped region:\n%s", between)
	}
	if !strings.Contains(r.body, `<noscript>`) || !strings.Contains(r.body, `<a href="/runs/7">Refresh</a>`) {
		t.Error("no Refresh link for a browser without JavaScript")
	}

	done := running(8)
	done.Run.Status = verify.RunCompleted
	e.runs.add(done)
	r = e.do(t, http.MethodGet, "/runs/8", nil, nil)
	if strings.Contains(r.body, "hx-trigger") || strings.Contains(r.body, "cancel-run") || strings.Contains(r.body, "<noscript>") {
		t.Errorf("an ended run still polls or offers Cancel:\n%s", r.body)
	}
}

func TestProgressPollAfterSessionExpiry(t *testing.T) {
	e := newEnv(t)
	e.setUpAdmin(t)
	e.runs.add(running(7))
	r := e.do(t, http.MethodGet, "/runs/7/progress", nil, http.Header{"HX-Request": {"true"}})
	if r.status != http.StatusOK || r.header.Get("HX-Redirect") != "/login" {
		t.Fatalf("poll without a session: %d, HX-Redirect %q; want 200 and /login", r.status, r.header.Get("HX-Redirect"))
	}
}

func TestCancelRun(t *testing.T) {
	e := signedIn(t)
	e.runs.add(running(7))
	token := e.csrfToken(t)
	r := e.do(t, http.MethodPost, "/runs/7/cancel", url.Values{"csrf_token": {token}}, nil)
	if r.status != http.StatusSeeOther || r.header.Get("Location") != "/runs/7" {
		t.Fatalf("cancel: %d → %q", r.status, r.header.Get("Location"))
	}
	if len(e.runs.cancelled) != 1 || e.runs.cancelled[0] != 7 {
		t.Errorf("cancelled %v, want [7]", e.runs.cancelled)
	}
	if r := e.do(t, http.MethodPost, "/runs/99/cancel", url.Values{"csrf_token": {token}}, nil); r.status != http.StatusNotFound {
		t.Errorf("cancel of an unknown run: %d, want 404", r.status)
	}
	if r := e.do(t, http.MethodPost, "/runs/7/cancel", url.Values{}, nil); r.status != http.StatusForbidden {
		t.Errorf("cancel without the form token: %d, want 403", r.status)
	}
}

func TestRunPage(t *testing.T) {
	e := signedIn(t)
	v := running(7)
	v.Run.Error = "<script>x</script>"
	e.runs.add(v)
	r := e.do(t, http.MethodGet, "/runs/7", nil, nil)
	if r.status != http.StatusOK {
		t.Fatalf("GET /runs/7: %d", r.status)
	}
	if strings.Contains(r.body, "<script>x") || !strings.Contains(r.body, "&lt;script&gt;x&lt;/script&gt;") {
		t.Error("the run's error isn't escaped")
	}
	if !strings.Contains(r.body, `action="/runs/7/cancel"`) {
		t.Error("an active run has no cancel button")
	}
	for path, status := range map[string]int{"/runs/99": http.StatusNotFound, "/runs/abc": http.StatusBadRequest} {
		if r := e.do(t, http.MethodGet, path, nil, nil); r.status != status {
			t.Errorf("GET %s: %d, want %d", path, r.status, status)
		}
	}
}

func TestRunsList(t *testing.T) {
	e := signedIn(t)
	e.runs.add(running(7))
	done := running(6)
	done.Run.Status = verify.RunCancelled
	e.runs.add(done)
	r := e.do(t, http.MethodGet, "/runs", nil, nil)
	for _, s := range []string{`href="/runs/7"`, `href="/runs/6"`, "Running", "Cancelled", `name="csrf_token"`, `action="/runs"`} {
		if !strings.Contains(r.body, s) {
			t.Errorf("/runs lacks %q", s)
		}
	}
	if strings.Index(r.body, `href="/runs/7"`) > strings.Index(r.body, `href="/runs/6"`) {
		t.Error("runs aren't listed newest first")
	}
}

func TestHtmxIsVendored(t *testing.T) {
	e := signedIn(t)
	r := e.do(t, http.MethodGet, "/static/htmx.min.js", nil, nil)
	sum := sha256.Sum256([]byte(r.body))
	if r.status != http.StatusOK || hex.EncodeToString(sum[:]) != htmxSHA256 {
		t.Fatalf("htmx: %d, sha256 %x", r.status, sum)
	}
	page := e.do(t, http.MethodGet, "/", nil, nil).body
	for _, s := range []string{
		`<script src="/static/htmx.min.js" defer></script>`,
		`<meta name="htmx-config" content='{"includeIndicatorStyles":false,"allowEval":false}'>`,
	} {
		if !strings.Contains(page, s) {
			t.Errorf("overview lacks %s", s)
		}
	}
}

func TestOverviewShowsCalibreAndLastRun(t *testing.T) {
	e := signedIn(t)
	e.runs.calibre = verify.CalibreStatus{State: "unreachable", Message: "library: list: calibre is unreachable"}
	done := running(3)
	done.Run.Status = verify.RunCompleted
	done.Progress = verify.Progress{Total: 42, Checked: 40, Failed: 2}
	e.runs.add(done)
	r := e.do(t, http.MethodGet, "/", nil, nil)
	for _, s := range []string{"Unreachable", "library: list: calibre is unreachable", "Completed", ">42<", ">40<", `href="/runs/3"`, `action="/runs"`, `name="csrf_token"`} {
		if !strings.Contains(r.body, s) {
			t.Errorf("overview lacks %q:\n%s", s, r.body)
		}
	}
}

func TestOverviewWithoutRuns(t *testing.T) {
	e := signedIn(t)
	if r := e.do(t, http.MethodGet, "/", nil, nil); !strings.Contains(r.body, "No library check has run yet.") {
		t.Errorf("overview:\n%s", r.body)
	}
}

func TestStartRunUnknownBook(t *testing.T) {
	e := signedIn(t)
	token := e.csrfToken(t)
	e.runs.startErr = fmt.Errorf("verify: start: %w", library.ErrBookNotFound)
	r := e.do(t, http.MethodPost, "/runs", url.Values{"csrf_token": {token}, "book_id": {"99"}}, nil)
	if r.status != http.StatusNotFound || !strings.Contains(r.body, "Calibre has no book with id 99") {
		t.Errorf("start for an unknown book: %d:\n%s", r.status, r.body)
	}
}

// TestRunPageWhereTheTimeWent: a run with stats shows where its time went,
// stage by stage, with its lookups and extractor calls; a run without shows
// no table.
func TestRunPageWhereTheTimeWent(t *testing.T) {
	e := signedIn(t)
	v := running(8)
	v.Run.Stats = verify.Stats{
		StageSeconds: map[string]float64{verify.StageCalibre: 6, verify.StageExportInspect: 0, verify.StageOCR: 0,
			verify.StageLookups: 12, verify.StageExtraction: 0, verify.StageStore: 96},
		Lookups:   map[string]map[string]int{"dnb": {"ok": 3, "stale_hit": 1}},
		Extractor: verify.ExtractorCounts{Calls: 3, Cached: 1},
	}
	e.runs.add(v)
	e.runs.add(running(7))
	r := e.do(t, http.MethodGet, "/runs/8", nil, nil)
	if r.status != http.StatusOK {
		t.Fatalf("GET /runs/8: %d", r.status)
	}
	for _, s := range []string{"Where the time went", "summed over the workers", "Reading Calibre records", "6s", "Storing results", "1m36s",
		"Catalogue lookups", "12s", "dnb", "ok 3", "stale_hit 1", "3 calls, 1 cached, 0 failed"} {
		if !strings.Contains(r.body, s) {
			t.Errorf("/runs/8 lacks %q", s)
		}
	}
	if i, j := strings.Index(r.body, "Reading Calibre records"), strings.Index(r.body, "Storing results"); i > j {
		t.Error("the stages aren't in pipeline order")
	}
	if r := e.do(t, http.MethodGet, "/runs/7", nil, nil); strings.Contains(r.body, "Where the time went") {
		t.Error("a run without stats shows the table")
	}
	zeros := running(9)
	zeros.Run.Stats = verify.Stats{StageSeconds: map[string]float64{verify.StageCalibre: 0, verify.StageStore: 0}}
	e.runs.add(zeros)
	if r := e.do(t, http.MethodGet, "/runs/9", nil, nil); strings.Contains(r.body, "Where the time went") {
		t.Error("a run cancelled before its first book shows a table of zeros")
	}
}

// TestARefusedWriteSaysWhatToDo: a fix that failed because Calibre refused
// the write tells the operator to give the account write access.
func TestARefusedWriteSaysWhatToDo(t *testing.T) {
	v := FixView{Status: "failed", Reason: "calibre_refused_write"}
	if got := fixStatus(v); !strings.Contains(got, "Calibre refused the write") || !strings.Contains(got, "needs write access") || !strings.Contains(got, "Fixing") {
		t.Errorf("fixStatus = %q", got)
	}
}
