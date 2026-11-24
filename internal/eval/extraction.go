package eval

import (
	"context"
	"fmt"
	"io"
	"maps"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/findings"
	"github.com/felixapel/calibre-bookwarden/internal/report"
)

// ExtractionReport measures a run's shadow extraction (M4 §9): how each
// extractor's calls fared and, per key, how often the voices agreed and how
// an agreed value compares with Calibre's and with the exact-ISBN records.
// It holds counts only, never library text.
type ExtractionReport struct {
	Run      int64 `json:"run"`
	Formats  int   `json:"formats"`
	Answered int   `json:"answered"` // formats with an extractor ok

	Broken      int                        `json:"broken"`
	BrokenBooks []int                      `json:"broken_books"`
	Extractors  map[string]*ExtractorCount `json:"extractors"`
	Keys        map[string]*KeyCount       `json:"keys"`
}

// ExtractorCount is one extractor's calls by state, and how many the cache
// answered.
type ExtractorCount struct {
	States map[string]int `json:"states"`
	Cached int            `json:"cached"`
}

// KeyCount is one key's outcomes over the formats with an extraction. Only
// agreed values are compared.
type KeyCount struct {
	Agreed   int        `json:"agreed"`
	Single   int        `json:"single"`
	Disagree int        `json:"disagree"`
	Calibre  Comparison `json:"calibre"`
	Records  Comparison `json:"records"`
}

// Comparison counts agreed values against a reference: the same after
// folding case, diacritics and punctuation (ISBNs as ISBN-13), different, or
// no reference value. A key the reference has no field for counts nowhere.
type Comparison struct {
	Match  int `json:"match"`
	Differ int `json:"differ"`
	None   int `json:"none"`
}

func newExtractionReport(run int64) *ExtractionReport {
	return &ExtractionReport{Run: run, BrokenBooks: []int{}, Extractors: map[string]*ExtractorCount{}, Keys: map[string]*KeyCount{}}
}

// Extractions walks the run's books in a read-only snapshot, verifies each
// seal, and measures every format's extraction. A broken seal is counted and
// listed; the walk goes on.
func Extractions(ctx context.Context, st report.Store, runID int64) (ExtractionReport, error) {
	snap, err := st.Snapshot(ctx, runID)
	if err != nil {
		return ExtractionReport{}, err
	}
	defer func() { _ = snap.Close(ctx) }()
	r := newExtractionReport(runID)
	broken, err := eachPackage(ctx, snap, func(b report.Book) error {
		r.add(*b.Package)
		return nil
	})
	if err != nil {
		return ExtractionReport{}, err
	}
	r.Broken, r.BrokenBooks = len(broken), append(r.BrokenBooks, broken...)
	return *r, nil
}

func (r *ExtractionReport) add(p evidence.Package) {
	for _, f := range p.Formats {
		x := f.Extraction
		if x == nil {
			continue
		}
		r.Formats++
		if slices.ContainsFunc(x.Extractors, func(e evidence.ExtractorRun) bool { return e.State == evidence.ExtractOK }) {
			r.Answered++
		}
		for _, e := range x.Extractors {
			c := r.Extractors[e.Name]
			if c == nil {
				c = &ExtractorCount{States: map[string]int{}}
				r.Extractors[e.Name] = c
			}
			c.States[e.State]++
			if e.Cached {
				c.Cached++
			}
		}
		for _, a := range x.Agreed {
			k := r.Keys[a.Key]
			if k == nil {
				k = &KeyCount{}
				r.Keys[a.Key] = k
			}
			switch a.Outcome {
			case evidence.AgreeAgreed:
				k.Agreed++
				compare(k, a, x, p)
			case evidence.AgreeSingle:
				k.Single++
			case evidence.AgreeDisagree:
				k.Disagree++
			}
		}
	}
}

// compare counts an agreed fact against Calibre's record and the exact-ISBN
// records: a match when any record has the same value.
func compare(k *KeyCount, a evidence.AgreedFact, x *evidence.Extraction, p evidence.Package) {
	norm := keyNorm(a.Key)
	got := factValues(a, norm)
	if len(got) == 0 {
		return
	}
	if field := calibreField(a.Key); field != nil {
		ref := normSet(field(p.Calibre), norm)
		switch {
		case len(ref) == 0:
			k.Calibre.None++
		case a.Key == "isbn" && slices.Contains(got, ref[0]),
			a.Key == "title" && withSubtitles(got, x, norm, ref):
			// Calibre keeps one ISBN of those printed, and the title as
			// "Main: Subtitle" (ADR-031).
			k.Calibre.Match++
		case slices.Equal(got, ref):
			k.Calibre.Match++
		default:
			k.Calibre.Differ++
		}
	}
	field := recordField(a.Key)
	if field == nil {
		return
	}
	found := false
	for _, l := range p.Lookups {
		for _, res := range l.Results {
			for _, rec := range res.Records {
				ref := normSet(field(rec), norm)
				if len(ref) == 0 {
					continue
				}
				if slices.Equal(got, ref) {
					k.Records.Match++
					return
				}
				found = true
			}
		}
	}
	if found {
		k.Records.Differ++
	} else {
		k.Records.None++
	}
}

