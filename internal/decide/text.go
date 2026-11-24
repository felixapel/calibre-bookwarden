package decide

import (
	"slices"
	"strings"
	"unicode"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/findings"
)

// Decision version 8: the book's printed text is a root (ADR-036). The text
// statement of a field is built from the sealed, agreed facts of the EPUB
// formats' extractions.

// Reasons added by version 8.
const (
	reasonFormatsTextConflict = "formats_text_conflict"
	// reasonTextRootsDisagree has no tier effect: it marks a field whose text
	// statement and other roots can't agree, and shows as a finding.
	reasonTextRootsDisagree = "text_roots_disagree"
)

// classText lets the text give authority and veto, like an official root.
const classText = classOfficial

var textFields = []string{fieldTitle, fieldAuthors, fieldPublisher, fieldISBN}

func hasTextStatement(field string) bool { return slices.Contains(textFields, field) }

// textStatement is the text's value for one field: its comparison key and the
// value as printed.
type textStatement struct {
	key     string
	display []string
	// For an ISBN: ebook is set when the value is labelled ebook, and
	// onlyPrinted when it is the only ISBN printed outside the back units. A
	// text-alone fill needs one of them (ADR-036 section 3.3).
	ebook, onlyPrinted bool
}

// textView is what the formats' extractions say, per field.
type textView struct {
	stated map[string]textStatement
	// vetoed fields have a `disagree` fact in some format: the printed text
	// is unclear, so nothing is proposed for them.
	vetoed map[string]bool
}

// formatText is one format's reading of the text, per field. A field it
// doesn't state is absent from stated.
type formatText struct {
	stated map[string]textStatement
	vetoed map[string]bool
}

func (v *view) collectText() {
	v.text = textView{stated: map[string]textStatement{}, vetoed: map[string]bool{}}
	var reads []formatText
	for _, f := range v.p.Formats {
		if f.Extraction == nil || !slices.ContainsFunc(f.Extraction.Extractors, func(e evidence.ExtractorRun) bool { return e.State == evidence.ExtractOK }) {
			continue
		}
		reads = append(reads, readText(f.Extraction, f.Excerpt, v.version))
	}
	for _, field := range textFields {
		var st *textStatement
		conflict := false
		for _, r := range reads {
			if r.vetoed[field] {
				v.text.vetoed[field] = true
			}
			s, ok := r.stated[field]
			switch {
			case !ok:
			case st == nil:
				st = &s
			case st.key != s.key:
				conflict = true
			default:
				st.ebook, st.onlyPrinted = st.ebook && s.ebook, st.onlyPrinted && s.onlyPrinted
			}
		}
		switch {
		case conflict:
			// The printed text is contested: no statement, and no other
			// root may propose the field either.
			v.reasons[reasonFormatsTextConflict] = true
			v.text.vetoed[field] = true
		case st != nil && !v.text.vetoed[field]:
			v.text.stated[field] = *st
		}
	}
}

