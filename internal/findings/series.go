package findings

// Findings version 6 (the series read path, evidence schema 5) compares
// Calibre's series and series index with what a file's OPF says. It only adds
// findings: nothing here proposes a value or authorizes a fix, and the
// decision versions don't read the series. When a name or an index can't be
// compared, no finding is derived, or metadata_unverified says why.

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/seriesindex"
)

// seriesArticles are the English articles ignored when two series names are
// compared: a leading one, or Calibre's trailing ", The". Only English, so
// that "Die Hard" and "Hard" stay different names.
var seriesArticles = []string{"the", "a", "an"}

var trailingArticle = regexp.MustCompile(`(?i),\s*(` + strings.Join(seriesArticles, "|") + `)\s*$`)

// seriesKey is a series name folded for comparison: case folded, punctuation
// and whitespace reduced to single spaces, a leading article dropped ("The
// Spike Saga", "the spike saga" and "Spike Saga, The" are one key). It is ""
// when nothing but punctuation is left.
//
// This is a heuristic for the series_missing and series_name_mismatch
// findings, where a wrong "same" only hides an observation. It is not an
// identity key: decision v11 must not treat two names with the same key as
// the same series.
func seriesKey(name string) string {
	name = tidy(name)
	if m := trailingArticle.FindStringSubmatchIndex(name); m != nil {
		name = name[m[2]:m[3]] + " " + name[:m[0]]
	}
	name = foldText(name)
	words := strings.FieldsFunc(name, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && !unicode.IsMark(r) })
	if len(words) > 1 && isSeriesArticle(words[0]) {
		words = words[1:]
	}
	return strings.Join(words, " ")
}

func isSeriesArticle(w string) bool {
	for _, a := range seriesArticles {
		if w == a {
			return true
		}
	}
	return false
}

// seriesFindings derives series_missing and series_mismatch for every
// readable format.
//   - series_missing: Calibre has no series and the file's OPF names one.
//   - series_name_mismatch (info): both name a series and the keys differ.
//   - series_mismatch (warning), on field series_index: the names agree and both give an index
//     that differs numerically.
//
// A file's series cut at the cap, or an index the file gave that isn't a
// plain decimal, is metadata_unverified instead. A missing index on either
// side is not a finding: Calibre reports 1.0 for a book with no index set, so
// its absence can't be told from a real 1.
func (d *derivation) seriesFindings(p evidence.Package) error {
	cal := p.Calibre
	if utf8.RuneCountInString(cal.Series) > maxFieldRunes {
		// Like an overlong title: nothing is compared, and the book still seals.
		d.add("metadata_unverified", "", "series", "field_too_long")
		return nil
	}
	calKey := seriesKey(cal.Series)
	for _, f := range p.Formats {
		m := f.Metadata
		if m == nil || !metadataEligible(f) {
			continue
		}
		if n, ok := seriesindex.Normalize(m.SeriesIndex); !ok || n != m.SeriesIndex {
			return invalidEvidence()
		}
		limits := m.Limits
		if limits != nil && limits.Series || utf8.RuneCountInString(m.Series) > maxFieldRunes {
			d.add("metadata_unverified", f.Format, "series", "field_too_long")
			continue
		}
		fileKey := seriesKey(m.Series)
		switch {
		case fileKey == "":
			continue
		case cal.Series == "":
			d.add("series_missing", f.Format, "series", "")
			continue
		case calKey == "":
			continue
		case calKey != fileKey:
			d.add("series_name_mismatch", f.Format, "series", "")
			continue
		}
		if limits != nil && limits.SeriesIndex {
			d.add("metadata_unverified", f.Format, "series_index", "invalid_value")
			continue
		}
		if cal.SeriesIndex != "" && m.SeriesIndex != "" && seriesindex.Compare(cal.SeriesIndex, m.SeriesIndex) != 0 {
			d.add("series_mismatch", f.Format, "series_index", "")
		}
	}
	return nil
}
