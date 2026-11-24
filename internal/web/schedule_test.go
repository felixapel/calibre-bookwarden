package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/schedule"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

type fakeSchedule struct {
	st  schedule.Status
	err error
}

func (f fakeSchedule) Status(context.Context) (schedule.Status, error) { return f.st, f.err }

func TestStartRunMode(t *testing.T) {
	tests := []struct {
		name  string
		form  url.Values
		want  int
		scope verify.Scope
	}{
		{"no mode is a full check", url.Values{}, http.StatusSeeOther, verify.Scope{}},
		{"full", url.Values{"mode": {"full"}}, http.StatusSeeOther, verify.Scope{}},
		{"incremental", url.Values{"mode": {"incremental"}}, http.StatusSeeOther, verify.Scope{Incremental: true}},
		{"incremental for one book", url.Values{"mode": {"incremental"}, "book_id": {"3"}}, http.StatusSeeOther, verify.Scope{Incremental: true, BookID: 3}},
		{"an unknown mode", url.Values{"mode": {"partial"}}, http.StatusBadRequest, verify.Scope{}},
		{"the mode twice", url.Values{"mode": {"full", "incremental"}}, http.StatusBadRequest, verify.Scope{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := signedIn(t)
			form := tt.form
			form.Set("csrf_token", e.csrfToken(t))
			r := e.do(t, http.MethodPost, "/runs", form, nil)
			if r.status != tt.want {
				t.Fatalf("POST /runs: %d, want %d: %s", r.status, tt.want, r.body)
			}
			if tt.want != http.StatusSeeOther {
				if len(e.runs.started) != 0 {
					t.Errorf("a refused request started %v", e.runs.started)
				}
				return
			}
			if len(e.runs.started) != 1 || e.runs.started[0] != tt.scope {
				t.Errorf("started %v, want [%v]", e.runs.started, tt.scope)
			}
		})
	}
}

func TestRunsPageOffersFullAndIncremental(t *testing.T) {
	e := signedIn(t, func(o *Options) { o.RetainRuns = 10 })
	e.runs.add(verify.RunView{Run: verify.Run{ID: 3, Status: verify.RunCompleted, Scope: verify.Scope{Incremental: true, ScheduledFor: time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC)}}})
	e.runs.add(verify.RunView{Run: verify.Run{ID: 2, Status: verify.RunCompleted}})
	r := e.do(t, http.MethodGet, "/runs", nil, nil)
	for _, want := range []string{
		`name="mode" value="full" checked`, `name="mode" value="incremental"`,
		"Bookwarden keeps the last 10 runs,",
		"incremental, scheduled",
	} {
		if !strings.Contains(r.body, want) {
			t.Errorf("runs page lacks %q:\n%s", want, r.body)
		}
	}
	if strings.Contains(r.body, `value="incremental" checked`) {
		t.Error("incremental is the default; a full check must be")
	}
}

func TestRunsPageRetentionKeepAll(t *testing.T) {
	e := signedIn(t)
	e.runs.add(verify.RunView{Run: verify.Run{ID: 2, Status: verify.RunCompleted}})
	if r := e.do(t, http.MethodGet, "/runs", nil, nil); !strings.Contains(r.body, "Bookwarden keeps every run,") {
		t.Errorf("runs page with no limit:\n%s", r.body)
	}
}

func TestProgressShowsReuse(t *testing.T) {
	e := signedIn(t)
	e.runs.add(verify.RunView{
		Run:      verify.Run{ID: 7, Status: verify.RunCompleted, Scope: verify.Scope{Incremental: true}},
		Progress: verify.Progress{Total: 10, Checked: 10, Reused: 8},
	})
	e.runs.add(verify.RunView{
		Run:      verify.Run{ID: 8, Status: verify.RunCompleted},
		Progress: verify.Progress{Total: 10, Checked: 10},
	})
	r := e.do(t, http.MethodGet, "/runs/7/progress", nil, nil)
	for _, want := range []string{"Incremental check", "8 of 10 books reused their file evidence", "<dt>Reused</dt><dd>8</dd>"} {
		if !strings.Contains(r.body, want) {
			t.Errorf("incremental progress lacks %q:\n%s", want, r.body)
		}
	}
	if r := e.do(t, http.MethodGet, "/runs/8/progress", nil, nil); strings.Contains(r.body, "Reused") || strings.Contains(r.body, "Incremental") {
		t.Errorf("a full run's progress mentions reuse:\n%s", r.body)
	}
}

