package ocr_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/inspect"
	"github.com/felixapel/calibre-bookwarden/internal/ocr"
	"github.com/felixapel/calibre-bookwarden/internal/sandbox"
)

func seq(from, to int) []int {
	var s []int
	for i := from; i <= to; i++ {
		s = append(s, i)
	}
	return s
}

func TestPagesToScan(t *testing.T) {
	for _, tt := range []struct {
		name       string
		count      int
		head, tail int
		want       []int
	}{
		{"ocr, no count", 0, 5, 5, seq(1, 5)}, {"ocr, negative", -3, 5, 5, seq(1, 5)}, {"ocr, 3", 3, 5, 5, seq(1, 3)},
		{"ocr, 10", 10, 5, 5, seq(1, 10)},
		{"ocr, 11", 11, 5, 5, append(seq(1, 5), seq(7, 11)...)},
		{"ocr, the largest count", 100_000, 5, 5, append(seq(1, 5), seq(99_996, 100_000)...)},
		{"ocr, too large", 100_001, 5, 5, seq(1, 5)}, {"ocr, absurd", 1_000_000_000, 5, 5, seq(1, 5)},
		{"text, no count", 0, 12, 3, seq(1, 12)}, {"text, 5", 5, 12, 3, seq(1, 5)},
		{"text, 15", 15, 12, 3, seq(1, 15)},
		{"text, 16", 16, 12, 3, append(seq(1, 12), seq(14, 16)...)},
		{"text, 300", 300, 12, 3, append(seq(1, 12), seq(298, 300)...)},
		{"text, too large", 100_001, 12, 3, seq(1, 12)},
	} {
		if got := ocr.PagesToScan(tt.count, tt.head, tt.tail); !slices.Equal(got, tt.want) {
			t.Errorf("%s: PagesToScan(%d, %d, %d) = %v, want %v", tt.name, tt.count, tt.head, tt.tail, got, tt.want)
		}
	}
}

func TestLanguage(t *testing.T) {
	for _, tt := range []struct {
		in   []string
		want string
	}{
		{nil, "eng"}, {[]string{}, "eng"}, {[]string{"eng"}, "eng"}, {[]string{"deu"}, "deu"}, {[]string{"ger"}, "deu"},
		{[]string{"spa"}, "spa"}, {[]string{"DEU"}, "deu"}, {[]string{"de"}, "deu"}, {[]string{"es"}, "spa"},
		{[]string{"fra"}, "eng"}, {[]string{"xx", ""}, "eng"}, {[]string{"fra", "deu", "eng"}, "deu"},
	} {
		if got := ocr.Language(tt.in); got != tt.want {
			t.Errorf("Language(%v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestOCRUsesTheBooksLanguage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "format")
	f := &fake{}
	e := engine(f)
	left := 180 * time.Second
	h := e.PDFHints(t.Context(), path, 1, false, []string{"ger"}, &left)
	if h.Lang != "deu" {
		t.Fatalf("Lang = %q, want deu", h.Lang)
	}
	var tesseract []string
	for _, s := range f.specs {
		if s.Program == tools.Tesseract {
			tesseract = s.Args
		}
	}
	if i := slices.Index(tesseract, "-l"); i < 0 || tesseract[i+1] != "deu" {
		t.Fatalf("tesseract args %v, want -l deu", tesseract)
	}
	left = 180 * time.Second
	if h := e.PDFHints(t.Context(), path, 1, true, []string{"ger"}, &left); h.Lang != "" {
		t.Fatalf("a text layer read recorded the language %q", h.Lang)
	}
}

func TestHintsRecordContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "format")
	f := &fake{output: func(sandbox.Spec) ([]byte, error) {
		return []byte("Hardcover ISBN 978-0-00-000004-0\nISBN 978-0-00-000006-4 (ebook)\n978-0-00-000004-0"), nil
	}}
	e := engine(f)
	left := 180 * time.Second
	h := e.PDFHints(t.Context(), path, 1, true, nil, &left)
	want := []evidence.ISBNContext{{ISBN: "9780000000040", Qualifier: "hardcover"}, {ISBN: "9780000000064", Qualifier: "ebook"}}
	if !slices.Equal(h.Context, want) || !slices.Equal(h.ISBNs, []string{"9780000000040", "9780000000064"}) {
		t.Fatalf("hints = %+v, want context %+v", h, want)
	}
}

var tools = ocr.Tools{PDFToPPM: "/t/pdftoppm", PDFToText: "/t/pdftotext", Tesseract: "/t/tesseract", TessdataDir: "/t/tessdata"}

// fake records every spec. A pdftoppm call creates the page image, as the
// real tool would; output answers a tool call.
type fake struct {
	mu     sync.Mutex
	specs  []sandbox.Spec
	output func(s sandbox.Spec) ([]byte, error)
}

func (f *fake) run(_ context.Context, s sandbox.Spec) ([]byte, error) {
	f.mu.Lock()
	f.specs = append(f.specs, s)
	f.mu.Unlock()
	if s.Program == tools.PDFToPPM {
		if err := os.WriteFile(s.Args[len(s.Args)-1]+".png", []byte("png"), 0o600); err != nil {
			return nil, err
		}
	}
	if f.output != nil {
		return f.output(s)
	}
	return nil, nil
}

func engine(f *fake) *ocr.Engine {
	e := ocr.New(tools)
	ocr.SetRun(e, f.run)
	return e
}

func TestOCRSpecs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "format")
	f := &fake{}
	f.output = func(s sandbox.Spec) ([]byte, error) {
		if s.Program == tools.Tesseract {
			if _, err := os.Stat(filepath.Join(dir, "page.png")); err != nil {
				t.Errorf("tesseract ran without a page image: %v", err)
			}
		}
		return nil, nil
	}
	e := engine(f)
	left := 180 * time.Second
	h := e.PDFHints(t.Context(), path, 11, false, nil, &left)
	if h.Method != evidence.HintOCR || h.Lang != "eng" || h.State != evidence.HintOK || !slices.Equal(h.Pages, append(seq(1, 5), seq(7, 11)...)) {
		t.Fatalf("hints = %+v", h)
	}
	if len(f.specs) != 20 {
		t.Fatalf("%d tool runs, want 20", len(f.specs))
	}
	for i, s := range f.specs {
		page := strconv.Itoa(h.Pages[i/2])
		var want []string
		if i%2 == 0 {
			want = []string{"-gray", "-png", "-scale-to", "3000", "-f", page, "-l", page, "-singlefile", path, filepath.Join(dir, "page")}
		} else {
			want = []string{filepath.Join(dir, "page.png"), "stdout", "--tessdata-dir", tools.TessdataDir, "-l", "eng", "--psm", "3"}
		}
		if !slices.Equal(s.Args, want) || !slices.Equal(s.Env, []string{"OMP_THREAD_LIMIT=1"}) || s.Dir != dir || s.Limits != sandbox.DefaultLimits || s.MaxStdout != 1<<20 {
			t.Fatalf("run %d = %+v, want args %v", i, s, want)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "page.png")); !os.IsNotExist(err) {
		t.Fatalf("the last page image was left behind: %v", err)
	}
}