// readText reads one extraction's agreed facts (ADR-036 section 1). Only
// agreed facts state a value; a disagree vetoes the field.
func readText(x *evidence.Extraction, ex *evidence.Excerpt, version int) formatText {
	r := formatText{stated: map[string]textStatement{}, vetoed: map[string]bool{}}
	fact := func(key string) (evidence.AgreedFact, bool) {
		i := slices.IndexFunc(x.Agreed, func(a evidence.AgreedFact) bool { return a.Key == key })
		if i < 0 {
			return evidence.AgreedFact{}, false
		}
		return x.Agreed[i], true
	}
	agreed := func(f evidence.AgreedFact, ok bool) bool { return ok && f.Outcome == evidence.AgreeAgreed }
	disagree := func(f evidence.AgreedFact, ok bool) bool { return ok && f.Outcome == evidence.AgreeDisagree }

	title, hasTitle := fact("title")
	sub, hasSub := fact("subtitle")
	switch {
	case disagree(title, hasTitle) || disagree(sub, hasSub):
		r.vetoed[fieldTitle] = true
	case agreed(title, hasTitle) && len(title.Values) == 1:
		text := clean(title.Values[0].Value)
		ok := true
		if agreed(sub, hasSub) {
			// Read the value only when there is exactly one: a subtitle
			// without a value states no title (evidence.Check refuses it).
			if ok = len(sub.Values) == 1; ok {
				text = joinSubtitle(text, clean(sub.Values[0].Value), version)
			}
		}
		if k := findings.TitleKey(text); ok && k.Valid && findings.JunkTitleReason(text) == "" {
			r.stated[fieldTitle] = textStatement{key: k.Value, display: []string{text}}
		}
	}

	contrib, hasContrib := fact("contributor")
	switch {
	case disagree(contrib, hasContrib):
		r.vetoed[fieldAuthors] = true
	case agreed(contrib, hasContrib):
		var names []string
		for _, c := range contrib.Values {
			if n := clean(c.Value); c.Role == "author" && n != "" {
				names = append(names, n)
			}
		}
		if k := findings.AuthorsKey(names); k.Valid && !textPlaceholderAuthors(k.Value) {
			r.stated[fieldAuthors] = textStatement{key: k.Value, display: names}
		}
	}

	imprint, hasImprint := fact("imprint")
	switch {
	case disagree(imprint, hasImprint):
		r.vetoed[fieldPublisher] = true
	case agreed(imprint, hasImprint) && len(imprint.Values) == 1:
		name := clean(imprint.Values[0].Value)
		if k := findings.PublisherKey(name); k.Valid && findings.JunkPublisherReason(name) == "" {
			r.stated[fieldPublisher] = textStatement{key: k.Value, display: []string{name}}
		}
	}

	isbn, hasISBN := fact("isbn")
	switch {
	case disagree(isbn, hasISBN):
		r.vetoed[fieldISBN] = true
	case agreed(isbn, hasISBN):
		front := frontISBNs(x, ex)
		counted := slices.DeleteFunc(slices.Clone(isbn.Values), func(v evidence.AgreedValue) bool { return !front.grounded(v) })
		if t, ebook, ok := printedISBN(counted); ok {
			r.stated[fieldISBN] = textStatement{key: t, display: []string{t}, ebook: ebook, onlyPrinted: len(front.printed) == 1}
		}
	}
	return r
}

// frontClaims are the grounded ISBN claims outside the excerpt's back units:
// what the book prints for itself, not an advertisement for other books.
type frontClaims struct {
	claims  []evidence.ExtractedClaim
	printed []string // the distinct values, by any source
}

func frontISBNs(x *evidence.Extraction, ex *evidence.Excerpt) frontClaims {
	var f frontClaims
	if ex == nil {
		return f
	}
	back := map[int]bool{}
	for _, u := range ex.Units {
		back[u.Pos] = u.Landmark == evidence.LandmarkBack
	}
	for _, c := range x.Claims {
		if c.Field != "isbn" || c.Status != "grounded" {
			continue
		}
		if isBack, known := back[c.Unit]; !known || isBack {
			continue
		}
		f.claims = append(f.claims, c)
		if !slices.Contains(f.printed, c.Value) {
			f.printed = append(f.printed, c.Value)
		}
	}
	return f
}

// grounded reports whether both the parser and an extractor ground the value
// with its label outside the back units (ADR-036 section 1).
func (f frontClaims) grounded(v evidence.AgreedValue) bool {
	var parser, model bool
	for _, c := range f.claims {
		if c.Value != v.Value || c.Label != v.Label {
			continue
		}
		if c.Source == evidence.ExtractParser {
			parser = true
		} else {
			model = true
		}
	}
	return parser && model
}

