package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/store"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

var allSeats = []SeatStatus{{Name: "haiku", Model: "m1", Configured: true}, {Name: "deepseek", Model: "m2", Configured: true}, {Name: "gemini", Model: "m3", Configured: true}}

func (w *world) service(seats []SeatStatus, fakes []*fakeSeat) *Service {
	return NewService(ServiceOptions{Reviewer: w.reviewer(fakes), Store: w.agents, Decisions: w.fixes, Seats: seats, Retry: 20 * time.Millisecond})
}

func setMode(t *testing.T, w *world, mode string) {
	t.Helper()
	if err := w.agents.SetMode(context.Background(), mode, "felix"); err != nil {
		t.Fatal(err)
	}
}

func TestRunFinishedQueues(t *testing.T) {
	missing := []SeatStatus{allSeats[0], allSeats[1], {Name: "gemini", Configured: false}}
	for _, tc := range []struct {
		name   string
		mode   string
		seats  []SeatStatus
		status verify.RunStatus
		want   string // the queued review's mode, or "" for none
	}{
		{"off", store.ModeOff, allSeats, verify.RunCompleted, ""},
		{"shadow", store.ModeShadow, allSeats, verify.RunCompleted, store.ModeShadow},
		{"shadow with a seat missing", store.ModeShadow, missing, verify.RunCompleted, ""},
		{"failed run", store.ModeShadow, allSeats, verify.RunFailed, ""},
		{"cancelled run", store.ModeShadow, allSeats, verify.RunCancelled, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			run := w.run(book(1, "One"))
			setMode(t, w, tc.mode)
			w.service(tc.seats, panel(nil)).RunFinished(context.Background(), run, tc.status)
			rs, err := w.agents.Reviews(context.Background(), run)
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == "" {
				if len(rs) != 0 {
					t.Fatalf("queued %+v", rs)
				}
				return
			}
			if len(rs) != 1 || rs[0].Kind != store.ReviewAuto || rs[0].Mode != tc.want || rs[0].RequestedBy != store.AgentActor {
				t.Fatalf("reviews = %+v", rs)
			}
		})
	}
}

func TestSetMode(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	svc := w.service(allSeats, panel(nil))
	for _, mode := range []string{"on", "auto", ""} {
		if err := svc.SetMode(ctx, mode, "felix"); err == nil || !strings.Contains(err.Error(), "invalid mode") {
			t.Fatalf("SetMode(%q) = %v, want an invalid mode error", mode, err)
		}
	}
	if m, _ := svc.Mode(ctx); m != store.ModeOff {
		t.Fatalf("Mode after refused changes = %q, want off", m)
	}
	if err := svc.SetMode(ctx, store.ModeShadow, "felix"); err != nil {
		t.Fatalf("shadow = %v", err)
	}
	if m, _ := svc.Mode(ctx); m != store.ModeShadow {
		t.Fatalf("Mode = %q", m)
	}
	short := w.service([]SeatStatus{allSeats[0], {Name: "deepseek"}, allSeats[2]}, panel(nil))
	if err := short.SetMode(ctx, store.ModeShadow, "felix"); !errors.Is(err, ErrSeatsMissing) || !strings.Contains(err.Error(), "deepseek") {
		t.Fatalf("shadow with a seat missing = %v", err)
	}
	if err := short.SetMode(ctx, store.ModeOff, "felix"); err != nil {
		t.Fatalf("off with a seat missing = %v", err)
	}
}

