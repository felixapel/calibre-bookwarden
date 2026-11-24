package verify_test

import (
	"bytes"
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/inspect"
	"github.com/felixapel/calibre-bookwarden/internal/ocr"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

// The OCR engine is the production excerpt hinter.
var _ verify.ExcerptHinter = (*ocr.Engine)(nil)

func syntheticExcerpt(method, text string) *evidence.Excerpt {
	b := evidence.NewExcerptBuilder(method)
	landmark := ""
	if method == evidence.ExcerptEPUBText {
		landmark = evidence.LandmarkFront
	}
	b.Add(1, landmark, text)
	return b.Excerpt()
}

// excerptHinter is a Hinter that can also hand back a text layer's excerpt.
type excerptHinter struct {
	recHinter
	excerpt *evidence.Excerpt
}

func (h *excerptHinter) PDFHintsExcerpt(ctx context.Context, path string, pages int, textLayer bool, languages []string, left *time.Duration) (*evidence.ISBNHints, *evidence.Excerpt) {
	return h.PDFHints(ctx, path, pages, textLayer, languages, left), h.excerpt
}

func TestEPUBExcerptIsSealedByHashAndReturnedByBody(t *testing.T) {
	ex := syntheticExcerpt(evidence.ExcerptEPUBText, "Synthetic copyright page\nISBN 978-0-00-000004-0")
	scanned := &evidence.ISBNHints{Method: evidence.HintEPUBText, State: evidence.HintOK, Pages: []int{1}}
	inspectAs(t, inspect.Result{State: evidence.StateOK, Detected: "epub", Text: evidence.TextPresent, ISBNHints: scanned, Excerpt: ex})
	out, formats := checkWith(t, nil, fileBook(3, "EPUB", paperPDF))
	want := *ex
	want.Text = "" // the body isn't in the package
	if !reflect.DeepEqual(formats[0].Excerpt, &want) {
		t.Fatalf("sealed excerpt %+v, want %+v", formats[0].Excerpt, &want)
	}
	if len(out.Excerpts) != 1 || out.Excerpts[ex.SHA256] != ex.Text {
		t.Fatalf("outcome excerpts = %v", out.Excerpts)
	}
	p, err := evidence.Open(out.Evidence, out.Seal)
	if err != nil || p.SchemaVersion != evidence.SchemaVersion {
		t.Fatalf("open: schema %d, %v", p.SchemaVersion, err)
	}
	if bytes.Contains(out.Evidence, []byte("copyright page")) {
		t.Error("the body is in the sealed package")
	}
}

func TestEPUBExcerptOnlyWithTheScan(t *testing.T) {
	ex := syntheticExcerpt(evidence.ExcerptEPUBText, "text")
	for _, tt := range []struct {
		name string
		res  inspect.Result
	}{
		{"locked", inspect.Result{State: evidence.StateDRM, Detected: "epub", Text: evidence.TextUnverified, Excerpt: ex}},
		{"corrupt", inspect.Result{State: evidence.StateCorrupt, Detected: "epub", Excerpt: ex}},
		{"no excerpt", inspect.Result{State: evidence.StateOK, Detected: "epub", Text: evidence.TextPresent, ISBNHints: &evidence.ISBNHints{Method: evidence.HintEPUBText, State: evidence.HintOK, Pages: []int{}}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			inspectAs(t, tt.res)
			out, formats := checkWith(t, nil, fileBook(3, "EPUB", paperPDF))
			if formats[0].Excerpt != nil || len(out.Excerpts) != 0 {
				t.Fatalf("excerpt %+v, bodies %v", formats[0].Excerpt, out.Excerpts)
			}
		})
	}
}

func TestPDFTextLayerExcerpt(t *testing.T) {
	ex := syntheticExcerpt(evidence.ExcerptTextLayer, "Synthetic title page")
	inspectAs(t, inspect.Result{State: evidence.StateOK, Detected: "pdf", Text: evidence.TextPresent, Pages: 3})
	h := &excerptHinter{excerpt: ex}
	out, formats := checkWith(t, h, fileBook(3, "PDF", paperPDF))
	if formats[0].Excerpt == nil || formats[0].Excerpt.SHA256 != ex.SHA256 || out.Excerpts[ex.SHA256] != ex.Text {
		t.Fatalf("excerpt %+v, bodies %v", formats[0].Excerpt, out.Excerpts)
	}
}

func TestPDFExcerptNeverFromOCR(t *testing.T) {
	// A hinter that hands back an excerpt for an OCR read is not believed.
	inspectAs(t, inspect.Result{State: evidence.StateOK, Detected: "pdf", Text: evidence.TextAbsent, Pages: 3})
	h := &excerptHinter{excerpt: syntheticExcerpt(evidence.ExcerptTextLayer, "noisy")}
	out, formats := checkWith(t, h, fileBook(3, "PDF", paperPDF))
	if formats[0].Excerpt != nil || len(out.Excerpts) != 0 {
		t.Fatalf("excerpt %+v, bodies %v", formats[0].Excerpt, out.Excerpts)
	}
}

func TestPDFExcerptWithAPlainHinter(t *testing.T) {
	inspectAs(t, inspect.Result{State: evidence.StateOK, Detected: "pdf", Text: evidence.TextPresent, Pages: 3})
	out, formats := checkWith(t, &recHinter{}, fileBook(3, "PDF", paperPDF))
	if formats[0].Excerpt != nil || len(out.Excerpts) != 0 || formats[0].ISBNHints == nil {
		t.Fatalf("excerpt %+v, bodies %v", formats[0].Excerpt, out.Excerpts)
	}
}

func TestExcerptMustFitTheFormatName(t *testing.T) {
	// A PDF stored under an EPUB name keeps its hints, but a text layer
	// excerpt on an "EPUB" would not open: it is left out.
	inspectAs(t, inspect.Result{State: evidence.StateOK, Detected: "pdf", Text: evidence.TextPresent, Pages: 3})
	h := &excerptHinter{excerpt: syntheticExcerpt(evidence.ExcerptTextLayer, "Synthetic title page")}
	out, formats := checkWith(t, h, fileBook(3, "EPUB", paperPDF))
	if out.State != verify.BookChecked || formats[0].ISBNHints == nil || formats[0].Excerpt != nil || len(out.Excerpts) != 0 {
		t.Fatalf("outcome %s, hints %+v, excerpt %+v, bodies %v", out.State, formats[0].ISBNHints, formats[0].Excerpt, out.Excerpts)
	}
}
