package decide

import (
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/findings"
)

// Decision version 11 (ADR-048): a fix is proposed only when an undo could
// put Calibre's current value back exactly. Apply refuses any other as
// calibre_cannot_restore, so proposing it only fills the inbox with fixes
// nobody can approve.
//
// Restorable is a frozen copy of the fix package's restore rule for Calibre
// 9.16 (ADR-021, ADR-028) as it stood at version 11; a test in package fix
// keeps the two in step. A later change to fix's rule needs a new decision
// version, not an edit here.

// restorableOnly drops the proposals an undo couldn't restore.
func (v *view) restorableOnly(ps []evidence.Proposal) []evidence.Proposal {
	if v.version < 11 {
		return ps
	}
	return slices.DeleteFunc(ps, func(p evidence.Proposal) bool { return !Restorable(p.Field, v.p.Calibre) })
}

// Restorable reports whether an undo of a write to field could put rec's
// values back as Calibre stores them: every field the write touches, with a
// title its title sort and authors their author sort.
func Restorable(field string, rec evidence.Record) bool {
	switch field {
	case fieldTitle:
		return rec.Title != "" && keepsText(rec.Title) && (rec.TitleSort == nil || restorableSort(rec.TitleSort))
	case fieldTitleSort:
		return restorableSort(rec.TitleSort)
	case fieldAuthors:
		return keepsAuthors(rec.Authors) && keepsText(rec.AuthorSort)
	case fieldAuthorSort:
		return keepsText(rec.AuthorSort)
	case fieldPublisher:
		return keepsText(rec.Publisher)
	case fieldLanguages:
		return keepsLanguages(rec.Languages)
	case fieldISBN:
		return keepsIdentifiers(rec.Identifiers)
	case fieldPubdate:
		return keepsPubdate(rec.PubDate)
	}
	return false
}

func restorableSort(s *string) bool { return s != nil && *s != "" && keepsText(*s) }

// keepsText reports whether Calibre stores s as it is: stripped, whitespace
// runs collapsed, no control characters.
func keepsText(s string) bool {
	return s == strings.Join(strings.Fields(s), " ") && !strings.ContainsFunc(s, unicode.IsControl)
}

func keepsAuthors(names []string) bool {
	if len(names) == 0 {
		return false
	}
	lower := cases.Lower(language.Und)
	for i, n := range names {
		if n == "" || !keepsText(n) || strings.ContainsAny(n, "&|") {
			return false
		}
		if slices.ContainsFunc(names[:i], func(m string) bool {
			return strings.EqualFold(m, n) || lower.String(m) == lower.String(n)
		}) {
			return false
		}
	}
	return true
}

var restoreLanguageCode = regexp.MustCompile(`^[a-z]{3}$`)

func keepsLanguages(codes []string) bool {
	for i, code := range codes {
		if !restoreLanguageCode.MatchString(code) || slices.Contains([]string{"und", "zxx", "mis", "mul"}, code) || slices.Contains(codes[:i], code) {
			return false
		}
	}
	return true
}

func keepsIdentifiers(ids map[string]string) bool {
	for k, v := range ids {
		if k == "" || k != strings.ToLower(strings.TrimSpace(k)) || strings.ContainsAny(k, ":,") ||
			v == "" || v != strings.TrimSpace(v) || strings.Contains(v, ",") {
			return false
		}
	}
	return true
}

// keepsPubdate reports whether Calibre stores the day of s as it is; an empty
// pubdate is restored as none.
func keepsPubdate(s string) bool {
	if s == "" {
		return true
	}
	for _, layout := range []string{time.DateOnly, time.RFC3339Nano} {
		if t, err := time.Parse(layout, s); err == nil {
			t = t.UTC()
			return t.Year() >= 1000 && !findings.ShiftedDay(t)
		}
	}
	return false
}
