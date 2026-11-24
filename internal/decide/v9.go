package decide

import (
	"slices"
	"strings"
	"time"
	"unicode"
)

// Decision version 9 (ADR-036, "Decision v9"). Both rules only change what a
// proposal shows or drop a proposal that changes nothing.

// The UTC offsets of the world run from -12h to +14h.
const (
	westmostOffset = -12 * time.Hour
	eastmostOffset = 14 * time.Hour
)

// sameLocalDate reports whether key, a "YYYY-MM-DD" date, is a date Calibre's
// timestamp can mean: the local midnight of key, read as UTC, lies from 12h
// before to 14h after the timestamp. Calibre stores a date picked at local
// midnight as UTC, so 2005-03-24T23:00Z is the 25th (+1h). The timestamp's own
// UTC date also counts, for a timestamp written with an offset. A value that
// isn't a full timestamp is exact and never matches loosely.
func sameLocalDate(calibrePubDate, key string) bool {
	t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(calibrePubDate))
	if err != nil {
		return false
	}
	t = t.UTC()
	d, err := time.Parse(time.DateOnly, key)
	if err != nil {
		return false
	}
	delta := d.Sub(t)
	return delta >= westmostOffset && delta <= eastmostOffset || key == t.Format(time.DateOnly)
}

// pubdateIsZoneShift reports whether proposing key would only restate
// Calibre's own pubdate in another time zone.
func (v *view) pubdateIsZoneShift(field, key string) bool {
	return v.version >= 9 && field == fieldPubdate && sameLocalDate(v.p.Calibre.PubDate, key)
}

// textDisplay is the value a text-rooted proposal shows. From version 9 a
// publisher takes the spelling of another root that states the same key, else
// it is title-cased when it was printed without a lowercase letter.
// From version 10, a title, author list or publisher in capitals follows
// capitalsDisplay.
func (v *view) textDisplay(field string, st textStatement) []string {
	if shown, ok := v.capitalsDisplay(field, st); ok {
		return shown
	}
	if v.version < 9 || field != fieldPublisher || len(st.display) != 1 {
		return st.display
	}
	if shown := v.otherSpelling(field, st.key); shown != nil {
		return shown
	}
	return []string{titleCasePublisher(st.display[0])}
}

// otherSpelling is the display of the first root that states key for the
// field alone, or nil.
func (v *view) otherSpelling(field, key string) []string {
	for _, s := range v.statements(field) {
		if len(s.keys) == 1 && s.keys[0] == key {
			if shown := v.display(field, key, s.name); len(shown) > 0 {
				return shown
			}
		}
	}
	return nil
}

var smallWords = []string{
	"of", "and", "the", "for", "on", "in", "at", "to", "a", "an",
	"de", "la", "las", "los", "del", "el", "y", "e", "du", "des", "et", "le", "les",
	"der", "die", "das", "und", "von", "zu", "di", "da", "il",
}

// corporateForms are the suffixes written in a fixed case; only the case
// changes, never the punctuation around them.
var corporateForms = map[string]string{
	"inc": "Inc", "ltd": "Ltd", "llc": "LLC", "co": "Co", "gmbh": "GmbH", "ag": "AG", "kg": "KG",
}

// titleCasePublisher title-cases a publisher printed in capitals word by word.
// A value with any lowercase letter is returned as printed. It changes case
// only: the result has the same key as the printed value.
func titleCasePublisher(s string) string {
	if strings.ContainsFunc(s, unicode.IsLower) {
		return s
	}
	words := strings.Fields(s)
	for i, w := range words {
		words[i] = titleCaseWord(w, i == 0)
	}
	return strings.Join(words, " ")
}

func titleCaseWord(w string, first bool) string {
	bare := strings.TrimRight(w, ",;")
	tail := w[len(bare):]
	if strings.ContainsAny(bare, "()") {
		return w // a bracketed acronym such as (USA), as printed
	}
	word := strings.TrimRight(bare, ".")
	dots := bare[len(word):]
	if form, ok := corporateForms[strings.ToLower(word)]; ok {
		return form + dots + tail
	}
	if strings.Contains(bare, ".") {
		parts := strings.Split(word, ".")
		if !slices.ContainsFunc(parts, func(p string) bool { return letterCount(p) > 1 }) {
			return w // initials such as A., U.S., C.H.
		}
		for i, p := range parts {
			parts[i] = caseRunes(p)
		}
		return strings.Join(parts, ".") + dots + tail
	}
	if lower := strings.ToLower(word); !first && dots == "" && slices.Contains(smallWords, lower) {
		return lower + tail
	}
	parts := strings.Split(word, "-")
	for i, p := range parts {
		if !strings.ContainsFunc(p, isVowel) {
			continue // an acronym such as DK, CRC or BBC
		}
		parts[i] = caseRunes(p)
	}
	return strings.Join(parts, "-") + dots + tail
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

func isVowel(r rune) bool {
	return strings.ContainsRune("aeiouyáéíóúàèìòùâêîôûäëïöüåæøœýÿ", unicode.ToLower(r))
}

// caseRunes upper-cases the first letter and lower-cases the rest, and also
// upper-cases the letter after an apostrophe that follows a single letter
// (O'Reilly, L'École), but not in Martin's.
func caseRunes(s string) string {
	var out []rune
	up, letters := true, 0
	for _, r := range s {
		switch {
		case unicode.IsLetter(r):
			if up {
				r = unicode.ToUpper(r)
			} else {
				r = unicode.ToLower(r)
			}
			up = false
			letters++
		case r == '\'' || r == '’':
			up = letters == 1
		}
		out = append(out, r)
	}
	return string(out)
}
