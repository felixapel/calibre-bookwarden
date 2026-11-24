package web

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/report"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

// fakeExcerpts serves excerpt bodies by hash and counts the reads.
type fakeExcerpts struct {
	mu     sync.Mutex
	bodies map[string]string
	err    error
	reads  []string
}

func (f *fakeExcerpts) Excerpt(_ context.Context, hash string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads = append(f.reads, hash)
	if f.err != nil {
		return "", f.err
	}
	body, ok := f.bodies[hash]
	if !ok {
		return "", errors.New("no such excerpt")
	}
	return body, nil
}

func (f *fakeExcerpts) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reads)
}

// excerptBook is a checked book with one EPUB whose excerpt is text and whose
// hint ISBN is isbn.
func excerptBook(text, isbn string, truncated bool) (report.Book, *fakeExcerpts) {
	b := evidence.NewExcerptBuilder(evidence.ExcerptEPUBText)
	b.Add(1, evidence.LandmarkCopyright, text)
	ex := b.Excerpt()
	ex.Truncated = truncated
	ex.Complete = true
	stored := *ex
	stored.Text = ""
	book := report.Book{
		Ref: library.BookRef{ID: 2, UUID: "frozen"}, State: verify.BookChecked,
		Package: &evidence.Package{
			Calibre: evidence.Record{Title: "Binary Tides"},
			Formats: []evidence.FormatEvidence{{
				Format: "EPUB", State: evidence.StateOK, Excerpt: &stored,
				ISBNHints: &evidence.ISBNHints{Method: evidence.HintEPUBText, State: evidence.HintOK, Pages: []int{1}, ISBNs: []string{isbn}},
			}},
		},
	}
	return book, &fakeExcerpts{bodies: map[string]string{ex.SHA256: ex.Text}}
}

func excerptEnv(t *testing.T, book report.Book, x *fakeExcerpts) *testEnv {
	t.Helper()
	e := newEnv(t, func(o *Options) { o.Reports = &fakeReports{book: book}; o.Excerpts = x })
	e.setUpAdmin(t)
	e.logIn(t)
	e.runs.add(running(7))
	return e
}

func TestBookPageShowsTheExcerpt(t *testing.T) {
	const isbn = "9780000000040"
	text := "Imprint <script>alert(1)</script>\nISBN 978-0-00-000004-0 (ebook)\nIn digits " + isbn + " and 1 \u202e978 0 00 000004 0\nPart 978000000004099 is longer\nOther ISBN 978-0-00-000006-4"
	book, x := excerptBook(text, isbn, false)
	r := excerptEnv(t, book, x).do(t, "GET", "/runs/7/books/2", nil, nil)
	if r.status != 200 {
		t.Fatalf("status %d", r.status)
	}
	body := r.body
	if !strings.Contains(body, "<details>") || !strings.Contains(body, "Front matter text (") || !strings.Contains(body, " characters)") {
		t.Errorf("no details block: %s", body)
	}
	if strings.Contains(body, ", truncated)") {
		t.Error("a whole excerpt is labelled truncated")
	}
	if strings.Contains(body, "<script>alert(1)") || !strings.Contains(body, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Error("the excerpt was not escaped")
	}
	if strings.Contains(body, "\u202e") {
		t.Error("a bidi override reached the page")
	}
	if !strings.Contains(body, "<pre") {
		t.Error("the text isn't in a pre")
	}
	for _, want := range []string{
		"<mark>978-0-00-000004-0</mark>",
		"<mark>" + isbn + "</mark>",
		"<mark>978 0 00 000004 0</mark>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("no %s in the page", want)
		}
	}
	if got := strings.Count(body, "<mark>"); got != 3 {
		t.Errorf("%d marks, want 3: a longer number and another ISBN must not be marked", got)
	}
	if x.count() != 1 {
		t.Errorf("%d excerpt reads, want 1", x.count())
	}
}

func TestBookPageLabelsATruncatedExcerpt(t *testing.T) {
	book, x := excerptBook("Synthetic front matter", "9780000000040", true)
	r := excerptEnv(t, book, x).do(t, "GET", "/runs/7/books/2", nil, nil)
	if !strings.Contains(r.body, "Front matter text (22 characters, truncated)") {
		t.Errorf("label: %s", r.body)
	}
}

