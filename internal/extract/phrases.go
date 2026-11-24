package extract

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The closed phrase tables of design section 5.2: role phrases, year labels
// and edition statements, in English, German, Spanish and French. A table is
// one case-insensitive regular expression whose named groups are the kinds;
// scan finds its phrases left to right, longest alternative first, and never
// starts a phrase inside another (so the "by" of "translated by" is not an
// author's "by").

// phraseHit is a phrase found at byte offsets [start, end) of the scanned
// text, and its kind: the name of the group that matched.
type phraseHit struct {
	start, end int
	kind       string
}

type phraseTable struct {
	anchored *regexp.Regexp
	groups   []string
}

func newPhraseTable(alts map[string]string, order []string) *phraseTable {
	var parts []string
	for _, k := range order {
		parts = append(parts, "(?P<"+k+">"+alts[k]+")")
	}
	return &phraseTable{
		anchored: regexp.MustCompile(`(?i)^(?:` + strings.Join(parts, "|") + `)`),
		groups:   order,
	}
}

// scan returns the phrases of the table in s. A phrase that starts or ends
// with a letter or digit must not touch another letter or digit.
func (t *phraseTable) scan(s string) []phraseHit {
	var out []phraseHit
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if i > 0 && isWordRune(lastRune(s[:i])) && isWordRune(r) {
			i += size
			continue
		}
		m := t.anchored.FindStringSubmatchIndex(s[i:])
		if m == nil {
			i += size
			continue
		}
		end := i + m[1]
		last := lastRune(s[:end])
		if isWordRune(last) && end < len(s) {
			if next, _ := utf8.DecodeRuneInString(s[end:]); isWordRune(next) {
				i += size
				continue
			}
		}
		kind := ""
		for k, name := range t.groups {
			if m[2*(k+1)] >= 0 {
				kind = name
			}
		}
		out = append(out, phraseHit{i, end, kind})
		i = end
	}
	return out
}

func lastRune(s string) rune {
	r, _ := utf8.DecodeLastRuneInString(s)
	return r
}

const lang = `[\p{L}\-]+(?: [\p{L}\-]+)?` // a language name, one or two words

// roles maps role phrases to roles. Quotes are already folded to ' and ".
var roles = newPhraseTable(map[string]string{
	RoleTranslator: `translated from (?:the )?` + lang + ` by|translated by|` +
		`übersetzt aus dem ` + lang + ` von|übersetzt von|aus dem ` + lang + ` von|` +
		`traducci[oó]n (?:del ` + lang + ` (?:de|por)|de|por)|traducido (?:del ` + lang + ` )?por|` +
		`traduit (?:de |du |d')(?:l')?` + lang + ` par|traduit par`,
	RoleEditor:       `edited by|herausgegeben von|edici[oó]n de|[eé]dit[eé] par`,
	RoleIntroduction: `with an? (?:introduction|foreword) by|(?:introduction|foreword) by`,
	RoleIllustrator:  `(?:with )?illustrat(?:ed|ions) by`,
	RoleAuthor:       `written by|by`,
}, []string{RoleTranslator, RoleEditor, RoleIntroduction, RoleIllustrator, RoleAuthor})

// yearLabels maps the closed set of labels a year may follow to what the year
// is the year of. A bare "Published" is read as this edition's: on a copyright
// page it names the edition in hand, and "First published" and the like are
// matched first and win.
var yearLabels = newPhraseTable(map[string]string{
	LabelFirstPublished: `first published|first publication|originally published|erstausgabe|erstmals erschienen|` +
		`erstver[oö]ffentlicht|erste auflage|primera edici[oó]n|primera publicaci[oó]n|premi[eè]re [eé]dition|premi[eè]re publication`,
	LabelThisEdition: `this edition(?: first)? published|published in this edition|this (?:e)?book edition|this edition|` +
		`esta edici[oó]n|diese ausgabe|neuausgabe|published|publicado|erschienen|publi[eé]`,
	LabelCopyright: `©|\(c\)|copyright|urheberrecht|derechos reservados`,
}, []string{LabelFirstPublished, LabelThisEdition, LabelCopyright})

