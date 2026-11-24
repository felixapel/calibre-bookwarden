package fix

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/decide"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/report"
	"github.com/felixapel/calibre-bookwarden/internal/store"
	"github.com/felixapel/calibre-bookwarden/internal/testutil/pgtest"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

const isbn = "9780000000019"

// fakeCalibre keeps one book per id and writes the way Calibre 9.16 does
// (ADR-021): a title recomputes the sort, authors their sort, an unchanged
// value moves nothing, and every effective change moves last_modified.
type fakeCalibre struct {
	mu        sync.Mutex
	books     map[int]library.Book
	names     map[string][]string // extra FieldNames entries
	clock     time.Time
	sets      int
	writes    [][]library.Change
	failSet   error       // returned before anything is written
	failSkip  int         // set-fields calls that succeed before failSet applies
	failRead  error       // returned by BookInLibrary
	hang      bool        // calls block until their context ends
	afterSet  func()      // runs after each successful set-fields
	afterRead func(n int) // runs after each read returned its book, with the read count
	reads     int
	errAfter  error                               // returned after the write landed
	alter     func(library.Change) library.Change // applied to what is stored
	alterOnce bool
	entered   chan struct{} // SetFields signals here, then waits on release
	release   chan struct{}
}

func newFake(books ...library.Book) *fakeCalibre {
	f := &fakeCalibre{books: map[int]library.Book{}, names: map[string][]string{}, clock: time.Date(2026, 10, 19, 8, 0, 0, 0, time.UTC)}
	for _, b := range books {
		f.books[b.ID] = b
	}
	return f
}

func cloneBook(b library.Book) library.Book {
	b.Authors = slices.Clone(b.Authors)
	b.Languages = slices.Clone(b.Languages)
	b.Identifiers = maps.Clone(b.Identifiers)
	b.Formats = slices.Clone(b.Formats)
	if b.TitleSort != nil {
		s := *b.TitleSort
		b.TitleSort = &s
	}
	return b
}

func (f *fakeCalibre) book(id int) library.Book {
	f.mu.Lock()
	defer f.mu.Unlock()
	return cloneBook(f.books[id])
}

func (f *fakeCalibre) edit(id int, fn func(*library.Book)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b := cloneBook(f.books[id])
	fn(&b)
	f.books[id] = b
}

func (f *fakeCalibre) BookInLibrary(ctx context.Context, lib string, id int) (library.Book, error) {
	if f.hang {
		<-ctx.Done()
		return library.Book{}, ctx.Err()
	}
	f.mu.Lock()
	if f.failRead != nil {
		f.mu.Unlock()
		return library.Book{}, f.failRead
	}
	b, ok := f.books[id]
	if !ok || lib != "library" {
		f.mu.Unlock()
		return library.Book{}, library.ErrBookNotFound
	}
	f.reads++
	n := f.reads
	out := cloneBook(b)
	f.mu.Unlock()
	if f.afterRead != nil {
		f.afterRead(n)
	}
	return out, nil
}

func (f *fakeCalibre) SetFields(ctx context.Context, lib string, id int, changes []library.Change) (library.Book, error) {
	if f.hang {
		<-ctx.Done()
		return library.Book{}, ctx.Err()
	}
	if f.afterSet != nil {
		defer f.afterSet()
	}
	if f.entered != nil {
		f.entered <- struct{}{}
		<-f.release
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sets++
	f.writes = append(f.writes, changes)
	if f.failSet != nil && f.sets > f.failSkip {
		return library.Book{}, f.failSet
	}
	b, ok := f.books[id]
	if !ok || lib != "library" {
		return library.Book{}, library.ErrBookNotFound
	}
	b = cloneBook(b)
	changed := false
	set := func(dst any, v any) {
		if !reflect.DeepEqual(reflect.ValueOf(dst).Elem().Interface(), v) {
			reflect.ValueOf(dst).Elem().Set(reflect.ValueOf(v))
			changed = true
		}
	}
	for _, c := range changes {
		if f.alter != nil {
			c = f.alter(c)
		}
		switch c.Field {
		case fTitle:
			v, _ := decode[string](c.Value)
			set(&b.Title, v)
			sort := "sort of " + v
			set(&b.TitleSort, &sort)
		case fSort:
			v, _ := decode[string](c.Value)
			set(&b.TitleSort, &v)
		case fAuthors:
			v, _ := decode[[]string](c.Value)
			set(&b.Authors, v)
			set(&b.AuthorSort, "sort of "+strings.Join(v, " & "))
		case fAuthorSort:
			v, _ := decode[string](c.Value)
			set(&b.AuthorSort, v)
		case fLanguages:
			v, _ := decode[[]string](c.Value)
			set(&b.Languages, v)
		case fIdentifiers:
			v, _ := decode[map[string]string](c.Value)
			set(&b.Identifiers, v)
		case fPublisher:
			v, _ := decode[string](c.Value)
			set(&b.Publisher, v)
		case fPubdate:
			v := ""
			if s, ok := decode[string](c.Value); ok && s != "" {
				if t, err := time.Parse("2006-01-02", s); err == nil {
					v = t.Format("2006-01-02T15:04:05") + "+00:00"
				} else if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
					v = t.UTC().Format("2006-01-02T15:04:05") + "+00:00"
				}
			}
			set(&b.PubDate, v)
		}
	}
	if f.alterOnce {
		f.alter = nil
	}
	if changed {
		f.clock = f.clock.Add(time.Second)
		b.LastModified = f.clock
	}
	f.books[id] = b
	if f.errAfter != nil {
		return library.Book{}, f.errAfter
	}
	answer := cloneBook(b)
	answer.Formats, answer.HasCover = []library.Format{}, false
	return answer, nil
}

