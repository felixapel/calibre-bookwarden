package inspect_test

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/findings"
	"github.com/felixapel/calibre-bookwarden/internal/inspect"
)

// words returns n bytes of ISBN-free filler text, from a fixed seed so that a
// failure repeats. It has no digits and compresses like prose.
func words(seed uint64, n int) string {
	r := rand.New(rand.NewPCG(seed, 1))
	var b strings.Builder
	for b.Len() < n {
		for range 2 + r.IntN(8) {
			b.WriteByte(byte('a' + r.IntN(26)))
		}
		b.WriteByte(' ')
	}
	return b.String()[:n]
}

// longDoc is a document of size bytes of text, with ins at byte offset at, in
// paragraphs so that the text has line breaks.
func longDoc(seed uint64, size, at int, ins string) string {
	text := words(seed, size)
	if ins != "" {
		text = text[:at] + " " + ins + " " + text[at:]
	}
	var b strings.Builder
	for len(text) > 0 {
		n := min(len(text), 500)
		b.WriteString("<p>" + text[:n] + "</p>")
		text = text[n:]
	}
	return page(b.String())
}

func TestDocScanWindowIs32KiB(t *testing.T) {
	if inspect.DocScanWindow != 32<<10 {
		t.Fatalf("window %d", inspect.DocScanWindow)
	}
}

// Run 10: EPUBs without landmarks, whose first 6 spine documents are whole
// chapters, failed as text_budget though the copyright page was read in full.
func TestEPUBScanWindow(t *testing.T) {
	const kib80 = 80 << 10
	tests := []struct {
		name  string
		build func() (first string, extra []string)
		isbns []string
		docs  int  // documents read
		cut   bool // every one of them cut at the window
	}{
		{
			name: "6 documents of 80 KiB, an ISBN early in the second",
			build: func() (string, []string) {
				return longDoc(1, kib80, 0, ""), []string{
					longDoc(2, kib80, 500, "ISBN 978-0-00-000004-0"), longDoc(3, kib80, 0, ""),
					longDoc(4, kib80, 0, ""), longDoc(5, kib80, 0, ""), longDoc(6, kib80, 0, ""),
				}
			},
			isbns: []string{isbnA}, docs: 6, cut: true,
		},
		{
			name: "a single file of 900 KiB, an ISBN in its first 2 KiB",
			build: func() (string, []string) {
				return longDoc(7, 900<<10, 1500, "ISBN 978-0-00-000004-0"), nil
			},
			isbns: []string{isbnA}, docs: 1, cut: true,
		},
		{
			name: "an ISBN after 40 KiB is outside the window",
			build: func() (string, []string) {
				return longDoc(8, kib80, 40<<10, "ISBN 978-0-00-000004-0"), nil
			},
			isbns: nil, docs: 1, cut: true,
		},
		{
			name: "an ISBN just inside the window",
			build: func() (string, []string) {
				return longDoc(9, kib80, 30<<10, "ISBN 978-0-00-000004-0"), nil
			},
			isbns: []string{isbnA}, docs: 1, cut: true,
		},
		{
			name: "a document under the window is whole",
			build: func() (string, []string) {
				return longDoc(10, 10<<10, 100, "ISBN 978-0-00-000004-0"), nil
			},
			isbns: []string{isbnA}, docs: 1, cut: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			first, extra := tc.build()
			res := inspectEPUBOptions(book(first, extra, ""))
			h := res.ISBNHints
			if res.State != evidence.StateOK || h == nil || h.State != evidence.HintOK || res.FailReason != "" {
				t.Fatalf("state %s hints %+v reason %q; want an ok scan with no reason", res.State, h, res.FailReason)
			}
			if len(h.ISBNs) != len(tc.isbns) || (len(tc.isbns) > 0 && !reflect.DeepEqual(h.ISBNs, tc.isbns)) {
				t.Errorf("ISBNs %v, want %v", h.ISBNs, tc.isbns)
			}
			// A windowed document counts as read: the pages are the spine
			// positions the scan read as it means to.
			if len(h.Pages) != tc.docs || len(res.ScanDocs) != tc.docs {
				t.Fatalf("pages %v, scan docs %+v; want %d", h.Pages, res.ScanDocs, tc.docs)
			}
			for _, d := range res.ScanDocs {
				wantReason := ""
				if tc.cut {
					wantReason = inspect.FailDocWindow
				}
				if d.Whole == tc.cut || d.Reason != wantReason {
					t.Errorf("scan doc %+v; want whole %v reason %q", d, !tc.cut, wantReason)
				}
			}
			if ex := res.Excerpt; ex != nil {
				if n := utf8.RuneCountInString(ex.Text); n != ex.Chars || n > evidence.MaxExcerptRunes {
					t.Errorf("excerpt %d runes, chars %d", n, ex.Chars)
				}
				if strings.ContainsRune(ex.Text, utf8.RuneError) {
					t.Error("excerpt holds U+FFFD")
				}
			}
		})
	}
}

