package web

import (
	"context"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/duplicates"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/report"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

func (f *fakeReports) Duplicates(_ context.Context, _ int64, df report.DuplicateFilter) (report.DuplicatePage, error) {
	f.dupFilter = df
	if err := report.ValidateDuplicateFilter(df); err != nil {
		return report.DuplicatePage{}, err
	}
	return f.dups, f.err
}
func (f *fakeReports) BookDuplicates(context.Context, int64, int) (report.DuplicatePage, error) {
	return f.bookDups, f.err
}

func dupEnv(t *testing.T, f *fakeReports) *testEnv {
	t.Helper()
	e := newEnv(t, func(o *Options) { o.Reports = f })
	e.setUpAdmin(t)
	e.logIn(t)
	e.runs.add(verify.RunView{Run: verify.Run{ID: 7, Status: verify.RunCompleted}})
	return e
}

func TestDuplicatesPage(t *testing.T) {
	f := &fakeReports{dups: report.DuplicatePage{
		Counts: map[string]int{duplicates.KindISBN: 1, duplicates.KindCover: 1},
		Page:   2, More: true,
		Groups: []report.DuplicateGroup{
			{Kind: duplicates.KindISBN, Value: "9780000000002", Label: "9780000000002", More: 290, Members: []report.DuplicateMember{
				{BookID: 1, Title: "The <Clockwork> Garden", Detail: "Calibre, EPUB"}, {BookID: 9, Title: "Copy", Detail: "EPUB"}}},
			{Kind: duplicates.KindCover, Members: []report.DuplicateMember{{BookID: 9, Title: "Copy"}, {BookID: 10, Title: "Sawtooth Proof"}}},
		},
	}}
	e := dupEnv(t, f)
	r := e.do(t, "GET", "/runs/7/duplicates?kind=isbn&page=2", nil, nil)
	if r.status != 200 || f.dupFilter != (report.DuplicateFilter{Kind: duplicates.KindISBN, Page: 2}) {
		t.Fatalf("status %d, filter %+v", r.status, f.dupFilter)
	}
	for _, want := range []string{
		"Same ISBN (1)", "Similar cover, a hint only (1)", "9780000000002",
		`href="/runs/7/books/1"`, "The &lt;Clockwork&gt; Garden", "Calibre, EPUB", "and 290 more",
		"near-identical covers can be different books", "Merging is done in Calibre",
		`href="/runs/7/duplicates?kind=isbn&amp;page=3"`, `href="/runs/7/duplicates?kind=isbn&amp;page=1"`,
	} {
		if !strings.Contains(r.body, want) {
			t.Errorf("duplicates page lacks %q", want)
		}
	}
	if strings.Contains(r.body, "/merge") || strings.Contains(r.body, ">Merge") || strings.Contains(r.body, "/delete") {
		t.Error("the duplicates page offers a merge")
	}
	f.dups = report.DuplicatePage{Page: 1, Counts: map[string]int{}}
	if r := e.do(t, "GET", "/runs/7/duplicates", nil, nil); !strings.Contains(r.body, "No possible duplicates in this check.") || f.dupFilter.Page != 1 {
		t.Errorf("empty page: %d %+v", r.status, f.dupFilter)
	}
}

func TestDuplicatesLatest(t *testing.T) {
	e := signedIn(t)
	if r := e.do(t, "GET", "/duplicates", nil, nil); r.status != 200 || !strings.Contains(r.body, "No checks yet.") {
		t.Fatalf("no runs: %d", r.status)
	}
	e.runs.add(verify.RunView{Run: verify.Run{ID: 5, Status: verify.RunCompleted}})
	e.runs.add(verify.RunView{Run: verify.Run{ID: 6, Status: verify.RunCompleted, Scope: verify.Scope{BookID: 2}}})
	if r := e.do(t, "GET", "/duplicates", nil, nil); r.status != 303 || r.header.Get("Location") != "/runs/5/duplicates" {
		t.Fatalf("latest: %d %s", r.status, r.header.Get("Location"))
	}
	// A newer one-book recheck never shows duplicates; an older cancelled
	// whole-library run does.
	e.runs.mu.Lock()
	delete(e.runs.runs, 5)
	e.runs.mu.Unlock()
	e.runs.add(verify.RunView{Run: verify.Run{ID: 4, Status: verify.RunCancelled}})
	if r := e.do(t, "GET", "/duplicates", nil, nil); r.status != 303 || r.header.Get("Location") != "/runs/4/duplicates" {
		t.Fatalf("after a recheck: %d %s", r.status, r.header.Get("Location"))
	}
	e.runs.mu.Lock()
	delete(e.runs.runs, 4)
	e.runs.mu.Unlock()
	if r := e.do(t, "GET", "/duplicates", nil, nil); r.status != 200 || !strings.Contains(r.body, "No whole-library checks yet.") {
		t.Fatalf("only rechecks: %d %s", r.status, r.header.Get("Location"))
	}
	if r := e.do(t, "GET", "/duplicates?x=1", nil, nil); r.status != 400 {
		t.Fatalf("unknown parameter: %d", r.status)
	}
}

func TestDuplicatesPageStates(t *testing.T) {
	f := &fakeReports{}
	e := dupEnv(t, f)
	for _, path := range []string{"/runs/7/duplicates?page=0", "/runs/7/duplicates?page=x", "/runs/7/duplicates?page=10001", "/runs/7/duplicates?kind=bogus",
		"/runs/7/duplicates?x=1", "/runs/7/duplicates?page=1&page=2", "/runs/no/duplicates"} {
		if r := e.do(t, "GET", path, nil, nil); r.status != 400 {
			t.Errorf("%s = %d", path, r.status)
		}
	}
	if r := e.do(t, "GET", "/runs/8/duplicates", nil, nil); r.status != 404 {
		t.Errorf("unknown run = %d", r.status)
	}
	for _, tt := range []struct {
		page report.DuplicatePage
		want string
	}{
		{report.DuplicatePage{Unavailable: report.DuplicatesSingleBook}, "Duplicates are compared within a whole-library check."},
		{report.DuplicatePage{Unavailable: report.DuplicatesPredate}, "This check predates duplicate detection."},
		{report.DuplicatePage{Unavailable: report.DuplicatesUnknownVersion}, "These duplicates were found under rules this version doesn"},
		{report.DuplicatePage{Partial: true, Counts: map[string]int{}}, "This check didn’t finish; only the books it checked were compared."},
	} {
		f.dups = tt.page
		if r := e.do(t, "GET", "/runs/7/duplicates", nil, nil); r.status != 200 || !strings.Contains(r.body, tt.want) {
			t.Errorf("%+v: %d lacks %q", tt.page, r.status, tt.want)
		}
	}
}

func TestBookPageDuplicates(t *testing.T) {
	f := &fakeReports{
		book: report.Book{Ref: library.BookRef{ID: 9, UUID: "u"}, State: verify.BookChecked, Package: &evidence.Package{Calibre: evidence.Record{Title: "Copy"}}},
		bookDups: report.DuplicatePage{Groups: []report.DuplicateGroup{
			{Kind: duplicates.KindFile, Label: "EPUB · aaaaaaaaaaaa", Members: []report.DuplicateMember{{BookID: 1, Title: "The <Clockwork> Garden", Detail: "EPUB"}}, More: 3},
		}},
	}
	e := dupEnv(t, f)
	r := e.do(t, "GET", "/runs/7/books/9", nil, nil)
	for _, want := range []string{"<h2>Possible duplicates</h2>", "Same file", "EPUB · aaaaaaaaaaaa", `href="/runs/7/books/1"`, "The &lt;Clockwork&gt; Garden", "and 3 more", `href="/runs/7/duplicates"`} {
		if !strings.Contains(r.body, want) {
			t.Errorf("book page lacks %q", want)
		}
	}
	f.bookDups = report.DuplicatePage{}
	if r := e.do(t, "GET", "/runs/7/books/9", nil, nil); !strings.Contains(r.body, "No possible duplicates in this check.") {
		t.Error("no 'no duplicates' line")
	}
	f.bookDups = report.DuplicatePage{Unavailable: report.DuplicatesSingleBook}
	if r := e.do(t, "GET", "/runs/7/books/9", nil, nil); !strings.Contains(r.body, "Duplicates are compared within a whole-library check.") {
		t.Error("no one-book notice")
	}
}