func (f *fakeCalibre) FieldNames(_ context.Context, _ string, field string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	names := slices.Clone(f.names[field])
	for _, b := range f.books {
		switch field {
		case fAuthors:
			names = append(names, b.Authors...)
		case fPublisher:
			if b.Publisher != "" {
				names = append(names, b.Publisher)
			}
		}
	}
	return names, nil
}

func (f *fakeCalibre) setCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sets
}

// book2 is fixture book 2: Calibre's title, authors and language disagree
// with its EPUB, and it has no publisher or date. Its authors are the
// placeholder Calibre gives a missing author, so the authors fix is a fill
// and the other-work guard (ADR-035) stays out of the way.
func book2() library.Book {
	sort := "Incorrect Catalogue Title"
	return library.Book{
		ID: 2, UUID: "u-2", Title: "Incorrect Catalogue Title", TitleSort: &sort, Authors: []string{"Unknown"},
		AuthorSort: "Unknown", Languages: []string{"fra"}, Identifiers: map[string]string{"isbn": isbn},
		LastModified: time.Date(2026, 10, 12, 21, 5, 55, 0, time.UTC),
		Formats:      []library.Format{{Name: "EPUB", Size: 2340, MTime: time.Date(2026, 10, 12, 21, 5, 55, 0, time.UTC)}},
	}
}

type sourceSays struct {
	date    string
	authors []string
}

// pkgFor seals what a check of b finds: its EPUB, and DNB and Open Library
// records that agree with the file.
func pkgFor(t *testing.T, b library.Book, says sourceSays) evidence.Package {
	t.Helper()
	if says.date == "" {
		says.date = "2001-07-26"
	}
	if says.authors == nil {
		says.authors = []string{"Bea Sample"}
	}
	snap, err := evidence.Snapshot(b)
	if err != nil {
		t.Fatal(err)
	}
	rec := func(id string) evidence.SourceRecord {
		return evidence.SourceRecord{ID: id, Title: "Lighthouse Signals", Authors: says.authors, Languages: []string{"eng"},
			Publisher: "Harbour Press", Date: says.date, ISBNs: []string{isbn}}
	}
	return evidence.Package{
		SchemaVersion: evidence.SchemaVersion, LibraryID: "library", BookID: b.ID, UUID: b.UUID, Snapshot: snap,
		Calibre: evidence.RecordFrom(b),
		Formats: []evidence.FormatEvidence{{Format: "EPUB", Size: 2340, State: evidence.StateOK, Detected: "epub", Metadata: &evidence.FileMetadata{
			Title: "Lighthouse Signals", Authors: []string{"Bea Sample"}, Languages: []string{"en"}, ISBNs: []string{isbn}, Date: says.date,
		}}},
		Lookups: []evidence.Lookup{{ISBN: isbn, Seeds: []string{evidence.SeedFile}, Results: []evidence.SourceResult{
			{Source: evidence.SourceDNB, Outcome: evidence.SourceOK, Records: []evidence.SourceRecord{rec("1")}},
			{Source: evidence.SourceOpenLibrary, Outcome: evidence.SourceOK, Records: []evidence.SourceRecord{rec("OL1M")}},
		}}},
	}
}

type env struct {
	t     *testing.T
	st    *store.Store
	fixes *store.FixRepo
	runs  *store.RunRepo
	cal   *fakeCalibre
	svc   *Service
}

func setup(t *testing.T, books ...library.Book) *env {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, pgtest.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if len(books) == 0 {
		books = []library.Book{book2()}
	}
	e := &env{t: t, st: st, fixes: store.NewFixRepo(st), runs: store.NewRunRepo(st), cal: newFake(books...)}
	e.svc = New(e.fixes, e.runs, e.cal, nil)
	if err := e.fixes.SetWritesEnabled(ctx, true, "felix"); err != nil {
		t.Fatal(err)
	}
	return e
}

// check stores a completed one-book run whose evidence is p.
func (e *env) check(p evidence.Package) int64 {
	e.t.Helper()
	ctx := context.Background()
	ref := library.BookRef{ID: p.BookID, UUID: p.UUID}
	run, err := e.runs.CreateRun(ctx, verify.Scope{}, "library", "v1", []library.BookRef{ref})
	if err != nil {
		e.t.Fatal(err)
	}
	raw, seal, err := decide.Seal(ctx, p)
	if err != nil {
		e.t.Fatal(err)
	}
	if err := e.runs.FinishBook(ctx, run.ID, ref, verify.Outcome{State: verify.BookChecked, Evidence: raw, Seal: seal}); err != nil {
		e.t.Fatal(err)
	}
	if err := e.runs.SetStatus(ctx, run.ID, verify.RunCompleted, ""); err != nil {
		e.t.Fatal(err)
	}
	return run.ID
}

