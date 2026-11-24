package report

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/decide"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

// fixBook is a checked book whose current decision proposes fields. Its
// sealed package has no proposals, as a legacy package: the report must use
// the recomputed decision.
func fixBook(id int, tier string, fields ...string) Book {
	b := Book{Ref: library.BookRef{ID: id}, State: verify.BookChecked,
		Package: &evidence.Package{Calibre: evidence.Record{Title: fmt.Sprintf("Book %d", id)}}, Decision: decide.Decision{Tier: tier}}
	for _, f := range fields {
		b.Decision.Proposals = append(b.Decision.Proposals, evidence.Proposal{Field: f, Current: []string{}, Proposed: []string{f + "-new"}, Roots: []string{"dnb", "file"}})
	}
	return b
}

type fixKey struct {
	book  int
	field string
}

func fixKeys(p FixPage) []fixKey {
	var out []fixKey
	for _, r := range p.Rows {
		out = append(out, fixKey{r.BookID, r.Proposal.Field})
	}
	return out
}

func TestFixes(t *testing.T) {
	ctx := context.Background()
	st := &fakeStore{books: []Book{fixBook(1, "B", "title", "languages"), fixBook(2, "A", "isbn"), {Ref: library.BookRef{ID: 3}, State: verify.BookFailed}, fixBook(4, "C")}}
	s := New(st)
	for _, tt := range []struct {
		name string
		f    FixFilter
		want []fixKey
	}{
		{"all", FixFilter{}, []fixKey{{1, "title"}, {1, "languages"}, {2, "isbn"}}},
		{"field", FixFilter{Field: "languages"}, []fixKey{{1, "languages"}}},
		{"tier", FixFilter{Tier: "A"}, []fixKey{{2, "isbn"}}},
		{"book", FixFilter{BookID: 2}, []fixKey{{2, "isbn"}}},
		{"nothing", FixFilter{Tier: "C"}, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p, err := s.Fixes(ctx, 7, tt.f, nil)
			if err != nil || fmt.Sprint(fixKeys(p)) != fmt.Sprint(tt.want) || p.Next != nil {
				t.Fatalf("Fixes = %+v, %v; want %v", p, err, tt.want)
			}
		})
	}
	p, _ := s.Fixes(ctx, 7, FixFilter{}, nil)
	if r := p.Rows[0]; r.RunID != 7 || r.Title != "Book 1" || r.Tier != "B" || r.Proposal.Proposed[0] != "title-new" {
		t.Fatalf("row = %+v", r)
	}
	for _, bad := range []struct {
		f      FixFilter
		cursor *Cursor
		want   error
	}{
		{FixFilter{Field: "series"}, nil, ErrInvalidFilter},
		{FixFilter{Tier: "D"}, nil, ErrInvalidFilter},
		{FixFilter{BookID: -1}, nil, ErrInvalidFilter},
		{FixFilter{}, &Cursor{RunID: 8, BookID: 1}, ErrInvalidCursor},
		{FixFilter{}, &Cursor{RunID: 7, BookID: 1, Ordinal: 5}, ErrInvalidCursor},
		{FixFilter{BookID: 2}, &Cursor{RunID: 7, BookID: 1}, ErrInvalidCursor},
	} {
		if _, err := s.Fixes(ctx, 7, bad.f, bad.cursor); !errors.Is(err, bad.want) {
			t.Fatalf("Fixes(%+v, %+v) = %v, want %v", bad.f, bad.cursor, err, bad.want)
		}
	}
	st.corrupt = 4
	if _, err := s.Fixes(ctx, 7, FixFilter{Tier: "A"}, nil); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("a damaged book after the matches: %v", err)
	}
}

func TestFixesPaging(t *testing.T) {
	ctx := context.Background()
	st := &fakeStore{}
	for id := 1; id <= 12; id++ {
		st.books = append(st.books, fixBook(id, "B", "title", "authors", "languages", "isbn", "publisher"))
	}
	s := New(st)
	first, err := s.Fixes(ctx, 7, FixFilter{}, nil)
	if err != nil || len(first.Rows) != 50 || first.Next == nil || *first.Next != (Cursor{RunID: 7, BookID: 10, Ordinal: 4}) {
		t.Fatalf("first page: %d rows, next %+v, %v", len(first.Rows), first.Next, err)
	}
	second, err := s.Fixes(ctx, 7, FixFilter{}, first.Next)
	if err != nil || len(second.Rows) != 10 || second.Next != nil || second.Rows[0].BookID != 11 || second.Rows[0].Proposal.Field != "title" {
		t.Fatalf("second page: %+v, %v", second, err)
	}
}
