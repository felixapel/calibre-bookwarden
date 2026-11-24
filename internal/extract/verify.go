package extract

import (
	"slices"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

// Fields a claim can be about.
const (
	FieldTitle       = "title"
	FieldSubtitle    = "subtitle"
	FieldContributor = "contributor"
	FieldImprint     = "imprint"
	FieldYear        = "year"
	FieldEdition     = "edition"
	FieldSeries      = "series"
	FieldSeriesIndex = "series_index"
	FieldISBN        = "isbn"
)

// Contributor roles. A translator, an editor or an introducer is never an
// author (ADR-031).
const (
	RoleAuthor       = "author"
	RoleEditor       = "editor"
	RoleTranslator   = "translator"
	RoleIllustrator  = "illustrator"
	RoleIntroduction = "introduction"
)

// What a year is the year of (Label of a year claim).
const (
	LabelCopyright      = "copyright"
	LabelFirstPublished = "first_published"
	LabelThisEdition    = "this_edition"
)

// Statuses of a verified claim.
const (
	StatusGrounded        = "grounded"
	StatusNotFound        = "not_found"
	StatusAmbiguous       = "ambiguous_quote"
	StatusValueNotDerived = "value_not_derived"
	StatusRoleUnproven    = "role_unproven"
	StatusExcludedSection = "excluded_section"
)

// SourceParser is the Source of a claim a deterministic parser made.
const SourceParser = "parser"

// Version is the version of the verifier's rules: normalisation, the section
// filter and the transforms. A package seals it with its claims, and Open
// re-verifies only claims sealed at the same version; any change to what a
// quote grounds bumps it, as decide versions are bumped.
const Version = 4

var knownFields = []string{
	FieldTitle, FieldSubtitle, FieldContributor, FieldImprint, FieldYear, FieldEdition, FieldSeries, FieldSeriesIndex, FieldISBN,
}

// Claim is what a model or a parser says the book prints: a field, its value,
// and the verbatim quote that shows it.
type Claim struct {
	Field string
	Value string
	// Role is the role of a contributor. Empty lets Verify fill in the role
	// the text proves.
	Role  string
	Quote string
	// Source says who made the claim: "parser", or an extractor's name.
	Source string
	// Label is the kind of a year (Label* constants) or the qualifier of an
	// ISBN (evidence.Qualifier*). Empty lets Verify fill in the label the text
	// proves; a label that the text doesn't give makes the claim not derived.
	Label string
}

// Verified is a claim after Verify. When Status is grounded, Value is the
// value the quote derives (canonical, so two claims for one printed line
// compare equal), and Role and Label are what the text proves.
type Verified struct {
	Claim
	// Unit is the position (ADR-032) of the excerpt unit the quote sits in, 0
	// for an excerpt with no units. Start and End are rune offsets of the
	// quote in the original excerpt text. All three are set whenever the
	// quote was found once or in one unit, whatever the verdict.
	Unit, Start, End int
	Status           string
	// Reason says why a value_not_derived value was refused (Refused*). It
	// is diagnostic only: not sealed, not stored, and no verdict reads it.
	Reason string `json:"-"`
}

// minQuoteRunes is the shortest quote a field accepts: a title of one rune
// would match almost any text.
func minQuoteRunes(field string) int {
	if field == FieldTitle || field == FieldSubtitle {
		return 2
	}
	return 1
}

// frame is where a quote was found, for the transforms that read around it.
type frame struct {
	x        Excerpt
	n        *Normalized
	s, e     int // the quote's span in the normalised text
	q        []rune
	unit     int // index into x.Units, or 0 with no units
	landmark string
}

// derived is what a transform returns: the value, its role and label, or why
// there is none.
type derived struct {
	value, role, label string
	status             string // "" when derived
	reason             string // refusalReason, when status is value_not_derived
}

func notDerived() derived      { return derived{status: StatusValueNotDerived} }
func unproven() derived        { return derived{status: StatusRoleUnproven} }
func got(value string) derived { return derived{value: value} }

// Verify decides whether the book's own text shows a claim (design section
// 5): the quote must be found in the excerpt, once or all in one unit (or,
// for a context-free field, alike in every unit); the span must not sit in
// an excluded section; and the value must be what a closed per-field
// transform makes of the quote.
func Verify(x Excerpt, c Claim) Verified {
	v := Verified{Claim: c}
	x = x.Prepared()
	q := []rune(x.fold(c.Quote))
	if len(q) < minQuoteRunes(c.Field) {
		v.Status = StatusNotFound
		return v
	}
	occ, units := x.ruled(q, c.Field)
	if len(occ) == 0 {
		v.Status = StatusNotFound
		return v
	}
	if slices.Min(units) == slices.Max(units) {
		return verifyAt(x, c, q, occ[0], units[0])
	}
	// In several units: a context-free value grounds when every occurrence
	// grounds it alike (v3), at the first; anything else is ambiguous.
	if !contextFree(c.Field) {
		v.Status = StatusAmbiguous
		return v
	}
	first := verifyAt(x, c, q, occ[0], units[0])
	for i := 1; i < len(occ); i++ {
		w := verifyAt(x, c, q, occ[i], units[i])
		if first.Status != StatusGrounded || w.Status != StatusGrounded || w.Value != first.Value {
			v.Status = StatusAmbiguous
			return v
		}
	}
	return first
}

// contextFree reports whether a field's value is read from its quote alone,
// so that every printing of a quote derives the same value.
func contextFree(field string) bool {
	return field == FieldTitle || field == FieldSubtitle || field == FieldImprint || field == FieldEdition
}

// verifyAt verifies c at the occurrence of q that starts at normalised index
// s, in unit index unit (-1 for none).
func verifyAt(x Excerpt, c Claim, q []rune, s, unit int) Verified {
	v := Verified{Claim: c}
	n := x.prep.n
	f := &frame{x: x, n: n, s: s, e: s + len(q), q: q, unit: max(unit, 0)}
	os, oe, spanOK := n.Span(f.s, f.e)
	if unit < 0 || !spanOK || x.fold(string(x.prep.orig[os:oe])) != string(q) {
		// Not in a unit, or the offsets don't give the quote back: fail closed.
		v.Status = StatusNotFound
		return v
	}
	v.Unit, f.landmark = x.unitInfo(f.unit)
	v.Start, v.End = os, oe

	if x.excludedAt(f.unit, f.s, f.e, c.Field) {
		v.Status = StatusExcludedSection
		return v
	}
	d := derive(f, c)
	if d.status != "" {
		v.Status, v.Reason = d.status, d.reason
		return v
	}
	v.Value, v.Role, v.Label = d.value, d.role, d.label
	v.Status = StatusGrounded
	return v
}

// ruled returns the occurrences of q that Verify rules on, with the unit of
// each (-1 for one that crosses units or sits in none). An occurrence in an
// excluded section (another book's ad, an "Also by" page, a back unit) is no
// evidence either way, so it is left out; when every occurrence is excluded,
// only the first is returned, and it is excluded. Verify then requires them
// all to sit in one unit and reads the first. Parse claims only there.
func (x Excerpt) ruled(q []rune, field string) (occ, units []int) {
	n := x.prep.n
	all := n.find(q)
	if len(all) == 0 {
		return nil, nil
	}
	unitsAll := make([]int, len(all))
	for i, s := range all {
		a, b := x.unitAt(n.start[s]), x.unitAt(n.end[s+len(q)-1]-1)
		unitsAll[i] = a
		if a != b {
			unitsAll[i] = -1
		}
		if unitsAll[i] < 0 || !x.excludedAt(unitsAll[i], s, s+len(q), field) {
			occ, units = append(occ, s), append(units, unitsAll[i])
		}
	}
	if len(occ) == 0 {
		return all[:1], unitsAll[:1]
	}
	return occ, units
}

// excludedAt reports whether the span [start, end) (normalised) of unit u
// sits in a region the section filter drops: a back unit, for every field but
// the ISBN, except the book's own copyright page in it (v3); or the part of a
// unit from its first heading on.
func (x Excerpt) excludedAt(u, start, end int, field string) bool {
	ownPage := u == x.prep.backCopyright && start >= x.prep.backFrom
	if _, landmark := x.unitInfo(u); landmark == evidence.LandmarkBack && field != FieldISBN && !ownPage {
		return true
	}
	cut, ok := x.prep.cut[u]
	return ok && end > cut
}

// Sealed is what an evidence package may hold of a verified claim (design
// section 8). It has no quote text: the offsets into the sealed excerpt show
// the quote. A model's value, role and label are kept only when grounded,
// when they are what the book's own text derives; otherwise they stay in the
// private raw output. A field outside the closed set is kept as "".
type Sealed struct {
	Field, Value, Role, Label, Source, Status string
	Unit, Start, End                          int
}

// Sealable returns the part of v that a package may seal.
func (v Verified) Sealable() Sealed {
	s := Sealed{Source: v.Source, Status: v.Status, Unit: v.Unit, Start: v.Start, End: v.End}
	if slices.Contains(knownFields, v.Field) {
		s.Field = v.Field
	}
	if v.Status == StatusGrounded {
		s.Value, s.Role, s.Label = v.Value, v.Role, v.Label
	}
	return s
}
