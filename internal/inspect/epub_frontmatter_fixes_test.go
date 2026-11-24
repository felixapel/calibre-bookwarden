package inspect_test

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/inspect"
	"github.com/felixapel/calibre-bookwarden/internal/inspect/inspecttest"
)

func TestHTMLBodyTextBounded(t *testing.T) {
	// Every write counts against the limit, newlines from block tags included.
	for _, limit := range []int{0, 1, 2, 3, 4, 5, 6, 7} {
		for _, tc := range []struct{ name, doc string }{
			{"consecutive br", "<html><body><br/><br/><br/><br/><br/>x</body></html>"},
			{"paragraphs", "<html><body><p></p><p></p><p></p>xyz</body></html>"},
			{"text then breaks", "<html><body>ab<br/><br/><br/>cd</body></html>"},
		} {
			text, _ := inspect.HTMLBodyText([]byte(tc.doc), limit)
			if len(text) > limit {
				t.Errorf("%s, limit %d: %d bytes", tc.name, limit, len(text))
			}
		}
	}
	tests := []struct {
		name      string
		doc       string
		limit     int
		wantText  string
		wantWhole bool
	}{
		{"limit-1 inside breaks", "<html><body><br/><br/><br/>x</body></html>", 5, "\n\n\n\n\n", false},
		{"fits exactly", "<html><body>abc</body></html>", 3, "abc", true},
		{"one over", "<html><body>abcd</body></html>", 3, "abc", false},
		{"the issue's repro", "<html><body><br/><br/><br/><br/><br/>x</body></html>", 3, "\n\n\n", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			text, whole := inspect.HTMLBodyText([]byte(tc.doc), tc.limit)
			if text != tc.wantText || whole != tc.wantWhole {
				t.Fatalf("got %q, %v; want %q, %v", text, whole, tc.wantText, tc.wantWhole)
			}
		})
	}
}

func TestEPUBFrontMatterTextEdge(t *testing.T) {
	// Text right up to a bound, then block tags and more text. At the
	// per-document window the scan stays ok; at the total bound it fails.
	const budget = 20 << 10
	tests := []struct {
		name   string
		bound  int
		budget int
		state  string
		reason string
	}{
		{"window", inspect.DocScanWindow, 0, evidence.HintOK, ""},
		{"total", budget, budget, evidence.HintFailed, inspect.FailTextBudget},
	}
	for _, tc := range tests {
		for _, fill := range []int{tc.bound - 1, tc.bound, tc.bound + 1} {
			t.Run(fmt.Sprintf("%s %d", tc.name, fill), func(t *testing.T) {
				if tc.budget > 0 {
					inspect.SetScanBudget(t, tc.budget)
				}
				doc := page(`<span>` + strings.Repeat("a", fill) + `</span><br/>x`)
				res := inspectEPUBOptions(book(doc, nil, ""))
				if res.State != evidence.StateOK || res.Metadata == nil || res.Metadata.Title != "Binary Tides" {
					t.Fatalf("format %s/%s (%s), metadata %+v; want ok with its metadata", res.State, res.Reason, res.Panic, res.Metadata)
				}
				if h := res.ISBNHints; h == nil || h.State != tc.state || res.FailReason != tc.reason {
					t.Fatalf("hints %+v reason %q, want %s %q", h, res.FailReason, tc.state, tc.reason)
				}
			})
		}
	}
}

func TestNavPaths(t *testing.T) {
	opf := []byte(`<package xmlns="http://www.idpf.org/2007/opf"><manifest>
<item id="b" href="zz/nav.xhtml" properties="nav"/>
<item id="a" href="nav.xhtml" properties="nav"/>
<item id="c" href="nav.xhtml" properties="nav"/>
<item id="d" href="./nav.xhtml" properties="scripted nav"/>
<item id="e" href="other.xhtml" properties="scripted"/>
</manifest><spine><itemref idref="a"/></spine></package>`)
	got, err := inspect.NavPaths(opf, "OEBPS")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"OEBPS/nav.xhtml", "OEBPS/zz/nav.xhtml"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("nav paths %v, want %v", got, want)
	}
}

func TestEPUBFrontMatterConflictingNavsAreStable(t *testing.T) {
	nav := func(typ string) string {
		return landmarkNav(landmarkLink(typ, "text/extra0.xhtml"), landmarkLink("bodymatter", "text/extra1.xhtml"))
	}
	o := book(page(`<p>Half title</p>`), filler(7, map[int]string{0: page(`<p>ISBN 978-0-00-000004-0</p>`)}), nav("copyright-page"))
	o.ExtraNavs = []string{nav("titlepage")}
	// OEBPS/nav.xhtml sorts before OEBPS/nav1.xhtml, so its label wins.
	for i := range 100 {
		res := inspectEPUBOptions(o)
		h := res.ISBNHints
		if h == nil || len(h.Context) != 1 || h.Context[0].Landmark != evidence.LandmarkCopyright {
			t.Fatalf("run %d: hints %+v, want the first nav's copyright label", i, h)
		}
	}
}

