package store

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/decide"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/findings"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/report"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

type listBook struct {
	id      int
	title   string
	authors []string
	tier    string
	props   []evidence.Proposal
}

// listRun stores a finished, checked run whose books have these titles,
// authors, tiers and proposals.
func listRun(t *testing.T, e *carryEnv, books []listBook) int64 {
	t.Helper()
	ctx := context.Background()
	refs := make([]library.BookRef, len(books))
	for i, b := range books {
		refs[i] = library.BookRef{ID: b.id, UUID: fmt.Sprintf("uuid-%d", b.id)}
	}
	run, err := e.r.CreateRun(ctx, verify.Scope{}, "lib", "v1", refs)
	if err != nil {
		t.Fatal(err)
	}
	var proj []report.BookProjection
	for i, b := range books {
		pkg := cleanEvidence(refs[i])
		pkg.LibraryID = "lib"
		pkg.Calibre.Title = b.title
		pkg.Calibre.Authors = b.authors
		raw, seal, err := decide.Seal(ctx, pkg)
		if err != nil {
			t.Fatal(err)
		}
		if err := e.r.FinishBook(ctx, run.ID, refs[i], verify.Outcome{State: verify.BookChecked, Evidence: raw, Seal: seal}); err != nil {
			t.Fatal(err)
		}
		proj = append(proj, report.BookProjection{BookID: b.id, Title: b.title, Tier: b.tier, Proposals: b.props, SearchText: report.SearchText(b.title, b.authors)})
	}
	if err := e.r.SetStatus(ctx, run.ID, verify.RunCompleted, ""); err != nil {
		t.Fatal(err)
	}
	c := report.SealCheck{RunID: run.ID, Books: len(proj), FindingsVersion: findings.Version, DecisionVersion: decide.Version}
	if err := e.rr.SaveSealCheck(ctx, c, proj); err != nil {
		t.Fatal(err)
	}
	return run.ID
}

func ids(p report.FixPage) []int {
	var out []int
	for _, r := range p.Rows {
		out = append(out, r.BookID)
	}
	return out
}

func prop(field string) []evidence.Proposal {
	return []evidence.Proposal{{Field: field, Current: []string{"Old"}, Proposed: []string{"New"}, Roots: []string{"dnb", "file"}}}
}

