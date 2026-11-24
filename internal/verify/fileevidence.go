package verify

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"golang.org/x/sync/semaphore"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/inspect"
	"github.com/felixapel/calibre-bookwarden/internal/library"
)

// formatTimeout bounds one format's export and inspection together (spec
// section 10: time per file).
var formatTimeout = 120 * time.Second

// inspectFile is inspect.File; tests replace it.
var inspectFile = inspect.File

// fileEvidence downloads and inspects every format and the cover of b, one
// at a time under the byte budget.
func (c checker) fileEvidence(ctx context.Context, ref library.BookRef, b library.Book) ([]evidence.FormatEvidence, *evidence.CoverEvidence, map[string]string, error) {
	dir, err := os.MkdirTemp(c.scratch, fmt.Sprintf("book-%d-", ref.ID))
	if err != nil {
		return nil, nil, nil, &bookFailed{reasonScratchError, err}
	}
	defer func() { _ = os.RemoveAll(dir) }()

	ordered := slices.SortedFunc(slices.Values(b.Formats), func(x, y library.Format) int { return strings.Compare(x.Name, y.Name) })
	formats := make([]evidence.FormatEvidence, 0, len(ordered))
	now := c.clock()
	hints := bookHints{hinter: c.hinter, languages: b.Languages, left: c.lim.HintTime, now: now}
	start := now()
	defer func() {
		c.stats.stage(StageExportInspect, now().Sub(start)-hints.spent)
		c.stats.stage(StageOCR, hints.spent)
	}()
	bodies := map[string]string{}
	for _, f := range ordered {
		fe, err := checkFormat(ctx, c.lib, c.libraryID, b.ID, f, filepath.Join(dir, "format"), c.lim, c.budget, c.logger, &hints)
		if err != nil {
			return nil, nil, nil, err
		}
		if e := fe.Excerpt; e != nil {
			bodies[e.SHA256] = e.Text
		}
		formats = append(formats, fe)
	}

	cover, err := checkCover(ctx, c.lib, c.libraryID, b, filepath.Join(dir, "cover"), c.lim, c.budget, c.logger)
	if err != nil {
		return nil, nil, nil, err
	}
	return formats, cover, bodies, nil
}

// checkFormat downloads one format to path, hashes and inspects it, and
// deletes it again before returning. The format's listed size is held from
// the budget for the whole time its bytes are in flight or on scratch.
func checkFormat(ctx context.Context, lib Library, libraryID string, bookID int, f library.Format, path string, lim Limits, budget *semaphore.Weighted, logger *slog.Logger, hints *bookHints) (evidence.FormatEvidence, error) {
	fe := evidence.FormatEvidence{Format: f.Name, Size: f.Size, State: evidence.StateNotInspected}
	switch {
	case f.Missing:
		// Never requested: if the file came back, Calibre would load it whole
		// without a size to check against the limit.
		fe.State, fe.Reason = evidence.StateMissing, evidence.ReasonMissingOnDisk
		return fe, nil
	case f.Size < 0:
		fe.Reason = evidence.ReasonSizeUnknown
		return fe, nil
	case f.Size > lim.FileSize:
		fe.Reason = evidence.ReasonTooLarge
		return fe, nil
	}
	if err := budget.Acquire(ctx, f.Size); err != nil {
		return fe, fmt.Errorf("verify: wait for the byte budget: %w", err)
	}
	defer budget.Release(f.Size)

	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) //nolint:gosec // path is inside our own scratch directory
	if err != nil {
		return fe, &bookFailed{reasonScratchError, err}
	}
	defer func() { _ = os.Remove(path) }()
	w := &trackedWriter{w: file}

	fctx, cancel := context.WithTimeout(ctx, formatTimeout)
	defer cancel()
	res, err := lib.ExportFormatInLibrary(fctx, libraryID, bookID, f.Name, w, f.Size)
	closeErr := file.Close()
	switch {
	case err != nil && ctx.Err() == nil && errors.Is(fctx.Err(), context.DeadlineExceeded):
		fe.Reason = evidence.ReasonTimeout
		return fe, nil
	case err != nil && w.err != nil:
		return fe, &bookFailed{reasonScratchError, err}
	case err != nil:
		return fe, err
	case closeErr != nil:
		return fe, &bookFailed{reasonScratchError, closeErr}
	}

	switch res.Status {
	case library.ExportOK:
		fe.SHA256 = res.SHA256
		if res.Bytes == 0 {
			fe.State = evidence.StateEmpty
			break
		}
		r, err := inspectFile(fctx, f.Name, path, lim.Inspect)
		if err != nil {
			return fe, &bookFailed{reasonScratchError, err}
		}
		if r.Panic != "" {
			logger.Error("a file inspector failed; the format is recorded as not inspected", "book", bookID, "format", f.Name, "panic", r.Panic)
		}
		if r.FailReason != "" {
			// Why a scan failed or never ran. It is not sealed, so this line
			// is its only trace; it holds no text and no ISBN.
			logger.Debug("a format scan did not finish ok", "book", bookID, "format", f.Name, "state", r.State, "reason", r.FailReason)
		}
		fe.State, fe.Reason, fe.Detected, fe.Text, fe.Metadata = r.State, r.Reason, r.Detected, r.Text, r.Metadata
		fe.FileCover = r.FileCover
		// The run's context, not the format's: OCR has its own budget.
		fe.ISBNHints, fe.Excerpt = hints.read(ctx, path, r)
		if fe.Excerpt != nil && evidence.ExcerptMethodFor(f.Name) != fe.Excerpt.Method {
			// A PDF stored under an EPUB name keeps its hints; its excerpt
			// would not open (the method must fit the format).
			fe.Excerpt = nil
		}
	case library.ExportNotInCalibre:
		fe.State, fe.Reason = evidence.StateMissing, evidence.ReasonNotInCalibre
	case library.ExportMissingOnDisk:
		fe.State, fe.Reason = evidence.StateMissing, evidence.ReasonMissingOnDisk
	case library.ExportSizeMismatch:
		fe.Reason = evidence.ReasonSizeMismatch
	}
	return fe, nil
}

