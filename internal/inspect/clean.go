package inspect

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/seriesindex"
)

// Caps on what a file can put into the evidence.
const (
	maxField = 512 // runes per string
	maxItems = 32  // items per list
	maxISBNs = 16
)

// clean makes a string from a file safe to seal and show: valid UTF-8, no
// control characters, whitespace collapsed and trimmed, at most maxField runes.
func clean(s string) string {
	out, _ := cleanLimited(s)
	return out
}

func cleanLimited(s string) (string, bool) {
	var b strings.Builder
	n, space := 0, false
	limited := false
	for _, r := range strings.ToValidUTF8(s, string(utf8.RuneError)) {
		switch {
		case unicode.IsSpace(r):
			space = b.Len() > 0
			continue
		case unicode.IsControl(r):
			continue
		}
		if space {
			if n++; n > maxField {
				limited = true
				break
			}
			b.WriteByte(' ')
			space = false
		}
		if n++; n > maxField {
			limited = true
			break
		}
		b.WriteRune(r)
	}
	return strings.TrimRightFunc(b.String(), unicode.IsSpace), limited
}

// cleanList cleans every item, drops empty ones and duplicates, and keeps at
// most maxItems.
func cleanList(in []string) []string {
	out, _ := cleanListLimited(in)
	return out
}

func cleanListLimited(in []string) ([]string, bool) {
	var out []string
	limited := false
	for _, s := range in {
		value, cut := cleanLimited(s)
		limited = limited || cut
		if value != "" && !slices.Contains(out, value) {
			if len(out) == maxItems {
				limited = true
				break
			}
			out = append(out, value)
		}
	}
	return out, limited
}

// isbnCandidates keeps the values shaped like an ISBN: a urn:isbn: or ISBN
// prefix, or nothing but digits, X, hyphens and spaces, leaving 13 digits or 9
// digits and a check character. Checksums aren't checked; that's a finding.
func isbnCandidates(values ...string) []string {
	out, _ := isbnCandidatesLimited(values...)
	return out
}

func isbnCandidatesLimited(values ...string) ([]string, bool) {
	var out []string
	for _, v := range values {
		v = strings.TrimSpace(v)
		lower := strings.ToLower(v)
		for _, prefix := range isbnPrefixes {
			if strings.HasPrefix(lower, prefix) {
				v = strings.TrimLeft(v[len(prefix):], ": ")
				break
			}
		}
		var digits []byte
		for i := 0; i < len(v); i++ {
			switch c := v[i]; {
			case c >= '0' && c <= '9', c == 'X':
				digits = append(digits, c)
			case c == 'x':
				digits = append(digits, 'X')
			case c == '-', c == ' ':
			default:
				digits = nil
				i = len(v)
			}
		}
		isbn := string(digits)
		if !shapedLikeISBN(isbn) || slices.Contains(out, isbn) {
			continue
		}
		if len(out) == maxISBNs {
			return out, true
		}
		out = append(out, isbn)
	}
	return out, false
}

var isbnPrefixes = []string{"urn:isbn:", "isbn"}

// isbnPrefixed reports whether a value says it is an ISBN.
func isbnPrefixed(v string) bool {
	lower := strings.ToLower(strings.TrimSpace(v))
	return slices.ContainsFunc(isbnPrefixes, func(p string) bool { return strings.HasPrefix(lower, p) })
}

func shapedLikeISBN(s string) bool {
	switch len(s) {
	case 13:
		return !strings.ContainsRune(s, 'X')
	case 10:
		return !strings.ContainsRune(s[:9], 'X')
	}
	return false
}

// newMeta cleans what a file says about itself; ISBNs holds raw candidate
// values. It returns nil when nothing is left.
func newMeta(m evidence.FileMetadata) *evidence.FileMetadata {
	limits := &evidence.MetadataLimits{Version: 1}
	out := evidence.FileMetadata{Limits: limits}
	out.Title, limits.Title = cleanLimited(m.Title)
	out.Authors, limits.Authors = cleanListLimited(m.Authors)
	out.Languages, limits.Languages = cleanListLimited(m.Languages)
	out.ISBNs, limits.ISBNs = isbnCandidatesLimited(m.ISBNs...)
	out.Publisher, limits.Publisher = cleanLimited(m.Publisher)
	out.Date, limits.Date = cleanLimited(m.Date)
	out.Series, limits.Series = cleanLimited(m.Series)
	if out.Series != "" {
		// An index that is not a plain decimal within the caps is marked,
		// not kept.
		var ok bool
		if out.SeriesIndex, ok = seriesindex.Normalize(m.SeriesIndex); !ok {
			limits.SeriesIndex = true
		}
	}
	if out.Title == "" && out.Authors == nil && out.Languages == nil && out.ISBNs == nil && out.Publisher == "" && out.Date == "" && out.Series == "" {
		return nil
	}
	return &out
}
