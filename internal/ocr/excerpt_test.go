package ocr_test

import (
	"bytes"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/sandbox"
)

// pagesOutput answers a pdftotext range with one synthetic text per page,
// each ended by a form feed as the tool does.
func pagesOutput(s sandbox.Spec) ([]byte, error) {
	first, _ := strconv.Atoi(s.Args[slices.Index(s.Args, "-f")+1])
	last, _ := strconv.Atoi(s.Args[slices.Index(s.Args, "-l")+1])
	var b bytes.Buffer
	for p := first; p <= last; p++ {
		b.WriteString("Synthetic page " + strconv.Itoa(p) + "  text\r\n\n\n\nsecond line")
		if p == 2 {
			b.WriteString("\nISBN 978-0-00-000004-0")
		}
		b.WriteString("\f")
	}
	return b.Bytes(), nil
}

func TestTextLayerExcerpt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "format")
	e := engine(&fake{output: pagesOutput})
	left := 180 * time.Second
	h, ex := e.PDFHintsExcerpt(t.Context(), path, 20, true, nil, &left)
	if h.Method != evidence.HintTextLayer || !slices.Equal(h.ISBNs, []string{"9780000000040"}) {
		t.Fatalf("hints = %+v", h)
	}
	if ex == nil || ex.Method != evidence.ExcerptTextLayer || ex.Truncated || !ex.Complete {
		t.Fatalf("excerpt = %+v", ex)
	}
	var pos []int
	for _, u := range ex.Units {
		pos = append(pos, u.Pos)
		if u.Landmark != "" {
			t.Errorf("a PDF unit has the landmark %q", u.Landmark)
		}
	}
	if !slices.Equal(pos, h.Pages) {
		t.Errorf("unit pages %v, pages read %v", pos, h.Pages)
	}
	u := ex.Units[1]
	if got := string([]rune(ex.Text)[u.Start:u.End]); got != "Synthetic page 2 text\n\nsecond line\nISBN 978-0-00-000004-0" {
		t.Errorf("page 2 slices %q", got)
	}
	if ex.SHA256 != evidence.ExcerptHash(ex.Text) {
		t.Error("hash")
	}
	t.Logf("text layer excerpt of 15 synthetic pages: %d chars, %d units", ex.Chars, len(ex.Units))
	// PDFHints is the same call without the excerpt.
	left = 180 * time.Second
	if again := e.PDFHints(t.Context(), path, 20, true, nil, &left); !reflect.DeepEqual(again, h) {
		t.Errorf("PDFHints = %+v, want %+v", again, h)
	}
}

func TestOCRNeverMakesAnExcerpt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "format")
	e := engine(&fake{output: func(sandbox.Spec) ([]byte, error) { return []byte("Noisy OCR text ISBN 978-0-00-000004-0"), nil }})
	left := 180 * time.Second
	h, ex := e.PDFHintsExcerpt(t.Context(), path, 3, false, nil, &left)
	if h.Method != evidence.HintOCR || len(h.ISBNs) != 1 || ex != nil {
		t.Fatalf("hints = %+v, excerpt = %+v", h, ex)
	}
}

func TestTextLayerExcerptSkipsFailedRanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "format")
	calls := 0
	e := engine(&fake{output: func(s sandbox.Spec) ([]byte, error) {
		calls++
		if calls == 1 {
			return nil, sandbox.ErrFailed
		}
		return pagesOutput(s)
	}})
	left := 180 * time.Second
	h, ex := e.PDFHintsExcerpt(t.Context(), path, 20, true, nil, &left)
	if h.State != evidence.HintFailed || ex == nil || len(ex.Units) != 3 || ex.Units[0].Pos != 18 {
		t.Fatalf("hints = %+v, excerpt = %+v", h, ex)
	}
	if ex.Complete || ex.Truncated || strings.Contains(ex.Text, "page 1 ") || strings.Contains(ex.Text, "page 12") {
		t.Errorf("a failed range left text or claimed to be complete: %+v", ex)
	}
}

func TestTextLayerExcerptUnexpectedPageBreaks(t *testing.T) {
	// A tool that returns fewer pages than asked can't be mapped to page
	// numbers: no unit, but the ISBN hints are as before.
	path := filepath.Join(t.TempDir(), "format")
	e := engine(&fake{output: func(sandbox.Spec) ([]byte, error) { return []byte("one page only\fISBN 978-0-00-000004-0\f"), nil }})
	left := 180 * time.Second
	h, ex := e.PDFHintsExcerpt(t.Context(), path, 5, true, nil, &left)
	if len(h.ISBNs) != 1 || ex != nil {
		t.Fatalf("hints = %+v, excerpt = %+v", h, ex)
	}
}

func TestTextLayerExcerptMisalignedRangeIsIncomplete(t *testing.T) {
	// The first range comes back with one page too few; the second is fine.
	path := filepath.Join(t.TempDir(), "format")
	calls := 0
	e := engine(&fake{output: func(s sandbox.Spec) ([]byte, error) {
		calls++
		if calls == 1 {
			return []byte("one page only\f"), nil
		}
		return pagesOutput(s)
	}})
	left := 180 * time.Second
	h, ex := e.PDFHintsExcerpt(t.Context(), path, 20, true, nil, &left)
	if h.State != evidence.HintOK || ex == nil || ex.Complete || len(ex.Units) != 3 || ex.Units[0].Pos != 18 || strings.Contains(ex.Text, "one page") {
		t.Fatalf("hints = %+v, excerpt = %+v", h, ex)
	}
}

func TestTextLayerTimeoutSealsNoExcerpt(t *testing.T) {
	// The second range times out after the first was read: how far the
	// deadline got must not decide which text is sealed.
	path := filepath.Join(t.TempDir(), "format")
	calls := 0
	e := engine(&fake{output: func(s sandbox.Spec) ([]byte, error) {
		calls++
		if calls == 2 {
			return nil, sandbox.ErrTimeout
		}
		return pagesOutput(s)
	}})
	left := 180 * time.Second
	h, ex := e.PDFHintsExcerpt(t.Context(), path, 20, true, nil, &left)
	if h.State != evidence.HintTimeout || len(h.Pages) != 12 || ex != nil {
		t.Fatalf("hints = %+v, excerpt = %+v", h, ex)
	}
	// A budget that is spent before the tool runs is a timeout too.
	left = 0
	if h, ex := e.PDFHintsExcerpt(t.Context(), path, 20, true, nil, &left); h.State != evidence.HintTimeout || ex != nil {
		t.Fatalf("hints = %+v, excerpt = %+v", h, ex)
	}
}

func TestTextLayerExcerptIsBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "format")
	e := engine(&fake{output: func(sandbox.Spec) ([]byte, error) {
		return []byte(strings.Repeat("lorem ipsum dolor sit amet\n", 800) + "\f"), nil
	}})
	left := 180 * time.Second
	_, ex := e.PDFHintsExcerpt(t.Context(), path, 1, true, nil, &left)
	if ex == nil || !ex.Truncated || !ex.Complete || ex.Chars > evidence.MaxExcerptRunes || ex.Chars < evidence.MaxExcerptRunes-200 {
		t.Fatalf("excerpt %+v", ex)
	}
}
