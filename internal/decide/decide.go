// Package decide derives a book's tier, the tier's reasons and per-field
// proposals from its sealed file evidence and source records (spec section
// 7, ADR-020). It makes no requests and changes nothing.
package decide

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/findings"
	"github.com/felixapel/calibre-bookwarden/internal/sources"
)

// Version fixes the tier and proposal rules, including the findings
// comparison keys they use and the Open Library root-sharing table
// (sources.SharedRoot). Changing any of them needs a new version and a reader
// that keeps the historical derivation.
//
// 2: a title proposal never shortens Calibre's title (ADR-026).
// 3: an authors proposal never only drops or re-forms Calibre's authors (ADR-027).
// 4: title and author sort proposals from Calibre's own rules (ADR-028).
// 5: a record matches the files despite a missing language, extra people, its
// own subtitle, a trailing parenthetical, a leading article or "&" (ADR-030).
// 6: with no metadata ISBN, an unambiguous copyright or title page ISBN selects
// the identity once a record's title confirms it; the other-work guard; an
// ISBN proposal needs the full record match (ADR-035 part 2).
// 7: records and files agree despite a leading article, "&", an apostrophe
// variant, an imprint series, a honorific, initials punctuation or a few
// contributors listed as authors; only tiers move, never a proposed value
// (ADR-030, "Decision v7 tolerances").
// 8: the book's printed text, from the sealed extraction, is a root; a title
// shortens only by its noise; the other-work guard reads the text; an ISBN
// printed on the copyright page may be proposed (ADR-036).
// 9: a pubdate proposal that only shifts Calibre's time zone is dropped, and a
// text-rooted publisher printed in capitals is shown in title case (ADR-036,
// "Decision v9").
// 10: corrections to the text root (ADR-045): authors dissent unless the lists
// are the same people; a text-alone title, author list or publisher in
// capitals takes another root's spelling or is not proposed; a pubdate Calibre
// would shift on write is not proposed; a record speaks about the title and
// authors only when it is the same book in the same language; a record's own
// subtitle field joins its title in the text path; without a text title a
// title shortens only by its noise; a title ending in ":", "?" or "!" joins
// its subtitle with a space.
const Version = 10

var (
	ErrUnknownVersion = errors.New("decide: unknown decision version")
	ErrMismatch       = errors.New("decide: stored decision differs from derivation")
)

// Decision is a book's tier, its sorted reason codes and its proposals.
type Decision struct {
	Tier      string
	Reasons   []string
	Proposals []evidence.Proposal
	// textDisagree are the fields whose text and other roots can't agree
	// (reason text_roots_disagree); they show as findings, never sealed.
	textDisagree []string
}

// sharedRoot names the national library whose root an Open Library record
// shares; tests replace it.
var sharedRoot = sources.SharedRoot

const (
	classExternal = iota
	classOfficial
	classInternal
)

var sourceClass = map[string]int{
	evidence.SourceDNB: classOfficial, evidence.SourceBNE: classOfficial,
	evidence.SourceOpenLibrary: classExternal, evidence.SourceGoogleBooks: classExternal,
}

// recordedOnly reports whether a source's records are sealed with the
// evidence but not decided on at this decision version: Wikidata and
// Hardcover, up to version 10 (ADR-035, ADR-030 amendment, ADR-036 section 6).
// Whether they count as independent roots is a precision decision for a later
// version.
func recordedOnly(source string, version int) bool {
	// Up to version 10: independence not established (ADR-035 part 2).
	return version <= 10 && (source == evidence.SourceWikidata || source == evidence.SourceHardcover)
}

// Record values at M3.1's capture cap may have been cut, so they're unverified.
const (
	recordCapRunes = 511
	recordCapItems = 16
)

const (
	fieldTitle     = "title"
	fieldAuthors   = "authors"
	fieldLanguages = "languages"
	fieldISBN      = "isbn"
	fieldPublisher = "publisher"
	fieldPubdate   = "pubdate"
)

var coreFields = []string{fieldTitle, fieldAuthors, fieldLanguages}

