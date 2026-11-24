package web

import (
	"errors"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/fix"
	"github.com/felixapel/calibre-bookwarden/internal/report"
)

// mutedType is the type of prop(...) fixes, muted in library "library".
var mutedType = fix.TypeMute{
	LibraryID: "library", FixType: fix.MuteType{Field: "title", Path: "dnb+file", Kind: "replace"},
	Fixes: 12, Misses: 12, Upper: 0.2, Muted: true, MutedAt: time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC), MutedBy: "rule",
}

func TestFixesMutedFilter(t *testing.T) {
	other := mutedType
	other.LibraryID = "elsewhere"
	notMuted := fix.TypeMute{LibraryID: "library", FixType: fix.MuteType{Field: "authors", Path: "dnb", Kind: "fill"}, Fixes: 30}
	f := &fakeFixing{mute: []fix.TypeMute{mutedType, other, notMuted}}
	row := report.FixRow{RunID: 7, BookID: 2, Title: "Two", Tier: "A", Proposal: prop("title", "Old", "New")}
	rep := &fakeReports{fixes: report.FixPage{Rows: []report.FixRow{row}, MutedHidden: 3}}
	e := fixingEnv(t, f, rep)
	types := []report.FixType{{Field: "title", Path: "dnb+file", Kind: "replace"}}

	// The default view hides the muted types of the run's library only, and
	// says how many fixes that left out, with a link to the filter.
	r := e.do(t, "GET", "/runs/7/fixes", nil, nil)
	if r.status != 200 || !reflect.DeepEqual(rep.fixFilter, report.FixFilter{MutedTypes: types}) {
		t.Fatalf("default = %d, filter %+v", r.status, rep.fixFilter)
	}
	for _, want := range []string{"3 fixes hidden in muted types", `href="/runs/7/fixes?muted=1"`} {
		if !strings.Contains(r.body, want) {
			t.Errorf("default view lacks %q", want)
		}
	}
	rep.fixes.MutedHidden = 1
	if r := e.do(t, "GET", "/runs/7/fixes", nil, nil); !strings.Contains(r.body, "1 fix hidden in muted types") {
		t.Error("a single hidden fix is not worded in the singular")
	}
	rep.fixes.MutedHidden = 0
	if r := e.do(t, "GET", "/runs/7/fixes", nil, nil); strings.Contains(r.body, "hidden in muted types") {
		t.Error("the hidden count shows with nothing hidden")
	}

	// The muted filter shows only those types, and they can still be decided.
	r = e.do(t, "GET", "/runs/7/fixes?muted=1&field=title", nil, nil)
	if r.status != 200 || !reflect.DeepEqual(rep.fixFilter, report.FixFilter{Field: "title", Muted: true, MutedTypes: types}) {
		t.Fatalf("muted = %d, filter %+v", r.status, rep.fixFilter)
	}
	for _, want := range []string{`name="pick" value="2:title"`, `name="action" value="approve"`, `<input type="hidden" name="muted" value="1">`, `href="/runs/7/fixes?field=title"`, "Muted types only"} {
		if !strings.Contains(r.body, want) {
			t.Errorf("muted view lacks %q", want)
		}
	}
	if strings.Contains(r.body, "hidden in muted types") {
		t.Error("the muted view shows the hidden count")
	}

	// The flag is validated like the other filters.
	for _, q := range []string{"muted=0", "muted=yes", "muted=", "muted=1&muted=1"} {
		if r := e.do(t, "GET", "/runs/7/fixes?"+q, nil, nil); r.status != 400 {
			t.Errorf("%s = %d, want 400", q, r.status)
		}
	}

	// With no muted type nothing is left out, and the muted view is empty.
	none := fixingEnv(t, &fakeFixing{}, rep)
	none.do(t, "GET", "/runs/7/fixes?muted=1", nil, nil)
	if !rep.fixFilter.Muted || len(rep.fixFilter.MutedTypes) != 0 {
		t.Errorf("filter %+v", rep.fixFilter)
	}

	// A failure reading the decisions is a failed page, not an unmuted one.
	broken := fixingEnv(t, &fakeFixing{muteErr: errors.New("boom")}, rep)
	if r := broken.do(t, "GET", "/runs/7/fixes", nil, nil); r.status != 500 {
		t.Errorf("mute error = %d, want 500", r.status)
	}
}