func TestTextLayerSpecs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "format")
	f := &fake{output: func(sandbox.Spec) ([]byte, error) { return []byte("ISBN 978-0-00-000004-0"), nil }}
	e := engine(f)
	left := 180 * time.Second
	h := e.PDFHints(t.Context(), path, 20, true, nil, &left)
	if h.Method != evidence.HintTextLayer || h.State != evidence.HintOK || !slices.Equal(h.Pages, append(seq(1, 12), seq(18, 20)...)) || !slices.Equal(h.ISBNs, []string{"9780000000040"}) {
		t.Fatalf("hints = %+v", h)
	}
	if len(f.specs) != 2 ||
		!slices.Equal(f.specs[0].Args, []string{"-q", "-enc", "UTF-8", "-f", "1", "-l", "12", path, "-"}) ||
		!slices.Equal(f.specs[1].Args, []string{"-q", "-enc", "UTF-8", "-f", "18", "-l", "20", path, "-"}) ||
		f.specs[0].Program != tools.PDFToText {
		t.Fatalf("specs = %+v", f.specs)
	}
}

func TestHintsFailureKeepsOtherPages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "format")
	f := &fake{}
	f.output = func(s sandbox.Spec) ([]byte, error) {
		if s.Program != tools.Tesseract {
			return nil, nil
		}
		f.mu.Lock()
		n := len(f.specs)
		f.mu.Unlock()
		switch n {
		case 2:
			return []byte("ISBN 978-0-00-000004-0"), nil
		case 4:
			return nil, sandbox.ErrFailed
		}
		return nil, nil
	}
	e := engine(f)
	left := 180 * time.Second
	h := e.PDFHints(t.Context(), path, 5, false, nil, &left)
	if h.State != evidence.HintFailed || !slices.Equal(h.Pages, []int{1, 3, 4, 5}) || !slices.Equal(h.ISBNs, []string{"9780000000040"}) {
		t.Fatalf("hints = %+v", h)
	}
}

func TestHintsTimeout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "format")
	block := &fake{}
	e := engine(block)
	ocr.SetRun(e, func(ctx context.Context, s sandbox.Spec) ([]byte, error) {
		block.mu.Lock()
		block.specs = append(block.specs, s)
		block.mu.Unlock()
		<-ctx.Done()
		return nil, sandbox.ErrTimeout
	})
	left := 200 * time.Millisecond
	h := e.PDFHints(t.Context(), path, 5, false, nil, &left)
	if h.State != evidence.HintTimeout || len(h.Pages) != 0 || left > 0 {
		t.Fatalf("hints = %+v, left %v", h, left)
	}
	runs := len(block.specs)
	h = e.PDFHints(t.Context(), path, 5, false, nil, &left)
	if h.State != evidence.HintTimeout || len(block.specs) != runs {
		t.Fatalf("a spent budget still ran tools: %+v", h)
	}
}

