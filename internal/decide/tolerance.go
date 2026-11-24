package decide

import (
	"encoding/json"
	"slices"
	"strings"
	"unicode"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/findings"
)

// Decision version 7's tolerances (ADR-030, "Decision v7 tolerances"). They
// widen what counts as the same title or the same authors when records are
// compared with each other and with the files. They only ever decide a tier:
// no proposal is built from a normalised form.

// articleSet is one language's closed list of leading articles. An indefinite
// article is folded only before at least two words, so a title such as "A
// Man" (Fallaci) is not read as "Man".
type articleSet struct{ definite, indefinite []string }

var articleSets = map[string]articleSet{
	"en": {definite: []string{"the"}, indefinite: []string{"a", "an"}},
	"de": {definite: []string{"der", "die", "das"}, indefinite: []string{"ein", "eine"}},
	"es": {definite: []string{"el", "la", "los", "las"}, indefinite: []string{"un", "una"}},
	"fr": {definite: []string{"le", "la", "les", "l'"}, indefinite: []string{"un", "une"}},
	"it": {definite: []string{"il", "lo", "la", "i", "gli", "le", "l'"}},
}

// conjunctions are the words read as "&" by language ("and" always is).
var conjunctions = map[string]string{"de": "und", "es": "y", "fr": "et", "it": "e"}

// honorifics are the closed set of leading titles dropped from an author's
// name. A name that is only one of them keeps it.
var honorifics = []string{"dr", "prof", "sir", "dame", "rev", "mr", "mrs", "ms"}

// maxExtraAuthors is how many more names one author list may hold than
// another and still be consistent with it.
const maxExtraAuthors = 2

func isLetter(r rune) bool { return unicode.IsLetter(r) || unicode.IsMark(r) }

// foldTitleV7 collapses whitespace, makes the apostrophe class ' ’ ʼ one
// apostrophe, reads a "?" between two letters (Open Library's mangled
// apostrophe) as one, reads "&" and the language's conjunction as "and", and
// attaches a detached ":" to the word before it.
func foldTitleV7(s, lang string) string {
	rs := []rune(s)
	out := make([]rune, len(rs))
	for i, r := range rs {
		switch {
		case r == '’' || r == 'ʼ':
			r = '\''
		case r == '?' && i > 0 && i+1 < len(rs) && isLetter(rs[i-1]) && isLetter(rs[i+1]):
			r = '\''
		}
		out[i] = r
	}
	conj := conjunctions[lang]
	var toks []string
	for _, tok := range strings.Fields(string(out)) {
		switch {
		case tok == "&" || conj != "" && tok == conj:
			tok = "and"
		case tok == ":" && len(toks) > 0:
			toks[len(toks)-1] += ":"
			continue
		}
		toks = append(toks, tok)
	}
	return strings.Join(toks, " ")
}

// canonTitleV7 reads a folded title key for comparison under version 7:
// trailing parentheticals that name no volume, one imprint series as a prefix
// and one leading article of the language on the main title and on the
// subtitle are ignored, and "&" is "and". It repeats until nothing changes,
// so it is idempotent, and it never reduces a title to nothing: a bare
// article stays as it is.
func canonTitleV7(key, lang string) string { return canonWithV7(key, lang, true) }

// canonWithV7 is canonTitleV7, optionally keeping trailing parentheticals.
func canonWithV7(key, lang string, dropParens bool) string {
	t := foldTitleV7(key, lang)
	for range len(key) + 2 {
		next := foldTitleV7(titleStepV7(t, lang, dropParens), lang)
		if next == t {
			break
		}
		t = next
	}
	return t
}

func titleStepV7(t, lang string, dropParens bool) string {
	if dropParens {
		t = dropParenthetical(t)
	}
	t = dropImprint(t)
	if i := strings.Index(t, ": "); i >= 0 {
		main, sub := t[:i], t[i+2:]
		return stripArticle(main, lang) + ": " + stripArticle(sub, lang)
	}
	return stripArticle(t, lang)
}

