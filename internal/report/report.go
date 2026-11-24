// Package report exposes verified historical views without consulting Calibre.
package report

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/decide"
	"github.com/felixapel/calibre-bookwarden/internal/duplicates"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

var (
	ErrNoSuchBook     = errors.New("report: no such frozen book")
	ErrIntegrity      = errors.New("report: evidence integrity failure")
	ErrInvalidFilter  = errors.New("report: invalid filter")
	ErrInvalidCursor  = errors.New("report: invalid cursor")
	ErrInvalidLimits  = errors.New("report: invalid export limits")
	ErrExportTooLarge = errors.New("report: export too large")
	// ErrRunNotFinished: only a finished run can be checked, since its
	// evidence is still being written.
	ErrRunNotFinished = errors.New("report: the run has not finished")
	ErrNoSealStore    = errors.New("report: the store cannot check seals")
)

type Book struct {
	Ref     library.BookRef
	State   verify.BookState
	Reason  string
	Package *evidence.Package
	// Findings is the current report derivation, distinct from sealed history.
	FindingsVersion int
	Findings        []evidence.Finding
	// Decision is the current tier and proposals, recomputed on load.
	Decision decide.Decision
}

// Matches reports whether the book's title or one of its authors contains
// the query, ignoring case and diacritics.
func (b Book) Matches(query string) bool {
	if b.Package == nil {
		return false
	}
	q := Fold(query)
	if strings.Contains(Fold(b.Package.Calibre.Title), q) {
		return true
	}
	for _, a := range b.Package.Calibre.Authors {
		if strings.Contains(Fold(a), q) {
			return true
		}
	}
	return false
}

type Snapshot interface {
	Run() verify.Run
	Book(context.Context, int) (Book, error)
	NextBook(context.Context, int) (Book, bool, error)
	Close(context.Context) error
	// DuplicateRows returns the run's stored keys shared by two or more
	// books, and its comparable covers (ADR-024).
	DuplicateRows(context.Context) ([]duplicates.KeyRow, []duplicates.Cover, error)
	// BookKeys returns one book's stored keys, and its cover when comparable.
	BookKeys(context.Context, int) ([]duplicates.Key, *duplicates.Cover, error)
	// BookGroupRows returns every stored key row sharing a kind and value
	// with one of the book's keys, the book's own included.
	BookGroupRows(context.Context, int) ([]duplicates.KeyRow, error)
	// NearCovers returns the other comparable covers within
	// duplicates.MaxCoverDistance of a hash.
	NearCovers(context.Context, int, uint64) ([]duplicates.Cover, error)
}
type Store interface {
	Snapshot(context.Context, int64) (Snapshot, error)
}
type Filter struct {
	Code, Format string
	BookID       int
}
type Cursor struct {
	RunID   int64 `json:"run_id"`
	BookID  int   `json:"book_id"`
	Ordinal int   `json:"ordinal"`
	// Offset is how many rows a sorted Fixes list has already shown; the
	// default order pages by book and ordinal instead.
	Offset int `json:"offset,omitempty"`
}
type Row struct {
	RunID   int64
	BookID  int
	Title   string
	Finding evidence.Finding
}
type Page struct {
	Rows []Row
	Next *Cursor
}

// FixFilter selects proposals: a field, a tier and one frozen book.
type FixFilter struct {
	Field, Tier string
	BookID      int
	// Decided keeps rows decided by the agent, by a person, or left by a
	// disagreeing panel (ADR-029); the web layer applies it.
	Decided string
	// Query keeps the books whose title or an author contains it, ignoring
	// case and diacritics. Sort is one of the Sort constants; an empty or
	// unknown value is the default, book order.
	Query, Sort string
	// Keys are the (book, field) pairs DecidedDisagree keeps: the web layer
	// knows which fixes the panel left for lack of unanimity.
	Keys []FixKey
	// Skip is how many rows a sorted list has already shown; the service
	// sets it from the cursor.
	Skip int
	// Muted keeps only the fixes of muted types (§6.3). MutedTypes are the
	// types the web layer found muted: without Muted their fixes are left
	// out, and none means nothing is left out.
	Muted      bool
	MutedTypes []FixType
}

// FixType is what muting groups fixes by: the field, the proposal's roots
// sorted and joined with "+", and "fill" when the current value
// is empty, else "replace".
type FixType struct{ Field, Path, Kind string }

