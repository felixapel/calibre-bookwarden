package fix

import (
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/store"
)

// Exports for the end-to-end test in package fix_test.

// FakeCalibre is this package's in-memory content server.
type FakeCalibre = fakeCalibre

func NewFakeCalibre(books ...library.Book) *FakeCalibre { return newFake(books...) }

func (f *fakeCalibre) Book(id int) library.Book { return f.book(id) }

// Book2 is fixture book 2.
func Book2() library.Book { return book2() }

// SealBook2 stores a completed one-book run for b with the evidence the
// fixture sources agree on, and returns its id.
func SealBook2(t *testing.T, st *store.Store, b library.Book) int64 {
	t.Helper()
	e := &env{t: t, st: st, fixes: store.NewFixRepo(st), runs: store.NewRunRepo(st)}
	return e.check(pkgFor(t, b, sourceSays{}))
}
