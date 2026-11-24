package extract

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/findings"
)

// ---- contributors and roles ----------------------------------------------

// particles are the lower-case words a name may hold besides capitalised ones.
var particles = map[string]bool{
	"von": true, "van": true, "der": true, "den": true, "de": true, "del": true, "della": true, "di": true,
	"da": true, "dos": true, "das": true, "du": true, "la": true, "le": true, "y": true, "bin": true,
	"ibn": true, "al": true, "el": true, "ap": true, "af": true, "ten": true, "ter": true, "zu": true, "zur": true,
}

var elidedParticle = regexp.MustCompile(`^[dl]'\p{Lu}`)

var nameSuffixes = map[string]bool{"jr": true, "sr": true, "ii": true, "iii": true, "iv": true}

// credentials are post-nominal letters: a name's second comma part made of
// them only is dropped ("Jane Doe, PhD" is Jane Doe). Lower case, no dots.
var credentials = map[string]bool{
	"phd": true, "md": true, "dphil": true, "mba": true, "msc": true, "bsc": true, "dds": true, "frs": true,
	"frsl": true, "obe": true, "mbe": true, "cbe": true, "kbe": true, "dbe": true, "qc": true, "kc": true, "esq": true,
}

// roleNouns name a role, not a person: a name holding one is refused ("Jane
// Doe, Editor" is no author). The abbreviations count only with their dot or
// after a comma, since "Ed" is also a given name.
var (
	roleNouns = map[string]bool{
		"editor": true, "editors": true, "translator": true, "translators": true, "illustrator": true,
		"illustrators": true, "compiler": true, "herausgeber": true, "herausgeberin": true, "übersetzer": true,
		"übersetzerin": true, "traductor": true, "traductora": true, "editora": true, "traducteur": true,
		"traductrice": true, "éditeur": true, "éditrice": true,
	}
	roleAbbreviations = map[string]bool{
		"ed": true, "eds": true, "hrsg": true, "hg": true, "übers": true, "trans": true, "tr": true, "comp": true,
		"ill": true, "illus": true,
	}
	// nameConnectors join two people; in any case they make a quote a list.
	nameConnectors = map[string]bool{"and": true, "&": true, "und": true, "et": true}
)

// trimName trims a name; a closing full stop stays only after an initial or a
// suffix ("J.", "Jr."), not after a sentence's last word.
func trimName(s string) string {
	return trimLine(s, func(word string) bool { return letterCount(word) <= 2 })
}

func nameToken(t string) bool {
	t = strings.TrimSuffix(t, ",")
	r, _ := utf8.DecodeRuneInString(t)
	switch {
	case t == "" || !unicode.IsLetter(r):
		return false
	case !unicode.IsLower(r):
		return true // capitalised, or a script with no case
	}
	return particles[t] || elidedParticle.MatchString(t)
}

// roleNoun reports whether a name token names a role; afterComma is true in a
// name's second comma part.
func roleNoun(t string, afterComma bool) bool {
	l := strings.ToLower(strings.TrimSuffix(t, ","))
	if roleNouns[strings.TrimSuffix(l, ".")] {
		return true
	}
	return roleAbbreviations[strings.TrimSuffix(l, ".")] && (afterComma || strings.HasSuffix(l, "."))
}

