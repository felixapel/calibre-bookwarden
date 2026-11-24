package store

import (
	"context"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/library"
)

func TestMuteBound(t *testing.T) {
	tests := []struct {
		name      string
		n, hits   int // fixes counted, correct ones
		upper     float64
		wantMuted bool
	}{
		{"10 with 1 approval", 10, 1, 0.394, true},
		{"10 with 2 approvals", 10, 2, 0.507, false},
		{"0 of 19", 19, 0, 0.146, true},
		{"6 of 46", 46, 6, 0.241, true},
		{"6 of 29", 29, 6, 0.368, true},
		{"9 rejections are too few", 9, 0, 0.2831, false},
		{"all approved", 30, 30, 1, false},
		{"none", 0, 0, 1, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upper, muted := MuteBound(tt.n, tt.n-tt.hits)
			if math.Abs(upper-tt.upper) > 0.0006 || muted != tt.wantMuted {
				t.Errorf("MuteBound(%d, %d) = %.4f, %v; want %.3f, %v", tt.n, tt.n-tt.hits, upper, muted, tt.upper, tt.wantMuted)
			}
		})
	}
}

func TestFixTypeOfSortsRoots(t *testing.T) {
	tests := []struct {
		name string
		p    evidence.Proposal
		want FixType
	}{
		{"replace", evidence.Proposal{Field: "title", Current: []string{"Old"}, Roots: []string{"file", "dnb"}}, FixType{"title", "dnb+file", "replace"}},
		{"fill", evidence.Proposal{Field: "publisher", Current: []string{}, Roots: []string{"text"}}, FixType{"publisher", "text", "fill"}},
		{"fill from nil", evidence.Proposal{Field: "isbn", Roots: []string{"dnb", "file"}}, FixType{"isbn", "dnb+file", "fill"}},
	}
	for _, tt := range tests {
		if got := FixTypeOf(tt.p); got != tt.want {
			t.Errorf("%s: %+v, want %+v", tt.name, got, tt.want)
		}
	}
}

// TestUnmuteHoldsForNineMoreDecisions: an unmute sticks until 10 more fixes
// of the type have been decided after it, then the rule applies again.
func TestUnmuteHoldsForNineMoreDecisions(t *testing.T) {
	typ := FixType{"title", "dnb+file", "replace"}
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	rejected := func(n int, from time.Time) []countedFix {
		var out []countedFix
		for i := range n {
			out = append(out, countedFix{libraryID: "lib", typ: typ, miss: true, decidedAt: from.Add(time.Duration(i) * time.Minute)})
		}
		return out
	}
	unmutes := map[libraryType]Unmute{{"lib", typ}: {By: "admin", At: at}}
	tests := []struct {
		name           string
		fixes          []countedFix
		unmutes        map[libraryType]Unmute
		muted, held    bool
		wantSince, fix int
	}{
		{"no unmute", rejected(10, at.Add(-time.Hour)), nil, true, false, 0, 10},
		{"unmuted, none since", rejected(10, at.Add(-time.Hour)), unmutes, false, true, 0, 10},
		{"9 since", append(rejected(10, at.Add(-time.Hour)), rejected(9, at.Add(time.Minute))...), unmutes, false, true, 9, 19},
		{"10 since", append(rejected(10, at.Add(-time.Hour)), rejected(10, at.Add(time.Minute))...), unmutes, true, false, 10, 20},
		{"a decision at the unmute doesn't count", append(rejected(10, at.Add(-time.Hour)), countedFix{libraryID: "lib", typ: typ, miss: true, decidedAt: at}), unmutes, false, true, 0, 11},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := muteStats(tt.fixes, tt.unmutes)
			if len(got) != 1 || got[0].Muted != tt.muted || got[0].Held != tt.held || got[0].Since != tt.wantSince || got[0].Fixes != tt.fix {
				t.Fatalf("muteStats = %+v; want muted %v held %v since %d fixes %d", got, tt.muted, tt.held, tt.wantSince, tt.fix)
			}
		})
	}
}

