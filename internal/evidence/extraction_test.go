package evidence_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

var rawHash = strings.Repeat("cd", 32)

// goodExtraction is a valid extraction of goodExcerpt: two extractors, one
// grounded title, one dropped claim, and the title agreed.
func goodExtraction() *evidence.Extraction {
	return &evidence.Extraction{
		PromptVersion: "extract-2", ExtractVersion: 2, ExcerptSHA256: goodExcerpt().SHA256,
		Extractors: []evidence.ExtractorRun{
			{Name: "luna", Family: "openai", Model: "opencode-go/gpt-6-luna", Effort: "low", ModelReported: "gpt-6-luna", State: evidence.ExtractOK, RawSHA256: rawHash},
			{Name: "glm", Family: "zhipu", Model: "opencode-go/glm-5.3-flash", ModelReported: "glm-5.3-flash", State: evidence.ExtractOK, RawSHA256: strings.Repeat("ef", 32), Cached: true},
		},
		Claims: []evidence.ExtractedClaim{
			{Field: "title", Value: "THE LANTERN", Source: "luna", Status: "grounded", Unit: 1, Start: 0, End: 10},
			{Field: "title", Value: "THE LANTERN", Source: "glm", Status: "grounded", Unit: 1, Start: 0, End: 10},
			{Field: "year", Source: "parser", Status: "excluded_section", Unit: 4, Start: 12, End: 18},
		},
		Agreed: []evidence.AgreedFact{
			{Key: "title", Outcome: evidence.AgreeAgreed, Values: []evidence.AgreedValue{{Value: "THE LANTERN"}}, Voices: []string{"glm", "luna"}},
		},
	}
}

func extractionPackage(t *testing.T, version int, ex *evidence.Excerpt, x *evidence.Extraction) ([]byte, string, evidence.Package) {
	t.Helper()
	p := pkg(t)
	p.SchemaVersion = version
	p.Formats = append([]evidence.FormatEvidence(nil), p.Formats...)
	p.Formats[1].Format = "EPUB"
	p.Formats[1].Excerpt = ex
	p.Formats[1].Extraction = x
	raw, seal, err := evidence.Seal(p)
	if err != nil {
		t.Fatal(err)
	}
	return raw, seal, p
}

func TestExtractionRoundTrip(t *testing.T) {
	raw, seal, p := extractionPackage(t, evidence.SchemaVersion, goodExcerpt(), goodExtraction())
	got, err := evidence.Open(raw, seal)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, p) {
		t.Fatalf("Open = %+v\nwant %+v", got.Formats[1].Extraction, p.Formats[1].Extraction)
	}
	// No quote text is part of the block.
	if strings.Contains(string(raw), `"quote"`) {
		t.Error("a sealed extraction carries quote text")
	}
}

