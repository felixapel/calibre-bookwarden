package sources

import (
	"strings"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

// nationalMARCPrefixes names, per national library, the Open Library
// source_records prefixes of its MARC dumps. An Open Library record whose
// source_records all carry one library's prefixes shares that library's root
// (spec section 7). It is empty on purpose: a sample of Open Library editions
// on 2026-10-18 showed MARC imports only from LoC, Harvard, Columbia, Talis
// and Scriblio, none from DNB or BNE (ADR-019). Sealed decisions depend on it:
// adding a prefix needs a new decide.Version and a reader that keeps the empty
// table for versions 1 to 5 (TestSharedRootTablePinned).
var nationalMARCPrefixes = map[string][]string{
	evidence.SourceDNB: {},
	evidence.SourceBNE: {},
}

// SharedRoot returns the national library whose root an Open Library record
// shares, or "".
func SharedRoot(r evidence.SourceRecord) string {
	if len(r.SourceRecords) == 0 {
		return ""
	}
	for library, prefixes := range nationalMARCPrefixes {
		all := len(prefixes) > 0
		for _, sr := range r.SourceRecords {
			if !hasAnyPrefix(sr, prefixes) {
				all = false
				break
			}
		}
		if all {
			return library
		}
	}
	return ""
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}
