package store

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/decide"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/findings"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/report"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

type carryEnv struct {
	t  *testing.T
	f  *FixRepo
	r  *RunRepo
	rr *ReportRepo
	s  *Store
}

func newCarryEnv(t *testing.T) *carryEnv {
	f, r, s := fixRepo(t)
	return &carryEnv{t: t, f: f, r: r, rr: NewReportRepo(s), s: s}
}

// project stores a finished run of a library whose books each have these
// proposals, and checks it, which is when rejections carry forward.
func (e *carryEnv) project(lib string, books []library.BookRef, props []evidence.Proposal) int64 {
	e.t.Helper()
	ctx := context.Background()
	run, err := e.r.CreateRun(ctx, verify.Scope{}, lib, "v1", books)
	if err != nil {
		e.t.Fatal(err)
	}
	var proj []report.BookProjection
	for _, b := range books {
		pkg := cleanEvidence(b)
		pkg.LibraryID = lib
		raw, seal, err := decide.Seal(ctx, pkg)
		if err != nil {
			e.t.Fatal(err)
		}
		if err := e.r.FinishBook(ctx, run.ID, b, verify.Outcome{State: verify.BookChecked, Evidence: raw, Seal: seal}); err != nil {
			e.t.Fatal(err)
		}
		proj = append(proj, report.BookProjection{BookID: b.ID, Title: "T", Tier: evidence.TierA, Proposals: props})
	}
	if err := e.r.SetStatus(ctx, run.ID, verify.RunCompleted, ""); err != nil {
		e.t.Fatal(err)
	}
	e.check(run.ID, proj)
	return run.ID
}

func (e *carryEnv) check(run int64, proj []report.BookProjection) {
	e.t.Helper()
	c := report.SealCheck{RunID: run, Books: len(proj), FindingsVersion: findings.Version, DecisionVersion: decide.Version}
	if err := e.rr.SaveSealCheck(context.Background(), c, proj); err != nil {
		e.t.Fatal(err)
	}
}

func (e *carryEnv) decide(run int64, book int, p evidence.Proposal, status string, basis Basis) {
	e.t.Helper()
	if err := e.f.Decide(context.Background(), run, book, []evidence.Proposal{p}, status, "felix", basis); err != nil {
		e.t.Fatal(err)
	}
}

func (e *carryEnv) row(run int64, book int, field string) (ProposalRow, bool) {
	e.t.Helper()
	rows, err := e.f.Proposals(context.Background(), run, book)
	if err != nil {
		e.t.Fatal(err)
	}
	for _, r := range rows {
		if r.Proposal.Field == field {
			return r, true
		}
	}
	return ProposalRow{}, false
}

var blind = Basis{Mode: DecisionIndividual}

func TestRejectionCarriesIntoTheNextRun(t *testing.T) {
	e := newCarryEnv(t)
	books := []library.BookRef{{ID: 10, UUID: "u-1"}}
	p := titleProposal("New")
	run1 := e.project("lib", books, []evidence.Proposal{p})
	e.decide(run1, 10, p, ProposalRejected, blind)
	orig, _ := e.row(run1, 10, "title")

	run2 := e.project("lib", books, []evidence.Proposal{p})
	got, ok := e.row(run2, 10, "title")
	if !ok {
		t.Fatal("the rejection did not carry")
	}
	if got.Status != ProposalRejected || got.DecisionMode != DecisionCarried || !got.VerdictsShown || got.DecidedBy != "felix" {
		t.Fatalf("carried row = %+v", got)
	}
	if !got.DecidedAt.Equal(orig.DecidedAt) {
		t.Fatalf("a carried row is decided at %v, the original at %v", got.DecidedAt, orig.DecidedAt)
	}
	if got.CarriedFrom == nil || *got.CarriedFrom != orig.ID || got.CarriedFromRun != run1 {
		t.Fatalf("carried from %v (run %d), want proposal %d in run %d", got.CarriedFrom, got.CarriedFromRun, orig.ID, run1)
	}

	// Checking the run again changes nothing.
	e.check(run2, []report.BookProjection{{BookID: 10, Title: "T", Tier: evidence.TierA, Proposals: []evidence.Proposal{p}}})
	again, _ := e.row(run2, 10, "title")
	if !reflect.DeepEqual(again, got) {
		t.Fatalf("re-check changed the row: %+v -> %+v", got, again)
	}

	// It carries on into a third run, pointing at the original.
	run3 := e.project("lib", books, []evidence.Proposal{p})
	third, ok := e.row(run3, 10, "title")
	if !ok || third.CarriedFrom == nil || *third.CarriedFrom != orig.ID || third.CarriedFromRun != run1 {
		t.Fatalf("run 3 = %+v, %v", third, ok)
	}
}

