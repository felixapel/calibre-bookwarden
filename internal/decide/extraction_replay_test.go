package decide_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/decide"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/findings"
)

// TestExtractionChangesNothing: M4's extraction is shadow evidence up to
// decision version 7 (design section 9); version 8 reads it as the text root
// (ADR-036, tested in v8_test.go). Every replay fixture, given an extraction on each format that
// agrees on another title, ISBN and author, derives the same findings and the
// same decision at every version as without it.
func TestExtractionChangesNothing(t *testing.T) {
	for name, p := range replayFixtures() {
		with := p
		with.SchemaVersion = evidence.SchemaVersion
		with.Formats = nil
		for _, f := range p.Formats {
			b := evidence.NewExcerptBuilder(evidence.ExcerptEPUBText)
			b.Add(1, evidence.LandmarkTitle, "A DIFFERENT TITLE\nby Someone Else\nISBN 978-0-306-40615-7")
			ex := b.Excerpt()
			ex.Text = ""
			f.Excerpt = ex
			f.Extraction = &evidence.Extraction{
				PromptVersion: "extract-2", ExtractVersion: 2, ExcerptSHA256: ex.SHA256,
				Extractors: []evidence.ExtractorRun{
					{Name: "luna", Family: "openai", Model: "m", State: evidence.ExtractOK, RawSHA256: strings.Repeat("ab", 32)},
					{Name: "glm", Family: "zhipu", Model: "m", State: evidence.ExtractOK, RawSHA256: strings.Repeat("cd", 32)},
				},
				Claims: []evidence.ExtractedClaim{
					{Field: "title", Value: "A DIFFERENT TITLE", Source: "luna", Status: "grounded", Unit: 1, Start: 0, End: 17},
					{Field: "title", Value: "A DIFFERENT TITLE", Source: "glm", Status: "grounded", Unit: 1, Start: 0, End: 17},
				},
				Agreed: []evidence.AgreedFact{
					{Key: "title", Outcome: evidence.AgreeAgreed, Values: []evidence.AgreedValue{{Value: "A DIFFERENT TITLE"}}, Voices: []string{"glm", "luna"}},
					{Key: "isbn", Outcome: evidence.AgreeAgreed, Values: []evidence.AgreedValue{{Value: "9780306406157"}}, Voices: []string{"luna", "parser"}},
					{Key: "contributor", Outcome: evidence.AgreeAgreed, Values: []evidence.AgreedValue{{Value: "Someone Else", Role: "author"}}, Voices: []string{"glm", "luna"}},
				},
			}
			with.Formats = append(with.Formats, f)
		}
		if _, _, err := decide.Seal(t.Context(), with); err != nil {
			t.Fatalf("%s: the fixture with extractions does not seal: %v", name, err)
		}
		for v := 1; v <= 7; v++ {
			a, err := decide.AtVersion(t.Context(), p, v)
			if err != nil {
				t.Fatalf("%s decide v%d: %v", name, v, err)
			}
			b, err := decide.AtVersion(t.Context(), with, v)
			if err != nil {
				t.Fatalf("%s decide v%d with extractions: %v", name, v, err)
			}
			ja, _ := json.Marshal(a)
			jb, _ := json.Marshal(b)
			if string(ja) != string(jb) {
				t.Errorf("%s decide v%d: an extraction changed the decision\nwithout: %s\nwith:    %s", name, v, ja, jb)
			}
		}
		for v := 1; v <= findings.Version; v++ {
			a, err := findings.DeriveVersion(t.Context(), p, v)
			if err != nil {
				t.Fatalf("%s findings v%d: %v", name, v, err)
			}
			b, err := findings.DeriveVersion(t.Context(), with, v)
			if err != nil {
				t.Fatalf("%s findings v%d with extractions: %v", name, v, err)
			}
			ja, _ := json.Marshal(a)
			jb, _ := json.Marshal(b)
			if string(ja) != string(jb) {
				t.Errorf("%s findings v%d: an extraction changed the findings", name, v)
			}
		}
	}
}
