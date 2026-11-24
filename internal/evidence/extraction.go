package evidence

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Extraction is a format's M4 extraction (schema 4): which extractors read
// its excerpt and how each fared, the claims the verifier judged, and what
// they agree on. It holds no quote text and no value the book's text did not
// derive; a model's raw answer is stored privately, and only its hash is
// sealed.
type Extraction struct {
	PromptVersion  string           `json:"prompt_version"`
	ExtractVersion int              `json:"extract_version"` // the verifier's rules (extract.Version)
	ExcerptSHA256  string           `json:"excerpt_sha256"`
	Extractors     []ExtractorRun   `json:"extractors"`
	Claims         []ExtractedClaim `json:"claims,omitempty"`
	Agreed         []AgreedFact     `json:"agreed,omitempty"`
}

// ExtractorRun is one extractor's call for the excerpt. Model is the model
// asked for, ModelReported the one the provider said answered.
type ExtractorRun struct {
	Name          string `json:"name"`
	Family        string `json:"family"`
	Model         string `json:"model"`
	Effort        string `json:"effort,omitempty"`
	ModelReported string `json:"model_reported,omitempty"`
	State         string `json:"state"`
	// RawSHA256 is the hash of the raw answer, set when there is one (ok,
	// invalid_output).
	RawSHA256 string `json:"raw_sha256,omitempty"`
	// Cached: the answer came from the extraction cache, not a call.
	Cached bool `json:"cached,omitempty"`
}

// The states of an extractor's call.
const (
	ExtractOK             = "ok"
	ExtractUnavailable    = "unavailable" // not called (cap, quota pause) or the provider failed
	ExtractQuotaExhausted = "quota_exhausted"
	ExtractInvalidOutput  = "invalid_output"
	ExtractTimeout        = "timeout"
	ExtractModelMismatch  = "model_mismatch"
)

var extractStates = []string{ExtractOK, ExtractUnavailable, ExtractQuotaExhausted, ExtractInvalidOutput, ExtractTimeout, ExtractModelMismatch}

// ExtractedClaim is one judged claim (extract.Sealed). Value, Role and Label
// are set only when grounded; Unit, Start and End locate the quote in the
// excerpt whenever it was found.
type ExtractedClaim struct {
	Field  string `json:"field"`
	Value  string `json:"value,omitempty"`
	Role   string `json:"role,omitempty"`
	Label  string `json:"label,omitempty"`
	Source string `json:"source"`
	Status string `json:"status"`
	Unit   int    `json:"unit"`
	Start  int    `json:"start"`
	End    int    `json:"end"`
}

// ExtractParser is the source name of the deterministic parsers.
const ExtractParser = "parser"

// The verifier's statuses and the fields it knows (package extract, which
// this package cannot import).
var (
	claimStatuses = []string{"grounded", "not_found", "ambiguous_quote", "value_not_derived", "role_unproven", "excluded_section"}
	extractFields = []string{"title", "subtitle", "contributor", "imprint", "year", "edition", "series", "series_index", "isbn"}
	claimRoles    = []string{"author", "editor", "translator", "illustrator", "introduction"}
	yearLabels    = []string{"copyright", "first_published", "this_edition"}
	claimLabels   = append(slices.Clone(yearLabels), QualifierEbook, QualifierPDF, QualifierHardcover, QualifierPaperback, QualifierPrint)
	// agreeKeys are extract.Agree's keys, besides year/<label>.
	agreeKeys = []string{"title", "subtitle", "imprint", "edition", "series", "contributor", "isbn", "series_index", "translator"}
)

// maxModelRunes bounds the one provider-written string a package holds.
const maxModelRunes = 200

// modelID reports whether s may be a reported model id: short, with no space
// or control character.
func modelID(s string) bool {
	return utf8.RuneCountInString(s) <= maxModelRunes && !strings.ContainsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) })
}

// backs reports whether claim c is a grounded voice for value v of key.
func backs(key string, v AgreedValue, c ExtractedClaim) bool {
	if c.Status != "grounded" || c.Value != v.Value {
		return false
	}
	if label, ok := strings.CutPrefix(key, "year/"); ok {
		return c.Field == "year" && c.Label == label
	}
	switch key {
	case "contributor":
		return c.Field == "contributor" && c.Role == v.Role
	case "translator":
		return c.Field == "contributor" && c.Role == "translator"
	case "isbn":
		return c.Field == "isbn" && c.Label == v.Label
	}
	return c.Field == key
}

// AgreedFact is what the voices say about one key (extract.Fact): Values for
// agreed and single, none for disagree.
type AgreedFact struct {
	Key     string        `json:"key"`
	Outcome string        `json:"outcome"`
	Values  []AgreedValue `json:"values,omitempty"`
	Voices  []string      `json:"voices"`
}

// AgreedValue is one value of an agreed fact.
type AgreedValue struct {
	Value string `json:"value"`
	Role  string `json:"role,omitempty"`
	Label string `json:"label,omitempty"`
}

// The outcomes of agreement.
const (
	AgreeAgreed   = "agreed"
	AgreeSingle   = "single"
	AgreeDisagree = "disagree"
)