func TestRequest(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	run := w.run(book(1, "One"))
	svc := w.service(allSeats, panel(nil))
	if err := svc.Request(ctx, run, "felix"); !errors.Is(err, ErrOff) {
		t.Fatalf("Request while off = %v", err)
	}
	setMode(t, w, store.ModeShadow)
	if err := svc.Request(ctx, run, "felix"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Request(ctx, run, "felix"); !errors.Is(err, ErrQueued) {
		t.Fatalf("second Request = %v, want ErrQueued", err)
	}
	rs, _ := w.agents.Reviews(ctx, run)
	if len(rs) != 1 || rs[0].Kind != store.ReviewManual || rs[0].Mode != store.ModeShadow || rs[0].RequestedBy != "felix" {
		t.Fatalf("reviews = %+v", rs)
	}
}

// serve runs svc until the test ends.
func serveFor(t *testing.T, svc *Service) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- svc.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Serve = %v", err)
		}
	})
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func reviewState(w *world, run int64) string {
	rs, _ := w.agents.Reviews(context.Background(), run)
	if len(rs) == 0 {
		return ""
	}
	return rs[len(rs)-1].State
}

func TestServeResumesRunning(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	run := w.run(book(1, "One"))
	setMode(t, w, store.ModeShadow)
	rv, _, _ := w.agents.Queue(ctx, run, store.ReviewAuto, store.ModeShadow, store.AgentActor)
	_ = w.agents.Start(ctx, rv.ID)
	serveFor(t, w.service(allSeats, panel(nil)))
	waitUntil(t, "the running review to finish", func() bool { return reviewState(w, run) == store.ReviewDone })
}

// Serving a shadow review records the verdicts and decides nothing: no
// approval, no rejection, no operation.
func TestServeShadowDecidesNothing(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	run := w.run(book(1, "One"))
	setMode(t, w, store.ModeShadow)
	svc := w.service(allSeats, panel(nil))
	serveFor(t, svc)
	svc.RunFinished(ctx, run, verify.RunCompleted)
	waitUntil(t, "the review to finish", func() bool { return reviewState(w, run) == store.ReviewDone })
	rows, err := w.fixes.RunProposals(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		t.Errorf("proposal %s was decided: %s by %s", r.Proposal.Field, r.Status, r.DecidedBy)
	}
	wantState(t, w.state(run, 1), map[string]string{
		"title_sort outcome": "left: shadow: approved", "author_sort outcome": "left: shadow: approved",
	})
}