// withSubtitles reports whether the title with one of the extraction's agreed
// subtitles is ref.
func withSubtitles(title []string, x *evidence.Extraction, norm func(string) string, ref []string) bool {
	for _, a := range x.Agreed {
		if a.Key != "subtitle" || a.Outcome != evidence.AgreeAgreed {
			continue
		}
		for _, v := range a.Values {
			if slices.Equal(normSet([]string{title[0] + " " + v.Value}, norm), ref) {
				return true
			}
		}
	}
	return false
}

// factValues is a fact's comparable values: the authors of a contributor
// fact, the values otherwise.
func factValues(a evidence.AgreedFact, norm func(string) string) []string {
	var vs []string
	for _, v := range a.Values {
		if a.Key == "contributor" && v.Role != "author" {
			continue
		}
		vs = append(vs, v.Value)
	}
	return normSet(vs, norm)
}

// calibreField is Calibre's field for a key, nil when it has none. Its
// pubdate is this edition's (ADR-031), so other years compare nowhere.
func calibreField(key string) func(evidence.Record) []string {
	switch key {
	case "title":
		return func(c evidence.Record) []string { return []string{c.Title} }
	case "contributor":
		return func(c evidence.Record) []string { return c.Authors }
	case "imprint":
		return func(c evidence.Record) []string { return []string{c.Publisher} }
	case "isbn":
		return func(c evidence.Record) []string { return []string{c.Identifiers["isbn"]} }
	case "year/this_edition":
		return func(c evidence.Record) []string { return []string{year(c.PubDate)} }
	}
	return nil
}

// recordField is a source record's field for a key, nil when it has none.
// ISBNs are left out: the lookups are seeded with the book's own, so they
// would match by construction.
func recordField(key string) func(evidence.SourceRecord) []string {
	switch key {
	case "title":
		return func(r evidence.SourceRecord) []string { return []string{r.Title} }
	case "subtitle":
		return func(r evidence.SourceRecord) []string { return []string{r.Subtitle} }
	case "contributor":
		return func(r evidence.SourceRecord) []string { return r.Authors }
	case "imprint":
		return func(r evidence.SourceRecord) []string { return []string{r.Publisher} }
	case "year/this_edition":
		return func(r evidence.SourceRecord) []string { return []string{year(r.Date)} }
	}
	return nil
}

var yearPattern = regexp.MustCompile(`\b[12]\d{3}\b`)

// year is the first year a date gives, as in "2019-05-01" or "June 5,
// 2003"; Calibre's empty date (year 101) gives none.
func year(date string) string { return yearPattern.FindString(date) }

func keyNorm(key string) func(string) string {
	if key == "isbn" {
		return func(s string) string { isbn, _ := findings.CanonicalISBN(s); return isbn }
	}
	return func(s string) string {
		return strings.Join(strings.FieldsFunc(report.Fold(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }), " ")
	}
}

// normSet normalises each value, drops empty ones, and sorts.
func normSet(vs []string, norm func(string) string) []string {
	var out []string
	for _, v := range vs {
		if v = norm(v); v != "" {
			out = append(out, v)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// WriteText prints the report.
func (r ExtractionReport) WriteText(w io.Writer) {
	p := func(format string, a ...any) { _, _ = fmt.Fprintf(w, format, a...) }
	p("run %d: shadow extraction of %d formats, %d answered by an extractor\nbooks with a broken seal: %d\n", r.Run, r.Formats, r.Answered, r.Broken)
	if r.Broken > 0 {
		p("broken books: %s\n", strings.Trim(fmt.Sprint(r.BrokenBooks), "[]"))
	}
	states := []string{evidence.ExtractOK, evidence.ExtractUnavailable, evidence.ExtractQuotaExhausted, evidence.ExtractInvalidOutput, evidence.ExtractTimeout, evidence.ExtractModelMismatch}
	p("\nextractors: %s, cached\n", strings.Join(states, ", "))
	for _, name := range slices.Sorted(maps.Keys(r.Extractors)) {
		c := r.Extractors[name]
		p("%-14s", name)
		for _, s := range states {
			p(" %6d", c.States[s])
		}
		p(" %6d\n", c.Cached)
	}
	p("\nkeys: agreed, single, disagree, coverage %% (of answered formats), agreement %%; agreed vs calibre: match, differ, none; vs records: match, differ, none\n")
	for _, key := range slices.Sorted(maps.Keys(r.Keys)) {
		k := r.Keys[key]
		given := k.Agreed + k.Single + k.Disagree
		p("%-24s %6d %6d %6d %6s %6s   %6d %6d %6d   %6d %6d %6d\n", key, k.Agreed, k.Single, k.Disagree,
			percent(given, r.Answered), percent(k.Agreed, given),
			k.Calibre.Match, k.Calibre.Differ, k.Calibre.None, k.Records.Match, k.Records.Differ, k.Records.None)
	}
}

func percent(n, of int) string {
	if of == 0 {
		return "-"
	}
	return fmt.Sprintf("%.1f", 100*float64(n)/float64(of))
}