// checkV9 stores a completed one-book run whose evidence is p, sealed under
// decision version 9 as runs before version 10 were: v10 proposes no date
// Calibre would shift, but such v9 proposals still reach the inbox and must
// be refused at write time.
func (e *env) checkV9(p evidence.Package) int64 {
	e.t.Helper()
	ctx := context.Background()
	ref := library.BookRef{ID: p.BookID, UUID: p.UUID}
	run, err := e.runs.CreateRun(ctx, verify.Scope{}, "library", "v1", []library.BookRef{ref})
	if err != nil {
		e.t.Fatal(err)
	}
	raw, _, err := decide.Seal(ctx, p)
	if err != nil {
		e.t.Fatal(err)
	}
	d, err := decide.AtVersion(ctx, p, 9)
	if err != nil {
		e.t.Fatal(err)
	}
	var sealed evidence.Package
	if err := json.Unmarshal(raw, &sealed); err != nil {
		e.t.Fatal(err)
	}
	sealed.DecisionVersion, sealed.Tier, sealed.TierReasons, sealed.Proposals = 9, d.Tier, d.Reasons, d.Proposals
	raw, seal, err := evidence.Seal(sealed)
	if err != nil {
		e.t.Fatal(err)
	}
	if err := e.runs.FinishBook(ctx, run.ID, ref, verify.Outcome{State: verify.BookChecked, Evidence: raw, Seal: seal}); err != nil {
		e.t.Fatal(err)
	}
	if err := e.runs.SetStatus(ctx, run.ID, verify.RunCompleted, ""); err != nil {
		e.t.Fatal(err)
	}
	return run.ID
}

// checkBook checks the fake's current book with the default sources.
func (e *env) checkBook(id int) int64 {
	e.t.Helper()
	return e.check(pkgFor(e.t, e.cal.book(id), sourceSays{}))
}

var allFields = []string{"title", "authors", "languages", "publisher", "pubdate"}

func (e *env) approve(run int64, bookID int, fields ...string) {
	e.t.Helper()
	if err := e.svc.Approve(context.Background(), run, bookID, fields, "felix", store.Basis{Mode: store.DecisionBulk}); err != nil {
		e.t.Fatalf("Approve(%v): %v", fields, err)
	}
}

// approveStored approves fields in the store only, as M4.1 did before
// Approve refused unwritable values, so Apply's own checks are exercised.
func (e *env) approveStored(run int64, bookID int, fields ...string) {
	e.t.Helper()
	ctx := context.Background()
	_, dec, err := e.svc.open(ctx, run, bookID)
	if err != nil {
		e.t.Fatal(err)
	}
	var ps []evidence.Proposal
	for _, p := range dec.Proposals {
		if slices.Contains(fields, p.Field) {
			ps = append(ps, p)
		}
	}
	if err := e.fixes.Decide(ctx, run, bookID, ps, store.ProposalApproved, "felix", store.Basis{Mode: store.DecisionBulk}); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) statuses(run int64, bookID int) map[string]string {
	e.t.Helper()
	rows, err := e.fixes.Proposals(context.Background(), run, bookID)
	if err != nil {
		e.t.Fatal(err)
	}
	out := map[string]string{}
	for _, r := range rows {
		out[r.Proposal.Field] = r.Status
		if r.Reason != "" {
			out[r.Proposal.Field] += "/" + r.Reason
		}
	}
	return out
}

// events lists an operation's states in order.
func (e *env) events(opID int64) []string {
	e.t.Helper()
	rows, err := e.st.Pool.Query(context.Background(), `SELECT state FROM operation_events WHERE operation_id = $1 ORDER BY seq`, opID)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func (e *env) operationIDs() []int64 {
	e.t.Helper()
	rows, err := e.st.Pool.Query(context.Background(), `SELECT id FROM operations ORDER BY id`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, id)
	}
	return out
}

// fieldsOf is a record without last_modified, which every write moves.
func fieldsOf(r evidence.Record) evidence.Record {
	r.LastModified = ""
	return r
}

func TestApprove(t *testing.T) {
	ctx := context.Background()
	e := setup(t)
	run := e.checkBook(2)
	e.approve(run, 2, "title", "pubdate")
	raw, seal, err := e.runs.Evidence(ctx, run, 2)
	if err != nil {
		t.Fatal(err)
	}
	_, dec, err := decide.Open(ctx, raw, seal)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := e.fixes.Proposals(ctx, run, 2)
	if err != nil || len(rows) != 2 {
		t.Fatalf("Proposals = %+v, %v", rows, err)
	}
	for _, r := range rows {
		i := slices.IndexFunc(dec.Proposals, func(p evidence.Proposal) bool { return p.Field == r.Proposal.Field })
		if i < 0 || !reflect.DeepEqual(dec.Proposals[i], r.Proposal) || r.Status != store.ProposalApproved {
			t.Errorf("stored %+v, sealed %+v", r, dec.Proposals)
		}
	}
	if err := e.svc.Approve(ctx, run, 2, []string{"isbn"}, "felix", store.Basis{Mode: store.DecisionBulk}); !errors.Is(err, ErrNoProposal) {
		t.Errorf("approving a field without a proposal: %v", err)
	}
	if err := e.svc.Reject(ctx, run, 2, []string{"title"}, "felix", store.Basis{Mode: store.DecisionBulk}); err != nil {
		t.Fatal(err)
	}
	if got := e.statuses(run, 2)["title"]; got != store.ProposalRejected {
		t.Errorf("title after Reject = %q", got)
	}
	newer := e.checkBook(2)
	if err := e.svc.Approve(ctx, run, 2, []string{"authors"}, "felix", store.Basis{Mode: store.DecisionBulk}); !errors.Is(err, ErrNoProposal) {
		t.Errorf("approving superseded evidence: %v", err)
	}
	e.approve(newer, 2, "authors")

	// Two formats that disagree on the title make the book Tier C, without
	// proposals.
	c := pkgFor(t, e.cal.book(2), sourceSays{})
	c.Formats = append(c.Formats, evidence.FormatEvidence{Format: "MOBI", State: evidence.StateOK, Detected: "mobi",
		Metadata: &evidence.FileMetadata{Title: "Another Title", Authors: []string{"Bea Sample"}, Languages: []string{"en"}, ISBNs: []string{isbn}}})
	tierC := e.check(c)
	if err := e.svc.Approve(ctx, tierC, 2, []string{"authors"}, "felix", store.Basis{Mode: store.DecisionBulk}); !errors.Is(err, ErrNoProposal) {
		t.Errorf("approving a Tier C book: %v", err)
	}
	if err := e.svc.Approve(ctx, 999, 2, []string{"title"}, "felix", store.Basis{Mode: store.DecisionBulk}); !errors.Is(err, ErrNoProposal) {
		t.Errorf("approving a run that doesn't exist: %v", err)
	}
}