func TestBookPageLabelsAnIncompleteExcerpt(t *testing.T) {
	book, x := excerptBook("Synthetic front matter", "9780000000040", false)
	book.Package.Formats[0].Excerpt.Complete = false
	r := excerptEnv(t, book, x).do(t, "GET", "/runs/7/books/2", nil, nil)
	if !strings.Contains(r.body, "Front matter text (22 characters, incomplete: a document could not be read)") {
		t.Errorf("label: %s", r.body)
	}
	book.Package.Formats[0].Excerpt.Truncated = true
	r = excerptEnv(t, book, x).do(t, "GET", "/runs/7/books/2", nil, nil)
	if !strings.Contains(r.body, "(22 characters, truncated, incomplete: a document could not be read)") {
		t.Errorf("label: %s", r.body)
	}
}

func TestBookPageSurvivesAnUnavailableExcerpt(t *testing.T) {
	book, x := excerptBook("Synthetic front matter", "9780000000040", false)
	for name, mutate := range map[string]func(e *Options){
		"missing":          func(_ *Options) { x.bodies = map[string]string{} },
		"read error":       func(_ *Options) { x.err = errors.New("corrupt") },
		"no reader":        func(o *Options) { o.Excerpts = nil },
		"wrong body":       func(_ *Options) { x.bodies = map[string]string{book.Package.Formats[0].Excerpt.SHA256: "Another text"} },
		"readable control": func(_ *Options) {},
	} {
		t.Run(name, func(t *testing.T) {
			saved, savedErr := x.bodies, x.err
			defer func() { x.bodies, x.err = saved, savedErr }()
			e := newEnv(t, func(o *Options) { o.Reports = &fakeReports{book: book}; o.Excerpts = x; mutate(o) })
			e.setUpAdmin(t)
			e.logIn(t)
			e.runs.add(running(7))
			r := e.do(t, "GET", "/runs/7/books/2", nil, nil)
			if name == "readable control" {
				if r.status != 200 || strings.Contains(r.body, "Text unavailable") {
					t.Fatalf("a readable excerpt shows as unavailable: %d", r.status)
				}
				return
			}
			if r.status != 200 || !strings.Contains(r.body, "Text unavailable") || strings.Contains(r.body, "Synthetic front matter") || strings.Contains(r.body, "Another text") {
				t.Fatalf("status %d, body %s", r.status, r.body)
			}
			if !strings.Contains(r.body, "Binary Tides") {
				t.Error("the rest of the page is missing")
			}
		})
	}
}

func TestBookPageWithoutExcerpts(t *testing.T) {
	book, x := excerptBook("x", "9780000000040", false)
	book.Package.Formats[0].Excerpt = nil
	r := excerptEnv(t, book, x).do(t, "GET", "/runs/7/books/2", nil, nil)
	if r.status != 200 || strings.Contains(r.body, "Front matter text") || x.count() != 0 {
		t.Fatalf("status %d, reads %d", r.status, x.count())
	}
}

func TestListPagesNeverLoadExcerpts(t *testing.T) {
	book, x := excerptBook("Synthetic front matter", "9780000000040", false)
	e := excerptEnv(t, book, x)
	for _, path := range []string{"/runs/7", "/runs/7/findings", "/runs/7/fixes", "/runs/7/duplicates", "/runs", "/"} {
		if r := e.do(t, "GET", path, nil, nil); r.status != 200 {
			t.Errorf("GET %s = %d, so the page never loaded", path, r.status)
		}
	}
	if x.count() != 0 {
		t.Fatalf("list pages read %d excerpts", x.count())
	}
	if r := e.do(t, "GET", "/runs/7/books/2", nil, nil); r.status != 200 || x.count() != 1 {
		t.Fatalf("book page: status %d, reads %d", r.status, x.count())
	}
}

func TestISBNMarks(t *testing.T) {
	const isbn = "9780000000040"
	tests := []struct {
		name, text string
		isbns      []string
		want       string // marks in <>, rest as is
	}{
		{"digits", "a " + isbn + " b", []string{isbn}, "a <" + isbn + "> b"},
		{"hyphens", "ISBN 978-0-00-000004-0.", []string{isbn}, "ISBN <978-0-00-000004-0>."},
		{"spaces", "978 0 00 000004 0", []string{isbn}, "<978 0 00 000004 0>"},
		{"unicode hyphens", "978\u20100\u201100\u2013000004\u20140", []string{isbn}, "<978\u20100\u201100\u2013000004\u20140>"},
		{"twice", isbn + "\n" + isbn, []string{isbn}, "<" + isbn + ">\n<" + isbn + ">"},
		{"longer number", "97800000000401", []string{isbn}, "97800000000401"},
		{"inside a longer number", "19780000000040", []string{isbn}, "19780000000040"},
		{"not across lines", "978-0-00\n000004-0", []string{isbn}, "978-0-00\n000004-0"},
		{"two ISBNs", "9780000000064 and " + isbn, []string{isbn, "9780000000064"}, "<9780000000064> and <" + isbn + ">"},
		{"none", "nothing here", []string{isbn}, "nothing here"},
		{"no hints", isbn, nil, isbn},
		{"empty hint", isbn, []string{""}, isbn},
		{"short hint", "a 123 b", []string{"123"}, "a 123 b"},
		{"hint with letters", "9780000000XYZ", []string{"9780000000XYZ"}, "9780000000XYZ"},
		{"bad hints do not stop good ones", isbn, []string{"", "123", isbn}, "<" + isbn + ">"},
		{"after a short number", "p. 12 " + isbn, []string{isbn}, "p. 12 <" + isbn + ">"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b strings.Builder
			runes := []rune(tt.text)
			for _, p := range segments(runes, isbnMarks(runes, tt.isbns), nil) {
				if p.Mark {
					b.WriteString("<" + p.Text + ">")
				} else {
					b.WriteString(p.Text)
				}
			}
			if b.String() != tt.want {
				t.Errorf("isbnMarks = %q, want %q", b.String(), tt.want)
			}
		})
	}
}