func TestRejectionCarriesByUUIDNotBookID(t *testing.T) {
	e := newCarryEnv(t)
	p := titleProposal("New")
	run1 := e.project("lib", []library.BookRef{{ID: 10, UUID: "u-1"}}, []evidence.Proposal{p})
	e.decide(run1, 10, p, ProposalRejected, blind)
	// The same book under another Calibre id carries; another book under the same id does not.
	run2 := e.project("lib", []library.BookRef{{ID: 77, UUID: "u-1"}, {ID: 10, UUID: "u-other"}}, []evidence.Proposal{p})
	if r, ok := e.row(run2, 77, "title"); !ok || r.DecisionMode != DecisionCarried {
		t.Fatalf("same uuid: %+v, %v", r, ok)
	}
	if r, ok := e.row(run2, 10, "title"); ok {
		t.Fatalf("same book id, other uuid, carried: %+v", r)
	}
}

func TestCarryNeedsTheSameFix(t *testing.T) {
	orig := evidence.Proposal{Field: "title", Current: []string{"Old"}, Proposed: []string{"New"}, Roots: []string{"dnb", "file"}}
	tests := []struct {
		name  string
		again evidence.Proposal
		carry bool
	}{
		{"identical", orig, true},
		{"roots in another order are the same path", evidence.Proposal{Field: "title", Current: []string{"Old"}, Proposed: []string{"New"}, Roots: []string{"file", "dnb"}}, true},
		{"another current value", evidence.Proposal{Field: "title", Current: []string{"Older"}, Proposed: []string{"New"}, Roots: []string{"dnb", "file"}}, false},
		{"a current value that differs by case", evidence.Proposal{Field: "title", Current: []string{"old"}, Proposed: []string{"New"}, Roots: []string{"dnb", "file"}}, false},
		{"another proposed value", evidence.Proposal{Field: "title", Current: []string{"Old"}, Proposed: []string{"Newer"}, Roots: []string{"dnb", "file"}}, false},
		{"another path", evidence.Proposal{Field: "title", Current: []string{"Old"}, Proposed: []string{"New"}, Roots: []string{"file", "isbndb"}}, false},
		{"a root fewer", evidence.Proposal{Field: "title", Current: []string{"Old"}, Proposed: []string{"New"}, Roots: []string{"dnb"}}, false},
		{"another field with the same values", evidence.Proposal{Field: "publisher", Current: []string{"Old"}, Proposed: []string{"New"}, Roots: []string{"dnb", "file"}}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newCarryEnv(t)
			books := []library.BookRef{{ID: 10, UUID: "u-1"}}
			run1 := e.project("lib", books, []evidence.Proposal{orig})
			e.decide(run1, 10, orig, ProposalRejected, blind)
			run2 := e.project("lib", books, []evidence.Proposal{tc.again})
			r, ok := e.row(run2, 10, tc.again.Field)
			if ok != tc.carry || (ok && r.DecisionMode != DecisionCarried) {
				t.Fatalf("carried = %v (%+v), want %v", ok, r, tc.carry)
			}
		})
	}
}

