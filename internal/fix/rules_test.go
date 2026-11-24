package fix

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/library"
)

func ch(field, value string) library.Change {
	return library.Change{Field: field, Value: json.RawMessage(value)}
}

func ptr(s string) *string { return &s }

func TestProposalChange(t *testing.T) {
	cur := evidence.Record{Identifiers: map[string]string{"isbn": "9780000000019", "amazon": "B0X"}}
	for _, tc := range []struct {
		p    evidence.Proposal
		want library.Change
	}{
		{evidence.Proposal{Field: "title", Proposed: []string{"Lighthouse Signals"}}, ch("title", `"Lighthouse Signals"`)},
		{evidence.Proposal{Field: "authors", Proposed: []string{"Bea Sample", "Ann & Co"}}, ch("authors", `["Bea Sample","Ann & Co"]`)},
		{evidence.Proposal{Field: "languages", Proposed: []string{"eng"}}, ch("languages", `["eng"]`)},
		{evidence.Proposal{Field: "publisher", Proposed: []string{"Harbour Press"}}, ch("publisher", `"Harbour Press"`)},
		{evidence.Proposal{Field: "pubdate", Proposed: []string{"2001-07-26"}}, ch("pubdate", `"2001-07-26"`)},
		{evidence.Proposal{Field: "isbn", Proposed: []string{"9780000000026"}}, ch("identifiers", `{"amazon":"B0X","isbn":"9780000000026"}`)},
		{evidence.Proposal{Field: "title_sort", Proposed: []string{"Captive Mind, The"}}, ch("sort", `"Captive Mind, The"`)},
		{evidence.Proposal{Field: "author_sort", Proposed: []string{"McCarthy, Cormac"}}, ch("author_sort", `"McCarthy, Cormac"`)},
	} {
		got, err := proposalChange(tc.p, cur)
		if err != nil || got.Field != tc.want.Field || string(got.Value) != string(tc.want.Value) {
			t.Errorf("proposalChange(%s) = %s %s, %v; want %s %s", tc.p.Field, got.Field, got.Value, err, tc.want.Field, tc.want.Value)
		}
	}
	if cur.Identifiers["isbn"] != "9780000000019" {
		t.Error("proposalChange changed the current identifiers")
	}
	for _, p := range []evidence.Proposal{
		{Field: "series", Proposed: []string{"x"}},
		{Field: "title", Proposed: []string{"a", "b"}},
		{Field: "isbn", Proposed: nil},
	} {
		if _, err := proposalChange(p, cur); err == nil {
			t.Errorf("proposalChange(%+v) succeeded", p)
		}
	}
}

func TestRestoreChanges(t *testing.T) {
	before := evidence.Record{
		Title: "Incorrect Catalogue Title", TitleSort: ptr("Incorrect Catalogue Title"),
		Authors: []string{"Wrong Author"}, AuthorSort: "Author, Wrong", Languages: []string{"fra"},
		Identifiers: map[string]string{"isbn": "9780000000019"}, Publisher: "",
	}
	for _, tc := range []struct {
		name    string
		changes []library.Change
		before  evidence.Record
		want    []library.Change
	}{
		{"title brings its sort", []library.Change{ch("title", `"X"`)}, before,
			[]library.Change{ch("title", `"Incorrect Catalogue Title"`), ch("sort", `"Incorrect Catalogue Title"`)}},
		{"authors bring their sort", []library.Change{ch("authors", `["X"]`), ch("publisher", `"P"`)}, before,
			[]library.Change{ch("authors", `["Wrong Author"]`), ch("author_sort", `"Author, Wrong"`), ch("publisher", `""`)}},
		{"an undo's pair once, title first", []library.Change{ch("sort", `"S"`), ch("title", `"T"`)}, before,
			[]library.Change{ch("title", `"Incorrect Catalogue Title"`), ch("sort", `"Incorrect Catalogue Title"`)}},
		{"unset pubdate is null", []library.Change{ch("pubdate", `"2001-07-26"`)}, before,
			[]library.Change{ch("pubdate", `null`)}},
		{"set pubdate is the timestamp", []library.Change{ch("pubdate", `"2001-07-26"`)}, evidence.Record{PubDate: "2000-07-25T22:00:00+00:00"},
			[]library.Change{ch("pubdate", `"2000-07-25T22:00:00+00:00"`)}},
		{"languages and identifiers", []library.Change{ch("languages", `["eng"]`), ch("identifiers", `{"isbn":"1"}`)}, before,
			[]library.Change{ch("languages", `["fra"]`), ch("identifiers", `{"isbn":"9780000000019"}`)}},
		{"empty lists stay lists", []library.Change{ch("languages", `["eng"]`), ch("identifiers", `{"isbn":"1"}`)}, evidence.Record{},
			[]library.Change{ch("languages", `[]`), ch("identifiers", `{}`)}},
		{"a sort alone goes back alone", []library.Change{ch("sort", `"S"`)}, before,
			[]library.Change{ch("sort", `"Incorrect Catalogue Title"`)}},
		{"an author sort alone goes back alone", []library.Change{ch("author_sort", `"S"`)}, before,
			[]library.Change{ch("author_sort", `"Author, Wrong"`)}},
		{"a nil title sort is left out", []library.Change{ch("title", `"X"`)}, evidence.Record{Title: "Old"},
			[]library.Change{ch("title", `"Old"`)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := restoreChanges(tc.changes, tc.before)
			if len(got) != len(tc.want) {
				t.Fatalf("restoreChanges = %s, want %s", show(got), show(tc.want))
			}
			for i := range got {
				if got[i].Field != tc.want[i].Field || string(got[i].Value) != string(tc.want[i].Value) {
					t.Fatalf("restoreChanges = %s, want %s", show(got), show(tc.want))
				}
			}
		})
	}
}