// printedISBN chooses the ISBN the values name, as version 6 does for the
// front matter (ADR-031, Q1): a value labelled hardcover, paperback, print or
// PDF never counts. If any value is labelled ebook, exactly one of those must
// remain; otherwise exactly one unlabelled value. Anything else is ambiguous.
// ebook reports whether the choice was the ebook-labelled one.
func printedISBN(vals []evidence.AgreedValue) (isbn string, ebook, ok bool) {
	var ebooks, plain []string
	for _, v := range vals {
		var into *[]string
		switch v.Label {
		case evidence.QualifierEbook:
			into = &ebooks
		case "":
			into = &plain
		default:
			continue
		}
		c, valid := findings.CanonicalISBN(v.Value)
		if !valid {
			return "", false, false
		}
		if !slices.Contains(*into, c) {
			*into = append(*into, c)
		}
	}
	switch {
	case len(ebooks) > 0:
		if len(ebooks) == 1 {
			return ebooks[0], true, true
		}
	case len(plain) == 1:
		return plain[0], false, true
	}
	return "", false, false
}

// textPlaceholderNames are the author names that stand for no author when
// the text fills them in: ADR-035's other-work list without the names that
// can be real attributions (ADR-036 section 3.3).
var textPlaceholderNames = []string{
	"unknown", "unknown author", "desconocido", "autor desconocido", //nolint:misspell // Spanish
	"unbekannt", "inconnu", "n/a", "none",
}

// textPlaceholderAuthors reports whether an authors key is a single
// placeholder name from textPlaceholderNames. An empty list is not one: callers
// test emptiness on the raw value.
func textPlaceholderAuthors(key string) bool {
	names := authorList(key)
	return len(names) == 1 && slices.Contains(textPlaceholderNames, names[0])
}

// publisherSuffixes are the corporate suffixes dropped when comparing
// publishers (ADR-036 section 3.1), as folded words. Longer phrases first.
var publisherSuffixes = [][]string{
	{"publishing", "group"}, {"publishing"}, {"publishers"}, {"verlag"},
	{"inc"}, {"ltd"}, {"llc"}, {"gmbh"}, {"sa"}, {"sl"},
	{"books"}, {"company"}, {"co"}, {"corp"},
}

// publisherWords folds a publisher: case, punctuation, and trailing
// corporate suffixes. It never reduces a name to nothing.
func publisherWords(s string) []string {
	s = strings.NewReplacer("&", " and ", ".", "", "'", "", "’", "").Replace(strings.ToLower(s))
	words := strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsMark(r) && !unicode.IsDigit(r) })
	for changed := true; changed; {
		changed = false
		for _, suffix := range publisherSuffixes {
			if len(words) > len(suffix) && slices.Equal(words[len(words)-len(suffix):], suffix) {
				words, changed = words[:len(words)-len(suffix)], true
				break
			}
		}
	}
	return words
}

// publisherEquivalent reports whether two publishers are the same imprint up
// to case, punctuation, "&" for "and", spacing and a corporate suffix
// (ADR-031: not corrections).
func publisherEquivalent(a, b string) bool {
	wa, wb := strings.Join(publisherWords(a), ""), strings.Join(publisherWords(b), "")
	return wa != "" && wa == wb
}

// titlesEquivalentV8 is version 7's title equivalence, also ignoring
// punctuation, straight and curly quotes and the dash variants (ADR-031,
// Title 4). It only removes proposals.
func titlesEquivalentV8(a, b, lang string) bool {
	if titlesEquivalentV7(a, b, lang) {
		return true
	}
	pa, pb := plainTitle(a), plainTitle(b)
	return pa != "" && titlesEquivalentV7(pa, pb, lang)
}

var quoteDashFold = strings.NewReplacer("‘", "'", "’", "'", "ʼ", "'", "“", `"`, "”", `"`, "–", "-", "—", "-", "−", "-", "‑", "-", "―", "-")

// plainTitle folds quotes and dashes, then keeps only the words of a title.
func plainTitle(s string) string {
	words := strings.FieldsFunc(quoteDashFold.Replace(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsMark(r) && !unicode.IsDigit(r) })
	return strings.Join(words, " ")
}