// muteEnv stores one run of n books for library "library" and decides their
// fixes.
type muteEnv struct {
	t     *testing.T
	f     *FixRepo
	run   int64
	books []library.BookRef
}

func newMuteEnv(t *testing.T, n int) *muteEnv {
	f, r, _ := fixRepo(t)
	books := refs(n)
	return &muteEnv{t: t, f: f, run: checkedRun(t, r, books), books: books}
}

// fixOf is the fix proposed for book i.
func fixOf(i int) evidence.Proposal {
	return evidence.Proposal{Field: "title", Current: []string{"Old"}, Proposed: []string{fmt.Sprintf("New %d", i)}, Roots: []string{"dnb", "file"}}
}

func (e *muteEnv) decide(i int, status string, basis Basis) {
	e.t.Helper()
	if err := e.f.Decide(context.Background(), e.run, e.books[i].ID, []evidence.Proposal{fixOf(i)}, status, "felix", basis); err != nil {
		e.t.Fatal(err)
	}
}

// stats returns the only type's state.
func (e *muteEnv) stats() TypeMute {
	e.t.Helper()
	got, err := e.f.MuteStats(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	if len(got) != 1 {
		e.t.Fatalf("MuteStats = %+v, want one type", got)
	}
	return got[0]
}

func TestMuteStats(t *testing.T) {
	ctx := context.Background()
	bulk, ind, shown := Basis{Mode: DecisionBulk}, Basis{Mode: DecisionIndividual}, Basis{Mode: DecisionIndividual, VerdictsShown: true}

	t.Run("individual and bulk count, with the verdicts shown or not", func(t *testing.T) {
		e := newMuteEnv(t, 12)
		for i := range 4 {
			e.decide(i, ProposalRejected, ind)
		}
		for i := 4; i < 8; i++ {
			e.decide(i, ProposalRejected, bulk)
		}
		for i := 8; i < 12; i++ {
			e.decide(i, ProposalRejected, shown)
		}
		got := e.stats()
		want := FixType{"title", "dnb+file", "replace"}
		if got.FixType != want || got.LibraryID != "library" || got.Fixes != 12 || got.Misses != 12 || !got.Muted || got.Upper >= 0.5 {
			t.Fatalf("stats = %+v", got)
		}
	})

	t.Run("carried and agent decisions do not count", func(t *testing.T) {
		e := newMuteEnv(t, 4)
		e.decide(0, ProposalRejected, ind)
		for i := 1; i < 3; i++ {
			if ok, err := e.f.DecideOpen(ctx, e.run, e.books[i].ID, fixOf(i), ProposalRejected, AgentActor); err != nil || !ok {
				t.Fatal(ok, err)
			}
		}
		e.decide(3, ProposalApproved, ind)
		if _, err := e.f.pool.Exec(ctx, `UPDATE proposals SET decision_mode = 'carried' WHERE book_id = $1`, e.books[3].ID); err != nil {
			t.Fatal(err)
		}
		got := e.stats()
		if got.Fixes != 1 || got.Misses != 1 {
			t.Fatalf("stats = %+v, want only the one person decision", got)
		}
	})

	t.Run("outcomes", func(t *testing.T) {
		e := newMuteEnv(t, 6)
		for i, status := range []string{ProposalApproved, ProposalApplied, ProposalStale, ProposalBlocked, ProposalFailed} {
			e.decide(i, ProposalApproved, ind)
			if status != ProposalApproved {
				if _, err := e.f.pool.Exec(ctx, `UPDATE proposals SET status = $2 WHERE book_id = $1`, e.books[i].ID, status); err != nil {
					t.Fatal(err)
				}
			}
		}
		e.decide(5, ProposalRejected, ind)
		if got := e.stats(); got.Fixes != 6 || got.Misses != 1 {
			t.Fatalf("stats = %+v, want 6 fixes and 1 miss", got)
		}
	})

	t.Run("an undo of an applied fix is a miss", func(t *testing.T) {
		e := newMuteEnv(t, 3)
		for i := range 3 {
			e.decide(i, ProposalApproved, ind)
		}
		// Book 0: applied and then undone. Book 1: applied, its undo failed.
		// Book 2: applied, no undo.
		for i := range 3 {
			op := completedOp(t, e.f, e.run, e.books[i].ID)
			if _, err := e.f.pool.Exec(ctx, `UPDATE proposals SET operation_id = $2, status = 'applied' WHERE book_id = $1`, e.books[i].ID, op); err != nil {
				t.Fatal(err)
			}
			if i == 2 {
				continue
			}
			undo := applyOp(e.run, e.books[i].ID)
			undo.Kind, undo.RunID, undo.Undoes = OpUndo, nil, &op
			id, err := e.f.StartOperation(ctx, undo, nil)
			if err != nil {
				t.Fatal(err)
			}
			steps := []string{OpWriting, OpVerifying, OpCompleted}
			if i == 1 {
				steps = []string{OpFailed}
			}
			for _, st := range steps {
				if err := e.f.Advance(ctx, id, st, nil, "interrupted"); err != nil {
					t.Fatal(err)
				}
			}
		}
		if got := e.stats(); got.Fixes != 3 || got.Misses != 1 {
			t.Fatalf("stats = %+v, want 3 fixes and 1 miss (only the completed undo)", got)
		}
	})

	// An approval carried into the next run and applied there is the one
	// that counts once it's applied, so undoing it is a miss (ADR-034).
	t.Run("an undo of a fix applied from a carried approval is a miss", func(t *testing.T) {
		e := newCarryEnv(t)
		books := []library.BookRef{{ID: 10, UUID: "u-1"}}
		p := titleProposal("New")
		run1 := e.project("lib", books, []evidence.Proposal{p})
		e.decide(run1, 10, p, ProposalApproved, ind)
		run2 := e.project("lib", books, []evidence.Proposal{p})
		op := completedOp(t, e.f, run2, 10)
		if _, err := e.f.pool.Exec(ctx, `UPDATE proposals SET operation_id = $2, status = 'applied' WHERE run_id = $1 AND book_id = 10`, run2, op); err != nil {
			t.Fatal(err)
		}
		undo := applyOp(run2, 10)
		undo.Kind, undo.RunID, undo.Undoes = OpUndo, nil, &op
		id, err := e.f.StartOperation(ctx, undo, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, st := range []string{OpWriting, OpVerifying, OpCompleted} {
			if err := e.f.Advance(ctx, id, st, nil, "interrupted"); err != nil {
				t.Fatal(err)
			}
		}
		got, err := e.f.MuteStats(ctx)
		if err != nil || len(got) != 1 || got[0].Fixes != 1 || got[0].Misses != 1 {
			t.Fatalf("stats = %+v, %v; want 1 fix and 1 miss", got, err)
		}
	})

	t.Run("the latest person decision of a fix counts once across runs", func(t *testing.T) {
		f, r, _ := fixRepo(t)
		books := refs(2)
		run1 := checkedRun(t, r, books)
		run2 := checkedRun(t, r, books)
		decide := func(run int64, i int, status string) {
			t.Helper()
			if err := f.Decide(ctx, run, books[i].ID, []evidence.Proposal{fixOf(i)}, status, "felix", ind); err != nil {
				t.Fatal(err)
			}
			// Distinct, increasing decision times.
			time.Sleep(5 * time.Millisecond)
		}
		decide(run1, 0, ProposalRejected)
		decide(run1, 1, ProposalApproved)
		decide(run2, 0, ProposalApproved) // the latest decision of fix 0
		decide(run2, 1, ProposalRejected) // the latest decision of fix 1
		got, err := f.MuteStats(ctx)
		if err != nil || len(got) != 1 || got[0].Fixes != 2 || got[0].Misses != 1 {
			t.Fatalf("stats = %+v, %v; want 2 fixes and 1 miss", got, err)
		}
	})

	t.Run("the rule mutes at 10 fixes", func(t *testing.T) {
		e := newMuteEnv(t, 10)
		for i := range 9 {
			e.decide(i, ProposalRejected, ind)
		}
		if got := e.stats(); got.Muted {
			t.Fatalf("9 rejections muted: %+v", got)
		}
		e.decide(9, ProposalRejected, ind)
		if got := e.stats(); !got.Muted || got.Fixes != 10 {
			t.Fatalf("10 rejections: %+v", got)
		}
	})
}

// TestMutedAt: a mute is derived, and so is when it began: the decision that
// made the rule true, counting from the newest unmute.
func TestMutedAt(t *testing.T) {
	typ := FixType{"title", "dnb+file", "replace"}
	t0 := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	at := func(i int) time.Time { return t0.Add(time.Duration(i) * time.Minute) }
	run := func(miss bool, from, n int) []countedFix {
		var out []countedFix
		for i := from; i < from+n; i++ {
			out = append(out, countedFix{libraryID: "lib", typ: typ, miss: miss, decidedAt: at(i)})
		}
		return out
	}
	join := func(parts ...[]countedFix) []countedFix {
		var out []countedFix
		for _, p := range parts {
			out = append(out, p...)
		}
		return out
	}
	// After 10 misses and 10 approvals the rule is false; it turns true again
	// at the first later miss that makes it so.
	remuted := 20
	for n, k := 20, 10; ; n, k = n+1, k+1 {
		if _, muted := MuteBound(n+1, k+1); muted {
			remuted = n // the miss at index n is the (n+1)th decision
			break
		}
		if n > 500 {
			t.Fatal("the rule never mutes again")
		}
	}
	tests := []struct {
		name    string
		fixes   []countedFix
		unmute  *Unmute
		muted   bool
		mutedAt time.Time
	}{
		{"the tenth rejection", run(true, 0, 10), nil, true, at(9)},
		{"later rejections don't move it", run(true, 0, 15), nil, true, at(9)},
		{"nine rejections are not muted", run(true, 0, 9), nil, false, time.Time{}},
		{"input order doesn't matter", join(run(true, 5, 5), run(true, 0, 5)), nil, true, at(9)},
		{"after an unmute, the decision that ends the hold", join(run(true, 0, 10), run(true, 11, 10)), &Unmute{By: "admin", At: at(10)}, true, at(20)},
		{"held, not muted", join(run(true, 0, 10), run(true, 11, 9)), &Unmute{By: "admin", At: at(10)}, false, time.Time{}},
		{"the streak starts again after the rule turned false", join(run(true, 0, 10), run(false, 10, 10), run(true, 20, 100)), nil, true, at(remuted)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			unmutes := map[libraryType]Unmute{}
			if tt.unmute != nil {
				unmutes[libraryType{"lib", typ}] = *tt.unmute
			}
			got := muteStats(tt.fixes, unmutes)
			if len(got) != 1 || got[0].Muted != tt.muted || !got[0].MutedAt.Equal(tt.mutedAt) || (tt.muted && got[0].MutedBy != "rule") || (!tt.muted && got[0].MutedBy != "") {
				t.Fatalf("muteStats = %+v; want muted %v at %v", got, tt.muted, tt.mutedAt)
			}
		})
	}
}

func TestMuteStatsFromTheLedger(t *testing.T) {
	ctx := context.Background()
	ind := Basis{Mode: DecisionIndividual}

	t.Run("MutedAt is the time of the decision that made the rule true", func(t *testing.T) {
		e := newMuteEnv(t, 11)
		for i := range 10 {
			e.decide(i, ProposalRejected, ind)
		}
		rows, err := e.f.Proposals(ctx, e.run, e.books[9].ID)
		if err != nil || len(rows) != 1 {
			t.Fatal(rows, err)
		}
		e.decide(10, ProposalRejected, ind)
		got := e.stats()
		if !got.Muted || got.MutedBy != "rule" || !got.MutedAt.Equal(rows[0].DecidedAt) {
			t.Fatalf("stats = %+v, want muted by the rule at %v", got, rows[0].DecidedAt)
		}
	})

	t.Run("a newer agent decision in a later run doesn't replace the person's", func(t *testing.T) {
		f, r, _ := fixRepo(t)
		books := refs(1)
		run1 := checkedRun(t, r, books)
		run2 := checkedRun(t, r, books)
		if err := f.Decide(ctx, run1, books[0].ID, []evidence.Proposal{fixOf(0)}, ProposalRejected, "felix", ind); err != nil {
			t.Fatal(err)
		}
		if ok, err := f.DecideOpen(ctx, run2, books[0].ID, fixOf(0), ProposalApproved, AgentActor); err != nil || !ok {
			t.Fatal(ok, err)
		}
		got, err := f.MuteStats(ctx)
		if err != nil || len(got) != 1 || got[0].Fixes != 1 || got[0].Misses != 1 {
			t.Fatalf("stats = %+v, %v; want the person's rejection", got, err)
		}
	})

	// A rejection carried into the next run by the check isn't counted; a
	// person deciding the carried row again is a decision of their own only
	// when it changes the status (Decide keeps the basis of the same status).
	for _, tt := range []struct {
		name       string
		again      string
		wantMisses int
		wantMode   string
	}{
		{"carried row left alone", "", 1, DecisionCarried},
		{"carried row rejected again", ProposalRejected, 1, DecisionCarried},
		{"carried row approved", ProposalApproved, 0, DecisionIndividual},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := newCarryEnv(t)
			books := []library.BookRef{{ID: 10, UUID: "u-1"}}
			p := titleProposal("New")
			run1 := e.project("lib", books, []evidence.Proposal{p})
			e.decide(run1, 10, p, ProposalRejected, blind)
			run2 := e.project("lib", books, []evidence.Proposal{p})
			if row, ok := e.row(run2, 10, "title"); !ok || row.DecisionMode != DecisionCarried {
				t.Fatalf("run 2 row = %+v, want a carried row", row)
			}
			if tt.again != "" {
				e.decide(run2, 10, p, tt.again, blind)
			}
			if row, _ := e.row(run2, 10, "title"); row.DecisionMode != tt.wantMode {
				t.Fatalf("mode = %q, want %q", row.DecisionMode, tt.wantMode)
			}
			got, err := e.f.MuteStats(ctx)
			if err != nil || len(got) != 1 || got[0].Fixes != 1 || got[0].Misses != tt.wantMisses {
				t.Fatalf("stats = %+v, %v; want 1 fix and %d misses", got, err, tt.wantMisses)
			}
		})
	}
}