// editions is the closed edition-statement grammar. Every match is a whole
// statement; the kind is always "edition".
var editions = newPhraseTable(map[string]string{
	"edition": `(?:first|second|third|fourth|fifth|sixth|seventh|eighth|ninth|tenth|\d{1,2}(?:st|nd|rd|th))` +
		`(?: (?:revised|updated|expanded|enlarged|illustrated|anniversary|international|new))* edition|` +
		`(?:revised|updated|expanded|enlarged|illustrated|abridged|unabridged|anniversary|collector's|special|deluxe|limited|international|new|centenary)` +
		`(?: and (?:revised|updated|expanded|enlarged))? edition|` +
		`(?:erste|zweite|dritte|vierte|fünfte|sechste|siebte|siebente|achte|neunte|zehnte|\d{1,2}\.)` +
		`(?:,? (?:überarbeitete|erweiterte|aktualisierte|verbesserte|neu bearbeitete))* auflage|` +
		`(?:überarbeitete|erweiterte|aktualisierte|verbesserte|neu bearbeitete)(?: und (?:erweiterte|aktualisierte|überarbeitete))? auflage|` +
		`neuausgabe|jubiläumsausgabe|` +
		`(?:primera|segunda|tercera|cuarta|quinta|sexta|s[eé]ptima|octava|novena|d[eé]cima|\d{1,2}\.?[ªa]) edici[oó]n` +
		`(?: (?:revisada|ampliada|actualizada|corregida|aumentada))*|` +
		`edici[oó]n (?:revisada|ampliada|actualizada|corregida|aumentada|aniversario|conmemorativa)|` +
		`(?:premi[eè]re|deuxi[eè]me|seconde|troisi[eè]me|nouvelle) [eé]dition` +
		`(?: (?:revue|augment[eé]e|corrig[eé]e|mise [àa] jour))*`,
}, []string{"edition"})

// sentenceEnd matches the end of a sentence: a word of three letters or more
// and a stop. Initials and suffixes ("J.", "Jr.") are shorter.
var sentenceEnd = regexp.MustCompile(`\p{L}{3,}[.!?](?: |$)`)

// ---- numbers -------------------------------------------------------------

var numberWords = map[string]string{
	"one": "1", "two": "2", "three": "3", "four": "4", "five": "5", "six": "6",
	"seven": "7", "eight": "8", "nine": "9", "ten": "10", "eleven": "11", "twelve": "12",
	"ein": "1", "eins": "1", "eine": "1", "zwei": "2", "drei": "3", "vier": "4", "fünf": "5", "fuenf": "5",
	"sechs": "6", "sieben": "7", "acht": "8", "neun": "9", "zehn": "10", "elf": "11", "zwölf": "12", "zwoelf": "12",
	"uno": "1", "una": "1", "dos": "2", "tres": "3", "cuatro": "4", "cinco": "5", "seis": "6",
	"siete": "7", "ocho": "8", "nueve": "9", "diez": "10", "once": "11", "doce": "12",
}

var ordinalWords = map[string]string{
	"first": "1", "second": "2", "third": "3", "fourth": "4", "fifth": "5", "sixth": "6",
	"seventh": "7", "eighth": "8", "ninth": "9", "tenth": "10", "eleventh": "11", "twelfth": "12",
}

func alternation(m map[string]string) string {
	var ks []string
	for k := range m {
		ks = append(ks, regexp.QuoteMeta(k))
	}
	// Longest first, so "eins" wins over "ein" and "fuenf" is not cut short.
	for i := range ks {
		for j := i + 1; j < len(ks); j++ {
			if len(ks[j]) > len(ks[i]) {
				ks[i], ks[j] = ks[j], ks[i]
			}
		}
	}
	return strings.Join(ks, "|")
}

// canonDecimal returns a plain decimal ("2", "2.5") in its shortest form:
// no leading zeros, no trailing fractional zeros. It refuses anything else.
func canonDecimal(s string) (string, bool) {
	whole, frac, _ := strings.Cut(strings.TrimSpace(s), ".")
	if whole == "" || len(whole) > 3 || len(frac) > 2 || !allDigits(whole) || (frac != "" && !allDigits(frac)) {
		return "", false
	}
	whole = strings.TrimLeft(whole, "0")
	if whole == "" {
		whole = "0"
	}
	if frac = strings.TrimRight(frac, "0"); frac != "" {
		whole += "." + frac
	}
	return whole, true
}

func allDigits(s string) bool {
	return s != "" && strings.IndexFunc(s, func(r rune) bool { return r < '0' || r > '9' }) < 0
}

// indexOfNumber reads a series index: a decimal, or a number word up to twelve.
func indexOfNumber(s string) (string, bool) {
	if v, ok := canonDecimal(s); ok {
		return v, true
	}
	v, ok := numberWords[strings.ToLower(s)]
	return v, ok
}

// ---- series --------------------------------------------------------------

