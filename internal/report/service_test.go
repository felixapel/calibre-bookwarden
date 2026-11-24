package report

import (
	"context"
	"errors"
	"fmt"
	"math/bits"
	"slices"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/duplicates"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

type fakeStore struct {
	run     verify.Run
	keys    []duplicates.KeyRow
	covers  []duplicates.Cover
	books   []Book
	corrupt int
	closed  int
	opened  int
	loads   int
	// rowsCalls counts whole-run DuplicateRows reads.
	rowsCalls int
}
type fakeSnapshot struct{ st *fakeStore }

func (f *fakeStore) Snapshot(ctx context.Context, id int64) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if id != 7 {
		return nil, verify.ErrNoSuchRun
	}
	f.opened++
	return &fakeSnapshot{f}, nil
}
func (f *fakeSnapshot) Run() verify.Run {
	if f.st.run.ID != 0 {
		return f.st.run
	}
	return verify.Run{ID: 7}
}
func (f *fakeSnapshot) Book(ctx context.Context, id int) (Book, error) {
	if err := ctx.Err(); err != nil {
		return Book{}, err
	}
	f.st.loads++
	if id == f.st.corrupt {
		return Book{}, ErrIntegrity
	}
	for _, b := range f.st.books {
		if b.Ref.ID == id {
			return b, nil
		}
	}
	return Book{}, ErrNoSuchBook
}
func (f *fakeSnapshot) NextBook(ctx context.Context, after int) (Book, bool, error) {
	for _, b := range f.st.books {
		if b.Ref.ID > after {
			got, err := f.Book(ctx, b.Ref.ID)
			if err != nil {
				err = fmt.Errorf("book %d: %w", b.Ref.ID, err)
				got.Ref.ID = b.Ref.ID
			}
			return got, true, err
		}
	}
	return Book{}, false, ctx.Err()
}
func (f *fakeSnapshot) Close(context.Context) error { f.st.closed++; return nil }

// DuplicateRows returns the keys shared by two or more books, as the store does.
func (f *fakeSnapshot) DuplicateRows(context.Context) ([]duplicates.KeyRow, []duplicates.Cover, error) {
	f.st.rowsCalls++
	var out []duplicates.KeyRow
	for _, k := range f.st.keys {
		if slices.ContainsFunc(f.st.keys, func(o duplicates.KeyRow) bool {
			return o.Kind == k.Kind && o.Value == k.Value && o.BookID != k.BookID
		}) {
			out = append(out, k)
		}
	}
	return out, f.st.covers, nil
}
func (f *fakeSnapshot) BookKeys(_ context.Context, id int) ([]duplicates.Key, *duplicates.Cover, error) {
	var keys []duplicates.Key
	for _, k := range f.st.keys {
		if k.BookID == id {
			keys = append(keys, k.Key)
		}
	}
	for _, c := range f.st.covers {
		if c.BookID == id {
			return keys, &c, nil
		}
	}
	return keys, nil, nil
}
func sampleBook(id, n int) Book {
	p := evidence.Package{Calibre: evidence.Record{Title: strings.Repeat("ñ", 600)}}
	for i := range n {
		p.Findings = append(p.Findings, evidence.Finding{Code: "file_missing", Format: fmt.Sprintf("F%03d", i)})
	}
	return Book{Ref: library.BookRef{ID: id, UUID: fmt.Sprint(id)}, State: verify.BookChecked, Package: &p, FindingsVersion: 2, Findings: p.Findings}
}

