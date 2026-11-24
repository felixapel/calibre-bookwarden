package findings

import (
	"encoding/json"
	"slices"

	"golang.org/x/text/language"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

// Key is one field's comparison key under the ADR-017 rules. An invalid key
// (missing, invalid, undetermined or too long) can neither agree nor
// conflict. The tier and proposal derivation (ADR-020) shares these rules;
// changing them changes historical derivations, so it needs new versions.
type Key struct {
	Value string
	Valid bool
}

func keyOf(v value) Key {
	if v.state != fieldValid {
		return Key{}
	}
	return Key{Value: v.key, Valid: true}
}

// The *Key functions below are frozen: every decision version up to 9 calls
// them without a version, and its sealed packages are replayed with them. A
// change here changes those decisions: add the new behaviour behind a new
// decision version instead.

// TitleKey folds case and whitespace, preserving accents and punctuation.
func TitleKey(title string) Key { return keyOf(metadataValues(title, nil, nil)["title"]) }

// AuthorsKey compares the ordered list.
func AuthorsKey(authors []string) Key { return keyOf(metadataValues("", authors, nil)["authors"]) }

// LanguagesKey compares the set of explicit primary languages.
func LanguagesKey(languages []string) Key {
	return keyOf(metadataValues("", nil, languages)["languages"])
}

// PublisherKey normalizes like a title; there is no alias policy.
func PublisherKey(publisher string) Key {
	k, state := normalizeField(publisher)
	return keyOf(value{k, state})
}

// DateKey is valid only for a full calendar date, as "YYYY-MM-DD".
func DateKey(date string) Key {
	k, state := dateValue(date)
	return keyOf(value{k, state})
}

// LanguageCodes returns a valid languages key as ISO 639-3 codes, Calibre's
// form, or nil.
func LanguageCodes(k Key) []string {
	var bases []string
	if !k.Valid || json.Unmarshal([]byte(k.Value), &bases) != nil {
		return nil
	}
	out := make([]string, 0, len(bases))
	for _, b := range bases {
		base, err := language.ParseBase(b)
		if err != nil {
			return nil
		}
		out = append(out, base.ISO3())
	}
	return out
}

// FormatFields are one format's keys under the current (version 3) capture
// rules. A capture-flagged field has an invalid key; a flagged ISBN field has
// no ISBNs.
type FormatFields struct {
	Eligible                                   bool
	Title, Authors, Languages, Publisher, Date Key
	ISBNs                                      []string // canonical ISBN-13, sorted, unique
	ISBNFlagged                                bool
}

// Fields returns a format's keys. It fails with ErrInvalidEvidence on a
// malformed capture marker, as derivation does.
func Fields(f evidence.FormatEvidence) (FormatFields, error) {
	var capture map[string]string
	if f.Metadata != nil {
		var err error
		if capture, err = captureQualifications(f.Metadata); err != nil {
			return FormatFields{}, err
		}
	}
	if !metadataEligible(f) {
		return FormatFields{}, nil
	}
	m := f.Metadata
	if m == nil {
		m = &evidence.FileMetadata{}
	}
	keep := func(field string, k Key) Key {
		if capture[field] != "" {
			return Key{}
		}
		return k
	}
	ff := FormatFields{
		Eligible:  true,
		Title:     keep("title", TitleKey(m.Title)),
		Authors:   keep("authors", AuthorsKey(m.Authors)),
		Languages: keep("languages", LanguagesKey(m.Languages)),
		Publisher: keep("publisher", PublisherKey(m.Publisher)),
		Date:      keep("pubdate", DateKey(m.Date)),
	}
	if capture["isbn"] != "" || listTooLong(m.ISBNs) {
		ff.ISBNFlagged = true
		return ff, nil
	}
	for _, raw := range m.ISBNs {
		if isbn, ok := CanonicalISBN(raw); ok {
			ff.ISBNs = append(ff.ISBNs, isbn)
		}
	}
	slices.Sort(ff.ISBNs)
	ff.ISBNs = slices.Compact(ff.ISBNs)
	return ff, nil
}
