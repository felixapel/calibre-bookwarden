package findings

import (
	"unicode/utf8"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

// These are the immutable M2.2 capture bounds used by derivation version 2.
// Cleaning can stop after a pending space and trim that space, leaving 511
// runes even though it discarded a suffix. Old evidence cannot disambiguate it.
const (
	captureItems = 32
	captureISBNs = 16
)

func captureQualifications(m *evidence.FileMetadata) (map[string]string, error) {
	out := make(map[string]string)
	scalars := map[string]string{"title": m.Title, "publisher": m.Publisher, "pubdate": m.Date}
	lists := map[string][]string{"authors": m.Authors, "languages": m.Languages}
	if limits := m.Limits; limits != nil {
		if limits.Version != 1 || len(m.ISBNs) > captureISBNs {
			return nil, invalidEvidence()
		}
		for _, s := range scalars {
			if utf8.RuneCountInString(s) > maxFieldRunes {
				return nil, invalidEvidence()
			}
		}
		for _, values := range lists {
			if len(values) > captureItems {
				return nil, invalidEvidence()
			}
			for _, s := range values {
				if utf8.RuneCountInString(s) > maxFieldRunes {
					return nil, invalidEvidence()
				}
			}
		}
		for _, isbn := range m.ISBNs {
			if len(isbn) > 13 {
				return nil, invalidEvidence()
			}
		}
		for field, limited := range map[string]bool{
			"title": limits.Title, "authors": limits.Authors, "languages": limits.Languages,
			"isbn": limits.ISBNs, "publisher": limits.Publisher, "pubdate": limits.Date,
		} {
			if limited {
				out[field] = "field_too_long"
			}
		}
		return out, nil
	}
	for field, s := range scalars {
		if n := utf8.RuneCountInString(s); n > maxFieldRunes {
			out[field] = "field_too_long"
		} else if n >= maxFieldRunes-1 {
			out[field] = "capture_limit_unverified"
		}
	}
	for field, values := range lists {
		if listTooLong(values) || len(values) > captureItems {
			out[field] = "field_too_long"
			continue
		}
		if len(values) == captureItems {
			out[field] = "capture_limit_unverified"
		}
		for _, s := range values {
			if utf8.RuneCountInString(s) >= maxFieldRunes-1 {
				out[field] = "capture_limit_unverified"
			}
		}
	}
	if listTooLong(m.ISBNs) || len(m.ISBNs) > captureISBNs {
		out["isbn"] = "field_too_long"
	} else if len(m.ISBNs) == captureISBNs {
		out["isbn"] = "capture_limit_unverified"
	}
	return out, nil
}
