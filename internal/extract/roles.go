package extract

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

// How the text proves a contributor's role (design section 5.2). "by" alone
// proves an author only as a by-line: the first words of a line. Anywhere else
// ("Translation by", "Cover design by", "Published by") it proves nothing, and
// a line of role words over a by-line ("Translated from the French" over "by
// John Roe") gives the by-line its role. A lone name on a title page is an
// author only where nothing before it on the page could make it something
// else.

// maxRoleReach is how far before a quote, on its line, a role phrase may sit.
const maxRoleReach = 120

// leadWords are the words that open a role and that, alone on the line over a
// by-line, give it that role; "" marks a role this package has no constant
// for, which leaves the by-line unproven. Matched as word prefixes.
var leadWords = []struct{ prefix, role string }{
	{"translat", RoleTranslator}, {"übersetz", RoleTranslator}, {"traduc", RoleTranslator}, {"tradui", RoleTranslator},
	{"edited", RoleEditor}, {"editor", RoleEditor}, {"herausgeg", RoleEditor}, {"édit", RoleEditor},
	{"introduc", RoleIntroduction}, {"foreword", RoleIntroduction}, {"preface", RoleIntroduction},
	{"préface", RoleIntroduction}, {"einleitung", RoleIntroduction}, {"vorwort", RoleIntroduction},
	{"prólogo", RoleIntroduction}, {"prologo", RoleIntroduction},
	{"illustrat", RoleIllustrator},
	{"afterword", ""}, {"nachwort", ""}, {"epílogo", ""}, {"postface", ""}, {"notes", ""}, {"anmerkungen", ""},
	{"annotat", ""}, {"commentary", ""}, {"selected", ""}, {"compiled", ""}, {"adapted", ""}, {"abridged", ""},
	{"retold", ""}, {"narrated", ""}, {"read", ""}, {"design", ""}, {"cover", ""}, {"photograph", ""},
	{"published", ""}, {"copyright", ""}, {"deutsch", ""}, {"english", ""}, {"version", ""},
}

// leadFiller are the words a role-lead line may hold besides lead words and a
// language: "With an Introduction", "Translated from the French".
var leadFiller = map[string]bool{
	"with": true, "an": true, "a": true, "and": true, "&": true, "new": true, "the": true, "from": true,
	"mit": true, "einer": true, "einem": true, "und": true, "aus": true, "dem": true, "con": true, "y": true,
	"del": true, "de": true, "du": true, "avec": true, "une": true, "et": true, "par": true, "by": true, "von": true,
}

// leadRole reads a line for role words. cue is false when it has none. When it
// has, role is the one role it gives, or "" when the line gives none this
// package proves: two roles, a role with no constant, or other words.
func leadRole(line string) (role string, cue bool) {
	words := strings.FieldsFunc(strings.ToLower(line), func(r rune) bool {
		return !unicode.IsLetter(r) && r != '&' && r != '\''
	})
	roles := map[string]bool{}
	other := 0
	for i, w := range words {
		matched := false
		for _, l := range leadWords {
			if strings.HasPrefix(w, l.prefix) && !strings.HasPrefix(w, "edition") && !strings.HasPrefix(w, "édition") {
				roles[l.role], matched = true, true
				break
			}
		}
		switch {
		case matched:
		case leadFiller[w]:
		case i > 0 && (words[i-1] == "the" || words[i-1] == "from" || words[i-1] == "dem" || words[i-1] == "del"):
			// a language after "from the", "aus dem", "del"
		default:
			other++
		}
	}
	if len(roles) == 0 {
		return "", false
	}
	if len(roles) != 1 || roles[""] || other > 0 {
		return "", true
	}
	for r := range roles {
		role = r
	}
	return role, true
}

// line returns the text of the normalised line [ls, le).
func (f *frame) line(ls, le int) string { return string(f.n.Runes[ls:le]) }