func show(cs []library.Change) string {
	var s string
	for _, c := range cs {
		s += c.Field + "=" + string(c.Value) + " "
	}
	return s
}

func TestStorable(t *testing.T) {
	for _, tc := range []struct {
		c    library.Change
		want bool
	}{
		{ch("pubdate", `"2001-07-02"`), true},
		{ch("pubdate", `"2001-07-30"`), true},
		{ch("pubdate", `"2001-12-31"`), true},
		{ch("pubdate", `"2001-07-25T22:00:00+00:00"`), true},
		{ch("pubdate", `null`), true},
		{ch("pubdate", `"2001-07-01"`), false},
		{ch("pubdate", `"2001-07-31"`), false},
		{ch("pubdate", `"2001-01-31"`), false},
		{ch("pubdate", `"2000-02-29"`), false},
		{ch("pubdate", `"2001-02-28"`), false},
		{ch("pubdate", `"2001-07-31T22:00:00+00:00"`), false},
		{ch("pubdate", `"2001-08-01T00:30:00+02:00"`), false}, // 2001-07-31 in UTC
		{ch("pubdate", `"0999-07-26"`), false},
		{ch("pubdate", `"2001"`), false},
		{ch("title", `"A B"`), true},
		{ch("title", `" A"`), false},
		{ch("title", `"A  B"`), false},
		{ch("title", `""`), false},
		{ch("title", `"A\u001fB"`), false},
		{ch("sort", `"B, A"`), true},
		{ch("sort", `""`), false},
		{ch("publisher", `""`), true},
		{ch("publisher", `"Harbour Press"`), true},
		{ch("author_sort", `""`), true},
		{ch("authors", `["Ann","Bo"]`), true},
		{ch("authors", `["Rowling, J. K."]`), true},
		{ch("authors", `["A & B"]`), false},
		{ch("authors", `["A|B"]`), false},
		{ch("authors", `["Ann","ann"]`), false},
		{ch("authors", `[]`), false},
		{ch("authors", `[""]`), false},
		{ch("languages", `["eng","deu"]`), true},
		{ch("languages", `[]`), true},
		{ch("languages", `["und"]`), false},
		{ch("languages", `["eng","eng"]`), false},
		{ch("languages", `["EN"]`), false},
		{ch("identifiers", `{"isbn":"9780000000019","amazon":"B0X"}`), true},
		{ch("identifiers", `{}`), true},
		{ch("identifiers", `{"ISBN":"1"}`), false},
		{ch("identifiers", `{"isbn":"1,2"}`), false},
		{ch("identifiers", `{"isbn":" 1"}`), false},
		{ch("identifiers", `{"is:bn":"1"}`), false},
		{ch("series", `"x"`), false},
		{ch("title", `["not a string"]`), false},
	} {
		if got := storable(tc.c); got != tc.want {
			t.Errorf("storable(%s %s) = %v, want %v", tc.c.Field, tc.c.Value, got, tc.want)
		}
	}
}