// Values are compared as lists, in order.
func TestCarryComparesValueListsInOrder(t *testing.T) {
	e := newCarryEnv(t)
	books := []library.BookRef{{ID: 10, UUID: "u-1"}}
	a := evidence.Proposal{Field: "authors", Current: []string{"A", "B"}, Proposed: []string{"C"}, Roots: []string{"dnb", "file"}}
	b := evidence.Proposal{Field: "authors", Current: []string{"B", "A"}, Proposed: []string{"C"}, Roots: []string{"dnb", "file"}}
	run1 := e.project("lib", books, []evidence.Proposal{a})
	e.decide(run1, 10, a, ProposalRejected, blind)
	run2 := e.project("lib", books, []evidence.Proposal{b})
	if r, ok := e.row(run2, 10, "authors"); ok {
		t.Fatalf("a reordered current list carried: %+v", r)
	}
}

func TestOnlyAPersonsRejectionCarries(t *testing.T) {
	p := titleProposal("New")
	books := []library.BookRef{{ID: 10, UUID: "u-1"}}
	tests := []struct {
		name  string
		setup func(e *carryEnv, run int64)
		carry bool
	}{
		{"individual reject", func(e *carryEnv, run int64) { e.decide(run, 10, p, ProposalRejected, blind) }, true},
		{"bulk reject", func(e *carryEnv, run int64) {
			e.decide(run, 10, p, ProposalRejected, Basis{Mode: DecisionBulk, VerdictsShown: true})
		}, true},
		// Felix, 2026-11-24: an approval carries too, for the same fix.
		{"approval", func(e *carryEnv, run int64) { e.decide(run, 10, p, ProposalApproved, blind) }, true},
		{"agent reject", func(e *carryEnv, run int64) {
			if ok, err := e.f.DecideOpen(context.Background(), run, 10, p, ProposalRejected, AgentActor); err != nil || !ok {
				e.t.Fatal(ok, err)
			}
		}, false},
		{"agent approval", func(e *carryEnv, run int64) {
			if ok, err := e.f.DecideOpen(context.Background(), run, 10, p, ProposalApproved, AgentActor); err != nil || !ok {
				e.t.Fatal(ok, err)
			}
		}, false},
		{"reject changed to approve", func(e *carryEnv, run int64) {
			e.decide(run, 10, p, ProposalRejected, blind)
			e.decide(run, 10, p, ProposalApproved, Basis{Mode: DecisionIndividual, VerdictsShown: true})
		}, true},
		{"approve changed to reject", func(e *carryEnv, run int64) {
			e.decide(run, 10, p, ProposalApproved, blind)
			e.decide(run, 10, p, ProposalRejected, Basis{Mode: DecisionIndividual, VerdictsShown: true})
		}, true},
		{"undecided", func(*carryEnv, int64) {}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newCarryEnv(t)
			run1 := e.project("lib", books, []evidence.Proposal{p})
			tc.setup(e, run1)
			run2 := e.project("lib", books, []evidence.Proposal{p})
			r, ok := e.row(run2, 10, "title")
			if ok != tc.carry {
				t.Fatalf("carried = %v (%+v), want %v", ok, r, tc.carry)
			}
		})
	}
}

// A person's rejection undone in a later run stops carrying: only the final
// decision counts.
func TestCarryFollowsTheLatestPersonDecision(t *testing.T) {
	e := newCarryEnv(t)
	p := titleProposal("New")
	books := []library.BookRef{{ID: 10, UUID: "u-1"}}
	run1 := e.project("lib", books, []evidence.Proposal{p})
	e.decide(run1, 10, p, ProposalRejected, blind)
	run2 := e.project("lib", books, []evidence.Proposal{p})
	// Felix reconsiders the carried rejection: a normal decision.
	e.decide(run2, 10, p, ProposalApproved, Basis{Mode: DecisionIndividual, VerdictsShown: true})
	got, _ := e.row(run2, 10, "title")
	if got.Status != ProposalApproved || got.DecisionMode != DecisionIndividual || !got.VerdictsShown || got.CarriedFrom != nil {
		t.Fatalf("after the change = %+v", got)
	}
	// The approval that replaced it carries instead (Felix, 2026-11-24).
	run3 := e.project("lib", books, []evidence.Proposal{p})
	if r, ok := e.row(run3, 10, "title"); !ok || r.Status != ProposalApproved || r.DecisionMode != DecisionCarried {
		t.Fatalf("run 3 = %+v, %v; want the approval carried", r, ok)
	}
}

