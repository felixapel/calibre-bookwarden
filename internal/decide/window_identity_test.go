package decide_test

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/decide"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/inspect"
	"github.com/felixapel/calibre-bookwarden/internal/inspect/inspecttest"
)

// A copyright page that a landmark with a #fragment labels as a whole file is
// not windowed by the ISBN scan, so the e-book ISBN printed deep in it is seen
// and v6 picks it, not the unlabelled print ISBN printed first (review C1).
func TestFrontMatterIdentityFromALongCopyrightFile(t *testing.T) {
	filler := strings.Repeat("lorem ipsum dolor sit amet ", 1600) // about 42 KiB
	copyright := `<?xml version="1.0" encoding="utf-8"?><html xmlns="http://www.w3.org/1999/xhtml"><head><title>T</title></head><body>` +
		`<p>Print edition ISBN 978-0-306-40615-7</p><p>` + filler + `</p><p>ISBN 978-3-551-55167-2 (ebook)</p></body></html>`
	nav := `<?xml version="1.0" encoding="utf-8"?><html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops"><head><title>N</title></head><body>` +
		`<nav epub:type="landmarks"><ol><li><a epub:type="copyright-page" href="text/extra0.xhtml#c">x</a></li><li><a epub:type="bodymatter" href="text/extra1.xhtml">x</a></li></ol></nav></body></html>`
	data := inspecttest.EPUB(inspecttest.EPUBOptions{
		Title: T, EPUB3: true, Nav: nav, Extra: []string{copyright, `<html><body><p>chapter</p></body></html>`},
		ContentDoc: `<html><body><p>Title</p></body></html>`,
	})
	res := inspect.Inspect(context.Background(), "EPUB", bytes.NewReader(data), int64(len(data)), inspect.Limits{})
	h := res.ISBNHints
	if h == nil || h.State != evidence.HintOK || !slices.Equal(h.ISBNs, []string{I2, I}) {
		t.Fatalf("hints %+v", h)
	}
	d, err := decide.Decide(t.Context(), frontPkg(h.Context, ok(evidence.SourceDNB, dnbT())))
	if err != nil {
		t.Fatal(err)
	}
	// The DNB record is the ebook ISBN's: the identity is that one.
	if want := []string{"identity_frontmatter", "isbn_missing"}; !slices.Equal(d.Reasons, want) {
		t.Fatalf("reasons %v, want %v", d.Reasons, want)
	}
}