// canonName reads a person's name from the part of a quote after any role
// phrase: letters, initials, particles and suffixes only, one person, and
// "Surname, Given" turned into "Given Surname"; a trailing part of
// credentials is dropped. The second result is false for anything else:
// digits, brackets, a role phrase or role noun, a lower-case word that is no
// particle, a connector in any case, a list.
func canonName(rest string) (string, bool) {
	rest = trimName(rest)
	if !hasLetter(rest) || utf8.RuneCountInString(rest) > 80 || len(roles.scan(rest)) > 0 {
		return "", false
	}
	for _, r := range rest {
		if !unicode.IsLetter(r) && !unicode.IsMark(r) && r != ' ' && r != '.' && r != '-' && r != '\'' && r != ',' {
			return "", false
		}
	}
	parts := strings.Split(rest, ",")
	if len(parts) == 2 && allCredentials(parts[1]) {
		parts = parts[:1]
	}
	if len(parts) > 2 {
		return "", false
	}
	var fields [][]string
	for k, p := range parts {
		f := strings.Fields(p)
		if len(f) == 0 || len(f) > 5 {
			return "", false
		}
		for i, t := range f {
			if !nameToken(t) || nameConnectors[strings.ToLower(t)] || roleNoun(t, k > 0) {
				return "", false
			}
			// "Ortega y Gasset" is one surname; "Jane Doe y Pablo Dos" is two people.
			if strings.EqualFold(t, "y") && len(f)-i-1 > 1 {
				return "", false
			}
		}
		fields = append(fields, f)
	}
	if len(fields) == 1 {
		return strings.Join(fields[0], " "), true
	}
	left, right := strings.Join(fields[0], " "), strings.Join(fields[1], " ")
	if len(fields[1]) == 1 && nameSuffixes[strings.ToLower(strings.TrimSuffix(right, "."))] {
		return left + ", " + right, true // "Jane Doe, Jr." stays as printed
	}
	for _, t := range fields[1] {
		if nameSuffixes[strings.ToLower(strings.TrimSuffix(t, "."))] {
			return "", false // "Doe, Jane Jr.": the order is not ours to guess
		}
	}
	return right + " " + left, true
}

func allCredentials(part string) bool {
	f := strings.Fields(part)
	for _, t := range f {
		if !credentials[strings.ToLower(strings.ReplaceAll(t, ".", ""))] {
			return false
		}
	}
	return len(f) > 0
}

func deriveContributor(f *frame, c Claim) derived {
	q, role, rest, lead := string(f.q), "", string(f.q), false
	if hits := roles.scan(q); len(hits) > 0 && hits[0].start == 0 {
		role, rest, lead = hits[0].kind, q[hits[0].end:], true
	}
	// v4 (ADR-046): letter case only. The name is parsed from the print, so a
	// list, a role noun or part of a name is no case difference, and dashes
	// are not folded: an em dash may join two people.
	name, ok := canonName(rest)
	if !ok || !sameCase(trimName(f.x.fold(c.Value)), name) || !f.nameEnds() {
		return notDerived()
	}
	if lead {
		role = f.phraseRole(role, f.s)
	} else {
		role = f.contextRole()
	}
	if role == "" && f.loneName() {
		role = RoleAuthor
	}
	if role == "" || (c.Role != "" && c.Role != role) {
		return unproven()
	}
	return derived{value: name, role: role}
}

// ---- year, edition -------------------------------------------------------

var (
	digitRun = regexp.MustCompile(`[0-9]+`)
	// yearListNext is the next year of a list after a year: "© 2003, 2019".
	yearListNext = regexp.MustCompile(`^(?:,? (?:and |& )?|, )([0-9]{4})`)
)

type yearHit struct {
	year, label string
	// labelStart and yearEnd are byte offsets in the scanned text: the span
	// from the label to the year, the shortest quote that shows the year.
	labelStart, yearEnd int
}

// yearHits lists the years in s that follow a label of the closed set. A
// label's year is the first number of three digits or more after it, before
// the next label, in the same sentence and clause; it must be four digits
// from 1400 to 2099 and stand alone (no group of an ISBN or a range), and not
// be a street number ("Inc., 1745 Broadway"). Years listed right after it
// ("© 2003, 2019") are the label's too.
//
// The labels and years are read with U+200B, U+FEFF and U+2060 dropped (an
// invisible character must not hide a label, review of #167), and the
// offsets map back to s: a label's start to its first byte, a year's end to
// just after its last digit.
func yearHits(s string) []yearHit {
	if strings.IndexFunc(s, isInvisible) < 0 {
		return yearHitsIn(s)
	}
	d, startAt, endAt := dropInvisibleBytes(s)
	var out []yearHit
	for _, h := range yearHitsIn(d) {
		h.labelStart, h.yearEnd = startAt[h.labelStart], endAt[h.yearEnd]
		// A year's digits stay one unbroken run as printed: "20", U+200B,
		// "03" is no year (years are not folded, ADR-046).
		if s[h.yearEnd-len(h.year):h.yearEnd] == h.year {
			out = append(out, h)
		}
	}
	return out
}

