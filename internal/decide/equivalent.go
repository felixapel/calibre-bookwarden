package decide

import (
	"regexp"
	"strings"

	"golang.org/x/text/language"

	"github.com/felixapel/calibre-bookwarden/internal/findings"
)

var yearPattern = regexp.MustCompile(`\b(\d{4})\b`)

// SameValue reports whether a value somebody typed and a proposed value are
// the same under ADR-036 section 3.1's equivalences, the ones the text root
// uses to say "not a correction":
//   - title: titlesEquivalentV8, folding the articles of lang (a Calibre
//     language code, "eng" or "en"; empty folds none);
//   - authors: authorsConsistentV7 on the typed names, split at "&", ";" or
//     a line break, against the proposed names;
//   - publisher: the same imprint up to case, punctuation, "&" and a
//     corporate suffix;
//   - pubdate: the same year;
//   - isbn: the same ISBN-13;
//   - languages: the same set of language codes.
//
// It is read-only and changes no decision. An unknown field, or a proposal
// that isn't one value where the field has one, is never the same.
func SameValue(field, typed string, proposed []string, lang string) bool {
	typed = strings.TrimSpace(typed)
	if typed == "" || len(proposed) == 0 {
		return false
	}
	switch field {
	case fieldTitle:
		if len(proposed) != 1 {
			return false
		}
		// The equivalences compare folded keys, as the decision does.
		a, b := findings.TitleKey(typed), findings.TitleKey(proposed[0])
		return a.Valid && b.Valid && titlesEquivalentV8(a.Value, b.Value, titleBase(lang))
	case fieldAuthors:
		names := strings.FieldsFunc(typed, func(r rune) bool { return r == '&' || r == ';' || r == '\n' })
		for i := range names {
			names[i] = strings.TrimSpace(names[i])
		}
		a, b := findings.AuthorsKey(names), findings.AuthorsKey(proposed)
		return a.Valid && b.Valid && authorsConsistentV7(authorList(a.Value), authorList(b.Value))
	case fieldPublisher:
		return len(proposed) == 1 && publisherEquivalent(typed, proposed[0])
	case fieldPubdate:
		if len(proposed) != 1 {
			return false
		}
		a, b := yearPattern.FindString(typed), yearPattern.FindString(proposed[0])
		return a != "" && a == b
	case fieldISBN:
		if len(proposed) != 1 {
			return false
		}
		a, ok := findings.CanonicalISBN(typed)
		b, ok2 := findings.CanonicalISBN(proposed[0])
		return ok && ok2 && a == b
	case fieldLanguages:
		return sameLanguages(strings.FieldsFunc(typed, func(r rune) bool { return r == ',' || r == ';' || r == ' ' }), proposed)
	}
	return false
}

// titleBase is the two-letter base the article sets are keyed by, or "".
func titleBase(code string) string {
	b, err := language.ParseBase(strings.TrimSpace(code))
	if err != nil {
		return ""
	}
	return b.String()
}

// sameLanguages compares two lists of language codes as sets, by ISO 639-3.
func sameLanguages(a, b []string) bool {
	norm := func(in []string) map[string]bool {
		out := map[string]bool{}
		for _, c := range in {
			base, err := language.ParseBase(strings.TrimSpace(c))
			if err != nil {
				return nil
			}
			out[base.ISO3()] = true
		}
		return out
	}
	na, nb := norm(a), norm(b)
	if len(na) == 0 || len(na) != len(nb) {
		return false
	}
	for c := range na {
		if !nb[c] {
			return false
		}
	}
	return true
}
