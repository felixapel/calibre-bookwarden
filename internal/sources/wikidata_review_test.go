package sources_test

import (
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/sources"
)

// isbn13With returns a checksum-valid 978 ISBN-13 for the n-th test number.
func isbn13With(n int) string {
	body := "978" + strings.Repeat("0", 9-len(strconv.Itoa(n))) + strconv.Itoa(n)
	sum := 0
	for i, c := range body {
		d := int(c - '0')
		if i%2 == 1 {
			d *= 3
		}
		sum += d
	}
	return body + strconv.Itoa((10-sum%10)%10)
}

func wdRow(q, field, value string) string {
	return `{"item":{"value":"http://www.wikidata.org/entity/` + q + `"},"field":{"value":"` + field + `"},"value":{"value":"` + value + `"}}`
}

func wdBody(rows []string) answer {
	return answer{status: 200, body: []byte(`{"head":{"vars":[]},"results":{"bindings":[` + strings.Join(rows, ",") + `]}}`)}
}

// An item with many ISBNs must keep the queried one, whatever the row order.
func TestWikidataKeepsQueriedISBNAmongMany(t *testing.T) {
	const queried = "9780765326355"
	var rows []string
	for i := range 20 {
		rows = append(rows, wdRow("Q1", "isbn13", isbn13With(i+1)))
	}
	rows = append(rows, wdRow("Q1", "isbn13", "978-0-7653-2635-5"), wdRow("Q1", "title", "T"))
	outcome, recs := lookup(t, sources.Wikidata(), wdFetcher(wdBody(rows)), queried)
	if outcome != evidence.SourceOK || len(recs) != 1 {
		t.Fatalf("%s %+v", outcome, recs)
	}
	if len(recs[0].ISBNs) != 16 || recs[0].ISBNs[0] != queried || !slices.IsSorted(recs[0].ISBNs[1:]) {
		t.Fatalf("ISBNs = %v", recs[0].ISBNs)
	}
}

// A full page of rows may be cut off, so it is not an answer (ADR-019).
func TestWikidataTruncatedAtLimit(t *testing.T) {
	var rows []string
	for range 599 {
		rows = append(rows, wdRow("Q1", "title", "T"))
	}
	rows[0] = wdRow("Q1", "isbn13", "978-0-7653-2635-5")
	if outcome, _ := lookup(t, sources.Wikidata(), wdFetcher(wdBody(rows)), "9780765326355"); outcome != evidence.SourceOK {
		t.Fatalf("599 rows: %s", outcome)
	}
	rows = append(rows, wdRow("Q1", "title", "T2"))
	if outcome, recs := lookup(t, sources.Wikidata(), wdFetcher(wdBody(rows)), "9780765326355"); outcome != evidence.SourceUnavailable || recs != nil {
		t.Fatalf("600 rows: %s %v", outcome, recs)
	}
}

// The same rows in another order give the same records.
func TestWikidataOrderIndependent(t *testing.T) {
	rows := []string{
		wdRow("Q2", "isbn13", "978-0-7653-2635-5"), wdRow("Q2", "language", "deu"), wdRow("Q2", "language", "eng"),
		wdRow("Q2", "isbn10", "0-7653-2635-3"), wdRow("Q2", "isbn13", "978-0-306-40615-7"), wdRow("Q2", "language", "fra"),
		wdRow("Q1", "isbn13", "978-0-7653-2635-5"),
	}
	_, a := lookup(t, sources.Wikidata(), wdFetcher(wdBody(rows)), "9780765326355")
	rev := slices.Clone(rows)
	slices.Reverse(rev)
	_, b := lookup(t, sources.Wikidata(), wdFetcher(wdBody(rev)), "9780765326355")
	if len(a) != 2 || len(a) != len(b) {
		t.Fatalf("%+v / %+v", a, b)
	}
	for i := range a {
		if !equalRecord(a[i], b[i]) {
			t.Fatalf("record %d differs:\n%+v\n%+v", i, a[i], b[i])
		}
	}
	if !slices.Equal(a[1].Languages, []string{"deu", "eng", "fra"}) {
		t.Fatalf("languages = %v", a[1].Languages)
	}
}

// Items stored without hyphens still match.
func TestWikidataQueryHasUnhyphenatedBranch(t *testing.T) {
	f := wdFetcher(file("wd-none.json"))
	lookup(t, sources.Wikidata(), f, "9780765326355")
	form, _ := url.ParseQuery(string(f.bodies[0]))
	if q := form.Get("query"); !strings.Contains(q, `{ ?item wdt:P212 "9780765326355" }`) {
		t.Fatalf("query lacks the unhyphenated branch:\n%s", q)
	}
}