// TypeOf is the type of a proposal.
func TypeOf(p evidence.Proposal) FixType {
	kind := "replace"
	if len(p.Current) == 0 {
		kind = "fill"
	}
	return FixType{Field: p.Field, Path: strings.Join(slices.Sorted(slices.Values(p.Roots)), "+"), Kind: kind}
}

// FixKey names one proposal of a run.
type FixKey struct {
	BookID int
	Field  string
}

// Sorts of the Fixes list.
const (
	SortBook    = "book"
	SortField   = "field"
	SortTier    = "tier"
	SortDecided = "decided"
)

// MaxFixQuery is the longest search, in characters.
const MaxFixQuery = 100

// Values of FixFilter.Decided.
const (
	DecidedAgent    = "agent"
	DecidedPerson   = "person"
	DecidedDisagree = "disagree"
)

// FixRow is one proposal of a verified book.
type FixRow struct {
	RunID    int64
	BookID   int
	Title    string
	Tier     string
	Proposal evidence.Proposal
}

type FixPage struct {
	Rows []FixRow
	Next *Cursor
	// MutedHidden is how many fixes of muted types the filters would show
	// but the default view leaves out. On a run without a check it counts
	// from the page on.
	MutedHidden int
	// Walked is whether the run has no check, so the page was read from the
	// sealed packages in book order: a sort is ignored and the Decided filter
	// is left to the page.
	Walked bool
}

type BookSummary struct {
	Ref           library.BookRef
	State         verify.BookState
	Reason, Title string
}
type BooksPage struct {
	Rows       []BookSummary
	NextBookID int
}
type CSVLimits struct {
	MaxRows  int
	MaxBytes int64
}

func DefaultCSVLimits() CSVLimits { return CSVLimits{MaxRows: 50000, MaxBytes: 32 << 20} }

// SealCheck is the latest whole-run verification of a finished run (ADR-038).
// Books counts every frozen book walked and Broken the ones that failed;
// FirstBroken is the lowest broken book id, 0 when none is broken.
type SealCheck struct {
	RunID       int64
	CheckedAt   time.Time
	Books       int
	Broken      int
	FirstBroken int
	Detail      string
	// FindingsVersion and DecisionVersion are the rules the check ran under.
	FindingsVersion, DecisionVersion int
}

// BookProjection is what the check keeps of one verified, checked book.
type BookProjection struct {
	BookID    int
	Title     string
	Tier      string
	Findings  []evidence.Finding
	Proposals []evidence.Proposal
	// SearchText is the folded title and authors a search looks at.
	SearchText string
}

// ProjectedBook says what the projection knows of one frozen book.
type ProjectedBook struct {
	// InRun is whether the run froze the book; Checked whether it has
	// projected rows, which only a checked book has.
	InRun, Checked      bool
	Findings, Proposals int
}

// SealStore is the Store of a service that can check seals: it saves a
// check with its projection and lists the runs that need one.
type SealStore interface {
	// SaveSealCheck replaces the run's projection with books and upserts
	// its check, atomically.
	SaveSealCheck(ctx context.Context, c SealCheck, books []BookProjection) error
	// RunsToCheck lists the finished runs whose check is missing, older
	// than staleBefore, older than the run's end or made under other findings
	// or decision versions than this binary's, by id.
	RunsToCheck(ctx context.Context, staleBefore time.Time) ([]int64, error)
	// FinishedRuns lists every finished run, by id.
	FinishedRuns(ctx context.Context) ([]int64, error)
}

// ProjectionReader is implemented by a Snapshot that can serve pages from
// the projection. Its reads see the same snapshot as the Snapshot's.
type ProjectionReader interface {
	// SealCheck returns the run's check when it exists, is not older than
	// the run's end and ran under this binary's findings and decision
	// versions; otherwise false.
	SealCheck(ctx context.Context) (SealCheck, bool, error)
	ProjectedBook(ctx context.Context, bookID int) (ProjectedBook, error)
	// FindingRows visits the findings that match f after the cursor, in book
	// and ordinal order, with the ordinal; limit 0 means all.
	FindingRows(ctx context.Context, f Filter, after *Cursor, limit int, visit func(Row, int) error) error
	FixRows(ctx context.Context, f FixFilter, after *Cursor, limit int, visit func(FixRow, int) error) error
	// MutedFixes counts the fixes of f.MutedTypes that the other filters keep.
	MutedFixes(ctx context.Context, f FixFilter) (int, error)
	// BookRows returns up to limit frozen books after a book id, in id order.
	BookRows(ctx context.Context, after, limit int) ([]BookSummary, error)
}