// An approval not yet applied survives the next check (Felix, 2026-11-24):
// the same fix in the next run is recorded as approved, and the earlier
// approval is retired as stale ("carried"), so Apply writes the fix once,
// from the newest run, and the waiting count stays one.
func TestApprovalCarriesAndRetiresTheEarlierOne(t *testing.T) {
	e := newCarryEnv(t)
	ctx := context.Background()
	books := []library.BookRef{{ID: 10, UUID: "u-1"}}
	p := titleProposal("New")
	run1 := e.project("lib", books, []evidence.Proposal{p})
	e.decide(run1, 10, p, ProposalApproved, Basis{Mode: DecisionBulk})
	orig, _ := e.row(run1, 10, "title")

	run2 := e.project("lib", books, []evidence.Proposal{p})
	got, ok := e.row(run2, 10, "title")
	if !ok || got.Status != ProposalApproved || got.DecisionMode != DecisionCarried || !got.VerdictsShown || got.DecidedBy != "felix" {
		t.Fatalf("carried row = %+v, %v", got, ok)
	}
	if got.CarriedFrom == nil || *got.CarriedFrom != orig.ID || got.CarriedFromRun != run1 {
		t.Fatalf("carried from %v (run %d), want proposal %d in run %d", got.CarriedFrom, got.CarriedFromRun, orig.ID, run1)
	}
	old, _ := e.row(run1, 10, "title")
	if old.Status != ProposalStale || old.Reason != "carried" {
		t.Fatalf("the earlier approval = %+v, want stale (carried)", old)
	}
	if n, err := e.f.ApprovedCount(ctx); err != nil || n != 1 {
		t.Fatalf("approved, waiting = %d, %v; want 1", n, err)
	}

	// A third run carries it on and retires run 2's copy.
	run3 := e.project("lib", books, []evidence.Proposal{p})
	if r, ok := e.row(run3, 10, "title"); !ok || r.Status != ProposalApproved || r.CarriedFrom == nil || *r.CarriedFrom != orig.ID {
		t.Fatalf("run 3 = %+v, %v", r, ok)
	}
	if r, _ := e.row(run2, 10, "title"); r.Status != ProposalStale {
		t.Fatalf("run 2's copy = %+v, want stale", r)
	}
	if n, _ := e.f.ApprovedCount(ctx); n != 1 {
		t.Fatalf("approved, waiting = %d after run 3, want 1", n)
	}

	// Another fix for the field comes back undecided, and the approval of
	// the old one stays as it was.
	q := titleProposal("Other")
	run4 := e.project("lib", books, []evidence.Proposal{q})
	if r, ok := e.row(run4, 10, "title"); ok {
		t.Fatalf("a different fix carried: %+v", r)
	}
	if r, _ := e.row(run3, 10, "title"); r.Status != ProposalApproved {
		t.Fatalf("run 3's approval = %+v, want it left alone", r)
	}
}

func TestRejectionNeverCarriesAcrossLibraries(t *testing.T) {
	e := newCarryEnv(t)
	p := titleProposal("New")
	books := []library.BookRef{{ID: 10, UUID: "u-1"}}
	run1 := e.project("lib-a", books, []evidence.Proposal{p})
	e.decide(run1, 10, p, ProposalRejected, blind)
	run2 := e.project("lib-b", books, []evidence.Proposal{p})
	if r, ok := e.row(run2, 10, "title"); ok {
		t.Fatalf("carried across libraries: %+v", r)
	}
}

