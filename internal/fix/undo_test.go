package fix

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/store"
)

// applied checks book 2, applies all five fields and returns the operation.
func (e *env) applied() store.Operation {
	e.t.Helper()
	ctx := context.Background()
	run := e.checkBook(2)
	e.approve(run, 2, allFields...)
	if _, err := e.svc.Apply(ctx, "felix"); err != nil {
		e.t.Fatal(err)
	}
	ops := e.operationIDs()
	op, err := e.fixes.Operation(ctx, ops[len(ops)-1])
	if err != nil || op.State != store.OpCompleted {
		e.t.Fatalf("operation = %+v, %v", op, err)
	}
	return op
}

func TestUndo(t *testing.T) {
	ctx := context.Background()
	e := setup(t)
	orig := e.applied()
	id, err := e.svc.Undo(ctx, orig.ID, "felix")
	if err != nil {
		t.Fatal(err)
	}
	undo, err := e.fixes.Operation(ctx, id)
	if err != nil || undo.Kind != store.OpUndo || undo.Undoes == nil || *undo.Undoes != orig.ID || undo.State != store.OpCompleted {
		t.Fatalf("undo = %+v, %v", undo, err)
	}
	if got := evidence.RecordFrom(e.cal.book(2)); !reflect.DeepEqual(fieldsOf(got), fieldsOf(orig.Before)) {
		t.Fatalf("book after undo = %+v\nbefore %+v", got, orig.Before)
	}
	if _, err := e.svc.Undo(ctx, id, "felix"); err != nil {
		t.Fatalf("undoing the undo: %v", err)
	}
	if got := evidence.RecordFrom(e.cal.book(2)); !reflect.DeepEqual(fieldsOf(got), fieldsOf(*orig.After)) {
		t.Fatalf("book after redo = %+v\nafter %+v", got, *orig.After)
	}
}

func TestUndoRefused(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		prep func(e *env, op store.Operation) int64
		want error
	}{
		{"book edited since", func(e *env, op store.Operation) int64 {
			e.cal.edit(2, func(b *library.Book) { b.Title = "Edited In Calibre" })
			return op.ID
		}, ErrChanged},
		{"unknown operation", func(*env, store.Operation) int64 { return 999 }, ErrNotUndoable},
		{"writes off", func(e *env, op store.Operation) int64 {
			if err := e.fixes.SetWritesEnabled(ctx, false, "felix"); err != nil {
				e.t.Fatal(err)
			}
			return op.ID
		}, ErrWritesOff},
		{"open incident", func(e *env, op store.Operation) int64 {
			other, err := e.fixes.StartOperation(ctx, op, nil)
			if err != nil {
				e.t.Fatal(err)
			}
			if err := e.fixes.Advance(ctx, other, store.OpFailed, nil, "interrupted: nothing was sent"); err != nil {
				e.t.Fatal(err)
			}
			return op.ID
		}, ErrPaused},
		{"case variant of a before author", func(e *env, op store.Operation) int64 {
			e.cal.names[fAuthors] = []string{"unknown"}
			return op.ID
		}, ErrBlocked},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := setup(t)
			op := e.applied()
			id := tc.prep(e, op)
			sets := e.cal.setCount()
			if _, err := e.svc.Undo(ctx, id, "felix"); !errors.Is(err, tc.want) {
				t.Fatalf("Undo = %v, want %v", err, tc.want)
			}
			if e.cal.setCount() != sets {
				t.Fatal("set-fields was called")
			}
		})
	}

	t.Run("a restored operation", func(t *testing.T) {
		e := setup(t)
		run := e.checkBook(2)
		e.approve(run, 2, "title")
		e.cal.alterOnce = true
		e.cal.alter = func(c library.Change) library.Change {
			c.Value = jsonValue("Altered")
			return c
		}
		if _, err := e.svc.Apply(ctx, "felix"); !errors.Is(err, ErrPaused) {
			t.Fatal(err)
		}
		inc, _ := e.fixes.Incidents(ctx)
		if err := e.fixes.Acknowledge(ctx, inc[0].ID, "felix"); err != nil {
			t.Fatal(err)
		}
		if _, err := e.svc.Undo(ctx, inc[0].OperationID, "felix"); !errors.Is(err, ErrNotUndoable) {
			t.Fatalf("Undo of a restored operation = %v", err)
		}
	})
}

