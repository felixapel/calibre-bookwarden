package decide

import (
	"context"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

// SetSharedRoot replaces the Open Library root-sharing rule for one test.
func SetSharedRoot(f func(evidence.SourceRecord) string) func() {
	old := sharedRoot
	sharedRoot = f
	return func() { sharedRoot = old }
}

// PublisherEquivalent exposes the publisher equivalence of ADR-036 3.1.
func PublisherEquivalent(a, b string) bool { return publisherEquivalent(a, b) }

// ProposalsAtV6Ungated returns the proposals version 6's field rules build
// for p, whatever its tier: version 6 withholds them from a Tier C book, but
// builds them the same way.
func ProposalsAtV6Ungated(ctx context.Context, p evidence.Package) ([]evidence.Proposal, error) {
	v, err := newView(ctx, p, 6)
	if err != nil {
		return nil, err
	}
	return v.proposals(), nil
}

// CalibreEmptyOrJunk reports whether Calibre's value for field is one the
// text may fill or replace alone (ADR-036 section 3.3).
func CalibreEmptyOrJunk(p evidence.Package, field string) bool {
	return (&view{p: p}).calibreEmptyOrJunk(field)
}
