package extract

import (
	"slices"
	"strings"
	"unicode"
)

// derive applies the closed transform of c.Field to the quote at f. The value
// must equal what it makes of the quote (design section 5.2).
func derive(f *frame, c Claim) derived {
	d := deriveField(f, c)
	if d.status == StatusValueNotDerived {
		d.reason = refusalReason(f, c)
	}
	return d
}

func deriveField(f *frame, c Claim) derived {
	// Only a contributor has a role, and only a year or an ISBN a label.
	if c.Role != "" && c.Field != FieldContributor || c.Label != "" && c.Field != FieldYear && c.Field != FieldISBN {
		return notDerived()
	}
	switch c.Field {
	case FieldTitle, FieldSubtitle:
		return deriveTitle(f, c)
	case FieldImprint:
		return deriveImprint(f, c)
	case FieldContributor:
		return deriveContributor(f, c)
	case FieldYear:
		return deriveYear(f, c)
	case FieldEdition:
		return deriveEdition(f, c)
	case FieldSeries, FieldSeriesIndex:
		return deriveSeries(f, c)
	case FieldISBN:
		return deriveISBN(f, c)
	}
	return notDerived()
}

func isEdge(r rune) bool { return unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) }

func hasLetterOrDigit(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) >= 0
}

// closers maps each opening bracket or quote to its closer.
var closers = map[rune]rune{'(': ')', '[': ']', '{': '}', '«': '»', '"': '"'}

func opener(c rune) (rune, bool) {
	for o, cl := range closers {
		if cl == c {
			return o, true
		}
	}
	return 0, false
}

// balanced reports whether r holds as many o as c (an even number when they
// are the same quote mark).
func balanced(r []rune, o, c rune) bool {
	no, nc := 0, 0
	for _, x := range r {
		switch x {
		case o:
			no++
		case c:
			nc++
		}
	}
	if o == c {
		return no%2 == 0
	}
	return no == nc
}

// trimEdges removes the whitespace and punctuation around a value. A bracket
// or quote stays when its partner stays ("Harbor House (UK)"), and a pair
// that wraps the whole value goes; an ellipsis stays. keepTail says whether
// the last rune of r[i:j], an edge rune, belongs to the value.
func trimEdges(s string, keepTail func(r []rune, i, j int) bool) string {
	r := []rune(s)
	i, j := 0, len(r)
	for {
		for i < j && isEdge(r[i]) && r[i] != '¿' && r[i] != '¡' {
			if c, ok := closers[r[i]]; ok && slices.Contains(r[i+1:j], c) {
				break
			}
			i++
		}
		for j > i && isEdge(r[j-1]) {
			last := r[j-1]
			if o, ok := opener(last); ok && slices.Contains(r[i:j-1], o) {
				break
			}
			if last == '…' || last == '.' && j-i >= 4 && string(r[j-3:j]) == "..." && r[j-4] != '.' {
				break
			}
			if keepTail(r, i, j) {
				break
			}
			j--
		}
		// A pair around the whole value goes: "(The Lantern)", a quoted title.
		if j-i >= 2 {
			if c, ok := closers[r[i]]; ok && r[j-1] == c && balanced(r[i+1:j-1], r[i], c) {
				i, j = i+1, j-1
				continue
			}
		}
		return string(r[i:j])
	}
}

// trimTitle trims a title. Case stays; an opening "¿" or "¡" and a closing
// "?" or "!" belong to a title and stay.
func trimTitle(s string) string {
	return trimEdges(s, func(r []rune, _, j int) bool { return r[j-1] == '?' || r[j-1] == '!' })
}

// trimLine is trimEdges for a value that ends in an abbreviation: the closing
// full stop stays when keepDot says that the last word is one, and goes
// otherwise, as a sentence period.
func trimLine(s string, keepDot func(word string) bool) string {
	return trimEdges(s, func(r []rune, i, j int) bool {
		if r[j-1] != '.' {
			return false
		}
		word := string(r[i:j])
		if k := strings.LastIndexByte(word, ' '); k >= 0 {
			word = word[k+1:]
		}
		return keepDot(word)
	})
}