func TestSettingsSchedule(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	lastSlot := time.Date(2026, 11, 1, 4, 0, 0, 0, berlin)
	tests := []struct {
		name   string
		sched  Schedule
		retain int
		has    []string
		lacks  []string
	}{
		{
			name: "no scheduler wired", sched: nil, retain: 10,
			has: []string{"<h2>Scheduled checks</h2>", "Scheduled checks: <strong>Off</strong>", "BOOKWARDEN_SCHEDULE_INCREMENTAL", "BOOKWARDEN_SCHEDULE_FULL", "keeps the last 10 runs"},
		},
		{
			name: "configured off", sched: fakeSchedule{st: schedule.Status{Zone: "UTC"}}, retain: 0,
			has: []string{"Scheduled checks: <strong>Off</strong>", "keeps every run"},
		},
		{
			name: "both on",
			sched: fakeSchedule{st: schedule.Status{
				Enabled: true, Incremental: "03:30", Full: "sun 04:00", Zone: "Europe/Berlin",
				NextIncremental: time.Date(2026, 11, 2, 3, 30, 0, 0, berlin), NextFull: time.Date(2026, 11, 8, 4, 0, 0, 0, berlin),
				Last: &schedule.LastRun{RunID: 12, Slot: schedule.Slot{Kind: schedule.Full, At: lastSlot}, Status: "completed"},
			}}, retain: 10,
			has: []string{
				"Scheduled checks: <strong>On</strong>", "Incremental check, every day at <strong>03:30</strong>", "Full check, every <strong>sun 04:00</strong>",
				"Europe/Berlin", "2026-11-02 03:30 CET", "2026-11-08 04:00 CET",
				`href="/runs/12"`, "full", "2026-11-01 04:00 CET",
			},
			lacks: []string{"Skipped"},
		},
		{
			name: "only full, with a skipped slot",
			sched: fakeSchedule{st: schedule.Status{
				Enabled: true, Full: "sun 04:00", Zone: "Europe/Berlin", NextFull: time.Date(2026, 11, 8, 4, 0, 0, 0, berlin),
				Skipped: &schedule.Skip{Slot: schedule.Slot{Kind: schedule.Incremental, At: lastSlot}, Reason: "schedule: slot skipped: a check is already running", At: lastSlot},
			}}, retain: 10,
			has:   []string{"Full check, every <strong>sun 04:00</strong>", "Incremental check: <strong>off</strong>", "Skipped", "a check is already running"},
			lacks: []string{"Last scheduled check"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := signedIn(t, func(o *Options) { o.Schedule, o.RetainRuns = tt.sched, tt.retain })
			r := e.do(t, http.MethodGet, "/settings", nil, nil)
			if r.status != http.StatusOK {
				t.Fatalf("GET /settings: %d", r.status)
			}
			for _, s := range tt.has {
				if !strings.Contains(r.body, s) {
					t.Errorf("settings lacks %q:\n%s", s, r.body)
				}
			}
			for _, s := range tt.lacks {
				if strings.Contains(r.body, s) {
					t.Errorf("settings contains %q", s)
				}
			}
		})
	}
}

func TestSettingsScheduleError(t *testing.T) {
	e := signedIn(t, func(o *Options) { o.Schedule = fakeSchedule{err: errors.New("database is down")} })
	if r := e.do(t, http.MethodGet, "/settings", nil, nil); r.status != http.StatusInternalServerError {
		t.Errorf("GET /settings with a failing history: %d, want 500", r.status)
	}
}
