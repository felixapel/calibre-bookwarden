package findings

import (
	"context"
	"errors"
	"math/bits"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

// hashAt returns a dHash that differs from base in the n lowest bits.
func hashAt(t *testing.T, base string, n int) string {
	t.Helper()
	v, err := strconv.ParseUint(base, 16, 64)
	if err != nil {
		t.Fatal(err)
	}
	var mask uint64 = 1<<n - 1
	out := strconv.FormatUint(v^mask, 16)
	for len(out) < 16 {
		out = "0" + out
	}
	if got := bits.OnesCount64(v ^ mustHash(t, out)); got != n {
		t.Fatalf("hashAt made distance %d, want %d", got, n)
	}
	return out
}

func mustHash(t *testing.T, s string) uint64 {
	t.Helper()
	v, err := strconv.ParseUint(s, 16, 64)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

const calHash = "0123456789abcdef"

func candidate(source string, w, h, flat int, dhash string) *evidence.CoverCandidate {
	c := &evidence.CoverCandidate{Source: source, State: evidence.StateOK, MediaType: "image/jpeg", SHA256: strings.Repeat("aa", 32), Width: w, Height: h, DHash: dhash, Flatness: flat}
	return c
}

func withCandidates(cal *evidence.CoverEvidence, file *evidence.CoverCandidate) evidence.Package {
	p := goodPackage()
	p.Calibre.HasCover = cal != nil && cal.State != evidence.StateMissing
	p.Cover = cal
	p.Formats[0].FileCover = file
	return p
}

func better(source, format string) evidence.Finding {
	return evidence.Finding{Code: "cover_better_available", Format: format, Reason: source}
}

func betterOnly(fs []evidence.Finding) []evidence.Finding {
	var out []evidence.Finding
	for _, f := range fs {
		if f.Code == "cover_better_available" {
			out = append(out, f)
		}
	}
	return out
}

func TestCoverBetterAvailable(t *testing.T) {
	missing := &evidence.CoverEvidence{State: evidence.StateMissing, Flatness: -1}
	lowRes := decoded(400, 600, 500) // shortest side 400; 1.5x is 600
	lowRes.DHash = calHash
	good := decoded(800, 1200, 500)
	good.DHash = calHash
	corrupt := &evidence.CoverEvidence{State: evidence.StateCorrupt, Bytes: 9, SHA256: "ab", Flatness: -1}
	big := func(w, h int) *evidence.CoverCandidate { return candidate("epub", w, h, 400, calHash) }

	tests := []struct {
		name string
		p    evidence.Package
		want []evidence.Finding
	}{
		{"Calibre has no cover, EPUB cover is big", withCandidates(missing, big(900, 1400)), []evidence.Finding{better("epub", "EPUB")}},
		{"no dHash condition when Calibre has none", withCandidates(missing, candidate("epub", 900, 1400, 400, "ffffffffffffffff")), []evidence.Finding{better("epub", "EPUB")}},
		{"candidate 599 px", withCandidates(missing, big(599, 1400)), nil},
		{"candidate 600 px", withCandidates(missing, big(600, 1400)), []evidence.Finding{better("epub", "EPUB")}},
		{"candidate short height 599", withCandidates(missing, big(1400, 599)), nil},
		{"candidate is a placeholder (flat)", withCandidates(missing, candidate("epub", 900, 1400, 29, calHash)), nil},
		{"candidate flatness 30", withCandidates(missing, candidate("epub", 900, 1400, 30, calHash)), []evidence.Finding{better("epub", "EPUB")}},
		{"candidate missing", withCandidates(missing, &evidence.CoverCandidate{Source: "epub", State: evidence.StateMissing, Flatness: -1}), nil},
		{"candidate corrupt", withCandidates(missing, &evidence.CoverCandidate{Source: "epub", State: evidence.StateCorrupt, Flatness: -1}), nil},
		{"candidate too large", withCandidates(missing, &evidence.CoverCandidate{Source: "epub", State: evidence.StateTooLarge, Flatness: -1}), nil},
		{"candidate not inspected", withCandidates(missing, &evidence.CoverCandidate{Source: "epub", State: evidence.StateNotInspected, Flatness: -1}), nil},
		{"no candidate", withCandidates(missing, nil), nil},

		{"Calibre low resolution, 1.5x, same artwork", withCandidates(lowRes, big(600, 900)), []evidence.Finding{better("epub", "EPUB")}},
		{"just under 1.5x: 898 vs 599", withCandidates(withHash(decoded(599, 800, 500)), big(898, 1200)), nil},
		{"exactly 1.5x: 600 vs 400", withCandidates(withHash(decoded(400, 800, 500)), big(600, 1200)), []evidence.Finding{better("epub", "EPUB")}},
		{"just under 1.5x", withCandidates(withHash(decoded(401, 800, 500)), big(601, 1200)), nil},
		{"just over 1.5x", withCandidates(withHash(decoded(399, 800, 500)), big(600, 1200)), []evidence.Finding{better("epub", "EPUB")}},
		{"dHash distance 12", withCandidates(lowRes, candidate("epub", 900, 1400, 400, hashAt(t, calHash, 12))), []evidence.Finding{better("epub", "EPUB")}},
		{"dHash distance 13", withCandidates(lowRes, candidate("epub", 900, 1400, 400, hashAt(t, calHash, 13))), nil},
		{"dHash distance 0", withCandidates(lowRes, candidate("epub", 900, 1400, 400, calHash)), []evidence.Finding{better("epub", "EPUB")}},
		{"tiny cover that is not flat keeps the hash check: same artwork", withCandidates(withHash(decoded(40, 60, 500)), big(900, 1400)), []evidence.Finding{better("epub", "EPUB")}},
		{"tiny cover that is not flat keeps the hash check: other artwork", withCandidates(withHash(decoded(40, 60, 500)), candidate("epub", 900, 1400, 400, hashAt(t, calHash, 13))), nil},
		{"flatness 29 Calibre cover is treated as missing", withCandidates(withHash(decoded(400, 600, 29)), candidate("epub", 900, 1400, 400, hashAt(t, calHash, 30))), []evidence.Finding{better("epub", "EPUB")}},
		{"flatness 30 Calibre cover is not", withCandidates(withHash(decoded(400, 600, 30)), candidate("epub", 900, 1400, 400, hashAt(t, calHash, 30))), nil},
		{"Calibre cover is fine", withCandidates(good, big(1800, 2400)), nil},
		{"Calibre cover is corrupt", withCandidates(corrupt, big(900, 1400)), nil},
		{"no Calibre cover evidence", withCandidates(nil, big(900, 1400)), nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Derive(context.Background(), tc.p)
			if err != nil {
				t.Fatal(err)
			}
			if b := betterOnly(got); !slices.Equal(b, tc.want) {
				t.Errorf("got %+v, want %+v", b, tc.want)
			}
		})
	}
}

func withHash(c *evidence.CoverEvidence) *evidence.CoverEvidence {
	c.DHash = calHash
	return c
}

// The 1.5x rule in integers, on both sides of the boundary, for any shortest
// side.
func TestCoverScaleRule(t *testing.T) {
	for _, tc := range []struct {
		cal, cand int
		want      bool
	}{{400, 600, true}, {400, 599, false}, {401, 601, false}, {401, 602, true}, {0, 600, true}, {599, 898, false}, {599, 899, true}} {
		if got := coverScaleOK(tc.cal, tc.cand); got != tc.want {
			t.Errorf("coverScaleOK(%d, %d) = %v, want %v", tc.cal, tc.cand, got, tc.want)
		}
	}
}

func TestHamming(t *testing.T) {
	if CoverMaxHamming != 12 {
		t.Errorf("CoverMaxHamming = %d, ADR-043 says 12", CoverMaxHamming)
	}
	a, b := "0000000000000000", "ffffffffffffffff"
	if d, ok := hamming(a, b); !ok || d != 64 {
		t.Errorf("hamming = %d %v", d, ok)
	}
	if _, ok := hamming(a, "xyz"); ok {
		t.Error("a malformed hash has a distance")
	}
}

// Findings versions 1 to 5 derive nothing from candidates; the sealed v5
// golden stays as it was.
func TestCoverBetterOnlyFromVersionSix(t *testing.T) {
	p := withCandidates(&evidence.CoverEvidence{State: evidence.StateMissing, Flatness: -1}, candidate("epub", 900, 1400, 400, calHash))
	for v := 1; v <= 6; v++ {
		got, err := DeriveVersion(context.Background(), p, v)
		if v < 4 {
			if !errors.Is(err, ErrInvalidEvidence) { // a cover before version 4 is refused, as always
				t.Errorf("version %d: %v", v, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("version %d: %v", v, err)
		}
		if has := len(betterOnly(got)) > 0; has != (v == 6) {
			t.Errorf("version %d: cover_better_available = %v", v, has)
		}
	}
}

func TestCoverCandidateEvidenceChecked(t *testing.T) {
	missing := &evidence.CoverEvidence{State: evidence.StateMissing, Flatness: -1}
	bad := candidate("epub", 900, 1400, 400, "ZZ")
	if _, err := Derive(context.Background(), withCandidates(missing, bad)); !errors.Is(err, ErrInvalidEvidence) {
		t.Errorf("a malformed candidate: %v", err)
	}
	wrongSource := candidate("other", 900, 1400, 400, calHash)
	if _, err := Derive(context.Background(), withCandidates(missing, wrongSource)); !errors.Is(err, ErrInvalidEvidence) {
		t.Errorf("a candidate with another source: %v", err)
	}
}

func TestCoverBetterIsCatalogued(t *testing.T) {
	for _, d := range Catalog() {
		if d.Code == "cover_better_available" {
			if d.Severity != "info" || d.Label == "" {
				t.Errorf("%+v", d)
			}
			return
		}
	}
	t.Error("cover_better_available is not in the catalogue")
}

func TestCoverBetterReasonLabels(t *testing.T) {
	for _, source := range []string{"epub"} {
		if ReasonLabel("cover_better_available", source) == source {
			t.Errorf("no label for source %s", source)
		}
	}
}