func TestApplyWritesOff(t *testing.T) {
	ctx := context.Background()
	e := setup(t)
	run := e.checkBook(2)
	e.approve(run, 2, allFields...)
	if err := e.fixes.SetWritesEnabled(ctx, false, "felix"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Apply(ctx, "felix"); !errors.Is(err, ErrWritesOff) {
		t.Fatalf("Apply with writes off: %v", err)
	}
	if e.cal.setCount() != 0 {
		t.Fatal("set-fields was called")
	}
}

func TestApplyCompletes(t *testing.T) {
	ctx := context.Background()
	e := setup(t)
	run := e.checkBook(2)
	before := evidence.RecordFrom(e.cal.book(2))
	e.approve(run, 2, allFields...)
	sum, err := e.svc.Apply(ctx, "felix")
	if err != nil || sum != (Summary{Applied: 5}) {
		t.Fatalf("Apply = %+v, %v", sum, err)
	}
	b := e.cal.book(2)
	if b.Title != "Lighthouse Signals" || !slices.Equal(b.Authors, []string{"Bea Sample"}) || !slices.Equal(b.Languages, []string{"eng"}) ||
		b.Publisher != "Harbour Press" || b.PubDate != "2001-07-26T00:00:00+00:00" {
		t.Fatalf("book after Apply = %+v", b)
	}
	ops := e.operationIDs()
	if len(ops) != 1 {
		t.Fatalf("operations = %v", ops)
	}
	if got := e.events(ops[0]); !slices.Equal(got, []string{"requested", "writing", "verifying", "completed"}) {
		t.Fatalf("states = %v", got)
	}
	op, err := e.fixes.Operation(ctx, ops[0])
	if err != nil || !reflect.DeepEqual(op.Before, before) || op.After == nil || !reflect.DeepEqual(*op.After, evidence.RecordFrom(b)) ||
		op.Kind != store.OpApply || op.RequestedBy != "felix" {
		t.Fatalf("operation = %+v, %v", op, err)
	}
	for f, s := range e.statuses(run, 2) {
		if s != store.ProposalApplied {
			t.Errorf("%s is %s", f, s)
		}
	}
}

func TestApplyStale(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(e *env, run int64)
		reason string
	}{
		{"sort-only edit", func(e *env, _ int64) { e.cal.edit(2, func(b *library.Book) { s := "Catalogue"; b.TitleSort = &s }) }, "book_changed"},
		{"format replaced, same last_modified", func(e *env, _ int64) { e.cal.edit(2, func(b *library.Book) { b.Formats[0].Size++ }) }, "book_changed"},
		{"newer run", func(e *env, _ int64) { e.checkBook(2) }, "superseded"},
		{"uuid differs", func(e *env, _ int64) { e.cal.edit(2, func(b *library.Book) { b.UUID = "u-other" }) }, "book_changed"},
		{"last_modified a second later", func(e *env, _ int64) {
			e.cal.edit(2, func(b *library.Book) { b.LastModified = b.LastModified.Add(time.Second) })
		}, "book_changed"},
		{"microseconds where the check saw whole seconds", func(e *env, _ int64) {
			e.cal.edit(2, func(b *library.Book) { b.LastModified = b.LastModified.Add(1500 * time.Microsecond) })
		}, "book_changed"},
		{"book gone", func(e *env, _ int64) {
			e.cal.mu.Lock()
			delete(e.cal.books, 2)
			e.cal.mu.Unlock()
		}, "book_gone"},
		{"edited proposal", func(e *env, run int64) {
			if _, err := e.st.Pool.Exec(context.Background(), `UPDATE proposals SET proposal = jsonb_set(proposal, '{proposed}', '["Evil"]')
				WHERE run_id = $1 AND field = 'title'`, run); err != nil {
				e.t.Fatal(err)
			}
		}, "proposal_changed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := setup(t)
			run := e.checkBook(2)
			e.approve(run, 2, allFields...)
			tc.change(e, run)
			sum, err := e.svc.Apply(context.Background(), "felix")
			if err != nil || sum != (Summary{Stale: 5}) {
				t.Fatalf("Apply = %+v, %v", sum, err)
			}
			for f, s := range e.statuses(run, 2) {
				if s != store.ProposalStale+"/"+tc.reason {
					t.Errorf("%s is %s", f, s)
				}
			}
			if e.cal.setCount() != 0 || len(e.operationIDs()) != 0 {
				t.Fatalf("set-fields calls %d, operations %v", e.cal.setCount(), e.operationIDs())
			}
		})
	}
}

