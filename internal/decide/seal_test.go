package decide_test

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/decide"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/findings"
	"github.com/felixapel/calibre-bookwarden/internal/library"
)

// legacyPackage is evidence_test's pkg: a package sealed before M2.4.
func legacyPackage(t *testing.T) evidence.Package {
	t.Helper()
	b := library.Book{
		ID: 3, UUID: "0c57e75c-16aa-4d88-ae0e-916c8f0e8b8e", Title: "Binary Tides",
		Authors: []string{"Cy Placeholder"}, AuthorSort: "Placeholder, Cy", Languages: []string{"eng"},
		Identifiers:  map[string]string{"isbn": "9780000000026"},
		LastModified: time.Date(2026, 10, 12, 21, 5, 55, 0, time.UTC),
		Formats: []library.Format{
			{Name: "AZW3", Size: 11075, MTime: time.Date(2026, 10, 12, 21, 5, 55, 498462000, time.UTC)},
			{Name: "EPUB", Size: 2326, MTime: time.Date(2026, 10, 12, 21, 5, 55, 24520000, time.UTC)},
		},
	}
	snap, err := evidence.Snapshot(b)
	if err != nil {
		t.Fatal(err)
	}
	return evidence.Package{
		SchemaVersion: evidence.SchemaVersionV1, LibraryID: "library", BookID: b.ID, UUID: b.UUID,
		Snapshot: snap, Calibre: evidence.RecordFrom(b),
		Formats: []evidence.FormatEvidence{
			{Format: "AZW3", Size: 11075, SHA256: strings.Repeat("a", 64), State: evidence.StateNotInspected, Reason: evidence.ReasonNoInspector},
			{Format: "EPUB", Size: 2326, State: evidence.StateMissing, Reason: evidence.ReasonMissingOnDisk},
		},
	}
}

// reseal decodes raw, applies edit and seals the result: a package whose
// seal is intact but whose decision no longer matches its evidence.
func reseal(t *testing.T, raw []byte, edit func(*evidence.Package)) ([]byte, string) {
	t.Helper()
	var p evidence.Package
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	edit(&p)
	out, seal, err := evidence.Seal(p)
	if err != nil {
		t.Fatal(err)
	}
	return out, seal
}

