package sources_test

import (
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/sources"
)

func gbFetcher(a answer) *fakeFetcher {
	return &fakeFetcher{routes: map[string]answer{"/books/v1/volumes": a}}
}

func TestGoogleBooksAcceptance(t *testing.T) {
	f := gbFetcher(file("gb-volume.json"))
	outcome, recs := lookup(t, sources.GoogleBooks("secret-key"), f, "9783551551672")
	want := evidence.SourceRecord{ID: "synthetic-match", Title: "Harry Potter und der Stein der Weisen", Authors: []string{"J. K. Rowling"},
		Languages: []string{"deu"}, Publisher: "Carlsen", Date: "1998-07-01", ISBNs: []string{"9783551551672"}}
	if outcome != evidence.SourceOK || len(recs) != 1 || !equalRecord(recs[0], want) {
		t.Fatalf("%s %+v", outcome, recs)
	}
	for name, body := range map[string]string{
		"error object":        `{"error":{"code":429,"message":"Quota exceeded"}}`,
		"items without total": `{"totalItems":0,"items":[{"id":"x","volumeInfo":{}}]}`,
		"total without items": `{"totalItems":5}`,
		"not json":            `<html>`,
	} {
		if outcome, _ := lookup(t, sources.GoogleBooks("k"), gbFetcher(answer{status: 200, body: []byte(body)}), "9783551551672"); outcome != evidence.SourceUnavailable {
			t.Errorf("%s: %s", name, outcome)
		}
	}
	if outcome, _ := lookup(t, sources.GoogleBooks("k"), gbFetcher(answer{status: 200, body: []byte(`{"kind":"books#volumes","totalItems":0}`)}), "9783551551672"); outcome != evidence.SourceNoMatch {
		t.Errorf("empty: %s", outcome)
	}
}

func TestGoogleBooksKeyHeader(t *testing.T) {
	f := gbFetcher(file("gb-volume.json"))
	lookup(t, sources.GoogleBooks("secret-key"), f, "9783551551672")
	if len(f.urls) != 1 || len(f.headers) != 1 {
		t.Fatalf("requests: %v", f.urls)
	}
	u, _ := url.Parse(f.urls[0])
	if u.Path != "/books/v1/volumes" || u.RawQuery != "q=isbn:9783551551672&maxResults=10" || strings.Contains(f.urls[0], "secret") {
		t.Fatalf("url = %s", f.urls[0])
	}
	if got := f.headers[0].Values("X-Goog-Api-Key"); !slices.Equal(got, []string{"secret-key"}) || len(f.headers[0]) != 1 {
		t.Fatalf("headers = %v", f.headers[0])
	}
	empty := gbFetcher(file("gb-volume.json"))
	if outcome, _ := lookup(t, sources.GoogleBooks(""), empty, "9783551551672"); outcome != evidence.SourceNotConfigured || len(empty.urls) != 0 {
		t.Fatalf("no key: %s, %d requests", outcome, len(empty.urls))
	}
}

// TestGoogleBooksProbe: since late September 2026 Google answers every
// isbn: query with 200 and no items. The probe asks for a volume Google
// certainly holds and passes only when it comes back.
func TestGoogleBooksProbe(t *testing.T) {
	const known = `{"kind":"books#volumes","totalItems":1,"items":[{"id":"kotPYEqx7kMC","volumeInfo":{"title":"1984","authors":["George Orwell"],"industryIdentifiers":[{"type":"ISBN_13","identifier":"9780451524935"}]}}]}`
	gb, ok := sources.GoogleBooks("k").(sources.Prober)
	if !ok {
		t.Fatal("Google Books has no probe")
	}
	for name, tt := range map[string]struct {
		a    answer
		pass bool
	}{
		"answers":        {answer{status: 200, body: []byte(known)}, true},
		"empty":          {answer{status: 200, body: []byte(`{"kind":"books#volumes","totalItems":0}`)}, false},
		"another volume": {answer{status: 200, body: []byte(strings.ReplaceAll(known, "9780451524935", "9783551551672"))}, false},
		"unavailable":    {answer{status: 503}, false},
		"quota exceeded": {answer{status: 429, body: []byte(`{"error":{"code":429}}`)}, false},
	} {
		f := gbFetcher(tt.a)
		if got := gb.Probe(t.Context(), f); got != tt.pass {
			t.Errorf("%s: probe = %v, want %v", name, got, tt.pass)
		}
		if len(f.urls) != 1 || !strings.Contains(f.urls[0], "q=isbn:9780451524935") {
			t.Errorf("%s: requests %v", name, f.urls)
		}
	}
	none := gbFetcher(answer{status: 200, body: []byte(known)})
	if p := sources.GoogleBooks("").(sources.Prober); !p.Probe(t.Context(), none) || len(none.urls) != 0 {
		t.Fatalf("no key: the probe must pass without a request (lookups say not_configured)")
	}
}