// tierCCodes are the reasons that make a book Tier C.
var tierCCodes = map[string]bool{
	reasonOtherWork:         true,
	"formats_isbn_conflict": true, "formats_title_conflict": true, "formats_authors_conflict": true, "formats_languages_conflict": true,
	"records_title_conflict": true, "records_authors_conflict": true, "records_languages_conflict": true,
}

type fileFormat struct {
	name   string
	raw    *evidence.FileMetadata
	fields findings.FormatFields
	hints  *evidence.ISBNHints
}

type root struct {
	name    string
	class   int
	records []evidence.SourceRecord
}

// view is what the rules read from a package.
type view struct {
	version  int
	p        evidence.Package
	formats  []fileFormat // eligible formats, by name
	identity string
	roots    map[string]*root
	reasons  map[string]bool
	// strictConflict and strictDisagree are the record conflicts and the
	// record-against-file disagreement under version 6's exact comparison.
	// Version 7's tolerances may clear the reasons, but never these: proposals
	// and identities keep the strict verdict.
	strictConflict map[string]bool
	strictDisagree bool
	// text is the printed text's statements (version 8 on); textDisagree the
	// fields it couldn't agree on with the other roots.
	text         textView
	textDisagree []string
}

// Decide derives the current decision for p.
func Decide(ctx context.Context, p evidence.Package) (Decision, error) {
	return AtVersion(ctx, p, Version)
}

// AtVersion derives p's decision under the rules of version, 1 to Version.
func AtVersion(ctx context.Context, p evidence.Package, version int) (Decision, error) {
	if version < 1 || version > Version {
		return Decision{}, ErrUnknownVersion
	}
	if err := ctx.Err(); err != nil {
		return Decision{}, err
	}
	v, err := newView(ctx, p, version)
	if err != nil {
		return Decision{}, err
	}
	d := Decision{Tier: evidence.TierA}
	for _, r := range slices.Sorted(maps.Keys(v.reasons)) {
		switch {
		case tierCCodes[r]:
			d.Tier = evidence.TierC
		case r == reasonTextRootsDisagree:
			// No tier effect; the proposals add it.
		case d.Tier == evidence.TierA:
			d.Tier = evidence.TierB
		}
	}
	if d.Tier != evidence.TierC {
		d.Proposals = v.proposals()
	}
	d.Reasons, d.textDisagree = slices.Sorted(maps.Keys(v.reasons)), v.textDisagree
	if len(d.Reasons) == 0 {
		d.Reasons = nil
	}
	if version >= 4 && !v.reasons[reasonOtherWork] {
		d.Proposals = append(d.Proposals, v.sortProposals(d.Proposals)...)
	}
	return d, nil
}

func newView(ctx context.Context, p evidence.Package, version int) (*view, error) {
	v := &view{version: version, p: p, roots: map[string]*root{}, reasons: map[string]bool{}, strictConflict: map[string]bool{}}
	if len(p.Formats) == 0 {
		v.reasons["no_formats"] = true
	}
	for _, f := range p.Formats {
		ff, err := findings.Fields(f)
		if err != nil {
			return nil, fmt.Errorf("decide: %w", err)
		}
		if !ff.Eligible {
			v.reasons["format_unreadable"] = true
			continue
		}
		v.formats = append(v.formats, fileFormat{name: f.Format, raw: f.Metadata, fields: ff, hints: f.ISBNHints})
	}
	slices.SortFunc(v.formats, func(a, b fileFormat) int { return strings.Compare(a.name, b.name) })
	v.fileReasons()
	provisional := ""
	if version >= 6 && v.identity == "" {
		provisional = v.frontMatterCandidate()
		v.identity = provisional
	}
	if err := v.collectRecords(ctx); err != nil {
		return nil, err
	}
	if provisional != "" {
		v.confirmFrontMatter()
	}
	v.recordReasons()
	if provisional != "" && v.disputedFrontMatter() {
		v.dropFrontMatter()
	}
	if version >= 8 {
		v.collectText()
	}
	if version >= 6 && v.otherWork() {
		v.reasons[reasonOtherWork] = true
	}
	return v, nil
}