func TestExtractionRefused(t *testing.T) {
	mod := func(f func(*evidence.Extraction)) *evidence.Extraction {
		x := goodExtraction()
		f(x)
		return x
	}
	tests := []struct {
		name    string
		version int
		ex      *evidence.Excerpt
		x       *evidence.Extraction
	}{
		{"schema 3", 3, goodExcerpt(), goodExtraction()},
		{"no excerpt", evidence.SchemaVersion, nil, goodExtraction()},
		{"another excerpt", evidence.SchemaVersion, goodExcerpt(), mod(func(x *evidence.Extraction) { x.ExcerptSHA256 = strings.Repeat("00", 32) })},
		{"no prompt version", evidence.SchemaVersion, goodExcerpt(), mod(func(x *evidence.Extraction) { x.PromptVersion = "" })},
		{"no verifier version", evidence.SchemaVersion, goodExcerpt(), mod(func(x *evidence.Extraction) { x.ExtractVersion = 0 })},
		{"no extractors", evidence.SchemaVersion, goodExcerpt(), mod(func(x *evidence.Extraction) { x.Extractors = nil })},
		{"one name twice", evidence.SchemaVersion, goodExcerpt(), mod(func(x *evidence.Extraction) { x.Extractors[1].Name = "luna" })},
		{"an extractor named parser", evidence.SchemaVersion, goodExcerpt(), mod(func(x *evidence.Extraction) { x.Extractors[1].Name = "parser" })},
		{"no name", evidence.SchemaVersion, goodExcerpt(), mod(func(x *evidence.Extraction) { x.Extractors[1].Name = "" })},
		{"unknown state", evidence.SchemaVersion, goodExcerpt(), mod(func(x *evidence.Extraction) { x.Extractors[0].State = "maybe" })},
		{"ok without a raw", evidence.SchemaVersion, goodExcerpt(), mod(func(x *evidence.Extraction) { x.Extractors[0].RawSHA256 = "" })},
		{"a bad raw hash", evidence.SchemaVersion, goodExcerpt(), mod(func(x *evidence.Extraction) { x.Extractors[0].RawSHA256 = "AB" })},
		{"a raw for a call that never answered", evidence.SchemaVersion, goodExcerpt(), mod(func(x *evidence.Extraction) { x.Extractors[0].State = evidence.ExtractTimeout })},
		{"an unknown source", evidence.SchemaVersion, goodExcerpt(), mod(func(x *evidence.Extraction) { x.Claims[0].Source = "gemini" })},
		{"a claim of an extractor that did not answer", evidence.SchemaVersion, goodExcerpt(), mod(func(x *evidence.Extraction) {
			x.Extractors[0].State, x.Extractors[0].RawSHA256 = evidence.ExtractUnavailable, ""
		})},
		{"an unknown status", evidence.SchemaVersion, goodExcerpt(), mod(func(x *evidence.Extraction) { x.Claims[0].Status = "plausible" })},
		{"an unknown field", evidence.SchemaVersion, goodExcerpt(), mod(func(x *evidence.Extraction) { x.Claims[0].Field = "price" })},
		{"grounded past the excerpt", evidence.SchemaVersion, goodExcerpt(), mod(func(x *evidence.Extraction) { x.Claims[0].End = 31 })},
		{"grounded with no span", evidence.SchemaVersion, goodExcerpt(), mod(func(x *evidence.Extraction) { x.Claims[0].Start = 10 })},
		{"grounded with no value", evidence.SchemaVersion, goodExcerpt(), mod(func(x *evidence.Extraction) { x.Claims[0].Value = "" })},
		{"a dropped claim with a value", evidence.SchemaVersion, goodExcerpt(), mod(func(x *evidence.Extraction) { x.Claims[2].Value = "1999" })},
		{"an unknown outcome", evidence.SchemaVersion, goodExcerpt(), mod(func(x *evidence.Extraction) { x.Agreed[0].Outcome = "likely" })},
		{"an unknown voice", evidence.SchemaVersion, goodExcerpt(), mod(func(x *evidence.Extraction) { x.Agreed[0].Voices = []string{"luna", "gemini"} })},
		{"agreed with no values", evidence.SchemaVersion, goodExcerpt(), mod(func(x *evidence.Extraction) { x.Agreed[0].Values = nil })},
		{"disagree with values", evidence.SchemaVersion, goodExcerpt(), mod(func(x *evidence.Extraction) { x.Agreed[0].Outcome = evidence.AgreeDisagree })},
		{"no key", evidence.SchemaVersion, goodExcerpt(), mod(func(x *evidence.Extraction) { x.Agreed[0].Key = "" })},
		// The agreed block holds only what grounded claims of its voices say.
		{"a key outside the set", evidence.SchemaVersion, goodExcerpt(), mod(func(x *evidence.Extraction) { x.Agreed[0].Key = "some text" })},
		{"a year key without a known label", evidence.SchemaVersion, goodExcerpt(), mod(func(x *evidence.Extraction) { x.Agreed[0].Key = "year/someday" })},
		{"one key twice", evidence.SchemaVersion, goodExcerpt(), mod(func(x *evidence.Extraction) { x.Agreed = append(x.Agreed, x.Agreed[0]) })},
		{"agreed by one voice", evidence.SchemaVersion, goodExcerpt(), mod(func(x *evidence.Extraction) { x.Agreed[0].Voices = []string{"luna"} })},
		{"a voice twice", evidence.SchemaVersion, goodExcerpt(), mod(func(x *evidence.Extraction) { x.Agreed[0].Voices = []string{"glm", "glm"} })},
		{"voices out of order", evidence.SchemaVersion, goodExcerpt(), mod(func(x *evidence.Extraction) { x.Agreed[0].Voices = []string{"luna", "glm"} })},
		{"a value no claim grounds", evidence.SchemaVersion, goodExcerpt(), mod(func(x *evidence.Extraction) { x.Agreed[0].Values[0].Value = "Something Else" })},
		{"a value only a non-voice grounds", evidence.SchemaVersion, goodExcerpt(), mod(func(x *evidence.Extraction) {
			x.Agreed[0].Outcome, x.Agreed[0].Voices = evidence.AgreeSingle, []string{"parser"}
		})},
		{"a grounded span outside its unit", evidence.SchemaVersion, goodExcerpt(), mod(func(x *evidence.Extraction) { x.Claims[0].End = 12 })},
		{"a grounded claim in no unit", evidence.SchemaVersion, goodExcerpt(), mod(func(x *evidence.Extraction) { x.Claims[0].Unit = 2 })},
		{"an unknown role", evidence.SchemaVersion, goodExcerpt(), mod(func(x *evidence.Extraction) { x.Claims[0].Role = "ghost" })},
		{"an unknown label", evidence.SchemaVersion, goodExcerpt(), mod(func(x *evidence.Extraction) { x.Claims[0].Label = "audiobook" })},
		{"a reported model that is not a model id", evidence.SchemaVersion, goodExcerpt(), mod(func(x *evidence.Extraction) { x.Extractors[0].ModelReported = "line one\nline two" })},
		{"a reported model too long", evidence.SchemaVersion, goodExcerpt(), mod(func(x *evidence.Extraction) { x.Extractors[0].ModelReported = strings.Repeat("m", 201) })},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, seal, _ := extractionPackage(t, tt.version, tt.ex, tt.x)
			if _, err := evidence.Open(raw, seal); !errors.Is(err, evidence.ErrInvalidEvidence) {
				t.Fatalf("Open = %v, want ErrInvalidEvidence", err)
			}
		})
	}
}

