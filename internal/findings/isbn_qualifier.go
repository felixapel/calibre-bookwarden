package findings

import (
	"slices"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

const (
	qualifierReach = 40 // runes before an ISBN that its label may sit in
	parenReach     = 30 // runes inside the parentheses after an ISBN
)

// ISBNMatch is a checksum-valid ISBN found in text, as canonical ISBN-13, and
// what its label says it identifies: an evidence.Qualifier* value or "".
type ISBNMatch struct {
	ISBN      string
	Qualifier string
}

// qualifierTerms is the closed table of labels, English, German and Spanish,
// lower case. A term matches only as whole words.
var qualifierTerms = []struct{ term, qualifier string }{
	{"ebook", evidence.QualifierEbook}, {"e-book", evidence.QualifierEbook}, {"epub", evidence.QualifierEbook},
	{"e-pub", evidence.QualifierEbook}, {"electronic", evidence.QualifierEbook}, {"digital", evidence.QualifierEbook},
	{"kindle", evidence.QualifierEbook}, {"libro electrónico", evidence.QualifierEbook},
	{"electrónico", evidence.QualifierEbook}, {"elektronisch", evidence.QualifierEbook},
	{"pdf", evidence.QualifierPDF},
	{"hardcover", evidence.QualifierHardcover}, {"hardback", evidence.QualifierHardcover},
	{"cloth", evidence.QualifierHardcover}, {"gebunden", evidence.QualifierHardcover},
	{"tapa dura", evidence.QualifierHardcover},
	{"paperback", evidence.QualifierPaperback}, {"pbk", evidence.QualifierPaperback},
	{"softcover", evidence.QualifierPaperback}, {"taschenbuch", evidence.QualifierPaperback},
	{"broschiert", evidence.QualifierPaperback}, {"tapa blanda", evidence.QualifierPaperback},
	{"rústica", evidence.QualifierPaperback}, {"rustica", evidence.QualifierPaperback},
	{"print", evidence.QualifierPrint},
}

// ScanISBNs returns what ExtractISBNs returns, in the same order, each with
// the qualifier its label gives it. The label is, in this order:
//   - a term of the table in parentheses right after the ISBN, "ISBN 978... (ebook)";
//   - the nearest term in the 40 runes before the ISBN, on its own line and after
//     the previous ISBN's label;
//   - the first term after the ISBN on its line, before the next ISBN's label.
//
// An ISBN seen again keeps the first label it was given, if any.
func ScanISBNs(text string) []ISBNMatch {
	rs := []rune(text)
	sightings := scan(rs, true)
	var out []ISBNMatch
	floor := 0 // labels before this belong to the previous ISBN
	for i, m := range sightings {
		q, upto := qualifierInParens(rs, m.end)
		if q == "" {
			// A previous label can reach past this ISBN's start, as when the
			// parentheses after one ISBN hold the next: the window is empty then.
			lo := min(max(floor, m.start-qualifierReach), m.start)
			q = nearestQualifier(lineOf(rs[lo:m.start]))
		}
		if q == "" {
			next := len(rs)
			if i+1 < len(sightings) {
				next = sightings[i+1].start
			}
			hi := max(min(next, m.end+qualifierReach), m.end)
			if after := afterWindow(rs[m.end:hi]); len(after) > 0 {
				if q = firstQualifier(after); q != "" {
					upto = m.end + len(after)
				}
			}
		}
		floor = max(floor, m.end, upto)
		if j := slices.IndexFunc(out, func(o ISBNMatch) bool { return o.ISBN == m.isbn }); j >= 0 {
			if out[j].Qualifier == "" {
				out[j].Qualifier = q
			}
			continue
		}
		out = append(out, ISBNMatch{ISBN: m.isbn, Qualifier: q})
	}
	return out
}

// lineOf returns the part of window after its last line break.
func lineOf(window []rune) []rune {
	if i := slices.Index(window, '\n'); i >= 0 {
		return lineOf(window[i+1:])
	}
	return window
}

// afterWindow returns the part of window up to the end of its line or the
// next "ISBN" label.
func afterWindow(window []rune) []rune {
	if i := slices.Index(window, '\n'); i >= 0 {
		window = window[:i]
	}
	for i := 0; i+4 <= len(window); i++ {
		if strings.EqualFold(string(window[i:i+4]), "isbn") {
			return window[:i]
		}
	}
	return window
}

// qualifierInParens returns the qualifier of a bracketed label right after
// rs[end:], and where that label ends; "" and 0 when there is none or it
// names no qualifier.
func qualifierInParens(rs []rune, end int) (string, int) {
	i := end
	for i < len(rs) && (rs[i] == ' ' || rs[i] == '\t') {
		i++
	}
	if i >= len(rs) || (rs[i] != '(' && rs[i] != '[') {
		return "", 0
	}
	closer := ')'
	if rs[i] == '[' {
		closer = ']'
	}
	for j := i + 1; j < len(rs) && j <= i+parenReach; j++ {
		if rs[j] == closer {
			if q := nearestQualifier(rs[i+1 : j]); q != "" {
				return q, j + 1
			}
			return "", 0
		}
	}
	return "", 0
}

// foldWindow normalises a window for matching the table: composed accents,
// lower case, and one hyphen for every dash.
func foldWindow(window []rune) []rune {
	out := make([]rune, 0, len(window))
	for _, r := range norm.NFC.String(string(window)) {
		if isDash(r) {
			r = '-'
		}
		out = append(out, unicode.ToLower(r))
	}
	return out
}

type termHit struct {
	qualifier  string
	start, end int
}

// termHits lists the table terms in window, as whole words.
func termHits(window []rune) []termHit {
	w := foldWindow(window)
	var hits []termHit
	for _, t := range qualifierTerms {
		term := []rune(t.term)
		for at := 0; at+len(term) <= len(w); at++ {
			end := at + len(term)
			if string(w[at:end]) != t.term || at > 0 && isWordRune(w[at-1]) || end < len(w) && isWordRune(w[end]) {
				continue
			}
			hits = append(hits, termHit{t.qualifier, at, end})
		}
	}
	return hits
}

// nearestQualifier returns the qualifier of the table term that ends last in
// window, or "".
func nearestQualifier(window []rune) string {
	best, bestEnd := "", -1
	for _, h := range termHits(window) {
		if h.end > bestEnd {
			best, bestEnd = h.qualifier, h.end
		}
	}
	return best
}

// firstQualifier returns the qualifier of the table term that starts first in
// window, or "".
func firstQualifier(window []rune) string {
	best, bestStart := "", len(window)+1
	for _, h := range termHits(window) {
		if h.start < bestStart {
			best, bestStart = h.qualifier, h.start
		}
	}
	return best
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }
