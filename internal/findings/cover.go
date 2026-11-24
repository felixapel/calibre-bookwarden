package findings

import (
	"regexp"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

// Version 4's cover thresholds (ADR-023); changing one needs a new version.
const (
	coverMinSide         = 600 // pixels, the shortest side
	coverPlaceholderSide = 50  // pixels, the shortest side
	coverFlatTenths      = 30  // luminance standard deviation, in tenths
)

var dhashPattern = regexp.MustCompile(`^[0-9a-f]{16}$`)

// coverFindings checks the cover evidence's shape and derives its findings.
func (d *derivation) coverFindings(c evidence.CoverEvidence) error {
	decoded := c.State == evidence.StateOK
	if decoded != (c.Flatness >= 0) || decoded != (c.DHash != "") || !knownReason(c.Reason) {
		return invalidEvidence()
	}
	switch c.State {
	case evidence.StateMissing:
		d.add("cover_missing", "", "", c.Reason)
	case evidence.StateCorrupt:
		d.add("cover_corrupt", "", "", c.Reason)
	case evidence.StateNotInspected:
		d.add("cover_uninspected", "", "", c.Reason)
	case evidence.StateOK:
		if c.Width <= 0 || c.Height <= 0 || !dhashPattern.MatchString(c.DHash) {
			return invalidEvidence()
		}
		if min(c.Width, c.Height) < coverMinSide {
			d.add("cover_low_resolution", "", "", "")
		}
		if CoverPlaceholder(c) {
			d.add("cover_placeholder", "", "", "")
		}
	default:
		return invalidEvidence()
	}
	return nil
}

// CoverPlaceholder is version 4's placeholder rule for a decoded cover: a
// near-uniform image or a tiny one.
func CoverPlaceholder(c evidence.CoverEvidence) bool { return coverFlat(c.Width, c.Height, c.Flatness) }

// coverFlat is that rule on a measured image: also applied to cover candidates.
func coverFlat(width, height, flatness int) bool {
	return min(width, height) < coverPlaceholderSide || flatness < coverFlatTenths
}