// checkCover exports and measures a book's cover like a format (ADR-023):
// capped, under the byte budget and the per-file deadline, and deleted again
// before returning. A book Calibre lists without a cover isn't asked for one.
func checkCover(ctx context.Context, lib Library, libraryID string, b library.Book, path string, lim Limits, budget *semaphore.Weighted, logger *slog.Logger) (*evidence.CoverEvidence, error) {
	ce := &evidence.CoverEvidence{State: evidence.StateNotInspected, Flatness: -1}
	if !b.HasCover {
		ce.State = evidence.StateMissing
		return ce, nil
	}
	limit := min(library.CoverCap, lim.FileSize)
	if err := budget.Acquire(ctx, limit); err != nil {
		return ce, fmt.Errorf("verify: wait for the byte budget: %w", err)
	}
	defer budget.Release(limit)

	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) //nolint:gosec // path is inside our own scratch directory
	if err != nil {
		return ce, &bookFailed{reasonScratchError, err}
	}
	defer func() { _ = os.Remove(path) }()
	w := &trackedWriter{w: file}

	fctx, cancel := context.WithTimeout(ctx, formatTimeout)
	defer cancel()
	res, err := lib.ExportCoverInLibrary(fctx, libraryID, b.ID, w, limit)
	closeErr := file.Close()
	switch {
	case err != nil && ctx.Err() == nil && errors.Is(fctx.Err(), context.DeadlineExceeded):
		ce.Reason = evidence.ReasonTimeout
		return ce, nil
	case err != nil && w.err != nil:
		return ce, &bookFailed{reasonScratchError, err}
	case err != nil:
		return ce, err
	case closeErr != nil:
		return ce, &bookFailed{reasonScratchError, closeErr}
	}
	switch res.Status {
	case library.ExportOK:
		ce.Bytes, ce.SHA256 = res.Bytes, res.SHA256
		r := inspect.Cover(func() (io.ReadCloser, error) { return os.Open(path) }, lim.Inspect) //nolint:gosec // path is inside our own scratch directory
		if r.Panic != "" {
			logger.Error("the cover inspector failed; the cover is recorded as not inspected", "book", b.ID, "panic", r.Panic)
		}
		if fctx.Err() != nil {
			ce.Reason = evidence.ReasonTimeout
			return ce, nil
		}
		ce.State, ce.Reason, ce.Detected, ce.Width, ce.Height, ce.DHash, ce.Flatness = r.State, r.Reason, r.Detected, r.Width, r.Height, r.DHash, r.Flatness
	case library.ExportNotInCalibre:
		ce.State, ce.Reason = evidence.StateMissing, evidence.ReasonMissingOnDisk
	case library.ExportTooLarge:
		ce.Reason = evidence.ReasonTooLarge
	default:
		ce.Reason = evidence.ReasonSizeMismatch
	}
	return ce, nil
}

// bookHints carries one book's hinter and its remaining tool time across
// the book's formats.
type bookHints struct {
	hinter    Hinter
	languages []string // the Calibre record's, which pick the OCR model
	left      time.Duration
	// now is the clock; spent adds up the hinter's time, which counts as
	// the run's OCR stage.
	now   func() time.Time
	spent time.Duration
}

// read returns the ISBN hints of a readable, unencrypted PDF whose text layer
// is known to be present or absent; of a readable EPUB, the ones the inspector
// took from its front matter, which cost no tool time (ADR-032); and nil for
// anything else. The excerpt (ADR-032 part 2) comes with the same read: an
// EPUB's front-matter text, or a PDF text layer's pages. OCR text is never
// kept, so a hinter's excerpt counts only for a text layer.
func (h *bookHints) read(ctx context.Context, path string, r inspect.Result) (*evidence.ISBNHints, *evidence.Excerpt) {
	if r.State == evidence.StateOK && r.ISBNHints != nil && r.ISBNHints.Method == evidence.HintEPUBText {
		// The inspector scanned it, whatever the bytes were detected as.
		var ex *evidence.Excerpt
		if r.Excerpt != nil && r.Excerpt.Method == evidence.ExcerptEPUBText {
			ex = r.Excerpt
		}
		return r.ISBNHints, ex
	}
	if h.hinter == nil || r.State != evidence.StateOK || r.Detected != "pdf" || r.Reason == evidence.ReasonEncrypted {
		return nil, nil
	}
	var textLayer bool
	switch r.Text {
	case evidence.TextPresent:
		textLayer = true
	case evidence.TextAbsent:
	default:
		return nil, nil
	}
	start := h.now()
	defer func() { h.spent += h.now().Sub(start) }()
	if eh, ok := h.hinter.(ExcerptHinter); ok {
		hints, ex := eh.PDFHintsExcerpt(ctx, path, r.Pages, textLayer, h.languages, &h.left)
		if hints == nil || hints.Method != evidence.HintTextLayer || ex == nil || ex.Method != evidence.ExcerptTextLayer {
			ex = nil
		}
		return hints, ex
	}
	return h.hinter.PDFHints(ctx, path, r.Pages, textLayer, h.languages, &h.left), nil
}

// trackedWriter remembers its own write error, so a full scratch disk is
// told apart from Calibre failing.
type trackedWriter struct {
	w   io.Writer
	err error
}

func (t *trackedWriter) Write(p []byte) (int, error) {
	n, err := t.w.Write(p)
	if err != nil {
		t.err = err
	}
	return n, err
}