// TestApplyLastModified: a book checked right after a write carries
// last_modified with microseconds; once Calibre reloads the library it
// reports the same instant in whole seconds, truncated. Only that isn't a
// change.
func TestApplyLastModified(t *testing.T) {
	for _, tc := range []struct {
		name  string
		now   func(checked time.Time) time.Time
		edit  func(b *library.Book)
		stale bool
	}{
		{"reloaded", func(c time.Time) time.Time { return c.Truncate(time.Second) }, nil, false},
		{"rounded up is a change", func(c time.Time) time.Time { return c.Truncate(time.Second).Add(time.Second) }, nil, true},
		{"a second earlier", func(c time.Time) time.Time { return c.Truncate(time.Second).Add(-time.Second) }, nil, true},
		{"reloaded but a format changed", func(c time.Time) time.Time { return c.Truncate(time.Second) },
			func(b *library.Book) { b.Formats[0].Size++ }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := setup(t)
			e.cal.edit(2, func(b *library.Book) { b.LastModified = b.LastModified.Add(876978 * time.Microsecond) })
			checked := e.cal.book(2).LastModified
			run := e.checkBook(2)
			e.approve(run, 2, allFields...)
			e.cal.edit(2, func(b *library.Book) {
				b.LastModified = tc.now(checked)
				if tc.edit != nil {
					tc.edit(b)
				}
			})
			sum, err := e.svc.Apply(context.Background(), "felix")
			want := Summary{Applied: 5}
			if tc.stale {
				want = Summary{Stale: 5}
			}
			if err != nil || sum != want {
				t.Fatalf("Apply = %+v, %v; want %+v", sum, err, want)
			}
			if tc.stale {
				return
			}
			op, err := e.fixes.Operation(context.Background(), e.operationIDs()[0])
			if err != nil || op.Before.LastModified != tc.now(checked).UTC().Format(time.RFC3339Nano) {
				t.Fatalf("operation before last_modified = %q, %v; want the value Calibre reported", op.Before.LastModified, err)
			}
		})
	}
}

func TestApplyBlocked(t *testing.T) {
	for _, tc := range []struct {
		name   string
		book   func(*library.Book)
		says   sourceSays
		names  []string
		field  string
		reason string
	}{
		{name: "first of the month", says: sourceSays{date: "2001-07-01"}, field: "pubdate", reason: "calibre_alters_value"},
		{name: "current date can't be restored", book: func(b *library.Book) { b.PubDate = "2001-07-31T22:00:00+00:00" },
			field: "pubdate", reason: "calibre_cannot_restore"},
		{name: "case variant of another book's author", says: sourceSays{authors: []string{"bea sample"}}, names: []string{"Bea Sample"},
			field: "authors", reason: "renames_shared_name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := book2()
			if tc.book != nil {
				tc.book(&b)
			}
			e := setup(t, b)
			e.cal.names[fAuthors] = tc.names
			check := e.check
			if tc.says.date != "" || tc.field == "pubdate" {
				// v10 no longer proposes a shifted day, and v11 nothing an
				// undo couldn't restore; Apply must still block both.
				check = e.checkV9
			}
			run := check(pkgFor(t, b, tc.says))
			e.approveStored(run, 2, allFields...)
			sum, err := e.svc.Apply(context.Background(), "felix")
			if err != nil || sum != (Summary{Applied: 4, Blocked: 1}) {
				t.Fatalf("Apply = %+v, %v", sum, err)
			}
			st := e.statuses(run, 2)
			for _, f := range allFields {
				want := store.ProposalApplied
				if f == tc.field {
					want = store.ProposalBlocked + "/" + tc.reason
				}
				if st[f] != want {
					t.Errorf("%s is %s, want %s", f, st[f], want)
				}
			}
			for _, w := range e.cal.writes {
				for _, c := range w {
					if c.Field == tc.field {
						t.Errorf("the blocked field was sent: %s", c.Value)
					}
				}
			}
		})
	}
}

