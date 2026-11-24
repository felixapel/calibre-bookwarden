package inspect_test

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/inspect"
	"github.com/felixapel/calibre-bookwarden/internal/inspect/inspecttest"
)

// Valid synthetic ISBN-13s.
const (
	isbnA = "9780000000040"
	isbnB = "9780000000064"
	isbnC = "9780000000026"
)

func page(body string) string {
	return `<?xml version="1.0" encoding="utf-8"?>
<html xmlns="http://www.w3.org/1999/xhtml"><head><title>T</title></head><body>` + body + `</body></html>`
}

func inspectEPUBOptions(o inspecttest.EPUBOptions) inspect.Result {
	data := inspecttest.EPUB(o)
	return inspect.Inspect(context.Background(), "EPUB", bytes.NewReader(data), int64(len(data)), inspect.Limits{})
}

// book builds an EPUB 3 whose spine is first, then extra, with the nav's
// landmarks.
func book(first string, extra []string, nav string) inspecttest.EPUBOptions {
	return inspecttest.EPUBOptions{Title: "Binary Tides", EPUB3: true, ContentDoc: first, Extra: extra, Nav: nav}
}

func landmarkNav(entries ...string) string {
	var b strings.Builder
	for _, e := range entries {
		b.WriteString(`<li>` + e + `</li>`)
	}
	return `<?xml version="1.0" encoding="utf-8"?>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops"><head><title>Nav</title></head><body>
<nav epub:type="toc"><ol><li><a epub:type="bodymatter" href="text/extra5.xhtml">Not a landmark</a></li></ol></nav>
<nav epub:type="landmarks"><ol>` + b.String() + `</ol></nav></body></html>`
}

func landmarkLink(typ, href string) string {
	return `<a epub:type="` + typ + `" href="` + href + `">x</a>`
}

// filler returns n documents with no ISBN, then puts docs at the given indexes.
func filler(n int, at map[int]string) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = page(fmt.Sprintf("<p>Filler paragraph %d.</p>", i))
		if d, ok := at[i]; ok {
			out[i] = d
		}
	}
	return out
}