var (
	num    = `(\d{1,3}(?:\.\d{1,2})?|` + alternation(numberWords) + `)`
	ord    = `(\d{1,3}(?:st|nd|rd|th)|` + alternation(ordinalWords) + `)`
	kind   = `(?:book|volume|vol\.?|part|band|bd\.?|libro|tomo|livre|tome)`
	artDet = `(?:the |der |die |das |la |el |le |les |l')?`
	tail   = `(?: series| reihe| serie| série)?`

	// "Book 2 of the X", "Volume 3 in the X series", "Band 3 der X-Reihe".
	reSeriesOf = regexp.MustCompile(`(?i)^` + kind + ` ` + num + ` (?:of|in|von|de|der|des|dans|en) ` + artDet + `(.+?)` + tail + `$`)
	// "Third book in the X", "#3 in the X series": an ordinal or a hash needs
	// a noun ("book", "novel") or the series word, or "#1 in the UK" would be
	// a series.
	reSeriesNth = regexp.MustCompile(`(?i)^(?:the )?(?:` + ord + `|#(\d{1,3}(?:\.\d{1,2})?)) ` +
		`(?:(book|novel|volume|installment|entry) (?:in|of)|in) ` + artDet + `(.+?)( series| reihe| serie| série)?$`)
	// "X, Book 2", "X Band 3", "X #2.5".
	reSeriesAfter = regexp.MustCompile(`(?i)^(.+?)[,:;]? (?:` + kind + ` ` + num + `|#(\d{1,3}(?:\.\d{1,2})?))$`)
)

// genericSeries are heads that name no series: "Book 2 of the Trilogy",
// "First in the series". Lower case, after any article.
var genericSeries = map[string]bool{
	"series": true, "novels": true, "novel": true, "books": true, "book": true, "set": true, "box set": true,
	"collection": true, "volumes": true, "edition": true, "trilogy": true, "saga": true, "cycle": true,
	"sequence": true, "quartet": true, "duology": true, "reihe": true, "serie": true, "série": true,
}

// seriesName checks a captured series name: it starts with a letter, is no
// number or ordinal word, no indefinite ("a box set"), no generic head, and
// holds no format character.
func seriesName(raw string) (string, bool) {
	name := trimTitle(raw)
	r, _ := utf8.DecodeRuneInString(name)
	if name == "" || !unicode.IsLetter(r) || hasFormatChar(name) {
		return "", false
	}
	l := strings.ToLower(name)
	first := strings.Fields(l)[0]
	if _, ok := numberWords[first]; ok {
		return "", false
	}
	if _, ok := ordinalWords[first]; ok || first == "a" || first == "an" {
		return "", false
	}
	for _, art := range []string{"the ", "der ", "die ", "das ", "la ", "el ", "le ", "les "} {
		l = strings.TrimPrefix(l, art)
	}
	if genericSeries[l] {
		return "", false
	}
	return name, true
}

// seriesStatement is what a series line prints.
type seriesStatement struct {
	name, index string
}

// parseSeries reads a quote against the closed series grammar. The whole
// quote must be one statement, and it must name the series: an index alone
// ("Book 2", "#1") grounds nothing, since it could be anything's number.
func parseSeries(s string) (seriesStatement, bool) {
	s = strings.TrimSpace(strings.TrimRight(strings.TrimSpace(s), ".,;:"))
	if m := reSeriesOf.FindStringSubmatch(s); m != nil {
		if idx, ok := indexOfNumber(m[1]); ok {
			if name, ok := seriesName(m[2]); ok {
				return seriesStatement{name, idx}, true
			}
		}
	}
	if m := reSeriesNth.FindStringSubmatch(s); m != nil && (m[3] != "" || m[5] != "") {
		idx, ok := "", false
		switch {
		case m[1] != "":
			idx, ok = ordinalIndex(m[1])
		case m[2] != "":
			idx, ok = canonDecimal(m[2])
		}
		if name, nameOK := seriesName(m[4]); ok && nameOK {
			return seriesStatement{name, idx}, true
		}
	}
	if m := reSeriesAfter.FindStringSubmatch(s); m != nil {
		idx, ok := "", false
		if m[2] != "" {
			idx, ok = indexOfNumber(m[2])
		} else if m[3] != "" {
			idx, ok = canonDecimal(m[3])
		}
		if name, nameOK := seriesName(m[1]); ok && nameOK {
			return seriesStatement{name, idx}, true
		}
	}
	return seriesStatement{}, false
}

func ordinalIndex(s string) (string, bool) {
	if v, ok := ordinalWords[strings.ToLower(s)]; ok {
		return v, true
	}
	return canonDecimal(strings.TrimRightFunc(s, unicode.IsLetter))
}

func hasLetter(s string) bool { return letterCount(s) > 0 }
