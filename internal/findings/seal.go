package findings

import (
	"context"
	"fmt"
	"slices"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

// Seal derives findings before sealing a new package.
func Seal(ctx context.Context, p evidence.Package) ([]byte, string, error) {
	fs, err := Derive(ctx, p)
	if err != nil {
		return nil, "", fmt.Errorf("findings: seal: %w", err)
	}
	p.FindingsVersion = Version
	p.Findings = fs
	return evidence.Seal(p)
}

// Open verifies the original seal and recomputes the versioned observations.
func Open(ctx context.Context, raw []byte, hash string) (evidence.Package, error) {
	if err := ctx.Err(); err != nil {
		return evidence.Package{}, err
	}
	p, err := evidence.Open(raw, hash)
	if err != nil {
		return evidence.Package{}, err
	}
	switch p.FindingsVersion {
	case 0:
		if len(p.Findings) != 0 {
			return evidence.Package{}, ErrInvalidEvidence
		}
	case 1, 2, 3, 4, 5, Version:
	default:
		return evidence.Package{}, ErrUnknownVersion
	}
	version := p.FindingsVersion
	if version == 0 {
		version = Version
	}
	fs, err := DeriveVersion(ctx, p, version)
	if err != nil {
		return evidence.Package{}, fmt.Errorf("findings: open: %w", err)
	}
	if p.FindingsVersion != 0 && !slices.Equal(p.Findings, fs) {
		return evidence.Package{}, ErrMismatch
	}
	p.Findings = fs
	return p, nil
}
