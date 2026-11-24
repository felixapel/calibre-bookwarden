package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/decide"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/fix"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/store"
	"github.com/felixapel/calibre-bookwarden/internal/testutil/pgtest"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

// world is a store with checked runs, the real writer's decisions and fake
// seats.
type world struct {
	t      *testing.T
	agents *store.AgentRepo
	runs   *store.RunRepo
	fixes  *store.FixRepo
	svc    *fix.Service
	st     *store.Store
}

func newWorld(t *testing.T) *world {
	t.Helper()
	st, err := store.Open(context.Background(), pgtest.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	w := &world{t: t, st: st, agents: store.NewAgentRepo(st), runs: store.NewRunRepo(st), fixes: store.NewFixRepo(st)}
	w.svc = fix.New(w.fixes, w.runs, nil, nil)
	return w
}

// book is a package whose title sort and author sort are both wrong, so it
// has a title_sort and an author_sort proposal.
func book(id int, title string) evidence.Package {
	wrong := "Wrong Sort"
	return evidence.Package{
		SchemaVersion: evidence.SchemaVersion, LibraryID: "library", BookID: id, UUID: fmt.Sprintf("u-%d", id),
		Calibre: evidence.Record{Title: title, Authors: []string{"Ada Reed"}, AuthorSort: "Ada Reed", Languages: []string{"eng"},
			Identifiers: map[string]string{}, TitleSort: &wrong, AuthorSortMap: map[string]string{"Ada Reed": "Reed, Ada"}},
	}
}

// run stores a completed run with the packages checked.
func (w *world) run(pkgs ...evidence.Package) int64 {
	w.t.Helper()
	ctx := context.Background()
	var refs []library.BookRef
	for _, p := range pkgs {
		refs = append(refs, library.BookRef{ID: p.BookID, UUID: p.UUID})
	}
	run, err := w.runs.CreateRun(ctx, verify.Scope{}, "library", "v1", refs)
	if err != nil {
		w.t.Fatal(err)
	}
	for i, p := range pkgs {
		raw, seal, err := decide.Seal(ctx, p)
		if err != nil {
			w.t.Fatal(err)
		}
		if err := w.runs.FinishBook(ctx, run.ID, refs[i], verify.Outcome{State: verify.BookChecked, Evidence: raw, Seal: seal}); err != nil {
			w.t.Fatal(err)
		}
	}
	if err := w.runs.SetStatus(ctx, run.ID, verify.RunCompleted, ""); err != nil {
		w.t.Fatal(err)
	}
	return run.ID
}

// fakeSeat answers from a script: title → field → answer; approve when the
// script says nothing. err makes every call fail with that code.
type fakeSeat struct {
	name    string
	script  map[string]map[string]string
	err     string
	tokens  int64
	onCall  func(packet string)
	calls   atomic.Int32
	mu      sync.Mutex
	packets []string
}

func (f *fakeSeat) Name() string  { return f.name }
func (f *fakeSeat) Model() string { return "fake-" + f.name }

func (f *fakeSeat) Judge(_ context.Context, _, packet string) (Reply, error) {
	f.calls.Add(1)
	f.mu.Lock()
	f.packets = append(f.packets, packet)
	f.mu.Unlock()
	if f.onCall != nil {
		f.onCall(packet)
	}
	if f.err != "" {
		return Reply{}, &seatError{seat: f.name, code: f.err}
	}
	line := packet[strings.Index(packet, "Fields: ")+len("Fields: "):]
	fields := strings.Split(line[:strings.Index(line, ".\n")], ", ")
	var data struct {
		Calibre struct{ Title string } `json:"calibre"`
	}
	_, block, _ := dataBlock(packet)
	if err := json.Unmarshal([]byte(block), &data); err != nil {
		return Reply{}, err
	}
	var out []map[string]string
	for _, field := range fields {
		a := f.script[data.Calibre.Title][field]
		if a == "" {
			a = Approve
		}
		out = append(out, map[string]string{"field": field, "verdict": a, "reason": f.name + " says " + a})
	}
	raw, _ := json.Marshal(map[string]any{"verdicts": out})
	return Reply{Text: string(raw), InputTokens: f.tokens, OutputTokens: f.tokens}, nil
}

func panel(script map[string]map[string]string) []*fakeSeat {
	return []*fakeSeat{{name: "haiku", script: script, tokens: 10}, {name: "deepseek", script: script, tokens: 10}, {name: "gemini", script: script, tokens: 10}}
}

func (w *world) reviewer(seats []*fakeSeat, opts ...func(*Options)) *Reviewer {
	ss := make([]Seat, len(seats))
	for i, s := range seats {
		ss[i] = s
	}
	o := Options{Store: w.agents, Evidence: w.runs, Fixes: w.fixes, Seats: ss, MaxTokens: 1_000_000}
	for _, f := range opts {
		f(&o)
	}
	return NewReviewer(o)
}

func (w *world) review(run int64, kind, mode string) store.Review {
	w.t.Helper()
	if err := w.agents.SetMode(context.Background(), mode, "felix"); err != nil {
		w.t.Fatal(err)
	}
	rv, ok, err := w.agents.Queue(context.Background(), run, kind, mode, "felix")
	if err != nil || !ok {
		w.t.Fatalf("Queue = %v, %v", ok, err)
	}
	return rv
}

// state is each field's decision and newest outcome for a book.
func (w *world) state(run int64, bookID int) map[string]string {
	w.t.Helper()
	ctx := context.Background()
	out := map[string]string{}
	rows, err := w.fixes.Proposals(ctx, run, bookID)
	if err != nil {
		w.t.Fatal(err)
	}
	for _, r := range rows {
		out[r.Proposal.Field] = r.Status + " by " + r.DecidedBy
	}
	os, err := w.agents.Outcomes(ctx, run)
	if err != nil {
		w.t.Fatal(err)
	}
	for _, o := range os {
		if o.BookID == bookID {
			out[o.Field+" outcome"] = o.Outcome + ": " + o.Reason
		}
	}
	return out
}

func calls(seats []*fakeSeat) int32 {
	var n int32
	for _, s := range seats {
		n += s.calls.Load()
	}
	return n
}

func wantState(t *testing.T, got, want map[string]string) {
	t.Helper()
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("unexpected %s = %q", k, got[k])
		}
	}
}