func TestCarriedRejectionIsNotALabelAndPrecisionIsUnchanged(t *testing.T) {
	ctx := context.Background()
	e := newCarryEnv(t)
	p := titleProposal("New")
	books := []library.BookRef{{ID: 10, UUID: "u-1"}, {ID: 20, UUID: "u-2"}}
	run1 := e.project("lib", books, []evidence.Proposal{p})
	e.decide(run1, 10, p, ProposalRejected, blind) // a miss
	e.decide(run1, 20, p, ProposalApproved, blind) // a hit, never carried
	before, err := e.f.Precision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 1 || before[0].Labels != 2 || before[0].Misses != 1 {
		t.Fatalf("before the carry = %+v", before)
	}
	run2 := e.project("lib", books, []evidence.Proposal{p})
	run3 := e.project("lib", books, []evidence.Proposal{p})
	after, err := e.f.Precision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("carries changed precision: %+v -> %+v", before, after)
	}
	for _, run := range []int64{run2, run3} {
		if r, ok := e.row(run, 10, "title"); !ok || r.DecisionMode != DecisionCarried {
			t.Fatalf("run %d: %+v, %v", run, r, ok)
		}
	}
	// Felix undoes the carried rejection in run 2: the original blind label
	// still stands, and a miss is still a miss.
	e.decide(run2, 10, p, ProposalApproved, Basis{Mode: DecisionIndividual, VerdictsShown: true})
	after, _ = e.f.Precision(ctx)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("a later decision changed precision: %+v -> %+v", before, after)
	}
}

func TestDecisionModeCarriedMigration(t *testing.T) {
	ctx := context.Background()
	e := newCarryEnv(t)
	p := titleProposal("New")
	books := []library.BookRef{{ID: 10, UUID: "u-1"}}
	run1 := e.project("lib", books, []evidence.Proposal{p})
	e.decide(run1, 10, p, ProposalRejected, blind)
	run2 := e.project("lib", books, []evidence.Proposal{p})

	// Put the table back as it was before migration 0018, seeded, then run it again.
	var oldName string
	if err := e.s.Pool.QueryRow(ctx, `SELECT conname FROM pg_constraint
		WHERE conrelid = 'proposals'::regclass AND contype = 'c' AND pg_get_constraintdef(oid) LIKE '%decision_mode%'`).Scan(&oldName); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.Pool.Exec(ctx, `DELETE FROM proposals WHERE decision_mode = 'carried';
		ALTER TABLE book_projection DROP COLUMN search_text;
		ALTER TABLE proposals DROP COLUMN carried_from;
		ALTER TABLE proposals DROP CONSTRAINT `+oldName+`;
		ALTER TABLE proposals ADD CONSTRAINT `+oldName+` CHECK (decision_mode IN ('individual', 'bulk', 'agent'))`); err != nil {
		t.Fatal(err)
	}
	raw, err := migrationFiles.ReadFile("migrations/0018_carried_decisions.sql")
	if err != nil {
		t.Fatal(err)
	}
	if bad := unsafeConstraints(string(raw)); len(bad) > 0 {
		t.Fatalf("unsafe constraints: %v", bad)
	}
	if _, err := e.s.Pool.Exec(ctx, string(raw)); err != nil {
		t.Fatal(err)
	}
	var stale int
	if err := e.s.Pool.QueryRow(ctx, `SELECT count(*) FROM seal_checks WHERE checked_at <> '-infinity'`).Scan(&stale); err != nil || stale != 0 {
		t.Fatalf("checks left fresh after the migration: %d, %v", stale, err)
	}
	// The seeded decision survived; the table now takes carried rows.
	if r, ok := e.row(run1, 10, "title"); !ok || r.Status != ProposalRejected || r.DecisionMode != DecisionIndividual {
		t.Fatalf("seeded row = %+v, %v", r, ok)
	}
	rows, err := e.s.Pool.Query(ctx, `SELECT conname || ' ' || convalidated::text || ' ' || pg_get_constraintdef(oid) FROM pg_constraint
		WHERE conrelid = 'proposals'::regclass AND contype IN ('c', 'f') AND (pg_get_constraintdef(oid) LIKE '%decision_mode%' OR pg_get_constraintdef(oid) LIKE '%carried_from%')`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		names = append(names, s)
	}
	rows.Close()
	if len(names) != 2 {
		t.Fatalf("constraints = %v", names)
	}
	for _, n := range names {
		if !strings.Contains(n, " true ") {
			t.Fatalf("constraint left NOT VALID: %s", n)
		}
	}
	// Only a first check carries, so forget run 2's.
	if _, err := e.s.Pool.Exec(ctx, `DELETE FROM seal_checks WHERE run_id = $1`, run2); err != nil {
		t.Fatal(err)
	}
	e.check(run2, []report.BookProjection{{BookID: 10, Title: "T", Tier: evidence.TierA, Proposals: []evidence.Proposal{p}}})
	if r, ok := e.row(run2, 10, "title"); !ok || r.DecisionMode != DecisionCarried {
		t.Fatalf("after the migration: %+v, %v", r, ok)
	}
}

