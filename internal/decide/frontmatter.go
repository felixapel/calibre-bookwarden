package decide

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/findings"
)

// Reasons added by decision version 6 (ADR-035 part 2).
const (
	reasonOtherWork           = "other_work"
	reasonIdentityFrontMatter = "identity_frontmatter"
	reasonFrontMatterMissing  = "frontmatter_isbn_missing"
	reasonFrontMatterAmbig    = "frontmatter_isbn_ambiguous"
	reasonFrontMatterUnconf   = "frontmatter_isbn_unconfirmed"
)

// frontMatterCandidate is version 6's second identity path. It applies only
// when the metadata path gave no identity and no format states, flags or
// garbles an ISBN of its own: every eligible format has none. The candidates
// are the ISBNs EPUB front-matter hints found on a copyright or title page.
// If any is labelled for the e-book, only those stay (ADR-031, Q1: the ISBN
// of the edition the file is); otherwise the ones labelled hardcover,
// paperback, print or PDF go. Exactly one ISBN may remain. It is returned as
// a provisional identity, which confirmFrontMatter still has to confirm.
// Hints are never authority (spec section 7): the ISBN only selects whose
// records are read.
func (v *view) frontMatterCandidate() string {
	if len(v.formats) == 0 {
		return ""
	}
	for _, f := range v.formats {
		// A garbled ISBN (bad checksum) is no valid one, but it is the file's own.
		if f.fields.ISBNFlagged || len(f.fields.ISBNs) > 0 || f.raw != nil && len(f.raw.ISBNs) > 0 {
			return ""
		}
	}
	type candidate struct{ isbn, qualifier string }
	var cands []candidate
	hinted, ambiguous := false, false
	for _, f := range v.formats {
		h := f.hints
		if h == nil || h.Method != evidence.HintEPUBText {
			continue
		}
		if h.State != evidence.HintOK {
			return "" // a front matter that wasn't fully read may hide the right ISBN
		}
		if len(h.ISBNs) >= findings.MaxISBNs {
			ambiguous = true // the list was cut off
		}
		hinted = hinted || len(h.ISBNs) > 0
		for _, c := range h.Context {
			if c.Landmark != evidence.LandmarkCopyright && c.Landmark != evidence.LandmarkTitle {
				continue
			}
			if isbn, ok := findings.CanonicalISBN(c.ISBN); ok {
				cands = append(cands, candidate{isbn, c.Qualifier})
			}
		}
	}
	if slices.ContainsFunc(cands, func(c candidate) bool { return c.qualifier == evidence.QualifierEbook }) {
		cands = slices.DeleteFunc(cands, func(c candidate) bool { return c.qualifier != evidence.QualifierEbook })
	} else {
		cands = slices.DeleteFunc(cands, func(c candidate) bool { return c.qualifier != "" })
	}
	var isbns []string
	for _, c := range cands {
		if !slices.Contains(isbns, c.isbn) {
			isbns = append(isbns, c.isbn)
		}
	}
	switch {
	case ambiguous:
		v.reasons[reasonFrontMatterAmbig] = true
	case len(isbns) == 1:
		return isbns[0]
	case len(isbns) > 1:
		v.reasons[reasonFrontMatterAmbig] = true
	case hinted:
		v.reasons[reasonFrontMatterMissing] = true
	}
	return ""
}

// confirmFrontMatter keeps the provisional identity if one of its records
// (from a source that counts at this version) confirms it; otherwise it drops
// the identity and the records collected for it. When the files state one
// complete title, authors and language, a record confirms only by matching
// them in full (ADR-030) and by an exact title key, which is stricter than
// ADR-030's title rule so that an omnibus is not confirmed by one volume's
// record. Otherwise the exact title key of the files or of Calibre decides.
func (v *view) confirmFrontMatter() {
	titles := slices.Clone(v.fileKeys(fieldTitle))
	var matches func(evidence.SourceRecord) bool
	if want, ok := v.want(); ok {
		titles = []string{want[fieldTitle]}
		matches = v.strictMatcher(want)
	} else if k := findings.TitleKey(v.p.Calibre.Title); k.Valid && !slices.Contains(titles, k.Value) {
		titles = append(titles, k.Value)
	}
	for _, rt := range v.roots {
		for _, r := range rt.records {
			if (matches == nil || matches(r)) && slices.ContainsFunc(titles, func(t string) bool { return exactTitle(r, t) }) {
				v.reasons[reasonIdentityFrontMatter] = true
				return
			}
		}
	}
	v.dropFrontMatter()
}

