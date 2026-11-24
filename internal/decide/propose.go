package decide

import (
	"maps"
	"slices"
	"strings"
	"unicode"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/findings"
)

// rootOrder ranks roots for a proposal's displayed value and its Roots list.
var rootOrder = []string{evidence.SourceDNB, evidence.SourceBNE, evidence.RootFile, evidence.SourceOpenLibrary, evidence.SourceGoogleBooks, evidence.RootText}

var proposalFields = []string{fieldTitle, fieldAuthors, fieldLanguages, fieldISBN, fieldPublisher, fieldPubdate}

// Fields returns the proposal fields, in proposal order.
func Fields() []string { return append(slices.Clone(proposalFields), fieldTitleSort, fieldAuthorSort) }

func rank(name string) int {
	if i := slices.Index(rootOrder, name); i >= 0 {
		return i
	}
	return len(rootOrder)
}

// statement is what one root says about a field: its distinct valid keys.
type statement struct {
	name  string
	class int
	keys  []string
}

func (v *view) statements(field string) []statement {
	out := []statement{{name: evidence.RootFile, class: classInternal, keys: v.fileKeys(field)}}
	for _, rt := range v.roots {
		out = append(out, statement{name: rt.name, class: rt.class, keys: v.rootKeys(rt, field, "")})
	}
	slices.SortFunc(out, func(a, b statement) int { return rank(a.name) - rank(b.name) })
	return out
}

// proposals applies the field rule (ADR-020) to every field in scope.
func (v *view) proposals() []evidence.Proposal {
	var out []evidence.Proposal
	for _, field := range proposalFields {
		var p *evidence.Proposal
		if field == fieldISBN {
			p = v.isbnProposal()
		} else {
			p = v.fieldProposal(field)
		}
		if p != nil {
			out = append(out, *p)
		}
	}
	return out
}

func (v *view) fieldProposal(field string) *evidence.Proposal {
	if v.version >= 8 && hasTextStatement(field) {
		if v.text.vetoed[field] {
			return nil
		}
		if st, ok := v.text.stated[field]; ok {
			return v.textProposal(field, st)
		}
	}
	// From version 7 a tolerance can clear a records conflict, but only exact
	// agreement proposes a title or an authors list: records that differ in
	// the field and agree through a tolerance (a translator listed by one
	// source, a dropped article) are no evidence for either spelling.
	if v.version >= 7 && (field == fieldTitle || field == fieldAuthors) && v.strictConflict[field] {
		return nil
	}
	stmts := v.statements(field)
	candidates := map[string]bool{}
	for _, s := range stmts {
		for _, k := range s.keys {
			candidates[k] = true
		}
	}
	for _, key := range slices.Sorted(maps.Keys(candidates)) {
		var support []string
		authority, vetoed := false, false
		for _, s := range stmts {
			switch {
			case len(s.keys) == 1 && s.keys[0] == key:
				support = append(support, s.name)
				authority = authority || s.class >= classOfficial
			case len(s.keys) > 0 && s.class >= classOfficial:
				vetoed = true // another value, or a split root
			}
		}
		if len(support) < 2 || !authority || vetoed {
			continue
		}
		if cal := calibreKey(v.p.Calibre, field); cal.Valid && cal.Value == key {
			return nil
		}
		if v.pubdateIsZoneShift(field, key) || v.pubdateUnwritable(field, key) {
			return nil
		}
		proposed := v.display(field, key, support[0])
		if field == fieldTitle && v.version >= 2 && len(proposed) == 1 && v.recordPathShortens(v.p.Calibre.Title, proposed[0]) {
			return nil
		}
		if field == fieldAuthors && v.version >= 3 && reformsOnly(v.p.Calibre.Authors, proposed) {
			return nil
		}
		return &evidence.Proposal{Field: field, Current: calibreValue(v.p.Calibre, field), Proposed: proposed, Roots: support}
	}
	return nil
}

// isbnProposal proposes the identity ISBN when Calibre's differs and a record
// from a source root agrees with the files' title.
func (v *view) isbnProposal() *evidence.Proposal {
	if v.version >= 8 {
		if v.text.vetoed[fieldISBN] {
			return nil
		}
		if st, ok := v.text.stated[fieldISBN]; ok {
			return v.textProposal(fieldISBN, st)
		}
	}
	if v.identity == "" {
		return nil
	}
	if cal := calibreISBNs(v.p.Calibre); len(cal) == 1 && cal[0] == v.identity {
		return nil
	}
	titles := v.fileKeys(fieldTitle)
	if len(titles) != 1 {
		return nil
	}
	vouches := func(r evidence.SourceRecord) bool {
		k := recordKey(r, fieldTitle)
		return k.Valid && k.Value == titles[0]
	}
	if v.version >= 6 {
		// From version 6 the record must also match the files in full
		// (ADR-030), so it is never looser than version 5's exact title.
		// A front-matter identity proposes no ISBN: its "file" root is a
		// page-text hint, which only selects records (ADR-035 part 2).
		want, ok := v.want()
		if !ok || v.reasons[reasonIdentityFrontMatter] {
			return nil
		}
		exact, match := vouches, v.strictMatcher(want)
		vouches = func(r evidence.SourceRecord) bool { return exact(r) && match(r) }
	}
	roots := []string{evidence.RootFile}
	for _, rt := range v.roots {
		if slices.ContainsFunc(rt.records, vouches) {
			roots = append(roots, rt.name)
		}
	}
	if len(roots) < 2 {
		return nil
	}
	slices.SortFunc(roots, func(a, b string) int { return rank(a) - rank(b) })
	return &evidence.Proposal{Field: fieldISBN, Current: calibreValue(v.p.Calibre, fieldISBN), Proposed: []string{v.identity}, Roots: roots}
}

