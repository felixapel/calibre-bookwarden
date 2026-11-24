package decide

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/findings"
)

// recordMatchesV5 is version 5's match of a record against the files' title,
// authors and languages keys (ADR-030). The record must state a title and
// authors. It matches when:
//   - its title equals the files', or its title and its own subtitle field
//     joined by ": " do, after both drop one trailing parenthetical that
//     names no volume, a leading "the", "a" or "an", and read "&" as "and";
//   - every file author is one of its authors (it may list translators or
//     editors too), compared by the exact folded name;
//   - its languages equal the files', or it states none and no other
//     identity record states a different language (otherLanguage).
func recordMatchesV5(r evidence.SourceRecord, want map[string]string, otherLanguage bool) bool {
	title, authors := recordKey(r, fieldTitle), recordKey(r, fieldAuthors)
	if !title.Valid || !authors.Valid {
		return false
	}
	if !titleMatches(r, want[fieldTitle]) || !authorsCovered(want[fieldAuthors], authors.Value) {
		return false
	}
	return languagesAgree(r, want, otherLanguage)
}

// languagesAgree is ADR-030's language rule: the record's languages equal the
// files', or it states none and no identity record states another.
func languagesAgree(r evidence.SourceRecord, want map[string]string, otherLanguage bool) bool {
	if len(r.Languages) == 0 {
		return !otherLanguage // a record that states no language contradicts none
	}
	langs := recordKey(r, fieldLanguages)
	return langs.Valid && langs.Value == want[fieldLanguages]
}

// titleMatches is ADR-030's title rule: the record's title, or its title and
// subtitle joined by ": ", equals titleKey after canonTitle.
func titleMatches(r evidence.SourceRecord, titleKey string) bool {
	title := recordKey(r, fieldTitle)
	if !title.Valid {
		return false
	}
	file := canonTitle(titleKey)
	if canonTitle(title.Value) == file {
		return true
	}
	if full := r.Title + ": " + r.Subtitle; r.Subtitle != "" && !atCap(full) {
		k := findings.TitleKey(full)
		return k.Valid && canonTitle(k.Value) == file
	}
	return false
}

// volume matches a parenthetical that may name a volume, part or book of a
// set: a volume word or abbreviation, "#" or "№", a roman numeral or a
// number of up to three digits (a four-digit year is no volume). Dropping it
// could match one volume against another. It errs toward keeping a
// parenthetical, which only makes the match stricter.
var volume = regexp.MustCompile(`(?i)\b(vols?|volumes?|volumen|books?|parts?|pts?|band|bd|t|tom|tomes?|tomos?|teil|deel|livres?|libros?|no|nos|nr)\b|#|№|^[ivxlcdm]+$|^\d{1,3}$`)

// canonTitle reads a folded title key without one trailing, unnested
// parenthetical that names no volume, without a leading English article, and
// with "&" as "and".
func canonTitle(key string) string {
	t := strings.TrimSpace(key)
	if strings.HasSuffix(t, ")") {
		if i := strings.LastIndex(t, " ("); i > 0 {
			inner := strings.TrimSpace(t[i+2 : len(t)-1])
			if !strings.ContainsAny(inner, "()") && !volume.MatchString(inner) {
				t = strings.TrimSpace(t[:i])
			}
		}
	}
	for _, article := range []string{"the ", "a ", "an "} {
		if rest, ok := strings.CutPrefix(t, article); ok && rest != "" {
			t = rest
			break
		}
	}
	return strings.ReplaceAll(t, " & ", " and ")
}

// authorsCovered reports whether every author in the files' authors key is in
// the record's authors key; both keys are JSON lists of folded names.
func authorsCovered(fileKey, recordKey string) bool {
	if fileKey == recordKey {
		return true
	}
	var file, record []string
	if json.Unmarshal([]byte(fileKey), &file) != nil || json.Unmarshal([]byte(recordKey), &record) != nil || len(file) == 0 {
		return false
	}
	for _, name := range file {
		if !slices.Contains(record, name) {
			return false
		}
	}
	return true
}