// exactTitle reports whether the record's title, or its title and subtitle
// joined by ": ", has exactly the given key.
func exactTitle(r evidence.SourceRecord, key string) bool {
	if k := recordKey(r, fieldTitle); k.Valid && k.Value == key {
		return true
	}
	if full := r.Title + ": " + r.Subtitle; r.Subtitle != "" && !atCap(full) {
		k := findings.TitleKey(full)
		return k.Valid && k.Value == key
	}
	return false
}

// dropFrontMatter fails closed: no identity, none of its records, and the
// reasons that came from them.
func (v *view) dropFrontMatter() {
	v.identity, v.roots = "", map[string]*root{}
	for r := range v.reasons {
		if strings.HasPrefix(r, "records_") || r == reasonIdentityFrontMatter || r == reasonOtherWork {
			delete(v.reasons, r)
		}
	}
	v.reasons[reasonFrontMatterUnconf] = true
	v.reasons["no_record"] = true
}

// disputedFrontMatter reports whether a confirmed front-matter identity ended
// up with a record conflict or no matching record: a disputed identity gets no
// proposals.
func (v *view) disputedFrontMatter() bool {
	if !v.reasons[reasonIdentityFrontMatter] {
		return false
	}
	for _, field := range coreFields {
		if v.strictConflict[field] {
			return true
		}
	}
	return v.strictDisagree
}

// otherWork reports whether the identity's records, which all agree on one
// title and one authors list, state both differently from Calibre: the ISBN
// belongs to another work than the book Calibre holds. Nothing is proposed
// for such a book.
func (v *view) otherWork() bool {
	if v.version >= 8 && v.textOtherWork() {
		return true
	}
	title, authors := findings.TitleKey(v.p.Calibre.Title), findings.AuthorsKey(v.p.Calibre.Authors)
	if v.identity == "" || !title.Valid || !authors.Valid {
		return false
	}
	var titles, lists []string
	for _, rt := range v.roots {
		for _, k := range rt.keys(fieldTitle) {
			if !slices.Contains(titles, k) {
				titles = append(titles, k)
			}
		}
		for _, k := range rt.keys(fieldAuthors) {
			if !slices.Contains(lists, k) {
				lists = append(lists, k)
			}
		}
	}
	if v.version >= 7 {
		// Records that agree up to version 7's tolerances are one work; the
		// guard needs none of them to be Calibre's own spelling.
		if len(titles) == 0 || len(lists) == 0 || !v.keysConsistent(fieldTitle, titles) || !v.keysConsistent(fieldAuthors, lists) ||
			slices.Contains(titles, title.Value) || slices.Contains(lists, authors.Value) {
			return false
		}
	} else if len(titles) != 1 || len(lists) != 1 || titles[0] == title.Value || lists[0] == authors.Value {
		return false
	}
	if placeholderAuthors(authors.Value) {
		return false
	}
	// Plain keys differ for a dropped subtitle, a leading article or another
	// form of a name. Those are the same work: a record that matches
	// Calibre's title under ADR-030's rule or is its shortened form, or one
	// that shares an author's surname, keeps the guard from firing.
	calAuthors := make([]string, 0, len(v.p.Calibre.Authors))
	for _, a := range v.p.Calibre.Authors {
		if s := surname(a); s != "" {
			calAuthors = append(calAuthors, s)
		}
	}
	for _, rt := range v.roots {
		for _, r := range rt.records {
			if titleMatches(r, title.Value) || shortens(v.p.Calibre.Title, r.Title) {
				return false
			}
			for _, a := range r.Authors {
				if s := surname(a); s != "" && slices.Contains(calAuthors, s) {
					return false
				}
			}
		}
	}
	return true
}

// placeholderAuthorNames are the folded names Calibre or a file uses for a
// missing author; Calibre's own default is "Unknown". The list is closed:
// extending it needs a change to ADR-035. Calibre authors that are empty or
// only one of these make the records a fill, not another work.
var placeholderAuthorNames = []string{
	"unknown", "unknown author", "anonymous", "anónimo", "anonimo", "desconocido",
	"autor desconocido", //nolint:misspell // Spanish
	"unbekannt", "inconnu", "various", "varios", "n/a", "none",
}

// placeholderAuthors reports whether an authors key (a JSON list of folded
// names) is empty or a single placeholder name.
func placeholderAuthors(key string) bool {
	var names []string
	if json.Unmarshal([]byte(key), &names) != nil {
		return false
	}
	return len(names) == 0 || len(names) == 1 && slices.Contains(placeholderAuthorNames, names[0])
}
