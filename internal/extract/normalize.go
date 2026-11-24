// Package extract is the grounding verifier and the deterministic parsers of
// M4 (design sections 5 and 6). It is pure code: no network, no database, and
// nothing calls it yet.
//
// A claim is a field, a value and the verbatim quote that is meant to show it.
// Verify finds the quote in a front-matter excerpt, derives the value from the
// quote by a closed per-field transform, and drops the claim if the span sits
// in an excluded section. Only a value the book's own text derives is
// grounded; nothing here trusts a model.
package extract

import (
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// Normalized is a text after the closed normalisation of design section 5.1,
// with a map back to the original.
//
// The steps are: soft hyphens removed, except before a combining mark (so
// that "e", a soft hyphen and an acute never become "é"); for a text layer
// only, line-break hyphenation ("-\n" between two letters) joined; each
// whitespace run folded to one space; curly quotes and apostrophes folded to
// straight ones; NFC. Nothing changes a letter or a digit: there is no NFKC, so
// "²" and ligatures stay as they are, and U+02BC, a letter, is no apostrophe.
//
// Line breaks are kept as side information, not as characters, so a quote may
// wrap lines and still match, while role and section rules can still see
// lines.
type Normalized struct {
	// Runes is the normalised text.
	Runes []rune

	start, end []int  // original rune range [start, end) each rune stands for
	valid      []bool // len(Runes)+1: a span may start or end at i
	brk        []int  // newlines inside the whitespace run a space stands for, 0 to 2
	breaks     []int  // indexes of the spaces with brk > 0
	origLen    int
}

type item struct {
	r    rune
	s, e int
	brk  int
}

func isLineBreak(r rune) bool {
	switch r {
	case '\n', '\v', '\f', 0x85, 0x2028, 0x2029:
		return true
	}
	return false
}

func foldQuote(r rune) rune {
	switch r {
	case '’', '‘':
		return '\''
	case '“', '”':
		return '"'
	}
	return r
}

// Normalize applies the normalisation to s, joining line-break hyphenation.
// Invalid UTF-8 counts as U+FFFD, one rune per bad byte, as []rune(s) does;
// offsets are rune offsets in []rune(s).
func Normalize(s string) *Normalized { return normalize(s, true) }

// normalize is Normalize with the hyphen join as an option. In an EPUB
// excerpt a line break is a block boundary, so "Smith-" and "Jones" on two
// lines are two blocks, not one word (design section 5.1).
func normalize(s string, joinHyphens bool) *Normalized {
	src := []rune(s)
	items := make([]item, 0, len(src))
	for i := 0; i < len(src); {
		r := src[i]
		switch {
		case r == 0xAD && (i+1 >= len(src) || !unicode.IsMark(src[i+1])):
			i++
		case unicode.IsSpace(r):
			j, lines := i, 0
			for j < len(src) && unicode.IsSpace(src[j]) {
				switch {
				case isLineBreak(src[j]):
					lines++
				case src[j] == '\r' && (j+1 >= len(src) || src[j+1] != '\n'):
					lines++
				}
				j++
			}
			items = appendSpace(items, i, j, min(lines, 2))
			i = j
		case joinHyphens && r == '-' && len(items) > 0 && unicode.IsLetter(items[len(items)-1].r) && items[len(items)-1].e == i:
			// "inter-\nnational": a hyphen at a line end between two letters.
			j := i + 1
			if j < len(src) && src[j] == '\r' {
				j++
			}
			if j < len(src) && src[j] == '\n' && j+1 < len(src) && unicode.IsLetter(src[j+1]) {
				i = j + 1
				break
			}
			items = append(items, item{r, i, i + 1, 0})
			i++
		default:
			items = append(items, item{foldQuote(r), i, i + 1, 0})
			i++
		}
	}
	return compose(items, len(src))
}

// appendSpace adds a whitespace run [s, e) holding lines line breaks, merging
// it into a space right before it (a dropped soft hyphen can leave two).
func appendSpace(items []item, s, e, lines int) []item {
	if n := len(items); n > 0 && items[n-1].r == ' ' {
		items[n-1].e = e
		items[n-1].brk = min(items[n-1].brk+lines, 2)
		return items
	}
	return append(items, item{' ', s, e, lines})
}

// compose runs NFC over the items and builds the tables. A segment NFC works
// on as one unit (a base with its marks) gives one rune, or several runes that
// no span may split.
func compose(items []item, origLen int) *Normalized {
	var sb strings.Builder
	for _, it := range items {
		sb.WriteRune(it.r)
	}
	text := sb.String()
	n := &Normalized{origLen: origLen}
	at := 0 // index of the next item
	for pos := 0; pos < len(text); {
		// NextBoundary gives the next point where NFC can be applied on its
		// own; what lies between two of them is one unit.
		size := norm.NFC.NextBoundaryInString(text[pos:], true)
		if size <= 0 || size > len(text)-pos {
			size = len(text) - pos
		}
		count := utf8.RuneCountInString(text[pos : pos+size])
		group := items[at : at+count]
		at += count
		seg := []rune(norm.NFC.String(text[pos : pos+size]))
		pos += size
		for k, r := range seg {
			s, e, b := group[0].s, group[len(group)-1].e, 0
			if len(group) == 1 && len(seg) == 1 {
				b = group[0].brk
			}
			n.Runes = append(n.Runes, r)
			n.start = append(n.start, s)
			n.end = append(n.end, e)
			n.valid = append(n.valid, k == 0)
			n.brk = append(n.brk, b)
		}
	}
	n.valid = append(n.valid, true)
	for i, b := range n.brk {
		if b > 0 && n.Runes[i] == ' ' {
			n.breaks = append(n.breaks, i)
		}
	}
	return n
}

// Span maps the normalised span [s, e) to original rune offsets [os, oe), and
// reports false when either end lies inside a unit NFC treats as one (a base
// letter and its marks) or out of range. Slicing the original there and
// normalising the slice gives the span back.
func (n *Normalized) Span(s, e int) (os, oe int, ok bool) {
	if s < 0 || e < s || e > len(n.Runes) || !n.valid[s] || !n.valid[e] {
		return 0, 0, false
	}
	if s == e {
		if s == len(n.Runes) {
			return n.origLen, n.origLen, true
		}
		return n.start[s], n.start[s], true
	}
	return n.start[s], n.end[e-1], true
}

// LineBounds returns the line around normalised index i as [start, end): the
// rune range between the surrounding line breaks. The breaks are not part of
// either neighbour.
func (n *Normalized) LineBounds(i int) (start, end int) {
	k := sort.SearchInts(n.breaks, i) // first break at or after i
	end = len(n.Runes)
	if k < len(n.breaks) {
		end = n.breaks[k]
	}
	if k > 0 {
		start = n.breaks[k-1] + 1
	}
	return start, end
}

// withBreaks returns the runes [s, e) with line breaks restored as "\n".
func (n *Normalized) withBreaks(s, e int) string {
	var sb strings.Builder
	for i := s; i < e && i < len(n.Runes); i++ {
		if n.brk[i] > 0 && n.Runes[i] == ' ' {
			sb.WriteString(strings.Repeat("\n", n.brk[i]))
			continue
		}
		sb.WriteRune(n.Runes[i])
	}
	return sb.String()
}

// find returns the start index of every occurrence of q in the text where
// both ends are valid span boundaries and the quote does not start or end in
// the middle of a word.
func (n *Normalized) find(q []rune) []int {
	var out []int
	if len(q) == 0 {
		return nil
	}
	for from := 0; from+len(q) <= len(n.Runes); {
		i := slices.Index(n.Runes[from:], q[0])
		if i < 0 {
			break
		}
		s := from + i
		from = s + 1
		e := s + len(q)
		if e > len(n.Runes) || !slices.Equal(n.Runes[s:e], q) || !n.valid[s] || !n.valid[e] {
			continue
		}
		if isWordRune(q[0]) && s > 0 && isWordRune(n.Runes[s-1]) {
			continue
		}
		if isWordRune(q[len(q)-1]) && e < len(n.Runes) && isWordRune(n.Runes[e]) {
			continue
		}
		out = append(out, s)
	}
	return out
}

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsMark(r)
}

// fold normalises s and trims the spaces around it, as a quote is searched
// in a text layer.
func fold(s string) string { return foldWith(s, true) }

func foldWith(s string, joinHyphens bool) string {
	return strings.TrimSpace(string(normalize(s, joinHyphens).Runes))
}
