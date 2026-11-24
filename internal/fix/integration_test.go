//go:build calibre

package fix

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/url"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/findings"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/sources"
	"github.com/felixapel/calibre-bookwarden/internal/store"
	"github.com/felixapel/calibre-bookwarden/internal/testutil/pgtest"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

// The write tests run inside the disposable Calibre in test/calibre, after
// the read-only test; scripts/integration-test.sh starts them.

func itClient(t *testing.T, user, passwordVar string) *library.Client {
	t.Helper()
	get := func(name string) string {
		v := os.Getenv(name)
		if v == "" {
			t.Skipf("%s is not set; run scripts/integration-test.sh", name)
		}
		return v
	}
	base, err := url.Parse(get("BW_IT_CALIBRE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	return library.New(library.Config{BaseURL: base, User: user, Password: get(passwordVar)})
}

func raw(v any) json.RawMessage { return jsonValue(v) }

// TestCalibreWriteBehaviour pins the Calibre 9.16 behaviour the writer's
// rules rely on (ADR-021), on fixture books 1 and 3.
func TestCalibreWriteBehaviour(t *testing.T) {
	ctx := context.Background()
	w := itClient(t, "writer", "BW_IT_WRITER_PASSWORD")
	lib, err := w.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	read := func(id int) library.Book {
		t.Helper()
		b, err := w.BookInLibrary(ctx, lib, id)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	set := func(id int, cs ...library.Change) library.Book {
		t.Helper()
		b, err := w.SetFields(ctx, lib, id, cs)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}

	t.Run("pubdate date-only is midnight UTC", func(t *testing.T) {
		if got := set(1, ch(fPubdate, `"2001-07-26"`)).PubDate; got != "2001-07-26T00:00:00+00:00" {
			t.Errorf("answer pubdate = %q", got)
		}
		if got := read(1).PubDate; got != "2001-07-26T00:00:00+00:00" {
			t.Errorf("listed pubdate = %q", got)
		}
	})
	t.Run("pubdate edge days shift", func(t *testing.T) {
		for sent, stored := range map[string]string{
			"2001-07-01": "2001-07-02", "2001-07-31": "2001-07-30", "2001-12-31": "2001-12-31",
			"2001-02-28": "2001-02-27", "2001-07-15": "2001-07-15",
		} {
			c := ch(fPubdate, string(raw(sent)))
			set(1, c)
			got := findings.DateKey(read(1).PubDate)
			if !got.Valid || got.Value != stored {
				t.Errorf("%s stored as %+v, want %s", sent, got, stored)
			}
			if storable(c) != (sent == stored) {
				t.Errorf("storable(%s) = %v, but Calibre stored %s", sent, storable(c), got.Value)
			}
		}
	})
	t.Run("a timestamp keeps its instant, null unsets", func(t *testing.T) {
		set(1, ch(fPubdate, `"2001-07-25T22:00:00+00:00"`))
		if c := ch(fPubdate, `"2001-07-25T22:00:00+00:00"`); !same(c, read(1)) {
			t.Errorf("listed pubdate = %q", read(1).PubDate)
		}
		set(1, ch(fPubdate, `null`))
		if got := read(1).PubDate; got != "" {
			t.Errorf("pubdate after null = %q", got)
		}
	})
	t.Run("a title write recomputes the sort, an ordered sort overrides it", func(t *testing.T) {
		set(1, ch(fTitle, `"The Clockwork Garden Revised"`))
		if b := read(1); b.TitleSort == nil || *b.TitleSort != "Clockwork Garden Revised, The" {
			t.Errorf("sort after a title write = %v", b.TitleSort)
		}
		set(1, ch(fTitle, `"The Clockwork Garden"`), ch(fSort, `"Custom Sort"`))
		if b := read(1); b.Title != "The Clockwork Garden" || b.TitleSort == nil || *b.TitleSort != "Custom Sort" {
			t.Errorf("title and sort = %q, %v", b.Title, b.TitleSort)
		}
	})
	t.Run("a sort-only write moves last_modified, an unchanged value doesn't", func(t *testing.T) {
		b0 := read(1)
		time.Sleep(10 * time.Millisecond)
		set(1, ch(fSort, `"Another Sort"`))
		b1 := read(1)
		if !b1.LastModified.After(b0.LastModified) {
			t.Errorf("last_modified %v → %v after a sort-only write", b0.LastModified, b1.LastModified)
		}
		time.Sleep(10 * time.Millisecond)
		set(1, ch(fSort, `"Another Sort"`))
		if b2 := read(1); !b2.LastModified.Equal(b1.LastModified) {
			t.Errorf("last_modified %v → %v after writing the same value", b1.LastModified, b2.LastModified)
		}
	})
	t.Run("authors: & becomes ;", func(t *testing.T) {
		before := read(3)
		c := ch(fAuthors, `["Cy & Placeholder"]`)
		set(3, c)
		if got := read(3).Authors; len(got) != 1 || strings.Contains(got[0], "&") || !strings.Contains(got[0], ";") || storable(c) {
			t.Errorf("authors = %q, storable = %v", got, storable(c))
		}
		set(3, ch(fAuthors, string(raw(before.Authors))), ch(fAuthorSort, string(raw(before.AuthorSort))))
	})
	t.Run("a case variant renames another book's author", func(t *testing.T) {
		names, err := w.FieldNames(ctx, lib, fAuthors)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(names, "Ada Example") || !caseCollision([]string{"ada example"}, names) {
			t.Fatalf("field names = %q", names)
		}
		before := read(3)
		set(3, ch(fAuthors, `["ada example"]`))
		if got := read(1).Authors; !slices.Equal(got, []string{"ada example"}) {
			t.Errorf("book 1's authors after writing book 3 = %q", got)
		}
		set(1, ch(fAuthors, `["Ada Example"]`))
		set(3, ch(fAuthors, string(raw(before.Authors))), ch(fAuthorSort, string(raw(before.AuthorSort))))
		if got := read(1).Authors; !slices.Equal(got, []string{"Ada Example"}) {
			t.Errorf("book 1's authors after the repair = %q", got)
		}
	})
	t.Run("identifiers replaces the whole map", func(t *testing.T) {
		ids := read(1).Identifiers
		more := maps.Clone(ids)
		more["test"] = "x"
		set(1, ch(fIdentifiers, string(raw(more))))
		if got := read(1).Identifiers; !maps.Equal(got, more) {
			t.Errorf("identifiers = %v", got)
		}
		set(1, ch(fIdentifiers, string(raw(ids))))
		if got := read(1).Identifiers; !maps.Equal(got, ids) {
			t.Errorf("identifiers after replacing = %v, want %v", got, ids)
		}
	})
	t.Run("the reader account gets ErrForbidden", func(t *testing.T) {
		r := itClient(t, "reader", "BW_IT_READER_PASSWORD")
		before := read(2)
		if _, err := r.SetFields(ctx, lib, 2, []library.Change{ch(fTitle, `"Should Not Apply"`)}); !errors.Is(err, library.ErrForbidden) {
			t.Errorf("reader set-fields = %v", err)
		}
		if names, err := r.FieldNames(ctx, lib, fAuthors); err != nil || len(names) == 0 {
			t.Errorf("reader field names = %q, %v", names, err)
		}
		if after := read(2); !reflect.DeepEqual(after, before) {
			t.Errorf("book 2 changed: %+v", after)
		}
	})
}

// itSources answers fixture book 2's ISBN with agreeing DNB and Open
// Library records, without the network.
type itSources struct{}

func (itSources) BeginRun(context.Context, int64, int) {}

func (itSources) Lookup(_ context.Context, _ int64, seeds []sources.Seed) []evidence.Lookup {
	var out []evidence.Lookup
	for _, s := range seeds {
		if s.ISBN != isbn || slices.ContainsFunc(out, func(l evidence.Lookup) bool { return l.ISBN == isbn }) {
			continue
		}
		rec := func(id string) evidence.SourceRecord {
			return evidence.SourceRecord{ID: id, Title: "Lighthouse Signals", Authors: []string{"Bea Sample"}, Languages: []string{"eng"},
				Publisher: "Harbour Press", Date: "2001-07-26", ISBNs: []string{isbn}}
		}
		out = append(out, evidence.Lookup{ISBN: isbn, Seeds: []string{s.Class}, Results: []evidence.SourceResult{
			{Source: evidence.SourceDNB, Outcome: evidence.SourceOK, Records: []evidence.SourceRecord{rec("1")}},
			{Source: evidence.SourceOpenLibrary, Outcome: evidence.SourceOK, Records: []evidence.SourceRecord{rec("OL1M")}},
		}})
	}
	return out
}

// altering appends " (x)" to the first title it sends, to force a
// readback mismatch on the real server.
type altering struct {
	*library.Client
	once sync.Once
}

func (a *altering) SetFields(ctx context.Context, lib string, id int, changes []library.Change) (library.Book, error) {
	out := slices.Clone(changes)
	a.once.Do(func() {
		for i, c := range out {
			if c.Field == fTitle {
				s, _ := decode[string](c.Value)
				out[i].Value = jsonValue(s + " (x)")
			}
		}
	})
	return a.Client.SetFields(ctx, lib, id, out)
}

// TestFixesOnRealCalibre proves spec section 15's fixing scenarios on book 2.
func TestFixesOnRealCalibre(t *testing.T) {
	ctx := context.Background()
	w := itClient(t, "writer", "BW_IT_WRITER_PASSWORD")
	lib, err := w.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, pgtest.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	runs, fixes := store.NewRunRepo(st), store.NewFixRepo(st)
	runner := verify.NewRunner(runs, w, verify.Options{ScratchDir: t.TempDir(), Sources: itSources{}})
	sctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- runner.Serve(sctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	read := func() library.Book {
		t.Helper()
		b, err := w.BookInLibrary(ctx, lib, 2)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	check := func() int64 {
		t.Helper()
		run, err := runner.Start(ctx, verify.Scope{BookID: 2, ExpectedLibraryID: lib, ExpectedUUID: read().UUID})
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(time.Minute)
		for {
			v, err := runner.View(ctx, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if v.Run.Status.Terminal() {
				if v.Run.Status != verify.RunCompleted || v.Progress.Checked != 1 {
					t.Fatalf("check ended %s with %+v", v.Run.Status, v.Progress)
				}
				return run.ID
			}
			if time.Now().After(deadline) {
				t.Fatal("check still running after a minute")
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	operations := func() int {
		t.Helper()
		var n int
		if err := st.Pool.QueryRow(ctx, `SELECT count(*) FROM operations`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	svc := New(fixes, runs, w, nil)
	if err := fixes.SetWritesEnabled(ctx, true, "integration"); err != nil {
		t.Fatal(err)
	}

	t.Run("fix, readback and undo restore the exact before values", func(t *testing.T) {
		before := evidence.RecordFrom(read())
		run := check()
		if err := svc.Approve(ctx, run, 2, allFields, "integration", store.Basis{Mode: store.DecisionBulk}); err != nil {
			t.Fatal(err)
		}
		sum, err := svc.Apply(ctx, "integration")
		if err != nil || sum != (Summary{Applied: 5}) {
			t.Fatalf("Apply = %+v, %v", sum, err)
		}
		b := read()
		if b.Title != "Lighthouse Signals" || !slices.Equal(b.Authors, []string{"Bea Sample"}) || !slices.Equal(b.Languages, []string{"eng"}) ||
			b.Publisher != "Harbour Press" || b.PubDate != "2001-07-26T00:00:00+00:00" {
			t.Fatalf("book after Apply = %+v", b)
		}
		var opID int64
		if err := st.Pool.QueryRow(ctx, `SELECT max(id) FROM operations`).Scan(&opID); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.Undo(ctx, opID, "integration"); err != nil {
			t.Fatal(err)
		}
		if got := evidence.RecordFrom(read()); !reflect.DeepEqual(fieldsOf(got), fieldsOf(before)) {
			t.Fatalf("book after undo = %+v\nbefore %+v", got, before)
		}
	})

	t.Run("a stale proposal writes nothing", func(t *testing.T) {
		run := check()
		if err := svc.Approve(ctx, run, 2, allFields, "integration", store.Basis{Mode: store.DecisionBulk}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.SetFields(ctx, lib, 2, []library.Change{ch(fSort, `"Edited In Calibre"`)}); err != nil {
			t.Fatal(err)
		}
		edited, ops := read(), operations()
		sum, err := svc.Apply(ctx, "integration")
		if err != nil || sum != (Summary{Stale: 5}) {
			t.Fatalf("Apply = %+v, %v", sum, err)
		}
		if b := read(); !reflect.DeepEqual(b, edited) || operations() != ops {
			t.Fatalf("book changed or an operation was recorded: %+v", b)
		}
	})

	t.Run("a read-only account is refused without an incident", func(t *testing.T) {
		before := evidence.RecordFrom(read())
		run := check()
		if err := svc.Approve(ctx, run, 2, []string{"title"}, "integration", store.Basis{Mode: store.DecisionBulk}); err != nil {
			t.Fatal(err)
		}
		readOnly := New(fixes, runs, itClient(t, "reader", "BW_IT_READER_PASSWORD"), nil)
		sum, err := readOnly.Apply(ctx, "integration")
		if !errors.Is(err, ErrRefused) || sum.Failed != 1 {
			t.Fatalf("Apply with the reader account = %+v, %v; want ErrRefused", sum, err)
		}
		if got := evidence.RecordFrom(read()); !reflect.DeepEqual(fieldsOf(got), fieldsOf(before)) {
			t.Fatalf("book after a refused write = %+v\nbefore %+v", got, before)
		}
		if inc, err := fixes.Incidents(ctx); err != nil || len(inc) != 0 {
			t.Fatalf("incidents = %+v, %v; want none", inc, err)
		}
		if paused, err := fixes.Paused(ctx); err != nil || paused {
			t.Fatalf("Paused = %v, %v; want writes not paused", paused, err)
		}
	})

	t.Run("a forced readback mismatch restores and opens an incident", func(t *testing.T) {
		before := evidence.RecordFrom(read())
		run := check()
		if err := svc.Approve(ctx, run, 2, []string{"title"}, "integration", store.Basis{Mode: store.DecisionBulk}); err != nil {
			t.Fatal(err)
		}
		forced := New(fixes, runs, &altering{Client: w}, nil)
		if _, err := forced.Apply(ctx, "integration"); !errors.Is(err, ErrPaused) {
			t.Fatalf("Apply = %v", err)
		}
		if got := evidence.RecordFrom(read()); !reflect.DeepEqual(fieldsOf(got), fieldsOf(before)) {
			t.Fatalf("book after restore = %+v\nbefore %+v", got, before)
		}
		inc, err := fixes.Incidents(ctx)
		if err != nil || len(inc) != 1 || inc[0].Reason != "readback_mismatch" {
			t.Fatalf("incidents = %+v, %v", inc, err)
		}
		if op, _ := fixes.Operation(ctx, inc[0].OperationID); op.State != store.OpRestored {
			t.Fatalf("operation = %+v", op)
		}
		if _, err := svc.Apply(ctx, "integration"); !errors.Is(err, ErrPaused) {
			t.Fatalf("Apply while paused = %v", err)
		}
		if err := fixes.Acknowledge(ctx, inc[0].ID, "integration"); err != nil {
			t.Fatal(err)
		}
		if paused, err := fixes.Paused(ctx); err != nil || paused {
			t.Fatalf("Paused after acknowledging = %v, %v", paused, err)
		}
	})

	t.Run("sort fixes write Calibre's own sorts and undo them alone", func(t *testing.T) {
		read1 := func() library.Book {
			t.Helper()
			b, err := w.BookInLibrary(ctx, lib, 1)
			if err != nil {
				t.Fatal(err)
			}
			return b
		}
		orig := read1()
		if orig.AuthorSortMap["Ada Example"] != "Example, Ada" {
			t.Fatalf("author table = %v, want Calibre's default sort", orig.AuthorSortMap)
		}
		t.Cleanup(func() {
			if _, err := w.SetFields(ctx, lib, 1, []library.Change{ch(fSort, string(jsonValue(*orig.TitleSort))), ch(fAuthorSort, string(jsonValue(orig.AuthorSort)))}); err != nil {
				t.Error(err)
			}
		})
		if _, err := w.SetFields(ctx, lib, 1, []library.Change{ch(fSort, `"Garden"`), ch(fAuthorSort, `"Road, The"`)}); err != nil {
			t.Fatal(err)
		}
		run, err := runner.Start(ctx, verify.Scope{BookID: 1, ExpectedLibraryID: lib, ExpectedUUID: orig.UUID})
		if err != nil {
			t.Fatal(err)
		}
		for deadline := time.Now().Add(time.Minute); ; time.Sleep(50 * time.Millisecond) {
			v, err := runner.View(ctx, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if v.Run.Status.Terminal() {
				if v.Run.Status != verify.RunCompleted {
					t.Fatalf("check ended %s", v.Run.Status)
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("check still running after a minute")
			}
		}
		if err := svc.Approve(ctx, run.ID, 1, []string{"title_sort", "author_sort"}, "integration", store.Basis{Mode: store.DecisionBulk}); err != nil {
			t.Fatal(err)
		}
		sum, err := svc.Apply(ctx, "integration")
		if err != nil || sum != (Summary{Applied: 2}) {
			t.Fatalf("Apply = %+v, %v", sum, err)
		}
		b := read1()
		if b.TitleSort == nil || *b.TitleSort != "Clockwork Garden, The" || b.AuthorSort != "Example, Ada" || b.Title != orig.Title {
			t.Fatalf("book after Apply = %+v", b)
		}
		// A book's author sort is the book's alone: the authors table stays.
		if !maps.Equal(b.AuthorSortMap, orig.AuthorSortMap) {
			t.Fatalf("authors table after Apply = %v, was %v", b.AuthorSortMap, orig.AuthorSortMap)
		}
		var opID int64
		if err := st.Pool.QueryRow(ctx, `SELECT max(id) FROM operations`).Scan(&opID); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.Undo(ctx, opID, "integration"); err != nil {
			t.Fatal(err)
		}
		if b := read1(); *b.TitleSort != "Garden" || b.AuthorSort != "Road, The" || b.Title != orig.Title || !maps.Equal(b.AuthorSortMap, orig.AuthorSortMap) {
			t.Fatalf("book after undo = %+v", b)
		}
	})

	t.Run("an authors replacement applies and undoes, as the title is right", func(t *testing.T) {
		// Book 2's authors are the placeholder Unknown, so its authors fix is
		// a fill. Here the title is right and the authors are another
		// person's: a replacement, which the other-work guard (ADR-035)
		// leaves alone because the title matches.
		orig := read()
		t.Cleanup(func() {
			cs := []library.Change{ch(fTitle, string(jsonValue(orig.Title))), ch(fAuthors, string(jsonValue(orig.Authors))), ch(fAuthorSort, string(jsonValue(orig.AuthorSort)))}
			if orig.TitleSort != nil {
				cs = append(cs, ch(fSort, string(jsonValue(*orig.TitleSort))))
			}
			if _, err := w.SetFields(ctx, lib, 2, cs); err != nil {
				t.Error(err)
			}
		})
		if _, err := w.SetFields(ctx, lib, 2, []library.Change{ch(fTitle, `"Lighthouse Signals"`), ch(fAuthors, `["Wrong Author"]`)}); err != nil {
			t.Fatal(err)
		}
		before := evidence.RecordFrom(read())
		run := check()
		if err := svc.Approve(ctx, run, 2, []string{"authors"}, "integration", store.Basis{Mode: store.DecisionBulk}); err != nil {
			t.Fatal(err)
		}
		sum, err := svc.Apply(ctx, "integration")
		if err != nil || sum != (Summary{Applied: 1}) {
			t.Fatalf("Apply = %+v, %v", sum, err)
		}
		if b := read(); !slices.Equal(b.Authors, []string{"Bea Sample"}) || b.Title != "Lighthouse Signals" {
			t.Fatalf("book after Apply = %+v", b)
		}
		var opID int64
		if err := st.Pool.QueryRow(ctx, `SELECT max(id) FROM operations`).Scan(&opID); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.Undo(ctx, opID, "integration"); err != nil {
			t.Fatal(err)
		}
		if got := evidence.RecordFrom(read()); !reflect.DeepEqual(fieldsOf(got), fieldsOf(before)) {
			t.Fatalf("book after undo = %+v\nbefore %+v", got, before)
		}
	})
}

// TestCoverOnRealCalibre pins the cover facts ADR-023 relies on: the export
// envelope, JPEG storage, nil for a book without a cover, and whether a new
// cover moves last_modified (the stale check's only signal for a cover).
func TestCoverOnRealCalibre(t *testing.T) {
	ctx := context.Background()
	w := itClient(t, "writer", "BW_IT_WRITER_PASSWORD")
	lib, err := w.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var buf strings.Builder
	res, err := w.ExportCoverInLibrary(ctx, lib, 1, &buf, library.CoverCap)
	if err != nil || res.Status != library.ExportOK || res.Bytes != int64(buf.Len()) || !strings.HasPrefix(buf.String(), "\xff\xd8\xff") {
		t.Fatalf("book 1 cover: %+v, %v, starts %q", res, err, buf.String()[:min(4, buf.Len())])
	}
	if res, err := w.ExportCoverInLibrary(ctx, lib, 5, &buf, library.CoverCap); err != nil || res.Status != library.ExportNotInCalibre {
		t.Fatalf("book 5 (no cover): %+v, %v", res, err)
	}
	if res, err := w.ExportCoverInLibrary(ctx, lib, 1, &buf, 100); err != nil || res.Status != library.ExportTooLarge {
		t.Fatalf("book 1 over a 100-byte cap: %+v, %v", res, err)
	}
	before, err := w.BookInLibrary(ctx, lib, 4)
	if err != nil || before.HasCover {
		t.Fatalf("book 4 before: %+v, %v", before, err)
	}
	time.Sleep(1100 * time.Millisecond) // last_modified has one-second precision in the listing
	// A 1×1 PNG, as the content server's editor sends a new cover.
	png := "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAAAAAA6fptVAAAACklEQVR4nGNgAAAAAgABSK+kcQAAAABJRU5ErkJggg=="
	if _, err := w.SetFields(ctx, lib, 4, []library.Change{{Field: "cover", Value: raw(png)}}); err != nil {
		t.Fatalf("set a cover: %v", err)
	}
	after, err := w.BookInLibrary(ctx, lib, 4)
	if err != nil || !after.HasCover {
		t.Fatalf("book 4 after: %+v, %v", after, err)
	}
	if !after.LastModified.After(before.LastModified) {
		t.Errorf("a new cover left last_modified at %s", after.LastModified)
	}
}
