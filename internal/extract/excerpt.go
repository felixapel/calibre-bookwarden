package extract

import (
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

// Excerpt is a sealed front-matter excerpt: its body, the unit map that says
// which spine document each part came from (ADR-032 part 2), and the method
// that read it. Offsets in every Verified are rune offsets into Text.
type Excerpt struct {
	Text  string
	Units []evidence.ExcerptUnit
	// Method is evidence.ExcerptEPUBText or evidence.ExcerptTextLayer. Only a
	// text layer joins line-break hyphenation; anything else is read as EPUB
	// text, where a line break is a block boundary.
	Method string

	prep *prepared
}

// NewExcerpt builds an Excerpt from the body, units and method of a stored one.
func NewExcerpt(e evidence.Excerpt) Excerpt {
	return Excerpt{Text: e.Text, Units: slices.Clone(e.Units), Method: e.Method}
}

type prepared struct {
	// What the preparation was made from: an Excerpt whose fields changed
	// since is prepared again.
	text   string
	units  []evidence.ExcerptUnit
	method string

	orig []rune
	n    *Normalized
	// cut maps a unit index to the normalised index where its excluded
	// section starts: the first heading.
	cut map[int]int
	// backCopyright is the index of the back unit that is the book's own
	// copyright page, or -1, and backFrom the normalised index where that
	// page starts (see backCopyrightPage).
	backCopyright, backFrom int
}

func (x Excerpt) joinHyphens() bool { return x.Method == evidence.ExcerptTextLayer }

// fold folds a quote or a value the way the excerpt's text is normalised.
func (x Excerpt) fold(s string) string { return foldWith(s, x.joinHyphens()) }

// Prepared returns x with its text normalised once, so that Verify and Parse
// called many times on one excerpt don't repeat the work. Verify and Parse do
// it themselves when they get an Excerpt that is not prepared, or whose text,
// units or method changed after it was.
func (x Excerpt) Prepared() Excerpt {
	if p := x.prep; p != nil && p.text == x.Text && p.method == x.Method && slices.Equal(p.units, x.Units) {
		return x
	}
	p := &prepared{
		text: x.Text, units: slices.Clone(x.Units), method: x.Method,
		orig: []rune(x.Text), n: normalize(x.Text, x.joinHyphens()), cut: map[int]int{},
	}
	// Headings are looked for on the lines as printed, before any hyphen join
	// could run a heading into the line above it.
	for ls := 0; ls <= len(p.orig); {
		le := ls
		for le < len(p.orig) && !isLineBreak(p.orig[le]) && p.orig[le] != '\r' {
			le++
		}
		if at, ok := headingAt(p.orig[ls:le]); ok {
			u := x.unitAt(ls + at)
			if c, seen := p.cut[u]; !seen || p.normIndex(ls+at) < c {
				p.cut[u] = p.normIndex(ls + at)
			}
		}
		ls = le + 1
	}
	var from int
	p.backCopyright, from = x.backCopyrightPage(p.orig)
	p.backFrom = p.normIndex(from)
	x.prep = p
	return x
}

// copyrightYear is a copyright line's mark and year on one line.
var copyrightYear = regexp.MustCompile(`(?i)(©|\(c\)|copyright)[^\n]*\b(1[5-9]|20)\d{2}\b`)

// rightsReserved are the phrases of a copyright page's rights line, lower
// case.
var rightsReserved = []string{
	"all rights reserved", "alle rechte vorbehalten", "todos los derechos reservados", "reservados todos los derechos",
	"tous droits réservés", "tutti i diritti riservati",
}

// permissionCredit marks a credit for quoted material, whose © line is not
// the book's, lower case.
var permissionCredit = []string{"by permission", "with permission", "words and music", "lyrics"}

// backCopyrightPage finds the back unit that is the book's own copyright
// page: its index and the original offset where the page starts, the line
// of its first copyright line; or -1. A back unit is such a page when it
// has a copyright line with a year and a rights line, credits no quoted
// material, and is the only one; and no other unit has a copyright line or
// the copyright landmark, since then the book's own page is there and a
// back one is another book's.
func (x Excerpt) backCopyrightPage(orig []rune) (int, int) {
	found, from := -1, 0
	for i, u := range x.Units {
		if u.Start < 0 || u.End > len(orig) || u.Start >= u.End {
			continue
		}
		text := string(orig[u.Start:u.End])
		m := copyrightYear.FindStringIndex(text)
		if u.Landmark != evidence.LandmarkBack {
			if m != nil || u.Landmark == evidence.LandmarkCopyright {
				return -1, 0
			}
			continue
		}
		lower := strings.ToLower(text)
		if m == nil || !slices.ContainsFunc(rightsReserved, func(p string) bool { return strings.Contains(lower, p) }) ||
			slices.ContainsFunc(permissionCredit, func(p string) bool { return strings.Contains(lower, p) }) {
			continue
		}
		if found >= 0 {
			return -1, 0
		}
		found, from = i, u.Start+utf8.RuneCountInString(text[:m[0]])
		for from > u.Start && !isLineBreak(orig[from-1]) {
			from--
		}
	}
	return found, from
}

// normIndex returns the first normalised index whose original range starts at
// or after original rune offset o.
func (p *prepared) normIndex(o int) int {
	return sort.Search(len(p.n.start), func(i int) bool { return p.n.start[i] >= o })
}

// unitAt returns the index of the unit holding original rune offset o, or -1
// when none does. With no units the whole text is the one unit 0.
func (x Excerpt) unitAt(o int) int {
	if len(x.Units) == 0 {
		return 0
	}
	for i, u := range x.Units {
		if o >= u.Start && o < u.End {
			return i
		}
	}
	return -1
}

// unitInfo returns the position and landmark of unit i; both are zero values
// when there are no units.
func (x Excerpt) unitInfo(i int) (pos int, landmark string) {
	if i < 0 || i >= len(x.Units) {
		return 0, ""
	}
	return x.Units[i].Pos, x.Units[i].Landmark
}

// headings is the closed list of lines that open a section that is not about
// this book: other books, praise, previews, ads. From such a heading to the
// end of its unit every claim is excluded (design section 5.3). Lower case; a
// heading matches at the start of a line, after a numbering, or after a
// sentence's end within a line, as a whole word.
var headings = []string{
	"also by", "also available", "books by", "more from", "by the same author", "other books by", "other titles by",
	"other works", "praise for", "coming soon", "read on for", "excerpt from", "a preview of", "look for", "don't miss",
	"weitere titel", "weitere bücher", "vom selben autor", "von derselben autorin", //nolint:misspell // German
	"ebenfalls erschienen", "ebenfalls von",
	"del mismo autor", "otros libros de", "también de", //nolint:misspell // Spanish
	"du même auteur", "dello stesso autore",
}

// headingAt returns the rune offset in line where an excluded section starts:
// the line's start, or the start of a sentence in it.
func headingAt(line []rune) (int, bool) {
	if isHeading(string(line)) {
		return 0, true
	}
	s := string(line)
	for _, m := range sentenceEnd.FindAllStringIndex(s, -1) {
		if isHeading(s[m[1]:]) {
			return utf8.RuneCountInString(s[:m[1]]), true
		}
	}
	return 0, false
}

// isHeading reports whether a line opens an excluded section. A zero-width
// space is read both as a space ("Also", ZWSP, "by") and as nothing (inside
// a word, "Al", ZWSP, "so by"), and the line is a heading if either reading
// is: excluding more only grounds less. Other format characters are dropped;
// curly quotes are folded, leading numbering and punctuation skipped, and a
// letter-spaced line ("A L S O  B Y") is read closed up.
func isHeading(line string) bool {
	return isHeadingRead(line, ' ') || strings.ContainsRune(line, 0x200B) && isHeadingRead(line, -1)
}

// isHeadingRead is isHeading with a zero-width space read as zwsp (-1 drops
// it).
func isHeadingRead(line string, zwsp rune) bool {
	line = strings.Map(func(r rune) rune {
		switch {
		case r == 0x200B:
			return zwsp
		case unicode.Is(unicode.Cf, r):
			return -1
		}
		return foldQuote(r)
	}, line)
	l := strings.ToLower(strings.TrimLeftFunc(line, func(r rune) bool { return !unicode.IsLetter(r) }))
	if headingPrefix(l, false) {
		return true
	}
	words := strings.Fields(l)
	if len(words) < 4 {
		return false
	}
	for _, w := range words {
		if utf8.RuneCountInString(w) != 1 {
			return false
		}
	}
	return headingPrefix(strings.Join(words, ""), true)
}

// headingPrefix reports whether l starts with a heading as a whole word;
// closed compares the headings with their spaces removed.
func headingPrefix(l string, closed bool) bool {
	for _, h := range headings {
		if closed {
			h = strings.ReplaceAll(h, " ", "")
		}
		rest, ok := strings.CutPrefix(l, h)
		if !ok {
			continue
		}
		if r, _ := utf8.DecodeRuneInString(rest); rest == "" || !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return true
		}
	}
	return false
}