// A carried row copies a proposal whose roots may be in another order than the
// decision it derives from. Precision groups proposals by their raw JSON, so a
// copy that isn't excluded would form a group of its own and add a miss.
func TestCarriedRowNeverAddsAMissWhateverTheRootOrder(t *testing.T) {
	ctx := context.Background()
	e := newCarryEnv(t)
	books := []library.BookRef{{ID: 10, UUID: "u-1"}}
	fileFirst := evidence.Proposal{Field: "title", Current: []string{"Old"}, Proposed: []string{"New"}, Roots: []string{"file", "dnb"}}
	dnbFirst := evidence.Proposal{Field: "title", Current: []string{"Old"}, Proposed: []string{"New"}, Roots: []string{"dnb", "file"}}
	run1 := e.project("lib", books, []evidence.Proposal{fileFirst})
	e.decide(run1, 10, fileFirst, ProposalApproved, blind) // a blind label, correct
	run2 := e.project("lib", books, []evidence.Proposal{dnbFirst})
	e.decide(run2, 10, dnbFirst, ProposalRejected, Basis{Mode: DecisionBulk, VerdictsShown: true})
	before, err := e.f.Precision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 1 || before[0].Labels != 1 {
		t.Fatalf("before = %+v", before)
	}
	run3 := e.project("lib", books, []evidence.Proposal{fileFirst})
	after, err := e.f.Precision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("a carried row changed precision: %+v -> %+v", before, after)
	}
	// Precision held because the row is carried, not because nothing carried.
	if r, ok := e.row(run3, 10, "title"); !ok || r.DecisionMode != DecisionCarried {
		t.Fatalf("run 3 did not carry: %+v, %v", r, ok)
	}
}

// An approval Apply retired as superseded, because the next run had
// already written the book's evidence, was never applied: it carries as an
// approval when that run is checked (ADR-034).
func TestSupersededApprovalCarries(t *testing.T) {
	e := newCarryEnv(t)
	books := []library.BookRef{{ID: 10, UUID: "u-1"}}
	p := titleProposal("New")
	run1 := e.project("lib", books, []evidence.Proposal{p})
	e.decide(run1, 10, p, ProposalApproved, Basis{Mode: DecisionIndividual})
	orig, _ := e.row(run1, 10, "title")
	if err := e.f.Close(context.Background(), []int64{orig.ID}, ProposalStale, "superseded"); err != nil {
		t.Fatal(err)
	}
	run2 := e.project("lib", books, []evidence.Proposal{p})
	if r, ok := e.row(run2, 10, "title"); !ok || r.Status != ProposalApproved || r.DecisionMode != DecisionCarried || r.CarriedFrom == nil || *r.CarriedFrom != orig.ID {
		t.Fatalf("run 2 = %+v, %v; want the superseded approval carried", r, ok)
	}
	// Any other stale reason means the fix no longer held: it doesn't carry.
	q := titleProposal("Other")
	run3 := e.project("lib", books, []evidence.Proposal{q})
	e.decide(run3, 10, q, ProposalApproved, Basis{Mode: DecisionIndividual})
	r3, _ := e.row(run3, 10, "title")
	if err := e.f.Close(context.Background(), []int64{r3.ID}, ProposalStale, "book_changed"); err != nil {
		t.Fatal(err)
	}
	run4 := e.project("lib", books, []evidence.Proposal{q})
	if r, ok := e.row(run4, 10, "title"); ok {
		t.Fatalf("a book_changed approval carried: %+v", r)
	}
}
