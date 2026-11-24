package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/config"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/inspect"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

const inspectUsage = "usage: bookwarden inspect --book N [--format EPUB] [--json] [--timeout 2m] [--large]"

// The command shares the container's /tmp tmpfs with a running check, so it
// stays small unless told otherwise.
const (
	inspectCap       = 64 << 20  // the largest format it exports without --large
	inspectMinFree   = 256 << 20 // free space needed when a size isn't known
	inspectDirPrefix = "bookwarden-inspect-"
	inspectStaleAge  = time.Hour
)

// inspectFile is inspect.File; tests replace it.
var inspectFile = inspect.File

// freeSpace is the bytes available to us in dir; tests replace it.
var freeSpace = func(dir string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return st.Bavail * uint64(st.Bsize), nil //nolint:gosec // the block size is positive
}

// neededFreeSpace is the free space an export of size bytes needs: twice the
// size, or inspectMinFree when the size isn't known.
func neededFreeSpace(size int64) uint64 {
	if size < 0 {
		return inspectMinFree
	}
	return 2 * uint64(size)
}

// cleanStaleInspectDirs removes the temp directories of earlier runs that
// were killed before they could clean up: ours by name, older than an hour.
func cleanStaleInspectDirs(root string, now time.Time) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), inspectDirPrefix) {
			continue
		}
		if info, err := e.Info(); err == nil && now.Sub(info.ModTime()) > inspectStaleAge {
			_ = os.RemoveAll(filepath.Join(root, e.Name()))
		}
	}
}

// errClass names an error without quoting it: Calibre's messages can hold the
// library path, which has the author and title in it.
func errClass(err error) string {
	var ce *library.CalibreError
	switch {
	case errors.As(err, &ce):
		return "calibre_error"
	case errors.Is(err, library.ErrUnreachable):
		return "calibre_unreachable"
	case errors.Is(err, library.ErrAuth), errors.Is(err, library.ErrForbidden):
		return "calibre_refused"
	case errors.Is(err, library.ErrIncompatible):
		return "calibre_incompatible"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return "timeout"
	}
	return "export_error"
}

// inspectOptions are the inspect subcommand's flags.
type inspectOptions struct {
	Book    int
	Format  string // "" for every format
	JSON    bool
	Large   bool          // lifts the 64 MiB cap on a format's size
	Timeout time.Duration // per format: its export and inspection together
}

func parseInspectArgs(args []string) (inspectOptions, error) {
	fs := flag.NewFlagSet("inspect", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := inspectOptions{}
	fs.IntVar(&o.Book, "book", 0, "")
	fs.StringVar(&o.Format, "format", "", "")
	fs.BoolVar(&o.JSON, "json", false, "")
	fs.BoolVar(&o.Large, "large", false, "")
	fs.DurationVar(&o.Timeout, "timeout", 2*time.Minute, "")
	if err := fs.Parse(args); err != nil {
		return o, fmt.Errorf("inspect: %w", err)
	}
	switch {
	case fs.NArg() != 0:
		return o, errors.New("inspect: unexpected argument")
	case o.Book <= 0:
		return o, errors.New("inspect: --book is required")
	case o.Timeout <= 0:
		return o, errors.New("inspect: --timeout must be positive")
	}
	o.Format = strings.ToUpper(strings.TrimSpace(o.Format))
	return o, nil
}

// inspectLibrary is the read-only part of the content-server client that the
// command uses. Nothing here can change the library.
type inspectLibrary interface {
	Check(ctx context.Context) (string, error)
	BookInLibrary(ctx context.Context, libraryID string, id int) (library.Book, error)
	ExportFormatInLibrary(ctx context.Context, libraryID string, bookID int, format string, w io.Writer, want int64) (library.ExportResult, error)
}

// inspectCommand exports a book's formats through the read-only client, runs
// the inspectors on them and prints what they found: states, reasons, counts
// and timings, never book text, titles or ISBN values. It reads only the
// BOOKWARDEN_CALIBRE_* variables, so it needs no database, and writes
// nothing to Calibre or to Postgres.
func inspectCommand(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	opts, err := parseInspectArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n%s\n", err, inspectUsage)
		return 2
	}
	cfg, err := config.CalibreFromEnv(getenv)
	if err != nil {
		fmt.Fprintf(stderr, "inspect: configuration: %v\n", err)
		return 1
	}
	for _, w := range cfg.Warnings {
		fmt.Fprintf(stderr, "inspect: configuration: %s\n", w)
	}
	lib := library.New(library.Config{BaseURL: cfg.URL, User: cfg.User, Password: cfg.Password, LibraryID: cfg.Library})
	return runInspect(ctx, lib, opts, verify.DefaultLimits, stdout, stderr)
}