func TestUndoTwice(t *testing.T) {
	ctx := context.Background()
	e := setup(t)
	orig := e.applied()
	if _, err := e.svc.Undo(ctx, orig.ID, "felix"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Undo(ctx, orig.ID, "felix"); !errors.Is(err, ErrNotUndoable) {
		t.Fatalf("second undo = %v", err)
	}
}

// crashed records an apply of fields on book 2 and leaves it in state, as a
// crash would. With landed, the write reached Calibre.
func (e *env) crashed(state string, landed bool, fields ...string) store.Operation {
	e.t.Helper()
	ctx := context.Background()
	run := e.checkBook(2)
	e.approve(run, 2, fields...)
	rows, err := e.fixes.Approved(ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	before := evidence.RecordFrom(e.cal.book(2))
	var changes []library.Change
	for _, r := range rows {
		c, err := proposalChange(r.Proposal, before)
		if err != nil {
			e.t.Fatal(err)
		}
		changes = append(changes, c)
	}
	op := store.Operation{Kind: store.OpApply, RunID: &run, LibraryID: "library", BookID: 2, UUID: "u-2", Changes: changes, Before: before, RequestedBy: "felix"}
	if op.ID, err = e.fixes.StartOperation(ctx, op, ids(rows)); err != nil {
		e.t.Fatal(err)
	}
	steps := map[string][]string{
		store.OpRequested: nil,
		store.OpWriting:   {store.OpWriting},
		store.OpVerifying: {store.OpWriting, store.OpVerifying},
		store.OpRestoring: {store.OpWriting, store.OpVerifying, store.OpRestoring},
	}[state]
	for _, st := range steps {
		detail := ""
		if st == store.OpRestoring {
			detail = "readback_mismatch: title"
		}
		if err := e.fixes.Advance(ctx, op.ID, st, nil, detail); err != nil {
			e.t.Fatal(err)
		}
	}
	if landed {
		if _, err := e.cal.SetFields(ctx, "library", 2, changes); err != nil {
			e.t.Fatal(err)
		}
	}
	return op
}

func TestRecover(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name    string
		state   string
		landed  bool
		fields  []string
		edit    func(*library.Book)
		want    []string // states after the crash
		restore bool     // the book is back to before
		left    string   // named in the failed detail
		found   string   // the title sort recorded before the restore wrote
	}{
		{name: "requested", state: store.OpRequested, fields: allFields, want: []string{"failed"}, restore: true},
		{name: "writing, write landed", state: store.OpWriting, landed: true, fields: allFields, want: []string{"restoring", "restored"}, restore: true},
		{name: "writing, nothing sent", state: store.OpWriting, fields: allFields, want: []string{"restoring", "restored"}, restore: true},
		{name: "verifying", state: store.OpVerifying, landed: true, fields: allFields, want: []string{"restoring", "restored"}, restore: true},
		{name: "restoring", state: store.OpRestoring, landed: true, fields: allFields, want: []string{"restored"}, restore: true},
		{name: "sort edited after the crash", state: store.OpWriting, landed: true, fields: []string{"title"},
			edit: func(b *library.Book) { s := "Felix Sort"; b.TitleSort = &s }, want: []string{"restoring", "restoring", "restored"}, restore: true, found: "Felix Sort"},
		{name: "edited after the crash", state: store.OpWriting, landed: true, fields: []string{"title", "authors"},
			edit: func(b *library.Book) { b.Title = "Someone Else's Title" }, want: []string{"restoring", "failed"}, left: "title"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := setup(t)
			before := evidence.RecordFrom(e.cal.book(2))
			op := e.crashed(tc.state, tc.landed, tc.fields...)
			if tc.edit != nil {
				e.cal.edit(2, tc.edit)
			}
			sets := e.cal.setCount()
			if err := e.svc.Recover(ctx); err != nil {
				t.Fatal(err)
			}
			got := e.events(op.ID)
			if !slices.Equal(got[len(got)-len(tc.want):], tc.want) {
				t.Fatalf("states = %v, want them to end %v", got, tc.want)
			}
			if inc, _ := e.fixes.Incidents(ctx); len(inc) != 1 {
				t.Fatalf("incidents = %+v", inc)
			}
			book := evidence.RecordFrom(e.cal.book(2))
			if tc.restore && !reflect.DeepEqual(fieldsOf(book), fieldsOf(before)) {
				t.Fatalf("book = %+v\nbefore %+v", book, before)
			}
			if tc.state == store.OpRequested && e.cal.setCount() != sets {
				t.Fatal("recovery of a requested operation wrote")
			}
			if tc.left != "" {
				done, _ := e.fixes.Operation(ctx, op.ID)
				if !strings.Contains(done.Detail, tc.left) || book.Title != "Someone Else's Title" ||
					!slices.Equal(book.Authors, before.Authors) || book.AuthorSort != before.AuthorSort {
					t.Fatalf("operation %+v, book %+v", done, book)
				}
			}
			if tc.found != "" {
				var sort string
				if err := e.st.Pool.QueryRow(ctx, `SELECT after->>'title_sort' FROM operation_events
					WHERE operation_id = $1 AND state = 'restoring' AND after IS NOT NULL`, op.ID).Scan(&sort); err != nil || sort != tc.found {
					t.Fatalf("recorded sort = %q, %v", sort, err)
				}
			}
			if un, _ := e.fixes.Unfinished(ctx); len(un) != 0 {
				t.Fatalf("unfinished after Recover: %+v", un)
			}
		})
	}

	t.Run("Calibre unreachable while restoring", func(t *testing.T) {
		e := setup(t)
		before := evidence.RecordFrom(e.cal.book(2))
		op := e.crashed(store.OpRestoring, true, allFields...)
		e.cal.failRead = library.ErrUnreachable
		if err := e.svc.Recover(ctx); !errors.Is(err, library.ErrUnreachable) {
			t.Fatalf("Recover = %v", err)
		}
		if got, _ := e.fixes.Operation(ctx, op.ID); got.State != store.OpRestoring {
			t.Fatalf("state = %s", got.State)
		}
		e.cal.failRead = nil
		if err := e.svc.Recover(ctx); err != nil {
			t.Fatal(err)
		}
		if got, _ := e.fixes.Operation(ctx, op.ID); got.State != store.OpRestored {
			t.Fatalf("state = %s", got.State)
		}
		if book := evidence.RecordFrom(e.cal.book(2)); !reflect.DeepEqual(fieldsOf(book), fieldsOf(before)) {
			t.Fatalf("book = %+v", book)
		}
	})

	t.Run("writes off records the incident and waits", func(t *testing.T) {
		e := setup(t)
		op := e.crashed(store.OpWriting, true, allFields...)
		if err := e.fixes.SetWritesEnabled(ctx, false, "felix"); err != nil {
			t.Fatal(err)
		}
		sets := e.cal.setCount()
		if err := e.svc.Recover(ctx); err != nil {
			t.Fatal(err)
		}
		if got, _ := e.fixes.Operation(ctx, op.ID); got.State != store.OpRestoring || e.cal.setCount() != sets {
			t.Fatalf("state = %s, writes %d", got.State, e.cal.setCount()-sets)
		}
		if paused, _ := e.fixes.Paused(ctx); !paused {
			t.Fatal("not paused")
		}
	})

	t.Run("Apply recovers first", func(t *testing.T) {
		e := setup(t)
		op := e.crashed(store.OpVerifying, true, allFields...)
		if _, err := e.svc.Apply(ctx, "felix"); !errors.Is(err, ErrPaused) {
			t.Fatalf("Apply = %v", err)
		}
		if got, _ := e.fixes.Operation(ctx, op.ID); got.State != store.OpRestored {
			t.Fatalf("state = %s", got.State)
		}
	})
}