// TestUnmuteHoldIgnoresOtherLibraries: only fixes of the type in the same
// library count toward the hold.
func TestUnmuteHoldIgnoresOtherLibraries(t *testing.T) {
	ctx := context.Background()
	e := newCarryEnv(t)
	p := titleProposal("New")
	var a, b []library.BookRef
	for i := 1; i <= 10; i++ {
		a = append(a, library.BookRef{ID: i * 10, UUID: fmt.Sprintf("a-%d", i)})
		b = append(b, library.BookRef{ID: i * 10, UUID: fmt.Sprintf("b-%d", i)})
	}
	runA := e.project("lib-a", a, []evidence.Proposal{p})
	runB := e.project("lib-b", b, []evidence.Proposal{p})
	for _, bk := range a {
		e.decide(runA, bk.ID, p, ProposalRejected, blind)
	}
	if err := e.f.Unmute(ctx, "lib-a", FixTypeOf(p), "admin"); err != nil {
		t.Fatal(err)
	}
	for _, bk := range b {
		e.decide(runB, bk.ID, p, ProposalRejected, blind)
	}
	got, err := e.f.MuteStats(ctx)
	if err != nil || len(got) != 2 {
		t.Fatalf("stats = %+v, %v", got, err)
	}
	if a := got[0]; a.LibraryID != "lib-a" || a.Since != 0 || !a.Held || a.Muted {
		t.Errorf("library a = %+v, want held with nothing decided since the unmute", a)
	}
	if b := got[1]; b.LibraryID != "lib-b" || b.Unmute != nil || !b.Muted {
		t.Errorf("library b = %+v, want muted with no unmute", b)
	}
}

