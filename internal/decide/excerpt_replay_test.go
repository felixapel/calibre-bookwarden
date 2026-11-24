package decide_test

import (
	"encoding/json"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/decide"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/findings"
)

// TestExcerptsChangeNothing: nothing in findings or decide reads an excerpt
// (ADR-032 part 2). Every replay fixture, given an excerpt on each format
// (with the text of an ISBN, a title and a language), derives the same
// findings at every findings version and the same decision at every decision
// version as without it. The golden test above pins the fixtures' bytes
// themselves.
func TestExcerptsChangeNothing(t *testing.T) {
	for name, p := range replayFixtures() {
		withExcerpts := p
		withExcerpts.SchemaVersion = evidence.SchemaVersion
		withExcerpts.Formats = nil
		for _, f := range p.Formats {
			b := evidence.NewExcerptBuilder(evidence.ExcerptEPUBText)
			b.Add(1, evidence.LandmarkCopyright, "A different title by Someone Else\nISBN 978-0-306-40615-7 deutsch")
			ex := b.Excerpt()
			ex.Text = ""
			f.Excerpt = ex
			withExcerpts.Formats = append(withExcerpts.Formats, f)
		}
		for v := 1; v <= decide.Version; v++ {
			a, err := decide.AtVersion(t.Context(), p, v)
			if err != nil {
				t.Fatalf("%s decide v%d: %v", name, v, err)
			}
			b, err := decide.AtVersion(t.Context(), withExcerpts, v)
			if err != nil {
				t.Fatalf("%s decide v%d with excerpts: %v", name, v, err)
			}
			ja, _ := json.Marshal(a)
			jb, _ := json.Marshal(b)
			if string(ja) != string(jb) {
				t.Errorf("%s decide v%d: an excerpt changed the decision\nwithout: %s\nwith:    %s", name, v, ja, jb)
			}
		}
		for v := 1; v <= findings.Version; v++ {
			a, err := findings.DeriveVersion(t.Context(), p, v)
			if err != nil {
				t.Fatalf("%s findings v%d: %v", name, v, err)
			}
			b, err := findings.DeriveVersion(t.Context(), withExcerpts, v)
			if err != nil {
				t.Fatalf("%s findings v%d with excerpts: %v", name, v, err)
			}
			ja, _ := json.Marshal(a)
			jb, _ := json.Marshal(b)
			if string(ja) != string(jb) {
				t.Errorf("%s findings v%d: an excerpt changed the findings", name, v)
			}
		}
	}
}
