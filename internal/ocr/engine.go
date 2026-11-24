// Package ocr turns a PDF's first and last pages into ISBN hints, from the
// text layer when there is one and by OCR when there isn't (ADR-018, ADR-032).
// Every tool runs in the sandbox, one at a time. OCR text is never kept; a text
// layer's pages become a bounded excerpt (ADR-032 part 2).
package ocr

import (
	"cmp"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/findings"
	"github.com/felixapel/calibre-bookwarden/internal/sandbox"
)

// Tools are the absolute paths of the native tools and the tesseract models.
type Tools struct{ PDFToPPM, PDFToText, Tesseract, TessdataDir string }

// DefaultTools are the paths in the bookwarden image.
var DefaultTools = Tools{
	PDFToPPM:    "/usr/bin/pdftoppm",
	PDFToText:   "/usr/bin/pdftotext",
	Tesseract:   "/usr/bin/tesseract",
	TessdataDir: "/usr/share/tessdata",
}

const (
	// A text layer is cheap, so it is read from the first 12 pages and the
	// last 3; OCR is not, so it reads the first 5 and the last 5.
	textHead, textTail = 12, 3
	ocrHead, ocrTail   = 5, 5

	maxPages  = 100_000 // a larger stated count is treated as unknown
	maxStdout = 1 << 20
	longSide  = "3000" // pixels of a rendered page's longer side
)

// defaultLang is the tesseract model for an unknown or unsupported language
// and for the self-check.
const defaultLang = "eng"

// Language returns the tesseract model for a book whose Calibre record lists
// languages (ISO 639-2, or ISO 639-1): the first listed language the image
// has a model for, and "eng" if there is none.
func Language(languages []string) string {
	for _, l := range languages {
		switch strings.ToLower(strings.TrimSpace(l)) {
		case "eng", "en":
			return "eng"
		case "deu", "ger", "de":
			return "deu"
		case "spa", "es":
			return "spa"
		}
	}
	return defaultLang
}

// Engine runs the tools. It is safe for concurrent use; tool runs are
// serialised.
type Engine struct {
	tools     Tools
	slot      chan struct{}
	available atomic.Bool
	run       func(context.Context, sandbox.Spec) ([]byte, error)
}

// New returns an engine that reports every PDF as unavailable until
// SelfCheck passes.
func New(tools Tools) *Engine {
	return &Engine{tools: tools, slot: make(chan struct{}, 1), run: sandbox.Run}
}

// pagesToScan returns the first head and the last tail pages of a count-page
// document, or the first head pages when the count is unknown.
func pagesToScan(count, head, tail int) []int {
	if count <= 0 || count > maxPages {
		count = head
	}
	var pages []int
	for p := 1; p <= count; p++ {
		if p <= head || p > count-tail {
			pages = append(pages, p)
		}
	}
	return pages
}

// PDFHints reads the PDF at path: its text layer when textLayer is set,
// otherwise by OCR in the model for the first of the book's languages that
// has one (see Language). left is the book's remaining tool time; it is
// charged only while this call holds the tool slot, never while it waits for it.
func (e *Engine) PDFHints(ctx context.Context, path string, pages int, textLayer bool, languages []string, left *time.Duration) *evidence.ISBNHints {
	h, _ := e.PDFHintsExcerpt(ctx, path, pages, textLayer, languages, left)
	return h
}

// PDFHintsExcerpt is PDFHints that also returns the text layer's bounded
// excerpt (ADR-032 part 2), built from the same pdftotext output: the pages
// read, in page order. OCR text is noisy and never kept, so an OCR read, an
// unavailable engine and a read that got no text return a nil excerpt.
func (e *Engine) PDFHintsExcerpt(ctx context.Context, path string, pages int, textLayer bool, languages []string, left *time.Duration) (*evidence.ISBNHints, *evidence.Excerpt) {
	h := &evidence.ISBNHints{Method: evidence.HintOCR, Pages: []int{}, Lang: Language(languages)}
	if textLayer {
		h.Method, h.Lang = evidence.HintTextLayer, ""
	}
	if !e.available.Load() {
		h.State = evidence.HintUnavailable
		return h, nil
	}
	var excerpt *evidence.ExcerptBuilder
	if textLayer {
		excerpt = evidence.NewExcerptBuilder(evidence.ExcerptTextLayer)
	}
	h = e.hints(ctx, path, pages, h, left, excerpt)
	if excerpt == nil || h.State == evidence.HintTimeout {
		// How far a deadline got must not decide which text is sealed.
		return h, nil
	}
	return h, excerpt.Excerpt()
}