// TestUnmuteHoldCountsDecidingAgain pins a known behaviour: Decide stamps a
// new decision time when a fix is decided the same way again (ADR-034 keeps
// its basis, not its time), and only the latest decision is kept, so
// re-deciding the same fixes after an unmute counts toward the hold.
func TestUnmuteHoldCountsDecidingAgain(t *testing.T) {
	ctx := context.Background()
	e := newMuteEnv(t, 10)
	for i := range 10 {
		e.decide(i, ProposalRejected, Basis{Mode: DecisionIndividual})
	}
	if err := e.f.Unmute(ctx, "library", FixType{"title", "dnb+file", "replace"}, "admin"); err != nil {
		t.Fatal(err)
	}
	if got := e.stats(); got.Since != 0 || got.Muted {
		t.Fatalf("after the unmute: %+v", got)
	}
	for i := range 10 {
		e.decide(i, ProposalRejected, Basis{Mode: DecisionBulk}) // "Reject all shown" again
	}
	if got := e.stats(); got.Fixes != 10 || got.Since != 10 || !got.Muted {
		t.Fatalf("after deciding the same 10 fixes again: %+v, want them to end the hold", got)
	}
}

// TestUnmuteIsAtomic: two requests to unmute one type record one unmute.
func TestUnmuteIsAtomic(t *testing.T) {
	ctx := context.Background()
	e := newMuteEnv(t, 10)
	for i := range 10 {
		e.decide(i, ProposalRejected, Basis{Mode: DecisionIndividual})
	}
	errs := make(chan error, 8)
	for range cap(errs) {
		go func() { errs <- e.f.Unmute(ctx, "library", FixType{"title", "dnb+file", "replace"}, "admin") }()
	}
	var done, refused int
	for range cap(errs) {
		switch err := <-errs; {
		case err == nil:
			done++
		case errors.Is(err, ErrNotMuted):
			refused++
		default:
			t.Fatal(err)
		}
	}
	var rows int
	if err := e.f.pool.QueryRow(ctx, `SELECT count(*) FROM fix_type_unmutes`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if done != 1 || refused != 7 || rows != 1 {
		t.Fatalf("%d unmuted, %d refused, %d rows; want 1, 7, 1", done, refused, rows)
	}
}

func TestUnmute(t *testing.T) {
	ctx := context.Background()
	ind := Basis{Mode: DecisionIndividual}
	e := newMuteEnv(t, 30)
	typ := FixType{"title", "dnb+file", "replace"}
	for i := range 10 {
		e.decide(i, ProposalRejected, ind)
	}
	if err := e.f.Unmute(ctx, "library", FixType{"title", "nothing", "fill"}, "admin"); !errors.Is(err, ErrNotMuted) {
		t.Fatalf("unmute of an unknown type = %v", err)
	}
	if err := e.f.Unmute(ctx, "other", typ, "admin"); !errors.Is(err, ErrNotMuted) {
		t.Fatalf("unmute in another library = %v", err)
	}
	if err := e.f.Unmute(ctx, "library", typ, "admin"); err != nil {
		t.Fatal(err)
	}
	got := e.stats()
	if got.Muted || !got.Held || got.Unmute == nil || got.Unmute.By != "admin" || got.Unmute.At.IsZero() || got.Since != 0 {
		t.Fatalf("after the unmute: %+v", got)
	}
	// An unmuted type isn't muted, so it can't be unmuted again.
	if err := e.f.Unmute(ctx, "library", typ, "admin"); !errors.Is(err, ErrNotMuted) {
		t.Fatalf("second unmute = %v", err)
	}
	for i := 10; i < 19; i++ {
		e.decide(i, ProposalRejected, ind)
	}
	if got := e.stats(); got.Muted || got.Since != 9 {
		t.Fatalf("after 9 more: %+v", got)
	}
	e.decide(19, ProposalRejected, ind)
	if got := e.stats(); !got.Muted || got.Since != 10 || got.Unmute == nil {
		t.Fatalf("after 10 more: %+v", got)
	}

	// The ledger is append-only.
	if _, err := e.f.pool.Exec(ctx, `UPDATE fix_type_unmutes SET unmuted_by = 'x'`); err == nil {
		t.Fatal("an unmute was changed")
	}
	if _, err := e.f.pool.Exec(ctx, `DELETE FROM fix_type_unmutes`); err == nil {
		t.Fatal("an unmute was deleted")
	}
}
