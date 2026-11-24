package sources

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/findings"
	"github.com/felixapel/calibre-bookwarden/internal/findings/isbnranges"
	"github.com/felixapel/calibre-bookwarden/internal/seriesindex"
)

// maxSPARQLBody caps a Wikidata answer. The query asks for at most
// sparqlLimit rows.
const (
	maxSPARQLBody = 2 << 20
	sparqlLimit   = 600
)

// qid matches a Wikidata item ID, which also is the bare label the label
// service falls back to when an entity has none in the asked languages.
var (
	qid     = regexp.MustCompile(`^Q[0-9]{1,12}$`)
	qidName = regexp.MustCompile(`^Q[0-9]+$`)
)

type wikidata struct{ base string }

// Wikidata looks an ISBN up by its exact, hyphenated string in P212 (ISBN-13)
// and, for a 978 ISBN, P957 (ISBN-10), in one SPARQL query (ADR-035). Wikidata
// stores ISBNs hyphenated and matches them as strings, so the ISBN is
// hyphenated from the International ISBN Agency's range table; an ISBN in an
// unassigned range is not queried.
func Wikidata() Adapter { return &wikidata{base: "https://query.wikidata.org"} }

func (w *wikidata) Name() string { return evidence.SourceWikidata }

// sparql builds the query. isbn, h13 and h10 are digits, hyphens and a final
// X (an ISBN-10 check digit), so they can't break out of their string
// literals. isbn is also matched unhyphenated, for items stored that way.
func sparql(isbn, h13, h10 string) string {
	match := `{ ?item wdt:P212 "` + h13 + `" } UNION { ?item wdt:P212 "` + isbn + `" }`
	if h10 != "" {
		match += ` UNION { ?item wdt:P957 "` + h10 + `" }`
	}
	return `SELECT DISTINCT ?item ?field ?value ?ent ?entLabel ?order ?prec WHERE {
  ` + match + `
  {
    { ?item wdt:P212 ?value . BIND("isbn13" AS ?field) }
    UNION { ?item wdt:P957 ?value . BIND("isbn10" AS ?field) }
    UNION { ?item wdt:P1476 ?value . BIND("title" AS ?field) }
    UNION { ?item wdt:P1680 ?value . BIND("subtitle" AS ?field) }
    UNION { ?item p:P50 ?st . ?st ps:P50 ?ent . OPTIONAL { ?st pq:P1545 ?order } BIND("author" AS ?field) }
    UNION { ?item p:P2093 ?st . ?st ps:P2093 ?value . OPTIONAL { ?st pq:P1545 ?order } BIND("authorstring" AS ?field) }
    UNION { ?item wdt:P407 ?lang . ?lang (wdt:P219|wdt:P218) ?value . BIND("language" AS ?field) }
    UNION { ?item wdt:P123 ?ent . BIND("publisher" AS ?field) }
    UNION { ?item p:P179 ?st . ?st ps:P179 ?ent . ?st wikibase:rank ?rank . FILTER(?rank != wikibase:DeprecatedRank) OPTIONAL { ?st pq:P1545 ?order } BIND("series" AS ?field) }
    UNION { ?item p:P577/psv:P577 ?tv . ?tv wikibase:timeValue ?value ; wikibase:timePrecision ?prec . BIND("date" AS ?field) }
  }
  SERVICE wikibase:label { bd:serviceParam wikibase:language "mul,en,de,es,fr,it,nl". }
}
LIMIT ` + strconv.Itoa(sparqlLimit)
}

type sparqlTerm struct {
	Value string `json:"value"`
}

type sparqlResponse struct {
	Results *struct {
		Bindings *[]map[string]sparqlTerm `json:"bindings"`
	} `json:"results"`
}

// person is an author with its P1545 series ordinal, when the item has one.
type person struct {
	name  string
	order float64
	has   bool
}

// membership is a series the item is part of (P179) with the ordinal text of
// its P1545 qualifier, as Wikidata states it.
type membership struct{ name, ordinal string }

type wdItem struct {
	titles, subtitles, publishers, dates []string
	authors, strAuthors                  []person
	langs, isbns                         []string
	series                               []membership
}

func (w *wikidata) Lookup(ctx context.Context, f Fetcher, isbn string) (string, []evidence.SourceRecord) {
	h13, ok := isbnranges.Hyphenate(isbn)
	if !ok {
		return evidence.SourceNotQueried, nil
	}
	h10, _ := isbnranges.Hyphenate10(isbn)
	form := url.Values{"query": {sparql(isbn, h13, h10)}}
	header := http.Header{
		"Accept":       {"application/sparql-results+json"},
		"Content-Type": {"application/x-www-form-urlencoded"},
	}
	status, body, err := f.Post(ctx, w.base+"/sparql", header, []byte(form.Encode()), maxSPARQLBody)
	if err != nil || status != http.StatusOK {
		return evidence.SourceUnavailable, nil
	}
	// A cut-off or error answer is unavailable, never an empty no_match.
	var resp sparqlResponse
	if json.Unmarshal(body, &resp) != nil || resp.Results == nil || resp.Results.Bindings == nil {
		return evidence.SourceUnavailable, nil
	}
	// A full page may be cut off at the limit, so it isn't an answer.
	if len(*resp.Results.Bindings) >= sparqlLimit {
		return evidence.SourceUnavailable, nil
	}
	items := map[string]*wdItem{}
	for _, row := range *resp.Results.Bindings {
		uri := row["item"].Value
		q := uri[strings.LastIndex(uri, "/")+1:]
		if !qid.MatchString(q) {
			continue
		}
		it := items[q]
		if it == nil {
			it = &wdItem{}
			items[q] = it
		}
		it.add(row)
	}
	qids := make([]string, 0, len(items))
	for q := range items {
		qids = append(qids, q)
	}
	slices.SortFunc(qids, func(a, b string) int {
		x, _ := strconv.Atoi(a[1:])
		y, _ := strconv.Atoi(b[1:])
		return x - y
	})
	recs := make([]evidence.SourceRecord, 0, len(qids))
	for _, q := range qids {
		recs = append(recs, items[q].record(q, isbn))
	}
	return accept(isbn, recs)
}

