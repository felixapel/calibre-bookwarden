package decide_test

import (
	"testing"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// The keys every decision version derives fold case and normalize with these
// Unicode tables: the stdlib's (unicode) and golang.org/x/text's (norm; cases
// ships the same tables in the same module). A change here can change keys in
// sealed packages, so a seal check could fail or a replay differ. Regenerate
// nothing: read ADR-038 first, then decide whether the new tables need a new
// decision version.
const (
	pinnedUnicodeVersion = "17.0.0"
	pinnedNormVersion    = "17.0.0"
)

func TestUnicodeTablesArePinned(t *testing.T) {
	if unicode.Version != pinnedUnicodeVersion {
		t.Errorf("unicode.Version = %s, pinned %s: read ADR-038 before moving the pin", unicode.Version, pinnedUnicodeVersion)
	}
	if norm.Version != pinnedNormVersion {
		t.Errorf("norm.Version = %s, pinned %s: read ADR-038 before moving the pin", norm.Version, pinnedNormVersion)
	}
}
