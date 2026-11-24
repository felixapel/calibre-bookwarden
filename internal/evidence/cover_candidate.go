package evidence

import (
	"fmt"
	"regexp"
	"slices"
)

// StateTooLarge is a cover candidate whose image was past the size cap. Only
// cover candidates use it; a format or the Calibre cover says not_inspected
// with ReasonTooLarge instead.
const StateTooLarge State = "too_large"

// CandidateEPUB names a cover candidate that came from an EPUB's own OPF.
const CandidateEPUB = "epub"

// CoverCandidate is the measures of an alternative cover (ADR-043): the cover
// image an EPUB's own OPF names. It holds measures only, never image bytes.
// State is ok, missing, corrupt, too_large or not_inspected; the measures are
// set only for ok, and Flatness is -1 otherwise. Schema 5.
type CoverCandidate struct {
	Source    string `json:"source"`
	State     State  `json:"state"`
	MediaType string `json:"media_type,omitempty"`
	SHA256    string `json:"sha256,omitempty"` // of the image bytes
	Width     int    `json:"width,omitempty"`
	Height    int    `json:"height,omitempty"`
	DHash     string `json:"dhash,omitempty"`
	Flatness  int    `json:"flatness"`
}

var (
	candidateMediaTypes = []string{"image/jpeg", "image/png", "image/gif", "image/webp"}
	candidateDHash      = regexp.MustCompile(`^[0-9a-f]{16}$`)
	candidateSHA        = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// checkCoverCandidates refuses candidates in a package of schema 4 or
// earlier, which can't have been sealed with them, and a candidate whose
// fields contradict its state or its source.
func (p Package) checkCoverCandidates() error {
	has := false
	for _, f := range p.Formats {
		if c := f.FileCover; c != nil {
			has = true
			if err := c.Validate(CandidateEPUB); err != nil {
				return fmt.Errorf("%s: %w", f.Format, err)
			}
		}
	}
	if has && p.SchemaVersion < SchemaVersionV5 {
		return fmt.Errorf("%w: schema %d holds a schema 5 cover candidate", ErrInvalidEvidence, p.SchemaVersion)
	}
	return nil
}

// Validate checks the candidate against its source and its state's closed set.
func (c *CoverCandidate) Validate(source string) error {
	bad := func(what string) error {
		return fmt.Errorf("%w: the %s cover candidate %s", ErrInvalidEvidence, source, what)
	}
	if c.Source != source {
		return bad("names the source " + c.Source)
	}
	if c.Width < 0 || c.Height < 0 {
		return bad("has negative dimensions")
	}
	if c.SHA256 != "" && !candidateSHA.MatchString(c.SHA256) {
		return bad("has a malformed hash")
	}
	switch c.State {
	case StateOK:
		if c.Width == 0 || c.Height == 0 || c.Flatness < 0 || !candidateDHash.MatchString(c.DHash) ||
			c.SHA256 == "" || !slices.Contains(candidateMediaTypes, c.MediaType) {
			return bad("is ok without its measures")
		}
	case StateMissing, StateTooLarge:
		if c.SHA256 != "" || c.MediaType != "" || c.Width != 0 || c.Height != 0 || c.DHash != "" || c.Flatness != -1 {
			return bad("is " + string(c.State) + " with measures")
		}
	case StateCorrupt, StateNotInspected:
		if c.DHash != "" || c.Flatness != -1 {
			return bad("is " + string(c.State) + " with measures")
		}
	default:
		return bad("has the state " + string(c.State))
	}
	return nil
}