// dropInvisibleBytes is dropInvisible with byte maps back to s: startAt[i]
// is where byte i of the result starts in s (len(result)+1 entries, the
// last one len(s)), endAt[i] is where the result's first i bytes end in s.
func dropInvisibleBytes(s string) (string, []int, []int) {
	var b strings.Builder
	startAt, endAt := []int{}, []int{0}
	for i, c := range s {
		_, n := utf8.DecodeRuneInString(s[i:])
		if isInvisible(c) {
			if next, _ := utf8.DecodeRuneInString(s[i+n:]); !unicode.IsMark(next) {
				continue
			}
		}
		b.WriteString(s[i : i+n])
		for j := range n {
			startAt = append(startAt, i+j)
			endAt = append(endAt, i+j+1)
		}
	}
	return b.String(), append(startAt, len(s)), endAt
}

// yearHitsIn is yearHits on a text with no invisible characters to drop.
func yearHitsIn(s string) []yearHit {
	labels := yearLabels.scan(s)
	var out []yearHit
	for k, l := range labels {
		limit := len(s)
		if k+1 < len(labels) {
			limit = labels[k+1].start
		}
		at := l.end
		var m []int
		for _, d := range digitRun.FindAllStringIndex(s[at:limit], -1) {
			if d[1]-d[0] >= 3 {
				m = []int{at + d[0], at + d[1]}
				break
			}
		}
		if m == nil {
			continue
		}
		gap := s[at:m[0]]
		if sentenceEnd.MatchString(gap) || strings.Contains(gap, ";") || !standaloneYear(s, m[0], m[1]) {
			continue
		}
		if strings.HasSuffix(strings.TrimRight(gap, " "), ",") && capitalAfter(s[m[1]:]) {
			continue // a street number after a comma: "Random House, Inc., 1745 Broadway"
		}
		out = append(out, yearHit{s[m[0]:m[1]], l.kind, l.start, m[1]})
		for end := m[1]; ; {
			n := yearListNext.FindStringSubmatchIndex(s[end:limit])
			if n == nil || !standaloneYear(s, end+n[2], end+n[3]) {
				break
			}
			out = append(out, yearHit{s[end+n[2] : end+n[3]], l.kind, l.start, end + n[3]})
			end += n[3]
		}
	}
	return out
}

// standaloneYear reports whether s[a:b], a run of digits, is a year on its
// own: four digits from 1400 to 2099, with no digit group joined to it by a
// space, a hyphen or a dash on either side.
func standaloneYear(s string, a, b int) bool {
	year := s[a:b]
	if len(year) != 4 || year < "1400" || year > "2099" {
		return false
	}
	joined := func(sep, digit rune) bool {
		return digit >= '0' && digit <= '9' && strings.ContainsRune(" -‐‑–—/", sep)
	}
	if b < len(s) {
		sep, n := utf8.DecodeRuneInString(s[b:])
		if next, _ := utf8.DecodeRuneInString(s[b+n:]); joined(sep, next) {
			return false
		}
	}
	if a > 0 {
		sep, n := utf8.DecodeLastRuneInString(s[:a])
		if prev, _ := utf8.DecodeLastRuneInString(s[:a-n]); joined(sep, prev) {
			return false
		}
	}
	return true
}

// capitalAfter reports whether a capitalised word follows right after a space.
func capitalAfter(s string) bool {
	r, n := utf8.DecodeRuneInString(s)
	if r != ' ' {
		return false
	}
	next, _ := utf8.DecodeRuneInString(s[n:])
	return unicode.IsUpper(next)
}

// deriveYear finds the claimed year among the year hits of the quote's lines
// whose label and year both sit in the quote. A year with two labels there
// needs the claim to name one.
func deriveYear(f *frame, c Claim) derived {
	want := strings.TrimSpace(f.x.fold(c.Value))
	ls, _ := f.n.LineBounds(f.s)
	_, le := f.n.LineBounds(f.e - 1)
	text := f.line(ls, le)
	qs := len(f.line(ls, f.s))
	qe := qs + len(string(f.q))
	labels := map[string]bool{}
	for _, h := range yearHits(text) {
		if h.year == want && h.labelStart >= qs && h.yearEnd <= qe && (c.Label == "" || c.Label == h.label) {
			labels[h.label] = true
		}
	}
	if len(labels) != 1 {
		return notDerived()
	}
	for l := range labels {
		return derived{value: want, label: l}
	}
	return notDerived()
}