func TestHintsUnavailable(t *testing.T) {
	e := ocr.New(tools)
	left := 180 * time.Second
	h := e.PDFHints(t.Context(), filepath.Join(t.TempDir(), "format"), 5, false, nil, &left)
	if h.State != evidence.HintUnavailable || h.Method != evidence.HintOCR || h.Pages == nil || left != 180*time.Second {
		t.Fatalf("hints = %+v", h)
	}
}

func TestOneAtATime(t *testing.T) {
	var running, peak atomic.Int32
	e := ocr.New(tools)
	ocr.SetRun(e, func(context.Context, sandbox.Spec) ([]byte, error) {
		n := running.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
		running.Add(-1)
		return nil, nil
	})
	var wg sync.WaitGroup
	lefts := make([]time.Duration, 4)
	for i := range lefts {
		lefts[i] = 180 * time.Second
		wg.Go(func() {
			e.PDFHints(t.Context(), filepath.Join(t.TempDir(), "format"), 3, true, nil, &lefts[i])
		})
	}
	wg.Wait()
	if peak.Load() != 1 {
		t.Fatalf("%d tools ran at once", peak.Load())
	}
	for i, l := range lefts {
		// Each book ran one 50 ms tool, but the last one waited about 150 ms.
		if used := 180*time.Second - l; used <= 0 || used >= 120*time.Millisecond {
			t.Errorf("book %d was charged %v", i, used)
		}
	}
}

func TestSelfCheckPDFHasNoTextLayer(t *testing.T) {
	res := inspect.Inspect(t.Context(), "PDF", bytes.NewReader(ocr.SelfCheckPDF), int64(len(ocr.SelfCheckPDF)), inspect.DefaultLimits)
	if res.State != evidence.StateOK || res.Text != evidence.TextAbsent || res.Pages != 1 {
		t.Fatalf("self-check PDF inspects as %+v", res)
	}
}

func TestSelfCheckRealTools(t *testing.T) {
	for _, p := range []string{ocr.DefaultTools.PDFToPPM, ocr.DefaultTools.PDFToText, ocr.DefaultTools.Tesseract, filepath.Join(ocr.DefaultTools.TessdataDir, "eng.traineddata")} {
		if _, err := os.Stat(p); err != nil && os.Getenv("BOOKWARDEN_REQUIRE_OCR") == "" {
			t.Skipf("%s is missing", p)
		}
	}
	e := ocr.New(ocr.DefaultTools)
	if err := e.SelfCheck(t.Context(), t.TempDir()); err != nil {
		t.Fatalf("SelfCheck: %v", err)
	}
}

// While the self-check is still running, a book's hints are unavailable, not
// ok and empty.
func TestHintsStayUnavailableDuringSelfCheck(t *testing.T) {
	e := ocr.New(tools)
	var during *evidence.ISBNHints
	ocr.SetRunOnly(e, func(_ context.Context, s sandbox.Spec) ([]byte, error) {
		if during == nil {
			gone, cancel := context.WithCancel(context.Background())
			cancel()
			left := time.Minute
			during = e.PDFHints(gone, "x.pdf", 1, false, nil, &left)
		}
		if s.Program == tools.Tesseract {
			return []byte("ISBN 978-0-00-000006-4"), nil
		}
		return nil, nil
	})
	if err := e.SelfCheck(t.Context(), t.TempDir()); err != nil {
		t.Skipf("the sandbox isn't usable here: %v", err)
	}
	if during == nil || during.State != evidence.HintUnavailable {
		t.Fatalf("hints during the self-check = %+v, want unavailable", during)
	}
	left := time.Minute
	if h := e.PDFHints(t.Context(), "x.pdf", 1, false, nil, &left); h.State != evidence.HintOK {
		t.Fatalf("hints after the self-check = %+v, want ok", h)
	}
}

func TestHintsKeepALaterPagesQualifier(t *testing.T) {
	path := filepath.Join(t.TempDir(), "format")
	var calls int
	f := &fake{output: func(sandbox.Spec) ([]byte, error) {
		calls++
		if calls == 1 {
			return []byte("ISBN 978-0-00-000004-0"), nil
		}
		return []byte("Paperback ISBN 978-0-00-000004-0"), nil
	}}
	e := engine(f)
	left := 180 * time.Second
	h := e.PDFHints(t.Context(), path, 20, true, nil, &left)
	want := []evidence.ISBNContext{{ISBN: "9780000000040", Qualifier: "paperback"}}
	if !slices.Equal(h.Context, want) {
		t.Fatalf("context %+v, want %+v", h.Context, want)
	}
}
