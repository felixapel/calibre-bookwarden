package eval

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/extract"
	"github.com/felixapel/calibre-bookwarden/internal/extraction"
	"github.com/felixapel/calibre-bookwarden/internal/store"
)

// ReverifyReport compares a run's sealed extractions with what today's
// parsers, verifier and agreement derive from the same stored excerpts and
// raw answers (M4 §5, §7, §8). No model is asked. It holds counts and book
// ids only, never an excerpt, a quote, a claimed value or a raw answer.
type ReverifyReport struct {
	Run            int64 `json:"run"`
	CurrentVersion int   `json:"current_extract_version"`
	// SealedVersions counts the formats by the extract.Version they were
	// sealed under.
	SealedVersions map[int]int `json:"sealed_versions"`
	Formats        int         `json:"formats"`
	// Skipped formats lacked their excerpt or a raw answer, or had one that
	// no longer matches its hash.
	Skipped int `json:"skipped"`
	// StateChanged counts the formats where an extractor's raw answer parses
	// differently from its sealed state. Unsealable counts the formats whose
	// re-derived block would fail the check Extract runs before sealing.
	StateChanged int `json:"state_changed"`
	Unsealable   int `json:"unsealable"`
	// Broken packages (a seal that doesn't hold) are left out of everything
	// above, and listed here as by --extraction.
	Broken      int   `json:"broken"`
	BrokenBooks []int `json:"broken_books"`

	// Sources are the claims per source ("parser" or an extractor) by status.
	Sources map[string]*StatusShift `json:"sources"`
	// Breakdown splits the claims of the statuses a verifier change targets
	// by field, sealed against re-derived, and the re-derived ones by the role
	// or label class they asked for (the sealed side drops it from an
	// ungrounded claim, so it has no class to compare).
	Breakdown map[string]*Breakdown `json:"breakdown"`
	// Refusals counts the models' re-derived value_not_derived claims (not
	// the parsers') by why the value was refused (extract.Refused*) and field: claims, and the books
	// they are in. Diagnostic only (extract.Verified.Reason).
	Refusals map[string]map[string]RefusalCount `json:"refusals"`
	Keys     map[string]*KeyShift               `json:"keys"`

	refusalBooks map[[2]string]map[int]bool

	AgreedChanged      int   `json:"agreed_changed"`
	AgreedChangedBooks []int `json:"agreed_changed_books"`
}

// StatusShift counts claims by status as sealed and as re-derived.
type StatusShift struct {
	Sealed  map[string]int `json:"sealed"`
	Derived map[string]int `json:"derived"`
}

// Breakdown is one status's claims by field, as sealed and as re-derived,
// and the re-derived ones by "field" or "field/class" (role or label).
type Breakdown struct {
	Sealed  map[string]int `json:"sealed"`
	Derived map[string]int `json:"derived"`
	Classes map[string]int `json:"derived_classes"`
}

// RefusalCount is how many refused values one reason and field have, and in
// how many books.
type RefusalCount struct {
	Values int `json:"values"`
	Books  int `json:"books"`
}

// KeyShift is a key's agreement outcomes as sealed and as re-derived.
type KeyShift struct {
	Sealed  Outcomes `json:"sealed"`
	Derived Outcomes `json:"derived"`
}

// Outcomes counts agreement outcomes.
type Outcomes struct {
	Agreed   int `json:"agreed"`
	Single   int `json:"single"`
	Disagree int `json:"disagree"`
}

// The statuses of a claim, in the order the report prints them.
var claimStatuses = []string{extract.StatusGrounded, extract.StatusExcludedSection, extract.StatusAmbiguous, extract.StatusValueNotDerived, extract.StatusRoleUnproven, extract.StatusNotFound}

// The statuses the next verifier change targets.
var targetStatuses = []string{extract.StatusValueNotDerived, extract.StatusRoleUnproven}

// The closed sets a claim's text is reduced to, so that a model's free text
// never reaches the report.
var (
	knownFields = []string{extract.FieldTitle, extract.FieldSubtitle, extract.FieldContributor, extract.FieldImprint, extract.FieldYear, extract.FieldEdition, extract.FieldSeries, extract.FieldSeriesIndex, extract.FieldISBN}
	knownClass  = []string{extract.RoleAuthor, extract.RoleEditor, extract.RoleTranslator, extract.RoleIllustrator, extract.RoleIntroduction, extract.LabelCopyright, extract.LabelFirstPublished, extract.LabelThisEdition, evidence.QualifierEbook, evidence.QualifierPDF, evidence.QualifierHardcover, evidence.QualifierPaperback, evidence.QualifierPrint}
)