func deriveEdition(f *frame, c Claim) derived {
	q, want := string(f.q), f.x.fold(c.Value)
	hits := editions.scan(q)
	for _, h := range hits {
		if q[h.start:h.end] == want {
			return got(want)
		}
	}
	// v4 (ADR-046): else letter case only; the value is the printed phrase.
	for _, h := range hits {
		if sameCase(q[h.start:h.end], want) {
			return got(q[h.start:h.end])
		}
	}
	return notDerived()
}

// ---- series --------------------------------------------------------------

func deriveSeries(f *frame, c Claim) derived {
	st, ok := parseSeries(string(f.q))
	if !ok {
		return notDerived()
	}
	if c.Field == FieldSeries {
		// v4 (ADR-046): letter case only; the value is the printed name.
		if !sameCase(trimTitle(f.x.fold(c.Value)), st.name) {
			return notDerived()
		}
		return got(st.name)
	}
	if v, ok := canonDecimal(f.x.fold(c.Value)); !ok || v != st.index {
		return notDerived()
	}
	return got(st.index)
}

// ---- ISBN ----------------------------------------------------------------

// deriveISBN takes the claimed ISBN if the quote holds it with a valid
// checksum, as an ISBN-13, and gives it the qualifier its label gives it,
// reading the label on the quote's own lines (ADR-032). An ISBN in a back unit
// gets no qualifier: there it may be an ad's.
func deriveISBN(f *frame, c Claim) derived {
	want, ok := findings.CanonicalISBN(c.Value)
	if !ok {
		return notDerived()
	}
	// v4 (ADR-046): any dash variant separates the groups, as a hyphen or an
	// en dash already did; the checksum still decides.
	held := false
	for _, m := range findings.ScanISBNs(dashFold.Replace(string(f.q))) {
		held = held || m.ISBN == want
	}
	if !held {
		return notDerived()
	}
	label := ""
	if f.landmark != evidence.LandmarkBack {
		ls, _ := f.n.LineBounds(f.s)
		_, le := f.n.LineBounds(f.e - 1)
		label = isbnLabel(dashFold.Replace(f.n.withBreaks(ls, le)), want)
	}
	if c.Label != "" && c.Label != label {
		return notDerived()
	}
	return derived{value: want, label: label}
}

// parenAfter is a bracketed label right after an ISBN: " (ebook)".
var parenAfter = regexp.MustCompile(`^\s*\([^()\n]{0,30}\)`)

// isbnLabel returns the qualifier of ISBN want in text, its line or lines. A
// label between two ISBNs of a line could be either's ("ISBN a Print ISBN
// b"), so the qualifier counts only when two readings agree: findings'
// reading of the whole text, and its reading of the ISBN's own segment, which
// runs from the previous ISBN (and its brackets) to this one's end (and its
// brackets) when another ISBN follows, or to the text's end when none does.
func isbnLabel(text, want string) string {
	whole, found := "", false
	for _, m := range findings.ScanISBNs(text) {
		if m.ISBN == want {
			whole, found = m.Qualifier, true
			break
		}
	}
	if !found || whole == "" {
		return ""
	}
	type token struct {
		s, e int
		isbn string
	}
	var toks []token
	for _, m := range isbnToken.FindAllStringIndex(text, -1) {
		if m[0] > 0 && isWordRune(lastRune(text[:m[0]])) {
			continue
		}
		if found := findings.ScanISBNs(text[m[0]:m[1]]); len(found) == 1 {
			toks = append(toks, token{m[0], m[1], found[0].ISBN})
		}
	}
	for k, t := range toks {
		if t.isbn != want {
			continue
		}
		from, to := 0, len(text)
		if k > 0 {
			prev := toks[k-1].e
			from = min(prev+len(parenAfter.FindString(text[prev:])), t.s)
		}
		if k+1 < len(toks) {
			to = t.e + len(parenAfter.FindString(text[t.e:]))
		}
		for _, m := range findings.ScanISBNs(text[from:to]) {
			if m.ISBN == want && m.Qualifier == whole {
				return whole
			}
		}
		return ""
	}
	return ""
}
