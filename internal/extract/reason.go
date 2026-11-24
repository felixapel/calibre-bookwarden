package extract

import (
	"strings"

	"github.com/felixapel/calibre-bookwarden/internal/findings"
)

// The reasons a value the quote doesn't derive was refused, for the reverify
// report (plan v4, C4). They are diagnostic only: never sealed, never stored,
// and no verdict depends on them.
const (
	// RefusedNotInQuote: the value, its edges trimmed, is not in the quote
	// even after folding case, dashes and format characters.
	RefusedNotInQuote = "value_not_in_quote"
	// RefusedCaseOnly: matches when both sides are case-folded.
	RefusedCaseOnly = "case_only"
	// RefusedDash: matches when dash variants are folded to "-" too.
	RefusedDash = "dash"
	// RefusedFormatChar: matches when U+200B, U+FEFF and U+2060 are removed
	// too.
	RefusedFormatChar = "format_char"
	// RefusedEdgeTrim: matches only after trimming punctuation at the edges
	// of both.
	RefusedEdgeTrim = "edge_trim"
	// RefusedRoleLine: the quote runs on into a role line; checked first, as
	// no fold changes it.
	RefusedRoleLine = "crosses_role_line"
	// RefusedOther: the value is in the quote but none of the above explains
	// the refusal: part of a title quote, a label or role the text doesn't
	// give, or a value its field's transform rejects.
	RefusedOther = "other"
)

// RefusalReasons lists the reasons in report order.
var RefusalReasons = []string{RefusedNotInQuote, RefusedCaseOnly, RefusedDash, RefusedFormatChar, RefusedEdgeTrim, RefusedRoleLine, RefusedOther}

var (
	dashFold   = strings.NewReplacer("\u2010", "-", "\u2011", "-", "\u2012", "-", "\u2013", "-", "\u2014", "-", "\u2015", "-", "\u2212", "-")
	formatDrop = strings.NewReplacer("\u200b", "", "\ufeff", "", "\u2060", "")
)

// refusalReason says why derive refused c's value at f, as a verifier that
// also folded case, dashes or format characters would see it. A quote that
// runs into a role line stays refused whatever the folds, so it is checked
// first. A value that matches before any fold, or with the folds derive
// applies since v4, was refused for something no fold changes (a label, a
// role, a field's own rule) and is other. The folds
// add up in order, so a value that needs case and dash folding is a dash.
// strings.ToLower is simple case mapping: ß against SS, or a final sigma,
// stays value_not_in_quote.
func refusalReason(f *frame, c Claim) string {
	if f.crossesRoleLine() {
		return RefusedRoleLine
	}
	q, v := string(f.q), f.x.fold(c.Value)
	match := fieldMatch(c.Field)
	if match(q, v, false) || matchV4(c.Field, match, q, v) {
		return RefusedOther
	}
	if match(q, v, true) {
		return RefusedCaseOnly
	}
	fq, fv := q, v
	for i, fold := range []func(string) string{dashFold.Replace, formatDrop.Replace} {
		fq, fv = fold(fq), fold(fv)
		if match(fq, fv, true) {
			return []string{RefusedDash, RefusedFormatChar}[i]
		}
	}
	edges := func(s string) string { return strings.TrimFunc(s, isEdge) }
	if match(edges(fq), edges(fv), true) {
		return RefusedEdgeTrim
	}
	if !strings.Contains(strings.ToLower(fq), strings.ToLower(edges(fv))) {
		return RefusedNotInQuote
	}
	return RefusedOther
}

// matchV4 reports whether v matches q with the folds derive itself applies
// since v4 (ADR-046): case, dashes and the three format characters for a
// title, subtitle or imprint; case for a name, a series or an edition; dashes
// for an ISBN. A value that matches so was refused for something no fold
// changes.
func matchV4(field string, match func(q, v string, foldCase bool) bool, q, v string) bool {
	switch field {
	case FieldTitle, FieldSubtitle, FieldImprint:
		return match(dropInvisible(dashFold.Replace(q)), dropInvisible(dashFold.Replace(v)), true)
	case FieldContributor, FieldSeries, FieldEdition:
		return match(q, v, true)
	case FieldISBN:
		return match(dashFold.Replace(q), v, false)
	}
	return false
}

// fieldMatch compares a value with a quote the way derive does for field,
// ignoring case when foldCase is set. A title, subtitle or imprint must equal
// the trimmed quote; a name, a series or an ISBN must equal what the quote
// parses to, parsed as printed (the name parser reads capitals), so a list, a
// role noun or part of a series name is no case difference; another field's
// value must lie within the quote.
func fieldMatch(field string) func(q, v string, foldCase bool) bool {
	eq := func(a, b string, foldCase bool) bool {
		if foldCase {
			return strings.ToLower(a) == strings.ToLower(b) //nolint:staticcheck // the same fold as the substring check below, not EqualFold's
		}
		return a == b
	}
	switch field {
	case FieldTitle, FieldSubtitle, FieldImprint:
		trim := trimTitle
		if field == FieldImprint {
			trim = trimImprint
		}
		return func(q, v string, foldCase bool) bool {
			want := trim(q)
			return hasLetterOrDigit(want) && !hasFormatChar(want) && eq(trim(v), want, foldCase)
		}
	case FieldContributor:
		return func(q, v string, foldCase bool) bool {
			rest := q
			if hits := roles.scan(q); len(hits) > 0 && hits[0].start == 0 {
				rest = q[hits[0].end:]
			}
			want, ok := canonName(rest)
			return ok && eq(trimName(v), want, foldCase)
		}
	case FieldSeries, FieldSeriesIndex:
		return func(q, v string, foldCase bool) bool {
			st, ok := parseSeries(q)
			if !ok {
				return false
			}
			if field == FieldSeries {
				return eq(trimTitle(v), st.name, foldCase)
			}
			idx, ok := canonDecimal(v)
			return ok && idx == st.index
		}
	case FieldISBN:
		// ScanISBNs folds dashes and hyphenation itself; an ISBN has no case.
		return func(q, v string, _ bool) bool {
			want, ok := findings.CanonicalISBN(v)
			if !ok {
				return false
			}
			for _, m := range findings.ScanISBNs(q) {
				if m.ISBN == want {
					return true
				}
			}
			return false
		}
	}
	return func(q, v string, foldCase bool) bool {
		if foldCase {
			q, v = strings.ToLower(q), strings.ToLower(v)
		}
		return strings.Contains(q, v)
	}
}
