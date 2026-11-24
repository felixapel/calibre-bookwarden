package decide_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/decide"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

// withCoverCandidates is p as a schema 5 package whose Calibre cover is
// missing and whose formats carry an EPUB cover candidate.
func withCoverCandidates(p evidence.Package) evidence.Package {
	p.SchemaVersion = evidence.SchemaVersion
	p.Cover = &evidence.CoverEvidence{State: evidence.StateMissing, Flatness: -1}
	p.Formats = slices.Clone(p.Formats)
	for i := range p.Formats {
		p.Formats[i].FileCover = &evidence.CoverCandidate{Source: evidence.CandidateEPUB, State: evidence.StateOK, MediaType: "image/jpeg",
			SHA256: strings.Repeat("ab", 32), Width: 1200, Height: 1800, DHash: "0123456789abcdef", Flatness: 400}
	}
	return p
}

// TestCoverCandidatesChangeNothing: candidates are sealed with the evidence but
// no decision version reads covers. Every replay fixture, and the version 8
// books, decide the same at every version with candidates as without.
func TestCoverCandidatesChangeNothing(t *testing.T) {
	fixtures := replayFixtures()
	fixtures["v8 base"] = v8Base()
	for name, p := range fixtures {
		with := withCoverCandidates(p)
		if _, _, err := decide.Seal(t.Context(), with); err != nil {
			t.Fatalf("%s: the fixture with candidates does not seal: %v", name, err)
		}
		for v := 1; v <= decide.Version; v++ {
			a, err := decide.AtVersion(t.Context(), p, v)
			if err != nil {
				t.Fatalf("%s decide v%d: %v", name, v, err)
			}
			b, err := decide.AtVersion(t.Context(), with, v)
			if err != nil {
				t.Fatalf("%s decide v%d with candidates: %v", name, v, err)
			}
			ja, _ := json.Marshal(a)
			jb, _ := json.Marshal(b)
			if string(ja) != string(jb) {
				t.Errorf("%s decide v%d: candidates changed the decision\nwithout: %s\nwith:    %s", name, v, ja, jb)
			}
		}
	}
}