type hintsReport struct {
	Method    string `json:"method"`
	State     string `json:"state"`
	Reason    string `json:"reason,omitempty"`
	ISBNCount int    `json:"isbn_count"`
	Pages     []int  `json:"pages"`
}

type docReport struct {
	Pos      int    `json:"pos"`
	Landmark string `json:"landmark"`
	Whole    bool   `json:"whole"`
	Reason   string `json:"reason,omitempty"` // doc_window: read to its first 32 KiB, which is not a failure
}

type excerptReport struct {
	Chars     int  `json:"chars"`
	Truncated bool `json:"truncated"`
	Complete  bool `json:"complete"`
}

// formatReport is what the command says about one format. It holds counts
// and states only.
type formatReport struct {
	Format     string         `json:"format"`
	Size       int64          `json:"size"`
	Kind       string         `json:"kind,omitempty"`
	State      string         `json:"state"`
	Reason     string         `json:"reason,omitempty"`
	ScanReason string         `json:"scan_reason,omitempty"` // why the scan didn't end ok, with no hints
	Text       string         `json:"text,omitempty"`
	PDFPages   int            `json:"pdf_pages,omitempty"`
	Hints      *hintsReport   `json:"hints,omitempty"`
	Docs       []docReport    `json:"docs,omitempty"`
	Excerpt    *excerptReport `json:"excerpt,omitempty"`
	ExportMS   *int64         `json:"export_ms,omitempty"`
	InspectMS  *int64         `json:"inspect_ms,omitempty"`

	note string // a line for stderr; not part of the report
}

type inspectReport struct {
	Book    int            `json:"book"`
	Formats []formatReport `json:"formats"`
}

const reasonInsufficientSpace = "insufficient_space"

// runInspect is inspectCommand once the library client exists.
func runInspect(ctx context.Context, lib inspectLibrary, opts inspectOptions, lim verify.Limits, stdout, stderr io.Writer) int {
	// Only closed messages are printed for anything that came from Calibre.
	fail := func(msg string) int {
		fmt.Fprintf(stderr, "inspect: %s\n", msg)
		return 1
	}
	if !opts.Large {
		lim.FileSize = min(lim.FileSize, inspectCap)
	}
	libraryID, err := lib.Check(ctx)
	if err != nil {
		return fail(errClass(err))
	}
	b, err := lib.BookInLibrary(ctx, libraryID, opts.Book)
	if errors.Is(err, library.ErrBookNotFound) {
		return fail(fmt.Sprintf("book %d not found", opts.Book))
	}
	if err != nil {
		return fail(errClass(err))
	}
	formats := slices.SortedFunc(slices.Values(b.Formats), func(x, y library.Format) int { return strings.Compare(x.Name, y.Name) })
	if opts.Format != "" {
		formats = slices.DeleteFunc(formats, func(f library.Format) bool { return !strings.EqualFold(f.Name, opts.Format) })
		if len(formats) == 0 {
			return fail(fmt.Sprintf("book %d has no %s format", opts.Book, opts.Format))
		}
	}
	cleanStaleInspectDirs(os.TempDir(), time.Now())
	dir, err := os.MkdirTemp("", inspectDirPrefix)
	if err != nil {
		return fail("cannot create the temp directory")
	}
	defer func() { _ = os.RemoveAll(dir) }()

	rep := inspectReport{Book: b.ID, Formats: make([]formatReport, 0, len(formats))}
	for _, f := range formats {
		fr, msg := inspectFormat(ctx, lib, libraryID, b.ID, f, filepath.Join(dir, "format"), opts.Timeout, lim)
		if msg != "" {
			return fail(msg)
		}
		if fr.note != "" {
			fmt.Fprintf(stderr, "inspect: %s: %s\n", f.Name, fr.note)
		}
		rep.Formats = append(rep.Formats, fr)
	}
	if opts.JSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			return fail("cannot write the report")
		}
		return 0
	}
	rep.writeText(stdout)
	return 0
}