// The excerpt reads its own bound from a document, so the window doesn't
// change what it holds, and its flags say what was cut: the 12,000-rune cap
// truncates it, and no document was missed.
func TestEPUBScanWindowExcerpt(t *testing.T) {
	var extra []string
	for i := range 5 {
		extra = append(extra, longDoc(uint64(20+i), 80<<10, 0, ""))
	}
	extra[0] = longDoc(30, 80<<10, 500, "ISBN 978-0-00-000004-0")
	res := inspectEPUBOptions(book(longDoc(31, 80<<10, 0, ""), extra, ""))
	ex := res.Excerpt
	if ex == nil || !ex.Truncated || !ex.Complete {
		t.Fatalf("excerpt %+v; want truncated and complete", ex)
	}
	if n := utf8.RuneCountInString(ex.Text); n != ex.Chars || n > evidence.MaxExcerptRunes || n < evidence.MaxExcerptRunes-200 {
		t.Errorf("%d runes, chars %d", n, ex.Chars)
	}
	if strings.ContainsRune(ex.Text, utf8.RuneError) || ex.SHA256 != evidence.ExcerptHash(ex.Text) {
		t.Error("excerpt text or hash is wrong")
	}
	small := inspectEPUBOptions(book(longDoc(32, 10<<10, 0, ""), nil, ""))
	if ex := small.Excerpt; ex == nil || !ex.Complete || ex.Truncated {
		t.Errorf("small book excerpt %+v", ex)
	}
}

// A cut at the window never splits a rune.
func TestScanWindowKeepsRunesWhole(t *testing.T) {
	for _, r := range []string{"é", "€", "😀"} {
		for shift := range 4 {
			doc := page(`<p>` + strings.Repeat("a", shift) + strings.Repeat(r, inspect.DocScanWindow) + `</p>`)
			text, whole := inspect.HTMLBodyText([]byte(doc), inspect.DocScanWindow)
			if whole || len(text) > inspect.DocScanWindow || !utf8.ValidString(text) {
				t.Errorf("%q shift %d: whole %v, %d bytes, valid %v", r, shift, whole, len(text), utf8.ValidString(text))
			}
		}
	}
}

// The read limit only fails a document it cut before the window was filled.
func TestEPUBScanWindowAndReadLimit(t *testing.T) {
	t.Run("a window filled before the read limit is windowed", func(t *testing.T) {
		res := inspectEPUBLimits(book(longDoc(60, 5<<20, 1500, "ISBN 978-0-00-000004-0"), nil, ""))
		h := res.ISBNHints
		if res.State != evidence.StateOK || h == nil || h.State != evidence.HintOK || res.FailReason != "" || !reflect.DeepEqual(h.ISBNs, []string{isbnA}) {
			t.Fatalf("state %s hints %+v reason %q", res.State, h, res.FailReason)
		}
		if len(res.ScanDocs) != 1 || res.ScanDocs[0].Reason != inspect.FailDocWindow || res.ScanDocs[0].Whole {
			t.Errorf("scan docs %+v", res.ScanDocs)
		}
		ex := res.Excerpt
		if ex == nil || !ex.Truncated || strings.ContainsRune(ex.Text, utf8.RuneError) || utf8.RuneCountInString(ex.Text) > evidence.MaxExcerptRunes {
			t.Errorf("excerpt %+v", ex)
		}
	})
	t.Run("a read limit before the window is filled fails", func(t *testing.T) {
		doc := page(`<p>ISBN 978-0-00-000004-0</p><p style="` + strings.Repeat("x", 5<<20) + `">tail</p>`)
		res := inspectEPUBLimits(book(doc, nil, ""))
		h := res.ISBNHints
		if h == nil || h.State != evidence.HintFailed || res.FailReason != inspect.FailDocReadLimit {
			t.Fatalf("hints %+v reason %q", h, res.FailReason)
		}
		if ex := res.Excerpt; ex == nil || !ex.Truncated || strings.ContainsRune(ex.Text, utf8.RuneError) {
			t.Errorf("excerpt %+v", ex)
		}
	})
}

