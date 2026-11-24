package decide

import (
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/findings"
)

// Decision version 10 (ADR-045): corrections to the text root. Part 1: author
// dissent checks consistency too, a text-alone title or author list in
// capitals is not proposed as printed, and a pubdate Calibre would shift on
// write is not proposed. Part 2: a record speaks about the title and authors
// only when it is the same book in the same language, three subtitle rules,
// and a publisher in capitals is treated as a title is.

// authorsDissent reports whether a root's author list dissents from the
// text's in the text-alone path (ADR-036 section 3.3). It does when the text
// doesn't cover the root's names; from version 10 also when the two lists are
// not the same people under rule 1 (authorsConsistentV7), such as a printed
// list that puts others before the root's first author.
func (v *view) authorsDissent(root, text []string) bool {
	if !coveredBy(root, text) {
		return true
	}
	return v.version >= 10 && !authorsConsistentV7(root, text)
}

// inCapitals reports whether a value has an uppercase letter and no lowercase
// one. Digits alone, and scripts without case, are not capitals.
func inCapitals(values []string) bool {
	s := strings.Join(values, " ")
	return strings.ContainsFunc(s, unicode.IsUpper) && !strings.ContainsFunc(s, unicode.IsLower)
}

// capitalsDisplay is the text-alone display of a title, author list or
// publisher printed
// in capitals from version 10: the spelling of another root that states the
// same key and is not in capitals itself, or nil when none does, and then
// nothing is proposed. ok is false when the rule doesn't apply.
func (v *view) capitalsDisplay(field string, st textStatement) (shown []string, ok bool) {
	if v.version < 10 || (field != fieldTitle && field != fieldAuthors && field != fieldPublisher) || !inCapitals(st.display) {
		return nil, false
	}
	for _, s := range v.statements(field) {
		if len(s.keys) == 1 && s.keys[0] == st.key {
			if shown := v.display(field, st.key, s.name); len(shown) > 0 && !inCapitals(shown) {
				return shown, true
			}
		}
	}
	return nil, true
}

// pubdateUnwritable reports whether, from version 10, proposing key as the
// pubdate is pointless because Calibre would store another day.
func (v *view) pubdateUnwritable(field, key string) bool {
	if v.version < 10 || field != fieldPubdate {
		return false
	}
	t, err := time.Parse(time.DateOnly, key)
	return err == nil && findings.ShiftedDay(t)
}

// recordSpeaks reports whether a record may support or dissent on field. From
// version 10 a record speaks about the title and authors only when it is the
// same book in the same language: a language it states is the files', and
// every file author is among its authors (rule 4). Each check needs the files
// to state one key for it; a record that states no language passes the first.
func (v *view) recordSpeaks(r evidence.SourceRecord, field string) bool {
	if v.version < 10 || (field != fieldTitle && field != fieldAuthors) {
		return true
	}
	if langs := v.fileKeys(fieldLanguages); len(langs) == 1 && len(r.Languages) > 0 &&
		!languagesAgree(r, map[string]string{fieldLanguages: langs[0]}, false) {
		return false
	}
	if authors := v.fileKeys(fieldAuthors); len(authors) == 1 {
		k := recordKey(r, fieldAuthors)
		return k.Valid && authorsCovered(authors[0], k.Value)
	}
	return true
}

// rootKeys are the distinct valid keys a root's records state for field,
// leaving out records that don't speak about it. In the text path (textKey
// set) from version 10, a record that keeps its subtitle in its own field
// states its title joined with it when that is the text's title (rule 5).
func (v *view) rootKeys(rt *root, field, textKey string) []string {
	var keys []string
	for _, r := range rt.records {
		if !v.recordSpeaks(r, field) {
			continue
		}
		k := recordKey(r, field)
		if v.version >= 10 && field == fieldTitle && textKey != "" && r.Subtitle != "" {
			if full := joinSubtitle(r.Title, r.Subtitle, v.version); !atCap(full) {
				if fk := findings.TitleKey(full); fk.Valid && fk.Value == textKey {
					k = fk
				}
			}
		}
		if k.Valid && !slices.Contains(keys, k.Value) {
			keys = append(keys, k.Value)
		}
	}
	return keys
}

// joinedTitleDisplay is the display of a record whose title joined with its
// own subtitle field has key, from version 10 (rule 5); nil otherwise.
func (v *view) joinedTitleDisplay(rootName, field, key string) []string {
	if v.version < 10 || field != fieldTitle || v.roots[rootName] == nil {
		return nil
	}
	for _, r := range v.roots[rootName].records {
		if full := joinSubtitle(r.Title, r.Subtitle, v.version); r.Subtitle != "" && !atCap(full) {
			if k := findings.TitleKey(full); k.Valid && k.Value == key {
				return []string{full}
			}
		}
	}
	return nil
}

// textStatements are statements for the text path: rootKeys with the text's
// key.
func (v *view) textStatements(field, textKey string) []statement {
	out := []statement{{name: evidence.RootFile, class: classInternal, keys: v.fileKeys(field)}}
	for _, rt := range v.roots {
		out = append(out, statement{name: rt.name, class: rt.class, keys: v.rootKeys(rt, field, textKey)})
	}
	slices.SortFunc(out, func(a, b statement) int { return rank(a.name) - rank(b.name) })
	return out
}

// recordPathShortens is the record path's title guard: from version 10 a
// title shortens only by its noise, as in the text path (rule 5).
func (v *view) recordPathShortens(calibre, proposed string) bool {
	if v.version >= 10 {
		return shortensV8(calibre, proposed)
	}
	return shortens(calibre, proposed)
}

// joinSubtitle joins a printed title and subtitle with ": ", and from version
// 10 with one space after a title that ends in ":", "?" or "!" (rule 5).
func joinSubtitle(title, subtitle string, version int) string {
	if version >= 10 && (strings.HasSuffix(title, ":") || strings.HasSuffix(title, "?") || strings.HasSuffix(title, "!")) {
		return title + " " + subtitle
	}
	return title + ": " + subtitle
}