func TestReviewSplitPanel(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	run := w.run(book(1, "Approve Both"), book(2, "Split"))
	seats := panel(map[string]map[string]string{"Split": {"title_sort": Reject}})
	seats[2].script = map[string]map[string]string{"Split": {"title_sort": Reject, "author_sort": Unsure}}
	got, err := w.reviewer(seats).Review(ctx, w.review(run, store.ReviewAuto, store.ModeShadow))
	if err != nil || got.State != store.ReviewDone {
		t.Fatalf("Review = %+v, %v", got, err)
	}
	wantState(t, w.state(run, 1), map[string]string{
		"title_sort outcome": "left: shadow: approved", "author_sort outcome": "left: shadow: approved",
	})
	wantState(t, w.state(run, 2), map[string]string{
		"title_sort outcome": "left: shadow: rejected", "author_sort outcome": "left: agents disagree",
	})
	vs, _ := w.agents.Verdicts(ctx, run, PromptVersion)
	if len(vs) != 12 {
		t.Fatalf("%d verdicts, want 12 (2 books × 2 fields × 3 seats)", len(vs))
	}
	if n := calls(seats); n != 6 {
		t.Fatalf("%d seat calls, want one per book per seat", n)
	}
	toks, _ := w.agents.Tokens(ctx, run)
	if toks["haiku"] != 40 {
		t.Fatalf("tokens = %v, want 20 per call", toks)
	}
}

func TestReviewShadow(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	run := w.run(book(1, "Approve Both"))
	got, err := w.reviewer(panel(nil)).Review(ctx, w.review(run, store.ReviewAuto, store.ModeShadow))
	if err != nil || got.State != store.ReviewDone {
		t.Fatalf("Review = %+v, %v", got, err)
	}
	wantState(t, w.state(run, 1), map[string]string{
		"title_sort outcome": "left: shadow: approved", "author_sort outcome": "left: shadow: approved",
	})
}