func TestDecideKeepsTheMutedFilter(t *testing.T) {
	f := &fakeFixing{mute: []fix.TypeMute{mutedType}}
	e := fixingEnv(t, f, &fakeReports{fixes: report.FixPage{Rows: []report.FixRow{{RunID: 7, BookID: 2, Title: "Two", Tier: "A", Proposal: prop("title", "Old", "New")}}}})
	token := e.csrfToken(t)
	r := e.do(t, "POST", "/runs/7/fixes/decide", url.Values{"csrf_token": {token}, "action": {"reject"}, "pick": {"2:title"}, "muted": {"1"}}, nil)
	if r.status != 303 || r.header.Get("Location") != "/runs/7/fixes?muted=1" {
		t.Fatalf("decide = %d, Location %q", r.status, r.header.Get("Location"))
	}
	if calls := f.recorded(); len(calls) != 1 || calls[0].Method != "Reject" {
		t.Errorf("calls %+v", calls)
	}
	r = e.do(t, "POST", "/runs/7/fixes/decide", url.Values{"csrf_token": {token}, "action": {"reject"}, "pick": {"2:title"}, "muted": {"2"}}, nil)
	if r.status != 400 {
		t.Errorf("bad muted flag = %d, want 400", r.status)
	}
}

func TestPrecisionShowsMuting(t *testing.T) {
	at := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	held := fix.TypeMute{LibraryID: "library", FixType: fix.MuteType{Field: "publisher", Path: "text", Kind: "fill"}, Fixes: 14, Misses: 14, Upper: 0.18, Held: true, Unmute: &fix.Unmute{By: "admin", At: at}, Since: 4}
	young := fix.TypeMute{LibraryID: "library", FixType: fix.MuteType{Field: "isbn", Path: "file", Kind: "fill"}, Fixes: 3, Misses: 3, Upper: 1}
	good := fix.TypeMute{LibraryID: "library", FixType: fix.MuteType{Field: "authors", Path: "dnb", Kind: "replace"}, Fixes: 40, Misses: 1, Upper: 0.999}
	f := &fakeFixing{mute: []fix.TypeMute{mutedType, held, young, good}}
	e := fixingEnv(t, f, nil)
	r := e.do(t, "GET", "/precision", nil, nil)
	if r.status != 200 {
		t.Fatalf("precision = %d", r.status)
	}
	for _, want := range []string{
		"<h2>Muting</h2>", "20.0 %", "Muted 2026-11-08 by the rule", `<th scope="col">Library</th>`, "<td>library</td>", "12", "Unmuted by admin on 2026-11-02; 4 of 10 decisions since",
		"Shown; needs 10 decided fixes", ">Shown<",
		`action="/precision/unmute"`, `name="csrf_token"`, `name="library_id" value="library"`, `name="path" value="dnb&#43;file"`, `>Unmute</button>`,
	} {
		if !strings.Contains(r.body, want) {
			t.Errorf("precision page lacks %q", want)
		}
	}
	if n := strings.Count(r.body, ">Unmute</button>"); n != 1 {
		t.Errorf("%d unmute buttons, want 1 (only the muted type)", n)
	}
}

func TestUnmute(t *testing.T) {
	f := &fakeFixing{mute: []fix.TypeMute{mutedType}}
	e := fixingEnv(t, f, nil)
	token := e.csrfToken(t)
	good := func() url.Values {
		return url.Values{"csrf_token": {token}, "library_id": {"library"}, "field": {"title"}, "path": {"dnb+file"}, "kind": {"replace"}}
	}
	r := e.do(t, "POST", "/precision/unmute", good(), nil)
	if r.status != 303 || r.header.Get("Location") != "/precision" {
		t.Fatalf("unmute = %d, Location %q", r.status, r.header.Get("Location"))
	}
	want := []unmuteCall{{"library", fix.MuteType{Field: "title", Path: "dnb+file", Kind: "replace"}, "admin"}}
	if !reflect.DeepEqual(f.unmutes, want) {
		t.Fatalf("unmutes = %+v, want %+v", f.unmutes, want)
	}

	for name, mutate := range map[string]func(url.Values){
		"no library":    func(v url.Values) { v.Del("library_id") },
		"no field":      func(v url.Values) { v.Del("field") },
		"no path":       func(v url.Values) { v.Del("path") },
		"no kind":       func(v url.Values) { v.Del("kind") },
		"extra key":     func(v url.Values) { v.Set("force", "1") },
		"two fields":    func(v url.Values) { v.Add("field", "authors") },
		"no csrf token": func(v url.Values) { v.Del("csrf_token") },
	} {
		before := len(f.unmutes)
		form := good()
		mutate(form)
		if r := e.do(t, "POST", "/precision/unmute", form, nil); r.status < 400 || len(f.unmutes) != before {
			t.Errorf("%s = %d, unmutes %d", name, r.status, len(f.unmutes)-before)
		}
	}
	if r := e.do(t, "POST", "/precision/unmute?x=1", good(), nil); r.status != 400 {
		t.Errorf("a query string = %d, want 400", r.status)
	}

	f.unmuteErr = fix.ErrNotMuted
	if r := e.do(t, "POST", "/precision/unmute", good(), nil); r.status != 409 {
		t.Errorf("a type that isn't muted = %d, want 409", r.status)
	}
	f.unmuteErr = errors.New("boom")
	if r := e.do(t, "POST", "/precision/unmute", good(), nil); r.status != 500 {
		t.Errorf("a failure = %d, want 500", r.status)
	}
}