// shortensV8 reports whether proposed's words are a strict subsequence of
// calibre's, so that words were dropped from anywhere in the title.
func shortensV8(calibre, proposed string) bool {
	c, p := titleWords(calibre), titleWords(proposed)
	if len(p) == 0 || len(p) >= len(c) {
		return false
	}
	i := 0
	for _, w := range c {
		if i < len(p) && w == p[i] {
			i++
		}
	}
	return i == len(p)
}

// coveredBy reports whether every author of root is among the text's.
func coveredBy(root, text []string) bool {
	have := normNames(text)
	return !slices.ContainsFunc(normNames(root), func(n string) bool { return !slices.Contains(have, n) })
}

// sameText reports whether two keys of a field are the same value under
// ADR-036 section 3.1's equivalences.
func (v *view) sameText(field, a, b string) bool {
	switch field {
	case fieldTitle:
		return titlesEquivalentV8(a, b, v.titleLang())
	case fieldAuthors:
		return authorsConsistentV7(authorList(a), authorList(b))
	case fieldPublisher:
		return publisherEquivalent(a, b)
	}
	return a == b
}

// titleNoiseIsText reports whether Calibre's title is the text title plus
// noise, and nothing else: the noise removed leaves exactly the text title.
func (v *view) titleNoiseIsText(st textStatement) bool {
	stripped, reasons := findings.StripTitleNoise(v.p.Calibre.Title, v.p.Calibre.Authors)
	if len(reasons) == 0 {
		return false
	}
	k := findings.TitleKey(stripped)
	return k.Valid && k.Value == st.key
}

// textEquivalent reports whether the text value and Calibre's are the same
// under the style guide's "not corrections" (ADR-036 section 3.1). A title
// that is the text title plus noise is not: removing noise is section 3.4's.
func (v *view) textEquivalent(field string, st textStatement) bool {
	if field == fieldISBN {
		cal := calibreISBNs(v.p.Calibre)
		return len(cal) == 1 && cal[0] == st.key
	}
	cal := calibreKey(v.p.Calibre, field)
	if !cal.Valid {
		return false
	}
	if field == fieldTitle && v.titleNoiseIsText(st) {
		return false
	}
	return v.sameText(field, st.key, cal.Value)
}

// calibreEmptyOrJunk reports whether Calibre's value for the field is empty or
// junk, the first condition of a single-source text proposal (ADR-036 3.3).
func (v *view) calibreEmptyOrJunk(field string) bool {
	cal := v.p.Calibre
	switch field {
	case fieldTitle:
		return findings.JunkTitleReason(cal.Title) != ""
	case fieldAuthors:
		if len(calibreValue(cal, fieldAuthors)) == 0 {
			return true
		}
		k := findings.AuthorsKey(cal.Authors)
		return k.Valid && textPlaceholderAuthors(k.Value)
	case fieldPublisher:
		return cal.Publisher == "" || findings.JunkPublisherReason(cal.Publisher) != ""
	case fieldISBN:
		return len(calibreValue(cal, fieldISBN)) == 0
	}
	return false
}