// dropParenthetical removes one trailing, unnested parenthetical that names
// no volume (ADR-030).
func dropParenthetical(t string) string {
	t = strings.TrimSpace(t)
	if strings.HasSuffix(t, ")") {
		if i := strings.LastIndex(t, " ("); i > 0 {
			inner := strings.TrimSpace(t[i+2 : len(t)-1])
			if !strings.ContainsAny(inner, "()") && !volume.MatchString(inner) {
				return strings.TrimSpace(t[:i])
			}
		}
	}
	return t
}

// dropImprint removes a leading imprint series from the findings list
// (ADR-031) when a title follows it.
func dropImprint(t string) string {
	for _, imprint := range findings.SeriesImprints() {
		if rest, ok := strings.CutPrefix(t, imprint+" "); ok && strings.TrimSpace(rest) != "" {
			return strings.TrimSpace(rest)
		}
	}
	return t
}

// isArticle reports whether a word is one of the language's articles,
// counting "l'" attached to a word.
func (a articleSet) isArticle(tok string) bool {
	if slices.Contains(a.definite, tok) || slices.Contains(a.indefinite, tok) {
		return true
	}
	return slices.Contains(a.definite, "l'") && strings.HasPrefix(tok, "l'") && len(tok) > 2
}

// stripArticle drops one leading article of lang from s when words follow
// that don't themselves start with an article, and when an indefinite
// article is followed by at least two words. s is otherwise returned as it is.
func stripArticle(s, lang string) string {
	set, ok := articleSets[lang]
	toks := strings.Fields(s)
	if !ok || len(toks) == 0 {
		return s
	}
	var rest []string
	switch first := toks[0]; {
	case slices.Contains(set.definite, "l'") && strings.HasPrefix(first, "l'") && len(first) > 2:
		rest = append([]string{first[2:]}, toks[1:]...)
	case slices.Contains(set.definite, first) && len(toks) > 1:
		rest = toks[1:]
	case slices.Contains(set.indefinite, first) && len(toks) > 2:
		rest = toks[1:]
	default:
		return s
	}
	if set.isArticle(rest[0]) {
		return s
	}
	return strings.Join(rest, " ")
}

// titlesEquivalentV7 reports whether two folded title keys are the same
// title under version 7's tolerances. A trailing parenthetical is ignored
// only when the other title has none: two different ones ("(Abridged)" and
// "(Unabridged)", two year ranges) are two different works or editions.
func titlesEquivalentV7(a, b, lang string) bool {
	if a == b {
		return true
	}
	hasParen := func(x string) bool {
		f := foldTitleV7(x, lang)
		return dropParenthetical(f) != strings.TrimSpace(f)
	}
	drop := !hasParen(a) || !hasParen(b)
	return canonWithV7(a, lang, drop) == canonWithV7(b, lang, drop)
}

// nameKeyV7 reads a folded author name for comparison: dots are blanks, so
// "E. M." "E.M." and "E M" agree, and leading honorifics go while a name
// remains. A name that would vanish is returned as it is.
func nameKeyV7(name string) string {
	toks := strings.Fields(strings.ReplaceAll(name, ".", " "))
	for len(toks) > 1 && slices.Contains(honorifics, toks[0]) {
		toks = toks[1:]
	}
	if len(toks) == 0 {
		return name
	}
	return strings.Join(toks, " ")
}

// authorsConsistentV7 reports whether two author lists (folded names) are
// the same people under version 7: equal after name normalisation, or one is
// a strict superset of the other with the same first author and at most
// maxExtraAuthors more names, such as a translator or illustrator that only
// one source lists (ADR-027, ADR-031). The verdict is for tiers; it never
// proposes adding or removing a name.
func authorsConsistentV7(a, b []string) bool {
	na, nb := normNames(a), normNames(b)
	if len(na) == 0 || len(nb) == 0 {
		return false
	}
	if slices.Equal(na, nb) {
		return true
	}
	short, long := na, nb
	if len(short) > len(long) {
		short, long = long, short
	}
	if len(short) == len(long) || short[0] != long[0] {
		return false
	}
	for _, name := range short {
		if !slices.Contains(long, name) {
			return false
		}
	}
	extra := 0
	var seen []string
	for _, name := range long {
		if !slices.Contains(short, name) && !slices.Contains(seen, name) {
			seen = append(seen, name)
			extra++
		}
	}
	return extra >= 1 && extra <= maxExtraAuthors
}