func TestReviewSkipsDecided(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	run := w.run(book(1, "Approve Both"))
	if err := w.svc.Reject(ctx, run, 1, []string{"title_sort"}, "felix", store.Basis{Mode: store.DecisionBulk}); err != nil {
		t.Fatal(err)
	}
	seats := panel(nil)
	if _, err := w.reviewer(seats).Review(ctx, w.review(run, store.ReviewAuto, store.ModeShadow)); err != nil {
		t.Fatal(err)
	}
	for _, p := range seats[0].packets {
		if strings.Contains(p, "title_sort\",\"current") || strings.Contains(p, "Fields: author_sort, title_sort") {
			t.Fatalf("a decided field was sent: %s", p)
		}
	}
	wantState(t, w.state(run, 1), map[string]string{
		"title_sort": "rejected by felix", "author_sort outcome": "left: shadow: approved",
	})
}

func TestReviewSkipsDecidedDuringJudging(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	run := w.run(book(1, "Approve Both"))
	seats := panel(nil)
	var once sync.Once
	seats[1].onCall = func(string) {
		once.Do(func() {
			if err := w.svc.Reject(ctx, run, 1, []string{"title_sort"}, "felix", store.Basis{Mode: store.DecisionBulk}); err != nil {
				t.Error(err)
			}
		})
	}
	if _, err := w.reviewer(seats).Review(ctx, w.review(run, store.ReviewAuto, store.ModeShadow)); err != nil {
		t.Fatal(err)
	}
	wantState(t, w.state(run, 1), map[string]string{
		"title_sort": "rejected by felix", "title_sort outcome": "left: shadow: approved",
		"author_sort outcome": "left: shadow: approved",
	})
}

func TestReviewTokenCap(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	run := w.run(book(1, "One"), book(2, "Two"))
	seats := panel(nil)
	got, err := w.reviewer(seats, func(o *Options) { o.MaxTokens = 50; o.Books = 1 }).Review(ctx, w.review(run, store.ReviewAuto, store.ModeShadow))
	if err != nil || got.State != store.ReviewStopped || got.StopReason != "token cap reached" {
		t.Fatalf("Review = %+v, %v", got, err)
	}
	if n := calls(seats); n != 3 {
		t.Fatalf("%d calls, want only the first book's", n)
	}
	if s := w.state(run, 2); len(s) != 0 {
		t.Fatalf("book 2 = %v, want untouched", s)
	}
}

func TestReviewResumes(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	run := w.run(book(1, "One"), book(2, "Two"))
	rv := w.review(run, store.ReviewAuto, store.ModeShadow)
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	seats := panel(nil)
	for _, s := range seats {
		s.onCall = func(p string) {
			if strings.Contains(p, `"title":"Two"`) {
				cancel()
			}
		}
	}
	if _, err := w.reviewer(seats, func(o *Options) { o.Books = 1 }).Review(cctx, rv); !errors.Is(err, context.Canceled) {
		t.Fatalf("first Review = %v, want context.Canceled", err)
	}
	if next, ok, _ := w.agents.Next(ctx); !ok || next.ID != rv.ID || next.State != store.ReviewRunning {
		t.Fatalf("Next = %+v, %v; want the interrupted review running", next, ok)
	}
	again := panel(nil)
	got, err := w.reviewer(again, func(o *Options) { o.Books = 1 }).Review(ctx, rv)
	if err != nil || got.State != store.ReviewDone {
		t.Fatalf("second Review = %+v, %v", got, err)
	}
	for _, s := range again {
		for _, p := range s.packets {
			if strings.Contains(p, `"title":"One"`) {
				t.Fatalf("%s judged book 1 again", s.name)
			}
		}
	}
	for _, id := range []int{1, 2} {
		wantState(t, w.state(run, id), map[string]string{
			"title_sort outcome": "left: shadow: approved", "author_sort outcome": "left: shadow: approved",
		})
	}
}