func TestEPUBFrontMatterHints(t *testing.T) {
	ctxISBN := func(isbn, landmark, qualifier string) evidence.ISBNContext {
		return evidence.ISBNContext{ISBN: isbn, Landmark: landmark, Qualifier: qualifier}
	}
	tests := []struct {
		name string
		opts inspecttest.EPUBOptions
		want evidence.ISBNHints
	}{
		{
			// Spine: 1 title page, 2 copyright, 3 body start, 4-5 middle, 6-8 the last three.
			name: "EPUB 3 landmarks label the title and copyright pages",
			opts: book(
				page(`<p>Binary Tides, Print ISBN 978-0-00-000006-4</p>`),
				filler(7, map[int]string{
					0: page(`<p>Copyright. eBook ISBN 978-0-00-000004-0.</p>`),
					2: page(`<p>Middle: ISBN 978-0-00-000008-8 is in no front or back document.</p>`),
					6: page(`<p>Also by the author: Paperback ISBN 978-0-00-000002-6</p>`),
				}),
				landmarkNav(landmarkLink("titlepage", "text/chapter%201.xhtml"), landmarkLink("copyright-page", "text/extra0.xhtml"), landmarkLink("bodymatter", "text/extra1.xhtml#start")),
			),
			want: evidence.ISBNHints{
				Method: evidence.HintEPUBText, State: evidence.HintOK, Pages: []int{1, 2, 6, 7, 8},
				ISBNs: []string{isbnB, isbnA, isbnC},
				Context: []evidence.ISBNContext{
					ctxISBN(isbnB, evidence.LandmarkTitle, evidence.QualifierPrint),
					ctxISBN(isbnA, evidence.LandmarkCopyright, evidence.QualifierEbook),
					ctxISBN(isbnC, evidence.LandmarkBack, evidence.QualifierPaperback),
				},
			},
		},
		{
			name: "EPUB 2 guide references label the same documents",
			opts: with(book(
				page(`<p>Title page</p>`),
				filler(5, map[int]string{0: page(`<p>Copyright. E-Book ISBN 978-0-00-000004-0</p>`)}),
				"",
			), func(o *inspecttest.EPUBOptions) {
				o.EPUB3 = false
				o.Guide = `<guide><reference type="title-page" title="Title" href="text/chapter%201.xhtml"/>` +
					`<reference type="copyright-page" title="Copyright" href="text/extra0.xhtml"/>` +
					`<reference type="text" title="Start" href="text/extra1.xhtml"/></guide>`
			}),
			want: evidence.ISBNHints{
				Method: evidence.HintEPUBText, State: evidence.HintOK, Pages: []int{1, 2, 4, 5, 6},
				ISBNs:   []string{isbnA},
				Context: []evidence.ISBNContext{ctxISBN(isbnA, evidence.LandmarkCopyright, evidence.QualifierEbook)},
			},
		},
		{
			// 12 documents and no landmark: documents 1-6 are front matter, 10-12 the end.
			name: "without landmarks the first 6 and the last 3 documents are read",
			opts: book(page(`<p>Plain</p>`), filler(11, map[int]string{
				4:  page(`<p>Front, document 6: ISBN 978-0-00-000004-0</p>`),
				5:  page(`<p>Document 7 is neither: ISBN 978-0-00-000006-4</p>`),
				10: page(`<p>Document 12: PDF ISBN 978-0-00-000002-6</p>`),
			}), ""),
			want: evidence.ISBNHints{
				Method: evidence.HintEPUBText, State: evidence.HintOK, Pages: []int{1, 2, 3, 4, 5, 6, 10, 11, 12},
				ISBNs: []string{isbnA, isbnC},
				Context: []evidence.ISBNContext{
					ctxISBN(isbnA, evidence.LandmarkFront, ""),
					ctxISBN(isbnC, evidence.LandmarkBack, evidence.QualifierPDF),
				},
			},
		},
		{
			name: "the first sighting keeps its order, the copyright page its context",
			opts: book(
				page(`<p>Half title. ISBN 978-0-00-000004-0 and ISBN 978-0-00-000006-4</p>`),
				filler(7, map[int]string{0: page(`<p>Imprint. Paperback ISBN 978-0-00-000004-0</p>`)}),
				landmarkNav(landmarkLink("copyright-page", "text/extra0.xhtml"), landmarkLink("bodymatter", "text/extra1.xhtml")),
			),
			want: evidence.ISBNHints{
				Method: evidence.HintEPUBText, State: evidence.HintOK, Pages: []int{1, 2, 6, 7, 8},
				ISBNs: []string{isbnA, isbnB},
				Context: []evidence.ISBNContext{
					ctxISBN(isbnA, evidence.LandmarkCopyright, evidence.QualifierPaperback),
					ctxISBN(isbnB, evidence.LandmarkFront, ""),
				},
			},
		},
		{
			name: "a front document seen first keeps its context when no copyright page repeats it",
			opts: book(
				page(`<p>Hardcover ISBN 978-0-00-000004-0</p>`),
				filler(7, map[int]string{0: page(`<p>Imprint. ebook ISBN 978-0-00-000004-0</p>`)}),
				"",
			),
			want: evidence.ISBNHints{
				Method: evidence.HintEPUBText, State: evidence.HintOK, Pages: []int{1, 2, 3, 4, 5, 6, 7, 8},
				ISBNs:   []string{isbnA},
				Context: []evidence.ISBNContext{ctxISBN(isbnA, evidence.LandmarkFront, evidence.QualifierHardcover)},
			},
		},
		{
			name: "no ISBN in the front matter",
			opts: book(page(`<p>Nothing here</p>`), filler(1, nil), ""),
			want: evidence.ISBNHints{Method: evidence.HintEPUBText, State: evidence.HintOK, Pages: []int{1, 2}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := inspectEPUBOptions(tc.opts)
			if res.State != evidence.StateOK {
				t.Fatalf("state %s/%s", res.State, res.Reason)
			}
			if !reflect.DeepEqual(res.ISBNHints, &tc.want) {
				t.Fatalf("hints\n got %+v\nwant %+v", res.ISBNHints, &tc.want)
			}
		})
	}
}