// hints runs the tools for h, whether or not the engine is available yet:
// the self-check uses it before it may flip availability.
func (e *Engine) hints(ctx context.Context, path string, pages int, h *evidence.ISBNHints, left *time.Duration, excerpt *evidence.ExcerptBuilder) *evidence.ISBNHints {
	textLayer := h.Method == evidence.HintTextLayer
	select {
	case e.slot <- struct{}{}:
	case <-ctx.Done():
		h.State = evidence.HintTimeout
		return h
	}
	defer func() { <-e.slot }()
	if *left <= 0 {
		h.State = evidence.HintTimeout
		return h
	}
	start := time.Now()
	defer func() { *left -= time.Since(start) }()
	tctx, cancel := context.WithDeadline(ctx, start.Add(*left))
	defer cancel()

	h.State = evidence.HintOK
	if textLayer {
		e.readTextLayer(tctx, path, pagesToScan(pages, textHead, textTail), h, excerpt)
	} else {
		e.readByOCR(tctx, path, pagesToScan(pages, ocrHead, ocrTail), h)
	}
	return h
}

func (e *Engine) spec(program, dir string, args ...string) sandbox.Spec {
	return sandbox.Spec{
		Program: program, Args: args, Dir: dir,
		Env:    []string{"OMP_THREAD_LIMIT=1"},
		Limits: sandbox.DefaultLimits, MaxStdout: maxStdout,
	}
}

// note records a tool error on h and reports whether to stop.
func note(h *evidence.ISBNHints, err error) (stop bool) {
	if errors.Is(err, sandbox.ErrTimeout) {
		h.State = evidence.HintTimeout
		return true
	}
	h.State = evidence.HintFailed
	return false
}

// addISBNs keeps the ISBNs in out that h doesn't have yet, each with its
// qualifier; a PDF page has no landmark.
func addISBNs(h *evidence.ISBNHints, out []byte) {
	for _, m := range findings.ScanISBNs(string(out)) {
		if i := slices.Index(h.ISBNs, m.ISBN); i >= 0 {
			// A later page's label is better than none.
			if h.Context[i].Qualifier == "" {
				h.Context[i].Qualifier = m.Qualifier
			}
		} else if len(h.ISBNs) < findings.MaxISBNs {
			h.ISBNs = append(h.ISBNs, m.ISBN)
			h.Context = append(h.Context, evidence.ISBNContext{ISBN: m.ISBN, Qualifier: m.Qualifier})
		}
	}
}

// addPages adds each page of a pdftotext range to the excerpt. The tool ends
// every page with a form feed; output that doesn't hold exactly one per page
// can't be mapped to page numbers: it adds nothing and leaves the excerpt
// incomplete.
func addPages(excerpt *evidence.ExcerptBuilder, pages []int, out []byte) {
	if excerpt == nil {
		return
	}
	texts := strings.Split(string(out), "\f")
	if len(texts) != len(pages)+1 || strings.TrimSpace(texts[len(pages)]) != "" {
		excerpt.MarkIncomplete()
		return
	}
	for i, p := range pages {
		excerpt.Add(p, "", texts[i])
	}
}

// readTextLayer runs pdftotext once per contiguous range of pages.
func (e *Engine) readTextLayer(ctx context.Context, path string, pages []int, h *evidence.ISBNHints, excerpt *evidence.ExcerptBuilder) {
	for len(pages) > 0 {
		n := 1
		for n < len(pages) && pages[n] == pages[n-1]+1 {
			n++
		}
		first, last := strconv.Itoa(pages[0]), strconv.Itoa(pages[n-1])
		out, err := e.run(ctx, e.spec(e.tools.PDFToText, filepath.Dir(path), "-q", "-enc", "UTF-8", "-f", first, "-l", last, path, "-"))
		if err != nil {
			if note(h, err) {
				return
			}
			if excerpt != nil {
				// This range adds no text.
				excerpt.MarkIncomplete()
			}
		} else {
			h.Pages = append(h.Pages, pages[:n]...)
			addISBNs(h, out)
			addPages(excerpt, pages[:n], out)
		}
		pages = pages[n:]
	}
}

// readByOCR renders one page at a time into the book's scratch directory,
// reads it with tesseract and deletes it before the next page.
func (e *Engine) readByOCR(ctx context.Context, path string, pages []int, h *evidence.ISBNHints) {
	dir := filepath.Dir(path)
	base := filepath.Join(dir, "page")
	image := base + ".png"
	defer func() { _ = os.Remove(image) }()
	for _, p := range pages {
		_ = os.Remove(image)
		page := strconv.Itoa(p)
		_, err := e.run(ctx, e.spec(e.tools.PDFToPPM, dir, "-gray", "-png", "-scale-to", longSide, "-f", page, "-l", page, "-singlefile", path, base))
		var out []byte
		if err == nil {
			out, err = e.run(ctx, e.spec(e.tools.Tesseract, dir, image, "stdout", "--tessdata-dir", e.tools.TessdataDir, "-l", cmp.Or(h.Lang, defaultLang), "--psm", "3"))
		}
		_ = os.Remove(image)
		if err != nil {
			if note(h, err) {
				return
			}
			continue
		}
		h.Pages = append(h.Pages, p)
		addISBNs(h, out)
	}
}