func TestReviewSupersededBook(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	old := w.run(book(1, "One"))
	w.run(book(1, "One"))
	seats := panel(nil)
	if _, err := w.reviewer(seats).Review(ctx, w.review(old, store.ReviewAuto, store.ModeShadow)); err != nil {
		t.Fatal(err)
	}
	if n := calls(seats); n != 0 || len(w.state(old, 1)) != 0 {
		t.Fatalf("judged superseded evidence: %d calls, %v", n, w.state(old, 1))
	}
}

func TestReviewModeSwitchedOff(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	run := w.run(book(1, "One"), book(2, "Two"))
	rv := w.review(run, store.ReviewAuto, store.ModeShadow)
	seats := panel(nil)
	seats[0].onCall = func(string) { _ = w.agents.SetMode(ctx, store.ModeOff, "felix") }
	got, err := w.reviewer(seats, func(o *Options) { o.Books = 1 }).Review(ctx, rv)
	if err != nil || got.State != store.ReviewStopped || got.StopReason != "agent fixing switched off" {
		t.Fatalf("Review = %+v, %v", got, err)
	}
	if s := w.state(run, 1); s["title_sort"] != "" {
		t.Fatalf("decided after the switch went off: %v", s)
	}
}

func TestReviewCalibration(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	run := w.run(book(1, "One"), book(2, "Two"))
	if err := w.svc.Approve(ctx, run, 1, []string{"title_sort"}, "felix", store.Basis{Mode: store.DecisionBulk}); err != nil {
		t.Fatal(err)
	}
	w.run(book(2, "Two")) // supersedes book 2
	seats := panel(map[string]map[string]string{"One": {"title_sort": Reject}})
	got, err := w.reviewer(seats).Review(ctx, w.review(run, store.ReviewCalibration, store.ModeShadow))
	if err != nil || got.State != store.ReviewDone {
		t.Fatalf("Review = %+v, %v", got, err)
	}
	wantState(t, w.state(run, 1), map[string]string{
		"title_sort": "approved by felix", "title_sort outcome": "left: shadow: rejected", "author_sort outcome": "left: shadow: approved",
	})
	wantState(t, w.state(run, 2), map[string]string{
		"title_sort outcome": "left: shadow: approved", "author_sort outcome": "left: shadow: approved",
	})
}

func TestReviewUsesShadowVerdicts(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	run := w.run(book(1, "One"))
	if _, err := w.reviewer(panel(nil)).Review(ctx, w.review(run, store.ReviewAuto, store.ModeShadow)); err != nil {
		t.Fatal(err)
	}
	seats := panel(nil)
	got, err := w.reviewer(seats).Review(ctx, w.review(run, store.ReviewManual, store.ModeShadow))
	if err != nil || got.State != store.ReviewDone {
		t.Fatalf("Review = %+v, %v", got, err)
	}
	if n := calls(seats); n != 0 {
		t.Fatalf("%d seat calls, want the stored verdicts used", n)
	}
	wantState(t, w.state(run, 1), map[string]string{
		"title_sort outcome": "left: shadow: approved", "author_sort outcome": "left: shadow: approved",
	})
}

// A seat out of quota leaves the book like any failed seat, and the verdict
// records quota_exhausted.
func TestReviewSeatQuotaExhausted(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	run := w.run(book(1, "One"))
	seats := panel(nil)
	seats[1].err = "quota_exhausted"
	if _, err := w.reviewer(seats).Review(ctx, w.review(run, store.ReviewAuto, store.ModeShadow)); err != nil {
		t.Fatal(err)
	}
	wantState(t, w.state(run, 1), map[string]string{
		"title_sort outcome": "left: deepseek failed: quota_exhausted", "author_sort outcome": "left: deepseek failed: quota_exhausted",
	})
}

