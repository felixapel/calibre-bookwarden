package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

func TestPinRun(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		form     func(tok string) url.Values
		want     int
		wantPins map[int64]bool
	}{
		{"pin", "/runs/7/pin", func(tok string) url.Values { return url.Values{"csrf_token": {tok}, "value": {"on"}} }, http.StatusSeeOther, map[int64]bool{7: true}},
		{"unpin", "/runs/7/pin", func(tok string) url.Values { return url.Values{"csrf_token": {tok}, "value": {"off"}} }, http.StatusSeeOther, map[int64]bool{7: false}},
		{"no value", "/runs/7/pin", func(tok string) url.Values { return url.Values{"csrf_token": {tok}} }, http.StatusBadRequest, nil},
		{"another value", "/runs/7/pin", func(tok string) url.Values { return url.Values{"csrf_token": {tok}, "value": {"maybe"}} }, http.StatusBadRequest, nil},
		{"no CSRF token", "/runs/7/pin", func(string) url.Values { return url.Values{"value": {"on"}} }, http.StatusForbidden, nil},
		{"a wrong CSRF token", "/runs/7/pin", func(string) url.Values { return url.Values{"csrf_token": {"nope"}, "value": {"on"}} }, http.StatusForbidden, nil},
		{"an unknown run", "/runs/99/pin", func(tok string) url.Values { return url.Values{"csrf_token": {tok}, "value": {"on"}} }, http.StatusNotFound, nil},
		{"a bad id", "/runs/abc/pin", func(tok string) url.Values { return url.Values{"csrf_token": {tok}, "value": {"on"}} }, http.StatusNotFound, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := signedIn(t)
			e.runs.add(running(7))
			r := e.do(t, http.MethodPost, tt.path, tt.form(e.csrfToken(t)), nil)
			if r.status != tt.want {
				t.Fatalf("POST %s: %d, want %d: %s", tt.path, r.status, tt.want, r.body)
			}
			if tt.want == http.StatusSeeOther && r.header.Get("Location") != "/runs/7" {
				t.Errorf("Location = %q, want /runs/7", r.header.Get("Location"))
			}
			if len(e.runs.pins) != len(tt.wantPins) {
				t.Errorf("pins = %v, want %v", e.runs.pins, tt.wantPins)
			}
			for id, want := range tt.wantPins {
				if got, ok := e.runs.pins[id]; !ok || got != want {
					t.Errorf("pins = %v, want %v", e.runs.pins, tt.wantPins)
				}
			}
		})
	}
}

func TestPinRunNeedsSignIn(t *testing.T) {
	e := newEnv(t)
	e.setUpAdmin(t)
	e.runs.add(running(7))
	if r := e.do(t, http.MethodPost, "/runs/7/pin", url.Values{"value": {"on"}}, nil); r.status != http.StatusUnauthorized {
		t.Errorf("signed out: %d, want 401", r.status)
	}
	if len(e.runs.pins) != 0 {
		t.Errorf("a signed-out request pinned %v", e.runs.pins)
	}
}

func TestRunPageOffersThePinToggle(t *testing.T) {
	e := signedIn(t)
	e.runs.add(running(7))
	pinned := running(8)
	pinned.Run.Pinned = true
	e.runs.add(pinned)

	r := e.do(t, http.MethodGet, "/runs/7", nil, nil)
	for _, want := range []string{`action="/runs/7/pin"`, `name="value" value="on"`, "Keep this run"} {
		if !strings.Contains(r.body, want) {
			t.Errorf("unpinned run page lacks %q", want)
		}
	}
	if strings.Contains(r.body, "Unpin") {
		t.Error("an unpinned run offers Unpin")
	}
	r = e.do(t, http.MethodGet, "/runs/8", nil, nil)
	for _, want := range []string{`action="/runs/8/pin"`, `name="value" value="off"`, "Unpin", "kept whatever the retention setting"} {
		if !strings.Contains(r.body, want) {
			t.Errorf("pinned run page lacks %q", want)
		}
	}
	if strings.Contains(r.body, "Keep this run") {
		t.Error("a pinned run offers Keep this run")
	}
	// The list marks it.
	e.runs.add(verify.RunView{Run: verify.Run{ID: 9, Status: verify.RunCompleted, Pinned: true}})
	if r := e.do(t, http.MethodGet, "/runs", nil, nil); !strings.Contains(r.body, "pinned") {
		t.Errorf("the runs list doesn't mark a pinned run:\n%s", r.body)
	}
}
