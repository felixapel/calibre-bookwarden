package fix

import (
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/decide"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

// Decision v11 proposes only what an undo can restore (ADR-048), with its
// own frozen copy of this package's restore rule. The two must agree: a
// proposal v11 makes is never refused as calibre_cannot_restore, and one it
// drops always would be.
func TestDecideV11RestoreRuleMatchesFix(t *testing.T) {
	str := func(s string) *string { return &s }
	good := evidence.Record{Title: "The Road", TitleSort: str("Road, The"), Authors: []string{"Cormac McCarthy"}, AuthorSort: "McCarthy, Cormac",
		Languages: []string{"eng"}, Identifiers: map[string]string{"isbn": "9780307387899"}, Publisher: "Vintage", PubDate: "2007-03-14T12:00:00+00:00"}
	records := map[string]func(*evidence.Record){
		"clean":                       func(*evidence.Record) {},
		"title double space":          func(r *evidence.Record) { r.Title = "The  Road" },
		"title edge space":            func(r *evidence.Record) { r.Title = "The Road " },
		"title control":               func(r *evidence.Record) { r.Title = "The\u0007Road" },
		"title sort double space":     func(r *evidence.Record) { r.TitleSort = str("Road,  The") },
		"title sort empty":            func(r *evidence.Record) { r.TitleSort = str("") },
		"title sort not captured":     func(r *evidence.Record) { r.TitleSort = nil },
		"no authors":                  func(r *evidence.Record) { r.Authors = nil },
		"author with ampersand":       func(r *evidence.Record) { r.Authors = []string{"A & B"} },
		"authors differ in case":      func(r *evidence.Record) { r.Authors = []string{"Ann Lee", "ann lee"} },
		"author sort double space":    func(r *evidence.Record) { r.AuthorSort = "McCarthy,  Cormac" },
		"author sort empty":           func(r *evidence.Record) { r.AuthorSort = "" },
		"publisher edge space":        func(r *evidence.Record) { r.Publisher = " Vintage" },
		"publisher empty":             func(r *evidence.Record) { r.Publisher = "" },
		"language und":                func(r *evidence.Record) { r.Languages = []string{"und"} },
		"language twice":              func(r *evidence.Record) { r.Languages = []string{"eng", "eng"} },
		"no languages":                func(r *evidence.Record) { r.Languages = nil },
		"identifier with comma":       func(r *evidence.Record) { r.Identifiers = map[string]string{"isbn": "9780307387899", "x": "a,b"} },
		"identifier key upper case":   func(r *evidence.Record) { r.Identifiers = map[string]string{"ISBN": "9780307387899"} },
		"no identifiers":              func(r *evidence.Record) { r.Identifiers = nil },
		"pubdate the 1st":             func(r *evidence.Record) { r.PubDate = "2007-01-01T00:00:00+00:00" },
		"pubdate shifted to the 31st": func(r *evidence.Record) { r.PubDate = "1991-07-31T22:00:00+00:00" },
		"pubdate 31 December":         func(r *evidence.Record) { r.PubDate = "2004-12-31T12:00:00+00:00" },
		"pubdate last of February":    func(r *evidence.Record) { r.PubDate = "2004-02-29T12:00:00+00:00" },
		"pubdate year 101":            func(r *evidence.Record) { r.PubDate = "0101-03-14T12:00:00+00:00" },
		"pubdate garbled":             func(r *evidence.Record) { r.PubDate = "March 2007" },
		"no pubdate":                  func(r *evidence.Record) { r.PubDate = "" },
		"pubdate date only, the 1st":  func(r *evidence.Record) { r.PubDate = "2007-03-01" },
		"pubdate date only, mid":      func(r *evidence.Record) { r.PubDate = "2007-03-14" },
		"pubdate year 999":            func(r *evidence.Record) { r.PubDate = "0999-03-14T12:00:00+00:00" },
		"pubdate year 1000":           func(r *evidence.Record) { r.PubDate = "1000-03-14T12:00:00+00:00" },
		"isbn with edge space":        func(r *evidence.Record) { r.Identifiers = map[string]string{"isbn": " 9780307387899"} },
		"isbn empty":                  func(r *evidence.Record) { r.Identifiers = map[string]string{"isbn": ""} },
		"isbn with comma":             func(r *evidence.Record) { r.Identifiers = map[string]string{"isbn": "a,b"} },
		"language two letters":        func(r *evidence.Record) { r.Languages = []string{"de"} },
		"language upper case":         func(r *evidence.Record) { r.Languages = []string{"ENG"} },
		"author with bar":             func(r *evidence.Record) { r.Authors = []string{"A | B"} },
		"author empty name":           func(r *evidence.Record) { r.Authors = []string{"Ann Lee", ""} },
		"title sort control":          func(r *evidence.Record) { r.TitleSort = str("Road,\u0007The") },
		"publisher control":           func(r *evidence.Record) { r.Publisher = "Vin\u0007tage" },
		"author sort control":         func(r *evidence.Record) { r.AuthorSort = "McCarthy,\u0007Cormac" },
	}
	// A value Calibre stores as it is, so only the restore can refuse.
	proposals := []evidence.Proposal{
		{Field: "title", Proposed: []string{"The Crossing"}},
		{Field: "title_sort", Proposed: []string{"Crossing, The"}},
		{Field: "authors", Proposed: []string{"Cormac McCarthy", "Ann Lee"}},
		{Field: "author_sort", Proposed: []string{"McCarthy, Cormac & Lee, Ann"}},
		{Field: "publisher", Proposed: []string{"Picador"}},
		{Field: "languages", Proposed: []string{"spa"}},
		{Field: "isbn", Proposed: []string{"9780679760849"}},
		{Field: "pubdate", Proposed: []string{"1994-06-14"}},
	}
	for name, mutate := range records {
		rec := good
		rec.Identifiers = map[string]string{"isbn": "9780307387899"}
		mutate(&rec)
		for _, p := range proposals {
			c, err := proposalChange(p, rec)
			if err != nil {
				t.Fatal(err)
			}
			got := unstorable(c, rec)
			if got == "calibre_alters_value" {
				continue // the new value itself, not the restore
			}
			if fixOK, v11OK := got == "", decide.Restorable(p.Field, rec); fixOK != v11OK {
				t.Errorf("%s, %s: fix says %q, decide v11 restorable = %v", name, p.Field, got, v11OK)
			}
		}
	}
}