func TestWriterLock(t *testing.T) {
	ctx := context.Background()
	e := setup(t)
	orig := e.applied()
	e.cal.entered, e.cal.release = make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := e.svc.Undo(ctx, orig.ID, "felix")
		done <- err
	}()
	<-e.cal.entered
	if _, err := e.svc.Apply(ctx, "felix"); !errors.Is(err, ErrBusy) {
		t.Errorf("Apply during an undo = %v", err)
	}
	if _, err := e.svc.Undo(ctx, orig.ID, "felix"); !errors.Is(err, ErrBusy) {
		t.Errorf("Undo during an undo = %v", err)
	}
	if err := e.svc.Recover(ctx); !errors.Is(err, ErrBusy) {
		t.Errorf("Recover during an undo = %v", err)
	}
	close(e.cal.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// TestUndoBeforeMapCapture: an operation recorded before bookwarden read the
// authors table still undoes once reads carry the table.
func TestUndoBeforeMapCapture(t *testing.T) {
	ctx := context.Background()
	e := setup(t)
	orig := e.applied()
	if orig.After.AuthorSortMap != nil {
		t.Fatal("fixture already carries an author table")
	}
	e.cal.edit(2, func(b *library.Book) { b.AuthorSortMap = map[string]string{"Bea Sample": "Sample, Bea"} })
	if _, err := e.svc.Undo(ctx, orig.ID, "felix"); err != nil {
		t.Fatalf("Undo = %v", err)
	}
}

// TestUndoBeforeSeriesCapture: an operation recorded before bookwarden read
// the series still undoes when the live book has one.
func TestUndoBeforeSeriesCapture(t *testing.T) {
	ctx := context.Background()
	e := setup(t)
	orig := e.applied()
	if orig.After.Series != "" || orig.After.SeriesIndex != "" {
		t.Fatal("fixture already carries a series")
	}
	e.cal.edit(2, func(b *library.Book) { b.Series, b.SeriesIndex = "The Spike Saga", "2.5" })
	if _, err := e.svc.Undo(ctx, orig.ID, "felix"); err != nil {
		t.Fatalf("Undo = %v", err)
	}
}

// TestUndoMapChanged: a recorded table that differs from Calibre's now is a
// change, as any other field.
func TestUndoMapChanged(t *testing.T) {
	ctx := context.Background()
	e := setup(t, func() library.Book {
		b := book2()
		b.AuthorSortMap = map[string]string{"Unknown": "Unknown"}
		return b
	}())
	orig := e.applied()
	e.cal.edit(2, func(b *library.Book) { b.AuthorSortMap = map[string]string{"Bea Sample": "Bea Sample"} })
	if _, err := e.svc.Undo(ctx, orig.ID, "felix"); !errors.Is(err, ErrChanged) {
		t.Fatalf("Undo = %v, want ErrChanged", err)
	}
}

// TestUndoLastModified: Calibre 9.16 reports last_modified with
// microseconds from its cache right after a write, and in whole seconds,
// truncated, once it reloads the library. Only that is the same instant.
func TestUndoLastModified(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name    string
		start   time.Duration // added to the fake clock before the apply
		now     func(recorded time.Time) time.Time
		edit    func(b *library.Book)
		changed bool
	}{
		{"reloaded", 876978 * time.Microsecond, func(r time.Time) time.Time { return r.Truncate(time.Second) }, nil, false},
		{"rounded up is a change", 876978 * time.Microsecond, func(r time.Time) time.Time { return r.Truncate(time.Second).Add(time.Second) }, nil, true},
		{"a second earlier", 876978 * time.Microsecond, func(r time.Time) time.Time { return r.Truncate(time.Second).Add(-time.Second) }, nil, true},
		{"whole seconds recorded, a second later", 0, func(r time.Time) time.Time { return r.Add(time.Second) }, nil, true},
		{"whole seconds recorded, microseconds now", 0, func(r time.Time) time.Time { return r.Add(1500 * time.Microsecond) }, nil, true},
		{"reloaded but the title changed", 876978 * time.Microsecond, func(r time.Time) time.Time { return r.Truncate(time.Second) },
			func(b *library.Book) { b.Title = "Edited" }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := setup(t)
			e.cal.clock = e.cal.clock.Add(tc.start)
			orig := e.applied()
			recorded, err := time.Parse(time.RFC3339Nano, orig.After.LastModified)
			if err != nil {
				t.Fatal(err)
			}
			e.cal.edit(2, func(b *library.Book) {
				b.LastModified = tc.now(recorded)
				if tc.edit != nil {
					tc.edit(b)
				}
			})
			id, err := e.svc.Undo(ctx, orig.ID, "felix")
			if tc.changed {
				if !errors.Is(err, ErrChanged) {
					t.Fatalf("Undo = %v, want ErrChanged", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Undo = %v", err)
			}
			undo, err := e.fixes.Operation(ctx, id)
			if err != nil || undo.Before.LastModified != tc.now(recorded).UTC().Format(time.RFC3339Nano) {
				t.Fatalf("undo before last_modified = %q, %v; want the value Calibre reported", undo.Before.LastModified, err)
			}
		})
	}
}

// TestUndoRefusedByCalibre: an undo Calibre refuses with the book unchanged
// fails without an incident, and the original can still be undone.
func TestUndoRefusedByCalibre(t *testing.T) {
	ctx := context.Background()
	e := setup(t)
	orig := e.applied()
	e.cal.failSet = library.ErrForbidden
	if _, err := e.svc.Undo(ctx, orig.ID, "felix"); !errors.Is(err, ErrRefused) {
		t.Fatalf("Undo = %v, want ErrRefused", err)
	}
	ops := e.operationIDs()
	if got := e.events(ops[len(ops)-1]); !slices.Equal(got, []string{"requested", "writing", "failed"}) {
		t.Fatalf("undo states = %v", got)
	}
	if inc, _ := e.fixes.Incidents(ctx); len(inc) != 0 {
		t.Fatalf("incidents = %+v, want none", inc)
	}
	e.cal.failSet = nil
	if _, err := e.svc.Undo(ctx, orig.ID, "felix"); err != nil {
		t.Fatalf("undo after access was given: %v", err)
	}
}
