package decide

import (
	"slices"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

var reasonLabels = map[string]string{
	"no_formats":                 "No formats",
	"format_unreadable":          "A format can’t be read for metadata",
	"isbn_missing":               "A format has no valid ISBN",
	"isbn_ambiguous":             "A format has several ISBNs",
	"title_incomplete":           "A format’s title is missing or unverified",
	"authors_incomplete":         "A format’s authors are missing or unverified",
	"languages_incomplete":       "A format’s language is missing or unverified",
	"no_record":                  "No source record for the files’ ISBN",
	"records_disagree":           "No source record matches the files",
	"formats_isbn_conflict":      "Formats name different ISBNs",
	"formats_title_conflict":     "Formats disagree on the title",
	"formats_authors_conflict":   "Formats disagree on the authors",
	"formats_languages_conflict": "Formats disagree on the language",
	"records_title_conflict":     "Source records disagree on the title",
	"records_authors_conflict":   "Source records disagree on the authors",
	"records_languages_conflict": "Source records disagree on the language",

	reasonIdentityFrontMatter: "The ISBN comes from the book’s copyright or title page",
	reasonFrontMatterMissing:  "The front matter has ISBNs, but none is usable",
	reasonFrontMatterAmbig:    "The front matter names several ISBNs for this edition",
	reasonFrontMatterUnconf:   "No source record for the front-matter ISBN matches the title",
	reasonFormatsTextConflict: "The formats’ printed text disagrees",
	reasonTextRootsDisagree:   "The book’s printed text and the other sources can’t agree",
	reasonOtherWork:           "The ISBN’s records describe another work than Calibre’s title and authors",
}

// ReasonLabel describes a tier reason; an unknown code is returned as is.
func ReasonLabel(code string) string {
	if label, ok := reasonLabels[code]; ok {
		return label
	}
	return code
}

// Findings returns the report entries a decision adds to the findings a
// package seals: a book whose identity ISBN belongs to another work is listed
// with the findings so it shows on the Findings page (ADR-035). They are
// derived on every load and never sealed.
func (d Decision) Findings() []evidence.Finding {
	var out []evidence.Finding
	if slices.Contains(d.Reasons, reasonOtherWork) {
		out = append(out, evidence.Finding{Code: reasonOtherWork, Field: "isbn"})
	}
	for _, field := range d.textDisagree {
		out = append(out, evidence.Finding{Code: reasonTextRootsDisagree, Field: field})
	}
	return out
}