// display returns the agreeing value as the first supporting root states it.
func (v *view) display(field, key, rootName string) []string {
	switch field {
	case fieldLanguages:
		return findings.LanguageCodes(findings.Key{Value: key, Valid: true})
	case fieldPubdate:
		return []string{key}
	}
	if rootName == evidence.RootFile {
		for _, f := range v.formats {
			if k := formatKey(f.fields, field); k.Valid && k.Value == key {
				return rawValue(f.raw.Title, f.raw.Authors, f.raw.Publisher, field)
			}
		}
		return nil
	}
	for _, r := range v.roots[rootName].records {
		if k := recordKey(r, field); k.Valid && k.Value == key {
			return rawValue(r.Title, r.Authors, r.Publisher, field)
		}
	}
	return v.joinedTitleDisplay(rootName, field, key)
}

// shortens reports whether proposed's words are a shorter run of calibre's:
// a main title without Calibre's subtitle, or without its leading article.
// Roots that state the main title alone are no evidence against the longer
// title, so there is no proposal (ADR-026).
func shortens(calibre, proposed string) bool {
	c, p := titleWords(calibre), titleWords(proposed)
	if len(p) == 0 || len(p) >= len(c) {
		return false
	}
	for i := 0; i+len(p) <= len(c); i++ {
		if slices.Equal(c[i:i+len(p)], p) {
			return true
		}
	}
	return false
}

// reformsOnly reports whether every proposed author's surname is one of
// Calibre's authors' surnames, counting repeats: the proposal would only drop
// names, such as a co-author or translator, reorder them, or change a name's
// form on this one book. Roots that list the main author alone are no
// evidence against Calibre's longer list, and a name form is changed for the
// whole library in Calibre (ADR-027).
func reformsOnly(calibre, proposed []string) bool {
	if len(proposed) == 0 {
		return false
	}
	have := map[string]int{}
	for _, name := range calibre {
		if s := surname(name); s != "" {
			have[s]++
		}
	}
	for _, name := range proposed {
		s := surname(name)
		if s == "" || have[s] == 0 {
			return false
		}
		have[s]--
	}
	return true
}

// surname is a name's last case-folded word, or "" for none.
func surname(name string) string {
	if w := titleWords(name); len(w) > 0 {
		return w[len(w)-1]
	}
	return ""
}

// titleWords is a title's case-folded words: runs of letters, combining
// marks and digits.
func titleWords(title string) []string {
	k := findings.TitleKey(title)
	if !k.Valid {
		return nil
	}
	return strings.FieldsFunc(k.Value, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsMark(r) && !unicode.IsDigit(r) })
}

func clean(s string) string { return strings.Join(strings.Fields(s), " ") }

func rawValue(title string, authors []string, publisher, field string) []string {
	switch field {
	case fieldTitle:
		return []string{clean(title)}
	case fieldPublisher:
		return []string{clean(publisher)}
	case fieldAuthors:
		var out []string
		for _, a := range authors {
			if c := clean(a); c != "" {
				out = append(out, c)
			}
		}
		return out
	}
	return nil
}

func calibreKey(r evidence.Record, field string) findings.Key {
	switch field {
	case fieldTitle:
		return findings.TitleKey(r.Title)
	case fieldAuthors:
		return findings.AuthorsKey(r.Authors)
	case fieldLanguages:
		return findings.LanguagesKey(r.Languages)
	case fieldPublisher:
		return findings.PublisherKey(r.Publisher)
	case fieldPubdate:
		return findings.DateKey(r.PubDate)
	}
	return findings.Key{}
}

// calibreISBNs returns Calibre's valid ISBNs, canonical, sorted and unique.
func calibreISBNs(r evidence.Record) []string {
	var out []string
	for key, val := range r.Identifiers {
		if isbn, ok := findings.CanonicalISBN(val); ok && strings.EqualFold(key, "isbn") {
			out = append(out, isbn)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// calibreValue is Calibre's value as read, without empty strings.
func calibreValue(r evidence.Record, field string) []string {
	var raw []string
	switch field {
	case fieldTitle:
		raw = []string{r.Title}
	case fieldAuthors:
		raw = r.Authors
	case fieldLanguages:
		raw = r.Languages
	case fieldPublisher:
		raw = []string{r.Publisher}
	case fieldPubdate:
		raw = []string{r.PubDate}
	case fieldISBN:
		for _, key := range slices.Sorted(maps.Keys(r.Identifiers)) {
			if strings.EqualFold(key, "isbn") {
				raw = append(raw, r.Identifiers[key])
			}
		}
	}
	out := []string{}
	for _, s := range raw {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}