func (it *wdItem) add(row map[string]sparqlTerm) {
	value := text(row["value"].Value)
	label := text(row["entLabel"].Value)
	order, err := strconv.ParseFloat(row["order"].Value, 64)
	p := person{order: order, has: err == nil}
	switch row["field"].Value {
	case "isbn13", "isbn10":
		// Uncapped here (the query's row limit bounds it): record caps the
		// list after putting the queried ISBN first.
		if c, ok := findings.CanonicalISBN(value); ok && !slices.Contains(it.isbns, c) {
			it.isbns = append(it.isbns, c)
		}
	case "title":
		it.titles = append(it.titles, value)
	case "subtitle":
		it.subtitles = append(it.subtitles, value)
	case "author":
		if label != "" && !qidName.MatchString(label) {
			p.name = label
			it.authors = append(it.authors, p)
		}
	case "authorstring":
		p.name = value
		it.strAuthors = append(it.strAuthors, p)
	case "language":
		if l := lang(value); l != "" && !slices.Contains(it.langs, l) {
			it.langs = append(it.langs, l)
		}
	case "publisher":
		if label != "" && !qidName.MatchString(label) {
			it.publishers = append(it.publishers, label)
		}
	case "series":
		if label != "" && !qidName.MatchString(label) {
			it.series = append(it.series, membership{name: label, ordinal: row["order"].Value})
		}
	case "date":
		if d := wikidataDate(value, row["prec"].Value); d != "" {
			it.dates = append(it.dates, d)
		}
	}
}

// wikidataDate keeps only the precision the item states: "2008" for a year,
// "2008-04" for a month, "2008-04-01" for a day or finer. Anything coarser
// than a year, or not a plain year, is dropped.
func wikidataDate(t, prec string) string {
	n, err := strconv.Atoi(prec)
	if err != nil || n < 9 || len(t) < 10 || t[4] != '-' || t[7] != '-' {
		return ""
	}
	for _, i := range []int{0, 1, 2, 3, 5, 6, 8, 9} {
		if t[i] < '0' || t[i] > '9' {
			return ""
		}
	}
	switch n {
	case 9:
		return t[:4]
	case 10:
		return t[:7]
	}
	return t[:10]
}

// first returns the smallest cleaned value: a record has one title, and the
// answer's row order isn't fixed, so the choice must not depend on it.
func first(values []string) string {
	var best string
	for _, v := range values {
		if v = text(v); v != "" && (best == "" || v < best) {
			best = v
		}
	}
	return best
}

// names orders people by P1545 when present, then by name, so the order never
// depends on the answer's row order.
func names(list []person) []string {
	slices.SortStableFunc(list, func(a, b person) int {
		switch {
		case a.has && b.has && a.order != b.order:
			if a.order < b.order {
				return -1
			}
			return 1
		case a.has != b.has:
			if a.has {
				return -1
			}
			return 1
		}
		return strings.Compare(a.name, b.name)
	})
	var out []string
	for _, p := range list {
		out = appendText(out, p.name)
	}
	return out
}

func (it *wdItem) record(q, isbn string) evidence.SourceRecord {
	authors := names(it.authors)
	if len(authors) == 0 {
		authors = names(it.strAuthors)
	}
	slices.Sort(it.dates)
	slices.Sort(it.langs)
	// The queried ISBN first, so the cap can't drop it; the rest sorted.
	var isbns []string
	if slices.Contains(it.isbns, isbn) {
		isbns = append(isbns, isbn)
	}
	for _, c := range slices.Sorted(slices.Values(it.isbns)) {
		if c != isbn && len(isbns) < maxItems {
			isbns = append(isbns, c)
		}
	}
	rec := evidence.SourceRecord{ID: id(q), Title: first(it.titles), Subtitle: first(it.subtitles),
		Authors: authors, Languages: cap16(it.langs), Publisher: first(it.publishers), ISBNs: isbns}
	if len(it.dates) > 0 {
		rec.Date = it.dates[0]
	}
	rec.Series, rec.SeriesIndex = it.seriesOf()
	return rec
}

// seriesOf picks the item's series: the smallest name, since the answer's row
// order isn't fixed and an item can be part of several. Its index is the
// ordinal when the item states exactly one valid ordinal for that series; a
// missing, non-decimal or conflicting ordinal leaves the index empty.
func (it *wdItem) seriesOf() (name, index string) {
	for _, m := range it.series {
		if n := text(m.name); n != "" && (name == "" || n < name) {
			name = n
		}
	}
	if name == "" {
		return "", ""
	}
	for _, m := range it.series {
		if text(m.name) != name {
			continue
		}
		o, ok := seriesindex.Normalize(m.ordinal)
		if !ok || o == "" || index != "" && o != index {
			return name, ""
		}
		index = o
	}
	return name, index
}

func cap16(list []string) []string {
	if len(list) > maxItems {
		return list[:maxItems]
	}
	return list
}