func TestApplyMismatch(t *testing.T) {
	ctx := context.Background()
	e := setup(t)
	run := e.checkBook(2)
	before := evidence.RecordFrom(e.cal.book(2))
	e.approve(run, 2, allFields...)
	e.cal.alterOnce = true
	e.cal.alter = func(c library.Change) library.Change {
		if c.Field == fTitle {
			s, _ := decode[string](c.Value)
			c.Value = jsonValue(s + " (x)")
		}
		return c
	}
	sum, err := e.svc.Apply(ctx, "felix")
	if !errors.Is(err, ErrPaused) || sum != (Summary{Failed: 5}) {
		t.Fatalf("Apply = %+v, %v", sum, err)
	}
	ops := e.operationIDs()
	if got := e.events(ops[0]); !slices.Equal(got, []string{"requested", "writing", "verifying", "restoring", "restoring", "restored"}) {
		t.Fatalf("states = %v", got)
	}
	if got := evidence.RecordFrom(e.cal.book(2)); !reflect.DeepEqual(fieldsOf(got), fieldsOf(before)) {
		t.Fatalf("book after restore = %+v\nbefore %+v", got, before)
	}
	inc, err := e.fixes.Incidents(ctx)
	if err != nil || len(inc) != 1 || inc[0].Reason != "readback_mismatch" || inc[0].OperationID != ops[0] {
		t.Fatalf("incidents = %+v, %v", inc, err)
	}
	for f, s := range e.statuses(run, 2) {
		if !strings.HasPrefix(s, store.ProposalFailed) {
			t.Errorf("%s is %s", f, s)
		}
	}
	sets := e.cal.setCount()
	run2 := e.checkBook(2)
	e.approve(run2, 2, "title")
	if _, err := e.svc.Apply(ctx, "felix"); !errors.Is(err, ErrPaused) {
		t.Fatalf("Apply while paused: %v", err)
	}
	if e.cal.setCount() != sets {
		t.Fatal("set-fields was called while paused")
	}
}

// TestApplyRefusedWrite: a read-only Calibre account refuses the write and
// the book is unchanged: the operation fails as calibre_refused_write with
// no incident, writes aren't paused, and the apply stops before the next
// book, whose fixes stay approved.
func TestApplyRefusedWrite(t *testing.T) {
	for _, refusal := range []error{library.ErrForbidden, library.ErrAuth} {
		t.Run(errorCode(refusal), func(t *testing.T) {
			ctx := context.Background()
			b3 := book2()
			b3.ID, b3.UUID = 3, "u-3"
			e := setup(t, book2(), b3)
			run2, run3 := e.checkBook(2), e.checkBook(3)
			e.approve(run2, 2, allFields...)
			e.approve(run3, 3, "title")
			e.cal.failSet = refusal
			sum, err := e.svc.Apply(ctx, "felix")
			if !errors.Is(err, ErrRefused) || errors.Is(err, ErrPaused) {
				t.Fatalf("Apply = %v, want ErrRefused", err)
			}
			if sum.Failed != len(allFields) || sum.Applied != 0 {
				t.Errorf("summary = %+v", sum)
			}
			ops := e.operationIDs()
			if len(ops) != 1 {
				t.Fatalf("%d operations, want 1: the next book was attempted", len(ops))
			}
			if got := e.events(ops[0]); !slices.Equal(got, []string{"requested", "writing", "failed"}) {
				t.Fatalf("states = %v", got)
			}
			if op, _ := e.fixes.Operation(ctx, ops[0]); op.Detail != "calibre_refused_write" {
				t.Errorf("detail = %q", op.Detail)
			}
			if e.cal.setCount() != 1 {
				t.Errorf("set-fields calls = %d, want 1", e.cal.setCount())
			}
			if inc, _ := e.fixes.Incidents(ctx); len(inc) != 0 {
				t.Errorf("incidents = %+v, want none", inc)
			}
			if paused, err := e.fixes.Paused(ctx); err != nil || paused {
				t.Errorf("Paused = %v, %v; want writes not paused", paused, err)
			}
			for f, st := range e.statuses(run2, 2) {
				if st != store.ProposalFailed+"/calibre_refused_write" {
					t.Errorf("book 2 %s = %s", f, st)
				}
			}
			if st := e.statuses(run3, 3)["title"]; st != store.ProposalApproved {
				t.Errorf("book 3 title = %s, want still approved", st)
			}
			if got := applyMessage(err, false); !strings.Contains(got, "Calibre refused the write") || !strings.Contains(got, "write access") {
				t.Errorf("apply message = %q", got)
			}
		})
	}
}

// TestApplyRefusedWriteThatChangedTheBook: a write refused after part of it
// landed takes the mismatch path as before: incident, restore, paused.
func TestApplyRefusedWriteThatChangedTheBook(t *testing.T) {
	ctx := context.Background()
	e := setup(t)
	run := e.checkBook(2)
	e.approve(run, 2, allFields...)
	e.cal.errAfter = library.ErrForbidden
	if _, err := e.svc.Apply(ctx, "felix"); !errors.Is(err, ErrPaused) {
		t.Fatalf("Apply = %v, want ErrPaused", err)
	}
	ops := e.operationIDs()
	if got := e.events(ops[0]); len(got) < 3 || got[2] != "restoring" {
		t.Fatalf("states = %v, want the mismatch path", got)
	}
	inc, _ := e.fixes.Incidents(ctx)
	if len(inc) != 1 || inc[0].Reason != "write_error" || !strings.Contains(inc[0].Detail, "forbidden") {
		t.Fatalf("incidents = %+v", inc)
	}
}

func TestApplyRestoreFails(t *testing.T) {
	ctx := context.Background()
	e := setup(t)
	run := e.checkBook(2)
	e.approve(run, 2, "title")
	e.cal.alter = func(c library.Change) library.Change {
		if c.Field == fTitle {
			s, _ := decode[string](c.Value)
			c.Value = jsonValue(s + " (x)")
		}
		return c
	}
	if _, err := e.svc.Apply(ctx, "felix"); !errors.Is(err, ErrPaused) {
		t.Fatalf("Apply = %v", err)
	}
	ops := e.operationIDs()
	if got := e.events(ops[0]); !slices.Equal(got, []string{"requested", "writing", "verifying", "restoring", "restoring", "failed"}) {
		t.Fatalf("states = %v", got)
	}
	if inc, _ := e.fixes.Incidents(ctx); len(inc) != 1 {
		t.Fatalf("incidents = %+v", inc)
	}
}