func TestEPUBFrontMatterNavProblemsFailTheScan(t *testing.T) {
	broken := `<html xmlns:epub="http://www.idpf.org/2007/ops"><body><nav epub:type="landmarks"><ol><li><a epub:type="bodymatter" href="x`
	for _, tc := range []struct {
		name string
		opts inspecttest.EPUBOptions
	}{
		{"unparseable nav", book(page(`<p>x</p>`), filler(2, nil), broken)},
		{"a listed nav that isn't there", with(book(page(`<p>x</p>`), filler(2, nil), ""), func(o *inspecttest.EPUBOptions) { o.MissingNav = true })},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := inspectEPUBOptions(tc.opts)
			if res.State != evidence.StateOK {
				t.Fatalf("format %s/%s, want ok", res.State, res.Reason)
			}
			if h := res.ISBNHints; h == nil || h.State != evidence.HintFailed {
				t.Fatalf("hints %+v, want failed", h)
			}
		})
	}
}

func TestEPUBFrontMatterKeepsALaterQualifier(t *testing.T) {
	o := book(page(`<p>ISBN 978-0-00-000004-0</p>`), []string{page(`<p>Imprint. ebook ISBN 978-0-00-000004-0</p>`)}, "")
	h := inspectEPUBOptions(o).ISBNHints
	want := []evidence.ISBNContext{{ISBN: isbnA, Landmark: evidence.LandmarkFront, Qualifier: evidence.QualifierEbook}}
	if h == nil || !reflect.DeepEqual(h.Context, want) {
		t.Fatalf("hints %+v, want context %+v", h, want)
	}
}

func TestEPUBHintScanTimeout(t *testing.T) {
	o := book(page(`<p>ISBN 978-0-00-000004-0</p>`), filler(3, nil), "")
	data := inspecttest.EPUB(o)
	run := func(ctx context.Context) inspect.Result {
		return inspect.Inspect(ctx, "EPUB", bytes.NewReader(data), int64(len(data)), inspect.Limits{})
	}
	t.Run("less than the margin left skips the scan", func(t *testing.T) {
		inspect.SetScanMargin(t, time.Hour)
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		res := run(ctx)
		if res.State != evidence.StateOK || res.Metadata == nil || res.Metadata.Title != "Binary Tides" {
			t.Fatalf("format %s/%s, metadata %+v; want ok with metadata", res.State, res.Reason, res.Metadata)
		}
		if h := res.ISBNHints; h == nil || h.State != evidence.HintTimeout || len(h.ISBNs) != 0 || h.Pages == nil {
			t.Fatalf("hints %+v, want timeout", h)
		}
	})
	t.Run("the scan's own deadline ends it, not the file's", func(t *testing.T) {
		inspect.SetScanMargin(t, time.Second)
		inspect.SetScanDocHook(t, func() { time.Sleep(600 * time.Millisecond) })
		ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
		defer cancel()
		res := run(ctx)
		if res.State != evidence.StateOK || res.Metadata == nil {
			t.Fatalf("format %s/%s, metadata %+v; want ok with metadata", res.State, res.Reason, res.Metadata)
		}
		h := res.ISBNHints
		if h == nil || h.State != evidence.HintTimeout || !reflect.DeepEqual(h.ISBNs, []string{isbnA}) || !reflect.DeepEqual(h.Pages, []int{1}) {
			t.Fatalf("hints %+v, want timeout keeping the first document", h)
		}
	})
	t.Run("without a deadline the scan completes", func(t *testing.T) {
		if h := run(context.Background()).ISBNHints; h == nil || h.State != evidence.HintOK {
			t.Fatalf("hints %+v", h)
		}
	})
}

func FuzzHTMLBodyText(f *testing.F) {
	f.Add([]byte("<html><body><br/><br/>x<p>ISBN 978-0-00-000004-0</p></body></html>"), uint16(3))
	f.Add([]byte("<body><script>a</script><div>b</div>"), uint16(0))
	f.Fuzz(func(t *testing.T, b []byte, limit uint16) {
		text, whole := inspect.HTMLBodyText(b, int(limit))
		if len(text) > int(limit) {
			t.Fatalf("%d bytes over limit %d", len(text), limit)
		}
		if whole && len(text) > int(limit) {
			t.Fatal("whole but over the limit")
		}
	})
}

func FuzzNavLandmarks(f *testing.F) {
	f.Add([]byte(`<nav epub:type="landmarks"><ol><li><a epub:type="bodymatter" href="a.xhtml#x">A</a></li></ol></nav>`))
	f.Add([]byte(`<nav epub:type="landmarks"><a href="`))
	f.Fuzz(func(t *testing.T, b []byte) {
		refs, _ := inspect.NavLandmarks(b)
		if len(refs) > len(b) {
			t.Fatalf("%d refs from %d bytes", len(refs), len(b))
		}
	})
}