func formatKey(ff findings.FormatFields, field string) findings.Key {
	switch field {
	case fieldTitle:
		return ff.Title
	case fieldAuthors:
		return ff.Authors
	case fieldLanguages:
		return ff.Languages
	case fieldPublisher:
		return ff.Publisher
	case fieldPubdate:
		return ff.Date
	}
	return findings.Key{}
}

func atCap(s string) bool { return utf8.RuneCountInString(s) >= recordCapRunes }

func listAtCap(list []string) bool {
	return len(list) >= recordCapItems || slices.ContainsFunc(list, atCap)
}

func recordKey(r evidence.SourceRecord, field string) findings.Key {
	switch field {
	case fieldTitle:
		if !atCap(r.Title) {
			return findings.TitleKey(r.Title)
		}
	case fieldAuthors:
		if !listAtCap(r.Authors) {
			return findings.AuthorsKey(r.Authors)
		}
	case fieldLanguages:
		if !listAtCap(r.Languages) {
			return findings.LanguagesKey(r.Languages)
		}
	case fieldPublisher:
		if !atCap(r.Publisher) {
			return findings.PublisherKey(r.Publisher)
		}
	case fieldPubdate:
		return findings.DateKey(r.Date)
	}
	return findings.Key{}
}

// fileReasons covers the files: ISBNs, incomplete and conflicting core
// fields, and the identity ISBN (ADR-020).
func (v *view) fileReasons() {
	identity, usable := "", true
	var sets [][]string
	for _, f := range v.formats {
		ff := f.fields
		switch {
		case ff.ISBNFlagged || len(ff.ISBNs) == 0:
			v.reasons["isbn_missing"] = true
			usable = usable && !ff.ISBNFlagged
		case len(ff.ISBNs) > 1:
			v.reasons["isbn_ambiguous"] = true
			usable = false
		case identity == "":
			identity = ff.ISBNs[0]
		case identity != ff.ISBNs[0]:
			usable = false
		}
		if len(ff.ISBNs) > 0 {
			sets = append(sets, ff.ISBNs)
		}
		for _, field := range coreFields {
			if !formatKey(ff, field).Valid {
				v.reasons[field+"_incomplete"] = true
			}
		}
	}
	if len(sets) > 1 {
		common := sets[0]
		for _, s := range sets[1:] {
			common = slices.DeleteFunc(slices.Clone(common), func(isbn string) bool { return !slices.Contains(s, isbn) })
		}
		if len(common) == 0 {
			v.reasons["formats_isbn_conflict"] = true
		}
	}
	for _, field := range coreFields {
		if len(v.fileKeys(field)) > 1 {
			v.reasons["formats_"+field+"_conflict"] = true
		}
	}
	if usable {
		v.identity = identity
	}
}

// fileKeys returns the distinct valid keys the eligible formats state.
func (v *view) fileKeys(field string) []string {
	var keys []string
	for _, f := range v.formats {
		if k := formatKey(f.fields, field); k.Valid && !slices.Contains(keys, k.Value) {
			keys = append(keys, k.Value)
		}
	}
	return keys
}

// collectRecords groups the identity ISBN's ok records into roots.
func (v *view) collectRecords(ctx context.Context) error {
	if v.identity == "" {
		return nil
	}
	for _, l := range v.p.Lookups {
		if l.ISBN != v.identity {
			continue
		}
		for _, res := range l.Results {
			if err := ctx.Err(); err != nil {
				return err
			}
			if res.Outcome != evidence.SourceOK {
				continue
			}
			if recordedOnly(res.Source, v.version) {
				continue
			}
			class, known := sourceClass[res.Source]
			if !known {
				return fmt.Errorf("decide: source: %w", findings.ErrInvalidEvidence)
			}
			for _, r := range res.Records {
				if !slices.Contains(r.ISBNs, v.identity) {
					continue
				}
				name := res.Source
				if name == evidence.SourceOpenLibrary {
					if shared := sharedRoot(r); shared != "" {
						name = shared
					}
				}
				rt := v.roots[name]
				if rt == nil {
					rt = &root{name: name}
					v.roots[name] = rt
				}
				rt.class = max(rt.class, class)
				rt.records = append(rt.records, r)
			}
		}
	}
	return nil
}