// inspectFormat exports one format to path, inspects it and deletes it again.
// It applies the limits a verify run applies. An error is only for a problem
// with the whole command, returned as a closed message that never quotes
// Calibre or a path: Calibre unreachable, the temp disk, or ctx.
func inspectFormat(ctx context.Context, lib inspectLibrary, libraryID string, bookID int, f library.Format, path string, timeout time.Duration, lim verify.Limits) (formatReport, string) {
	fr := formatReport{Format: f.Name, Size: f.Size, State: string(evidence.StateNotInspected)}
	switch {
	case f.Missing:
		fr.State, fr.Reason = string(evidence.StateMissing), evidence.ReasonMissingOnDisk
		return fr, ""
	case f.Size < 0:
		fr.Reason = evidence.ReasonSizeUnknown
		return fr, ""
	case f.Size > lim.FileSize:
		fr.Reason = evidence.ReasonTooLarge
		return fr, ""
	}
	if free, err := freeSpace(filepath.Dir(path)); err != nil || free < neededFreeSpace(f.Size) {
		fr.Reason = reasonInsufficientSpace
		fr.note = fmt.Sprintf("not enough free space in the temp directory (need %d MiB, have %d MiB); is a check running?", neededFreeSpace(f.Size)>>20, free>>20)
		if err != nil {
			fr.note = "not enough free space: cannot tell how much the temp directory has"
		}
		return fr, ""
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) //nolint:gosec // path is inside our own temp directory
	if err != nil {
		return fr, "cannot create the temp file"
	}
	defer func() { _ = os.Remove(path) }()

	fctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()
	res, err := lib.ExportFormatInLibrary(fctx, libraryID, bookID, f.Name, file, f.Size)
	closeErr := file.Close()
	exportMS := time.Since(start).Milliseconds()
	switch {
	case err != nil && ctx.Err() == nil && errors.Is(fctx.Err(), context.DeadlineExceeded):
		fr.Reason = evidence.ReasonTimeout
		fr.ExportMS = &exportMS
		return fr, ""
	case err != nil && ctx.Err() == nil && errors.As(err, new(*library.CalibreError)):
		// This format only; the others may still be fine.
		fr.Reason = "calibre_error"
		fr.ExportMS = &exportMS
		return fr, ""
	case err != nil:
		return fr, fmt.Sprintf("export %s: %s", f.Name, errClass(err))
	case closeErr != nil:
		return fr, "cannot write the temp file"
	}
	fr.ExportMS = &exportMS

	switch res.Status {
	case library.ExportOK:
	case library.ExportNotInCalibre:
		fr.State, fr.Reason = string(evidence.StateMissing), evidence.ReasonNotInCalibre
		return fr, ""
	case library.ExportMissingOnDisk:
		fr.State, fr.Reason = string(evidence.StateMissing), evidence.ReasonMissingOnDisk
		return fr, ""
	default:
		fr.Reason = evidence.ReasonSizeMismatch
		return fr, ""
	}
	if res.Bytes == 0 {
		fr.State = string(evidence.StateEmpty)
		return fr, ""
	}
	start = time.Now()
	r, err := inspectFile(fctx, f.Name, path, lim.Inspect)
	inspectMS := time.Since(start).Milliseconds()
	if err != nil {
		return fr, "cannot read the exported file"
	}
	fr.InspectMS = &inspectMS
	fr.State, fr.Reason, fr.Kind, fr.Text, fr.PDFPages = string(r.State), r.Reason, r.Detected, r.Text, r.Pages
	if h := r.ISBNHints; h != nil && r.State == evidence.StateOK {
		fr.Hints = &hintsReport{Method: h.Method, State: h.State, Reason: r.FailReason, ISBNCount: len(h.ISBNs), Pages: h.Pages}
	} else {
		fr.ScanReason = r.FailReason
	}
	for _, d := range r.ScanDocs {
		fr.Docs = append(fr.Docs, docReport{Pos: d.Pos, Landmark: d.Landmark, Whole: d.Whole, Reason: d.Reason})
	}
	// Only what verify would seal: a readable file, and a method that fits
	// the format's name.
	if e := r.Excerpt; e != nil && r.State == evidence.StateOK && evidence.ExcerptMethodFor(f.Name) == e.Method {
		fr.Excerpt = &excerptReport{Chars: e.Chars, Truncated: e.Truncated, Complete: e.Complete}
	}
	return fr, ""
}