// Review C1: a copyright or title landmark can label a whole file
// (resolveHref drops the #fragment), so those documents are never windowed.
func TestEPUBCopyrightAndTitleAreNotWindowed(t *testing.T) {
	for _, typ := range []string{"copyright-page", "titlepage"} {
		t.Run(typ, func(t *testing.T) {
			doc := longDoc(71, 80<<10, 40<<10, "ISBN 978-0-00-000006-4 (ebook)")
			doc = strings.Replace(doc, "<body>", "<body><p>ISBN 978-0-00-000004-0</p>", 1)
			nav := landmarkNav(landmarkLink(typ, "text/extra0.xhtml#c"), landmarkLink("bodymatter", "text/extra1.xhtml"))
			res := inspectEPUBOptions(book(page("<p>Title</p>"), []string{doc, page("<p>ch</p>")}, nav))
			h := res.ISBNHints
			if res.State != evidence.StateOK || h == nil || h.State != evidence.HintOK || res.FailReason != "" {
				t.Fatalf("hints %+v reason %q", h, res.FailReason)
			}
			if !reflect.DeepEqual(h.ISBNs, []string{isbnA, isbnB}) || len(h.Context) != 2 || h.Context[1].Qualifier != evidence.QualifierEbook {
				t.Fatalf("ISBNs %v context %+v; want A, then B labelled ebook", h.ISBNs, h.Context)
			}
			wantLandmark := evidence.LandmarkCopyright
			if typ == "titlepage" {
				wantLandmark = evidence.LandmarkTitle
			}
			if h.Context[1].Landmark != wantLandmark {
				t.Errorf("landmark %q, want %q", h.Context[1].Landmark, wantLandmark)
			}
			for _, d := range res.ScanDocs {
				if d.Reason != "" {
					t.Errorf("scan doc %+v was windowed", d)
				}
			}
		})
	}
	t.Run("a copyright document over the total bound still fails", func(t *testing.T) {
		nav := landmarkNav(landmarkLink("copyright-page", "text/extra0.xhtml#c"), landmarkLink("bodymatter", "text/extra1.xhtml"))
		res := inspectEPUBOptions(book(page("<p>Title</p>"), []string{longDoc(72, 500<<10, 500, "ISBN 978-0-00-000004-0"), page("<p>ch</p>")}, nav))
		if h := res.ISBNHints; h == nil || h.State != evidence.HintFailed || res.FailReason != inspect.FailTextBudget {
			t.Fatalf("hints %+v reason %q", h, res.FailReason)
		}
	})
}

// Review I1: tokenizing goes on after the window, so a parse error later in
// the read bytes fails the scan as it does on main.
func TestEPUBScanWindowStillParses(t *testing.T) {
	doc := `<?xml version="1.0" encoding="utf-8"?>
<html xmlns="http://www.w3.org/1999/xhtml"><head><title>T</title></head><body><p>ISBN 978-0-00-000004-0 ` + words(3, 40<<10) + `</p><p class="x`
	res := inspectEPUBOptions(book(doc, []string{page("<p>two</p>")}, ""))
	h := res.ISBNHints
	if h == nil || h.State != evidence.HintFailed || res.FailReason != inspect.FailXMLError || !reflect.DeepEqual(h.ISBNs, []string{isbnA}) {
		t.Fatalf("hints %+v reason %q", h, res.FailReason)
	}
	if !reflect.DeepEqual(h.Pages, []int{2}) {
		t.Errorf("pages %v, want [2]: the broken document isn't read", h.Pages)
	}
	if len(res.ScanDocs) != 2 || res.ScanDocs[0].Whole || res.ScanDocs[0].Reason != "" {
		t.Errorf("scan docs %+v", res.ScanDocs)
	}
	if ex := res.Excerpt; ex == nil || ex.Complete {
		t.Errorf("excerpt %+v; want incomplete, as on main", ex)
	}
}

