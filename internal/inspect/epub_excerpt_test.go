package inspect_test

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/inspect"
	"github.com/felixapel/calibre-bookwarden/internal/inspect/inspecttest"
)

func TestExcerptText(t *testing.T) {
	tests := []struct{ name, doc, want string }{
		{"paragraphs", page(`<h1>Title</h1><p>One</p><p>Two</p>`), "Title\n\nOne\n\nTwo"},
		{"br is one line break", page(`<p>a<br/>b<br>c</p>`), "a\nb\nc"},
		{"list items", page(`<ul><li>x</li><li>y</li></ul>`), "x\n\ny"},
		{"table cells", page(`<table><tr><td>1</td><td>2</td></tr></table>`), "1\n\n2"},
		{"entities", page(`<p>Caf&eacute; &amp; &lt;b&gt; &#169; &nbsp;x</p>`), "Café & <b> © x"},
		{"script and style are skipped", page(`<p>a</p><script>var x = 1;</script><style>p { color: red }</style><p>b</p>`), "a\n\nb"},
		{"the head is skipped", `<html><head><title>Hidden title</title></head><body><p>Shown</p></body></html>`, "Shown"},
		{"nested inline elements", page(`<p>Hello <b>big <i>wide <a href="x">world</a></i></b>!</p>`), "Hello big wide world!"},
		{"whitespace inside a line", page("<p>  a \n\t b   c  </p>"), "a b c"},
		{"blank lines are capped at one", page(`<p>a</p><p></p><p></p><div><br/><br/><br/></div><p>b</p>`), "a\n\nb"},
		{"nfc composes a decomposed e acute", page(`<p>Cafe&#769; Caf` + "é" + `</p>`), "Café Café"},
		{"no body text", page(`<img src="a.png"/>`), ""},
		{"no body", `<html><head><title>T</title></head></html>`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := inspect.ExcerptText([]byte(tt.doc)); got != tt.want {
				t.Errorf("ExcerptText = %q, want %q", got, tt.want)
			}
		})
	}
}

func FuzzExcerptText(f *testing.F) {
	for _, s := range []string{
		page(`<p>Caf&eacute;<br/>&#x202e;rtl</p>`),
		page(`<script>x</script><p>a</p>` + strings.Repeat(`<p>`+strings.Repeat("é", 700)+`</p>`, 40)),
		page("<p>ẹ́̂ \x00 \xff \r\n</p>"),
		`<html><body><p>unclosed <b>`,
		"\xef\xbb\xbf<html><body>" + strings.Repeat("á", 20000) + "</body></html>",
		"\xff\xfe<\x00h\x00t\x00m\x00l\x00>\x00",
		"",
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, doc []byte) {
		b := evidence.NewExcerptBuilder(evidence.HintEPUBText)
		b.Add(1, evidence.LandmarkFront, inspect.ExcerptText(doc))
		b.Add(2, evidence.LandmarkBack, inspect.ExcerptText(doc))
		ex := b.Excerpt()
		if ex == nil {
			return
		}
		if !utf8.ValidString(ex.Text) || !norm.NFC.IsNormalString(ex.Text) {
			t.Fatalf("excerpt isn't valid NFC: %q", ex.Text)
		}
		if n := utf8.RuneCountInString(ex.Text); n > evidence.MaxExcerptRunes || n != ex.Chars {
			t.Fatalf("excerpt has %d runes, chars %d", n, ex.Chars)
		}
		if strings.ContainsRune(ex.Text, 0) || strings.Contains(ex.Text, "\r") || strings.Contains(ex.Text, "\n\n\n") {
			t.Fatalf("excerpt has NUL, CR or a double blank line: %q", ex.Text)
		}
		if ex.SHA256 != evidence.ExcerptHash(ex.Text) {
			t.Fatal("excerpt hash is wrong")
		}
		prev := 0
		runes := []rune(ex.Text)
		for _, u := range ex.Units {
			if u.Start < prev || u.End <= u.Start || u.End > len(runes) {
				t.Fatalf("unit %+v out of range of %d runes", u, len(runes))
			}
			prev = u.End
		}
	})
}