func TestReviewSeatFailure(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	run := w.run(book(1, "One"))
	seats := panel(nil)
	seats[2].err = "timeout"
	if _, err := w.reviewer(seats).Review(ctx, w.review(run, store.ReviewAuto, store.ModeShadow)); err != nil {
		t.Fatal(err)
	}
	wantState(t, w.state(run, 1), map[string]string{
		"title_sort outcome": "left: gemini failed: timeout", "author_sort outcome": "left: gemini failed: timeout",
	})
	// A later review asks only the seat that failed.
	retry := panel(nil)
	if _, err := w.reviewer(retry).Review(ctx, w.review(run, store.ReviewManual, store.ModeShadow)); err != nil {
		t.Fatal(err)
	}
	if retry[0].calls.Load() != 0 || retry[1].calls.Load() != 0 || retry[2].calls.Load() != 1 {
		t.Fatalf("calls = %d %d %d", retry[0].calls.Load(), retry[1].calls.Load(), retry[2].calls.Load())
	}
	wantState(t, w.state(run, 1), map[string]string{
		"title_sort outcome": "left: shadow: approved", "author_sort outcome": "left: shadow: approved",
	})
}

// TestReviewMissingSeatDecidesNothing: a review run with a seat of the panel
// unconfigured (a key removed before a restart) leaves every field.
func TestReviewMissingSeatDecidesNothing(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	run := w.run(book(1, "One"))
	seats := panel(nil)[:2]
	if _, err := w.reviewer(seats, func(o *Options) { o.Panel = []string{"haiku", "deepseek", "gemini"} }).Review(ctx, w.review(run, store.ReviewAuto, store.ModeShadow)); err != nil {
		t.Fatal(err)
	}
	wantState(t, w.state(run, 1), map[string]string{
		"title_sort outcome": "left: gemini didn't answer", "author_sort outcome": "left: gemini didn't answer",
	})
}

// TestReviewResumeDoesntReaskFailedSeat: within one review, a seat that
// failed isn't asked again on resume; a later review asks it.
func TestReviewResumeDoesntReaskFailedSeat(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	run := w.run(book(1, "One"))
	rv := w.review(run, store.ReviewAuto, store.ModeShadow)
	first := panel(nil)
	first[2].err = "timeout"
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	first[2].onCall = func(string) {} // fails; the review then records it
	if _, err := w.reviewer(first).Review(cctx, rv); err != nil {
		t.Fatal(err)
	}
	// Resume the same review (as if it had stayed running).
	again := panel(nil)
	if _, err := w.reviewer(again).Review(ctx, rv); err != nil {
		t.Fatal(err)
	}
	if n := calls(again); n != 0 {
		t.Fatalf("%d calls when resuming the same review", n)
	}
}

func TestReviewModeOffBeforeJudging(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	run := w.run(book(1, "One"), book(2, "Two"))
	rv := w.review(run, store.ReviewAuto, store.ModeShadow)
	if err := w.agents.SetMode(ctx, store.ModeOff, "felix"); err != nil { // switched off while the review waited
		t.Fatal(err)
	}
	seats := panel(nil)
	got, err := w.reviewer(seats).Review(ctx, rv)
	if err != nil || got.State != store.ReviewStopped || got.StopReason != "agent fixing switched off" {
		t.Fatalf("Review = %+v, %v", got, err)
	}
	if n := calls(seats); n != 0 {
		t.Fatalf("%d seat calls after the mode was off, want none", n)
	}
}

// The cap is checked before a book is asked, so only books already in flight
// can cross it: at most Books of them, three seats each.
func TestReviewTokenCapBoundsInFlightBooks(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	run := w.run(book(1, "One"), book(2, "Two"), book(3, "Three"), book(4, "Four"), book(5, "Five"))
	seats := panel(nil)
	got, err := w.reviewer(seats, func(o *Options) { o.MaxTokens = 1; o.Books = 3 }).Review(ctx, w.review(run, store.ReviewAuto, store.ModeShadow))
	if err != nil || got.State != store.ReviewStopped || got.StopReason != "token cap reached" {
		t.Fatalf("Review = %+v, %v", got, err)
	}
	if n := calls(seats); n > 9 {
		t.Fatalf("%d calls, want at most the 3 books in flight, 3 seats each", n)
	}
	for _, id := range []int{4, 5} {
		if s := w.state(run, id); len(s) != 0 {
			t.Fatalf("book %d = %v, want untouched after the cap", id, s)
		}
	}
}

