package findings

import (
	"strings"
	"unicode"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

// These tables and the algorithm are pinned to the legacy authority.py at
// ac7a90cafbc2d250bb6057dd348dae2d617c94ca. They are observations, not fix authority.
var corporateEntities = map[string]string{
	"the economist": "Economist, The", "the new yorker": "New Yorker, The", "financial times": "Financial Times",
	"der spiegel": "Spiegel, Der", "spiegel online": "Spiegel Online", "the wall street journal": "Wall Street Journal, The",
	"nature": "Nature", "lonely planet": "Lonely Planet", "harvard business review": "Harvard Business Review",
}
var compoundSurnames = map[string]string{"mario vargas llosa": "Vargas Llosa", "gabriel garcia marquez": "García Márquez"}
var nobleParticles = map[string]bool{
	"von": true, "van": true, "de": true, "del": true, "della": true, "da": true, "di": true, "du": true, "des": true, "der": true, "den": true,
	"el": true, "al": true, "le": true, "la": true, "fitz": true, "mac": true, "mc": true,
}
var multiwordParticles = map[string]bool{"de la": true, "de las": true, "de los": true, "van der": true}
var generationalSuffixes = map[string]bool{"jr": true, "jr.": true, "sr": true, "sr.": true, "ii": true, "iii": true, "iv": true, "v": true, "ph.d.": true, "phd": true, "md": true}

func authorSort(name string) string {
	n := strings.TrimFunc(name, legacyWhitespace)
	if n == "" {
		return ""
	}
	if corporate, ok := corporateEntities[strings.ToLower(n)]; ok {
		return corporate
	}
	if strings.Contains(n, ",") {
		var parts []string
		for _, part := range strings.Split(n, ",") {
			if s := strings.TrimFunc(part, legacyWhitespace); s != "" {
				parts = append(parts, s)
			}
		}
		if len(parts) == 2 && generationalSuffixes[strings.ToLower(parts[1])] {
			return authorSort(parts[0]) + ", " + parts[1]
		}
		return n
	}
	for _, prefix := range []string{"Saint ", "St. ", "San "} {
		if strings.HasPrefix(n, prefix) {
			return strings.TrimLeftFunc(n[len(prefix):], legacyWhitespace) + ", Saint"
		}
	}
	for _, prefix := range []string{"Pope ", "Papa "} {
		if strings.HasPrefix(n, prefix) {
			return strings.TrimLeftFunc(n[len(prefix):], legacyWhitespace) + ", Pope"
		}
	}
	words := strings.FieldsFunc(n, legacyWhitespace)
	if len(words) == 1 {
		return words[0]
	}
	if surname, ok := compoundSurnames[authorityKey(n)]; ok {
		given := strings.Join(words[:len(words)-len(strings.Fields(surname))], " ")
		if given != "" {
			return surname + ", " + given
		}
	}
	for _, particleWords := range []int{2, 1} {
		if len(words) < particleWords+2 {
			continue
		}
		particle := cases.Fold().String(strings.Join(words[len(words)-particleWords-1:len(words)-1], " "))
		known := nobleParticles[particle]
		if particleWords == 2 {
			known = multiwordParticles[particle]
		}
		if known {
			return strings.Join(words[len(words)-particleWords-1:], " ") + ", " + strings.Join(words[:len(words)-particleWords-1], " ")
		}
	}
	return words[len(words)-1] + ", " + strings.Join(words[:len(words)-1], " ")
}
func authorityKey(name string) string {
	s := strings.Map(func(r rune) rune {
		if norm.NFKD.PropertiesString(string(r)).CCC() != 0 {
			return -1
		}
		return r
	}, norm.NFKD.String(name))
	return cases.Fold().String(strings.Join(strings.FieldsFunc(s, legacyWhitespace), " "))
}

// Python str.split/strip and re \s additionally accept these four separators.
func legacyWhitespace(r rune) bool { return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f) }
func authorSorts(authors []string) string {
	var sorts []string
	for _, name := range authors {
		if strings.TrimFunc(name, legacyWhitespace) != "" {
			sorts = append(sorts, authorSort(name))
		}
	}
	return strings.Join(sorts, " & ")
}
func (d *derivation) authorSortFinding(rec evidence.Record) {
	if listTooLong(rec.Authors) {
		return
	}
	if _, state := normalizeStrings(rec.Authors); state != fieldValid {
		return
	}
	got, state := normalizeField(rec.AuthorSort)
	if state == fieldMissing {
		d.add("author_sort_missing", "", "author_sort", "")
		return
	}
	if state == fieldTooLong {
		d.add("metadata_unverified", "", "author_sort", "field_too_long")
		return
	}
	want, ws := normalizeField(authorSorts(rec.Authors))
	if ws != fieldValid {
		d.add("metadata_unverified", "", "author_sort", "field_too_long")
		return
	}
	if got != want {
		d.add("author_sort_mismatch", "", "author_sort", "")
	}
}
