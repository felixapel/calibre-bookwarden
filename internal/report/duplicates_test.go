package report

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/duplicates"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

const (
	dupISBN  = "9780000000002"
	dupCover = 0x0f0f_0f0f_0f0f_0f0f
)

// dupBook is a checked book whose Calibre record carries isbn, and whose
// cover is comparable when hash is non-zero.
func dupBook(id int, title, isbn string, hash uint64) Book {
	p := evidence.Package{Calibre: evidence.Record{Title: title, Authors: []string{"Ada Example"}, Identifiers: map[string]string{"isbn": isbn}}}
	if hash != 0 {
		p.Cover = &evidence.CoverEvidence{State: evidence.StateOK, Width: 600, Height: 900, DHash: fmt.Sprintf("%016x", hash), Flatness: 400}
	}
	return Book{Ref: library.BookRef{ID: id}, State: verify.BookChecked, Package: &p}
}

// dupStore stores the books and the projection FinishBook would write.
func dupStore(books ...Book) *fakeStore {
	st := &fakeStore{run: verify.Run{ID: 7, Status: verify.RunCompleted, DuplicatesVersion: duplicates.Version}, books: books}
	for _, b := range books {
		for _, k := range duplicates.Keys(*b.Package) {
			st.keys = append(st.keys, duplicates.KeyRow{BookID: b.Ref.ID, Key: k})
		}
		if h, compared, ok := duplicates.CoverHash(b.Package.Cover); ok && compared {
			st.covers = append(st.covers, duplicates.Cover{BookID: b.Ref.ID, Hash: h})
		}
	}
	return st
}

func memberIDs(g DuplicateGroup) []int {
	var ids []int
	for _, m := range g.Members {
		ids = append(ids, m.BookID)
	}
	return ids
}

func TestDuplicates(t *testing.T) {
	ctx := context.Background()
	st := dupStore(
		dupBook(1, "The Clockwork Garden", dupISBN, dupCover),
		dupBook(2, "Lighthouse <Signals>", "9780000000019", 0),
		dupBook(9, "The Clockwork Garden", dupISBN, dupCover^0xf),
	)
	page, err := New(st).Duplicates(ctx, 7, DuplicateFilter{Page: 1})
	if err != nil {
		t.Fatal(err)
	}
	if page.Unavailable != "" || page.Partial || page.More || page.Page != 1 {
		t.Fatalf("page %+v", page)
	}
	if want := map[string]int{duplicates.KindISBN: 1, duplicates.KindTitleAuthor: 1, duplicates.KindCover: 1}; !mapsEqual(page.Counts, want) {
		t.Fatalf("counts %v", page.Counts)
	}
	var kinds []string
	for _, g := range page.Groups {
		kinds = append(kinds, g.Kind)
		if !slices.Equal(memberIDs(g), []int{1, 9}) || g.More != 0 || g.Members[0].Title != "The Clockwork Garden" {
			t.Errorf("%s group %+v", g.Kind, g)
		}
	}
	if !slices.Equal(kinds, []string{duplicates.KindISBN, duplicates.KindTitleAuthor, duplicates.KindCover}) {
		t.Fatalf("kinds %v", kinds)
	}
	if g := page.Groups[0]; g.Label != dupISBN || g.Members[0].Detail != "Calibre" {
		t.Errorf("isbn group %+v", g)
	}
	if g := page.Groups[1]; g.Label != "The Clockwork Garden — Ada Example" {
		t.Errorf("title group label %q", g.Label)
	}

	filtered, err := New(st).Duplicates(ctx, 7, DuplicateFilter{Kind: duplicates.KindCover, Page: 1})
	if err != nil || len(filtered.Groups) != 1 || filtered.Groups[0].Kind != duplicates.KindCover || filtered.Counts[duplicates.KindISBN] != 1 {
		t.Fatalf("cover filter %+v, %v", filtered, err)
	}
	if empty, err := New(st).Duplicates(ctx, 7, DuplicateFilter{Page: 2}); err != nil || len(empty.Groups) != 0 || empty.More {
		t.Fatalf("page 2 %+v, %v", empty, err)
	}
}