func TestViewMap(t *testing.T) {
	in := "a\u202ab\u202cc\u2066d\u2069e\u200bf\u00adg\ufeffh\nline\u202e"
	got, at := viewMap(in)
	if want := "abcdefgh\nline"; got != want {
		t.Errorf("viewMap = %q, want %q", got, want)
	}
	// Each stored offset maps to the kept runes before it.
	if want := []int{0, 1, 1, 2, 2, 3, 3, 4, 4, 5, 5, 6, 6, 7, 7, 8, 9, 10, 11, 12, 13, 13}; !slices.Equal(at, want) {
		t.Errorf("offsets = %v, want %v", at, want)
	}
	if got, _ := viewMap("Café ünï 日本語\n"); got != "Café ünï 日本語\n" {
		t.Errorf("viewMap changed ordinary text: %q", got)
	}
}

// extractionBook is excerptBook with an extraction of its excerpt: luna and
// glm ground the title, glm and the parser disagree on the year, and deepseek
// was answered by another model.
func extractionBook(t *testing.T, prefix string) (report.Book, *fakeExcerpts) {
	t.Helper()
	text := prefix + "THE LANTERN\n© 2019 Jane Doe"
	book, x := excerptBook(text, "9780000000040", false)
	f := &book.Package.Formats[0]
	start := len([]rune(prefix))
	title := func(src string) evidence.ExtractedClaim {
		return evidence.ExtractedClaim{Field: "title", Value: "THE LANTERN", Source: src, Status: "grounded", Unit: 1, Start: start, End: start + 11}
	}
	f.Extraction = &evidence.Extraction{
		PromptVersion: "extract-2", ExtractVersion: 2, ExcerptSHA256: f.Excerpt.SHA256,
		Extractors: []evidence.ExtractorRun{
			{Name: "luna", Family: "openai", Model: "opencode-go/gpt-6-luna", Effort: "low", State: evidence.ExtractOK, RawSHA256: strings.Repeat("ab", 32), Cached: true},
			{Name: "deepseek", Family: "deepseek", Model: "opencode-go/deepseek-v4.1-flash", State: evidence.ExtractModelMismatch},
			{Name: "glm", Family: "zhipu", Model: "opencode-go/glm-5.3-flash", State: evidence.ExtractOK, RawSHA256: strings.Repeat("cd", 32)},
		},
		Claims: []evidence.ExtractedClaim{
			title("luna"), title("glm"),
			{Field: "year", Value: "2019", Label: "copyright", Source: "parser", Status: "grounded", Unit: 1, Start: start + 12, End: start + 18},
			{Field: "year", Value: "2018", Label: "copyright", Source: "glm", Status: "value_not_derived", Unit: 1, Start: start + 12, End: start + 18},
		},
		Agreed: []evidence.AgreedFact{
			{Key: "title", Outcome: evidence.AgreeAgreed, Values: []evidence.AgreedValue{{Value: "THE LANTERN"}}, Voices: []string{"glm", "luna"}},
			{Key: "year/copyright", Outcome: evidence.AgreeSingle, Values: []evidence.AgreedValue{{Value: "2019", Label: "copyright"}}, Voices: []string{"parser"}},
			{Key: "edition", Outcome: evidence.AgreeDisagree, Voices: []string{"glm", "luna"}},
		},
	}
	book.Package.Calibre.PubDate = "2020-01-01"
	return book, x
}

func TestBookPageShowsTheExtraction(t *testing.T) {
	book, x := extractionBook(t, "\u202e")
	body := excerptEnv(t, book, x).do(t, "GET", "/runs/7/books/2", nil, nil).body
	for _, want := range []string{
		"Read from the front matter",
		"decisions ignore it",
		"luna · opencode-go/gpt-6-luna (low) · ok, cached",
		"deepseek · opencode-go/deepseek-v4.1-flash · model_mismatch",
		// Agreed values next to Calibre's.
		"<td>title</td><td>agreed</td><td>THE LANTERN</td><td>glm, luna</td><td>Binary Tides</td>",
		"<td>year/copyright</td><td>single</td><td>2019 (copyright)</td><td>parser</td><td>2020-01-01</td>",
		"<td>edition</td><td>disagree</td><td>—</td><td>glm, luna</td><td>—</td>",
		"1 claim was not grounded in the book’s text.",
		// The grounded quotes, highlighted at their offsets past the stripped override.
		`<mark class="claim" title="title · glm, luna">THE LANTERN</mark>`,
		`<mark class="claim" title="year (copyright) · parser">© 2019</mark>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("no %q in the page", want)
		}
	}
	if strings.Contains(body, "2018") {
		t.Error("a value the text did not derive reached the page")
	}
}

func TestBookPageShowsTheExtractionWithoutItsText(t *testing.T) {
	book, x := extractionBook(t, "")
	x.err = errors.New("gone")
	body := excerptEnv(t, book, x).do(t, "GET", "/runs/7/books/2", nil, nil).body
	if !strings.Contains(body, "<td>title</td><td>agreed</td><td>THE LANTERN</td>") || !strings.Contains(body, "Text unavailable") {
		t.Errorf("the extraction table needs no text: %s", body)
	}
	if strings.Contains(body, `class="claim"`) {
		t.Error("claims marked in a text that is unavailable")
	}
}

func TestHighlightOverlappingMarks(t *testing.T) {
	text := "ab ISBN 9780000000040 cd"
	runes := []rune(text)
	got := segments(runes, [][2]int{{8, 21}}, []claimSpan{{from: 3, to: 21, label: "isbn · glm"}, {from: 3, to: 21, label: "isbn · parser"}})
	var b strings.Builder
	for _, p := range got {
		switch {
		case p.Mark && p.Claim != "":
			b.WriteString("[" + p.Text + "|" + p.Claim + "]")
		case p.Claim != "":
			b.WriteString("{" + p.Text + "|" + p.Claim + "}")
		case p.Mark:
			b.WriteString("<" + p.Text + ">")
		default:
			b.WriteString(p.Text)
		}
	}
	if want := "ab {ISBN |isbn · glm; isbn · parser}[9780000000040|isbn · glm; isbn · parser] cd"; b.String() != want {
		t.Errorf("segments = %q, want %q", b.String(), want)
	}
}

func TestBookPageExtractionEdges(t *testing.T) {
	const isbn = "9780000000040"
	text := "THE LAN\u200bTERN\nISBN " + isbn
	book, x := excerptBook(text, isbn, false)
	f := &book.Package.Formats[0]
	end := len([]rune(text))
	f.Extraction = &evidence.Extraction{
		ExcerptSHA256: f.Excerpt.SHA256,
		Extractors:    []evidence.ExtractorRun{{Name: "glm", Model: "m", State: evidence.ExtractOK}},
		Claims: []evidence.ExtractedClaim{
			// A format character inside the quote, and a quote that ends the text.
			{Field: "title", Value: "THE LANTERN", Source: "glm", Status: "grounded", Unit: 1, Start: 0, End: 12},
			{Field: "isbn", Value: isbn, Source: "parser", Status: "grounded", Unit: 1, Start: end - 13, End: end},
			// Past the stored text: never placed.
			{Field: "title", Value: "X", Source: "glm", Status: "grounded", Unit: 1, Start: end, End: end + 5},
			{Field: "year", Source: "glm", Status: "excluded_section", Unit: 1},
			{Field: "edition", Source: "glm", Status: "not_found"},
		},
	}
	body := excerptEnv(t, book, x).do(t, "GET", "/runs/7/books/2", nil, nil).body
	for _, want := range []string{
		`<mark class="claim" title="title · glm">THE LANTERN</mark>`,
		// A claimed hint keeps its hint mark inside the claim's.
		`<mark class="claim" title="isbn · parser"><mark>` + isbn + `</mark></mark>`,
		"No field was read from the text.",
		"2 claims were not grounded in the book’s text.",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("no %q in the page", want)
		}
	}
	if strings.Count(body, `class="claim"`) != 2 {
		t.Errorf("%d claim marks, want 2", strings.Count(body, `class="claim"`))
	}
}
