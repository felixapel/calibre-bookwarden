package fix

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/store"
)

// noSortOp records an apply of a title sort on book 2 whose before record
// has no title sort, so the write can't be put back. With landed, Calibre
// holds the new sort.
func (e *env) noSortOp(landed bool, steps ...string) store.Operation {
	e.t.Helper()
	ctx := context.Background()
	e.cal.edit(2, func(b *library.Book) { b.TitleSort = nil })
	before := evidence.RecordFrom(e.cal.book(2))
	changes := []library.Change{{Field: fSort, Value: jsonValue("Fresh Sort")}}
	run := e.checkBook(2)
	op := store.Operation{Kind: store.OpApply, RunID: &run, LibraryID: "library", BookID: 2, UUID: "u-2", Changes: changes, Before: before, RequestedBy: "felix"}
	var err error
	if op.ID, err = e.fixes.StartOperation(ctx, op, nil); err != nil {
		e.t.Fatal(err)
	}
	if landed {
		if _, err := e.cal.SetFields(ctx, "library", 2, changes); err != nil {
			e.t.Fatal(err)
		}
	}
	for _, st := range steps {
		var after *evidence.Record
		if st == store.OpCompleted {
			r := evidence.RecordFrom(e.cal.book(2))
			after = &r
		}
		if err := e.fixes.Advance(ctx, op.ID, st, after, ""); err != nil {
			e.t.Fatal(err)
		}
	}
	got, err := e.fixes.Operation(ctx, op.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	return got
}

// TestRestoreReportsWhatItCannotWrite: a field the operation wrote and the
// before record can't express never ends "restored".
func TestRestoreReportsWhatItCannotWrite(t *testing.T) {
	ctx := context.Background()
	e := setup(t)
	op := e.noSortOp(true, store.OpWriting, store.OpVerifying)
	if err := e.svc.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := e.fixes.Operation(ctx, op.ID)
	if err != nil || got.State != store.OpFailed || !strings.Contains(got.Detail, "restore_incomplete: sort") {
		t.Fatalf("operation = %+v, %v", got, err)
	}
}

// TestUndoRefusesWhatItCannotWrite: an undo that couldn't put every changed
// field back refuses before writing anything.
func TestUndoRefusesWhatItCannotWrite(t *testing.T) {
	ctx := context.Background()
	e := setup(t)
	op := e.noSortOp(true, store.OpWriting, store.OpVerifying, store.OpCompleted)
	sets := e.cal.setCount()
	if _, err := e.svc.Undo(ctx, op.ID, "felix"); !errors.Is(err, ErrBlocked) || !strings.Contains(err.Error(), "sort") {
		t.Fatalf("Undo = %v, want ErrBlocked naming sort", err)
	}
	if e.cal.setCount() != sets || len(e.operationIDs()) != 1 {
		t.Fatal("the refused undo wrote or recorded something")
	}
}

// TestMismatchRestoreLeavesLaterEdits: after a readback mismatch, a field
// that holds neither what the operation wrote nor what Calibre answered with
// was edited meanwhile, and the restore leaves it.
func TestMismatchRestoreLeavesLaterEdits(t *testing.T) {
	ctx := context.Background()
	e := setup(t)
	run := e.checkBook(2)
	e.approve(run, 2, "title", "languages", "publisher")
	e.cal.alterOnce = true
	e.cal.alter = func(c library.Change) library.Change {
		if c.Field == fTitle {
			s, _ := decode[string](c.Value)
			c.Value = jsonValue(s + " (x)")
		}
		return c
	}
	// Read 1 is the stale check, read 2 the readback; the edit lands after it.
	e.cal.afterRead = func(n int) {
		if n == 2 {
			e.cal.edit(2, func(b *library.Book) { b.Languages = []string{"deu"} })
		}
	}
	if _, err := e.svc.Apply(ctx, "felix"); !errors.Is(err, ErrPaused) {
		t.Fatalf("Apply = %v", err)
	}
	ops := e.operationIDs()
	op, err := e.fixes.Operation(ctx, ops[0])
	if err != nil || op.State != store.OpFailed || !strings.Contains(op.Detail, "restore_incomplete: languages") {
		t.Fatalf("operation = %+v, %v", op, err)
	}
	got := e.cal.book(2)
	if !slices.Equal(got.Languages, []string{"deu"}) {
		t.Errorf("languages = %v, the later edit was overwritten", got.Languages)
	}
	if got.TitleSort == nil || *got.TitleSort != "Incorrect Catalogue Title" {
		t.Errorf("title sort = %v, want it restored with the title", got.TitleSort)
	}
	if got.Title != "Incorrect Catalogue Title" || got.Publisher != "" {
		t.Errorf("the untouched fields weren't restored: title %q, publisher %q", got.Title, got.Publisher)
	}
}

// TestSummaryCountsOnlyClosedRows: rows decided elsewhere while the apply ran
// are skipped, and the summary doesn't count them as stale or blocked.
func TestSummaryCountsOnlyClosedRows(t *testing.T) {
	for _, tc := range []struct {
		name  string
		says  sourceSays
		stale bool
	}{
		{name: "stale", stale: true},
		{name: "blocked", says: sourceSays{date: "2001-07-01"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			b := book2()
			e := setup(t, b)
			check := e.check
			if !tc.stale {
				check = e.checkV9 // v10 no longer proposes a shifted day
			}
			run := check(pkgFor(t, b, tc.says))
			e.approveStored(run, 2, "pubdate")
			rows, err := e.fixes.Approved(ctx)
			if err != nil || len(rows) != 1 {
				t.Fatalf("Approved = %+v, %v", rows, err)
			}
			if tc.stale {
				e.cal.edit(2, func(b *library.Book) { b.Title = "Edited In Calibre" })
			}
			e.cal.afterRead = func(n int) {
				if n != 1 {
					return
				}
				if err := e.fixes.Close(ctx, ids(rows), store.ProposalStale, "decided_elsewhere"); err != nil {
					t.Error(err)
				}
			}
			sum, err := e.svc.Apply(ctx, "felix")
			if err != nil || sum != (Summary{}) {
				t.Fatalf("Apply = %+v, %v", sum, err)
			}
		})
	}
}

// TestRecoverNamesUnrestorableField: after a crash, a field the restore can't
// write back fails the operation and is named, even where the write never
// landed.
func TestRecoverNamesUnrestorableField(t *testing.T) {
	ctx := context.Background()
	e := setup(t)
	op := e.noSortOp(false, store.OpWriting)
	if err := e.svc.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := e.fixes.Operation(ctx, op.ID)
	if err != nil || got.State != store.OpFailed || !strings.Contains(got.Detail, "restore_incomplete: sort") {
		t.Fatalf("operation = %+v, %v", got, err)
	}
}

// TestWriteErrorAfterLandedAlteredWrite: a write that landed, altered by
// Calibre, before the call failed isn't known to be ours, so it stays and
// fails the operation with an incident.
func TestWriteErrorAfterLandedAlteredWrite(t *testing.T) {
	ctx := context.Background()
	e := setup(t)
	run := e.checkBook(2)
	e.approve(run, 2, "title")
	e.cal.alterOnce = true
	e.cal.alter = func(c library.Change) library.Change {
		c.Value = jsonValue("Altered")
		return c
	}
	e.cal.errAfter = library.ErrForbidden
	if _, err := e.svc.Apply(ctx, "felix"); !errors.Is(err, ErrPaused) {
		t.Fatalf("Apply = %v", err)
	}
	ops := e.operationIDs()
	op, err := e.fixes.Operation(ctx, ops[0])
	if err != nil || op.State != store.OpFailed || !strings.Contains(op.Detail, "restore_incomplete: title") {
		t.Fatalf("operation = %+v, %v", op, err)
	}
	if got := e.cal.book(2).Title; got != "Altered" {
		t.Errorf("title = %q, want the landed value left", got)
	}
	if inc, _ := e.fixes.Incidents(ctx); len(inc) != 1 {
		t.Fatalf("incidents = %+v", inc)
	}
}

func TestHoldsKnown(t *testing.T) {
	sortOf := func(s string) *string { return &s }
	book := func(title string, sort *string) library.Book { return library.Book{Title: title, TitleSort: sort} }
	titleGroup := []library.Change{ch("title", `"Old"`), ch("sort", `"Old"`)}
	wroteTitle := []library.Change{ch("title", `"New"`)}
	authorsGroup := []library.Change{ch("authors", `["Old"]`), ch("author_sort", `"Old, A"`)}
	wroteAuthors := []library.Change{ch("authors", `["New"]`)}
	for _, tc := range []struct {
		name     string
		group    []library.Change
		wrote    []library.Change
		observed []library.Book
		cur      library.Book
		want     bool
	}{
		{"title written, recomputed sort not observed", titleGroup, wroteTitle, nil, book("New", sortOf("sort of New")), true},
		{"title back at before, unknown sort", titleGroup, wroteTitle, nil, book("Old", sortOf("Elsewhere")), false},
		{"title edited, known sort", titleGroup, wroteTitle, []library.Book{book("New", sortOf("sort of New"))}, book("Edited", sortOf("sort of New")), false},
		{"title edited, sort not observed", titleGroup, wroteTitle, nil, book("Edited", sortOf("sort of New")), false},
		{"sort the operation wrote holds an unknown value", titleGroup, []library.Change{ch("title", `"New"`), ch("sort", `"Mine"`)}, nil, book("New", sortOf("sort of New")), false},
		{"observed sort differs from the current one", titleGroup, wroteTitle, []library.Book{book("New", sortOf("sort of New"))}, book("New", sortOf("Edited")), false},
		{"authors written, recomputed sort not observed", authorsGroup, wroteAuthors, nil,
			library.Book{Authors: []string{"New"}, AuthorSort: "sort of New"}, true},
		{"authors back at before, unknown author sort", authorsGroup, wroteAuthors, nil,
			library.Book{Authors: []string{"Old"}, AuthorSort: "Elsewhere"}, false},
		{"authors edited, known author sort", authorsGroup, wroteAuthors, []library.Book{{Authors: []string{"New"}, AuthorSort: "sort of New"}},
			library.Book{Authors: []string{"Edited"}, AuthorSort: "sort of New"}, false},
		{"author sort the operation wrote holds an unknown value", authorsGroup, []library.Change{ch("authors", `["New"]`), ch("author_sort", `"Mine"`)}, nil,
			library.Book{Authors: []string{"New"}, AuthorSort: "sort of New"}, false},
		{"identifiers with an added key", []library.Change{ch("identifiers", `{"isbn":"1"}`)}, []library.Change{ch("identifiers", `{"isbn":"2"}`)}, []library.Book{{Identifiers: map[string]string{"isbn": "2"}}},
			library.Book{Identifiers: map[string]string{"isbn": "2", "mobi": "x"}}, false},
		{"date-only pubdate write against the stored timestamp", []library.Change{ch("pubdate", `null`)}, []library.Change{ch("pubdate", `"2001-07-26"`)}, nil,
			library.Book{PubDate: "2001-07-26T00:00:00+00:00"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := holdsKnown(tc.group, tc.wrote, tc.observed, tc.cur); got != tc.want {
				t.Fatalf("holdsKnown = %v, want %v", got, tc.want)
			}
		})
	}
}
