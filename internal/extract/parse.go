package extract

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/findings"
)

// maxParsedYearQuote bounds the quote of a year claim, in runes: a label far
// from its year is not a year line.
const maxParsedYearQuote = 200

// isbnToken is an ISBN as printed: an optional "ISBN" label, then digits that
// may be joined by dashes. ScanISBNs decides whether it is one.
var isbnToken = regexp.MustCompile(`(?i)(?:ISBN[ -]?(?:1[03])?[: ]*)?[0-9][0-9\-‐‑–]{8,}[0-9Xx]`)

// Parse returns the claims the deterministic parsers make from the excerpt
// (design section 6): ISBNs with their qualifier, year lines, translators
// and series statements. Each quote is a span of the normalised excerpt, so
// Verify finds it. Parse does no filtering: a claim in an excluded section is
// made, and Verify drops it.
func Parse(x Excerpt) []Claim {
	x = x.Prepared()
	n := x.prep.n
	var out []Claim
	seen := map[string]bool{}
	for at := 0; at <= len(n.Runes); {
		ls, le := n.LineBounds(at)
		at = le + 1
		if ls >= le {
			continue
		}
		line := string(n.Runes[ls:le])
		// add takes a claim whose quote starts at byte offset from of the
		// line. The verifier rules on one occurrence of a quote (ruled), so
		// a claim read anywhere else, where the text around it may say
		// something else, is no claim of the parser's.
		add := func(c Claim, from int) {
			c.Source = SourceParser
			key := strings.Join([]string{c.Field, c.Value, c.Role, c.Label, c.Quote}, "\x00")
			occ, _ := x.ruled([]rune(c.Quote), c.Field)
			if seen[key] || len(occ) == 0 || occ[0] != ls+utf8.RuneCountInString(line[:from]) {
				return
			}
			seen[key] = true
			out = append(out, c)
		}
		_, landmark := x.unitInfo(x.unitAt(n.start[ls]))
		parseISBNs(line, landmark == evidence.LandmarkBack, add)
		parseYears(line, add)
		parseTranslators(line, add)
		parseSeriesLine(line, add)
	}
	return out
}

// parseISBNs claims each ISBN of a line with its qualifier; back is true for a
// line of a back unit, whose ISBNs get none.
func parseISBNs(line string, back bool, add func(Claim, int)) {
	for _, m := range isbnToken.FindAllStringIndex(line, -1) {
		if m[0] > 0 && isWordRune(lastRune(line[:m[0]])) {
			continue
		}
		tok := line[m[0]:m[1]]
		found := findings.ScanISBNs(tok)
		if len(found) != 1 {
			continue
		}
		label := ""
		if !back {
			label = isbnLabel(line, found[0].ISBN)
		}
		add(Claim{Field: FieldISBN, Value: found[0].ISBN, Quote: tok, Label: label}, m[0])
	}
}

func parseYears(line string, add func(Claim, int)) {
	for _, h := range yearHits(line) {
		quote := line[h.labelStart:h.yearEnd]
		if utf8.RuneCountInString(quote) > maxParsedYearQuote {
			continue
		}
		add(Claim{Field: FieldYear, Value: h.year, Quote: quote, Label: h.label}, h.labelStart)
	}
}

type tok struct{ s, e int }

// tokens splits line[from:] at spaces, with byte offsets in line.
func tokens(line string, from int) []tok {
	var out []tok
	for i := from; i < len(line); {
		if line[i] == ' ' {
			i++
			continue
		}
		j := i
		for j < len(line) && line[j] != ' ' {
			j++
		}
		out = append(out, tok{i, j})
		i = j
	}
	return out
}

func nameWord(w string) bool {
	if !nameToken(w) {
		return false
	}
	for _, r := range w {
		if !unicode.IsLetter(r) && !unicode.IsMark(r) && r != '.' && r != '-' && r != '\'' {
			return false
		}
	}
	return true
}

// nameSpans reads up to three names after a role phrase ending at byte
// offset from, joined by "and", "&", "und" or "et". A name is at most five
// words and ends at a comma, a semicolon, a sentence's full stop or the first
// word that is none of a name.
func nameSpans(line string, from int) [][2]int {
	var out [][2]int
	ts := tokens(line, from)
	for i := 0; i < len(ts) && len(out) < 3; {
		start, end, words := ts[i].s, 0, 0
		stop := false
		for i < len(ts) && words < 5 {
			t := line[ts[i].s:ts[i].e]
			core := strings.TrimRight(t, ",;")
			if !nameWord(core) {
				break
			}
			end = ts[i].s + len(core)
			words++
			i++
			if core != t || strings.HasSuffix(core, ".") && letterCount(core) >= 3 {
				// "Doe, Jr.": a suffix after the comma is the name's.
				if strings.HasSuffix(t, ",") && i < len(ts) {
					next := strings.TrimRight(line[ts[i].s:ts[i].e], ",;")
					if nameSuffixes[strings.ToLower(strings.TrimSuffix(next, "."))] {
						end = ts[i].s + len(next)
						i++
					}
				}
				stop = true
				break
			}
		}
		if words == 0 {
			break
		}
		out = append(out, [2]int{start, end})
		if stop || i >= len(ts) || !nameConnectors[strings.ToLower(line[ts[i].s:ts[i].e])] {
			break
		}
		i++
	}
	return out
}

// parseTranslators claims the names after a translator phrase.
func parseTranslators(line string, add func(Claim, int)) {
	for _, h := range roles.scan(line) {
		if h.kind != RoleTranslator {
			continue
		}
		for k, sp := range nameSpans(line, h.end) {
			name, ok := canonName(line[sp[0]:sp[1]])
			if !ok {
				break // a later name's quote would not hold the phrase
			}
			quote := line[sp[0]:sp[1]]
			if k == 0 {
				quote = line[h.start:sp[1]]
			}
			if !nameEndsBefore(quote, line[sp[1]:]) {
				break // the name runs on into something that is no name
			}
			from := sp[0]
			if k == 0 {
				from = h.start
			}
			add(Claim{Field: FieldContributor, Value: name, Role: RoleTranslator, Quote: quote}, from)
		}
	}
}

func parseSeriesLine(line string, add func(Claim, int)) {
	st, ok := parseSeries(line)
	if !ok {
		return
	}
	add(Claim{Field: FieldSeries, Value: st.name, Quote: line}, 0)
	add(Claim{Field: FieldSeriesIndex, Value: st.index, Quote: line}, 0)
}
