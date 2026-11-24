package web

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/report"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

type fakeReports struct {
	csv       func(io.Writer) error
	err       error
	book      report.Book
	page      report.Page
	books     report.BooksPage
	fixes     report.FixPage
	fixPages  map[int]report.FixPage // by the cursor's book id, 0 for the first page; overrides fixes
	fixFilter report.FixFilter
	dups      report.DuplicatePage
	bookDups  report.DuplicatePage
	dupFilter report.DuplicateFilter
}

func (f *fakeReports) CSV(_ context.Context, _ int64, _ report.Filter, w io.Writer, _ report.CSVLimits) error {
	if f.csv != nil {
		return f.csv(w)
	}
	if f.err != nil {
		return f.err
	}
	_, err := io.WriteString(w, "run_id,book_id,title,code,format,field,reason\n7,2,Title,title_mismatch,PDF,title,\n")
	return err
}

func (f *fakeReports) Book(context.Context, int64, int) (report.Book, error) { return f.book, f.err }
func (f *fakeReports) Books(context.Context, int64, int) (report.BooksPage, error) {
	return f.books, f.err
}
func (f *fakeReports) Findings(context.Context, int64, report.Filter, *report.Cursor) (report.Page, error) {
	return f.page, f.err
}

func TestFindingsSignIn(t *testing.T) {
	e := newEnv(t)
	e.setUpAdmin(t)
	for _, path := range []string{"/findings", "/runs/7/findings", "/runs/7/books/2"} {
		r := e.do(t, http.MethodGet, path, nil, nil)
		if r.status != 303 || r.header.Get("Location") != "/login" {
			t.Fatalf("%s=%d", path, r.status)
		}
	}
}
func TestFindingsSelection(t *testing.T) {
	e := signedIn(t)
	if r := e.do(t, "GET", "/findings", nil, nil); r.status != 200 || !strings.Contains(r.body, "No checks yet.") {
		t.Fatalf("empty=%d/%s", r.status, r.body)
	}
	for _, v := range []verify.RunView{{Run: verify.Run{ID: 5, Status: verify.RunCompleted}}, {Run: verify.Run{ID: 6, Status: verify.RunCompleted, Scope: verify.Scope{BookID: 2}}}, {Run: verify.Run{ID: 7, Status: verify.RunRunning}}} {
		e.runs.add(v)
	}
	if r := e.do(t, "GET", "/findings", nil, nil); r.status != 303 || r.header.Get("Location") != "/runs/5/findings" {
		t.Fatalf("selection=%d/%s", r.status, r.header.Get("Location"))
	}
	e.runs.mu.Lock()
	delete(e.runs.runs, 5)
	e.runs.mu.Unlock()
	if r := e.do(t, "GET", "/findings", nil, nil); r.header.Get("Location") != "/runs/6/findings" {
		t.Fatalf("fallback=%s", r.header.Get("Location"))
	}
}
func TestFindingsQueries(t *testing.T) {
	e := signedIn(t)
	e.runs.add(running(7))
	badCursor := base64.RawURLEncoding.EncodeToString([]byte(`{"run_id":8,"book_id":2,"ordinal":0}`))
	unknownCursor := base64.RawURLEncoding.EncodeToString([]byte(`{"run_id":7,"book_id":2,"ordinal":0,"x":1}`))
	for _, path := range []string{"/runs/no/findings", "/runs/0/findings", "/runs/7/books/no", "/runs/7/books/0", "/runs/7/findings?code=invented", "/runs/7/findings?format=epub", "/runs/7/findings?book_id=-1", "/runs/7/findings?x=1", "/runs/7/findings?code=no_formats&code=no_formats", "/runs/7/findings?cursor=" + strings.Repeat("a", 129), "/runs/7/findings?cursor=" + badCursor, "/runs/7/findings?cursor=" + unknownCursor, "/runs/7?after_book=-1", "/runs/7?after_book=abc", "/runs/7?x=1", "/runs/7?after_book=1&after_book=2", "/runs/7/books/2?x=1"} {
		if r := e.do(t, "GET", path, nil, nil); r.status != 400 {
			t.Errorf("%s=%d want400", path, r.status)
		}
	}
	if r := e.do(t, "GET", "/runs/99/findings", nil, nil); r.status != 404 {
		t.Fatalf("missing=%d", r.status)
	}
}
func TestFindingsViews(t *testing.T) {
	f := &fakeReports{
		book:  report.Book{Ref: library.BookRef{ID: 2, UUID: "frozen"}, State: verify.BookChecked, Package: &evidence.Package{Calibre: evidence.Record{Title: `<img src=x onerror="alert(1)">`, Authors: []string{"<script>author</script>"}}, Formats: []evidence.FormatEvidence{{Format: "PDF", State: evidence.StateOK, Reason: evidence.ReasonXrefRepaired}}}, FindingsVersion: 2, Findings: []evidence.Finding{{Code: "title_mismatch", Format: "PDF", Field: "title"}}},
		page:  report.Page{Rows: []report.Row{{RunID: 7, BookID: 2, Title: "<script>title</script>", Finding: evidence.Finding{Code: "title_mismatch", Format: "PDF"}}}, Next: &report.Cursor{RunID: 7, BookID: 2, Ordinal: 49}},
		books: report.BooksPage{Rows: []report.BookSummary{{Ref: library.BookRef{ID: 2}, State: verify.BookChecked, Title: "Clean"}, {Ref: library.BookRef{ID: 3}, State: verify.BookFailed}}, NextBookID: 3},
	}
	e := newEnv(t, func(o *Options) { o.Reports = f })
	e.setUpAdmin(t)
	e.logIn(t)
	e.runs.add(running(7))
	for _, path := range []string{"/runs/7/findings?code=title_mismatch&format=PDF", "/runs/7/books/2", "/runs/7"} {
		r := e.do(t, "GET", path, nil, nil)
		if r.status != 200 || r.header.Get("Cache-Control") != "no-store" {
			t.Fatalf("%s=%d", path, r.status)
		}
		if strings.Contains(r.body, "<script>title") || strings.Contains(r.body, "<script>author") || strings.Contains(r.body, "<img src=x") {
			t.Fatal("metadata was not escaped")
		}
		if path == "/runs/7" {
			for _, want := range []string{`href="/runs/7/books/2"`, `href="/runs/7/books/3"`, `after_book=3`} {
				if !strings.Contains(r.body, want) {
					t.Fatalf("missing %s", want)
				}
			}
		}
		if strings.Contains(path, "findings") {
			for _, want := range []string{"partial", `href="/runs/7/books/2"`, `code=title_mismatch`, `format=PDF`, `cursor=`} {
				if !strings.Contains(r.body, want) {
					t.Fatalf("missing %s", want)
				}
			}
			if strings.Contains(r.body, `name="cursor"`) {
				t.Fatal("filter form retained cursor")
			}
		}
		if strings.Contains(path, "books") {
			for _, want := range []string{"frozen", "xref_repaired", "title_mismatch", "&lt;script&gt;author"} {
				if !strings.Contains(r.body, want) {
					t.Fatalf("book lacks %s", want)
				}
			}
		}
	}
	sort := "<b>Sort</b>, The"
	f.book.Package.Calibre.TitleSort = &sort
	f.book.Package.Formats[0].ISBNHints = &evidence.ISBNHints{
		Method: evidence.HintOCR, State: evidence.HintTimeout, Pages: []int{1, 2}, ISBNs: []string{"9780000000057"}, Lang: "deu",
		Context: []evidence.ISBNContext{{ISBN: "9780000000057", Landmark: evidence.LandmarkCopyright, Qualifier: evidence.QualifierEbook}},
	}
	r := e.do(t, "GET", "/runs/7/books/2", nil, nil)
	for _, want := range []string{"Title sort", "&lt;b&gt;Sort&lt;/b&gt;, The", "ISBN hints", "ocr", "timeout", "1 2", "9780000000057", "OCR language", "deu", "copyright", "ebook"} {
		if !strings.Contains(r.body, want) {
			t.Fatalf("book page lacks %q", want)
		}
	}
	f.book.Package.Calibre.TitleSort = nil
	f.book.Package.Formats[0].ISBNHints = nil
	if r := e.do(t, "GET", "/runs/7/books/2", nil, nil); strings.Contains(r.body, "Title sort") || strings.Contains(r.body, "ISBN hints") {
		t.Fatal("absent title sort or hints were rendered")
	}
	if strings.Contains(r.body, "<h2>Sources</h2>") {
		t.Fatal("a package without lookups shows a Sources section")
	}
	f.book.Package.Lookups = []evidence.Lookup{{ISBN: "9783551551672", Seeds: []string{"file", "ocr"}, Results: []evidence.SourceResult{
		{Source: evidence.SourceDNB, Outcome: evidence.SourceOK, Records: []evidence.SourceRecord{{
			ID: "958577617", Title: "<b>x</b>", Subtitle: "Sub", Authors: []string{"Rowling, J. K."}, Languages: []string{"deu"},
			Publisher: "Carlsen", Date: "2000", ISBNs: []string{"9783551551672"}, SourceRecords: []string{"<i>marc</i>"},
		}}},
		{Source: evidence.SourceBNE, Outcome: evidence.SourceNoMatch},
		{Source: evidence.SourceWikidata, Outcome: evidence.SourceOK, Records: []evidence.SourceRecord{{
			ID: "Q100000001", Title: "Wikidata title", ISBNs: []string{"9783551551672"},
		}}},
		{Source: evidence.SourceHardcover, Outcome: evidence.SourceNotConfigured},
	}}}
	r = e.do(t, "GET", "/runs/7/books/2", nil, nil)
	for _, want := range []string{"<h2>Sources</h2>", "wikidata", "Q100000001", "Wikidata title", "hardcover", "not_configured", "9783551551672", "file", "ocr", "dnb", "958577617", "&lt;b&gt;x&lt;/b&gt;", "Sub", "Rowling, J. K.", "deu", "Carlsen", "2000", "&lt;i&gt;marc&lt;/i&gt;", "bne", "no_match"} {
		if !strings.Contains(r.body, want) {
			t.Fatalf("book page lacks %q", want)
		}
	}
	if strings.Contains(r.body, "<b>x</b>") || strings.Contains(r.body, "<i>marc") {
		t.Fatal("source record was not escaped")
	}
	f.book.Package.Lookups = nil
	f.book.Package.Formats[0].Metadata = &evidence.FileMetadata{Title: "Retained prefix", Limits: &evidence.MetadataLimits{Version: 1, Title: true}}
	f.book.Package.FindingsVersion = 1
	f.book.Package.Findings = nil // Historical findings differ from the current view.
	f.book.Findings = []evidence.Finding{{Code: "metadata_unverified", Format: "PDF", Field: "title", Reason: "field_too_long"}}
	if r := e.do(t, "GET", "/runs/7/books/2", nil, nil); r.status != 200 || !strings.Contains(r.body, "metadata_unverified") || !strings.Contains(r.body, "capture limits") || strings.Contains(r.body, "No local findings.") {
		t.Fatalf("current capture findings were hidden: %d/%s", r.status, r.body)
	}
	for _, tt := range []struct {
		err    error
		status int
	}{{report.ErrNoSuchBook, 404}, {report.ErrIntegrity, 500}, {context.DeadlineExceeded, 504}} {
		f.err = tt.err
		r := e.do(t, "GET", "/runs/7/books/2", nil, nil)
		if r.status != tt.status || strings.Contains(r.body, "frozen") {
			t.Fatalf("error=%d/%s", r.status, r.body)
		}
	}
	f.err = nil
	f.book = report.Book{Ref: library.BookRef{ID: 2}, State: verify.BookSourceChanged, Reason: "changed"}
	if r := e.do(t, "GET", "/runs/7/books/2", nil, nil); r.status != 200 || !strings.Contains(r.body, "source_changed") || !strings.Contains(r.body, "changed") {
		t.Fatalf("operational=%d/%s", r.status, r.body)
	}
}
