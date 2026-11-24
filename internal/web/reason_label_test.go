package web

import (
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/report"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

// TestFindingReasonsAreReadable: the Findings and Book pages show the closed
// reasons of the version-5 findings as text, and keep the raw code of others.
func TestFindingReasonsAreReadable(t *testing.T) {
	joined := evidence.Finding{Code: "joined_authors", Field: "authors", Reason: "y"}
	f := &fakeReports{
		book: report.Book{Ref: library.BookRef{ID: 2, UUID: "frozen"}, State: verify.BookChecked, FindingsVersion: 5,
			Package:  &evidence.Package{Calibre: evidence.Record{Title: "T"}},
			Findings: []evidence.Finding{joined, {Code: "file_missing", Format: "EPUB", Reason: "missing_on_disk"}}},
		page: report.Page{Rows: []report.Row{
			{RunID: 7, BookID: 2, Title: "T", Finding: joined},
			{RunID: 7, BookID: 2, Title: "T", Finding: evidence.Finding{Code: "junk_title", Field: "title", Reason: "file_extension"}},
			{RunID: 7, BookID: 2, Title: "T", Finding: evidence.Finding{Code: "file_missing", Format: "EPUB", Reason: "missing_on_disk"}},
		}},
	}
	e := newEnv(t, func(o *Options) { o.Reports = f })
	e.setUpAdmin(t)
	e.logIn(t)
	e.runs.add(running(7))
	for _, tt := range []struct {
		path string
		want []string
	}{
		{"/runs/7/findings", []string{"two names joined by &#39;y&#39;", "ends in a file extension", "missing_on_disk"}},
		{"/runs/7/books/2", []string{"two names joined by &#39;y&#39;", "missing_on_disk"}},
	} {
		r := e.do(t, "GET", tt.path, nil, nil)
		if r.status != 200 {
			t.Fatalf("%s = %d", tt.path, r.status)
		}
		for _, want := range tt.want {
			if !strings.Contains(r.body, want) {
				t.Errorf("%s lacks %q", tt.path, want)
			}
		}
		if strings.Contains(r.body, "<br>y<") || strings.Contains(r.body, "· y<") {
			t.Errorf("%s shows the raw reason y", tt.path)
		}
	}
}
