package agent

import (
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

// TestPacketIgnoresRecordedOnlySources: Wikidata and Hardcover records are
// sealed but not shown to the panel yet, so a package that gained them builds
// the same packet, with the same hash, as before (ADR-035).
func TestPacketIgnoresRecordedOnlySources(t *testing.T) {
	old := nonceSource
	t.Cleanup(func() { nonceSource = old })
	nonceSource = func() (string, error) { return "abc123", nil }

	before, err := BuildPacket(fixturePackage(), fixtureOpen)
	if err != nil {
		t.Fatal(err)
	}
	p := fixturePackage()
	rec := evidence.SourceRecord{ID: "Q1", Title: "WIKIDATA-ONLY-TITLE", Authors: []string{"Nobody"}, ISBNs: []string{"9780679728566"}}
	p.Lookups[0].Results = append(p.Lookups[0].Results,
		evidence.SourceResult{Source: evidence.SourceWikidata, Outcome: evidence.SourceOK, Records: []evidence.SourceRecord{rec}},
		evidence.SourceResult{Source: evidence.SourceHardcover, Outcome: evidence.SourceOK, Records: []evidence.SourceRecord{rec}})
	after, err := BuildPacket(p, fixtureOpen)
	if err != nil {
		t.Fatal(err)
	}
	if after.Text != before.Text || after.Hash != before.Hash {
		t.Fatalf("the packet changed:\n%s", after.Text)
	}
	if strings.Contains(after.Text, "WIKIDATA-ONLY-TITLE") || strings.Contains(after.Text, evidence.SourceWikidata) {
		t.Fatalf("a recorded-only record reached the packet:\n%s", after.Text)
	}
}
