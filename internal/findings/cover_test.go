package findings

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

func decoded(w, h, flatness int) *evidence.CoverEvidence {
	return &evidence.CoverEvidence{State: evidence.StateOK, Bytes: 1000, SHA256: "ab", Detected: "jpeg", Width: w, Height: h, DHash: "0123456789abcdef", Flatness: flatness}
}

func TestCoverFindings(t *testing.T) {
	for _, tt := range []struct {
		name  string
		cover *evidence.CoverEvidence
		has   bool
		want  []evidence.Finding
	}{
		{"good", decoded(800, 1200, 500), true, nil},
		{"no cover in Calibre", &evidence.CoverEvidence{State: evidence.StateMissing, Flatness: -1}, false, []evidence.Finding{{Code: "cover_missing"}}},
		{"listed but not sent", &evidence.CoverEvidence{State: evidence.StateMissing, Reason: evidence.ReasonMissingOnDisk, Flatness: -1}, true, []evidence.Finding{{Code: "cover_missing", Reason: "missing_on_disk"}}},
		{"corrupt", &evidence.CoverEvidence{State: evidence.StateCorrupt, Bytes: 9, SHA256: "ab", Flatness: -1}, true, []evidence.Finding{{Code: "cover_corrupt"}}},
		{"too large", &evidence.CoverEvidence{State: evidence.StateNotInspected, Reason: evidence.ReasonTooLarge, Flatness: -1}, true, []evidence.Finding{{Code: "cover_uninspected", Reason: "too_large"}}},
		{"timeout", &evidence.CoverEvidence{State: evidence.StateNotInspected, Reason: evidence.ReasonTimeout, Flatness: -1}, true, []evidence.Finding{{Code: "cover_uninspected", Reason: "timeout"}}},
		{"599 px", decoded(599, 900, 500), true, []evidence.Finding{{Code: "cover_low_resolution"}}},
		{"600 px", decoded(600, 900, 500), true, nil},
		{"short height", decoded(900, 599, 500), true, []evidence.Finding{{Code: "cover_low_resolution"}}},
		{"flatness 29", decoded(800, 1200, 29), true, []evidence.Finding{{Code: "cover_placeholder"}}},
		{"flatness 30", decoded(800, 1200, 30), true, nil},
		{"49 px", decoded(49, 400, 500), true, []evidence.Finding{{Code: "cover_low_resolution"}, {Code: "cover_placeholder"}}},
		{"50 px", decoded(50, 400, 500), true, []evidence.Finding{{Code: "cover_low_resolution"}}},
	} {
		p := goodPackage()
		p.Calibre.HasCover = tt.has
		p.Cover = tt.cover
		got, err := Derive(context.Background(), p)
		if err != nil || !slices.Equal(got, tt.want) {
			t.Errorf("%s: %+v, %v; want %+v", tt.name, got, err, tt.want)
		}
	}
}

func TestCoverEvidenceChecked(t *testing.T) {
	for name, c := range map[string]*evidence.CoverEvidence{
		"unknown state":     {State: "shiny", Flatness: -1},
		"unknown reason":    {State: evidence.StateNotInspected, Reason: "bored", Flatness: -1},
		"ok without hash":   {State: evidence.StateOK, Width: 800, Height: 1200, Flatness: 50},
		"ok without size":   {State: evidence.StateOK, DHash: "0123456789abcdef", Flatness: 50},
		"ok, no flatness":   {State: evidence.StateOK, Width: 800, Height: 1200, DHash: "0123456789abcdef", Flatness: -1},
		"bad hash":          {State: evidence.StateOK, Width: 800, Height: 1200, DHash: "0123456789ABCDEF", Flatness: 50},
		"hash not decoded":  {State: evidence.StateCorrupt, DHash: "0123456789abcdef", Flatness: -1},
		"flatness, corrupt": {State: evidence.StateCorrupt, Flatness: 3},
	} {
		p := goodPackage()
		p.Calibre.HasCover = true
		p.Cover = c
		if _, err := Derive(context.Background(), p); !errors.Is(err, ErrInvalidEvidence) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestLegacyPackagesDeriveNoCoverFindings(t *testing.T) {
	ctx := context.Background()
	p := goodPackage()
	p.Calibre.HasCover = false // a version 1–3 package never carries cover evidence
	for _, v := range []int{1, 2, 3, Version} {
		got, err := DeriveVersion(ctx, p, v)
		if err != nil || len(got) != 0 {
			t.Errorf("version %d: %+v, %v", v, got, err)
		}
	}
	p.Cover = decoded(10, 10, 0)
	for _, v := range []int{1, 2, 3} {
		if _, err := DeriveVersion(ctx, p, v); !errors.Is(err, ErrInvalidEvidence) {
			t.Errorf("version %d with cover evidence: %v", v, err)
		}
	}
	raw, seal, err := Seal(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Open(ctx, raw, seal)
	if err != nil || got.FindingsVersion != Version || len(got.Findings) != 2 {
		t.Fatalf("package sealed at the current version: %+v, %v", got.Findings, err)
	}
}

func TestCoverPlaceholder(t *testing.T) {
	for _, tt := range []struct {
		w, h, flatness int
		want           bool
	}{
		{600, 900, 30, false},
		{600, 900, 29, true},
		{50, 900, 400, false},
		{900, 49, 400, true},
	} {
		c := evidence.CoverEvidence{State: evidence.StateOK, Width: tt.w, Height: tt.h, DHash: "00000000000000ff", Flatness: tt.flatness}
		if got := CoverPlaceholder(c); got != tt.want {
			t.Errorf("CoverPlaceholder(%d×%d, %d) = %v", tt.w, tt.h, tt.flatness, got)
		}
	}
}