// Review M1: a cut inside an ISBN-13 must not leave a valid ISBN-10 prefix.
func TestEPUBScanWindowTrimsACutISBN(t *testing.T) {
	var full string
	for n := 0; full == ""; n++ {
		d12 := fmt.Sprintf("978%09d", 123456789+n*7919)
		sum := 0
		for i, c := range d12 {
			w := 1
			if i%2 == 1 {
				w = 3
			}
			sum += w * int(c-'0')
		}
		c := d12 + string(rune('0'+(10-sum%10)%10))
		if _, ok := findings.CanonicalISBN(c[:10]); ok {
			full = c
		}
	}
	// The body text is "\n", the filler, "ISBN " and the first 10 digits.
	fill := inspect.DocScanWindow - 1 - len("ISBN ") - 10 - 1
	doc := page("<p>" + words(5, fill) + " ISBN " + full + " end</p>")
	res := inspectEPUBOptions(book(doc, nil, ""))
	h := res.ISBNHints
	if h == nil || h.State != evidence.HintOK || len(res.ScanDocs) != 1 || res.ScanDocs[0].Reason != inspect.FailDocWindow {
		t.Fatalf("hints %+v docs %+v; want a windowed ok scan", h, res.ScanDocs)
	}
	if len(h.ISBNs) != 0 {
		t.Errorf("ISBNs %v from a cut ISBN-13 %s", h.ISBNs, full)
	}
}

// What is a failure stays one: the window hides neither a document that
// doesn't parse nor a total bound that runs out.
func TestEPUBScanWindowKeepsRealFailures(t *testing.T) {
	broken := `<html><body><p>ISBN 978-0-00-000006-4</p><p class="x`
	t.Run("a document that doesn't parse after a windowed one", func(t *testing.T) {
		res := inspectEPUBOptions(book(longDoc(40, 80<<10, 500, "ISBN 978-0-00-000004-0"), []string{broken}, ""))
		h := res.ISBNHints
		if h == nil || h.State != evidence.HintFailed || res.FailReason != inspect.FailXMLError || !reflect.DeepEqual(h.ISBNs, []string{isbnA, isbnB}) {
			t.Fatalf("hints %+v reason %q", h, res.FailReason)
		}
		if len(res.ScanDocs) != 2 || res.ScanDocs[0].Reason != inspect.FailDocWindow || res.ScanDocs[1].Reason != "" || res.ScanDocs[1].Whole {
			t.Errorf("scan docs %+v", res.ScanDocs)
		}
	})
	t.Run("the total bound still fails the scan", func(t *testing.T) {
		inspect.SetScanBudget(t, 40<<10)
		extra := []string{longDoc(42, 80<<10, 0, ""), page(`<p>ISBN 978-0-00-000006-4</p>`)}
		res := inspectEPUBOptions(book(longDoc(41, 80<<10, 500, "ISBN 978-0-00-000004-0"), extra, ""))
		h := res.ISBNHints
		if h == nil || h.State != evidence.HintFailed || res.FailReason != inspect.FailTextBudget || !reflect.DeepEqual(h.ISBNs, []string{isbnA}) {
			t.Fatalf("hints %+v reason %q", h, res.FailReason)
		}
	})
	t.Run("the default total bound holds the ordinary worst case", func(t *testing.T) {
		// 12 front documents and 3 back ones, each cut at the window.
		var extra []string
		for i := 1; i < 20; i++ {
			extra = append(extra, longDoc(uint64(50+i), 40<<10, 0, fmt.Sprintf("n%d", i)))
		}
		nav := landmarkNav(landmarkLink("bodymatter", "text/extra18.xhtml"))
		res := inspectEPUBOptions(book(longDoc(50, 40<<10, 0, ""), extra, nav))
		if h := res.ISBNHints; res.FailReason != "" || h == nil || h.State != evidence.HintOK || len(h.Pages) != 15 {
			t.Fatalf("hints %+v reason %q", h, res.FailReason)
		}
	})
}