func TestEPUBExcerptUnits(t *testing.T) {
	// Spine: 1 title page, 2 copyright, 3 body start, 4-5 middle, 6-8 the last three.
	opts := book(
		page(`<h1>Binary Tides</h1><p>by Cy Placeholder</p>`),
		filler(7, map[int]string{
			0: page(`<p>Copyright 2026. eBook ISBN 978-0-00-000004-0</p><p>All rights reserved.</p>`),
			2: page(`<p>Middle, never read.</p>`),
			5: page(`<p>Imprint of the back.</p>`),
			6: page(`<p>Also by the author: Paperback ISBN 978-0-00-000002-6</p>`),
		}),
		landmarkNav(landmarkLink("titlepage", "text/chapter%201.xhtml"), landmarkLink("copyright-page", "text/extra0.xhtml"), landmarkLink("bodymatter", "text/extra1.xhtml")),
	)
	res := inspectEPUBOptions(opts)
	if res.State != evidence.StateOK || res.Excerpt == nil {
		t.Fatalf("state %s, excerpt %+v", res.State, res.Excerpt)
	}
	ex := res.Excerpt
	if ex.Method != evidence.ExcerptEPUBText || ex.Truncated {
		t.Errorf("method %q truncated %v", ex.Method, ex.Truncated)
	}
	if ex.SHA256 != evidence.ExcerptHash(ex.Text) || ex.Chars != utf8.RuneCountInString(ex.Text) {
		t.Errorf("hash or chars wrong: %+v", ex)
	}
	// The units follow the pages the ISBN scan read, in its order.
	var pos []int
	var landmarks []string
	var slices []string
	for _, u := range ex.Units {
		pos = append(pos, u.Pos)
		landmarks = append(landmarks, u.Landmark)
		slices = append(slices, string([]rune(ex.Text)[u.Start:u.End]))
	}
	if want := res.ISBNHints.Pages; !reflect.DeepEqual(pos, want) {
		t.Errorf("unit positions %v, hints read pages %v", pos, want)
	}
	if want := []string{"title", "copyright", "back", "back", "back"}; !reflect.DeepEqual(landmarks, want) {
		t.Errorf("landmarks %v, want %v", landmarks, want)
	}
	wantSlices := map[int]string{
		1: "Binary Tides\n\nby Cy Placeholder",
		2: "Copyright 2026. eBook ISBN 978-0-00-000004-0\n\nAll rights reserved.",
	}
	for i, u := range ex.Units {
		if want, ok := wantSlices[u.Pos]; ok && slices[i] != want {
			t.Errorf("unit %d (pos %d) slices %q, want %q", i, u.Pos, slices[i], want)
		}
	}
	if strings.Contains(ex.Text, "Middle") {
		t.Error("a middle document is in the excerpt")
	}
	if !strings.Contains(slices[len(slices)-1], "Paperback ISBN 978-0-00-000002-6") {
		t.Errorf("last unit %q", slices[len(slices)-1])
	}
	t.Logf("fixture excerpt: %d chars, %d units, %d bytes", ex.Chars, len(ex.Units), len(ex.Text))
	// The sealed hints keep holding no text.
	raw, err := evidence.Canonical(res.ISBNHints)
	if err != nil || strings.Contains(string(raw), "Binary Tides") {
		t.Errorf("hints %s, err %v", raw, err)
	}
}

func TestEPUBExcerptSkipsNonText(t *testing.T) {
	// Only the cover image document: nothing to excerpt.
	res := inspectEPUBOptions(book(page(`<img src="a.png"/>`), nil, ""))
	if res.ISBNHints == nil || res.Excerpt != nil {
		t.Fatalf("hints %+v excerpt %+v", res.ISBNHints, res.Excerpt)
	}
}

func TestEPUBExcerptCapped(t *testing.T) {
	big := page(`<p>` + strings.Repeat("lorem ipsum ", 3_000) + `</p><p>tail line</p>`)
	res := inspectEPUBOptions(book(big, filler(2, nil), ""))
	ex := res.Excerpt
	if ex == nil || !ex.Truncated || !ex.Complete {
		t.Fatalf("excerpt %+v", ex)
	}
	if n := utf8.RuneCountInString(ex.Text); n != ex.Chars || n > evidence.MaxExcerptRunes {
		t.Errorf("%d runes, chars %d", n, ex.Chars)
	}
	// The ISBN scan itself is unaffected by the excerpt's bound.
	if res.ISBNHints == nil || res.ISBNHints.State != evidence.HintOK || !reflect.DeepEqual(res.ISBNHints.Pages, []int{1, 2, 3}) {
		t.Errorf("hints %+v", res.ISBNHints)
	}
}

// hugeLimits lets a test build documents far more compressible than a real
// book, to reach the read limits.
var hugeLimits = inspect.Limits{MaxRatio: 1 << 30}

func inspectEPUBLimits(o inspecttest.EPUBOptions) inspect.Result {
	data := inspecttest.EPUB(o)
	return inspect.Inspect(context.Background(), "EPUB", bytes.NewReader(data), int64(len(data)), hugeLimits)
}

// empties is n empty inline elements each followed by a space: n bytes of
// text, as the excerpt reads it, in 8n bytes of markup.
func empties(n int) string { return strings.Repeat("<i></i> ", n) }

