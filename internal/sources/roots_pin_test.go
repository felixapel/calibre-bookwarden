package sources_test

import (
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/decide"
	"github.com/felixapel/calibre-bookwarden/internal/sources"
)

// TestSharedRootTablePinned: sealed decisions group Open Library records by
// this table. Decision versions 1 to 5 were all sealed with it empty;
// filling it regroups roots in stored decisions, so it needs decide.Version 6
// and a decide.AtVersion that keeps the empty table for 1 to 5, or every
// stored run fails its integrity check.
func TestSharedRootTablePinned(t *testing.T) {
	n := 0
	for _, prefixes := range sources.NationalPrefixes() {
		n += len(prefixes)
	}
	if decide.Version <= 5 && n != 0 {
		t.Fatalf("the root-sharing table changed under decide.Version %d: %v", decide.Version, sources.NationalPrefixes())
	}
}
