//go:build live

package sources_test

import (
	"os"
	"slices"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/sources"
)

// TestLiveSources queries the real hosts through the production client. CI
// never runs it: go test -tags live -run TestLiveSources ./internal/sources/
func TestLiveSources(t *testing.T) {
	client := sources.NewClient(sources.ClientOptions{UserAgent: sources.UserAgent("live-probe"), AllowHosts: sources.Hosts})
	cases := []struct {
		adapter          sources.Adapter
		isbn, title, lng string
	}{
		{sources.DNB(), "9783551551672", "Harry Potter und der Stein der Weisen", "deu"},
		{sources.OpenLibrary(), "9783551551672", "Harry Potter und der Stein der Weisen", "deu"},
		{sources.BNE(), "9788478884452", "Harry Potter y la piedra filosofal", "spa"},
	}
	if key := os.Getenv("BOOKWARDEN_GOOGLE_BOOKS_KEY"); key != "" {
		cases = append(cases, struct {
			adapter          sources.Adapter
			isbn, title, lng string
		}{sources.GoogleBooks(key), "9783551551672", "Harry Potter und der Stein der Weisen", "deu"})
	} else {
		t.Log("googlebooks: skipped, BOOKWARDEN_GOOGLE_BOOKS_KEY is not set")
	}
	for _, c := range cases {
		t.Run(c.adapter.Name(), func(t *testing.T) {
			outcome, recs := c.adapter.Lookup(t.Context(), client, c.isbn)
			if outcome != evidence.SourceOK {
				t.Fatalf("outcome %s", outcome)
			}
			found := false
			for _, r := range recs {
				if !slices.Contains(r.ISBNs, c.isbn) || r.ID == "" {
					t.Fatalf("record without the ISBN or an ID: %+v", r)
				}
				if r.Title == c.title && slices.Contains(r.Authors, "J. K. Rowling") && slices.Contains(r.Languages, c.lng) && r.Date != "" && r.Publisher != "" {
					found = true
				}
				t.Logf("%+v", r)
			}
			if !found {
				t.Fatalf("no record has title %q, author J. K. Rowling, language %s, a date and a publisher", c.title, c.lng)
			}
		})
	}
}

// liveExact looks isbn up through the production client and requires a
// record with the title, the author and the language.
func liveExact(t *testing.T, a sources.Adapter, isbn, title, author, lng string) {
	t.Helper()
	client := sources.NewClient(sources.ClientOptions{UserAgent: sources.UserAgent("live-probe"), AllowHosts: sources.Hosts, HostPace: sources.HostPace})
	outcome, recs := a.Lookup(t.Context(), client, isbn)
	if outcome != evidence.SourceOK {
		t.Fatalf("outcome %s", outcome)
	}
	for _, r := range recs {
		t.Logf("%+v", r)
		if !slices.Contains(r.ISBNs, isbn) || r.ID == "" {
			t.Fatalf("record without the ISBN or an ID: %+v", r)
		}
		if r.Title == title && slices.Contains(r.Authors, author) && slices.Contains(r.Languages, lng) {
			return
		}
	}
	t.Fatalf("no record has title %q, author %q and language %s", title, author, lng)
}

// TestLiveWikidata asks the real SPARQL endpoint for a public item (ADR-035).
func TestLiveWikidata(t *testing.T) {
	liveExact(t, sources.Wikidata(), "9780765326355", "The Way of Kings", "Brandon Sanderson", "eng")
}

// TestLiveHardcover runs only with BOOKWARDEN_HARDCOVER_TOKEN set.
func TestLiveHardcover(t *testing.T) {
	token := os.Getenv("BOOKWARDEN_HARDCOVER_TOKEN")
	if token == "" {
		t.Skip("hardcover: BOOKWARDEN_HARDCOVER_TOKEN is not set")
	}
	liveExact(t, sources.Hardcover(token, nil), "9780765326355", "The Way of Kings", "Brandon Sanderson", "eng")
}
