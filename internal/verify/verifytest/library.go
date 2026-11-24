// Package verifytest holds fakes for testing code that runs checks.
package verifytest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/library"
)

// FakeBook is one book in a FakeLibrary. Files holds each format's content
// by name; its length may differ from the format's Size. Status forces
// ExportNotInCalibre or ExportMissingOnDisk for a format.
type FakeBook struct {
	Book   library.Book
	Files  map[string][]byte
	Status map[string]library.ExportStatus
	// Cover is sent when Book.HasCover; CoverStatus, unless ExportOK, is
	// answered instead.
	Cover       []byte
	CoverStatus library.ExportStatus
}

type mutation struct {
	n int
	f func(*library.Book) (deleted bool)
}

type exportKey struct {
	id     int
	format string
}

// FakeLibrary answers like library.Client, from memory. It is safe for
// concurrent use.
type FakeLibrary struct {
	mu         sync.Mutex
	libraryID  string
	books      map[int]*FakeBook
	checkErr   error
	checks     int
	checkDelay time.Duration
	down       bool
	exportErr  func(bookID int, format string) error
	mutations  map[int]mutation
	bookCalls  map[int]int
	exports    map[exportKey]int
	delay      time.Duration
	coverDelay time.Duration
	onCover    map[int]func(*library.Book)
	inFlight   int64
	maxFlight  int64
}

// NewLibrary returns a library holding copies of books.
func NewLibrary(libraryID string, books ...FakeBook) *FakeLibrary {
	l := &FakeLibrary{
		libraryID: libraryID,
		books:     make(map[int]*FakeBook, len(books)),
		mutations: map[int]mutation{},
		bookCalls: map[int]int{},
		exports:   map[exportKey]int{},
	}
	for _, b := range books {
		b.Book = copyBook(b.Book)
		l.books[b.Book.ID] = &b
	}
	return l
}

// Edit applies f to book id as if Felix changed it in Calibre between two
// runs. It does nothing for an unknown id.
func (l *FakeLibrary) Edit(id int, f func(*FakeBook)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if b, ok := l.books[id]; ok {
		f(b)
	}
}

// SetCheckError makes Check return err while it is non-nil.
func (l *FakeLibrary) SetCheckError(err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.checkErr = err
}

// SetDown makes every method return library.ErrUnreachable while down.
func (l *FakeLibrary) SetDown(down bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.down = down
}

// SetExportError makes ExportFormat return f's error when it is non-nil. f
// runs without the library's lock, so it may block.
func (l *FakeLibrary) SetExportError(f func(bookID int, format string) error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.exportErr = f
}

// MutateOnRead applies f to book id on the n-th Book call for it; the change
// stays. f returning true deletes the book.
func (l *FakeLibrary) MutateOnRead(id, n int, f func(*library.Book) (deleted bool)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.mutations[id] = mutation{n: n, f: f}
}

// SetExportDelay makes each export take d, or until its context ends.
func (l *FakeLibrary) SetExportDelay(d time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.delay = d
}

// ExportCalls counts the exports of one format that reached the library.
func (l *FakeLibrary) ExportCalls(id int, format string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.exports[exportKey{id, format}]
}

// MaxConcurrentExportBytes is the largest sum of want over exports that were
// in progress at the same time.
func (l *FakeLibrary) MaxConcurrentExportBytes() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.maxFlight
}

// CheckCalls is how often Check was called.
func (l *FakeLibrary) CheckCalls() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.checks
}

// SetCheckDelay makes each Check take d.
func (l *FakeLibrary) SetCheckDelay(d time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.checkDelay = d
}

func (l *FakeLibrary) Check(context.Context) (string, error) {
	l.mu.Lock()
	d := l.checkDelay
	l.checks++
	l.mu.Unlock()
	time.Sleep(d)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.down {
		return "", fmt.Errorf("fake: %w", library.ErrUnreachable)
	}
	if l.checkErr != nil {
		return "", l.checkErr
	}
	return l.libraryID, nil
}

func (l *FakeLibrary) CheckInLibrary(ctx context.Context, libraryID string) (string, error) {
	if libraryID != l.libraryID {
		return "", library.ErrNoLibrary
	}
	return l.Check(ctx)
}

func (l *FakeLibrary) ListIDsInLibrary(ctx context.Context, libraryID string) ([]library.BookRef, error) {
	if libraryID != l.libraryID {
		return nil, library.ErrNoLibrary
	}
	return l.ListIDs(ctx)
}

func (l *FakeLibrary) BookInLibrary(ctx context.Context, libraryID string, id int) (library.Book, error) {
	if libraryID != l.libraryID {
		return library.Book{}, library.ErrNoLibrary
	}
	return l.Book(ctx, id)
}

func (l *FakeLibrary) ExportFormatInLibrary(ctx context.Context, libraryID string, id int, format string, w io.Writer, want int64) (library.ExportResult, error) {
	if libraryID != l.libraryID {
		return library.ExportResult{}, library.ErrNoLibrary
	}
	return l.ExportFormat(ctx, id, format, w, want)
}

func (l *FakeLibrary) ListIDs(context.Context) ([]library.BookRef, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.down {
		return nil, fmt.Errorf("fake: %w", library.ErrUnreachable)
	}
	refs := make([]library.BookRef, 0, len(l.books))
	for _, id := range slices.Sorted(maps.Keys(l.books)) {
		refs = append(refs, library.BookRef{ID: id, UUID: l.books[id].Book.UUID})
	}
	return refs, nil
}

