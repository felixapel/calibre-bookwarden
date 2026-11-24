// Package inspect reads a downloaded format and reports whether it is
// readable and what it says about itself (spec section 10). Every inspector is
// pure Go, runs in-process and is bounded by Limits and the caller's context.
package inspect

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strings"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

// Limits bound one file's inspection. Zero fields take DefaultLimits.
type Limits struct {
	MaxEntries      int   // zip entries
	MaxDecompressed int64 // bytes decompressed from one file in total
	MaxRatio        int64 // decompressed to compressed bytes, per zip entry
	MaxPixels       int64 // above this an image is checked by its header only
}

// DefaultLimits are the spec's section 10 limits.
var DefaultLimits = Limits{MaxEntries: 10_000, MaxDecompressed: 4 << 30, MaxRatio: 100, MaxPixels: 24_000_000}

func (l Limits) withDefaults() Limits {
	if l.MaxEntries <= 0 {
		l.MaxEntries = DefaultLimits.MaxEntries
	}
	if l.MaxDecompressed <= 0 {
		l.MaxDecompressed = DefaultLimits.MaxDecompressed
	}
	if l.MaxRatio <= 0 {
		l.MaxRatio = DefaultLimits.MaxRatio
	}
	if l.MaxPixels <= 0 {
		l.MaxPixels = DefaultLimits.MaxPixels
	}
	return l
}

// Result is what inspecting one file found. Panic holds a recovered panic
// and its stack for the caller's log; it is never sealed.
type Result struct {
	State    evidence.State
	Reason   string
	Detected string
	Text     string
	Metadata *evidence.FileMetadata
	Panic    string
	// Pages is a PDF's page count as its page tree states it, 0 when absent
	// or implausible. It steers OCR and is not evidence.
	Pages int
	// ISBNHints is an EPUB's front-matter scan (method epub_text), nil for
	// other formats and for an EPUB that wasn't scanned.
	ISBNHints *evidence.ISBNHints
	// Excerpt is an EPUB's front-matter text (ADR-032 part 2), built in the
	// same pass; nil when there is none. Its body is not sealed.
	Excerpt *evidence.Excerpt
	// FileCover is the measures of the cover image an EPUB's own OPF names
	// (ADR-043), nil when it names none; never the image.
	FileCover *evidence.CoverCandidate
	// FailReason says why the file's ISBN scan didn't finish ok, or why there
	// was none: one of the Fail constants, "" when nothing went wrong. It is
	// for logs and diagnostics only and is never sealed (Reason is the
	// sealed one).
	FailReason string
	// ScanDocs are the spine documents an EPUB's ISBN scan read, in the
	// order it read them; nil when it read none. Diagnostics only: never
	// sealed.
	ScanDocs []ScanDoc
}

// ScanDoc is one spine document the ISBN scan read: its 1-based spine
// position, its landmark, and whether it was read and parsed to its end.
// Reason is FailDocWindow for a document that was read only to its per-document
// window, which is not a failure, and "" otherwise.
type ScanDoc struct {
	Pos      int
	Landmark string
	Whole    bool
	Reason   string
}

// Why a file's scan failed, timed out or never ran (Result.FailReason). The
// set is closed.
const (
	FailTextBudget    = "text_budget"    // the scan's total text bound ran out
	FailDocReadLimit  = "doc_read_limit" // a document was cut at the per-document read limit
	FailDocWindow     = "doc_window"     // a document was read only to its first 32 KiB of text; ScanDoc.Reason only, never a failure
	FailXMLLimit      = "xml_limit"      // an XML entry was larger than the XML size bound
	FailXMLError      = "xml_error"      // a document or the OPF didn't parse
	FailNavUnreadable = "nav_unreadable" // a navigation document was missing or didn't parse
	FailZipLimit      = "zip_limit"      // a zip limit of spec section 10 was hit
	FailDeadline      = "deadline"       // the file's deadline passed
	FailDRM           = "drm"            // the book is locked to a reader
	FailOther         = "other"          // anything else: a corrupt file, an unsupported method, a panic
)

type inspector func(ctx context.Context, r io.ReaderAt, size int64, lim Limits) Result

// inspectors holds one inspector per kind.
var inspectors = map[string]inspector{
	"epub": inspectEPUB,
	"cbz":  inspectCBZ,
	"mobi": inspectMOBI,
	"pdf":  inspectPDF,
	"jpeg": inspectImage,
	"png":  inspectImage,
	"gif":  inspectImage,
	"webp": inspectImage,
}

// declaredKinds maps Calibre's format names to the inspector for bytes that
// Detect doesn't recognise.
var declaredKinds = map[string]string{
	"EPUB": "epub", "KEPUB": "epub", "PDF": "pdf", "MOBI": "mobi", "AZW": "mobi", "AZW3": "mobi", "PRC": "mobi",
	"CBZ": "cbz", "JPG": "jpeg", "JPEG": "jpeg", "PNG": "png", "GIF": "gif", "WEBP": "webp",
}

// Inspect inspects size bytes from r, which Calibre calls declared. It never
// panics, and returns not_inspected/timeout once ctx has ended.
func Inspect(ctx context.Context, declared string, r io.ReaderAt, size int64, lim Limits) (res Result) {
	if size == 0 {
		return Result{State: evidence.StateEmpty}
	}
	kind := ""
	defer func() {
		if v := recover(); v != nil {
			res = Result{State: evidence.StateNotInspected, Reason: evidence.ReasonInspectorError, Panic: fmt.Sprintf("%v\n%s", v, debug.Stack()), FailReason: FailOther}
		} else if ctx.Err() != nil {
			res = Result{State: evidence.StateNotInspected, Reason: evidence.ReasonTimeout, FailReason: FailDeadline}
		}
		res.Detected = kind
	}()
	kind = Detect(r, size)
	f := pick(kind, strings.TrimPrefix(strings.ToUpper(declared), "ORIGINAL_"))
	if f == nil {
		return Result{State: evidence.StateNotInspected, Reason: evidence.ReasonNoInspector}
	}
	return f(ctx, r, size, lim.withDefaults())
}

func pick(kind, declared string) inspector {
	switch kind {
	case "zip":
		switch declared {
		case "EPUB", "KEPUB":
			return inspectors["epub"]
		case "CBZ":
			return inspectors["cbz"]
		}
		return nil
	case "palmdoc":
		return inspectors["mobi"]
	case "rar":
		return nil
	case "unknown":
		return inspectors[declaredKinds[declared]]
	}
	return inspectors[kind]
}

// File inspects the file at path. The error is only for a file that can't be
// opened or stat'd; everything wrong inside the file is in the Result.
func File(ctx context.Context, declared, path string, lim Limits) (Result, error) {
	f, err := os.Open(path) //nolint:gosec // the caller's own scratch file
	if err != nil {
		return Result{}, fmt.Errorf("inspect: %w", err)
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return Result{}, fmt.Errorf("inspect: %w", err)
	}
	return Inspect(ctx, declared, f, st.Size(), lim), nil
}