// TestApplyStopsBetweenBooks: the switch is read again before each book, so
// turning it off mid-batch writes nothing more.
func TestApplyStopsBetweenBooks(t *testing.T) {
	ctx := context.Background()
	b3 := book2()
	b3.ID, b3.UUID = 3, "u-3"
	e := setup(t, book2(), b3)
	run2, run3 := e.checkBook(2), e.checkBook(3)
	e.approve(run2, 2, allFields...)
	e.approve(run3, 3, allFields...)
	e.cal.afterSet = func() {
		if err := e.fixes.SetWritesEnabled(ctx, false, "felix"); err != nil {
			t.Error(err)
		}
	}
	sum, err := e.svc.Apply(ctx, "felix")
	if !errors.Is(err, ErrWritesOff) || sum != (Summary{Applied: 5}) {
		t.Fatalf("Apply = %+v, %v", sum, err)
	}
	if e.cal.setCount() != 1 || e.statuses(run3, 3)["title"] != store.ProposalApproved {
		t.Fatalf("book 3: set-fields calls %d, statuses %v", e.cal.setCount(), e.statuses(run3, 3))
	}
}

// TestApplyISBN: an ISBN fix rewrites the identifiers map with only the ISBN
// changed, and undo restores the whole map.
func TestApplyISBN(t *testing.T) {
	ctx := context.Background()
	b := book2()
	b.Identifiers = map[string]string{"isbn": "9780000000026", "amazon": "B0X"}
	e := setup(t, b)
	run := e.checkBook(2)
	e.approve(run, 2, "isbn")
	if sum, err := e.svc.Apply(ctx, "felix"); err != nil || sum != (Summary{Applied: 1}) {
		t.Fatalf("Apply = %+v, %v", sum, err)
	}
	if got := e.cal.book(2).Identifiers; !maps.Equal(got, map[string]string{"isbn": isbn, "amazon": "B0X"}) {
		t.Fatalf("identifiers after Apply = %v", got)
	}
	ops := e.operationIDs()
	if _, err := e.svc.Undo(ctx, ops[0], "felix"); err != nil {
		t.Fatal(err)
	}
	if got := e.cal.book(2).Identifiers; !maps.Equal(got, b.Identifiers) {
		t.Fatalf("identifiers after Undo = %v", got)
	}
}

// TestApplyRestoreRefused: a restore that Calibre refuses for good ends
// failed, so the incident can be acknowledged instead of blocking every
// writer session.
func TestApplyRestoreRefused(t *testing.T) {
	ctx := context.Background()
	e := setup(t)
	run := e.checkBook(2)
	e.approve(run, 2, "title")
	e.cal.alterOnce = true
	e.cal.alter = func(c library.Change) library.Change {
		c.Value = jsonValue("Altered")
		return c
	}
	e.cal.failSet, e.cal.failSkip = library.ErrForbidden, 1
	if _, err := e.svc.Apply(ctx, "felix"); !errors.Is(err, ErrPaused) {
		t.Fatalf("Apply = %v", err)
	}
	ops := e.operationIDs()
	op, _ := e.fixes.Operation(ctx, ops[0])
	if op.State != store.OpFailed || !strings.Contains(op.Detail, "forbidden") {
		t.Fatalf("operation = %+v", op)
	}
	if err := e.svc.Recover(ctx); err != nil {
		t.Fatalf("Recover after a refused restore = %v", err)
	}
	inc, _ := e.fixes.Incidents(ctx)
	if len(inc) != 1 {
		t.Fatalf("incidents = %+v", inc)
	}
	if err := e.fixes.Acknowledge(ctx, inc[0].ID, "felix"); err != nil {
		t.Fatal(err)
	}
}

// TestCalibreTimeout: a Calibre that never answers releases the writer lock
// after the call timeout instead of holding it forever.
func TestCalibreTimeout(t *testing.T) {
	ctx := context.Background()
	e := setup(t)
	run := e.checkBook(2)
	e.approve(run, 2, "title")
	e.svc.readTimeout, e.svc.writeTimeout = 50*time.Millisecond, 50*time.Millisecond
	e.cal.hang = true
	done := make(chan error, 1)
	go func() {
		_, err := e.svc.Apply(ctx, "felix")
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Apply = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Apply still waiting on a hung Calibre")
	}
	if _, err := e.svc.Apply(ctx, "felix"); errors.Is(err, ErrBusy) {
		t.Fatal("the writer lock was kept")
	}
	if got := e.statuses(run, 2)["title"]; got != store.ProposalApproved {
		t.Fatalf("title is %s", got)
	}
}