// TestReviewShadowDecidesNothing: a review never creates an approval, a
// rejection or an operation, even when the whole panel agrees.
func TestReviewShadowDecidesNothing(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	run := w.run(book(1, "Approve Both"))
	if _, err := w.reviewer(panel(nil)).Review(ctx, w.review(run, store.ReviewAuto, store.ModeShadow)); err != nil {
		t.Fatal(err)
	}
	rows, err := w.fixes.RunProposals(ctx, run)
	if err != nil || len(rows) != 0 {
		t.Fatalf("RunProposals = %+v, %v; want no decision of any kind", rows, err)
	}
	os, err := w.agents.Outcomes(ctx, run)
	if err != nil || len(os) != 2 {
		t.Fatalf("Outcomes = %+v, %v", os, err)
	}
	for _, o := range os {
		if o.Outcome != store.OutcomeLeft {
			t.Errorf("%s outcome = %q, want left", o.Field, o.Outcome)
		}
	}
}

// badEvidence serves the real sealed packages except for one book.
type badEvidence struct {
	inner  Evidence
	bookID int
	serve  func(raw []byte, seal string) ([]byte, string, error)
}

func (b badEvidence) Evidence(ctx context.Context, runID int64, bookID int) ([]byte, string, error) {
	raw, seal, err := b.inner.Evidence(ctx, runID, bookID)
	if err != nil || bookID != b.bookID {
		return raw, seal, err
	}
	return b.serve(raw, seal)
}

// tamper breaks a package's seal.
func tamper(raw []byte, seal string) ([]byte, string, error) {
	return bytes.Replace(raw, []byte("Two"), []byte("Tw0"), 1), seal, nil
}

// outcomesOf returns a book's newest outcomes by field.
func (w *world) outcomesOf(run int64, bookID int) map[string]store.Outcome {
	w.t.Helper()
	all, err := w.agents.Outcomes(context.Background(), run)
	if err != nil {
		w.t.Fatal(err)
	}
	out := map[string]store.Outcome{}
	for _, o := range all {
		if o.BookID == bookID {
			out[o.Field] = o
		}
	}
	return out
}

// wantLeft checks a book's outcomes are exactly these fields, all left with
// this reason.
func wantLeft(t *testing.T, got map[string]store.Outcome, reason string, fields ...string) {
	t.Helper()
	if len(got) != len(fields) {
		t.Errorf("outcomes = %+v, want fields %v", got, fields)
	}
	for _, f := range fields {
		if o, ok := got[f]; !ok || o.Outcome != store.OutcomeLeft || o.Reason != reason {
			t.Errorf("outcome for %q = %+v, want left: %s", f, o, reason)
		}
	}
}

// wantJudged checks the panel judged books 1 and 3 and left them for a
// person, and that no seat saw book 2.
func wantJudged(t *testing.T, w *world, run int64, seats []*fakeSeat) {
	t.Helper()
	for _, id := range []int{1, 3} {
		wantState(t, w.state(run, id), map[string]string{
			"title_sort outcome": "left: shadow: approved", "author_sort outcome": "left: shadow: approved",
		})
	}
	if n := calls(seats); n != 6 {
		t.Errorf("%d seat calls, want 6 (books 1 and 3 only)", n)
	}
	for _, s := range seats {
		for _, p := range s.packets {
			if _, block, _ := dataBlock(p); strings.Contains(block, `"title":"Two"`) || strings.Contains(block, `"title":"Tw0"`) {
				t.Errorf("%s was sent book 2: %s", s.name, p)
			}
		}
	}
}

