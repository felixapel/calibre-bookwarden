package ocr

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/sandbox"
)

// selfCheckPDF draws its ISBN as outlines, so only OCR can read it
// (testdata/gen-selfcheck.sh).
//
//go:embed testdata/selfcheck.pdf
var selfCheckPDF []byte

const (
	selfCheckISBN = "9780000000064"
	selfCheckTime = 60 * time.Second
)

// SelfCheck runs the whole OCR path, sandbox included, on an embedded PDF in
// a directory under scratch. Until it passes, every PDF's hints are
// unavailable.
func (e *Engine) SelfCheck(ctx context.Context, scratch string) error {
	e.available.Store(false)
	if err := sandbox.Supported(); err != nil {
		return err
	}
	dir, err := os.MkdirTemp(scratch, "ocr-check-")
	if err != nil {
		return fmt.Errorf("ocr: self-check scratch: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	path := filepath.Join(dir, "check.pdf")
	if err := os.WriteFile(path, selfCheckPDF, 0o600); err != nil {
		return fmt.Errorf("ocr: self-check scratch: %w", err)
	}
	left := selfCheckTime
	// Availability flips only once the check has passed, so books processed
	// meanwhile get unavailable hints, not empty ok ones.
	h := e.hints(ctx, path, 1, &evidence.ISBNHints{Method: evidence.HintOCR, Pages: []int{}, Lang: defaultLang}, &left, nil)
	if h.State != evidence.HintOK || !slices.Equal(h.ISBNs, []string{selfCheckISBN}) {
		return fmt.Errorf("ocr: self-check read %s %v, want ok [%s]", h.State, h.ISBNs, selfCheckISBN)
	}
	e.available.Store(true)
	return nil
}
