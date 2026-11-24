package sources_test

import (
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/sources"
)

func wdFetcher(a answer) *fakeFetcher {
	return &fakeFetcher{routes: map[string]answer{"/sparql": a}}
}

func TestWikidataAcceptance(t *testing.T) {
	f := wdFetcher(file("wd-9780765326355.json"))
	outcome, recs := lookup(t, sources.Wikidata(), f, "9780765326355")
	want := []evidence.SourceRecord{
		{ID: "Q100000001", Title: "The Synthetic Chronicle", Subtitle: "A Test", Authors: []string{"Ay Author", "Bee Author"},
			Languages: []string{"eng"}, Publisher: "Example Press", Date: "2008-04-01", ISBNs: []string{"9780765326355"}},
		// The author strings stand in only without P50 authors; a publisher
		// the label service couldn't name (a bare QID) is dropped.
		{ID: "Q100000002", Title: "Second Synthetic Item", Authors: []string{"Yan String", "Zed String"},
			Languages: []string{"eng"}, Date: "2009", ISBNs: []string{"9780765326355"}},
	}
	if outcome != evidence.SourceOK || len(recs) != len(want) {
		t.Fatalf("%s %+v", outcome, recs)
	}
	for i := range want {
		if !equalRecord(recs[i], want[i]) {
			t.Errorf("record %d = %+v, want %+v", i, recs[i], want[i])
		}
	}
}

// P179 (part of the series) and its P1545 ordinal are recorded on the item:
// the smallest series name, and its ordinal only when it is one valid decimal.
func TestWikidataSeries(t *testing.T) {
	f := wdFetcher(file("wd-series.json"))
	outcome, recs := lookup(t, sources.Wikidata(), f, "9780765326355")
	want := map[string][2]string{
		"Q100000010": {"The Synthetic Cycle", "2.5"}, // 2.50 is canonical 2.5; the later-named series loses
		"Q100000011": {"Cycle Two", ""},              // two ordinals for one series
		"Q100000012": {"Cycle Three", ""},            // II is not a decimal
		"Q100000013": {"", ""},                       // a bare QID label is no name
		"Q100000014": {"Cycle Five", ""},             // no ordinal
		"Q100000015": {"Cycle Six", "1"},             // 1.0 is canonical 1
	}
	if outcome != evidence.SourceOK || len(recs) != len(want) {
		t.Fatalf("%s %+v", outcome, recs)
	}
	for _, r := range recs {
		if w, ok := want[r.ID]; !ok || r.Series != w[0] || r.SeriesIndex != w[1] {
			t.Errorf("%s: series %q index %q, want %q", r.ID, r.Series, r.SeriesIndex, w)
		}
		if _, err := evidence.Canonical(r); err != nil {
			t.Errorf("%s doesn't seal: %v", r.ID, err)
		}
	}
	// The items of the older fixture state no series.
	_, plain := lookup(t, sources.Wikidata(), wdFetcher(file("wd-9780765326355.json")), "9780765326355")
	for _, r := range plain {
		if r.Series != "" || r.SeriesIndex != "" {
			t.Errorf("%s: series %q index %q, want none", r.ID, r.Series, r.SeriesIndex)
		}
	}
}

func TestWikidataRequest(t *testing.T) {
	tests := []struct {
		name, isbn string
		want       []string
		absent     []string
	}{
		{"978 asks the ISBN-13 and the ISBN-10", "9780765326355", []string{`wdt:P212 "978-0-7653-2635-5"`, `wdt:P957 "0-7653-2635-3"`}, nil},
		{"979 has no ISBN-10", "9791000000008", []string{`wdt:P212 "979-10-00-00000-8"`}, []string{`wdt:P957 "`}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := wdFetcher(file("wd-none.json"))
			if outcome, _ := lookup(t, sources.Wikidata(), f, tc.isbn); outcome != evidence.SourceNoMatch {
				t.Fatalf("outcome %s", outcome)
			}
			if len(f.urls) != 1 || f.urls[0] != "https://test.invalid/sparql" || f.methods[0] != "POST" {
				t.Fatalf("requests %v %v", f.methods, f.urls)
			}
			h := f.headers[0]
			if h.Get("Accept") != "application/sparql-results+json" || h.Get("Content-Type") != "application/x-www-form-urlencoded" {
				t.Fatalf("headers %v", h)
			}
			form, err := url.ParseQuery(string(f.bodies[0]))
			if err != nil || len(form) != 1 || form.Get("query") == "" {
				t.Fatalf("body %q (%v)", f.bodies[0], err)
			}
			q := form.Get("query")
			for _, w := range tc.want {
				if !strings.Contains(q, w) {
					t.Errorf("query lacks %s:\n%s", w, q)
				}
			}
			for _, a := range tc.absent {
				if strings.Contains(q, a) {
					t.Errorf("query has %s:\n%s", a, q)
				}
			}
			for _, p := range []string{"P1476", "P1680", "P50", "P1545", "P2093", "P407", "P219", "P218", "P123", "P577", "P179", "wikibase:DeprecatedRank"} {
				if !strings.Contains(q, p) {
					t.Errorf("query lacks %s", p)
				}
			}
		})
	}
}