// rootKeys returns the distinct valid keys a root's records state.
func (rt *root) keys(field string) []string {
	var keys []string
	for _, r := range rt.records {
		if k := recordKey(r, field); k.Valid && !slices.Contains(keys, k.Value) {
			keys = append(keys, k.Value)
		}
	}
	return keys
}

func (v *view) recordReasons() {
	if len(v.roots) == 0 {
		v.reasons["no_record"] = true
		return
	}
	// Two identity records stating different values conflict, whether they
	// come from one root or two (spec section 7: "exact records conflict").
	for _, field := range coreFields {
		var keys []string
		for _, rt := range v.roots {
			for _, k := range rt.keys(field) {
				if !slices.Contains(keys, k) {
					keys = append(keys, k)
				}
			}
		}
		if len(keys) > 1 {
			v.strictConflict[field] = true
			// The files' own keys must be consistent with every record's too.
			all := slices.Clone(keys)
			for _, k := range v.fileKeys(field) {
				if !slices.Contains(all, k) {
					all = append(all, k)
				}
			}
			if v.version < 7 || !v.keysConsistent(field, all) {
				v.reasons["records_"+field+"_conflict"] = true
			}
		}
	}
	want, ok := v.want()
	if !ok {
		return // the files' own reasons explain the tier
	}
	matches, strict := v.matcher(want), v.strictMatcher(want)
	anyMatch, anyStrict := false, false
	for _, rt := range v.roots {
		for _, r := range rt.records {
			anyMatch = anyMatch || matches(r)
			anyStrict = anyStrict || strict(r)
		}
	}
	v.strictDisagree = !anyStrict
	if !anyMatch {
		v.reasons["records_disagree"] = true
	}
}

// want returns the files' single title, authors and languages keys, or false
// when a format lacks one or the formats state several.
func (v *view) want() (map[string]string, bool) {
	want := map[string]string{}
	for _, field := range coreFields {
		keys := v.fileKeys(field)
		if len(v.formats) == 0 || len(keys) != 1 || v.reasons[field+"_incomplete"] {
			return nil, false
		}
		want[field] = keys[0]
	}
	return want, true
}

// matcher returns the version's test of a record against the files' keys:
// version 7's tolerances widen version 5's match for the tier verdict.
func (v *view) matcher(want map[string]string) func(evidence.SourceRecord) bool {
	if v.version < 7 {
		return v.strictMatcher(want)
	}
	other, lang := v.otherLanguage(want), v.titleLang()
	return func(r evidence.SourceRecord) bool { return recordMatchesV7(r, want, other, lang) }
}

// strictMatcher is the match up to version 6, which version 7 keeps wherever
// a match selects values or an identity: the ISBN proposal and the
// front-matter confirmation.
func (v *view) strictMatcher(want map[string]string) func(evidence.SourceRecord) bool {
	if v.version < 5 {
		return func(r evidence.SourceRecord) bool { return recordMatches(r, want) }
	}
	other := v.otherLanguage(want)
	return func(r evidence.SourceRecord) bool { return recordMatchesV5(r, want, other) }
}

// otherLanguage reports whether an identity record states a language other
// than the files'. A record that states no language may vouch for the files'
// only while none does (ADR-030).
func (v *view) otherLanguage(want map[string]string) bool {
	other := false
	for _, rt := range v.roots {
		for _, k := range rt.keys(fieldLanguages) {
			other = other || k != want[fieldLanguages]
		}
	}
	return other
}

func recordMatches(r evidence.SourceRecord, want map[string]string) bool {
	for field, key := range want {
		if k := recordKey(r, field); !k.Valid || k.Value != key {
			return false
		}
	}
	return true
}