func TestEPUBFrontMatterBounds(t *testing.T) {
	t.Run("more than 12 front documents reads 12", func(t *testing.T) {
		// A body landmark on the last document makes all earlier ones front matter.
		extra := filler(19, map[int]string{12: page(`<p>ISBN 978-0-00-000008-8</p>`)}) // spine position 14
		o := book(page(`<p>x</p>`), extra, landmarkNav(landmarkLink("bodymatter", "text/extra18.xhtml")))
		res := inspectEPUBOptions(o)
		want := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 18, 19, 20}
		if h := res.ISBNHints; h == nil || h.State != evidence.HintOK || !reflect.DeepEqual(h.Pages, want) || len(h.ISBNs) != 0 {
			t.Fatalf("hints %+v, want pages %v", h, want)
		}
	})
	t.Run("the text budget marks the scan failed", func(t *testing.T) {
		// The first document uses the whole budget, cut at the window; the
		// next is never read.
		inspect.SetScanBudget(t, inspect.DocScanWindow)
		big := page(`<p>ISBN 978-0-00-000004-0</p><p>` + strings.Repeat("lorem ipsum ", 30_000) + `</p><p>ISBN 978-0-00-000006-4</p>`)
		res := inspectEPUBOptions(book(big, filler(2, nil), ""))
		h := res.ISBNHints
		if h == nil || h.State != evidence.HintFailed || len(h.ISBNs) != 1 || h.ISBNs[0] != isbnA {
			t.Fatalf("hints %+v, want failed with only the first ISBN", h)
		}
	})
	t.Run("a document that doesn't parse marks the scan failed", func(t *testing.T) {
		broken := `<html><body><p>ISBN 978-0-00-000004-0</p><p class="x`
		res := inspectEPUBOptions(book(page(`<p>Title</p>`), []string{broken, page(`<p>Next ISBN 978-0-00-000006-4</p>`)}, ""))
		h := res.ISBNHints
		if h == nil || h.State != evidence.HintFailed || !reflect.DeepEqual(h.ISBNs, []string{isbnA, isbnB}) || !reflect.DeepEqual(h.Pages, []int{1, 3}) {
			t.Fatalf("hints %+v, want failed, both ISBNs, pages [1 3]", h)
		}
	})
	t.Run("scripts and styles are not text", func(t *testing.T) {
		res := inspectEPUBOptions(book(page(`<script>var isbn = "978-0-00-000004-0";</script><style>.a{}</style><p>Hi</p>`), nil, ""))
		if h := res.ISBNHints; h == nil || len(h.ISBNs) != 0 {
			t.Fatalf("hints %+v", h)
		}
	})
}

func TestEPUBFrontMatterNotScanned(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts inspecttest.EPUBOptions
	}{
		{"adept rights", with(book(page(`<p>ISBN 978-0-00-000004-0</p>`), nil, ""), func(o *inspecttest.EPUBOptions) { o.Rights = true })},
		{"encrypted content", with(book(page(`<p>ISBN 978-0-00-000004-0</p>`), nil, ""), func(o *inspecttest.EPUBOptions) {
			o.Encryption = inspecttest.EncryptionXML("http://www.w3.org/2001/04/xmlenc#aes128-cbc", "OEBPS/text/chapter%201.xhtml")
		})},
		{"a missing chapter", with(book(page(`<p>x</p>`), nil, ""), func(o *inspecttest.EPUBOptions) { o.NoChapter = true })},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if res := inspectEPUBOptions(tc.opts); res.ISBNHints != nil {
				t.Fatalf("state %s: hints %+v, want none", res.State, res.ISBNHints)
			}
		})
	}
}

func TestEPUBFrontMatterStoresNoText(t *testing.T) {
	res := inspectEPUBOptions(book(page(`<p>Secret prose sentence. ISBN 978-0-00-000004-0</p>`), nil, ""))
	raw, err := evidence.Canonical(res.ISBNHints)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("Secret")) {
		t.Fatalf("the hints hold book text: %s", raw)
	}
}
