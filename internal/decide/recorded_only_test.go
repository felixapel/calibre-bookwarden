package decide_test

import (
	"encoding/json"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/decide"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

// TestRecordedOnlySources: Wikidata and Hardcover records are sealed with the
// evidence but ignored by every decision version up to 10. They never fail as
// an unknown source, never count as a root and never conflict (ADR-035).
func TestRecordedOnlySources(t *testing.T) {
	other := []string{"Someone Else"}
	extras := map[string][]evidence.SourceResult{
		"agreeing records": {
			ok(evidence.SourceWikidata, rec("Q1", T, []string{A}, []string{"deu"}, I)),
			ok(evidence.SourceHardcover, rec("1", T, []string{A}, []string{"deu"}, I)),
		},
		"conflicting records": {
			ok(evidence.SourceWikidata, rec("Q1", "Another Title", other, []string{"eng"}, I)),
			ok(evidence.SourceHardcover, rec("1", "Yet Another", other, []string{"fra"}, I)),
		},
		"unavailable": {
			{Source: evidence.SourceWikidata, Outcome: evidence.SourceUnavailable},
			{Source: evidence.SourceHardcover, Outcome: evidence.SourceUnavailable},
		},
		"no_match": {
			{Source: evidence.SourceWikidata, Outcome: evidence.SourceNoMatch},
			{Source: evidence.SourceHardcover, Outcome: evidence.SourceNoMatch},
		},
		"other outcomes": {
			{Source: evidence.SourceWikidata, Outcome: evidence.SourceNotQueried},
			{Source: evidence.SourceHardcover, Outcome: evidence.SourceNotConfigured},
		},
	}
	bases := map[string]func(extra ...evidence.SourceResult) evidence.Package{
		"OL and DNB": func(extra ...evidence.SourceResult) evidence.Package {
			res := append([]evidence.SourceResult{
				ok(evidence.SourceDNB, dnbT()),
				ok(evidence.SourceOpenLibrary, rec("/books/OL1M", T, []string{A}, []string{"deu"}, I)),
			}, extra...)
			return book([]evidence.FormatEvidence{epub("", []string{A}, []string{"de"}, []string{I})}, lookup(I, []string{"file"}, res...))
		},
		// Without DNB, the extra records would have been a second root.
		"OL alone, Calibre title unknown": func(extra ...evidence.SourceResult) evidence.Package {
			res := append([]evidence.SourceResult{
				ok(evidence.SourceOpenLibrary, rec("/books/OL1M", T, []string{A}, []string{"deu"}, I)),
			}, extra...)
			p := book([]evidence.FormatEvidence{epub("", []string{A}, []string{"de"}, []string{I})}, lookup(I, []string{"file"}, res...))
			p.Calibre.Title = "Unknown"
			return p
		},
		"no other record": func(extra ...evidence.SourceResult) evidence.Package {
			return book([]evidence.FormatEvidence{epub(T, []string{A}, []string{"de"}, []string{I})}, lookup(I, []string{"file"}, extra...))
		},
	}
	for baseName, build := range bases {
		for extraName, extra := range extras {
			t.Run(baseName+"/"+extraName, func(t *testing.T) {
				for version := 1; version <= decide.Version; version++ {
					want, err := decide.AtVersion(t.Context(), build(), version)
					if err != nil {
						t.Fatalf("v%d without the extra sources: %v", version, err)
					}
					got, err := decide.AtVersion(t.Context(), build(extra...), version)
					if err != nil {
						t.Fatalf("v%d: the extra sources made the decision fail: %v", version, err)
					}
					w, _ := json.Marshal(want)
					g, _ := json.Marshal(got)
					if string(w) != string(g) {
						t.Fatalf("v%d: the extra sources changed the decision\nwithout: %s\nwith:    %s", version, w, g)
					}
				}
			})
		}
	}
}

// A package that carries ok Wikidata and Hardcover records seals and opens
// again, with the decision it had without them.
func TestRecordedOnlySealRoundTrip(t *testing.T) {
	p := tierA()
	p.Lookups[0].Results = append(p.Lookups[0].Results,
		ok(evidence.SourceWikidata, rec("Q1", "Another Title", []string{"Someone Else"}, []string{"eng"}, I)),
		ok(evidence.SourceHardcover, rec("1", T, []string{A}, []string{"deu"}, I)))
	raw, seal, err := decide.Seal(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	got, d, err := decide.Open(t.Context(), raw, seal)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	want, err := decide.Decide(t.Context(), tierA())
	if err != nil {
		t.Fatal(err)
	}
	g, _ := json.Marshal(d)
	w, _ := json.Marshal(want)
	if string(g) != string(w) || len(got.Lookups[0].Results) != 3 {
		t.Fatalf("decision %s, want %s; %d results", g, w, len(got.Lookups[0].Results))
	}
}

// An unknown source still fails closed.
func TestUnknownSourceStillFails(t *testing.T) {
	p := book([]evidence.FormatEvidence{epub(T, []string{A}, []string{"de"}, []string{I})},
		lookup(I, []string{"file"}, ok("somewhere", rec("x", T, []string{A}, []string{"deu"}, I))))
	if _, err := decide.Decide(t.Context(), p); err == nil {
		t.Fatal("an unknown source did not fail")
	}
}
