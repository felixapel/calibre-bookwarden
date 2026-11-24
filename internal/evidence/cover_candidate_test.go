package evidence_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

func okCandidate() *evidence.CoverCandidate {
	return &evidence.CoverCandidate{
		Source: evidence.CandidateEPUB, State: evidence.StateOK, MediaType: "image/jpeg",
		SHA256: strings.Repeat("ab", 32), Width: 1200, Height: 1800, DHash: "00ff00ff00ff00ff", Flatness: 412,
	}
}

func candidatePackage(t *testing.T, version int) evidence.Package {
	t.Helper()
	p := pkg(t)
	p.SchemaVersion = version
	p.Formats = append([]evidence.FormatEvidence(nil), p.Formats...)
	p.Formats[1].FileCover = okCandidate()
	return p
}

func TestCoverCandidateRoundTrip(t *testing.T) {
	p := candidatePackage(t, evidence.SchemaVersion)
	raw, seal, err := evidence.Seal(p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := evidence.Open(raw, seal)
	if err != nil {
		t.Fatal(err)
	}
	if *got.Formats[1].FileCover != *p.Formats[1].FileCover {
		t.Errorf("round trip changed the candidate:\n got %+v\nwant %+v", got.Formats[1].FileCover, p.Formats[1].FileCover)
	}
	for _, key := range []string{`"file_cover"`, `"dhash":"00ff00ff00ff00ff"`, `"flatness":412`, `"media_type":"image/jpeg"`} {
		if !bytes.Contains(raw, []byte(key)) {
			t.Errorf("sealed package lacks %s", key)
		}
	}
}

func TestCoverCandidateRefusedBeforeSchema5(t *testing.T) {
	for _, version := range []int{1, 2, 3, 4} {
		p := candidatePackage(t, version)
		raw, seal, err := evidence.Seal(p)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := evidence.Open(raw, seal); !errors.Is(err, evidence.ErrInvalidEvidence) {
			t.Errorf("schema %d: err = %v, want ErrInvalidEvidence", version, err)
		}
	}
	if evidence.SchemaVersionV5 != 5 {
		t.Errorf("SchemaVersionV5 = %d", evidence.SchemaVersionV5)
	}
}

func TestCoverCandidateShapeRefused(t *testing.T) {
	with := func(c evidence.CoverCandidate) func(p *evidence.Package) {
		return func(p *evidence.Package) { p.Formats[1].FileCover = &c }
	}
	bad := map[string]func(p *evidence.Package){
		"unknown state":           func(p *evidence.Package) { p.Formats[1].FileCover.State = "pending" },
		"ok without a hash":       func(p *evidence.Package) { p.Formats[1].FileCover.DHash = "" },
		"ok with a bad hash":      func(p *evidence.Package) { p.Formats[1].FileCover.DHash = "XYZ" },
		"ok without flatness":     func(p *evidence.Package) { p.Formats[1].FileCover.Flatness = -1 },
		"ok without dimensions":   func(p *evidence.Package) { p.Formats[1].FileCover.Width = 0 },
		"ok without sha":          func(p *evidence.Package) { p.Formats[1].FileCover.SHA256 = "" },
		"ok non image media type": func(p *evidence.Package) { p.Formats[1].FileCover.MediaType = "application/xhtml+xml" },
		"missing with a hash": with(evidence.CoverCandidate{Source: evidence.CandidateEPUB,
			State: evidence.StateMissing, Flatness: -1, DHash: "00ff00ff00ff00ff"}),
		"corrupt with flatness": with(evidence.CoverCandidate{Source: evidence.CandidateEPUB, State: evidence.StateCorrupt, Flatness: 3}),
		"another source":        func(p *evidence.Package) { p.Formats[1].FileCover.Source = "openlibrary" },
		"negative dimensions":   func(p *evidence.Package) { p.Formats[1].FileCover.Width = -1 },
		"malformed sha":         func(p *evidence.Package) { p.Formats[1].FileCover.SHA256 = "abc" },
	}
	for name, mut := range bad {
		t.Run(name, func(t *testing.T) {
			p := candidatePackage(t, evidence.SchemaVersion)
			mut(&p)
			raw, seal, err := evidence.Seal(p)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := evidence.Open(raw, seal); !errors.Is(err, evidence.ErrInvalidEvidence) {
				t.Errorf("err = %v, want ErrInvalidEvidence", err)
			}
		})
	}
	for _, s := range []evidence.State{evidence.StateMissing, evidence.StateCorrupt, evidence.StateTooLarge, evidence.StateNotInspected} {
		p := candidatePackage(t, evidence.SchemaVersion)
		p.Formats[1].FileCover = &evidence.CoverCandidate{Source: evidence.CandidateEPUB, State: s, Flatness: -1}
		raw, seal, err := evidence.Seal(p)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := evidence.Open(raw, seal); err != nil {
			t.Errorf("state %s without measures: %v", s, err)
		}
	}
}

// Without a candidate a schema 5 package has no candidate key at all, so
// packages sealed before them keep their bytes.
func TestNoCandidateAddsNoKeys(t *testing.T) {
	raw, _, err := evidence.Seal(pkg(t))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("file_cover")) {
		t.Errorf("candidate key on a package without one: %s", raw)
	}
}
