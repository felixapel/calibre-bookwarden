package decide_test

import (
	"bytes"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/decide"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

// withSeries is p as a schema 5 package that carries a series everywhere it
// can: Calibre's record, every format's metadata, and every source record,
// the file and the sources disagreeing with Calibre on name and index.
func withSeries(p evidence.Package) evidence.Package {
	p.SchemaVersion = evidence.SchemaVersion
	p.Calibre.Series, p.Calibre.SeriesIndex = "The Spike Saga", "2.5"
	p.Formats = slices.Clone(p.Formats)
	for i, f := range p.Formats {
		if f.Metadata == nil {
			continue
		}
		m := *f.Metadata
		m.Series, m.SeriesIndex = "Another Saga", "7"
		p.Formats[i].Metadata = &m
	}
	p.Lookups = slices.Clone(p.Lookups)
	for i, l := range p.Lookups {
		l.Results = slices.Clone(l.Results)
		for j, r := range l.Results {
			r.Records = slices.Clone(r.Records)
			for k := range r.Records {
				r.Records[k].Series, r.Records[k].SeriesIndex = "Third Saga", "1"
			}
			l.Results[j] = r
		}
		p.Lookups[i] = l
	}
	return p
}

// TestSchema4SealedPackageReplays: testdata/schema4_sealed.json is a package
// sealed by the code that predates schema 5 (findings version 5, decision
// version 8, an extraction, proposals). It must still open, derive the same
// findings and decision it was sealed with, and re-seal to the same bytes.
func TestSchema4SealedPackageReplays(t *testing.T) {
	raw, err := os.ReadFile("testdata/schema4_sealed.json")
	if err != nil {
		t.Fatal(err)
	}
	s, err := os.ReadFile("testdata/schema4_sealed.seal")
	if err != nil {
		t.Fatal(err)
	}
	seal := strings.TrimSpace(string(s))
	p, d, err := decide.Open(t.Context(), raw, seal)
	if err != nil {
		t.Fatalf("a schema 4 package no longer opens: %v", err)
	}
	if p.SchemaVersion != evidence.SchemaVersionV4 || p.FindingsVersion != 5 || p.DecisionVersion != 8 || len(p.Proposals) == 0 || p.Formats[0].Extraction == nil {
		t.Fatalf("schema %d, findings v%d, decision v%d, %d proposals", p.SchemaVersion, p.FindingsVersion, p.DecisionVersion, len(p.Proposals))
	}
	if d.Tier != p.Tier || p.Calibre.Series != "" || p.Formats[0].Metadata.Series != "" {
		t.Errorf("tier %s, stored %s", d.Tier, p.Tier)
	}
	again, againSeal, err := evidence.Seal(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(again, raw) || againSeal != seal {
		t.Errorf("re-sealing changed the package")
	}
}

// TestSeriesChangesNothing: the series is read and sealed but no decision
// version reads it yet. Every replay fixture, and the version 8 books, given
// a series on every side, decide the same at every version as without.
func TestSeriesChangesNothing(t *testing.T) {
	fixtures := replayFixtures()
	fixtures["v8 base"] = v8Base()
	for name, p := range fixtures {
		with := withSeries(p)
		if with.Calibre.Series == "" {
			t.Fatalf("%s: the fixture has no series", name)
		}
		if _, _, err := decide.Seal(t.Context(), with); err != nil {
			t.Fatalf("%s: the fixture with a series does not seal: %v", name, err)
		}
		for v := 1; v <= decide.Version; v++ {
			a, err := decide.AtVersion(t.Context(), p, v)
			if err != nil {
				t.Fatalf("%s decide v%d: %v", name, v, err)
			}
			b, err := decide.AtVersion(t.Context(), with, v)
			if err != nil {
				t.Fatalf("%s decide v%d with a series: %v", name, v, err)
			}
			ja, _ := json.Marshal(a)
			jb, _ := json.Marshal(b)
			if string(ja) != string(jb) {
				t.Errorf("%s decide v%d: a series changed the decision\nwithout: %s\nwith:    %s", name, v, ja, jb)
			}
		}
	}
}
