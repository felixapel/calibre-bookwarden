package decide

import (
	"slices"
	"strings"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/findings"
)

const (
	fieldTitleSort  = "title_sort"
	fieldAuthorSort = "author_sort"
)

// sortProposals restates Calibre's own sorts for its record (ADR-028): the
// title sort its rule gives for the title and first language, and the author
// sort it joins from its authors table. A sort is left alone beside a
// proposal for a field its rule reads (title and languages, or authors),
// since it would be computed from the old value, and the author sort
// whenever a table entry isn't Calibre's default for the name.
func (v *view) sortProposals(made []evidence.Proposal) []evidence.Proposal {
	has := func(field string) bool {
		return slices.ContainsFunc(made, func(p evidence.Proposal) bool { return p.Field == field })
	}
	rec := v.p.Calibre
	var out []evidence.Proposal
	if want, ok := findings.TitleSortProposal(rec); ok && !has(fieldTitle) && !has(fieldLanguages) {
		out = append(out, sortProposal(fieldTitleSort, *rec.TitleSort, want))
	}
	if want, ok := authorSort(rec); ok && want != rec.AuthorSort && !has(fieldAuthors) {
		out = append(out, sortProposal(fieldAuthorSort, rec.AuthorSort, want))
	}
	return out
}

func sortProposal(field, current, want string) evidence.Proposal {
	cur := []string{}
	if current != "" {
		cur = []string{current}
	}
	return evidence.Proposal{Field: field, Current: cur, Proposed: []string{want}, Roots: []string{evidence.RootCalibreRule}}
}

// authorSort is Calibre's author sort for rec: its authors' table sorts
// joined with " & ", when every one is Calibre's default for the name.
func authorSort(rec evidence.Record) (string, bool) {
	if rec.AuthorSortMap == nil || len(rec.Authors) == 0 {
		return "", false
	}
	parts := make([]string, 0, len(rec.Authors))
	for _, a := range rec.Authors {
		s, ok := rec.AuthorSortMap[a]
		if !ok || s != findings.CalibreAuthorSort(a) {
			return "", false
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " & "), true
}
