// Package verify runs read-only checks of the Calibre library: it freezes
// the book list, checks books in parallel under a byte budget, waits while
// Calibre is away, and seals one evidence package per book.
package verify

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/inspect"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/sources"
)

// BookState is a book's state within a run (spec section 6).
type BookState string

const (
	BookPending       BookState = "pending"
	BookChecked       BookState = "checked"
	BookSourceChanged BookState = "source_changed"
	BookFailed        BookState = "failed"
)

// Outcome is how checking one book ended. Evidence holds the canonical
// package bytes and is set only for BookChecked. Excerpts holds the body of
// each excerpt the package refers to, by hash (ADR-032 part 2): the package
// seals the hash, and the store keeps the body next to it.
//
// Reused says the package's file evidence came from an earlier package of the
// book (ADR-039), and InspectKey is the key of the inspector build that made
// that file evidence. Neither is sealed: a reused package is byte-identical to
// the one a full check seals.
type Outcome struct {
	State      BookState
	Reason     string
	Evidence   []byte
	Seal       string
	Excerpts   map[string]string
	Reused     bool
	InspectKey string
	// MeasuredAt is when the file evidence was measured from the files: now
	// for a full check, the earlier package's time for a reused one.
	MeasuredAt time.Time
	// Raws are the extractors' raw answers the package's extraction refers
	// to (M4), stored privately; only their hashes are sealed.
	Raws []RawOutput
}

// Extraction reads a format's excerpt with M4's extractors and parsers
// (shadow): the sealable block and the raw answers to store with it. Extract
// fails only when ctx ends; a provider's failure is a state in the block.
type Extraction interface {
	BeginRun(runID int64)
	Extract(ctx context.Context, ex evidence.Excerpt, body string) (*evidence.Extraction, []RawOutput, error)
}

// RawOutput is one extractor's raw answer: its hash, its body, and the
// extraction cache key it answers ("" for none).
type RawOutput struct{ Key, SHA256, Body string }

// Prior is a book's newest sealed package, for an incremental run to reuse
// the file evidence of (ADR-039). Package has been opened, so its seal holds.
// InspectKey is the key the package was stored with ("" for a package from
// before keys were kept). Excerpts holds the body of every excerpt the
// package refers to, read back and checked against its hash.
type Prior struct {
	Package    evidence.Package
	InspectKey string
	// MeasuredAt is when its file evidence was measured; zero when unknown,
	// which is never reused.
	MeasuredAt time.Time
	Excerpts   map[string]string
}

// Library is what a check needs from Calibre; *library.Client implements it.
type Library interface {
	Check(ctx context.Context) (string, error)
	CheckInLibrary(ctx context.Context, libraryID string) (string, error)
	ListIDsInLibrary(ctx context.Context, libraryID string) ([]library.BookRef, error)
	BookInLibrary(ctx context.Context, libraryID string, id int) (library.Book, error)
	ExportFormatInLibrary(ctx context.Context, libraryID string, bookID int, format string, w io.Writer, want int64) (library.ExportResult, error)
	ExportCoverInLibrary(ctx context.Context, libraryID string, bookID int, w io.Writer, limit int64) (library.ExportResult, error)
}

// Hinter reads ISBN hints from a PDF's text layer or by OCR; *ocr.Engine
// implements it. languages are the Calibre record's (ISO 639-2), which pick the
// OCR model. left is the book's remaining tool time, which it charges.
type Hinter interface {
	PDFHints(ctx context.Context, path string, pages int, textLayer bool, languages []string, left *time.Duration) *evidence.ISBNHints
}

// ExcerptHinter is a Hinter that also returns a text layer's excerpt from the
// same read (ADR-032 part 2); *ocr.Engine implements it. A Hinter without it
// gives hints and no excerpt.
type ExcerptHinter interface {
	Hinter
	PDFHintsExcerpt(ctx context.Context, path string, pages int, textLayer bool, languages []string, left *time.Duration) (*evidence.ISBNHints, *evidence.Excerpt)
}

// Sources looks a book's ISBNs up in outside catalogues; *sources.Service
// implements it. BeginRun is called whenever a run starts or resumes, with
// the run's refresh budget for expired cache answers (ADR-044).
type Sources interface {
	BeginRun(ctx context.Context, runID int64, refresh int)
	Lookup(ctx context.Context, runID int64, seeds []sources.Seed) []evidence.Lookup
}

// Limits bound what a run downloads (spec section 10). FileSize is the
// largest format that is requested; InFlight caps the bytes being exported
// across all workers at once. HintTime is each book's tool time for ISBN
// hints.
type Limits struct {
	FileSize, InFlight int64
	Inspect            inspect.Limits
	HintTime           time.Duration
}

// DefaultLimits are the spec's defaults: 512 MiB per file, 1 GiB in flight,
// the inspectors' section 10 limits and 180 s of OCR per book.
var DefaultLimits = Limits{FileSize: 512 << 20, InFlight: 1 << 30, Inspect: inspect.DefaultLimits, HintTime: 180 * time.Second}

// Scope says what a run checks. BookID 0 means the whole library. An
// Incremental run reuses a book's file evidence from its newest sealed
// package when nothing the evidence depends on has changed (ADR-039).
// ScheduledFor is the scheduler's slot the run was started for, zero for a run
// somebody started.
type Scope struct {
	BookID            int
	ExpectedLibraryID string
	ExpectedUUID      string
	Incremental       bool
	ScheduledFor      time.Time
}