func mapsEqual(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func TestDuplicatesPaging(t *testing.T) {
	var books []Book
	for i := range 30 {
		isbn := fmt.Sprintf("97800000%04d", i)
		isbn += fmt.Sprint(isbnCheck(isbn))
		books = append(books, dupBook(2*i+1, fmt.Sprintf("A%d", i), isbn, 0), dupBook(2*i+2, fmt.Sprintf("B%d", i), isbn, 0))
	}
	s := New(dupStore(books...))
	first, err := s.Duplicates(context.Background(), 7, DuplicateFilter{Page: 1})
	if err != nil || len(first.Groups) != 25 || !first.More || first.Counts[duplicates.KindISBN] != 30 {
		t.Fatalf("page 1: %d groups, more %v, %v", len(first.Groups), first.More, err)
	}
	second, err := s.Duplicates(context.Background(), 7, DuplicateFilter{Page: 2})
	if err != nil || len(second.Groups) != 5 || second.More || !slices.Equal(memberIDs(second.Groups[0]), []int{51, 52}) {
		t.Fatalf("page 2: %+v, %v", second, err)
	}
}

// isbnCheck is the ISBN-13 check digit of 12 digits.
func isbnCheck(twelve string) int {
	sum := 0
	for i, c := range twelve {
		d := int(c - '0')
		if i%2 == 1 {
			d *= 3
		}
		sum += d
	}
	return (10 - sum%10) % 10
}

func TestDuplicatesBigGroup(t *testing.T) {
	var books []Book
	for id := 1; id <= 300; id++ {
		books = append(books, dupBook(id, fmt.Sprintf("Title %d", id), dupISBN, 0))
	}
	st := dupStore(books...)
	page, err := New(st).Duplicates(context.Background(), 7, DuplicateFilter{Kind: duplicates.KindISBN, Page: 1})
	if err != nil || len(page.Groups) != 1 {
		t.Fatalf("%+v, %v", page, err)
	}
	if g := page.Groups[0]; !slices.Equal(memberIDs(g), []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}) || g.More != 290 {
		t.Fatalf("group shows %v, more %d", memberIDs(g), g.More)
	}
	if st.loads != 10 {
		t.Fatalf("loaded %d packages, want 10", st.loads)
	}
}

func TestDuplicatesUnavailable(t *testing.T) {
	for _, tt := range []struct {
		name        string
		run         verify.Run
		unavailable string
		partial     bool
	}{
		{"one book", verify.Run{ID: 7, Status: verify.RunCompleted, Scope: verify.Scope{BookID: 1}, DuplicatesVersion: 1}, "single_book", false},
		{"before M5.2", verify.Run{ID: 7, Status: verify.RunCompleted}, "predates", false},
		{"later rules", verify.Run{ID: 7, Status: verify.RunCompleted, DuplicatesVersion: 2}, "unknown_version", false},
		{"cancelled", verify.Run{ID: 7, Status: verify.RunCancelled, DuplicatesVersion: 1}, "", true},
	} {
		st := dupStore(dupBook(1, "T", dupISBN, 0), dupBook(9, "T", dupISBN, 0))
		st.run = tt.run
		s := New(st)
		page, err := s.Duplicates(context.Background(), 7, DuplicateFilter{Page: 1})
		if err != nil || page.Unavailable != tt.unavailable || page.Partial != tt.partial || (tt.unavailable != "") != (len(page.Groups) == 0) {
			t.Errorf("%s: %+v, %v", tt.name, page, err)
		}
		book, err := s.BookDuplicates(context.Background(), 7, 1)
		if err != nil || book.Unavailable != tt.unavailable || book.Partial != tt.partial {
			t.Errorf("%s: book %+v, %v", tt.name, book, err)
		}
	}
}

func TestDuplicatesIntegrity(t *testing.T) {
	ctx := context.Background()
	// A stored key the member's package doesn't produce.
	st := dupStore(dupBook(1, "T", dupISBN, 0), dupBook(9, "T", dupISBN, 0), dupBook(3, "Other", "9780000000019", 0))
	st.keys = append(st.keys, duplicates.KeyRow{BookID: 3, Key: duplicates.Key{Kind: duplicates.KindISBN, Value: dupISBN, Detail: "Calibre"}})
	if _, err := New(st).Duplicates(ctx, 7, DuplicateFilter{Page: 1}); !errors.Is(err, ErrIntegrity) {
		t.Errorf("an added key: %v", err)
	}
	// A stored cover hash that differs from the sealed one.
	st = dupStore(dupBook(1, "A", dupISBN, dupCover), dupBook(9, "B", "9780000000019", dupCover))
	st.covers[1].Hash ^= 1
	if _, err := New(st).Duplicates(ctx, 7, DuplicateFilter{Page: 1}); !errors.Is(err, ErrIntegrity) {
		t.Errorf("an edited cover hash: %v", err)
	}
	// A member that wasn't checked.
	st = dupStore(dupBook(1, "T", dupISBN, 0), dupBook(9, "T", dupISBN, 0))
	st.books[1] = Book{Ref: library.BookRef{ID: 9}, State: verify.BookFailed}
	if _, err := New(st).Duplicates(ctx, 7, DuplicateFilter{Page: 1}); !errors.Is(err, ErrIntegrity) {
		t.Errorf("an unchecked member: %v", err)
	}
}

