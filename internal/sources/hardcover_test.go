package sources_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/sources"
)

const hcToken = "hc-secret-token"

func hcFetcher(a answer) *fakeFetcher {
	return &fakeFetcher{routes: map[string]answer{"/v1/graphql": a}}
}

func hcAdapter(token string, buf *bytes.Buffer) sources.Adapter {
	if buf == nil {
		buf = &bytes.Buffer{}
	}
	return sources.Hardcover(token, slog.New(slog.NewTextHandler(buf, nil)))
}

func TestHardcoverAcceptance(t *testing.T) {
	f := hcFetcher(file("hc-editions.json"))
	outcome, recs := lookup(t, hcAdapter(hcToken, nil), f, "9780765326355")
	want := evidence.SourceRecord{ID: "20000001", Title: "The Synthetic Chronicle", Subtitle: "A Test",
		Authors: []string{"Ay Author", "Bee Author"}, Languages: []string{"eng"}, Publisher: "Example Press", Date: "2008-04-01",
		ISBNs: []string{"9780765326355"}}
	if outcome != evidence.SourceOK || len(recs) != 1 || !equalRecord(recs[0], want) {
		t.Fatalf("%s %+v", outcome, recs)
	}
}

// TestHardcoverRequest pins the query's field names (testdata/hc-query.graphql)
// and checks that the token travels only in the Authorization header.
func TestHardcoverRequest(t *testing.T) {
	pinned, err := os.ReadFile("testdata/hc-query.graphql")
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{hcToken, "Bearer " + hcToken} {
		f := hcFetcher(file("hc-editions.json"))
		lookup(t, hcAdapter(token, nil), f, "9780765326355")
		if len(f.urls) != 1 || f.urls[0] != "https://test.invalid/v1/graphql" || f.methods[0] != "POST" {
			t.Fatalf("requests %v %v", f.methods, f.urls)
		}
		h := f.headers[0]
		if h.Get("Authorization") != "Bearer "+hcToken || h.Get("Content-Type") != "application/json" || h.Get("Accept") != "application/json" {
			t.Fatalf("headers %v", h)
		}
		if bytes.Contains(f.bodies[0], []byte(hcToken)) || strings.Contains(f.urls[0], hcToken) {
			t.Fatalf("the token leaked into the request: %s", f.bodies[0])
		}
		var body map[string]any
		if err := json.Unmarshal(f.bodies[0], &body); err != nil || len(body) != 1 {
			t.Fatalf("body %s (%v)", f.bodies[0], err)
		}
		if got, want := body["query"], strings.ReplaceAll(strings.TrimSpace(string(pinned)), "{ISBN}", "9780765326355"); got != want {
			t.Fatalf("query:\n%v\nwant:\n%v", got, want)
		}
	}
}

func TestHardcoverNotConfigured(t *testing.T) {
	f := hcFetcher(file("hc-editions.json"))
	if outcome, recs := lookup(t, hcAdapter("", nil), f, "9780765326355"); outcome != evidence.SourceNotConfigured || recs != nil || len(f.urls) != 0 {
		t.Fatalf("%s %v, %d requests", outcome, recs, len(f.urls))
	}
}

func TestHardcoverOutcomes(t *testing.T) {
	tests := []struct {
		name string
		a    answer
		want string
	}{
		{"no editions", answer{status: 200, body: []byte(`{"data":{"editions":[]}}`)}, evidence.SourceNoMatch},
		{"only another book", answer{status: 200, body: []byte(`{"data":{"editions":[{"book_id":1,"title":"x","isbn_13":"9780306406157"}]}}`)}, evidence.SourceNoMatch},
		{"graphql errors", answer{status: 200, body: []byte(`{"errors":[{"message":"field not found"}]}`)}, evidence.SourceUnavailable},
		{"errors beside data", answer{status: 200, body: []byte(`{"errors":[{"message":"x"}],"data":{"editions":[]}}`)}, evidence.SourceUnavailable},
		{"null data", answer{status: 200, body: []byte(`{"data":null}`)}, evidence.SourceUnavailable},
		{"no editions key", answer{status: 200, body: []byte(`{"data":{}}`)}, evidence.SourceUnavailable},
		{"null editions", answer{status: 200, body: []byte(`{"data":{"editions":null}}`)}, evidence.SourceUnavailable},
		{"truncated", answer{status: 200, body: []byte(`{"data":{"editions":[{"book_id":1`)}, evidence.SourceUnavailable},
		{"empty body", answer{status: 200, body: []byte{}}, evidence.SourceUnavailable},
		{"html", answer{status: 200, body: []byte("<html>")}, evidence.SourceUnavailable},
		{"429", answer{status: 429}, evidence.SourceUnavailable},
		{"500", answer{status: 500}, evidence.SourceUnavailable},
		{"fetch error", answer{err: sources.ErrUnavailable}, evidence.SourceUnavailable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			outcome, recs := lookup(t, hcAdapter(hcToken, nil), hcFetcher(tc.a), "9780765326355")
			if outcome != tc.want || recs != nil {
				t.Fatalf("%s %+v, want %s", outcome, recs, tc.want)
			}
		})
	}
}

// A refused token is unavailable, and logged once without the token.
func TestHardcoverTokenRefused(t *testing.T) {
	var buf bytes.Buffer
	a := hcAdapter(hcToken, &buf)
	refused := fmt.Errorf("%w: status 401: %w", sources.ErrUnavailable, sources.ErrUnauthorized)
	for range 3 {
		if outcome, recs := lookup(t, a, hcFetcher(answer{err: refused}), "9780765326355"); outcome != evidence.SourceUnavailable || recs != nil {
			t.Fatalf("%s %v", outcome, recs)
		}
	}
	// A different failure is not logged as a refused token.
	lookup(t, a, hcFetcher(answer{err: errors.New("boom")}), "9780765326355")
	out := buf.String()
	if strings.Count(out, "token") != 1 || strings.Contains(out, hcToken) || strings.Count(out, "\n") != 1 {
		t.Fatalf("log = %q", out)
	}
}

func TestHardcoverRejectsNonDigitISBN(t *testing.T) {
	f := hcFetcher(file("hc-editions.json"))
	if outcome, _ := lookup(t, hcAdapter(hcToken, nil), f, `1") { id } #`); outcome != evidence.SourceUnavailable || len(f.urls) != 0 {
		t.Fatalf("%s, %d requests", outcome, len(f.urls))
	}
}