// prevLine returns the line before the one starting at ls, if there is one
// in the same unit and it is not empty.
func (f *frame) prevLine(ls int) (start, end int, ok bool) {
	if ls == 0 {
		return 0, 0, false
	}
	ps, pe := f.n.LineBounds(ls - 1)
	if ps >= pe || f.x.unitAt(f.n.start[ps]) != f.x.unitAt(f.n.start[ls]) {
		return 0, 0, false
	}
	return ps, pe, true
}

// phraseRole is the role a phrase of kind kind, starting at normalised index
// at, gives the name after it. Every role phrase but "by" gives its own role.
// "by" gives one only when it opens its line: then a role-lead line just above
// gives its role, and with none it is an author.
func (f *frame) phraseRole(kind string, at int) string {
	if kind != RoleAuthor {
		return kind
	}
	ls, _ := f.n.LineBounds(at)
	if strings.TrimFunc(dropInvisible(f.line(ls, at)), isEdge) != "" {
		return ""
	}
	ps, pe, ok := f.prevLine(ls)
	if !ok {
		return RoleAuthor
	}
	// An invisible character must not hide a role (review of #166).
	if role, cue := leadRole(dropInvisible(f.line(ps, pe))); cue {
		return role
	}
	return RoleAuthor
}

// contextRole is the role a phrase before the quote gives it: the last role
// phrase earlier on the quote's line, or one that ends the line before when
// the quote opens its own line ("Translated by" over "Jane Doe"). Between the
// phrase and the quote there may be only other names joined by a connector
// ("Translated by A and B"); anything else (a sentence end, a semicolon,
// "with") cuts the link.
func (f *frame) contextRole() string {
	ls, _ := f.n.LineBounds(f.s)
	from := max(ls, f.s-maxRoleReach)
	prefix := f.line(from, f.s)
	if hits := roles.scan(prefix); len(hits) > 0 {
		h := hits[len(hits)-1]
		if !namesThenConnector(prefix[h.end:]) {
			return ""
		}
		return f.phraseRole(h.kind, from+utf8.RuneCountInString(prefix[:h.start]))
	}
	if strings.TrimFunc(prefix, isEdge) != "" {
		return ""
	}
	ps, pe, ok := f.prevLine(ls)
	if !ok {
		return ""
	}
	// Read with invisible characters dropped, and map the phrase back to its
	// place in the line, so that phraseRole checks the text truly before it.
	dropped, at := dropInvisibleAt(f.line(ps, pe))
	prev := strings.TrimRightFunc(dropped, isEdge)
	hits := roles.scan(prev)
	if len(hits) == 0 || hits[len(hits)-1].end != len(prev) {
		return ""
	}
	h := hits[len(hits)-1]
	return f.phraseRole(h.kind, ps+at[utf8.RuneCountInString(prev[:h.start])])
}

// namesThenConnector reports whether gap, the text between a role phrase and
// a quote, is empty, or names ending in a connector: "Jane Doe and ".
func namesThenConnector(gap string) bool {
	words := strings.Fields(gap)
	if len(words) == 0 {
		return strings.TrimFunc(gap, isEdge) == ""
	}
	if !nameConnectors[strings.ToLower(words[len(words)-1])] {
		return false
	}
	for _, w := range words[:len(words)-1] {
		core := strings.TrimSuffix(w, ",")
		if nameConnectors[strings.ToLower(core)] {
			continue
		}
		if !nameWord(core) || strings.HasSuffix(core, ".") && letterCount(core) >= 3 {
			return false
		}
	}
	return true
}

// wholeLine reports whether the quote is its entire line, ignoring the
// punctuation around it.
func (f *frame) wholeLine() bool {
	ls, le := f.n.LineBounds(f.s)
	return strings.TrimFunc(f.line(ls, le), isEdge) == strings.TrimFunc(string(f.q), isEdge)
}

