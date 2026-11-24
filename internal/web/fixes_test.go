package web

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/decide"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/report"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

func (f *fakeReports) Fixes(_ context.Context, _ int64, filter report.FixFilter, c *report.Cursor) (report.FixPage, error) {
	f.fixFilter = filter
	if f.fixPages != nil {
		key := 0
		if c != nil {
			key = c.BookID
		}
		return f.fixPages[key], f.err
	}
	return f.fixes, f.err
}

func TestFixesView(t *testing.T) {
	proposal := evidence.Proposal{Field: "title", Current: []string{"Old <i>t</i>"}, Proposed: []string{"<b>x</b>"}, Roots: []string{"dnb", "file"}}
	f := &fakeReports{
		fixes: report.FixPage{Rows: []report.FixRow{{RunID: 7, BookID: 2, Title: "<b>x</b>", Tier: "B", Proposal: proposal}}, Next: &report.Cursor{RunID: 7, BookID: 2, Ordinal: 0}},
		book: report.Book{Ref: library.BookRef{ID: 2, UUID: "frozen"}, State: verify.BookChecked, Package: &evidence.Package{Calibre: evidence.Record{Title: "T"}},
			Decision: decide.Decision{Tier: "B", Reasons: []string{"no_record"}, Proposals: []evidence.Proposal{proposal}}},
	}
	e := newEnv(t, func(o *Options) { o.Reports = f })
	e.setUpAdmin(t)
	e.logIn(t)
	e.runs.add(verify.RunView{Run: verify.Run{ID: 5, Status: verify.RunCompleted}})
	e.runs.add(running(7))
	if r := e.do(t, "GET", "/fixes", nil, nil); r.status != 303 || r.header.Get("Location") != "/runs/5/fixes" {
		t.Fatalf("/fixes = %d %s", r.status, r.header.Get("Location"))
	}
	r := e.do(t, "GET", "/runs/7/fixes?field=title&tier=B&book_id=2", nil, nil)
	if r.status != 200 || r.header.Get("Cache-Control") != "no-store" || !reflect.DeepEqual(f.fixFilter, report.FixFilter{Field: "title", Tier: "B", BookID: 2}) {
		t.Fatalf("fixes = %d, filter %+v", r.status, f.fixFilter)
	}
	for _, want := range []string{"&lt;b&gt;x&lt;/b&gt;", "Old &lt;i&gt;t&lt;/i&gt;", "Tier B", "dnb", "file", "→", `href="/runs/7/books/2"`, "cursor=", "approving"} {
		if !strings.Contains(r.body, want) {
			t.Fatalf("fixes page lacks %q", want)
		}
	}
	if strings.Contains(r.body, "<b>x</b>") || strings.Count(r.body, `method="post"`) != 1 {
		t.Fatal("unescaped value, or a form besides sign-out")
	}
	for _, path := range []string{"/runs/7/fixes?field=series", "/runs/7/fixes?tier=D", "/runs/7/fixes?x=1", "/runs/7/fixes?book_id=-1", "/runs/no/fixes"} {
		if r := e.do(t, "GET", path, nil, nil); r.status != 400 {
			t.Errorf("%s = %d, want 400", path, r.status)
		}
	}
	r = e.do(t, "GET", "/runs/7/books/2", nil, nil)
	for _, want := range []string{"Tier B", "No source record for the files’ ISBN", "&lt;b&gt;x&lt;/b&gt;", "Proposed fixes"} {
		if !strings.Contains(r.body, want) {
			t.Fatalf("book page lacks %q", want)
		}
	}
	f.book = report.Book{Ref: library.BookRef{ID: 2, UUID: "frozen"}, State: verify.BookFailed, Reason: "calibre_error"}
	if r := e.do(t, "GET", "/runs/7/books/2", nil, nil); strings.Contains(r.body, "Tier") || strings.Contains(r.body, "Proposed fixes") {
		t.Fatal("a book without evidence shows a tier")
	}
}
