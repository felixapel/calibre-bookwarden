package web

import (
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/report"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

// The book page shows the series and its index from Calibre, from the file
// and from a source record, escaped, and only when there is one.
func TestBookPageSeries(t *testing.T) {
	f := &fakeReports{book: report.Book{Ref: library.BookRef{ID: 2, UUID: "frozen"}, State: verify.BookChecked, Package: &evidence.Package{
		Calibre: evidence.Record{Title: "T", Series: "<b>Calibre Saga</b>", SeriesIndex: "2.5"},
		Formats: []evidence.FormatEvidence{{Format: "EPUB", State: evidence.StateOK, Metadata: &evidence.FileMetadata{
			Title: "T", Series: "File Saga", SeriesIndex: "3", Limits: &evidence.MetadataLimits{Version: 1, SeriesIndex: true},
		}}},
		Lookups: []evidence.Lookup{{ISBN: "9783551551672", Seeds: []string{"file"}, Results: []evidence.SourceResult{
			{Source: evidence.SourceWikidata, Outcome: evidence.SourceOK, Records: []evidence.SourceRecord{{
				ID: "Q100000001", ISBNs: []string{"9783551551672"}, Series: "Wikidata Saga", SeriesIndex: "0.5",
			}}},
		}}},
	}}}
	e := newEnv(t, func(o *Options) { o.Reports = f })
	e.setUpAdmin(t)
	e.logIn(t)
	e.runs.add(running(7))
	r := e.do(t, "GET", "/runs/7/books/2", nil, nil)
	if r.status != 200 {
		t.Fatalf("status %d", r.status)
	}
	for _, want := range []string{"<dt>Series</dt>", "&lt;b&gt;Calibre Saga&lt;/b&gt; #2.5", "File Saga #3", "Wikidata Saga #0.5", "capture limits"} {
		if !strings.Contains(r.body, want) {
			t.Errorf("book page lacks %q", want)
		}
	}
	if strings.Contains(r.body, "<b>Calibre Saga") {
		t.Error("the series was not escaped")
	}
	f.book.Package.Calibre.Series, f.book.Package.Calibre.SeriesIndex = "", ""
	f.book.Package.Formats[0].Metadata = &evidence.FileMetadata{Title: "T"}
	f.book.Package.Lookups = nil
	if r := e.do(t, "GET", "/runs/7/books/2", nil, nil); strings.Contains(r.body, "<dt>Series</dt>") {
		t.Error("a book without a series shows a Series row")
	}
}