func TestReportPagination(t *testing.T) {
	ctx := context.Background()
	st := &fakeStore{books: []Book{sampleBook(2, 62), sampleBook(4, 40)}}
	s := New(st)
	var all []string
	var cursor *Cursor
	for page := 0; page < 3; page++ {
		got, err := s.Findings(ctx, 7, Filter{}, cursor)
		if err != nil {
			t.Fatal(err)
		}
		want := 50
		if page == 2 {
			want = 2
		}
		if len(got.Rows) != want {
			t.Fatalf("page %d rows=%d", page, len(got.Rows))
		}
		for _, r := range got.Rows {
			if len([]rune(r.Title)) != 512 {
				t.Fatal("uncapped title")
			}
			all = append(all, fmt.Sprintf("%d/%s", r.BookID, r.Finding.Format))
		}
		if page < 2 {
			if got.Next == nil {
				t.Fatal("no next cursor")
			}
			last := got.Rows[49]
			if got.Next.BookID != last.BookID || (page == 0 && got.Next.Ordinal != 49) || (page == 1 && got.Next.Ordinal != 37) {
				t.Fatalf("cursor=%+v", got.Next)
			}
		} else if got.Next != nil {
			t.Fatal("unexpected final cursor")
		}
		cursor = got.Next
	}
	if len(all) != 102 || all[50] != "2/F050" || all[62] != "4/F000" {
		t.Fatalf("boundary=%v", all)
	}
	for _, c := range []*Cursor{{RunID: 8, BookID: 2, Ordinal: 0}, {RunID: 7, BookID: 2, Ordinal: 62}, {RunID: 7, BookID: 3, Ordinal: 0}, {RunID: 7, BookID: 2, Ordinal: -1}} {
		if _, err := s.Findings(ctx, 7, Filter{}, c); !errors.Is(err, ErrInvalidCursor) {
			t.Fatalf("cursor %+v=%v", c, err)
		}
	}
	if _, err := s.Findings(ctx, 7, Filter{Code: "invented"}, nil); !errors.Is(err, ErrInvalidFilter) {
		t.Fatalf("filter=%v", err)
	}
	if got, err := s.Findings(ctx, 7, Filter{Code: "no_formats"}, nil); err != nil || len(got.Rows) != 0 {
		t.Fatalf("empty=%+v/%v", got, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.Findings(cancelled, 7, Filter{}, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
	if st.closed != st.opened || st.opened == 0 {
		t.Fatalf("snapshots opened=%d closed=%d", st.opened, st.closed)
	}
}

func TestFilteredProjectionCorruption(t *testing.T) {
	for _, corrupt := range []int{1, 3} {
		for _, cursor := range []*Cursor{nil, {RunID: 7, BookID: 2, Ordinal: 0}} {
			st := &fakeStore{books: []Book{sampleBook(1, 1), sampleBook(2, 62), sampleBook(3, 1)}, corrupt: corrupt}
			if _, err := New(st).Findings(context.Background(), 7, Filter{Code: "no_formats"}, cursor); !errors.Is(err, ErrIntegrity) {
				t.Fatalf("hidden corruption=%v", err)
			}
			if st.closed != 1 {
				t.Fatal("leaked snapshot")
			}
		}
	}
}

func TestReportBooks(t *testing.T) {
	st := &fakeStore{}
	for id := 1; id <= 52; id++ {
		b := sampleBook(id, 0)
		if id%2 == 0 {
			b.State = verify.BookFailed
			b.Package = nil
			b.Reason = "calibre_error"
		}
		st.books = append(st.books, b)
	}
	s := New(st)
	a, err := s.Books(context.Background(), 7, 0)
	if err != nil || len(a.Rows) != 50 || a.NextBookID != 50 {
		t.Fatalf("first=%+v/%v", a, err)
	}
	b, err := s.Books(context.Background(), 7, a.NextBookID)
	if err != nil || len(b.Rows) != 2 || b.Rows[0].Ref.ID != 51 || b.NextBookID != 0 {
		t.Fatalf("second=%+v/%v", b, err)
	}
	if a.Rows[1].State != verify.BookFailed || a.Rows[0].Title == "" {
		t.Fatal("lost clean or operational book")
	}
}

func TestReportWalk(t *testing.T) {
	st := &fakeStore{books: []Book{sampleBook(1, 3), sampleBook(2, 2)}}
	s := New(st)
	var n int
	err := s.Walk(context.Background(), 7, Filter{Format: "F001"}, func(Row) error { n++; return nil })
	if err != nil || n != 2 {
		t.Fatalf("walk=%d/%v", n, err)
	}
	stop := errors.New("callback")
	n = 0
	err = s.Walk(context.Background(), 7, Filter{}, func(Row) error { n++; return stop })
	if !errors.Is(err, stop) || n != 1 {
		t.Fatalf("stop=%d/%v", n, err)
	}
	if _, err = s.Book(context.Background(), 7, 99); !errors.Is(err, ErrNoSuchBook) {
		t.Fatalf("book=%v", err)
	}
	if _, err = s.Book(context.Background(), 99, 1); !errors.Is(err, verify.ErrNoSuchRun) {
		t.Fatalf("run=%v", err)
	}
}

// BookGroupRows returns every row sharing a kind and value with the book's.
func (f *fakeSnapshot) BookGroupRows(_ context.Context, id int) ([]duplicates.KeyRow, error) {
	var out []duplicates.KeyRow
	for _, k := range f.st.keys {
		if slices.ContainsFunc(f.st.keys, func(o duplicates.KeyRow) bool { return o.BookID == id && o.Kind == k.Kind && o.Value == k.Value }) {
			out = append(out, k)
		}
	}
	return out, nil
}

// NearCovers returns the other comparable covers within MaxCoverDistance.
func (f *fakeSnapshot) NearCovers(_ context.Context, id int, h uint64) ([]duplicates.Cover, error) {
	var out []duplicates.Cover
	for _, c := range f.st.covers {
		if c.BookID != id && bits.OnesCount64(c.Hash^h) <= duplicates.MaxCoverDistance {
			out = append(out, c)
		}
	}
	return out, nil
}

// EachBook walks one snapshot and loads each book once.
func TestEachBook(t *testing.T) {
	st := &fakeStore{books: []Book{sampleBook(2, 1), sampleBook(4, 1), sampleBook(9, 1)}}
	var ids []int
	err := New(st).EachBook(context.Background(), 7, func(b Book) error {
		ids = append(ids, b.Ref.ID)
		return nil
	})
	if err != nil || !slices.Equal(ids, []int{2, 4, 9}) {
		t.Fatalf("visited %v, %v", ids, err)
	}
	if st.loads != 3 || st.opened != 1 || st.closed != 1 {
		t.Fatalf("loads %d, snapshots %d/%d", st.loads, st.opened, st.closed)
	}
	st.corrupt = 4
	if err := New(st).EachBook(context.Background(), 7, func(Book) error { return nil }); !errors.Is(err, ErrIntegrity) || !strings.Contains(err.Error(), "book 4") {
		t.Fatalf("a broken seal: %v", err)
	}
}