func TestCaseCollision(t *testing.T) {
	for _, tc := range []struct {
		names, existing []string
		want            bool
	}{
		{[]string{"ada example"}, []string{"Ada Example"}, true},
		{[]string{"Ada Example"}, []string{"Ada Example"}, false},
		{[]string{"ΟΔΟΣ"}, []string{"οδος"}, true},
		{[]string{"Ann"}, nil, false},
		{[]string{"Bea Sample", "ann"}, []string{"Ada Example", "Ann", "Bea Sample"}, true},
	} {
		if got := caseCollision(tc.names, tc.existing); got != tc.want {
			t.Errorf("caseCollision(%q, %q) = %v, want %v", tc.names, tc.existing, got, tc.want)
		}
	}
}

func TestSame(t *testing.T) {
	b := library.Book{
		Title: "T", TitleSort: ptr("S"), Authors: []string{"A"}, AuthorSort: "A", Languages: []string{"eng"},
		Identifiers: map[string]string{"isbn": "1"}, Publisher: "P", PubDate: "2001-07-26T00:00:00+00:00",
	}
	for _, tc := range []struct {
		name string
		c    library.Change
		b    library.Book
		want bool
	}{
		{"title", ch("title", `"T"`), b, true},
		{"title differs", ch("title", `"T "`), b, false},
		{"sort", ch("sort", `"S"`), b, true},
		{"nil sort", ch("sort", `"S"`), library.Book{}, false},
		{"authors", ch("authors", `["A"]`), b, true},
		{"author_sort", ch("author_sort", `"A"`), b, true},
		{"languages", ch("languages", `["eng"]`), b, true},
		{"languages order", ch("languages", `["deu","eng"]`), library.Book{Languages: []string{"eng", "deu"}}, false},
		{"empty languages", ch("languages", `[]`), library.Book{}, true},
		{"identifiers", ch("identifiers", `{"isbn":"1"}`), b, true},
		{"identifiers extra key", ch("identifiers", `{"isbn":"1"}`), library.Book{Identifiers: map[string]string{"isbn": "1", "x": "2"}}, false},
		{"empty identifiers", ch("identifiers", `{}`), library.Book{}, true},
		{"publisher", ch("publisher", `"P"`), b, true},
		{"empty publisher", ch("publisher", `""`), library.Book{}, true},
		{"date-only pubdate", ch("pubdate", `"2001-07-26"`), b, true},
		{"date-only pubdate, other day", ch("pubdate", `"2001-07-26"`), library.Book{PubDate: "2001-07-25T22:00:00+00:00"}, false},
		{"timestamp, same instant", ch("pubdate", `"2001-07-26T02:00:00+02:00"`), b, true},
		{"timestamp, other instant", ch("pubdate", `"2001-07-26T00:00:01+00:00"`), b, false},
		{"null pubdate", ch("pubdate", `null`), library.Book{}, true},
		{"null pubdate, set", ch("pubdate", `null`), b, false},
		{"unknown field", ch("series", `"x"`), b, false},
	} {
		if got := same(tc.c, tc.b); got != tc.want {
			t.Errorf("%s: same = %v, want %v", tc.name, got, tc.want)
		}
	}
	if !reflect.DeepEqual(b.Identifiers, map[string]string{"isbn": "1"}) {
		t.Error("same changed the book")
	}
}

func TestUnstorableCannotRestore(t *testing.T) {
	if got := unstorable(ch("sort", `"S"`), evidence.Record{Title: "T"}); got != "calibre_cannot_restore" {
		t.Fatalf("unstorable = %q", got)
	}
}

func TestRestoreChangesMissing(t *testing.T) {
	for _, tc := range []struct {
		name    string
		changes []library.Change
		before  evidence.Record
		want    []string
	}{
		{"a written sort without a before sort", []library.Change{ch("sort", `"S"`)}, evidence.Record{Title: "Old"}, []string{"sort"}},
		{"an unknown field", []library.Change{ch("rating", `5`)}, evidence.Record{}, []string{"rating"}},
		{"a title's recomputed sort is not a write", []library.Change{ch("title", `"X"`)}, evidence.Record{Title: "Old"}, nil},
		{"everything expressible", []library.Change{ch("title", `"X"`)}, evidence.Record{Title: "Old", TitleSort: ptr("Old")}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, got := restoreChanges(tc.changes, tc.before); !slices.Equal(got, tc.want) {
				t.Fatalf("missing = %v, want %v", got, tc.want)
			}
		})
	}
}