// fieldOf is a claim's field, with any text outside the closed set reduced to
// "other".
func fieldOf(field string) string {
	if slices.Contains(knownFields, field) {
		return field
	}
	return "other"
}

// class is the "field", "field/role" or "field/label" of a claim, with any
// text outside the closed sets reduced to "other".
func class(field, role, label string) string {
	c := fieldOf(field)
	switch k := cmpOr(role, label); {
	case k == "":
	case slices.Contains(knownClass, k):
		c += "/" + k
	default:
		c += "/other"
	}
	return c
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func newReverifyReport(run int64) *ReverifyReport {
	return &ReverifyReport{Run: run, CurrentVersion: extract.Version, SealedVersions: map[int]int{}, AgreedChangedBooks: []int{}, BrokenBooks: []int{},
		Sources: map[string]*StatusShift{}, Breakdown: map[string]*Breakdown{}, Keys: map[string]*KeyShift{},
		Refusals: map[string]map[string]RefusalCount{}, refusalBooks: map[[2]string]map[int]bool{}}
}

// Reverify re-derives every sealed extraction of the run under today's
// rules, read-only, and counts what differs. A package whose seal doesn't
// hold is left to the seal checks.
func Reverify(ctx context.Context, repo *store.ReportRepo, runID int64) (ReverifyReport, error) {
	r := newReverifyReport(runID)
	whole, err := Extractions(ctx, repo, runID)
	if err != nil {
		return ReverifyReport{}, fmt.Errorf("eval: reverify run %d: %w", runID, err)
	}
	r.Broken, r.BrokenBooks = whole.Broken, append(r.BrokenBooks, whole.BrokenBooks...)
	err = repo.Extractions(ctx, runID, func(x store.ExtractionRef) error {
		r.Formats++
		r.SealedVersions[x.Extraction.ExtractVersion]++
		if x.Missing {
			r.Skipped++
			return nil
		}
		d, err := extraction.Rederive(x.Excerpt, x.Body, x.Extraction, x.Raws)
		if errors.Is(err, extraction.ErrReverify) {
			r.Skipped++
			return nil
		}
		if err != nil {
			return fmt.Errorf("book %d: %w", x.BookID, err)
		}
		if d.StateChanged > 0 {
			r.StateChanged++
		}
		if d.Unsealable {
			r.Unsealable++
		}
		r.add(x.BookID, x.Extraction, d)
		return nil
	})
	if err != nil {
		return ReverifyReport{}, fmt.Errorf("eval: reverify run %d: %w", runID, err)
	}
	return *r, nil
}

func (r *ReverifyReport) add(book int, sealed evidence.Extraction, d extraction.Rederived) {
	for _, c := range sealed.Claims {
		r.source(c.Source).Sealed[c.Status]++
		if slices.Contains(targetStatuses, c.Status) {
			r.breakdown(c.Status).Sealed[fieldOf(c.Field)]++
		}
	}
	for _, v := range d.Verified {
		r.source(v.Source).Derived[v.Status]++
		if slices.Contains(targetStatuses, v.Status) {
			b := r.breakdown(v.Status)
			b.Derived[fieldOf(v.Field)]++
			b.Classes[class(v.Field, v.Role, v.Label)]++
		}
		if v.Status == extract.StatusValueNotDerived && v.Reason != "" && v.Source != extract.SourceParser {
			r.refusal(book, v.Reason, fieldOf(v.Field))
		}
	}
	for _, a := range sealed.Agreed {
		r.key(a.Key).Sealed.count(a.Outcome)
	}
	for _, a := range d.Agreed {
		r.key(a.Key).Derived.count(a.Outcome)
	}
	if !reflect.DeepEqual(nilAgreed(sealed.Agreed), nilAgreed(d.Agreed)) {
		r.AgreedChanged++
		if !slices.Contains(r.AgreedChangedBooks, book) {
			r.AgreedChangedBooks = append(r.AgreedChangedBooks, book)
		}
	}
}

func (r *ReverifyReport) refusal(book int, reason, field string) {
	byField := r.Refusals[reason]
	if byField == nil {
		byField = map[string]RefusalCount{}
		r.Refusals[reason] = byField
	}
	c := byField[field]
	c.Values++
	k := [2]string{reason, field}
	if r.refusalBooks[k] == nil {
		r.refusalBooks[k] = map[int]bool{}
	}
	if !r.refusalBooks[k][book] {
		r.refusalBooks[k][book] = true
		c.Books++
	}
	byField[field] = c
}

func nilAgreed(a []evidence.AgreedFact) []evidence.AgreedFact {
	if len(a) == 0 {
		return nil
	}
	return a
}

func (o *Outcomes) count(outcome string) {
	switch outcome {
	case evidence.AgreeAgreed:
		o.Agreed++
	case evidence.AgreeSingle:
		o.Single++
	case evidence.AgreeDisagree:
		o.Disagree++
	}
}

func (r *ReverifyReport) source(name string) *StatusShift {
	s := r.Sources[name]
	if s == nil {
		s = &StatusShift{Sealed: map[string]int{}, Derived: map[string]int{}}
		r.Sources[name] = s
	}
	return s
}

func (r *ReverifyReport) breakdown(status string) *Breakdown {
	b := r.Breakdown[status]
	if b == nil {
		b = &Breakdown{Sealed: map[string]int{}, Derived: map[string]int{}, Classes: map[string]int{}}
		r.Breakdown[status] = b
	}
	return b
}

func (r *ReverifyReport) key(name string) *KeyShift {
	k := r.Keys[name]
	if k == nil {
		k = &KeyShift{}
		r.Keys[name] = k
	}
	return k
}

// WriteText prints the report, counts and book ids only.
func (r ReverifyReport) WriteText(w io.Writer) {
	p := func(format string, a ...any) { _, _ = fmt.Fprintf(w, format, a...) }
	p("run %d: re-verification of %d formats with an extraction under verifier rules %d\n", r.Run, r.Formats, r.CurrentVersion)
	p("sealed under rules:")
	for _, v := range slices.Sorted(maps.Keys(r.SealedVersions)) {
		p(" %d (%d formats)", v, r.SealedVersions[v])
	}
	p("\nskipped for a missing or corrupt excerpt or raw answer: %d\n", r.Skipped)
	p("formats where a raw answer's parse state changed: %d\n", r.StateChanged)
	p("formats whose re-derived block would not seal: %d\n", r.Unsealable)
	p("books with a broken seal, left out: %d\n", r.Broken)
	if r.Broken > 0 {
		p("broken books: %s\n", strings.Trim(fmt.Sprint(r.BrokenBooks), "[]"))
	}

	p("\nclaims by status, sealed -> re-derived (delta):\n")
	for _, name := range slices.Sorted(maps.Keys(r.Sources)) {
		s := r.Sources[name]
		p("%s\n", name)
		for _, st := range claimStatuses {
			p("  %-18s %s\n", st, shift(s.Sealed[st], s.Derived[st]))
		}
	}
	for _, st := range targetStatuses {
		b := r.Breakdown[st]
		if b == nil {
			continue
		}
		p("\n%s by field, sealed -> re-derived (delta):\n", st)
		fields := slices.Sorted(maps.Keys(b.Sealed))
		fields = append(fields, slices.Sorted(maps.Keys(b.Derived))...)
		slices.Sort(fields)
		for _, f := range slices.Compact(fields) {
			p("  %-28s %s\n", f, shift(b.Sealed[f], b.Derived[f]))
		}
		p("%s by class (re-derived only):\n", st)
		for _, c := range slices.Sorted(maps.Keys(b.Classes)) {
			p("  %-28s %d\n", c, b.Classes[c])
		}
	}
	if len(r.Refusals) > 0 {
		p("\n%s by reason and field (re-derived): values, books\n", extract.StatusValueNotDerived)
		for _, reason := range extract.RefusalReasons {
			for _, f := range slices.Sorted(maps.Keys(r.Refusals[reason])) {
				c := r.Refusals[reason][f]
				p("  %-20s %-16s %6d %6d\n", reason, f, c.Values, c.Books)
			}
		}
	}
	p("\nkeys, sealed -> re-derived: agreed, single, disagree\n")
	for _, name := range slices.Sorted(maps.Keys(r.Keys)) {
		k := r.Keys[name]
		p("%-24s %s   %s   %s\n", name, shift(k.Sealed.Agreed, k.Derived.Agreed), shift(k.Sealed.Single, k.Derived.Single), shift(k.Sealed.Disagree, k.Derived.Disagree))
	}
	p("\nformats whose agreed set changed: %d\n", r.AgreedChanged)
	if r.AgreedChanged > 0 {
		p("books: %v\n", r.AgreedChangedBooks)
	}
}

func shift(sealed, derived int) string {
	return fmt.Sprintf("%d -> %d (%+d)", sealed, derived, derived-sealed)
}