// kind is the run's kind in the ledger: book, incremental or full.
func (s Scope) kind() string {
	switch {
	case s.BookID != 0:
		return "book"
	case s.Incremental:
		return "incremental"
	}
	return "full"
}

// RunStatus is a run's state. Running and waiting runs are active; at most
// one run is active at a time.
type RunStatus string

const (
	RunRunning   RunStatus = "running"
	RunWaiting   RunStatus = "waiting" // for Calibre to come back
	RunCompleted RunStatus = "completed"
	RunCancelled RunStatus = "cancelled"
	RunFailed    RunStatus = "failed"
)

// Terminal reports whether a run in this status has ended.
func (s RunStatus) Terminal() bool {
	return s == RunCompleted || s == RunCancelled || s == RunFailed
}

// Run is one check of the library. Error explains a failed or waiting run.
type Run struct {
	ID                    int64
	Scope                 Scope
	Status                RunStatus
	LibraryID, AppVersion string
	Error                 string
	CancelRequested       bool
	StartedAt, FinishedAt time.Time
	// DuplicatesVersion is the duplicate rules the run was checked under
	// (ADR-024); 0 for runs created before M5.2.
	DuplicatesVersion int
	// Pinned keeps the run whatever retention says (ADR-039).
	Pinned bool
	// Stats is where the run's time went so far (B5).
	Stats Stats
}

// Progress counts a run's books by state.
type Progress struct {
	Total, Pending, Checked, SourceChanged, Failed int
	// Reused counts the checked books whose file evidence was reused
	// (ADR-039); it is part of Checked.
	Reused int
}

// RunView is a run with its progress.
type RunView struct {
	Run      Run
	Progress Progress
}

// CalibreStatus is the result of the last compatibility probe. State is one
// of unknown, ok, unreachable, incompatible, auth_failed, forbidden or error.
type CalibreStatus struct {
	State, Message, LibraryID string
	CheckedAt                 time.Time
}

var (
	ErrRunActive       = errors.New("verify: a run is already active")
	ErrNoSuchRun       = errors.New("verify: no such run")
	ErrCalibreNotReady = errors.New("verify: calibre is not ready")
	ErrBookFinished    = errors.New("verify: the book already has a result in this run")
	ErrInvalidScope    = errors.New("verify: incomplete identity guards")
	ErrBookChanged     = errors.New("verify: book identity changed")
	// ErrStoreUnavailable wraps a store error that means the database is
	// away or busy: the runner stops the batch and resumes the run later.
	// Any other FinishBook error fails that book alone.
	ErrStoreUnavailable = errors.New("verify: the store is unavailable")
)

// MaxCrashes is how many times the process may die with a book in hand
// before the book is failed as process_crashed instead of inspected again.
const MaxCrashes = 2

// RunStore persists runs. Implementations: store.RunRepo (Postgres) and
// verifytest.MemStore.
type RunStore interface {
	// CreateRun freezes books as the run's book list. ErrRunActive if a run
	// is running or waiting.
	CreateRun(ctx context.Context, scope Scope, libraryID, appVersion string, books []library.BookRef) (Run, error)
	// ActiveRun returns the running or waiting run, if any.
	ActiveRun(ctx context.Context) (Run, bool, error)
	Run(ctx context.Context, id int64) (Run, error)
	// PendingBooks returns the books without a result, in frozen order.
	PendingBooks(ctx context.Context, runID int64) ([]library.BookRef, error)
	// FinishBook stores a pending book's outcome and evidence in one
	// transaction. ErrBookFinished if the book has no pending row; an error
	// wrapping ErrStoreUnavailable if the database is away or busy.
	FinishBook(ctx context.Context, runID int64, ref library.BookRef, out Outcome) error
	// TakeBook marks a pending book as in hand; FinishBook and ReleaseBooks
	// clear the mark.
	TakeBook(ctx context.Context, runID int64, bookID int) error
	ReleaseBooks(ctx context.Context, runID int64, bookIDs []int) error
	// RecoverCrashed counts a crash for each pending book still in hand,
	// clears the marks, and fails each book with MaxCrashes or more as
	// process_crashed. It returns how many it failed and the pending books
	// left with a crash.
	RecoverCrashed(ctx context.Context, runID int64) (int, []int, error)
	// SetStatus sets the status and its message; terminal ones set FinishedAt.
	SetStatus(ctx context.Context, runID int64, status RunStatus, message string) error
	// SaveRunStats replaces the run's stats, and its ledger row's if it has
	// one.
	SaveRunStats(ctx context.Context, runID int64, stats Stats) error
	// RequestCancel flags an active run; it is a no-op for an ended run.
	RequestCancel(ctx context.Context, runID int64) error
	Progress(ctx context.Context, runID int64) (Progress, error)
	// ListRuns returns up to limit runs, newest first.
	ListRuns(ctx context.Context, limit int) ([]Run, error)
	// PriorEvidence returns the newest sealed package of a book of this
	// library, if there is one that opens and whose excerpts are all present
	// and match their hashes; ok is false otherwise. Reuse is only ever an
	// optimisation, so a package that can't be used is no error.
	PriorEvidence(ctx context.Context, libraryID string, ref library.BookRef) (prior Prior, ok bool, err error)
	// SetPinned pins or unpins a run. ErrNoSuchRun if there is none.
	SetPinned(ctx context.Context, runID int64, pinned bool) error
	// LockRunner takes the lock that lets one process run checks. ok is false
	// while another holder has it.
	LockRunner(ctx context.Context) (release func(), ok bool, err error)
}