// notNameWords are words a title, a subtitle or an imprint line holds and a
// person's name doesn't. Lower case.
var notNameWords = map[string]bool{
	"the": true, "a": true, "an": true, "of": true, "and": true, "novel": true, "novels": true, "story": true,
	"stories": true, "tale": true, "tales": true, "book": true, "books": true, "poems": true, "memoir": true,
	"roman": true, "novela": true, "house": true, "press": true, "publishing": true, "publishers": true,
	"verlag": true, "editions": true, "ediciones": true, "éditions": true, "edition": true, "volume": true,
	"part": true, "series": true, "ein": true, "eine": true, "une": true, "una": true, "un": true, "classics": true,
}

// loneName reports whether the quote is a lone name a title page proves to be
// an author's: its whole line, of two words or more and none a title or
// imprint word, under the unit's first line, and with no role phrase, by-line,
// role-lead line, connector line or language line above it in the unit.
func (f *frame) loneName() bool {
	if f.landmark != evidence.LandmarkTitle || !f.wholeLine() {
		return false
	}
	words := strings.Fields(strings.ToLower(string(f.q)))
	if len(words) < 2 {
		return false
	}
	for _, w := range words {
		if notNameWords[strings.TrimFunc(w, isEdge)] {
			return false
		}
	}
	ls, _ := f.n.LineBounds(f.s)
	above := 0
	for at := f.unitStart(); at < ls; {
		s, e := f.n.LineBounds(at)
		at = e + 1
		if s >= e {
			continue
		}
		above++
		l := dropInvisible(f.line(s, e))
		if len(roles.scan(l)) > 0 || languageLine(l) {
			return false
		}
		if _, cue := leadRole(l); cue {
			return false
		}
		if t := strings.ToLower(strings.TrimFunc(l, isEdge)); nameConnectors[t] || t == "y" {
			return false
		}
	}
	return above > 0
}

// unitStart returns the normalised index where the quote's unit starts.
func (f *frame) unitStart() int {
	if f.unit >= len(f.x.Units) {
		return 0
	}
	return f.x.prep.normIndex(f.x.Units[f.unit].Start)
}

// languageLine reports whether a line names the language a book was
// translated from: "From the French", "Aus dem Englischen".
func languageLine(l string) bool {
	l = strings.ToLower(strings.TrimLeftFunc(l, isEdge))
	for _, p := range []string{"from the ", "from ", "aus dem ", "aus der ", "del ", "traduit "} {
		if strings.HasPrefix(l, p) {
			return true
		}
	}
	return false
}

// nameEnds reports whether the name the quote ends with ends there.
func (f *frame) nameEnds() bool {
	_, le := f.n.LineBounds(f.e - 1)
	return nameEndsBefore(string(f.q), f.line(f.e, le))
}

// nameEndsBefore reports whether a name quote, followed on its line by after,
// ends where the quote does: the line ends, or a comma, a semicolon, a
// bracket, a dash or a connector follows. A hyphen, an apostrophe or another
// word right after it means the quote cut a longer name ("Jane" of "Jane Doe",
// "Kate O" of "Kate O'Hara"), and so does a comma before a suffix ("Jane Doe,
// Jr.").
func nameEndsBefore(quote, after string) bool {
	if after == "" {
		return true
	}
	r, _ := utf8.DecodeRuneInString(after)
	switch r {
	case ',':
		next := strings.Fields(after[1:])
		return len(next) == 0 || !nameSuffixes[strings.ToLower(strings.Trim(next[0], ".,;"))]
	case ';', '(', ')', '/', ':':
		return true
	case '.':
		// A sentence's end, not an initial: the name's last word is three
		// letters or more, and a space or the line's end follows.
		words := strings.Fields(quote)
		rest := after[1:]
		return len(words) > 0 && letterCount(words[len(words)-1]) >= 3 && (rest == "" || rest[0] == ' ')
	case ' ':
		rest := strings.TrimLeft(after, " ")
		if rest == "" {
			return true
		}
		r2, _ := utf8.DecodeRuneInString(rest)
		if strings.ContainsRune("([—–-/|·•,;", r2) {
			return true
		}
		w := strings.ToLower(strings.Fields(rest)[0])
		return nameConnectors[w] || w == "with" || w == "mit" || w == "con" || w == "avec" || w == "y"
	}
	return false
}
