package web

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/store"
)

type fakeLedger struct {
	b   store.BurnIn
	err error
}

func (f fakeLedger) BurnIn(context.Context) (store.BurnIn, error) { return f.b, f.err }

// TestRunsPageShowsTheBurnIn: the Runs page shows the three burn-in numbers
// against their targets, and the ledger rows below them.
func TestRunsPageShowsTheBurnIn(t *testing.T) {
	zero := 0
	e := newEnv(t, func(o *Options) {
		o.Ledger = fakeLedger{b: store.BurnIn{
			CleanStreak: 13, SealPending: true, DailyP95: 63*time.Minute + 56*time.Second, DailyRuns: 11, LongestFull: 15*time.Minute + 42*time.Second,
			Rows: []store.LedgerRow{
				{RunID: 31, Kind: "incremental", Scheduled: true, Status: "completed", StartedAt: time.Date(2026, 11, 23, 2, 30, 0, 0, time.UTC), Books: 12},
				{RunID: 30, Kind: "full", Scheduled: true, Status: "completed", StartedAt: time.Date(2026, 11, 22, 3, 0, 0, 0, time.UTC),
					Duration: 15*time.Minute + 42*time.Second, Books: 3236, Reused: 0, SealBroken: &zero},
			},
		}}
	})
	e.setUpAdmin(t)
	e.logIn(t)
	r := e.do(t, http.MethodGet, "/runs", nil, nil)
	for _, want := range []string{"Burn-in", "13 of 14", "seal check of the newest run is still pending", "1:03:56", "11 daily runs", "15:42", "target 10:00", "target 2:00:00", "3236", "<td>0</td></tr>", "not checked"} {
		if !strings.Contains(r.body, want) {
			t.Errorf("Runs page lacks %q", want)
		}
	}
}

// TestRunsPageWithoutLedger: with no ledger the panel is hidden.
func TestRunsPageWithoutLedger(t *testing.T) {
	e := newEnv(t)
	e.setUpAdmin(t)
	e.logIn(t)
	if r := e.do(t, http.MethodGet, "/runs", nil, nil); strings.Contains(r.body, "Burn-in") {
		t.Error("Runs page shows a burn-in panel without a ledger")
	}
}

// TestRunsPageSurvivesALedgerError: a ledger that can't be read hides the
// panel; the runs themselves still show.
func TestRunsPageSurvivesALedgerError(t *testing.T) {
	e := newEnv(t, func(o *Options) { o.Ledger = fakeLedger{err: errors.New("db")} })
	e.setUpAdmin(t)
	e.logIn(t)
	r := e.do(t, http.MethodGet, "/runs", nil, nil)
	if r.status != http.StatusOK || strings.Contains(r.body, "Burn-in") {
		t.Errorf("Runs page = %d, burn-in shown %v; want 200 without the panel", r.status, strings.Contains(r.body, "Burn-in"))
	}
}