func TestEPUBExcerptReadLimit(t *testing.T) {
	// The first document reaches the 48,000 bytes the excerpt reads of one
	// document while its text is still short: the tail is cut and the
	// excerpt is truncated, but the next document is still read.
	res := inspectEPUBLimits(book(page(`<p>head`+empties(60_000)+`</p><p>tail</p>`), []string{page(`<p>Second document</p>`)}, ""))
	ex := res.Excerpt
	if ex == nil || !ex.Truncated || !ex.Complete {
		t.Fatalf("excerpt %+v", ex)
	}
	if !strings.Contains(ex.Text, "head") || strings.Contains(ex.Text, "tail") || !strings.Contains(ex.Text, "Second document") {
		t.Errorf("text %q", ex.Text)
	}
	if len(ex.Units) != 2 || ex.Units[1].Pos != 2 {
		t.Errorf("units %+v", ex.Units)
	}
}

func TestEPUBExcerptReadLimitKeepsRunesWhole(t *testing.T) {
	// 47,999 bytes of text are read when the next token, a three-byte
	// character, no longer fits: it is left out whole, not split.
	body := `<p>日本語` + empties(47_989) + `日<i></i>日</p>`
	res := inspectEPUBLimits(book(page(body), []string{page(`<p>Next document</p>`)}, ""))
	ex := res.Excerpt
	if ex == nil {
		t.Fatal("no excerpt")
	}
	if strings.ContainsRune(ex.Text, '\uFFFD') || !utf8.ValidString(ex.Text) {
		t.Fatalf("the excerpt holds a split rune: %q", ex.Text[:min(len(ex.Text), 60)])
	}
	if !ex.Truncated || !ex.Complete || !strings.HasPrefix(ex.Text, "日本語") || !strings.HasSuffix(ex.Text, "Next document") || len(ex.Units) != 2 {
		t.Errorf("excerpt %+v", ex)
	}
}

func TestEPUBExcerptXMLLimit(t *testing.T) {
	// A document longer than the 4 MiB read for one XML document is cut
	// there: its text so far counts, the excerpt is truncated, and the next
	// document is read.
	huge := strings.Repeat("<i></i>", 640_000)
	res := inspectEPUBLimits(book(page(`<p>head</p>`+huge), []string{page(`<p>Next</p>`)}, ""))
	ex := res.Excerpt
	if ex == nil || !ex.Truncated || !ex.Complete || !strings.Contains(ex.Text, "head") || !strings.HasSuffix(ex.Text, "Next") {
		t.Fatalf("excerpt %+v", ex)
	}
}

func TestEPUBExcerptBrokenDocumentAddsNoText(t *testing.T) {
	broken := `<html><body><p>Kept before the break.</p><p class="x`
	res := inspectEPUBOptions(book(page(`<p>Title</p>`), []string{broken, page(`<p>Third</p>`)}, ""))
	ex := res.Excerpt
	if ex == nil || ex.Complete || ex.Truncated {
		t.Fatalf("excerpt %+v", ex)
	}
	var pos []int
	for _, u := range ex.Units {
		pos = append(pos, u.Pos)
	}
	if !reflect.DeepEqual(pos, []int{1, 3}) || strings.Contains(ex.Text, "Kept") || ex.Text != "Title\n\nThird" {
		t.Errorf("units %v, text %q", pos, ex.Text)
	}
	if h := res.ISBNHints; h == nil || h.State != evidence.HintFailed {
		t.Errorf("hints %+v", h)
	}
}

func TestEPUBExcerptTextBudgetLeavesItIncomplete(t *testing.T) {
	// The ISBN scan's total text bound runs out in the first document, so the
	// second is never read: the excerpt says so.
	inspect.SetScanBudget(t, 20<<10)
	res := inspectEPUBLimits(book(page(`<p>head`+empties(300_000)+`</p>`), []string{page(`<p>Second</p>`)}, ""))
	ex := res.Excerpt
	if ex == nil || ex.Complete || !ex.Truncated || strings.Contains(ex.Text, "Second") {
		t.Fatalf("excerpt %+v", ex)
	}
}

