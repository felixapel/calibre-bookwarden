package eval

import (
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/extract"
	"github.com/felixapel/calibre-bookwarden/internal/extraction"
)

// The refusal table counts model values only, not the parsers' claims.
func TestRefusalsCountModelValuesOnly(t *testing.T) {
	r := newReverifyReport(1)
	refused := func(field, source, reason string) extract.Verified {
		return extract.Verified{Claim: extract.Claim{Field: field, Source: source}, Status: extract.StatusValueNotDerived, Reason: reason}
	}
	r.add(1, evidence.Extraction{}, extraction.Rederived{Verified: []extract.Verified{
		refused(extract.FieldISBN, extract.SourceParser, extract.RefusedOther),
		refused(extract.FieldTitle, "luna", extract.RefusedDash),
	}})
	if len(r.Refusals) != 1 || r.Refusals[extract.RefusedDash]["title"] != (RefusalCount{Values: 1, Books: 1}) {
		t.Fatalf("refusals = %+v", r.Refusals)
	}
}
