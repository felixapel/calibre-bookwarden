package sources_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/sources"
)

// sruPage is an SRU answer claiming total matches but carrying one record per
// entry of isbns (the page the server returned).
func sruPage(total int, isbns ...string) []byte {
	var recs strings.Builder
	for i, isbn := range isbns {
		fmt.Fprintf(&recs, `<record><recordData><record xmlns="http://www.loc.gov/MARC21/slim"><leader>00000nam a2200000 c 4500</leader>`+
			`<controlfield tag="001">%d</controlfield><controlfield tag="008">%s</controlfield>%s%s</record></recordData></record>`,
			i+1, date008, df("020", " ", " ", "a", isbn), df("245", "1", "0", "a", "Other"))
	}
	return fmt.Appendf(nil, `<?xml version="1.0"?><searchRetrieveResponse xmlns="http://www.loc.gov/zing/srw/"><version>1.1</version>`+
		`<numberOfRecords>%d</numberOfRecords><records>%s</records></searchRetrieveResponse>`, total, recs.String())
}

// A page shorter than numberOfRecords may hide the matching record, or
// another record for the same ISBN that conflicts with the one returned, so
// any truncated page is unavailable: never no_match, never a partial ok.
func TestSRUTruncatedAnswer(t *testing.T) {
	const other = "9780306406157"
	for _, tc := range []struct {
		name string
		body []byte
		want string
	}{
		{"truncated, none accepted", sruPage(42, other, other), evidence.SourceUnavailable},
		{"truncated, zero returned", sruPage(42), evidence.SourceUnavailable},
		{"truncated, one accepted", sruPage(42, other, isbnOK), evidence.SourceUnavailable},
		{"complete, one accepted", sruPage(2, other, isbnOK), evidence.SourceOK},
		{"complete, none accepted", sruPage(2, other, other), evidence.SourceNoMatch},
		{"complete, none at all", sruPage(0), evidence.SourceNoMatch},
	} {
		for _, src := range []struct {
			name string
			a    sources.Adapter
			f    *fakeFetcher
		}{
			{"dnb", sources.DNB(), dnbWith(tc.body)},
			{"bne", sources.BNE(), &fakeFetcher{routes: map[string]answer{"/view/sru/34BNE_INST": {status: 200, body: tc.body}}}},
		} {
			t.Run(src.name+"/"+tc.name, func(t *testing.T) {
				outcome, recs := lookup(t, src.a, src.f, isbnOK)
				if outcome != tc.want || (outcome != evidence.SourceOK && recs != nil) || (outcome == evidence.SourceOK && len(recs) != 1) {
					t.Fatalf("%s %+v, want %s", outcome, recs, tc.want)
				}
			})
		}
	}
}

// The service must not cache a truncated answer as no_match.
func TestSRUTruncatedAnswerIsNotCached(t *testing.T) {
	a := sources.DNB()
	sources.SetBase(a, "https://test.invalid")
	cache := newMemCache()
	s := sources.NewService(dnbWith(sruPage(42, "9780306406157")), cache, a)
	got := s.Lookup(t.Context(), 1, []sources.Seed{{ISBN: isbnOK, Class: "file"}})
	if r := got[0].Results[0]; r.Outcome != evidence.SourceUnavailable || len(cache.puts) != 0 {
		t.Fatalf("outcome %s, puts %v", r.Outcome, cache.puts)
	}
}