// hasFormatChar reports whether s holds an invisible format or bidi control
// character (Unicode Cf). A value with one is refused: what it shows is not
// what it is.
func hasFormatChar(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return unicode.Is(unicode.Cf, r) }) >= 0
}

func letterCount(s string) int {
	n := 0
	for _, r := range s {
		if unicode.IsLetter(r) {
			n++
		}
	}
	return n
}

// imprintAbbreviations are corporate abbreviations whose full stop belongs to
// the imprint ("Inc.", "Ltd.").
var imprintAbbreviations = map[string]bool{
	"inc.": true, "ltd.": true, "co.": true, "corp.": true, "bros.": true, "llc.": true,
	"plc.": true, "ltda.": true, "cia.": true, "gmbh.": true, "pty.": true, "pte.": true,
}

func trimImprint(s string) string {
	return trimLine(s, func(word string) bool {
		w := strings.ToLower(word)
		return imprintAbbreviations[w] || strings.Contains(strings.TrimSuffix(w, "."), ".") || letterCount(w) <= 1
	})
}

func deriveTitle(f *frame, c Claim) derived { return deriveWhole(f, c, trimTitle) }

func deriveImprint(f *frame, c Claim) derived { return deriveWhole(f, c, trimImprint) }

// deriveWhole derives a value that must be the whole trimmed quote: a title,
// a subtitle or an imprint. Since v4 (ADR-046) the comparison folds case and
// dashes and drops U+200B, U+FEFF and U+2060 on both sides; the value is the
// print, with those three invisible characters dropped, never the model's
// spelling.
func deriveWhole(f *frame, c Claim, trim func(string) string) derived {
	want := trim(dropInvisible(string(f.q)))
	if !hasLetterOrDigit(want) || hasFormatChar(want) || f.crossesRoleLine() || !sameFolded(trim(dropInvisible(f.x.fold(c.Value))), want) {
		return notDerived()
	}
	return got(want)
}

// dropInvisible removes U+200B, U+FEFF and U+2060, except before a
// combining mark: as with the soft hyphen (normalize.go), dropping one there
// would join the mark to the letter before it. One that stays still refuses
// the value as a format character.
func dropInvisible(s string) string {
	if strings.IndexFunc(s, isInvisible) < 0 {
		return s
	}
	out, _ := dropInvisibleAt(s)
	return out
}

// dropInvisibleAt is dropInvisible that also returns, for each rune of the
// result and one past its end, the rune index in s it came from, so that a
// match in the result maps back to the text.
func dropInvisibleAt(s string) (string, []int) {
	r := []rune(s)
	out := make([]rune, 0, len(r))
	at := make([]int, 0, len(r)+1)
	for i, c := range r {
		if isInvisible(c) && (i+1 >= len(r) || !unicode.IsMark(r[i+1])) {
			continue
		}
		out = append(out, c)
		at = append(at, i)
	}
	return string(out), append(at, len(r))
}

func isInvisible(r rune) bool { return r == '\u200b' || r == '\ufeff' || r == '\u2060' }

// sameFolded compares two values as v4's verifier does: letter case (simple
// lower-case mapping, so ß against SS stays different) and dash variants
// folded. Callers drop the invisible format characters first where a field
// allows it.
func sameFolded(a, b string) bool { return sameCase(dashFold.Replace(a), dashFold.Replace(b)) }

// sameCase compares two values with letter case folded by simple lower-case
// mapping (strings.ToLower), not Unicode case folding: ß against SS, a final
// sigma or the long s stay different.
func sameCase(a, b string) bool { return lowerCase(a) == lowerCase(b) }

func lowerCase(s string) string { return strings.ToLower(s) }

// crossesRoleLine reports whether the quote runs on into a line that opens
// with a role phrase: a title or an imprint may wrap lines, but a by-line or
// a "Translated by" line is no part of it.
func (f *frame) crossesRoleLine() bool {
	_, le := f.n.LineBounds(f.s)
	for le < f.e {
		ls := le + 1
		_, le = f.n.LineBounds(ls)
		// An invisible character must not hide a role phrase (v4).
		l := strings.TrimLeftFunc(dropInvisible(f.line(ls, min(le, f.e))), isEdge)
		if hits := roles.scan(l); len(hits) > 0 && hits[0].start == 0 {
			return true
		}
	}
	return false
}
