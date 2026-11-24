package store

import (
	"context"
	"reflect"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/decide"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

func agentRepo(t *testing.T) (*AgentRepo, *FixRepo, *RunRepo) {
	t.Helper()
	f, r, s := fixRepo(t)
	return NewAgentRepo(s), f, r
}

func verdict(review, run int64, book int, field, seat, answer string) Verdict {
	v := Verdict{ReviewID: review, RunID: run, BookID: book, Field: field, Seat: seat, PromptVersion: "v1",
		ProposalHash: "p", PacketHash: "k", Model: "m-" + seat, Answer: answer, Reason: "because", InputTokens: 10, OutputTokens: 5}
	if answer == "" {
		v.ErrorCode, v.Reason = "timeout", ""
	}
	return v
}

func TestAgentMode(t *testing.T) {
	ctx := context.Background()
	a, _, _ := agentRepo(t)
	if m, err := a.Mode(ctx); err != nil || m != ModeOff {
		t.Fatalf("Mode = %q, %v; want off", m, err)
	}
	for _, tc := range []struct {
		mode    string
		wantErr bool
	}{
		{ModeShadow, false},
		{ModeOff, false},
		{ModeOn, true},
		{"auto", true},
		{"", true},
	} {
		err := a.SetMode(ctx, tc.mode, "felix")
		if (err != nil) != tc.wantErr {
			t.Fatalf("SetMode(%q) error = %v; want error %v", tc.mode, err, tc.wantErr)
		}
	}
	// The refused modes left the last accepted one.
	if m, err := a.Mode(ctx); err != nil || m != ModeOff {
		t.Fatalf("Mode = %q, %v; want off", m, err)
	}
}

// A database whose latest stored mode is the retired "on" reads as shadow.
func TestAgentModeLegacyOnReadsAsShadow(t *testing.T) {
	ctx := context.Background()
	a, _, _ := agentRepo(t)
	if _, err := a.pool.Exec(ctx, `INSERT INTO settings (name, value, changed_by) VALUES ('agent_fixing', '"on"'::jsonb, 'felix')`); err != nil {
		t.Fatal(err)
	}
	if m, err := a.Mode(ctx); err != nil || m != ModeShadow {
		t.Fatalf("Mode = %q, %v; want shadow", m, err)
	}
}

func TestQueueReview(t *testing.T) {
	ctx := context.Background()
	a, _, r := agentRepo(t)
	run := checkedRun(t, r, refs(1))
	for _, tc := range []struct {
		name, kind string
		done       bool // finish the review queued in this row
		want       bool
	}{
		{"first auto", ReviewAuto, true, true},
		{"second auto", ReviewAuto, false, false},
		{"manual", ReviewManual, false, true},
		{"manual while one is queued", ReviewManual, true, false},
		{"manual after done", ReviewManual, false, true},
	} {
		rv, ok, err := a.Queue(ctx, run, tc.kind, ModeShadow, "felix")
		if err != nil || ok != tc.want {
			t.Fatalf("%s: Queue = %v, %v; want %v", tc.name, ok, err, tc.want)
		}
		if ok && (rv.RunID != run || rv.Kind != tc.kind || rv.State != ReviewQueued || rv.Mode != ModeShadow) {
			t.Fatalf("%s: review = %+v", tc.name, rv)
		}
		if tc.done {
			open, _ := a.Reviews(ctx, run)
			for _, o := range open {
				if o.State == ReviewQueued && o.Kind == tc.kind {
					if err := a.Finish(ctx, o.ID, ReviewDone, ""); err != nil {
						t.Fatal(err)
					}
				}
			}
		}
	}
}

func TestNextReview(t *testing.T) {
	ctx := context.Background()
	a, _, r := agentRepo(t)
	run := checkedRun(t, r, refs(1))
	if _, ok, err := a.Next(ctx); err != nil || ok {
		t.Fatalf("Next on empty = %v, %v", ok, err)
	}
	first, _, _ := a.Queue(ctx, run, ReviewAuto, ModeOn, "agent")
	second, _, _ := a.Queue(ctx, run, ReviewManual, ModeOn, "felix")
	if err := a.Start(ctx, second.ID); err != nil {
		t.Fatal(err)
	}
	if rv, ok, err := a.Next(ctx); err != nil || !ok || rv.ID != second.ID || rv.State != ReviewRunning {
		t.Fatalf("Next = %+v, %v, %v; want the running review", rv, ok, err)
	}
	if err := a.Finish(ctx, second.ID, ReviewStopped, "token cap reached"); err != nil {
		t.Fatal(err)
	}
	if rv, ok, err := a.Next(ctx); err != nil || !ok || rv.ID != first.ID {
		t.Fatalf("Next = %+v, %v, %v; want the queued review", rv, ok, err)
	}
	rs, err := a.Reviews(ctx, run)
	if err != nil || len(rs) != 2 || rs[1].StopReason != "token cap reached" || rs[1].FinishedAt == nil {
		t.Fatalf("Reviews = %+v, %v", rs, err)
	}
}

func TestAddVerdictInsertOnce(t *testing.T) {
	ctx := context.Background()
	a, _, r := agentRepo(t)
	books := refs(1)
	run := checkedRun(t, r, books)
	rv, _, _ := a.Queue(ctx, run, ReviewAuto, ModeOn, "agent")
	first := verdict(rv.ID, run, books[0].ID, "title", "haiku", "approve")
	second := first
	second.Answer = "reject"
	for _, v := range []Verdict{first, second} {
		if err := a.AddVerdict(ctx, v); err != nil {
			t.Fatal(err)
		}
	}
	got, err := a.Verdicts(ctx, run, "v1")
	if err != nil || len(got) != 1 || got[0].Answer != "approve" || got[0].Model != "m-haiku" || got[0].InputTokens != 10 {
		t.Fatalf("Verdicts = %+v, %v", got, err)
	}
	if got, _ := a.Verdicts(ctx, run, "v2"); len(got) != 0 {
		t.Fatalf("Verdicts of another prompt version = %+v", got)
	}
}

func TestVerdictAnswerXorError(t *testing.T) {
	ctx := context.Background()
	a, _, r := agentRepo(t)
	books := refs(1)
	run := checkedRun(t, r, books)
	rv, _, _ := a.Queue(ctx, run, ReviewAuto, ModeOn, "agent")
	for _, tc := range []struct {
		name          string
		answer, errCd string
	}{
		{"both", "approve", "timeout"},
		{"neither", "", ""},
		{"unknown answer", "maybe", ""},
	} {
		v := verdict(rv.ID, run, books[0].ID, "title", "haiku", "approve")
		v.Answer, v.ErrorCode = tc.answer, tc.errCd
		if err := a.AddVerdict(ctx, v); err == nil {
			t.Errorf("%s: AddVerdict accepted it", tc.name)
		}
	}
}

func TestTokensPerSeat(t *testing.T) {
	ctx := context.Background()
	a, _, r := agentRepo(t)
	books := refs(1)
	run := checkedRun(t, r, books)
	r1, _, _ := a.Queue(ctx, run, ReviewAuto, ModeShadow, "agent")
	r2, _, _ := a.Queue(ctx, run, ReviewManual, ModeShadow, "felix")
	for _, v := range []Verdict{
		verdict(r1.ID, run, books[0].ID, "title", "haiku", "approve"),
		verdict(r1.ID, run, books[0].ID, "title", "gemini", ""),
		verdict(r2.ID, run, books[0].ID, "title", "haiku", "approve"),
	} {
		if err := a.AddVerdict(ctx, v); err != nil {
			t.Fatal(err)
		}
	}
	got, err := a.Tokens(ctx, run)
	if err != nil || !reflect.DeepEqual(got, map[string]int64{"haiku": 30, "gemini": 15}) {
		t.Fatalf("Tokens = %v, %v", got, err)
	}
}

func TestOutcomesNewest(t *testing.T) {
	ctx := context.Background()
	a, _, r := agentRepo(t)
	books := refs(1)
	run := checkedRun(t, r, books)
	r1, _, _ := a.Queue(ctx, run, ReviewAuto, ModeShadow, "agent")
	r2, _, _ := a.Queue(ctx, run, ReviewManual, ModeOn, "felix")
	for _, o := range []Outcome{
		{ReviewID: r1.ID, RunID: run, BookID: books[0].ID, Field: "title", Outcome: OutcomeLeft, Reason: "shadow"},
		{ReviewID: r1.ID, RunID: run, BookID: books[0].ID, Field: "isbn", Outcome: OutcomeLeft, Reason: "agents disagree"},
		{ReviewID: r2.ID, RunID: run, BookID: books[0].ID, Field: "title", Outcome: OutcomeApproved},
		{ReviewID: r2.ID, RunID: run, BookID: books[0].ID, Field: "title", Outcome: OutcomeRejected}, // a no-op
	} {
		if err := a.AddOutcome(ctx, o); err != nil {
			t.Fatal(err)
		}
	}
	got, err := a.Outcomes(ctx, run)
	if err != nil || len(got) != 2 {
		t.Fatalf("Outcomes = %+v, %v", got, err)
	}
	byField := map[string]Outcome{}
	for _, o := range got {
		byField[o.Field] = o
	}
	if byField["title"].Outcome != OutcomeApproved || byField["title"].ReviewID != r2.ID || byField["isbn"].Reason != "agents disagree" {
		t.Fatalf("Outcomes = %+v", got)
	}
}

func TestProposalBooks(t *testing.T) {
	ctx := context.Background()
	a, _, r := agentRepo(t)
	books := refs(3)
	run, err := r.CreateRun(ctx, verify.Scope{}, "library", "v1", books)
	if err != nil {
		t.Fatal(err)
	}
	for i, b := range books {
		p := cleanEvidence(b)
		sort := "Tides, Binary" // a wrong sort: proposes Calibre's own
		if i == 1 {
			sort = "Binary Tides" // nothing to fix
		}
		p.Calibre.TitleSort = &sort
		raw, seal, err := decide.Seal(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		if err := r.FinishBook(ctx, run.ID, b, verify.Outcome{State: verify.BookChecked, Evidence: raw, Seal: seal}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := a.ProposalBooks(ctx, run.ID)
	if err != nil || !reflect.DeepEqual(got, []int{books[0].ID, books[2].ID}) {
		t.Fatalf("ProposalBooks = %v, %v", got, err)
	}
}

func TestDecideOpenLeavesPersonDecision(t *testing.T) {
	ctx := context.Background()
	f, r, _ := fixRepo(t)
	books := refs(1)
	run := checkedRun(t, r, books)
	p := titleProposal("New")
	if err := f.Decide(ctx, run, books[0].ID, []evidence.Proposal{p}, ProposalRejected, "felix", Basis{Mode: DecisionBulk}); err != nil {
		t.Fatal(err)
	}
	ok, err := f.DecideOpen(ctx, run, books[0].ID, p, ProposalApproved, AgentActor)
	if err != nil || ok {
		t.Fatalf("DecideOpen = %v, %v; want false", ok, err)
	}
	rows, _ := f.Proposals(ctx, run, books[0].ID)
	if len(rows) != 1 || rows[0].Status != ProposalRejected || rows[0].DecidedBy != "felix" {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestDecideOpenInserts(t *testing.T) {
	ctx := context.Background()
	f, r, _ := fixRepo(t)
	books := refs(1)
	run := checkedRun(t, r, books)
	ok, err := f.DecideOpen(ctx, run, books[0].ID, titleProposal("New"), ProposalRejected, AgentActor)
	if err != nil || !ok {
		t.Fatalf("DecideOpen = %v, %v", ok, err)
	}
	rows, _ := f.Proposals(ctx, run, books[0].ID)
	if len(rows) != 1 || rows[0].Status != ProposalRejected || rows[0].DecidedBy != AgentActor {
		t.Fatalf("rows = %+v", rows)
	}
	if _, err := f.DecideOpen(ctx, run, books[0].ID, titleProposal("New"), ProposalApplied, AgentActor); err == nil {
		t.Fatal("DecideOpen accepted a final status")
	}
	// A person can still change the agent's decision before it is applied.
	if err := f.Decide(ctx, run, books[0].ID, []evidence.Proposal{titleProposal("New")}, ProposalApproved, "felix", Basis{Mode: DecisionBulk}); err != nil {
		t.Fatal(err)
	}
}