func TestOpenDecision(t *testing.T) {
	src := tierA()
	src.Calibre.Languages = nil
	raw, seal, err := decide.Seal(t.Context(), src)
	if err != nil {
		t.Fatal(err)
	}
	p, d, err := decide.Open(t.Context(), raw, seal)
	if err != nil || p.DecisionVersion != decide.Version || p.Tier != evidence.TierA || d.Tier != evidence.TierA ||
		p.FindingsVersion != findings.Version || len(p.Proposals) != 1 || !equalProposals(p.Proposals, d.Proposals) {
		t.Fatalf("Open = %+v, %+v, %v", p, d, err)
	}
	for name, tt := range map[string]struct {
		edit func(*evidence.Package)
		want error
	}{
		"tier promoted":         {func(p *evidence.Package) { p.Tier = evidence.TierB }, decide.ErrMismatch},
		"proposal dropped":      {func(p *evidence.Package) { p.Proposals = nil }, decide.ErrMismatch},
		"proposal edited":       {func(p *evidence.Package) { p.Proposals[0].Proposed = []string{"eng"} }, decide.ErrMismatch},
		"reason added":          {func(p *evidence.Package) { p.TierReasons = []string{"no_record"} }, decide.ErrMismatch},
		"unknown version":       {func(p *evidence.Package) { p.DecisionVersion = decide.Version + 1 }, decide.ErrUnknownVersion},
		"version 0 with a tier": {func(p *evidence.Package) { p.DecisionVersion = 0 }, findings.ErrInvalidEvidence},
	} {
		t.Run(name, func(t *testing.T) {
			bad, badSeal := reseal(t, raw, tt.edit)
			if _, _, err := decide.Open(t.Context(), bad, badSeal); !errors.Is(err, tt.want) {
				t.Fatalf("Open = %v, want %v", err, tt.want)
			}
		})
	}
	// A package sealed under version 1 keeps its version 1 proposals, such
	// as a title that drops Calibre's subtitle.
	v1 := tierA()
	v1.Calibre.Title = T + ": Roman"
	d1, err := decide.AtVersion(t.Context(), v1, 1)
	if err != nil || len(d1.Proposals) != 1 || d1.Proposals[0].Field != "title" || !slices.Equal(d1.Proposals[0].Proposed, []string{T}) {
		t.Fatalf("version 1 decision %+v, %v", d1, err)
	}
	fs, err := findings.Derive(t.Context(), v1)
	if err != nil {
		t.Fatal(err)
	}
	v1.FindingsVersion, v1.Findings = findings.Version, fs
	v1.DecisionVersion, v1.Tier, v1.TierReasons, v1.Proposals = 1, d1.Tier, d1.Reasons, d1.Proposals
	v1Raw, v1Seal, err := evidence.Seal(v1)
	if err != nil {
		t.Fatal(err)
	}
	if p, d, err := decide.Open(t.Context(), v1Raw, v1Seal); err != nil || p.DecisionVersion != 1 || !equalProposals(d.Proposals, d1.Proposals) {
		t.Fatalf("version 1 Open = %+v, %+v, %v", p, d, err)
	}
	bad, badSeal := reseal(t, v1Raw, func(p *evidence.Package) { p.Proposals = nil })
	if _, _, err := decide.Open(t.Context(), bad, badSeal); !errors.Is(err, decide.ErrMismatch) {
		t.Fatalf("version 1 without its proposal: %v, want ErrMismatch", err)
	}
	for _, version := range []int{0, decide.Version + 1} {
		if _, err := decide.AtVersion(t.Context(), v1, version); !errors.Is(err, decide.ErrUnknownVersion) {
			t.Fatalf("AtVersion %d: %v, want ErrUnknownVersion", version, err)
		}
	}
	// A package sealed under version 2 keeps its version 2 proposals, such
	// as authors that drop a name Calibre lists.
	v2 := tierA()
	v2.Calibre.Authors = []string{A, "Klaus Fritz"}
	d2, err := decide.AtVersion(t.Context(), v2, 2)
	if err != nil || len(d2.Proposals) != 1 || d2.Proposals[0].Field != "authors" || !slices.Equal(d2.Proposals[0].Proposed, []string{A}) {
		t.Fatalf("version 2 decision %+v, %v", d2, err)
	}
	fs2, err := findings.Derive(t.Context(), v2)
	if err != nil {
		t.Fatal(err)
	}
	v2.FindingsVersion, v2.Findings = findings.Version, fs2
	v2.DecisionVersion, v2.Tier, v2.TierReasons, v2.Proposals = 2, d2.Tier, d2.Reasons, d2.Proposals
	v2Raw, v2Seal, err := evidence.Seal(v2)
	if err != nil {
		t.Fatal(err)
	}
	if p, d, err := decide.Open(t.Context(), v2Raw, v2Seal); err != nil || p.DecisionVersion != 2 || !equalProposals(d.Proposals, d2.Proposals) {
		t.Fatalf("version 2 Open = %+v, %+v, %v", p, d, err)
	}
	bad2, bad2Seal := reseal(t, v2Raw, func(p *evidence.Package) { p.Proposals = nil })
	if _, _, err := decide.Open(t.Context(), bad2, bad2Seal); !errors.Is(err, decide.ErrMismatch) {
		t.Fatalf("version 2 without its proposal: %v, want ErrMismatch", err)
	}
	// A package sealed under version 3 keeps no sort proposals.
	v3 := tierA()
	wrong := "Stein der Weisen"
	v3.Calibre.TitleSort = &wrong
	d3, err := decide.AtVersion(t.Context(), v3, 3)
	if err != nil || len(d3.Proposals) != 0 {
		t.Fatalf("version 3 decision %+v, %v", d3, err)
	}
	fs3, err := findings.Derive(t.Context(), v3)
	if err != nil {
		t.Fatal(err)
	}
	v3.FindingsVersion, v3.Findings = findings.Version, fs3
	v3.DecisionVersion, v3.Tier, v3.TierReasons, v3.Proposals = 3, d3.Tier, d3.Reasons, d3.Proposals
	v3Raw, v3Seal, err := evidence.Seal(v3)
	if err != nil {
		t.Fatal(err)
	}
	if p, d, err := decide.Open(t.Context(), v3Raw, v3Seal); err != nil || p.DecisionVersion != 3 || len(d.Proposals) != 0 {
		t.Fatalf("version 3 Open = %+v, %+v, %v", p, d, err)
	}
	legacy := legacyPackage(t)
	legacyRaw, legacySeal, err := evidence.Seal(legacy)
	if err != nil || legacySeal != "8509f231af0478e8552b7422a1ca9fc59344c413fc9bfa91e085bf2743627e81" {
		t.Fatalf("legacy seal %s, %v", legacySeal, err)
	}
	lp, ld, err := decide.Open(t.Context(), legacyRaw, legacySeal)
	if err != nil || lp.DecisionVersion != 0 || lp.Tier != "" || ld.Tier != evidence.TierB {
		t.Fatalf("legacy Open = %+v, %+v, %v", lp, ld, err)
	}
}

// TestOpenSchemaV1 seals a package the way schema 1 did, with PDF hints that
// have no context or language, and checks that it opens and that re-deriving
// it gives the same bytes: the findings and the decision don't depend on the
// schema version.
func TestOpenSchemaV1(t *testing.T) {
	p := legacyPackage(t)
	p.Formats[0] = evidence.FormatEvidence{
		Format: "PDF", Size: 100, SHA256: strings.Repeat("b", 64), State: evidence.StateOK, Detected: "pdf", Text: evidence.TextAbsent,
		ISBNHints: &evidence.ISBNHints{Method: evidence.HintOCR, State: evidence.HintOK, Pages: []int{1, 2}, ISBNs: []string{"9780000000026"}},
	}
	raw, seal, err := decide.Seal(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := decide.Open(t.Context(), raw, seal)
	if err != nil {
		t.Fatalf("a schema 1 package didn't open: %v", err)
	}
	if got.SchemaVersion != evidence.SchemaVersionV1 {
		t.Fatalf("schema %d", got.SchemaVersion)
	}
	again, againSeal, err := decide.Seal(t.Context(), got)
	if err != nil || againSeal != seal || string(again) != string(raw) {
		t.Fatalf("re-deriving a schema 1 package changed it: %v\n%s\n%s", err, raw, again)
	}
}
