package findings

import (
	"maps"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/language"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

// titleArticles is Calibre 9.16's per_language_title_sort_articles tweak,
// verbatim. Python's \s is rewritten to pySpaceClass when compiled.
var titleArticles = map[string][]string{
	"eng": {`A\s+`, `The\s+`, `An\s+`},
	"epo": {`La\s+`, `L'`, `L´`},
	"spa": {`El\s+`, `La\s+`, `Lo\s+`, `Los\s+`, `Las\s+`, `Un\s+`, `Una\s+`, `Unos\s+`, `Unas\s+`},
	"fra": {`Le\s+`, `La\s+`, `L'`, `L´`, `L’`, `Les\s+`, `Un\s+`, `Une\s+`, `Des\s+`, `De\s+(La\s+)?`, `D'`, `D´`, `D’`},
	"pol": {},
	"ita": {`Lo\s+`, `Il\s+`, `L'`, `L´`, `La\s+`, `Gli\s+`, `I\s+`, `Le\s+`, `Uno\s+`, `Un\s+`, `Una\s+`, `Un'`, `Un´`,
		`Dei\s+`, `Degli\s+`, `Delle\s+`, `Del\s+`, `Della\s+`, `Dello\s+`, `Dell'`, `Dell´`},
	"por": {`A\s+`, `O\s+`, `Os\s+`, `As\s+`, `Um\s+`, `Uns\s+`, `Uma\s+`, `Umas\s+`},
	"ron": {`Un\s+`, `O\s+`, `Nişte\s+`},
	"deu": {`Der\s+`, `Die\s+`, `Das\s+`, `Den\s+`, `Ein\s+`, `Eine\s+`, `Einen\s+`, `Dem\s+`, `Des\s+`, `Einem\s+`, `Eines\s+`},
	"nld": {`De\s+`, `Het\s+`, `Een\s+`, `'n\s+`, `'s\s+`, `Ene\s+`, `Ener\s+`, `Enes\s+`, `Den\s+`, `Der\s+`, `Des\s+`, `'t\s+`},
	"swe": {`En\s+`, `Ett\s+`, `Det\s+`, `Den\s+`, `De\s+`},
	"tur": {`Bir\s+`},
	"afr": {`'n\s+`, `Die\s+`},
	"ell": {`O\s+`, `I\s+`, `To\s+`, `Ta\s+`, `Tus\s+`, `Tis\s+`, `'Enas\s+`, `'Mia\s+`, `'Ena\s+`, `'Enan\s+`},
	"hun": {`A\s+`, `Az\s+`, `Egy\s+`},
}

// pySpaceClass is what Python's \s matches in a str pattern.
const pySpaceClass = `[\t-\r\x{1C}-\x{20}\x{85}\p{Z}]`

// titlePatterns holds one compiled pattern per table language; nil never
// matches (Polish has no articles).
var titlePatterns = func() map[string]*regexp.Regexp {
	m := map[string]*regexp.Regexp{}
	for lang, pats := range titleArticles {
		if len(pats) == 0 {
			m[lang] = nil
			continue
		}
		alts := slices.Sorted(slices.Values(pats))
		m[lang] = regexp.MustCompile(`(?i)^(` + strings.ReplaceAll(strings.Join(alts, "|"), `\s`, pySpaceClass) + `)`)
	}
	return m
}()

// quotePairs is Calibre 9.16's quote_pairs: an opening quote and the closing
// quotes that pair with it.
var quotePairs = map[rune]string{
	'"': `"`, '\'': `'`, '“': `”“`, '”': `””`, '„': `”“`, '‚': `’‘`, '’': `’‘`, '‘': `’‘`,
	'‹': `›`, '›': `‹`, '《': `》`, '〈': `〉`, '»': `«»`, '«': `«»`, '「': `」`, '『': `』`,
}

// pySpace is Python's str.isspace.
func pySpace(r rune) bool { return unicode.IsSpace(r) || (r >= 0x1C && r <= 0x1F) }

// canonicalLang is Calibre's canonicalize_lang for codes: an ISO 639-1 code
// becomes ISO 639-3, a valid ISO 639-3 code stays, anything else is "".
func canonicalLang(raw string) string {
	raw = strings.ToLower(strings.TrimFunc(raw, pySpace))
	raw, _, _ = strings.Cut(strings.ReplaceAll(raw, "_", "-"), "-")
	raw, _, _ = strings.Cut(raw, "@")
	raw = strings.TrimFunc(raw, pySpace)
	b, err := language.ParseBase(raw)
	if err != nil {
		return ""
	}
	switch iso3 := b.ISO3(); {
	case len(raw) == 2:
		return iso3
	case len(raw) == 3 && iso3 == raw:
		return raw
	}
	return ""
}

// stripQuotes removes a leading quote and, if it pairs with it, the last
// character.
func stripQuotes(s string) string {
	q, n := utf8.DecodeRuneInString(s)
	closers, ok := quotePairs[q]
	if !ok || s == "" {
		return s
	}
	s = s[n:]
	if last, m := utf8.DecodeLastRuneInString(s); s != "" && strings.ContainsRune(closers, last) {
		s = s[:len(s)-m]
	}
	return s
}

// titleSort is Calibre 9.16's title_sort(title, lang=lang) with the default
// tweaks (library_order). An unknown language, or one without a table, uses
// English, as Calibre does.
func titleSort(title, lang string) string {
	pat, ok := titlePatterns[canonicalLang(lang)]
	if !ok {
		pat = titlePatterns["eng"]
	}
	return titleSortWith(title, pat)
}

func titleSortWith(title string, pat *regexp.Regexp) string {
	title = stripQuotes(strings.TrimFunc(title, pySpace))
	if pat != nil {
		if loc := pat.FindStringSubmatchIndex(title); loc != nil && loc[3] > loc[2] {
			prep := title[loc[2]:loc[3]]
			title = stripQuotes(title[len(prep):] + ", " + prep)
		}
	}
	return strings.TrimFunc(title, pySpace)
}

// tableLanguages lists the article table's languages in a fixed order.
var tableLanguages = slices.Sorted(maps.Keys(titleArticles))

// titleSortFinding compares Calibre's title sort with Calibre's own rule for
// the book's first language (ADR-018). A sort that another table language
// would produce is accepted: Calibre-Web and non-English Calibre installs
// use other article sets.
func (d *derivation) titleSortFinding(rec evidence.Record) {
	if code, reason, _ := titleSortState(rec); code != "" {
		d.add(code, "", "title_sort", reason)
	}
}

// TitleSortProposal returns Calibre's title sort for rec's title and first
// language, and whether rec's sort is one titleSortFinding flags as missing,
// with its article not moved, or wrong (ADR-028).
func TitleSortProposal(rec evidence.Record) (string, bool) {
	code, _, want := titleSortState(rec)
	if want == "" {
		return "", false
	}
	switch code {
	case "title_sort_missing", "title_sort_article_not_moved", "title_sort_mismatch":
		return want, true
	}
	return "", false
}

// titleSortState classifies rec's title sort: the finding code and reason
// ("" when the sort is fine or can't be judged) and the sort Calibre's rule
// gives.
func titleSortState(rec evidence.Record) (code, reason, want string) {
	if rec.TitleSort == nil {
		return "", "", ""
	}
	got, state := normalizeField(*rec.TitleSort)
	title, titleState := normalizeField(rec.Title)
	switch {
	case state == fieldTooLong || titleState == fieldTooLong:
		return "metadata_unverified", "field_too_long", ""
	case titleState != fieldValid:
		if state == fieldMissing {
			return "title_sort_missing", "", ""
		}
		return "", "", ""
	}
	lang := ""
	if len(rec.Languages) > 0 {
		lang = rec.Languages[0]
	}
	want = titleSort(rec.Title, lang)
	if state == fieldMissing {
		return "title_sort_missing", "", want
	}
	norm := func(s string) string { n, _ := normalizeField(s); return n }
	switch got {
	case norm(want):
		return "", "", want
	case title:
		return "title_sort_article_not_moved", "", want
	}
	for _, l := range tableLanguages {
		if got == norm(titleSortWith(rec.Title, titlePatterns[l])) {
			return "", "", want
		}
	}
	return "title_sort_mismatch", "", want
}
