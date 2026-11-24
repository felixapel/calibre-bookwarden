package inspect

import (
	"context"
	"testing"
	"time"
)

// ExcerptText is excerptText.
var ExcerptText = excerptText

// HTMLBodyText is htmlBodyText.
var HTMLBodyText = htmlBodyText

// NavLandmarks returns each landmark's type and href, and whether the nav
// parsed to its end.
func NavLandmarks(raw []byte) (refs [][2]string, ok bool) {
	out, ok := navLandmarks(raw)
	for _, r := range out {
		refs = append(refs, [2]string{r.typ, r.href})
	}
	return refs, ok
}

// NavPaths returns the nav documents an OPF lists, as the scan reads them.
func NavPaths(opf []byte, dir string) ([]string, error) {
	p, err := parseOPF(opf)
	if err != nil {
		return nil, err
	}
	return navPaths(p, dir), nil
}

// SetScanMargin replaces the time the scan leaves before the file's deadline.
func SetScanMargin(t testing.TB, d time.Duration) {
	old := scanMargin
	scanMargin = d
	t.Cleanup(func() { scanMargin = old })
}

// SetScanContext replaces how the scan derives its own context from the
// file's deadline, so that a test ends the scan without waiting for a clock.
func SetScanContext(t testing.TB, f func(parent context.Context, deadline time.Time) (context.Context, context.CancelFunc)) {
	old := scanContext
	scanContext = f
	t.Cleanup(func() { scanContext = old })
}

// SetScanDocHook runs f after each document the scan reads.
func SetScanDocHook(t testing.TB, f func()) {
	scanDocHook = f
	t.Cleanup(func() { scanDocHook = nil })
}

// SetScanTextHook runs f after the scan has read a document's ISBN text and
// before it reads the same document's excerpt text.
func SetScanTextHook(t testing.TB, f func()) {
	scanTextHook = f
	t.Cleanup(func() { scanTextHook = nil })
}

// SetScanBudget replaces the text the whole scan may read.
func SetScanBudget(t testing.TB, n int) {
	old := scanBudget
	scanBudget = n
	t.Cleanup(func() { scanBudget = old })
}

// DocScanWindow is the text each document contributes to the ISBN scan.
const DocScanWindow = docScanWindow