func TestBookDuplicates(t *testing.T) {
	ctx := context.Background()
	st := dupStore(
		dupBook(1, "The Clockwork Garden", dupISBN, dupCover),
		dupBook(2, "Lighthouse Signals", "9780000000019", 0),
		dupBook(9, "The Clockwork Garden", dupISBN, dupCover),
	)
	s := New(st)
	page, err := s.BookDuplicates(ctx, 7, 9)
	if err != nil || len(page.Groups) != 3 {
		t.Fatalf("%+v, %v", page, err)
	}
	for _, g := range page.Groups {
		if !slices.Equal(memberIDs(g), []int{1}) {
			t.Errorf("%s group lists %v, want the other book only", g.Kind, memberIDs(g))
		}
	}
	if none, err := s.BookDuplicates(ctx, 7, 2); err != nil || len(none.Groups) != 0 {
		t.Fatalf("book 2: %+v, %v", none, err)
	}
	if _, err := s.BookDuplicates(ctx, 7, 404); !errors.Is(err, ErrNoSuchBook) {
		t.Fatalf("missing book: %v", err)
	}

	for name, tamper := range map[string]func(*fakeStore){
		"key removed": func(st *fakeStore) {
			st.keys = slices.DeleteFunc(st.keys, func(k duplicates.KeyRow) bool { return k.BookID == 2 })
		},
		"key added": func(st *fakeStore) {
			st.keys = append(st.keys, duplicates.KeyRow{BookID: 2, Key: duplicates.Key{Kind: duplicates.KindFile, Value: strings.Repeat("a", 64)}})
		},
		"cover dropped": func(st *fakeStore) { st.covers = st.covers[:1] },
	} {
		st := dupStore(dupBook(1, "A", dupISBN, dupCover), dupBook(2, "B", "9780000000019", dupCover))
		tamper(st)
		if _, err := New(st).BookDuplicates(ctx, 7, 2); !errors.Is(err, ErrIntegrity) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestValidateDuplicateFilter(t *testing.T) {
	for _, tt := range []struct {
		f  DuplicateFilter
		ok bool
	}{
		{DuplicateFilter{Page: 1}, true},
		{DuplicateFilter{Kind: duplicates.KindCover, Page: 10000}, true},
		{DuplicateFilter{Page: 0}, false},
		{DuplicateFilter{Page: 10001}, false},
		{DuplicateFilter{Kind: "bogus", Page: 1}, false},
	} {
		if err := ValidateDuplicateFilter(tt.f); (err == nil) != tt.ok {
			t.Errorf("%+v: %v", tt.f, err)
		}
	}
}

// The Book page reads only the book's own groups: no whole-run read, and
// covers within distance 4 of this book's (not the whole chain).
func TestBookDuplicatesNarrow(t *testing.T) {
	st := dupStore(
		dupBook(1, "A", dupISBN, dupCover),
		dupBook(2, "B", dupISBN, dupCover^0xf),
		dupBook(3, "C", "9780000000019", dupCover^0xff),
	)
	page, err := New(st).BookDuplicates(context.Background(), 7, 1)
	if err != nil {
		t.Fatal(err)
	}
	if st.rowsCalls != 0 {
		t.Errorf("the Book page read the whole run's rows %d times", st.rowsCalls)
	}
	var got []string
	for _, g := range page.Groups {
		got = append(got, fmt.Sprintf("%s %v", g.Kind, memberIDs(g)))
	}
	if want := []string{"isbn [2]", "cover [2]"}; !slices.Equal(got, want) {
		t.Errorf("groups %v, want %v", got, want)
	}
}