func TestEPUBExcerptNoneOnTimeout(t *testing.T) {
	o := book(page(`<p>Title</p>`), filler(3, nil), "")
	data := inspecttest.EPUB(o)
	run := func(ctx context.Context) inspect.Result {
		return inspect.Inspect(ctx, "EPUB", bytes.NewReader(data), int64(len(data)), inspect.Limits{})
	}
	t.Run("the scan runs out of time midway", func(t *testing.T) {
		inspect.SetScanMargin(t, time.Second)
		inspect.SetScanDocHook(t, func() { time.Sleep(600 * time.Millisecond) })
		ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
		defer cancel()
		res := run(ctx)
		if h := res.ISBNHints; h == nil || h.State != evidence.HintTimeout || !reflect.DeepEqual(h.Pages, []int{1}) {
			t.Fatalf("hints %+v, want timeout keeping the first document", h)
		}
		if res.Excerpt != nil {
			t.Fatalf("a timed-out scan sealed %d characters", res.Excerpt.Chars)
		}
	})
	t.Run("less than the margin left", func(t *testing.T) {
		inspect.SetScanMargin(t, time.Hour)
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if res := run(ctx); res.Excerpt != nil {
			t.Fatalf("excerpt %+v", res.Excerpt)
		}
	})
	t.Run("the deadline passes during the excerpt's own pass", func(t *testing.T) {
		// One document, so no later read could notice the deadline.
		single := inspecttest.EPUB(book(page(`<p>Only document</p>`), nil, ""))
		inspect.SetScanMargin(t, time.Second)
		inspect.SetScanTextHook(t, func() { time.Sleep(700 * time.Millisecond) })
		ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
		defer cancel()
		res := inspect.Inspect(ctx, "EPUB", bytes.NewReader(single), int64(len(single)), inspect.Limits{})
		if h := res.ISBNHints; h == nil || h.State != evidence.HintTimeout {
			t.Fatalf("hints %+v, want timeout", h)
		}
		if res.Excerpt != nil {
			t.Fatalf("excerpt %+v", res.Excerpt)
		}
	})
	t.Run("without a deadline the excerpt is whole", func(t *testing.T) {
		if res := run(context.Background()); res.Excerpt == nil || !res.Excerpt.Complete {
			t.Fatalf("excerpt %+v", res.Excerpt)
		}
	})
}

func FuzzEPUBExcerpt(f *testing.F) {
	for _, s := range []string{
		page(`<p>Caf&eacute;<br/>&#x202e;rtl</p>`),
		page(`<p>日本語` + empties(50) + `</p>`),
		page(`<script>x</script><p>a</p>` + strings.Repeat(`<p>`+strings.Repeat("é", 700)+`</p>`, 40)),
		`<html><body><p>unclosed <b>`,
		`<html><body><p>Kept</p><p class="x`,
		page("<p>ẹ́̂ \x00 \r\n</p>"),
		"",
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, doc []byte) {
		// The real path: a generated EPUB, the scan, the builder.
		data := inspecttest.EPUB(inspecttest.EPUBOptions{Title: "T", EPUB3: true, ContentDoc: string(doc), Extra: []string{page(`<p>Second ` + isbnA + `</p>`)}})
		res := inspect.Inspect(context.Background(), "EPUB", bytes.NewReader(data), int64(len(data)), hugeLimits)
		ex := res.Excerpt
		if ex == nil {
			return
		}
		if !utf8.ValidString(ex.Text) || !norm.NFC.IsNormalString(ex.Text) {
			t.Fatalf("excerpt isn't valid NFC: %q", ex.Text)
		}
		input := !utf8.Valid(doc) || bytes.Contains(doc, []byte("\uFFFD")) || bytes.Contains(doc, []byte("encoding")) || bytes.Contains(doc, []byte("&#"))
		if !input && strings.ContainsRune(ex.Text, '\uFFFD') {
			t.Fatalf("a replacement character the input didn't have: %q", ex.Text)
		}
		runes := []rune(ex.Text)
		if len(runes) > evidence.MaxExcerptRunes || len(runes) != ex.Chars || ex.SHA256 != evidence.ExcerptHash(ex.Text) {
			t.Fatalf("%d runes, chars %d", len(runes), ex.Chars)
		}
		if strings.ContainsRune(ex.Text, 0) || strings.Contains(ex.Text, "\r") || strings.Contains(ex.Text, "\n\n\n") {
			t.Fatalf("NUL, CR or a double blank line: %q", ex.Text)
		}
		prevPos, prevEnd := 0, 0
		for _, u := range ex.Units {
			if u.Pos <= prevPos || u.Start < prevEnd || u.End <= u.Start || u.End > len(runes) {
				t.Fatalf("unit %+v out of order or range (%d runes)", u, len(runes))
			}
			prevPos, prevEnd = u.Pos, u.End
		}
	})
}

func TestEPUBExcerptNotForUnscannedEPUBs(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts inspecttest.EPUBOptions
	}{
		{"adept rights", with(book(page(`<p>Text</p>`), nil, ""), func(o *inspecttest.EPUBOptions) { o.Rights = true })},
		{"a missing chapter", with(book(page(`<p>x</p>`), nil, ""), func(o *inspecttest.EPUBOptions) { o.NoChapter = true })},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if res := inspectEPUBOptions(tc.opts); res.Excerpt != nil {
				t.Fatalf("excerpt %+v", res.Excerpt)
			}
		})
	}
}
