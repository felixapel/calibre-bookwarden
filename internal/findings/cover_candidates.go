package findings

// Findings version 6 (ADR-043) adds cover_better_available: Calibre's cover is
// missing, small or a placeholder, and a candidate measured from the EPUB
// itself is a big enough picture of the same artwork. It
// only observes: nothing here writes a cover or proposes a value, and no
// decision version reads covers.

import (
	"math/bits"
	"strconv"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

// Version 6's candidate thresholds (ADR-043); changing one needs a new
// version.
const (
	// CoverMaxHamming is the largest Hamming distance between the 64-bit
	// difference hashes of Calibre's cover and a candidate for the two to count
	// as the same artwork. Two unrelated covers differ in about 32 bits.
	CoverMaxHamming = 12
	// A candidate must be at least coverScaleNum/coverScaleDen (1.5) times
	// Calibre's cover in its shortest side, when Calibre has a cover.
	coverScaleNum = 3
	coverScaleDen = 2
)

// coverWantsBetter reports whether a better cover would help: Calibre's cover
// is missing, below the minimum resolution, or a placeholder. A cover that is
// corrupt or wasn't inspected is not a case: there is nothing to compare.
func coverWantsBetter(c *evidence.CoverEvidence) bool {
	if c == nil {
		return false
	}
	switch c.State {
	case evidence.StateMissing:
		return true
	case evidence.StateOK:
		return min(c.Width, c.Height) < coverMinSide || CoverPlaceholder(*c)
	}
	return false
}

// coverScaleOK reports whether cand is at least 1.5 times cal, in integers.
func coverScaleOK(cal, cand int) bool { return cand*coverScaleDen >= cal*coverScaleNum }

// hamming is the number of differing bits between two dHashes; ok is false for
// a hash that isn't 16 hex digits.
func hamming(a, b string) (int, bool) {
	if !dhashPattern.MatchString(a) || !dhashPattern.MatchString(b) {
		return 0, false
	}
	x, errA := strconv.ParseUint(a, 16, 64)
	y, errB := strconv.ParseUint(b, 16, 64)
	if errA != nil || errB != nil {
		return 0, false
	}
	return bits.OnesCount64(x ^ y), true
}

// coverCandidateFindings derives cover_better_available for each candidate
// that qualifies, with the candidate's source as the reason. An EPUB's own
// cover names its format.
func (d *derivation) coverCandidateFindings(p evidence.Package) error {
	for _, f := range p.Formats {
		if f.FileCover != nil && f.FileCover.Validate(evidence.CandidateEPUB) != nil {
			return invalidEvidence()
		}
	}
	if !coverWantsBetter(p.Cover) {
		return nil
	}
	for _, f := range p.Formats {
		if f.FileCover != nil && candidateBetter(p.Cover, f.FileCover) {
			d.add("cover_better_available", f.Format, "", evidence.CandidateEPUB)
		}
	}
	return nil
}

// candidateBetter applies version 6's rule to one candidate, given that
// Calibre's cover wants a better one.
func candidateBetter(cal *evidence.CoverEvidence, c *evidence.CoverCandidate) bool {
	if c.State != evidence.StateOK || min(c.Width, c.Height) < coverMinSide || coverFlat(c.Width, c.Height, c.Flatness) {
		return false
	}
	if cal.State != evidence.StateOK || cal.Flatness < coverFlatTenths {
		// Calibre has no cover, or one that is near-uniform: a flat image has
		// no artwork for the candidate to be the same as, so it is treated
		// like a missing cover. A tiny cover that isn't flat is a thumbnail of
		// real artwork and keeps the size and hash conditions.
		return true
	}
	if !coverScaleOK(min(cal.Width, cal.Height), min(c.Width, c.Height)) {
		return false
	}
	dist, ok := hamming(cal.DHash, c.DHash)
	return ok && dist <= CoverMaxHamming
}