// textDissent reports whether a root that may back a fix states a value for
// the field that is neither empty nor junk and differs from the text
// (ADR-036 section 3.3). Records of a root are never recorded-only ones: those
// were not collected.
func (v *view) textDissent(field string, st textStatement) bool {
	if field == fieldISBN {
		for _, f := range v.formats {
			if slices.ContainsFunc(f.fields.ISBNs, func(i string) bool { return i != st.key }) {
				return true
			}
		}
		return len(v.roots) > 0 && v.identity != st.key
	}
	differs := func(key findings.Key, raw string) bool {
		if !key.Valid || v.junkValue(field, raw, key.Value) {
			return false
		}
		if field == fieldAuthors {
			// A root listing people the text doesn't dissents; from version 10
			// so does one whose list is not the same people as the text's.
			return v.authorsDissent(authorList(key.Value), authorList(st.key))
		}
		return !v.sameText(field, st.key, key.Value)
	}
	for _, f := range v.formats {
		if f.raw != nil && differs(formatKey(f.fields, field), rawOf(f.raw.Title, f.raw.Publisher, field)) {
			return true
		}
	}
	for _, rt := range v.roots {
		for _, r := range rt.records {
			if !v.recordSpeaks(r, field) {
				continue
			}
			if differs(recordKey(r, field), rawOf(r.Title, r.Publisher, field)) {
				// A record that keeps the subtitle in its own field states
				// the joined title too.
				if field == fieldTitle && r.Subtitle != "" {
					if full := findings.TitleKey(joinSubtitle(r.Title, r.Subtitle, v.version)); full.Valid && v.sameText(field, st.key, full.Value) {
						continue
					}
				}
				return true
			}
		}
	}
	return false
}

func rawOf(title, publisher, field string) string {
	switch field {
	case fieldTitle:
		return title
	case fieldPublisher:
		return publisher
	}
	return ""
}

// junkValue reports whether a root's value for the field is junk by the
// detector of section 3.3.
func (v *view) junkValue(field, raw, key string) bool {
	switch field {
	case fieldTitle:
		return findings.JunkTitleReason(raw) != ""
	case fieldPublisher:
		return findings.JunkPublisherReason(raw) != ""
	case fieldAuthors:
		return textPlaceholderAuthors(key)
	}
	return false
}

// flagTextDisagree records that the field's text and other roots can't agree.
func (v *view) flagTextDisagree(field string) {
	v.reasons[reasonTextRootsDisagree] = true
	v.textDisagree = append(v.textDisagree, field)
}

// textProposal applies section 3 to a field with a text statement.
func (v *view) textProposal(field string, st textStatement) *evidence.Proposal {
	if v.textEquivalent(field, st) {
		return nil
	}
	if field == fieldISBN {
		return v.textISBNProposal(st)
	}
	if field == fieldPublisher && !v.calibreEmptyOrJunk(fieldPublisher) {
		return nil // a real publisher is never replaced from the text (ADR-031, Publisher 2 and 3)
	}
	if (field != fieldTitle && field != fieldAuthors) || !v.strictConflict[field] {
		stmts := append(v.textStatements(field, st.key), statement{name: evidence.RootText, class: classText, keys: []string{st.key}})
		slices.SortFunc(stmts, func(a, b statement) int { return rank(a.name) - rank(b.name) })
		var support []string
		authority, vetoed := false, false
		for _, s := range stmts {
			switch {
			case len(s.keys) == 1 && s.keys[0] == st.key:
				support = append(support, s.name)
				authority = authority || s.class >= classOfficial
			case len(s.keys) > 0 && s.class >= classOfficial:
				vetoed = true
			}
		}
		if len(support) >= 2 && authority && !vetoed {
			proposed := st.display
			if support[0] != evidence.RootText {
				proposed = v.display(field, st.key, support[0])
			}
			if field == fieldTitle && len(proposed) == 1 && shortensV8(v.p.Calibre.Title, proposed[0]) &&
				!v.titleNoiseIsText(st) && !v.calibreEmptyOrJunk(fieldTitle) {
				return nil // words are never dropped from a title but its noise (ADR-026)
			}
			if !v.mayReplace(field, st, support) {
				return nil
			}
			if field == fieldAuthors && reformsOnly(v.p.Calibre.Authors, proposed) {
				return nil
			}
			return &evidence.Proposal{Field: field, Current: calibreValue(v.p.Calibre, field), Proposed: proposed, Roots: support}
		}
	}
	return v.textAlone(field, st)
}

