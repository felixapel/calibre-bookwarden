package fix

import (
	"context"
	"reflect"
	"slices"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

// TestAuthorsReplacementAndUndo: a book whose title is right and whose authors
// are another person's gets its authors replaced and restored. It is not
// another work (ADR-035), and unlike book 2's placeholder authors it is a
// replacement, not a fill.
func TestAuthorsReplacementAndUndo(t *testing.T) {
	ctx := context.Background()
	b := book2()
	b.Title, b.Authors, b.AuthorSort = "Lighthouse Signals", []string{"Wrong Author"}, "Author, Wrong"
	sort := "Lighthouse Signals"
	b.TitleSort = &sort
	e := setup(t, b)
	before := evidence.RecordFrom(e.cal.book(2))
	run := e.checkBook(2)
	e.approve(run, 2, "authors")
	if sum, err := e.svc.Apply(ctx, "felix"); err != nil || sum != (Summary{Applied: 1}) {
		t.Fatalf("Apply = %+v, %v", sum, err)
	}
	if got := e.cal.book(2).Authors; !slices.Equal(got, []string{"Bea Sample"}) {
		t.Fatalf("authors after Apply = %q", got)
	}
	ops := e.operationIDs()
	if _, err := e.svc.Undo(ctx, ops[len(ops)-1], "felix"); err != nil {
		t.Fatal(err)
	}
	if got := evidence.RecordFrom(e.cal.book(2)); !reflect.DeepEqual(fieldsOf(got), fieldsOf(before)) {
		t.Fatalf("book after undo = %+v\nbefore %+v", got, before)
	}
}
