package decide

import (
	"context"
	"fmt"
	"slices"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/findings"
)

// Seal derives the findings and the decision, then seals the package.
func Seal(ctx context.Context, p evidence.Package) ([]byte, string, error) {
	fs, err := findings.Derive(ctx, p)
	if err != nil {
		return nil, "", fmt.Errorf("decide: seal: %w", err)
	}
	p.FindingsVersion, p.Findings = findings.Version, fs
	d, err := Decide(ctx, p)
	if err != nil {
		return nil, "", fmt.Errorf("decide: seal: %w", err)
	}
	p.DecisionVersion, p.Tier, p.TierReasons, p.Proposals = Version, d.Tier, d.Reasons, d.Proposals
	return evidence.Seal(p)
}

// Open verifies the seal and the findings, then recomputes the decision under
// the rules of the version it was sealed with, which it must match exactly; a
// package sealed before M3.2 gets the current decision derived in memory. A
// stored tier is never trusted.
func Open(ctx context.Context, raw []byte, seal string) (evidence.Package, Decision, error) {
	p, err := findings.Open(ctx, raw, seal)
	if err != nil {
		return evidence.Package{}, Decision{}, err
	}
	switch {
	case p.DecisionVersion == 0:
		if p.Tier != "" || len(p.TierReasons) != 0 || len(p.Proposals) != 0 {
			return evidence.Package{}, Decision{}, fmt.Errorf("decide: open: %w", findings.ErrInvalidEvidence)
		}
	case p.DecisionVersion < 0 || p.DecisionVersion > Version:
		return evidence.Package{}, Decision{}, ErrUnknownVersion
	}
	version := p.DecisionVersion
	if version == 0 {
		version = Version
	}
	d, err := AtVersion(ctx, p, version)
	if err != nil {
		return evidence.Package{}, Decision{}, fmt.Errorf("decide: open: %w", err)
	}
	if p.DecisionVersion != 0 && !sameDecision(p, d) {
		return evidence.Package{}, Decision{}, ErrMismatch
	}
	return p, d, nil
}

func sameDecision(p evidence.Package, d Decision) bool {
	return p.Tier == d.Tier && slices.Equal(p.TierReasons, d.Reasons) &&
		slices.EqualFunc(p.Proposals, d.Proposals, func(a, b evidence.Proposal) bool {
			return a.Field == b.Field && slices.Equal(a.Current, b.Current) && slices.Equal(a.Proposed, b.Proposed) && slices.Equal(a.Roots, b.Roots)
		})
}