func TestExtractionStatesWithoutAnswers(t *testing.T) {
	// Every extractor failing still seals: states, no claims, no raw.
	x := goodExtraction()
	x.Extractors[0].State, x.Extractors[0].RawSHA256 = evidence.ExtractQuotaExhausted, ""
	x.Extractors[1].State, x.Extractors[1].RawSHA256, x.Extractors[1].Cached = evidence.ExtractModelMismatch, "", false
	x.Claims, x.Agreed = nil, nil
	raw, seal, _ := extractionPackage(t, evidence.SchemaVersion, goodExcerpt(), x)
	if _, err := evidence.Open(raw, seal); err != nil {
		t.Fatal(err)
	}
	// An answer that was not valid JSON keeps its raw, for re-verification.
	x.Extractors[0].State, x.Extractors[0].RawSHA256 = evidence.ExtractInvalidOutput, rawHash
	raw, seal, _ = extractionPackage(t, evidence.SchemaVersion, goodExcerpt(), x)
	if _, err := evidence.Open(raw, seal); err != nil {
		t.Fatal(err)
	}
}

func TestSchema3StillOpens(t *testing.T) {
	raw, seal, _ := excerptPackage(t, evidence.SchemaVersionV3, goodExcerpt())
	if _, err := evidence.Open(raw, seal); err != nil {
		t.Fatal(err)
	}
}
