package sources_test

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/sources"
)

func olFetcher() *fakeFetcher {
	return &fakeFetcher{routes: map[string]answer{
		"/isbn/9783551551672.json": file("ol-edition-OL27958188M.json"),
		"/works/OL82563W.json":     file("ol-work-OL82563W.json"),
		"/authors/OL23919A.json":   file("ol-author-OL23919A.json"),
	}}
}

func TestOpenLibrary(t *testing.T) {
	f := olFetcher()
	outcome, recs := lookup(t, sources.OpenLibrary(), f, "9783551551672")
	want := evidence.SourceRecord{ID: "/books/OL27958188M", Title: "Harry Potter und der Stein der Weisen", Authors: []string{"J. K. Rowling"},
		Languages: []string{"deu"}, Publisher: "Carlsen", Date: "2000", ISBNs: []string{"9783551551672"}}
	if outcome != evidence.SourceOK || len(recs) != 1 || !equalRecord(recs[0], want) {
		t.Fatalf("%s %+v", outcome, recs)
	}
	if !slices.Equal(f.urls, []string{"https://test.invalid/isbn/9783551551672.json", "https://test.invalid/works/OL82563W.json", "https://test.invalid/authors/OL23919A.json"}) {
		t.Fatalf("urls = %v", f.urls)
	}
	if outcome, _ := lookup(t, sources.OpenLibrary(), &fakeFetcher{}, "9783551551672"); outcome != evidence.SourceNoMatch {
		t.Fatalf("404: %s", outcome)
	}
}

func TestOpenLibraryISBNMismatch(t *testing.T) {
	edition := strings.Replace(string(file("ol-edition-OL27958188M.json").body), "9783551551672", "9780306406157", 1)
	edition = strings.Replace(edition, `"3551551677"`, `"0306406152"`, 1)
	f := &fakeFetcher{routes: map[string]answer{"/isbn/9783551551672.json": {status: 200, body: []byte(edition)}}}
	if outcome, recs := lookup(t, sources.OpenLibrary(), f, "9783551551672"); outcome != evidence.SourceNoMatch || recs != nil || len(f.urls) != 1 {
		t.Fatalf("%s %+v, %d requests", outcome, recs, len(f.urls))
	}
}

func TestOpenLibraryPartialAuthors(t *testing.T) {
	f := olFetcher()
	f.routes["/authors/OL23919A.json"] = answer{err: sources.ErrUnavailable}
	if outcome, recs := lookup(t, sources.OpenLibrary(), f, "9783551551672"); outcome != evidence.SourceUnavailable || recs != nil {
		t.Fatalf("%s %+v", outcome, recs)
	}
}

func TestOpenLibraryHostile(t *testing.T) {
	for name, edition := range map[string]string{
		"traversal work key":   `{"key":"/books/OL1M","title":"T","isbn_13":["9783551551672"],"works":[{"key":"/works/../../admin"}]}`,
		"author key elsewhere": `{"key":"/books/OL1M","title":"T","isbn_13":["9783551551672"],"authors":[{"key":"https://evil.example/authors/OL1A"}]}`,
		"wrong types":          `{"key":"/books/OL1M","title":7,"isbn_13":["9783551551672"]}`,
		"no key":               `{"title":"T","isbn_13":["9783551551672"]}`,
		"not json":             `<html>`,
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeFetcher{routes: map[string]answer{"/isbn/9783551551672.json": {status: 200, body: []byte(edition)}}}
			outcome, recs := lookup(t, sources.OpenLibrary(), f, "9783551551672")
			if outcome != evidence.SourceUnavailable || recs != nil || len(f.urls) != 1 {
				t.Fatalf("%s %+v, urls %v", outcome, recs, f.urls)
			}
		})
	}
}

func FuzzOpenLibrary(f *testing.F) {
	f.Add(file("ol-edition-OL27958188M.json").body)
	f.Fuzz(func(t *testing.T, edition []byte) {
		ff := olFetcher()
		ff.routes["/isbn/9783551551672.json"] = answer{status: 200, body: edition}
		_, recs := lookup(t, sources.OpenLibrary(), ff, "9783551551672")
		for _, r := range recs {
			if !slices.Contains(r.ISBNs, "9783551551672") || len(r.Authors) > 8 || len(r.SourceRecords) > 16 {
				t.Fatalf("record %+v", r)
			}
		}
		for _, u := range ff.urls {
			if !strings.HasPrefix(u, "https://test.invalid/") || strings.Contains(u, "..") {
				t.Fatalf("request to %s", u)
			}
		}
	})
}

// TestOpenLibraryRedirectRecords: Open Library answers a merged author or
// work with 200 and {"type":{"key":"/type/redirect"},"location":...}. One
// redirect is followed; anything else fails closed rather than dropping an
// author.
func TestOpenLibraryRedirectRecords(t *testing.T) {
	redirect := func(to string) answer {
		return answer{status: 200, body: []byte(`{"key":"x","type":{"key":"/type/redirect"},"location":"` + to + `"}`)}
	}
	author := answer{status: 200, body: []byte(`{"key":"/authors/OL2A","type":{"key":"/type/author"},"name":"J. K. Rowling"}`)}
	for name, tt := range map[string]struct {
		routes  map[string]answer
		outcome string
		authors []string
	}{
		"author redirect followed": {map[string]answer{"/authors/OL23919A.json": redirect("/authors/OL2A"), "/authors/OL2A.json": author}, evidence.SourceOK, []string{"J. K. Rowling"}},
		"work redirect followed": {map[string]answer{
			"/works/OL82563W.json": redirect("/works/OL9W"), "/works/OL9W.json": file("ol-work-OL82563W.json"),
		}, evidence.SourceOK, []string{"J. K. Rowling"}},
		"redirect chain":         {map[string]answer{"/authors/OL23919A.json": redirect("/authors/OL2A"), "/authors/OL2A.json": redirect("/authors/OL3A")}, evidence.SourceUnavailable, nil},
		"redirect off the graph": {map[string]answer{"/authors/OL23919A.json": redirect("https://evil.example/authors/OL2A")}, evidence.SourceUnavailable, nil},
		"author without a name":  {map[string]answer{"/authors/OL23919A.json": {status: 200, body: []byte(`{"key":"/authors/OL23919A","type":{"key":"/type/author"}}`)}}, evidence.SourceUnavailable, nil},
	} {
		t.Run(name, func(t *testing.T) {
			f := olFetcher()
			maps.Copy(f.routes, tt.routes)
			outcome, recs := lookup(t, sources.OpenLibrary(), f, "9783551551672")
			if outcome != tt.outcome || (tt.outcome == evidence.SourceOK && !slices.Equal(recs[0].Authors, tt.authors)) || (tt.outcome != evidence.SourceOK && recs != nil) {
				t.Fatalf("%s %+v", outcome, recs)
			}
		})
	}
}