func TestFixListSearch(t *testing.T) {
	e := newCarryEnv(t)
	run := listRun(t, e, []listBook{
		{10, "Café Müller", []string{"Pina Bausch"}, "A", prop("title")},
		{20, "Ensaio sobre a Cegueira", []string{"José Saramago"}, "A", prop("title")},
		{30, "Plain", []string{"Ada Reed"}, "B", prop("title")},
		{40, "100% sure_thing", []string{"X"}, "B", prop("title")},
	})
	svc := report.New(NewReportRepo(e.s))
	tests := []struct {
		q    string
		want []int
	}{
		{"", []int{10, 20, 30, 40}},
		{"cafe", []int{10}},
		{"CAFÉ", []int{10}},
		{"muller", []int{10}},
		{"MÜLLER", []int{10}},
		{"jose", []int{20}},
		{"SARAMAGO", []int{20}},
		{"josé sara", []int{20}},
		{"ada", []int{30}},
		{"bausch", []int{10}},
		{"nothing like it", nil},
		{"%", []int{40}},
		{"_", []int{40}},
	}
	for _, tc := range tests {
		t.Run(tc.q, func(t *testing.T) {
			p, err := svc.Fixes(context.Background(), run, report.FixFilter{Query: tc.q}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got := ids(p); !slices.Equal(got, tc.want) {
				t.Fatalf("q=%q: books %v, want %v", tc.q, got, tc.want)
			}
		})
	}
}

func TestFixListSorts(t *testing.T) {
	ctx := context.Background()
	e := newCarryEnv(t)
	run := listRun(t, e, []listBook{
		{10, "A", nil, "C", prop("title")},
		{20, "B", nil, "A", prop("publisher")},
		{30, "C", nil, "B", prop("authors")},
		{40, "D", nil, "A", prop("isbn")},
	})
	// Decide 30 first and then 10: the newest decision comes first.
	e.decide(run, 30, prop("authors")[0], ProposalApproved, blind)
	e.decide(run, 10, prop("title")[0], ProposalRejected, blind)
	svc := report.New(NewReportRepo(e.s))
	tests := []struct {
		sort string
		want []int
	}{
		{"", []int{10, 20, 30, 40}},
		{"book", []int{10, 20, 30, 40}},
		{"field", []int{30, 40, 20, 10}}, // authors, isbn, publisher, title
		{"tier", []int{20, 40, 30, 10}},  // A (20, 40 by book), B, C
		{"decided", []int{10, 30, 20, 40}},
		{"nonsense", []int{10, 20, 30, 40}},
	}
	for _, tc := range tests {
		t.Run(tc.sort, func(t *testing.T) {
			p, err := svc.Fixes(ctx, run, report.FixFilter{Sort: tc.sort}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got := ids(p); !slices.Equal(got, tc.want) {
				t.Fatalf("sort=%q: books %v, want %v", tc.sort, got, tc.want)
			}
		})
	}
}

// Search, the Decided filter and every sort filter before they page.
func TestFixListFiltersBeforePaging(t *testing.T) {
	ctx := context.Background()
	e := newCarryEnv(t)
	var books []listBook
	for i := 1; i <= 130; i++ {
		title := fmt.Sprintf("Other %d", i)
		if i%2 == 0 {
			title = fmt.Sprintf("Match %d", i) // 65 matches, every other book
		}
		books = append(books, listBook{i, title, nil, "A", prop("title")})
	}
	run := listRun(t, e, books)
	for i := 2; i <= 130; i += 2 {
		if i <= 60 || i > 100 { // 30 + 15 decided by a person, all among the matches
			e.decide(run, i, prop("title")[0], ProposalApproved, blind)
		}
	}
	for i := 1; i <= 130; i += 2 { // the agent decided every odd book
		if ok, err := e.f.DecideOpen(ctx, run, i, prop("title")[0], ProposalRejected, AgentActor); err != nil || !ok {
			t.Fatal(ok, err)
		}
	}
	svc := report.New(NewReportRepo(e.s))
	tests := []struct {
		name string
		f    report.FixFilter
		want []int // the number of rows on each page
	}{
		{"search", report.FixFilter{Query: "match"}, []int{50, 15}},
		{"search sorted by field", report.FixFilter{Query: "match", Sort: "field"}, []int{50, 15}},
		{"search sorted by decided", report.FixFilter{Query: "match", Sort: "decided"}, []int{50, 15}},
		{"decided by a person", report.FixFilter{Decided: report.DecidedPerson}, []int{45}},
		{"decided by the agent", report.FixFilter{Decided: report.DecidedAgent}, []int{50, 15}},
		{"person and search", report.FixFilter{Decided: report.DecidedPerson, Query: "match 1"}, []int{20}}, // 10..18 and 102..130
		{"unfiltered", report.FixFilter{}, []int{50, 50, 30}},
		{"unfiltered by tier", report.FixFilter{Sort: "tier"}, []int{50, 50, 30}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got []int
			var cursor *report.Cursor
			seen := map[int]bool{}
			for range 5 {
				p, err := svc.Fixes(ctx, run, tc.f, cursor)
				if err != nil {
					t.Fatal(err)
				}
				for _, id := range ids(p) {
					if seen[id] {
						t.Fatalf("book %d on two pages", id)
					}
					seen[id] = true
				}
				got = append(got, len(p.Rows))
				if p.Next == nil {
					break
				}
				cursor = p.Next
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("page sizes %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFixListDecidedDisagreeUsesTheGivenKeys(t *testing.T) {
	e := newCarryEnv(t)
	run := listRun(t, e, []listBook{
		{10, "A", nil, "A", prop("title")},
		{20, "B", nil, "A", prop("title")},
		{30, "C", nil, "A", prop("title")},
	})
	e.decide(run, 30, prop("title")[0], ProposalApproved, blind)
	svc := report.New(NewReportRepo(e.s))
	// 30 is decided, so it is not "left"; 10 is the only undecided one named.
	f := report.FixFilter{Decided: report.DecidedDisagree, Keys: []report.FixKey{{BookID: 10, Field: "title"}, {BookID: 30, Field: "title"}}}
	p, err := svc.Fixes(context.Background(), run, f, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(p); !slices.Equal(got, []int{10}) {
		t.Fatalf("books %v, want [10]", got)
	}
	p, err = svc.Fixes(context.Background(), run, report.FixFilter{Decided: report.DecidedDisagree}, nil)
	if err != nil || len(p.Rows) != 0 {
		t.Fatalf("without keys: %v, %v", p.Rows, err)
	}
}