func normNames(in []string) []string {
	out := make([]string, len(in))
	for i, n := range in {
		out[i] = nameKeyV7(n)
	}
	return out
}

// authorList reads an authors key, a JSON list of folded names.
func authorList(key string) []string {
	var names []string
	if json.Unmarshal([]byte(key), &names) != nil {
		return nil
	}
	return names
}

// authorsMatchV7 is the files' authors key against a record's under version
// 7: every file author is among the record's by normalised name, or the lists
// are consistent (either may hold up to two more names).
func authorsMatchV7(fileKey, recordKey string) bool {
	file, record := authorList(fileKey), authorList(recordKey)
	if len(file) == 0 || len(record) == 0 {
		return false
	}
	nr := normNames(record)
	if !slices.ContainsFunc(normNames(file), func(n string) bool { return !slices.Contains(nr, n) }) {
		return true
	}
	return authorsConsistentV7(file, record)
}

// titleMatchesV7 is titleMatches under version 7's title equivalence.
func titleMatchesV7(r evidence.SourceRecord, titleKey, lang string) bool {
	title := recordKey(r, fieldTitle)
	if !title.Valid {
		return false
	}
	if titlesEquivalentV7(title.Value, titleKey, lang) {
		return true
	}
	if full := r.Title + ": " + r.Subtitle; r.Subtitle != "" && !atCap(full) {
		k := findings.TitleKey(full)
		return k.Valid && titlesEquivalentV7(k.Value, titleKey, lang)
	}
	return false
}

// recordMatchesV7 is version 7's match of a record against the files. It
// accepts everything version 5's does, and also a title and authors that
// agree under version 7's tolerances.
func recordMatchesV7(r evidence.SourceRecord, want map[string]string, otherLanguage bool, lang string) bool {
	title, authors := recordKey(r, fieldTitle), recordKey(r, fieldAuthors)
	if !title.Valid || !authors.Valid {
		return false
	}
	if !titleMatches(r, want[fieldTitle]) && !titleMatchesV7(r, want[fieldTitle], lang) {
		return false
	}
	if !authorsCovered(want[fieldAuthors], authors.Value) && !authorsMatchV7(want[fieldAuthors], authors.Value) {
		return false
	}
	return languagesAgree(r, want, otherLanguage)
}

// titleLang is the one language whose articles and conjunction apply to the
// book's titles: the files' single language, else the records' single
// language, else none (no article is folded).
func (v *view) titleLang() string {
	keys := v.fileKeys(fieldLanguages)
	if len(keys) == 0 {
		for _, rt := range v.roots {
			for _, k := range rt.keys(fieldLanguages) {
				if !slices.Contains(keys, k) {
					keys = append(keys, k)
				}
			}
		}
	}
	if len(keys) != 1 {
		return ""
	}
	var bases []string
	if json.Unmarshal([]byte(keys[0]), &bases) != nil || len(bases) != 1 {
		return ""
	}
	return bases[0]
}

// keysConsistent reports whether every pair of the distinct keys that
// records state for a field is the same under version 7's tolerances. Only
// titles and authors have any; languages never are.
func (v *view) keysConsistent(field string, keys []string) bool {
	lang := v.titleLang()
	for i, a := range keys {
		for _, b := range keys[i+1:] {
			switch field {
			case fieldTitle:
				if !titlesEquivalentV7(a, b, lang) {
					return false
				}
			case fieldAuthors:
				if !authorsConsistentV7(authorList(a), authorList(b)) {
					return false
				}
			default:
				return false
			}
		}
	}
	return true
}