// One book whose evidence can't be read or opened is left for a person with
// a reason, and the review goes on with the books behind it.
func TestReviewBadBookDoesntHaltFeed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		serve  func(raw []byte, seal string) ([]byte, string, error)
		reason string
		fields []string
	}{
		{"seal mismatch", tamper, "evidence unavailable: seal_broken", []string{"author_sort", "title_sort"}},
		{"missing package", func([]byte, string) ([]byte, string, error) {
			return nil, "", fmt.Errorf("evidence: %w", verify.ErrNoSuchRun)
		}, "evidence unavailable: missing", []string{""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			w := newWorld(t)
			run := w.run(book(1, "One"), book(2, "Two"), book(3, "Three"))
			seats := panel(nil)
			rv := w.reviewer(seats, func(o *Options) {
				o.Books = 1
				o.Evidence = badEvidence{inner: w.runs, bookID: 2, serve: tc.serve}
			})
			got, err := rv.Review(ctx, w.review(run, store.ReviewAuto, store.ModeShadow))
			if err != nil || got.State != store.ReviewDone {
				t.Fatalf("Review = %+v, %v; want done", got, err)
			}
			wantJudged(t, w, run, seats)
			wantLeft(t, w.outcomesOf(run, 2), tc.reason, tc.fields...)
		})
	}
}

// A packet that can't be built because the data carries the block marker
// leaves that book, and only that book.
func TestReviewMarkerInDataLeavesBook(t *testing.T) {
	ctx := context.Background()
	old := nonceSource
	t.Cleanup(func() { nonceSource = old })
	nonceSource = func() (string, error) { return "Two", nil } // collides with book 2's title
	w := newWorld(t)
	run := w.run(book(1, "One"), book(2, "Two"), book(3, "Three"))
	seats := panel(nil)
	got, err := w.reviewer(seats, func(o *Options) { o.Books = 1 }).Review(ctx, w.review(run, store.ReviewAuto, store.ModeShadow))
	if err != nil || got.State != store.ReviewDone {
		t.Fatalf("Review = %+v, %v; want done", got, err)
	}
	wantJudged(t, w, run, seats)
	wantLeft(t, w.outcomesOf(run, 2), "evidence unavailable: marker", "author_sort", "title_sort")
}