// writeText prints the report as plain lines. Only counts, states, reasons
// and landmarks go in it.
func (r inspectReport) writeText(w io.Writer) {
	fmt.Fprintf(w, "book %d: %d formats\n", r.Book, len(r.Formats))
	for _, f := range r.Formats {
		fmt.Fprintf(w, "format %s size=%d", f.Format, f.Size)
		if f.Kind != "" {
			fmt.Fprintf(w, " kind=%s", f.Kind)
		}
		fmt.Fprintf(w, " state=%s", f.State)
		if f.Reason != "" {
			fmt.Fprintf(w, " reason=%s", f.Reason)
		}
		if f.Text != "" {
			fmt.Fprintf(w, " text=%s", f.Text)
		}
		if f.PDFPages > 0 {
			fmt.Fprintf(w, " pdf_pages=%d", f.PDFPages)
		}
		fmt.Fprintln(w)
		if f.ScanReason != "" {
			fmt.Fprintf(w, "  scan: reason=%s\n", f.ScanReason)
		}
		if h := f.Hints; h != nil {
			pages := make([]string, len(h.Pages))
			for i, p := range h.Pages {
				pages[i] = strconv.Itoa(p)
			}
			fmt.Fprintf(w, "  hints: method=%s state=%s", h.Method, h.State)
			if h.Reason != "" {
				fmt.Fprintf(w, " reason=%s", h.Reason)
			}
			fmt.Fprintf(w, " isbns=%d pages=%s\n", h.ISBNCount, strings.Join(pages, ","))
		}
		if len(f.Docs) > 0 {
			docs := make([]string, len(f.Docs))
			for i, d := range f.Docs {
				docs[i] = fmt.Sprintf("%d:%s", d.Pos, d.Landmark)
				switch {
				case d.Reason == inspect.FailDocWindow:
					docs[i] += "(window)"
				case !d.Whole:
					docs[i] += "(partial)"
				}
			}
			fmt.Fprintf(w, "  read %d documents: %s\n", len(f.Docs), strings.Join(docs, " "))
		}
		if e := f.Excerpt; e != nil {
			fmt.Fprintf(w, "  excerpt: chars=%d truncated=%t complete=%t\n", e.Chars, e.Truncated, e.Complete)
		}
		if f.ExportMS != nil {
			fmt.Fprintf(w, "  timings: export=%dms", *f.ExportMS)
			if f.InspectMS != nil {
				fmt.Fprintf(w, " inspect=%dms", *f.InspectMS)
			}
			fmt.Fprintln(w)
		}
	}
}