// TestApplySortProposals writes Calibre's own sorts, reads them back and
// undoes them alone.
func TestApplySortProposals(t *testing.T) {
	ctx := context.Background()
	b := book2()
	b.Title, b.Authors, b.Languages = "Lighthouse Signals", []string{"Bea Sample"}, []string{"eng"}
	wrong := "Signals"
	b.TitleSort, b.AuthorSort, b.AuthorSortMap = &wrong, "Road, The", map[string]string{"Bea Sample": "Sample, Bea"}
	e := setup(t, b)
	run := e.checkBook(2)
	e.approve(run, 2, "title_sort", "author_sort")
	sum, err := e.svc.Apply(ctx, "felix")
	if err != nil || sum.Applied != 2 {
		t.Fatalf("Apply = %+v, %v", sum, err)
	}
	got := e.cal.book(2)
	if got.TitleSort == nil || *got.TitleSort != "Lighthouse Signals" || got.AuthorSort != "Sample, Bea" || got.Title != "Lighthouse Signals" {
		t.Fatalf("book after apply = %+v", got)
	}
	ops := e.operationIDs()
	if _, err := e.svc.Undo(ctx, ops[len(ops)-1], "felix"); err != nil {
		t.Fatal(err)
	}
	got = e.cal.book(2)
	if *got.TitleSort != wrong || got.AuthorSort != "Road, The" || got.Title != "Lighthouse Signals" {
		t.Fatalf("book after undo = %+v", got)
	}
}

// TestApplyBeforeMapCapture: an approval from a check sealed before the
// authors table was read still applies once reads carry the table; its
// proposals don't depend on it (ADR-028).
func TestApplyBeforeMapCapture(t *testing.T) {
	ctx := context.Background()
	e := setup(t)
	run := e.checkBook(2)
	e.approve(run, 2, "title")
	e.cal.edit(2, func(b *library.Book) { b.AuthorSortMap = map[string]string{"Unknown": "Unknown"} })
	if sum, err := e.svc.Apply(ctx, "felix"); err != nil || sum.Applied != 1 || sum.Stale != 0 {
		t.Fatalf("Apply = %+v, %v", sum, err)
	}
}

// TestApplyBeforeSeriesCapture: an approval from a check sealed before
// bookwarden read the series (schema 4) still applies when the live book has
// one.
func TestApplyBeforeSeriesCapture(t *testing.T) {
	ctx := context.Background()
	e := setup(t)
	run := e.checkBook(2)
	e.approve(run, 2, "title")
	e.cal.edit(2, func(b *library.Book) { b.Series, b.SeriesIndex = "The Spike Saga", "2.5" })
	if sum, err := e.svc.Apply(ctx, "felix"); err != nil || sum.Applied != 1 || sum.Stale != 0 {
		t.Fatalf("Apply = %+v, %v", sum, err)
	}
}

// TestApproveVerifiesItsOwnPackage pins ADR-038: pages may read a checked
// run's projection, but a write opens and verifies the package it acts on.
func TestApproveVerifiesItsOwnPackage(t *testing.T) {
	ctx := context.Background()
	e := setup(t)
	run := e.checkBook(2)
	reports := report.New(store.NewReportRepo(e.st))
	if check, err := reports.CheckSeals(ctx, run); err != nil || check.Broken != 0 {
		t.Fatalf("CheckSeals = %+v, %v", check, err)
	}
	if _, err := e.st.Pool.Exec(ctx, `UPDATE evidence SET package=jsonb_set(package,'{calibre_record,title}','"Edited"') WHERE run_id=$1`, run); err != nil {
		t.Fatal(err)
	}
	// The page still reads the passing check, until the next one...
	if _, err := reports.Fixes(ctx, run, report.FixFilter{}, nil); err != nil {
		t.Fatalf("page between checks: %v", err)
	}
	// ...while every write path fails on the edited package.
	if err := e.svc.Approve(ctx, run, 2, []string{"title"}, "felix", store.Basis{Mode: store.DecisionBulk}); err == nil {
		t.Fatal("Approve acted on an edited package")
	}
	if _, _, err := e.svc.open(ctx, run, 2); err == nil {
		t.Fatal("the writer opened an edited package")
	}
	if check, err := reports.CheckSeals(ctx, run); err != nil || check.Broken != 1 || check.FirstBroken != 2 {
		t.Fatalf("the next check = %+v, %v", check, err)
	}
}

// TestApplyRefusedWriteNeedsProof: a refusal counts as harmless only when a
// fresh read proves every field unchanged. A read that fails, or a book
// whose last field changed, takes the mismatch path.
func TestApplyRefusedWriteNeedsProof(t *testing.T) {
	for _, tc := range []struct {
		name  string
		after func(f *fakeCalibre)
	}{
		{"the read after the refusal fails", func(f *fakeCalibre) { f.failRead = library.ErrUnreachable }},
		{"only the last field changed", func(f *fakeCalibre) {
			b := f.books[2]
			b.PubDate = "1999-01-02T00:00:00+00:00"
			f.books[2] = b
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			e := setup(t)
			run := e.checkBook(2)
			e.approve(run, 2, allFields...)
			e.cal.failSet = library.ErrForbidden
			// The apply's own read (the stale check) comes first.
			e.cal.afterRead = func(n int) {
				if n == 1 {
					e.cal.mu.Lock()
					tc.after(e.cal)
					e.cal.mu.Unlock()
				}
			}
			// The mismatch path: restored and paused, or, when Calibre can't
			// be read, left restoring for the next session; an incident
			// either way.
			if _, err := e.svc.Apply(ctx, "felix"); err == nil || errors.Is(err, ErrRefused) {
				t.Fatalf("Apply = %v, want the mismatch path", err)
			}
			if inc, _ := e.fixes.Incidents(ctx); len(inc) != 1 {
				t.Fatalf("incidents = %+v, want one", inc)
			}
		})
	}
}
