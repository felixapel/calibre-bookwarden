// Package fix writes approved proposals to Calibre (spec section 8,
// ADR-021): one book at a time, under one writer, after a stale check, with a
// write-ahead ledger, readback, restore and an incident on a mismatch, and
// undo.
package fix

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/findings"
	"github.com/felixapel/calibre-bookwarden/internal/library"
)

// Calibre's field names, as set-fields takes them.
const (
	fTitle       = "title"
	fSort        = "sort"
	fAuthors     = "authors"
	fAuthorSort  = "author_sort"
	fLanguages   = "languages"
	fIdentifiers = "identifiers"
	fPublisher   = "publisher"
	fPubdate     = "pubdate"
)

// jsonValue encodes v without HTML escaping, so the value sent reads as the
// value meant.
func jsonValue(v any) json.RawMessage {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		panic(err) // strings, lists and string maps always encode
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n"))
}

func decode[T any](raw json.RawMessage) (T, bool) {
	var v T
	err := json.Unmarshal(raw, &v)
	return v, err == nil
}

// proposalChange is the set-fields change for an approved proposal. An ISBN
// is written as the whole identifiers map with "isbn" replaced, because
// Calibre replaces the map.
func proposalChange(p evidence.Proposal, cur evidence.Record) (library.Change, error) {
	one := func(field string) (library.Change, error) {
		if len(p.Proposed) != 1 {
			return library.Change{}, fmt.Errorf("fix: %s proposal has %d values", p.Field, len(p.Proposed))
		}
		return library.Change{Field: field, Value: jsonValue(p.Proposed[0])}, nil
	}
	switch p.Field {
	case "title":
		return one(fTitle)
	case "publisher":
		return one(fPublisher)
	case "pubdate":
		return one(fPubdate)
	case "title_sort":
		return one(fSort)
	case "author_sort":
		return one(fAuthorSort)
	case "authors":
		return library.Change{Field: fAuthors, Value: jsonValue(orEmpty(p.Proposed))}, nil
	case "languages":
		return library.Change{Field: fLanguages, Value: jsonValue(orEmpty(p.Proposed))}, nil
	case "isbn":
		if len(p.Proposed) != 1 {
			return library.Change{}, fmt.Errorf("fix: isbn proposal has %d values", len(p.Proposed))
		}
		ids := maps.Clone(cur.Identifiers)
		if ids == nil {
			ids = map[string]string{}
		}
		ids["isbn"] = p.Proposed[0]
		return library.Change{Field: fIdentifiers, Value: jsonValue(ids)}, nil
	}
	return library.Change{}, fmt.Errorf("fix: unknown proposal field %q", p.Field)
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// beforeChange is the change that writes field's value in before. A nil title
// sort can't be written.
func beforeChange(field string, before evidence.Record) (library.Change, bool) {
	var v any
	switch field {
	case fTitle:
		v = before.Title
	case fSort:
		if before.TitleSort == nil {
			return library.Change{}, false
		}
		v = *before.TitleSort
	case fAuthors:
		v = orEmpty(before.Authors)
	case fAuthorSort:
		v = before.AuthorSort
	case fLanguages:
		v = orEmpty(before.Languages)
	case fIdentifiers:
		ids := before.Identifiers
		if ids == nil {
			ids = map[string]string{}
		}
		v = ids
	case fPublisher:
		v = before.Publisher
	case fPubdate:
		if before.PubDate == "" {
			return library.Change{Field: fPubdate, Value: json.RawMessage("null")}, true
		}
		v = before.PubDate
	default:
		return library.Change{}, false
	}
	return library.Change{Field: field, Value: jsonValue(v)}, true
}

// restoreChanges writes before's values for every field changes touch, in
// pairs: a title brings its sort and authors their sort, each after the
// field whose write recomputes it. A sort written without its title or
// authors goes back alone (ADR-028). The second result names the fields
// changes write that before can't express, so no restore can put them back.
func restoreChanges(changes []library.Change, before evidence.Record) ([]library.Change, []string) {
	wrote := func(field string) bool {
		return slices.ContainsFunc(changes, func(c library.Change) bool { return c.Field == field })
	}
	var out []library.Change
	var missing []string
	seen := map[string]bool{}
	add := func(fields ...string) {
		for _, f := range fields {
			if seen[f] {
				continue
			}
			seen[f] = true
			if c, ok := beforeChange(f, before); ok {
				out = append(out, c)
			} else if wrote(f) {
				missing = append(missing, f)
			}
		}
	}
	for _, c := range changes {
		switch {
		case c.Field == fTitle || (c.Field == fSort && wrote(fTitle)):
			add(fTitle, fSort)
		case c.Field == fAuthors || (c.Field == fAuthorSort && wrote(fAuthors)):
			add(fAuthors, fAuthorSort)
		default:
			add(c.Field)
		}
	}
	return out, missing
}

// clean reports whether Calibre keeps s as it is: stripped, whitespace runs
// collapsed, and no control characters.
func clean(s string) bool {
	return s == strings.Join(strings.Fields(s), " ") && !strings.ContainsFunc(s, unicode.IsControl)
}

var languageCode = regexp.MustCompile(`^[a-z]{3}$`)

// storable reports whether Calibre 9.16 stores c's value exactly (ADR-021):
// set-fields strips text, replaces "&" and "|" in authors, folds names that
// differ in case, drops unknown language codes, cleans identifiers and moves
// dates off a month's first and last days.
func storable(c library.Change) bool {
	switch c.Field {
	case fTitle, fSort:
		s, ok := decode[string](c.Value)
		return ok && s != "" && clean(s)
	case fAuthorSort, fPublisher:
		s, ok := decode[string](c.Value)
		return ok && clean(s)
	case fAuthors:
		names, ok := decode[[]string](c.Value)
		if !ok || len(names) == 0 {
			return false
		}
		for i, n := range names {
			if n == "" || !clean(n) || strings.ContainsAny(n, "&|") {
				return false
			}
			if slices.ContainsFunc(names[:i], func(m string) bool { return foldEqual(m, n) }) {
				return false
			}
		}
		return true
	case fLanguages:
		codes, ok := decode[[]string](c.Value)
		if !ok {
			return false
		}
		for i, code := range codes {
			if !languageCode.MatchString(code) || slices.Contains([]string{"und", "zxx", "mis", "mul"}, code) || slices.Contains(codes[:i], code) {
				return false
			}
		}
		return true
	case fIdentifiers:
		ids, ok := decode[map[string]string](c.Value)
		if !ok {
			return false
		}
		for k, v := range ids {
			if k == "" || k != strings.ToLower(strings.TrimSpace(k)) || strings.ContainsAny(k, ":,") ||
				v == "" || v != strings.TrimSpace(v) || strings.Contains(v, ",") {
				return false
			}
		}
		return true
	case fPubdate:
		if string(c.Value) == "null" {
			return true
		}
		s, ok := decode[string](c.Value)
		if !ok {
			return false
		}
		d, ok := pubdateDay(s)
		return ok && !findings.ShiftedDay(d)
	}
	return false
}

// unstorable names why Calibre would store c differently, or couldn't put
// back before's value of the fields c touches; "" when neither.
func unstorable(c library.Change, before evidence.Record) string {
	if !storable(c) {
		return "calibre_alters_value"
	}
	back, missing := restoreChanges([]library.Change{c}, before)
	if len(missing) > 0 {
		return "calibre_cannot_restore"
	}
	for _, r := range back {
		if !storable(r) {
			return "calibre_cannot_restore"
		}
	}
	return ""
}

// unwritable is unstorable for a proposal against the record it was made
// from.
func unwritable(p evidence.Proposal, before evidence.Record) string {
	c, err := proposalChange(p, before)
	if err != nil {
		return "calibre_alters_value"
	}
	return unstorable(c, before)
}

// Unwritable is the page's view of unwritable, from the proposal alone: the
// record is rebuilt from the proposal's current value. Approve checks
// against the full sealed record, and Apply also checks for shared names.
func Unwritable(p evidence.Proposal) string {
	var r evidence.Record
	first := ""
	if len(p.Current) > 0 {
		first = p.Current[0]
	}
	switch p.Field {
	case "title":
		r.Title = first
	case "authors":
		r.Authors = p.Current
	case "languages":
		r.Languages = p.Current
	case "publisher":
		r.Publisher = first
	case "pubdate":
		r.PubDate = first
	case "title_sort":
		r.TitleSort = &first
	case "author_sort":
		r.AuthorSort = first
	case "isbn":
		if first != "" {
			r.Identifiers = map[string]string{"isbn": first}
		}
	}
	return unwritable(p, r)
}

// pubdateDay is the UTC calendar day Calibre stores for a date or timestamp.
// Years before 1000 read as unset.
func pubdateDay(s string) (time.Time, bool) {
	for _, layout := range []string{"2006-01-02", time.RFC3339Nano} {
		if t, err := time.Parse(layout, s); err == nil {
			t = t.UTC()
			return t, t.Year() >= 1000
		}
	}
	return time.Time{}, false
}

// foldEqual reports whether Calibre's ICU lower-case key could match a and b.
func foldEqual(a, b string) bool {
	if strings.EqualFold(a, b) {
		return true
	}
	lower := cases.Lower(language.Und)
	return lower.String(a) == lower.String(b)
}

// caseCollision reports whether a name differs from an existing one only in
// case. Calibre would rename that existing item for every book.
func caseCollision(names, existing []string) bool {
	lower := cases.Lower(language.Und)
	for _, e := range existing {
		le := lower.String(e)
		for _, n := range names {
			if n != e && (strings.EqualFold(n, e) || lower.String(n) == le) {
				return true
			}
		}
	}
	return false
}

// same reports whether b holds c's value as Calibre reads it back.
func same(c library.Change, b library.Book) bool {
	switch c.Field {
	case fTitle, fAuthorSort, fPublisher:
		s, ok := decode[string](c.Value)
		got := map[string]string{fTitle: b.Title, fAuthorSort: b.AuthorSort, fPublisher: b.Publisher}[c.Field]
		return ok && s == got
	case fSort:
		s, ok := decode[string](c.Value)
		return ok && b.TitleSort != nil && *b.TitleSort == s
	case fAuthors, fLanguages:
		want, ok := decode[[]string](c.Value)
		got := b.Authors
		if c.Field == fLanguages {
			got = b.Languages
		}
		return ok && slices.Equal(want, got)
	case fIdentifiers:
		want, ok := decode[map[string]string](c.Value)
		return ok && maps.Equal(want, b.Identifiers)
	case fPubdate:
		if string(c.Value) == "null" {
			return b.PubDate == ""
		}
		s, ok := decode[string](c.Value)
		if !ok {
			return false
		}
		if _, err := time.Parse("2006-01-02", s); err == nil {
			k := findings.DateKey(b.PubDate)
			return k.Valid && k.Value == s
		}
		want, err1 := time.Parse(time.RFC3339Nano, s)
		got, err2 := time.Parse(time.RFC3339Nano, b.PubDate)
		return err1 == nil && err2 == nil && want.Equal(got)
	}
	return false
}