// Fields a person decided, and books a newer check superseded, get no left
// outcome from a book whose evidence is bad.
func TestReviewBadBookRespectsDecidedAndSuperseded(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	bad := func(id int) func(*Options) {
		return func(o *Options) { o.Evidence = badEvidence{inner: w.runs, bookID: id, serve: tamper} }
	}
	old := w.run(book(2, "Two"))
	run := w.run(book(2, "Two")) // supersedes old
	if err := w.svc.Reject(ctx, run, 2, []string{"title_sort"}, "felix", store.Basis{Mode: store.DecisionBulk}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.reviewer(panel(nil), bad(2)).Review(ctx, w.review(run, store.ReviewAuto, store.ModeShadow)); err != nil {
		t.Fatal(err)
	}
	wantLeft(t, w.outcomesOf(run, 2), "evidence unavailable: seal_broken", "author_sort")
	if _, err := w.reviewer(panel(nil), bad(2)).Review(ctx, w.review(old, store.ReviewAuto, store.ModeShadow)); err != nil {
		t.Fatal(err)
	}
	if got := w.outcomesOf(old, 2); len(got) != 0 {
		t.Errorf("superseded run got outcomes: %+v", got)
	}
}

// A cancelled context while a package opens is an interruption, not a bad
// book: nothing is recorded and the error is ctx's.
func TestReviewCancelledDuringOpenRecordsNothing(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	run := w.run(book(1, "One"), book(2, "Two"))
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	rv := w.reviewer(panel(nil), func(o *Options) {
		o.Books = 1
		o.Evidence = badEvidence{inner: w.runs, bookID: 2, serve: func(raw []byte, seal string) ([]byte, string, error) {
			cancel()
			return tamper(raw, seal)
		}}
	})
	if _, err := rv.Review(cctx, w.review(run, store.ReviewAuto, store.ModeShadow)); !errors.Is(err, context.Canceled) {
		t.Fatalf("Review = %v, want context.Canceled", err)
	}
	if got := w.outcomesOf(run, 2); len(got) != 0 {
		t.Errorf("outcomes = %+v, want none", got)
	}
}

// A store failure still ends the review, to resume later.
func TestReviewStoreErrorEndsReview(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	run := w.run(book(1, "One"), book(2, "Two"))
	rv := w.reviewer(panel(nil), func(o *Options) {
		o.Books = 1
		o.Evidence = badEvidence{inner: w.runs, bookID: 2, serve: func([]byte, string) ([]byte, string, error) {
			return nil, "", errors.New("connection reset")
		}}
	})
	if got, err := rv.Review(ctx, w.review(run, store.ReviewAuto, store.ModeShadow)); err == nil || got.State == store.ReviewDone {
		t.Fatalf("Review = %+v, %v; want an error", got, err)
	}
}

// A stored verdict whose proposal hash isn't the proposal being judged now
// is not an answer: the seat is asked again.
func TestReviewIgnoresVerdictsForOtherProposal(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	run := w.run(book(1, "One"))
	rv := w.review(run, store.ReviewAuto, store.ModeShadow)
	for _, seat := range []string{"haiku", "deepseek", "gemini"} {
		for _, field := range []string{"title_sort", "author_sort"} {
			v := store.Verdict{ReviewID: rv.ID, RunID: run, BookID: 1, Field: field, Seat: seat, PromptVersion: PromptVersion,
				ProposalHash: "hash-of-another-proposal", Answer: Approve}
			if err := w.agents.AddVerdict(ctx, v); err != nil {
				t.Fatal(err)
			}
		}
	}
	later := w.review(run, store.ReviewManual, store.ModeShadow)
	seats := panel(map[string]map[string]string{"One": {"title_sort": Reject, "author_sort": Reject}})
	if _, err := w.reviewer(seats).Review(ctx, later); err != nil {
		t.Fatal(err)
	}
	if n := calls(seats); n != 3 {
		t.Fatalf("%d seat calls, want every seat asked again", n)
	}
	wantState(t, w.state(run, 1), map[string]string{
		"title_sort outcome": "left: shadow: rejected", "author_sort outcome": "left: shadow: rejected",
	})
}

// A database that still holds mode "on" (and a review queued in it) reads as
// shadow: a unanimous panel creates no approval, no rejection and no
// operation, and every outcome is left.
func TestReviewLegacyOnDecidesNothing(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	run := w.run(book(1, "Approve Both"), book(2, "Reject Both"))
	if _, err := w.st.Pool.Exec(ctx, `INSERT INTO settings (name, value, changed_by) VALUES ('agent_fixing', '"on"'::jsonb, 'felix')`); err != nil {
		t.Fatal(err)
	}
	rv, ok, err := w.agents.Queue(ctx, run, store.ReviewAuto, store.ModeOn, store.AgentActor)
	if err != nil || !ok {
		t.Fatalf("Queue = %v, %v", ok, err)
	}
	seats := panel(map[string]map[string]string{"Reject Both": {"title_sort": Reject, "author_sort": Reject}})
	got, err := w.reviewer(seats).Review(ctx, rv)
	if err != nil || got.State != store.ReviewDone {
		t.Fatalf("Review = %+v, %v", got, err)
	}
	if rows, err := w.fixes.RunProposals(ctx, run); err != nil || len(rows) != 0 {
		t.Fatalf("RunProposals = %+v, %v; want none", rows, err)
	}
	os, err := w.agents.Outcomes(ctx, run)
	if err != nil || len(os) != 4 {
		t.Fatalf("Outcomes = %+v, %v; want 4", os, err)
	}
	for _, o := range os {
		if o.Outcome != store.OutcomeLeft || !strings.HasPrefix(o.Reason, "shadow: ") {
			t.Errorf("book %d %s outcome = %q: %q", o.BookID, o.Field, o.Outcome, o.Reason)
		}
	}
}