func (l *FakeLibrary) Book(_ context.Context, id int) (library.Book, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.down {
		return library.Book{}, fmt.Errorf("fake: %w", library.ErrUnreachable)
	}
	l.bookCalls[id]++
	b, ok := l.books[id]
	if m, has := l.mutations[id]; has && ok && l.bookCalls[id] == m.n {
		if m.f(&b.Book) {
			delete(l.books, id)
			ok = false
		}
	}
	if !ok {
		return library.Book{}, fmt.Errorf("fake: book %d: %w", id, library.ErrBookNotFound)
	}
	return copyBook(b.Book), nil
}

func (l *FakeLibrary) ExportFormat(ctx context.Context, bookID int, format string, w io.Writer, want int64) (library.ExportResult, error) {
	l.mu.Lock()
	if l.down {
		l.mu.Unlock()
		return library.ExportResult{}, fmt.Errorf("fake: %w", library.ErrUnreachable)
	}
	if f := l.exportErr; f != nil {
		l.mu.Unlock()
		if err := f(bookID, format); err != nil {
			return library.ExportResult{}, err
		}
		l.mu.Lock()
	}
	l.exports[exportKey{bookID, format}]++
	l.inFlight += want
	l.maxFlight = max(l.maxFlight, l.inFlight)
	delay := l.delay
	var (
		content []byte
		status  = library.ExportNotInCalibre
	)
	if b, ok := l.books[bookID]; ok && slices.ContainsFunc(b.Book.Formats, func(f library.Format) bool { return f.Name == format }) {
		content = b.Files[format]
		status = library.ExportOK
		if s, forced := b.Status[format]; forced {
			status = s
		}
	}
	l.mu.Unlock()
	defer func() {
		l.mu.Lock()
		l.inFlight -= want
		l.mu.Unlock()
	}()

	if delay > 0 {
		t := time.NewTimer(delay)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return library.ExportResult{}, fmt.Errorf("fake: export: %w", ctx.Err())
		case <-t.C:
		}
	}
	switch {
	case status != library.ExportOK:
		return library.ExportResult{Status: status}, nil
	case len(content) == 0 && want > 0:
		return library.ExportResult{Status: library.ExportMissingOnDisk}, nil
	case int64(len(content)) != want:
		return library.ExportResult{Status: library.ExportSizeMismatch}, nil
	}
	if _, err := w.Write(content); err != nil {
		return library.ExportResult{}, fmt.Errorf("fake: write: %w", err)
	}
	h := sha256.Sum256(content)
	return library.ExportResult{Status: library.ExportOK, Bytes: want, SHA256: hex.EncodeToString(h[:])}, nil
}

// SetCoverDelay makes every cover export wait d, or until its context ends.
func (l *FakeLibrary) SetCoverDelay(d time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.coverDelay = d
}

// OnCoverExport runs f on book id's record while its cover is exported, as
// if Felix changed the book in Calibre at that moment.
func (l *FakeLibrary) OnCoverExport(id int, f func(*library.Book)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.onCover == nil {
		l.onCover = map[int]func(*library.Book){}
	}
	l.onCover[id] = f
}

// ExportCoverInLibrary answers like library.Client: nil without a cover,
// ExportTooLarge over limit. limit counts as in flight while it runs.
func (l *FakeLibrary) ExportCoverInLibrary(ctx context.Context, libraryID string, bookID int, w io.Writer, limit int64) (library.ExportResult, error) {
	if libraryID != l.libraryID {
		return library.ExportResult{}, library.ErrNoLibrary
	}
	l.mu.Lock()
	if l.down {
		l.mu.Unlock()
		return library.ExportResult{}, fmt.Errorf("fake: %w", library.ErrUnreachable)
	}
	if l.exportErr != nil {
		if err := l.exportErr(bookID, "cover"); err != nil {
			l.mu.Unlock()
			return library.ExportResult{}, err
		}
	}
	l.exports[exportKey{bookID, "cover"}]++
	if f := l.onCover[bookID]; f != nil {
		if b, ok := l.books[bookID]; ok {
			f(&b.Book)
		}
	}
	l.inFlight += limit
	l.maxFlight = max(l.maxFlight, l.inFlight)
	delay := l.coverDelay
	var content []byte
	status := library.ExportNotInCalibre
	if b, ok := l.books[bookID]; ok && b.Book.HasCover && b.Cover != nil {
		content, status = b.Cover, library.ExportOK
		if b.CoverStatus != library.ExportOK {
			status = b.CoverStatus
		}
	}
	l.mu.Unlock()
	defer func() {
		l.mu.Lock()
		l.inFlight -= limit
		l.mu.Unlock()
	}()
	if delay > 0 {
		t := time.NewTimer(delay)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return library.ExportResult{}, fmt.Errorf("fake: cover export: %w", ctx.Err())
		case <-t.C:
		}
	}
	switch {
	case status != library.ExportOK:
		return library.ExportResult{Status: status}, nil
	case int64(len(content)) > limit:
		return library.ExportResult{Status: library.ExportTooLarge}, nil
	}
	if _, err := w.Write(content); err != nil {
		return library.ExportResult{}, fmt.Errorf("fake: write: %w", err)
	}
	h := sha256.Sum256(content)
	return library.ExportResult{Status: library.ExportOK, Bytes: int64(len(content)), SHA256: hex.EncodeToString(h[:])}, nil
}

func copyBook(b library.Book) library.Book {
	b.Authors = slices.Clone(b.Authors)
	b.Languages = slices.Clone(b.Languages)
	b.Identifiers = maps.Clone(b.Identifiers)
	b.Formats = slices.Clone(b.Formats)
	return b
}