// An ISBN in an unassigned range can't be hyphenated, so the exact string
// isn't known: nothing is asked and nothing is claimed.
func TestWikidataUnhyphenatable(t *testing.T) {
	f := wdFetcher(file("wd-none.json"))
	if outcome, recs := lookup(t, sources.Wikidata(), f, "9786700000007"); outcome != evidence.SourceNotQueried || recs != nil || len(f.urls) != 0 {
		t.Fatalf("%s %v, %d requests", outcome, recs, len(f.urls))
	}
}

func TestWikidataOutcomes(t *testing.T) {
	good := string(file("wd-9780765326355.json").body)
	tests := []struct {
		name string
		a    answer
		want string
	}{
		{"empty answer", file("wd-none.json"), evidence.SourceNoMatch},
		{"only another book", answer{status: 200, body: []byte(`{"head":{"vars":["item"]},"results":{"bindings":[{"item":{"type":"uri","value":"http://www.wikidata.org/entity/Q1"},"field":{"type":"literal","value":"isbn13"},"value":{"type":"literal","value":"978-0-306-40615-7"}}]}}`)}, evidence.SourceNoMatch},
		{"fetch error", answer{err: sources.ErrUnavailable}, evidence.SourceUnavailable},
		{"429", answer{status: 429}, evidence.SourceUnavailable},
		{"500", answer{status: 500}, evidence.SourceUnavailable},
		{"404", answer{status: 404}, evidence.SourceUnavailable},
		{"truncated JSON", answer{status: 200, body: []byte(good[:len(good)/2])}, evidence.SourceUnavailable},
		{"empty body", answer{status: 200, body: []byte{}}, evidence.SourceUnavailable},
		{"html", answer{status: 200, body: []byte("<html>timeout</html>")}, evidence.SourceUnavailable},
		{"no results object", answer{status: 200, body: []byte(`{}`)}, evidence.SourceUnavailable},
		{"no bindings array", answer{status: 200, body: []byte(`{"head":{"vars":[]},"results":{}}`)}, evidence.SourceUnavailable},
		{"null bindings", answer{status: 200, body: []byte(`{"head":{"vars":[]},"results":{"bindings":null}}`)}, evidence.SourceUnavailable},
		{"error text", answer{status: 200, body: []byte(`{"error":"QueryTimeoutException"}`)}, evidence.SourceUnavailable},
		{"timeout", answer{err: errors.New("deadline exceeded")}, evidence.SourceUnavailable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			outcome, recs := lookup(t, sources.Wikidata(), wdFetcher(tc.a), "9780765326355")
			if outcome != tc.want || (tc.want != evidence.SourceOK && recs != nil) {
				t.Fatalf("%s %+v, want %s", outcome, recs, tc.want)
			}
		})
	}
}

func TestWikidataCaps(t *testing.T) {
	var rows []string
	row := func(q, field, value string) string {
		return `{"item":{"value":"http://www.wikidata.org/entity/` + q + `"},"field":{"value":"` + field + `"},"value":{"value":"` + value + `"}}`
	}
	for i := range 12 {
		q := "Q" + strings.Repeat("1", i+1)
		title := "T" + q
		if i == 0 {
			title = strings.Repeat("x", 600)
		}
		rows = append(rows, row(q, "isbn13", "978-0-7653-2635-5"), row(q, "title", title))
	}
	body := `{"head":{"vars":[]},"results":{"bindings":[` + strings.Join(rows, ",") + `]}}`
	outcome, recs := lookup(t, sources.Wikidata(), wdFetcher(answer{status: 200, body: []byte(body)}), "9780765326355")
	if outcome != evidence.SourceOK || len(recs) != 8 {
		t.Fatalf("%s, %d records", outcome, len(recs))
	}
	for _, r := range recs {
		if len([]rune(r.Title)) > 512 {
			t.Fatalf("title of %d runes", len([]rune(r.Title)))
		}
	}
}