func TestRunSummary(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	run := w.run(book(1, "One"), book(2, "Split"), book(3, "Felix"))
	setMode(t, w, store.ModeShadow)
	svc := w.service(allSeats, nil)
	if sum, err := svc.RunSummary(ctx, run); err != nil || sum.Reviewed {
		t.Fatalf("RunSummary before a review = %+v, %v", sum, err)
	}
	seats := panel(map[string]map[string]string{"One": {"author_sort": Reject}})
	seats[0].script = map[string]map[string]string{"One": {"author_sort": Reject}, "Split": {"title_sort": Reject, "author_sort": Unsure}, "Felix": {"title_sort": Unsure}}
	if _, err := w.reviewer(seats).Review(ctx, w.review(run, store.ReviewAuto, store.ModeShadow)); err != nil {
		t.Fatal(err)
	}
	// Felix decides one the panel judged, so it is no longer his to do.
	if err := w.svc.Reject(ctx, run, 3, []string{"title_sort"}, "felix", store.Basis{Mode: store.DecisionBulk}); err != nil {
		t.Fatal(err)
	}
	sum, err := svc.RunSummary(ctx, run)
	if err != nil || !sum.Reviewed || sum.Applied != 0 || sum.Approved != 0 || sum.Rejected != 0 || sum.ForYou != 5 || sum.Tokens["haiku"] != 60 {
		t.Fatalf("RunSummary = %+v, %v", sum, err)
	}
	vs, err := svc.Verdicts(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	got := vs[Key{BookID: 2, Field: "author_sort"}]
	if len(got) != 3 || got[0].Seat != "haiku" || got[0].Answer != Unsure || got[1].Seat != "deepseek" || got[2].Seat != "gemini" {
		t.Fatalf("verdicts = %+v", got)
	}
	os, err := svc.Outcomes(ctx, run)
	if err != nil || os[Key{BookID: 2, Field: "author_sort"}].Reason != "agents disagree" || os[Key{BookID: 1, Field: "title_sort"}].Reason != "shadow: approved" {
		t.Fatalf("outcomes = %+v, %v", os, err)
	}
}

func TestServeSkipsReviewsWithSeatMissing(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	run := w.run(book(1, "One"))
	setMode(t, w, store.ModeShadow)
	rv, _, _ := w.agents.Queue(ctx, run, store.ReviewAuto, store.ModeShadow, store.AgentActor)
	seats := panel(nil)
	serveFor(t, w.service([]SeatStatus{allSeats[0], allSeats[1], {Name: "gemini"}}, seats))
	time.Sleep(150 * time.Millisecond)
	if n := calls(seats); n != 0 {
		t.Fatalf("%d seat calls with a judge missing", n)
	}
	if rs, _ := w.agents.Reviews(ctx, run); rs[0].ID != rv.ID || rs[0].State != store.ReviewQueued {
		t.Fatalf("review = %+v, want it left queued", rs[0])
	}
}

// failingEvidence fails every read, as a book that always errors would.
type failingEvidence struct{}

func (failingEvidence) Evidence(context.Context, int64, int) ([]byte, string, error) {
	return nil, "", errors.New("evidence unreadable")
}

func TestServeStopsFailingReview(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	run := w.run(book(1, "One"))
	setMode(t, w, store.ModeShadow)
	if _, _, err := w.agents.Queue(ctx, run, store.ReviewAuto, store.ModeShadow, store.AgentActor); err != nil {
		t.Fatal(err)
	}
	r := w.reviewer(panel(nil), func(o *Options) { o.Evidence = failingEvidence{} })
	svc := NewService(ServiceOptions{Reviewer: r, Store: w.agents, Decisions: w.fixes, Seats: allSeats, Retry: 10 * time.Millisecond})
	serveFor(t, svc)
	waitUntil(t, "the failing review to stop", func() bool { return reviewState(w, run) == store.ReviewStopped })
	rs, _ := w.agents.Reviews(ctx, run)
	if !strings.HasPrefix(rs[0].StopReason, "failed repeatedly") {
		t.Fatalf("stop reason %q", rs[0].StopReason)
	}
}

// The page shows only verdicts the reviewer would use: a newer verdict on
// another proposal doesn't replace the one on this proposal.
func TestServiceVerdictsSkipOtherProposals(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	run := w.run(book(1, "One"))
	setMode(t, w, store.ModeShadow)
	svc := w.service(allSeats, nil)
	if _, err := w.reviewer(panel(nil)).Review(ctx, w.review(run, store.ReviewAuto, store.ModeShadow)); err != nil {
		t.Fatal(err)
	}
	later, _, _ := w.agents.Queue(ctx, run, store.ReviewManual, store.ModeShadow, "felix")
	stale := store.Verdict{ReviewID: later.ID, RunID: run, BookID: 1, Field: "title_sort", Seat: "haiku", PromptVersion: PromptVersion, ProposalHash: "another", Answer: Reject}
	if err := w.agents.AddVerdict(ctx, stale); err != nil {
		t.Fatal(err)
	}
	vs, err := svc.Verdicts(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	got := vs[Key{BookID: 1, Field: "title_sort"}]
	if len(got) != 3 || got[0].Seat != "haiku" || got[0].Answer != Approve {
		t.Fatalf("verdicts = %+v, want haiku's approve on this proposal", got)
	}
}

// A book-level outcome (no field) isn't a proposal waiting for a person.
func TestRunSummaryIgnoresBookLevelOutcomes(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	run := w.run(book(1, "One"))
	rv := w.review(run, store.ReviewAuto, store.ModeShadow)
	if err := w.agents.AddOutcome(ctx, store.Outcome{ReviewID: rv.ID, RunID: run, BookID: 1, Field: "", Outcome: store.OutcomeLeft, Reason: "evidence unavailable: missing"}); err != nil {
		t.Fatal(err)
	}
	sum, err := w.service(allSeats, nil).RunSummary(ctx, run)
	if err != nil || sum.ForYou != 0 {
		t.Fatalf("RunSummary = %+v, %v; want ForYou 0", sum, err)
	}
}