// checkExtraction refuses an extraction a schema before 4 can't carry, one on
// a format with no excerpt or of another excerpt, and one that breaks the
// closed sets: states, statuses, fields, sources, spans and outcomes.
func (p Package) checkExtraction() error {
	for _, f := range p.Formats {
		x := f.Extraction
		if x == nil {
			continue
		}
		if p.SchemaVersion < SchemaVersionV4 {
			return fmt.Errorf("%w: schema %d holds a schema 4 extraction in %s", ErrInvalidEvidence, p.SchemaVersion, f.Format)
		}
		if f.Excerpt == nil || x.ExcerptSHA256 != f.Excerpt.SHA256 {
			return fmt.Errorf("%w: %s extraction is not of its excerpt", ErrInvalidEvidence, f.Format)
		}
		if err := x.Check(f.Excerpt); err != nil {
			return fmt.Errorf("%w: %s extraction: %w", ErrInvalidEvidence, f.Format, err)
		}
	}
	return nil
}

// Check reports whether x may be sealed as the extraction of ex: what Open
// refuses, short of the schema and the excerpt hash.
func (x *Extraction) Check(ex *Excerpt) error {
	if x.PromptVersion == "" || x.ExtractVersion < 1 {
		return fmt.Errorf("versions %q, %d", x.PromptVersion, x.ExtractVersion)
	}
	if len(x.Extractors) == 0 {
		return fmt.Errorf("no extractors")
	}
	answered := map[string]bool{ExtractParser: true} // sources that may hold claims
	seen := map[string]bool{}
	for _, e := range x.Extractors {
		switch {
		case e.Name == "" || e.Name == ExtractParser || seen[e.Name]:
			return fmt.Errorf("extractor name %q", e.Name)
		case !slices.Contains(extractStates, e.State):
			return fmt.Errorf("%s state %q", e.Name, e.State)
		case (e.State == ExtractOK || e.State == ExtractInvalidOutput) != (e.RawSHA256 != ""):
			return fmt.Errorf("%s raw for state %s", e.Name, e.State)
		case e.RawSHA256 != "" && !ValidExcerptHash(e.RawSHA256):
			return fmt.Errorf("%s raw hash %q", e.Name, e.RawSHA256)
		case !modelID(e.ModelReported) || !modelID(e.Model):
			return fmt.Errorf("%s model id", e.Name)
		}
		seen[e.Name] = true
		answered[e.Name] = e.State == ExtractOK
	}
	units := map[int]ExcerptUnit{}
	for _, u := range ex.Units {
		units[u.Pos] = u
	}
	for i, c := range x.Claims {
		grounded := c.Status == "grounded"
		switch {
		case !answered[c.Source]:
			return fmt.Errorf("claim %d source %q", i, c.Source)
		case !slices.Contains(claimStatuses, c.Status):
			return fmt.Errorf("claim %d status %q", i, c.Status)
		case c.Field != "" && !slices.Contains(extractFields, c.Field) || grounded && c.Field == "":
			return fmt.Errorf("claim %d field %q", i, c.Field)
		case c.Start < 0 || c.End < c.Start || c.End > ex.Chars:
			return fmt.Errorf("claim %d span %d-%d of %d", i, c.Start, c.End, ex.Chars)
		case !grounded && (c.Value != "" || c.Role != "" || c.Label != ""):
			return fmt.Errorf("claim %d: a %s claim carries a value", i, c.Status)
		case c.Role != "" && !slices.Contains(claimRoles, c.Role) || c.Label != "" && !slices.Contains(claimLabels, c.Label):
			return fmt.Errorf("claim %d role %q, label %q", i, c.Role, c.Label)
		}
		if grounded {
			u, ok := units[c.Unit]
			if c.Start == c.End || c.Value == "" || !ok || c.Start < u.Start || c.End > u.End {
				return fmt.Errorf("claim %d: a grounded claim needs a value and a span inside its unit", i)
			}
		}
	}
	keys := map[string]bool{}
	for i, a := range x.Agreed {
		switch {
		case a.Key == "":
			return fmt.Errorf("fact %d has no key", i)
		case a.Outcome != AgreeAgreed && a.Outcome != AgreeSingle && a.Outcome != AgreeDisagree:
			return fmt.Errorf("fact %d outcome %q", i, a.Outcome)
		case (a.Outcome == AgreeDisagree) != (len(a.Values) == 0):
			return fmt.Errorf("fact %d: %s with %d values", i, a.Outcome, len(a.Values))
		case len(a.Voices) == 0 || a.Outcome == AgreeAgreed && len(a.Voices) < 2:
			return fmt.Errorf("fact %d has %d voices", i, len(a.Voices))
		case !slices.IsSorted(a.Voices) || len(slices.Compact(slices.Clone(a.Voices))) != len(a.Voices):
			return fmt.Errorf("fact %d: voices not sorted and distinct", i)
		case keys[a.Key]:
			return fmt.Errorf("fact %d: key %q twice", i, a.Key)
		}
		keys[a.Key] = true
		if label, ok := strings.CutPrefix(a.Key, "year/"); ok && !slices.Contains(yearLabels, label) || !ok && !slices.Contains(agreeKeys, a.Key) {
			return fmt.Errorf("fact %d key %q", i, a.Key)
		}
		for _, v := range a.Voices {
			if !answered[v] {
				return fmt.Errorf("fact %d voice %q", i, v)
			}
		}
		// Each value is what a grounded claim of one of its voices says.
		for _, v := range a.Values {
			if !slices.ContainsFunc(x.Claims, func(c ExtractedClaim) bool { return slices.Contains(a.Voices, c.Source) && backs(a.Key, v, c) }) {
				return fmt.Errorf("fact %d has a value no voice grounds", i)
			}
		}
	}
	return nil
}
