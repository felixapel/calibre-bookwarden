package report

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

var searchBooks = []struct {
	id      int
	title   string
	authors []string
}{
	{10, "Søren Kierkegaard: Frygt og Bæven", []string{"Ørsted"}},
	{20, "Die Straße", []string{"Ärztin Müller"}},
	{30, "Łódź", []string{"Đorđe Marković"}},
	{40, "Cœur et Æon", []string{"José Saramago"}},
	{50, "Plain", []string{"Ada Reed"}},
	{60, "Line", []string{"Break"}},
}

// SearchQueries is shared by the walk test here and, by value, the store's
// projection test: both paths must give these answers.
var searchQueries = []struct {
	q    string
	want []int
}{
	{"soren", []int{10}},
	{"SØREN", []int{10}},
	{"ørsted", []int{10}},
	{"orsted", []int{10}},
	{"ØRSTED", []int{10}},
	{"baeven", []int{10}},
	{"strasse", []int{20}},
	{"STRASSE", []int{20}},
	{"straße", []int{20}},
	{"arztin", []int{20}},
	{"lodz", []int{30}},
	{"dorde", []int{30}},
	{"marković", []int{30}},
	{"coeur", []int{40}},
	{"aeon", []int{40}},
	{"jose", []int{40}},
	{"  jose   sara ", []int{40}},
	{"line break", nil}, // never across the title and the author
	{"e\nb", nil},
	{"zzz", nil},
}

// A run without a check is walked; the search folds the sealed title and
// authors with Fold, as a check does for the projection's search text.
func TestWalkSearchFoldsLikeTheProjection(t *testing.T) {
	var books []Book
	for _, b := range searchBooks {
		bk := fixBook(b.id, "A", "title")
		bk.Package.Calibre = evidence.Record{Title: b.title, Authors: b.authors}
		books = append(books, bk)
	}
	s := New(&fakeStore{books: books})
	for _, tc := range searchQueries {
		p, err := s.Fixes(context.Background(), 7, FixFilter{Query: tc.q}, nil)
		if err != nil {
			t.Fatal(tc.q, err)
		}
		var got []int
		for _, r := range p.Rows {
			got = append(got, r.BookID)
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("q=%q: books %v, want %v", tc.q, got, tc.want)
		}
	}
}

// Matches and the projection's SearchText are two views of one rule.
func TestMatchesAgreesWithSearchText(t *testing.T) {
	for _, b := range searchBooks {
		bk := Book{Package: &evidence.Package{Calibre: evidence.Record{Title: b.title, Authors: b.authors}}}
		text := SearchText(b.title, b.authors)
		for _, tc := range searchQueries {
			q := normalizeQuery(tc.q)
			if got, want := bk.Matches(q), strings.Contains(text, Fold(q)); got != want {
				t.Errorf("book %d q=%q: Matches %v, SearchText %v", b.id, tc.q, got, want)
			}
		}
	}
	if (Book{}).Matches("x") {
		t.Error("a book without a package matches")
	}
}

// A walked page can't be sorted by decision or paged by offset, and says so.
func TestWalkedPageIsMarked(t *testing.T) {
	s := New(&fakeStore{books: []Book{fixBook(1, "A", "title")}})
	p, err := s.Fixes(context.Background(), 7, FixFilter{Sort: SortField}, nil)
	if err != nil || !p.Walked || len(p.Rows) != 1 || p.Next != nil {
		t.Fatalf("page = %+v, %v", p, err)
	}
}
