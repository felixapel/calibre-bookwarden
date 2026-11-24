package store

import (
	"context"
	"math"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

func TestDecisionBasisRecorded(t *testing.T) {
	ctx := context.Background()
	f, r, _ := fixRepo(t)
	books := refs(1)
	run := checkedRun(t, r, books)
	p := []evidence.Proposal{titleProposal("New")}
	steps := []struct {
		name   string
		status string
		basis  Basis
	}{
		{"individual blind", ProposalApproved, Basis{Mode: DecisionIndividual}},
		{"bulk shown", ProposalRejected, Basis{Mode: DecisionBulk, VerdictsShown: true}},
		{"individual shown", ProposalApproved, Basis{Mode: DecisionIndividual, VerdictsShown: true}},
	}
	for _, st := range steps {
		if err := f.Decide(ctx, run, books[0].ID, p, st.status, "felix", st.basis); err != nil {
			t.Fatal(st.name, err)
		}
		rows, err := f.Proposals(ctx, run, books[0].ID)
		if err != nil || len(rows) != 1 {
			t.Fatalf("%s: %v, %v", st.name, rows, err)
		}
		if rows[0].DecisionMode != st.basis.Mode || rows[0].VerdictsShown != st.basis.VerdictsShown {
			t.Fatalf("%s: recorded %q shown=%v", st.name, rows[0].DecisionMode, rows[0].VerdictsShown)
		}
	}
	if err := f.Decide(ctx, run, books[0].ID, p, ProposalApproved, "felix", Basis{Mode: "bogus"}); err == nil {
		t.Fatal("Decide accepted an unknown mode")
	}
	if err := f.Decide(ctx, run, books[0].ID, p, ProposalApproved, "felix", Basis{Mode: DecisionAgent}); err == nil {
		t.Fatal("Decide accepted the agent mode for a person")
	}
}

func TestDecideOpenRecordsAgentMode(t *testing.T) {
	ctx := context.Background()
	f, r, _ := fixRepo(t)
	books := refs(1)
	run := checkedRun(t, r, books)
	if ok, err := f.DecideOpen(ctx, run, books[0].ID, titleProposal("New"), ProposalApproved, AgentActor); err != nil || !ok {
		t.Fatalf("DecideOpen = %v, %v", ok, err)
	}
	rows, _ := f.Proposals(ctx, run, books[0].ID)
	if len(rows) != 1 || rows[0].DecisionMode != DecisionAgent {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestDecisionLabelsMigration(t *testing.T) {
	ctx := context.Background()
	f, r, s := fixRepo(t)
	books := refs(2)
	run := checkedRun(t, r, books)
	p := []evidence.Proposal{titleProposal("New")}
	if err := f.Decide(ctx, run, books[0].ID, p, ProposalApproved, "felix", Basis{Mode: DecisionIndividual}); err != nil {
		t.Fatal(err)
	}
	if ok, err := f.DecideOpen(ctx, run, books[1].ID, titleProposal("New"), ProposalApproved, AgentActor); err != nil || !ok {
		t.Fatal(ok, err)
	}
	// Put the table back as it was before the migration, then run it again.
	if _, err := s.Pool.Exec(ctx, `ALTER TABLE proposals DROP COLUMN decision_mode, DROP COLUMN verdicts_shown; DROP TABLE verdict_reveals`); err != nil {
		t.Fatal(err)
	}
	raw, err := migrationFiles.ReadFile("migrations/0013_decision_labels.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, string(raw)); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		book  int
		mode  string
		shown bool
	}{{books[0].ID, DecisionBulk, true}, {books[1].ID, DecisionAgent, true}} {
		rows, _ := f.Proposals(ctx, run, c.book)
		if len(rows) != 1 || rows[0].DecisionMode != c.mode || rows[0].VerdictsShown != c.shown {
			t.Fatalf("book %d after migration: %+v", c.book, rows)
		}
	}
}

func TestLowerBound(t *testing.T) {
	tests := []struct {
		n, k int
		want float64
		ok   bool
	}{
		{0, 0, 0, false},
		{60, 0, 0.9513, true},
		{299, 0, 0.9900, true},
		{598, 0, 0.9950, true},
		{949, 1, 0.9950, true},
		{100, 1, 0.9534, true},
		{20, 2, 0.7174, true},
		{10, 3, 0.3934, true},
		{10, 10, 0, true},
	}
	for _, tt := range tests {
		got, ok := LowerBound(tt.n, tt.k)
		if ok != tt.ok || math.Abs(got-tt.want) > 0.00006 {
			t.Errorf("LowerBound(%d, %d) = %.5f, %v; want %.4f, %v", tt.n, tt.k, got, ok, tt.want, tt.ok)
		}
	}
}

func TestPrecision(t *testing.T) {
	ctx := context.Background()
	f, r, s := fixRepo(t)
	books := refs(6)
	run := checkedRun(t, r, books)
	fill := evidence.Proposal{Field: "publisher", Current: []string{}, Proposed: []string{"P"}, Roots: []string{"file", "dnb"}}
	replace := titleProposal("New") // roots dnb+file, non-empty current
	other := evidence.Proposal{Field: "title", Current: []string{"Old"}, Proposed: []string{"X"}, Roots: []string{"file"}}
	ind := Basis{Mode: DecisionIndividual}
	decide := func(b int, p evidence.Proposal, status string, basis Basis) {
		t.Helper()
		if err := f.Decide(ctx, run, books[b].ID, []evidence.Proposal{p}, status, "felix", basis); err != nil {
			t.Fatal(err)
		}
	}
	decide(0, replace, ProposalApproved, ind)
	decide(1, replace, ProposalApproved, ind)
	decide(2, replace, ProposalRejected, ind)
	decide(3, replace, ProposalApproved, Basis{Mode: DecisionBulk})                          // bulk: no label
	decide(4, other, ProposalApproved, Basis{Mode: DecisionIndividual, VerdictsShown: true}) // shown: no label
	decide(5, fill, ProposalApproved, ind)
	// Applied counts as correct: approved, then taken by the writer.
	if _, err := s.Pool.Exec(ctx, `UPDATE proposals SET status = 'applied' WHERE book_id = $1`, books[1].ID); err != nil {
		t.Fatal(err)
	}
	got, err := f.Precision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		field, path, kind string
		n, k              int
	}
	var have []row
	for _, g := range got {
		have = append(have, row{g.Field, g.Path, g.Kind, g.Labels, g.Misses})
	}
	want := []row{
		{"publisher", "dnb+file", "fill", 1, 0},
		{"title", "dnb+file", "replace", 3, 1},
	}
	if len(have) != len(want) {
		t.Fatalf("precision = %+v", have)
	}
	for i := range want {
		if have[i] != want[i] {
			t.Fatalf("row %d = %+v, want %+v", i, have[i], want[i])
		}
	}
}

// TestDecideKeepsBasisOnSameStatus: approving an approved fix again, e.g. by
// "Approve all shown", doesn't change the decision, so its basis stays.
func TestDecideKeepsBasisOnSameStatus(t *testing.T) {
	ctx := context.Background()
	f, r, _ := fixRepo(t)
	books := refs(1)
	run := checkedRun(t, r, books)
	p := []evidence.Proposal{titleProposal("New")}
	if err := f.Decide(ctx, run, books[0].ID, p, ProposalApproved, "felix", Basis{Mode: DecisionIndividual}); err != nil {
		t.Fatal(err)
	}
	if err := f.Decide(ctx, run, books[0].ID, p, ProposalApproved, "felix", Basis{Mode: DecisionBulk, VerdictsShown: true}); err != nil {
		t.Fatal(err)
	}
	rows, _ := f.Proposals(ctx, run, books[0].ID)
	if len(rows) != 1 || rows[0].DecisionMode != DecisionIndividual || rows[0].VerdictsShown {
		t.Fatalf("after re-approval: %+v", rows)
	}
	// Changing the decision does take the new basis.
	if err := f.Decide(ctx, run, books[0].ID, p, ProposalRejected, "felix", Basis{Mode: DecisionBulk, VerdictsShown: true}); err != nil {
		t.Fatal(err)
	}
	rows, _ = f.Proposals(ctx, run, books[0].ID)
	if rows[0].DecisionMode != DecisionBulk || !rows[0].VerdictsShown {
		t.Fatalf("after change: %+v", rows)
	}
}

// TestDecideAfterRecordedReveal: a fix whose verdicts were on screen once is
// never decided blind, whatever the form says.
func TestDecideAfterRecordedReveal(t *testing.T) {
	ctx := context.Background()
	f, r, _ := fixRepo(t)
	books := refs(2)
	run := checkedRun(t, r, books)
	if err := f.RecordReveals(ctx, run, []RevealKey{{BookID: books[0].ID, Field: "title"}, {BookID: books[0].ID, Field: "title"}}); err != nil {
		t.Fatal(err)
	}
	for _, b := range books {
		if err := f.Decide(ctx, run, b.ID, []evidence.Proposal{titleProposal("New")}, ProposalApproved, "felix", Basis{Mode: DecisionIndividual}); err != nil {
			t.Fatal(err)
		}
	}
	for i, wantShown := range []bool{true, false} {
		rows, _ := f.Proposals(ctx, run, books[i].ID)
		if len(rows) != 1 || rows[0].VerdictsShown != wantShown {
			t.Fatalf("book %d: %+v, want shown=%v", books[i].ID, rows, wantShown)
		}
	}
}

// TestPrecisionCountsAFixOnce: the same fix proposed again in a later run is
// one label, judged by its first decision; a later rejection is a miss.
func TestPrecisionCountsAFixOnce(t *testing.T) {
	ctx := context.Background()
	f, r, _ := fixRepo(t)
	books := refs(3)
	run1 := checkedRun(t, r, books)
	run2 := checkedRun(t, r, books)
	ind := Basis{Mode: DecisionIndividual}
	decide := func(run int64, b int, status string, basis Basis) {
		t.Helper()
		if err := f.Decide(ctx, run, books[b].ID, []evidence.Proposal{titleProposal("New")}, status, "felix", basis); err != nil {
			t.Fatal(err)
		}
	}
	decide(run1, 0, ProposalApproved, ind) // twice blind: one label
	decide(run2, 0, ProposalApproved, ind)
	decide(run1, 1, ProposalApproved, Basis{Mode: DecisionBulk}) // first bulk: no label
	decide(run2, 1, ProposalApproved, ind)
	decide(run1, 2, ProposalApproved, ind) // later rejected: a miss
	decide(run2, 2, ProposalRejected, Basis{Mode: DecisionBulk})
	if _, err := f.DecideOpen(ctx, run2, books[1].ID, evidence.Proposal{Field: "publisher", Current: []string{}, Proposed: []string{"P"}, Roots: []string{"dnb", "file"}}, ProposalApproved, AgentActor); err != nil {
		t.Fatal(err)
	}
	got, err := f.Precision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Field != "title" || got[0].Labels != 2 || got[0].Misses != 1 {
		t.Fatalf("precision = %+v, want one title type with 2 labels, 1 miss", got)
	}
}