// mayReplace is the record rule: replacing a real Calibre value needs a record
// among the supporters. Fills, junk and placeholder replacements, and exact
// noise removal need none.
func (v *view) mayReplace(field string, st textStatement, support []string) bool {
	switch {
	case len(calibreValue(v.p.Calibre, field)) == 0, v.calibreEmptyOrJunk(field), field == fieldTitle && v.titleNoiseIsText(st):
		return true
	}
	return slices.ContainsFunc(support, func(name string) bool { return name != evidence.RootFile && name != evidence.RootText })
}

// textAlone is section 3.3: the text proposes alone over an empty or junk
// value when no root dissents; otherwise a dissent is flagged.
func (v *view) textAlone(field string, st textStatement) *evidence.Proposal {
	dissent := v.textDissent(field, st)
	if dissent {
		v.flagTextDisagree(field)
		return nil
	}
	if !v.calibreEmptyOrJunk(field) {
		return nil
	}
	if field == fieldISBN && !st.ebook && !st.onlyPrinted {
		return nil // a series, set or audiobook ISBN can sit beside the book's own
	}
	proposed := v.textDisplay(field, st)
	if v.version >= 10 && len(proposed) == 0 {
		return nil // printed in capitals, and no root gives a spelling
	}
	return &evidence.Proposal{Field: field, Current: calibreValue(v.p.Calibre, field), Proposed: proposed, Roots: []string{evidence.RootText}}
}

// textISBNProposal is section 3 for the ISBN: the file supports the text ISBN
// when it names it alone, a record when it is for that ISBN and matches the
// book the way version 6's ISBN proposal requires: the file's exact title
// key, and the files in full.
func (v *view) textISBNProposal(st textStatement) *evidence.Proposal {
	t := st.key
	support := []string{evidence.RootText}
	if v.identity == t {
		if !v.reasons[reasonIdentityFrontMatter] && !v.textDissent(fieldISBN, st) {
			support = append(support, evidence.RootFile)
		}
		titles := v.fileKeys(fieldTitle)
		if want, ok := v.want(); ok && len(titles) == 1 {
			strict := v.strictMatcher(want)
			match := func(r evidence.SourceRecord) bool {
				k := recordKey(r, fieldTitle)
				return k.Valid && k.Value == titles[0] && strict(r)
			}
			for _, rt := range v.roots {
				if slices.ContainsFunc(rt.records, match) {
					support = append(support, rt.name)
				}
			}
		}
	}
	if len(support) >= 2 && (len(calibreValue(v.p.Calibre, fieldISBN)) == 0 || v.mayReplace(fieldISBN, st, support)) {
		slices.SortFunc(support, func(a, b string) int { return rank(a) - rank(b) })
		return &evidence.Proposal{Field: fieldISBN, Current: calibreValue(v.p.Calibre, fieldISBN), Proposed: []string{t}, Roots: support}
	}
	return v.textAlone(fieldISBN, st)
}

// textOtherWork is section 4: the text title and the text authors both differ
// from Calibre's, which says the file is another work than Calibre's record.
// A junk title and placeholder authors are fills, not another work.
func (v *view) textOtherWork() bool {
	title, authors := v.text.stated[fieldTitle], v.text.stated[fieldAuthors]
	ct, ca := findings.TitleKey(v.p.Calibre.Title), findings.AuthorsKey(v.p.Calibre.Authors)
	if title.key == "" || authors.key == "" || !ct.Valid || !ca.Valid {
		return false
	}
	if findings.JunkTitleReason(v.p.Calibre.Title) != "" || placeholderAuthors(ca.Value) {
		return false
	}
	for _, a := range v.p.Calibre.Authors {
		if s := surname(a); s != "" && slices.ContainsFunc(authors.display, func(n string) bool { return surname(n) == s }) {
			return false // the same author, or a form of the name (as ADR-035's guard)
		}
	}
	return !v.sameText(fieldTitle, title.key, ct.Value) && !v.titleNoiseIsText(title) &&
		!v.sameText(fieldAuthors, authors.key, ca.Value)
}
