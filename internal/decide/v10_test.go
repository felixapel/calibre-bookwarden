package decide_test

import (
	"slices"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/decide"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

// Decision version 10, part 1 (plan v4, C2): a printed author list no longer
// overrides catalogue roots it is inconsistent with, a text-alone title or
// author list printed in capitals is not proposed as printed, and a pubdate
// Calibre would shift on write is not proposed. Every case shows v9 beside v10.

// v10Outcome is what one version decides for the case's field: the proposed
// values and roots, nil for no proposal, and whether text_roots_disagree is
// among the reasons.
type v10Outcome struct {
	proposed, roots []string
	disagree        bool
}

func authorsOf(names ...string) evidence.AgreedFact {
	vals := make([]evidence.AgreedValue, len(names))
	for i, n := range names {
		vals[i] = person(n, "author")
	}
	return agreedFact("contributor", vals...)
}

// textOnly leaves the text as the only root: no lookups, no file title or
// authors.
func textOnly(p *evidence.Package) {
	p.Lookups = nil
	p.Formats[0].Metadata.Title, p.Formats[0].Metadata.Authors = "", nil
}

func TestDecisionV10(t *testing.T) {
	const junkTitle = "scan_20190101_123456.pdf"
	for _, tt := range []struct {
		name    string
		p       evidence.Package
		field   string
		v9, v10 v10Outcome
	}{
		{name: "authors: the text's list puts others before the roots' only author",
			p:     v8Book(baseFacts().with(authorsOf("Bob B", "Cara C", "Dan D", printedMan)), func(p *evidence.Package) { p.Calibre.Authors = nil }),
			field: "authors",
			v9:    v10Outcome{proposed: []string{"Bob B", "Cara C", "Dan D", printedMan}, roots: []string{"text"}},
			v10:   v10Outcome{disagree: true}},
		{name: "authors: the text adds a second author after the roots' first (Q14)",
			p:     v8Book(baseFacts().with(authorsOf(printedMan, "Bob B")), func(p *evidence.Package) { p.Calibre.Authors = nil }),
			field: "authors",
			v9:    v10Outcome{proposed: []string{printedMan, "Bob B"}, roots: []string{"text"}},
			v10:   v10Outcome{proposed: []string{printedMan, "Bob B"}, roots: []string{"text"}}},
		{name: "title: printed in capitals, no other root",
			p: v8Book(baseFacts().with(agreedFact("title", av("THE PRINTED TITLE"))), func(p *evidence.Package) {
				textOnly(p)
				p.Calibre.Title = junkTitle
			}),
			field: "title",
			v9:    v10Outcome{proposed: []string{"THE PRINTED TITLE"}, roots: []string{"text"}},
			v10:   v10Outcome{}},
		{name: "title: printed in capitals, the file's spelling",
			p: v8Book(baseFacts().with(agreedFact("title", av("THE PRINTED TITLE"))), func(p *evidence.Package) {
				textOnly(p)
				p.Calibre.Title, p.Formats[0].Metadata.Title = junkTitle, "The Printed Title"
			}),
			field: "title",
			v9:    v10Outcome{proposed: []string{"The Printed Title"}, roots: []string{"file", "text"}},
			v10:   v10Outcome{proposed: []string{"The Printed Title"}, roots: []string{"file", "text"}}},
		// A placeholder in the DNB record no longer speaks about authors
		// (rule 4: it doesn't cover the file's), so the file and the text
		// propose together, with the file's spelling.
		{name: "authors: printed in capitals, the file's spelling",
			p: v8Book(baseFacts().with(authorsOf("ANN AUTHOR")), func(p *evidence.Package) {
				p.Calibre.Authors = []string{"Unknown"}
				p.Lookups[0].Results[0].Records[0].Authors = []string{"Unknown"}
			}),
			field: "authors",
			v9:    v10Outcome{proposed: []string{"ANN AUTHOR"}, roots: []string{"text"}},
			v10:   v10Outcome{proposed: []string{printedMan}, roots: []string{"file", "text"}}},
		// A record's placeholder publisher vetoes the main path, so the text
		// stands alone; the file states the same key and lends its spelling,
		// unless that is in capitals too.
		{name: "publisher: printed in capitals, text alone, the file's spelling",
			p: v9Imprint("MCGRAW-HILL EDUCATION", "McGraw-Hill Education", "Unknown"), field: "publisher",
			v9:  v10Outcome{proposed: []string{"McGraw-Hill Education"}, roots: []string{"text"}},
			v10: v10Outcome{proposed: []string{"McGraw-Hill Education"}, roots: []string{"text"}}},
		{name: "publisher: printed in capitals, text alone, the file's spelling in capitals too",
			p: v9Imprint("MCGRAW-HILL EDUCATION", "MCGRAW-HILL EDUCATION", "Unknown"), field: "publisher",
			v9:  v10Outcome{proposed: []string{"MCGRAW-HILL EDUCATION"}, roots: []string{"text"}},
			v10: v10Outcome{}},
		{name: "title: digits only are not capitals",
			p: v8Book(baseFacts().with(agreedFact("title", av("1984"))), func(p *evidence.Package) {
				textOnly(p)
				p.Calibre.Title = junkTitle
			}),
			field: "title",
			v9:    v10Outcome{proposed: []string{"1984"}, roots: []string{"text"}},
			v10:   v10Outcome{proposed: []string{"1984"}, roots: []string{"text"}}},
		{name: "title: a script without case is not capitals",
			p: v8Book(baseFacts().with(agreedFact("title", av("ノルウェイの森"))), func(p *evidence.Package) {
				textOnly(p)
				p.Calibre.Title = junkTitle
			}),
			field: "title",
			v9:    v10Outcome{proposed: []string{"ノルウェイの森"}, roots: []string{"text"}},
			v10:   v10Outcome{proposed: []string{"ノルウェイの森"}, roots: []string{"text"}}},
		{name: "authors: two extra printed names after the roots' first author",
			p:     v8Book(baseFacts().with(authorsOf(printedMan, "Bob B", "Cara C")), func(p *evidence.Package) { p.Calibre.Authors = nil }),
			field: "authors",
			v9:    v10Outcome{proposed: []string{printedMan, "Bob B", "Cara C"}, roots: []string{"text"}},
			v10:   v10Outcome{proposed: []string{printedMan, "Bob B", "Cara C"}, roots: []string{"text"}}},
		{name: "authors: three extra printed names are not the same people",
			p:     v8Book(baseFacts().with(authorsOf(printedMan, "Bob B", "Cara C", "Dan D")), func(p *evidence.Package) { p.Calibre.Authors = nil }),
			field: "authors",
			v9:    v10Outcome{proposed: []string{printedMan, "Bob B", "Cara C", "Dan D"}, roots: []string{"text"}},
			v10:   v10Outcome{disagree: true}},
		// Calibre's authors are right already: the inconsistent printed list
		// is flagged, nothing is proposed in either version.
		{name: "authors: Calibre agrees with the roots, the text puts another first",
			p:     v8Book(baseFacts().with(authorsOf("Bob B", printedMan)), nil),
			field: "authors",
			v9:    v10Outcome{},
			v10:   v10Outcome{disagree: true}},
		{name: "authors: printed in capitals over a placeholder, no other root",
			p: v8Book(baseFacts().with(authorsOf("ANN AUTHOR")), func(p *evidence.Package) {
				textOnly(p)
				p.Calibre.Authors = []string{"Unknown"}
			}),
			field: "authors",
			v9:    v10Outcome{proposed: []string{"ANN AUTHOR"}, roots: []string{"text"}},
			v10:   v10Outcome{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for version, want := range map[int]v10Outcome{9: tt.v9, 10: tt.v10} {
				d, err := decide.AtVersion(t.Context(), tt.p, version)
				if err != nil {
					t.Fatalf("v%d: %v", version, err)
				}
				p := proposalOf(d, tt.field)
				switch {
				case want.proposed == nil && p != nil:
					t.Errorf("v%d: proposes %+v, want no %s proposal", version, *p, tt.field)
				case want.proposed != nil && (p == nil || !slices.Equal(p.Proposed, want.proposed) || !slices.Equal(p.Roots, want.roots)):
					t.Errorf("v%d: %s proposal %+v, want %q from %v", version, tt.field, p, want.proposed, want.roots)
				}
				if got := slices.Contains(d.Reasons, "text_roots_disagree"); got != want.disagree {
					t.Errorf("v%d: text_roots_disagree = %v, want %v (reasons %v)", version, got, want.disagree, d.Reasons)
				}
			}
		})
	}
}

// Calibre moves the 1st of a month and the last day of a month other than
// December when it stores a date, so v10 proposes no such pubdate.
func TestDecisionV10UnwritablePubdate(t *testing.T) {
	const other = "2010-05-05T12:00:00+00:00"
	for _, tt := range []struct {
		name, calibre, date string
		v10Fill             bool
	}{
		{"the 1st", other, "2005-01-01", false},
		{"the last day of June", other, "2005-06-30", false},
		{"a leap day, the last of February", other, "2004-02-29", false},
		{"mid-month", other, "2005-06-15", true},
		{"31 December is not shifted", other, "2005-12-31", true},
		// The refusal production saw: Calibre holds the day it shifted to.
		{"over Calibre's own shifted day", "2005-06-29T00:00:00+00:00", "2005-06-30", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := v9Dated(tt.calibre, tt.date)
			for version, want := range map[int]bool{9: true, 10: tt.v10Fill} {
				d, err := decide.AtVersion(t.Context(), p, version)
				if err != nil {
					t.Fatal(err)
				}
				got := proposalOf(d, "pubdate")
				switch {
				case want && (got == nil || !slices.Equal(got.Proposed, []string{tt.date})):
					t.Errorf("v%d: pubdate proposal %+v, want %s", version, got, tt.date)
				case !want && got != nil:
					t.Errorf("v%d: proposes %+v, want no pubdate proposal", version, *got)
				}
			}
		})
	}
}

// Version 10 still derives as it did once version 11 is current.
func TestDecisionV10StillDerives(t *testing.T) {
	p := v9Imprint("MIT PRESS", "MIT Press", "")
	at, err := decide.AtVersion(t.Context(), p, 10)
	if err != nil {
		t.Fatal(err)
	}
	if b := proposalOf(at, "publisher"); b == nil || !slices.Equal(b.Proposed, []string{"MIT Press"}) {
		t.Fatalf("AtVersion(10) publisher %+v", b)
	}
}

// v10Translated is a book whose EPUB is in English but titled with the
// original's title, as is an Open Library record in recordLangs; Calibre
// has the translated title.
func v10Translated(recordLangs []string) evidence.Package {
	file := epub("Der Originaltitel", []string{A}, []string{"en"}, []string{I})
	p := book([]evidence.FormatEvidence{file}, lookup(I, []string{evidence.SeedFile},
		ok(evidence.SourceOpenLibrary, rec("/books/OL1M", "Der Originaltitel", []string{A}, recordLangs, I))))
	p.Calibre.Title = "The Translated Title"
	return p
}

// Decision version 10, part 2 (plan v4, C3): a catalogue record speaks about
// the title and authors only when it is the same book in the same language,
// three subtitle defects, and publishers printed in capitals.
func TestDecisionV10Part2(t *testing.T) {
	const junkTitle = "scan_20190101_123456.pdf"
	textTitle := func(title, subtitle string) evidence.Package {
		facts := factSet{"title": agreedFact("title", av(title))}
		if subtitle != "" {
			facts["subtitle"] = agreedFact("subtitle", av(subtitle))
		}
		return v8Book(facts, func(p *evidence.Package) {
			textOnly(p)
			p.Calibre.Title = junkTitle
		})
	}
	for _, tt := range []struct {
		name    string
		p       evidence.Package
		field   string
		v9, v10 v10Outcome
	}{
		{name: "rule 4: a record in another language doesn't back the file's title",
			p: v10Translated([]string{"deu"}), field: "title",
			v9:  v10Outcome{proposed: []string{"Der Originaltitel"}, roots: []string{"file", "openlibrary"}},
			v10: v10Outcome{}},
		{name: "rule 4: a record in the files' language does",
			p: v10Translated([]string{"eng"}), field: "title",
			v9:  v10Outcome{proposed: []string{"Der Originaltitel"}, roots: []string{"file", "openlibrary"}},
			v10: v10Outcome{proposed: []string{"Der Originaltitel"}, roots: []string{"file", "openlibrary"}}},
		{name: "rule 4: a record without a language behaves as before",
			p: v10Translated(nil), field: "title",
			v9:  v10Outcome{proposed: []string{"Der Originaltitel"}, roots: []string{"file", "openlibrary"}},
			v10: v10Outcome{proposed: []string{"Der Originaltitel"}, roots: []string{"file", "openlibrary"}}},
		// Open Library is no authority, so it never vetoed the fill.
		{name: "rule 4: the text and the file fill the title beside an Open Library record in another language",
			p: func() evidence.Package {
				p := v10Translated([]string{"deu"})
				p.Calibre.Title, p.Formats[0].Metadata.Title = "", "The Translated Title"
				factSet{"title": agreedFact("title", av("The Translated Title"))}.stamp(&p)
				return p
			}(), field: "title",
			v9:  v10Outcome{proposed: []string{"The Translated Title"}, roots: []string{"file", "text"}},
			v10: v10Outcome{proposed: []string{"The Translated Title"}, roots: []string{"file", "text"}}},
		{name: "rule 4: a DNB record in another language doesn't dissent from the text",
			p: func() evidence.Package {
				p := v10Translated([]string{"deu"})
				p.Lookups[0].Results[0] = ok(evidence.SourceDNB, rec("958577617", "Der Originaltitel", []string{A}, []string{"deu"}, I))
				p.Calibre.Title, p.Formats[0].Metadata.Title = "", "The Translated Title"
				factSet{"title": agreedFact("title", av("The Translated Title"))}.stamp(&p)
				return p
			}(), field: "title",
			v9:  v10Outcome{disagree: true},
			v10: v10Outcome{proposed: []string{"The Translated Title"}, roots: []string{"file", "text"}}},
		{name: "rule 5: a record's subtitle in its own field backs the text's joined title",
			p: v8Book(baseFacts().with(agreedFact("title", av("Main: Sub"))), func(p *evidence.Package) {
				p.Calibre.Title, p.Formats[0].Metadata.Title = "Old Title", "Main: Sub"
				r := &p.Lookups[0].Results[0].Records[0]
				r.Title, r.Subtitle = "Main", "Sub"
			}), field: "title",
			v9:  v10Outcome{},
			v10: v10Outcome{proposed: []string{"Main: Sub"}, roots: []string{"dnb", "file", "text"}}},
		// Review of #155: a record's joined title follows the text's join.
		{name: "rule 5: a record's subtitle after a question mark joins as the text's does",
			p: v8Book(baseFacts().with(agreedFact("title", av("Who?")), agreedFact("subtitle", av("Sub"))), func(p *evidence.Package) {
				p.Calibre.Title, p.Formats[0].Metadata.Title = "Old Title", "Who? Sub"
				r := &p.Lookups[0].Results[0].Records[0]
				r.Title, r.Subtitle = "Who?", "Sub"
			}), field: "title",
			v9:  v10Outcome{},
			v10: v10Outcome{proposed: []string{"Who? Sub"}, roots: []string{"dnb", "file", "text"}}},
		{name: "rule 5: a title in capitals takes a record's joined spelling",
			p: v8Book(baseFacts().with(agreedFact("title", av("MAIN")), agreedFact("subtitle", av("SUB"))), func(p *evidence.Package) {
				p.Calibre.Title, p.Formats[0].Metadata.Title = junkTitle, ""
				r := &p.Lookups[0].Results[0].Records[0]
				r.Title, r.Subtitle = "Main", "Sub"
			}), field: "title",
			v9:  v10Outcome{proposed: []string{"MAIN: SUB"}, roots: []string{"text"}},
			v10: v10Outcome{proposed: []string{"Main: Sub"}, roots: []string{"dnb", "text"}}},
		// Review of #155: the dissent half of rule 4. The file's junk title is
		// still a key, so the two-root path is vetoed by nothing and the
		// text stands alone; the German record no longer dissents.
		{name: "rule 4: a record in another language doesn't dissent from a text-alone title",
			p: func() evidence.Package {
				p := v10Translated([]string{"deu"})
				p.Lookups[0].Results[0] = ok(evidence.SourceDNB, rec("958577617", "Der Originaltitel", []string{A}, []string{"deu"}, I))
				p.Calibre.Title, p.Formats[0].Metadata.Title = junkTitle, junkTitle
				factSet{"title": agreedFact("title", av("The Translated Title"))}.stamp(&p)
				return p
			}(), field: "title",
			v9:  v10Outcome{disagree: true},
			v10: v10Outcome{proposed: []string{"The Translated Title"}, roots: []string{"text"}}},
		// Rule 4 compares with the files' languages and authors even when
		// the files state no title.
		{name: "rule 4: a DNB record in another language doesn't dissent when the file has no title",
			p: func() evidence.Package {
				p := v10Translated([]string{"deu"})
				p.Lookups[0].Results[0] = ok(evidence.SourceDNB, rec("958577617", "Der Originaltitel", []string{A}, []string{"deu"}, I))
				p.Calibre.Title, p.Formats[0].Metadata.Title = junkTitle, ""
				factSet{"title": agreedFact("title", av("The Translated Title"))}.stamp(&p)
				return p
			}(), field: "title",
			v9:  v10Outcome{disagree: true},
			v10: v10Outcome{proposed: []string{"The Translated Title"}, roots: []string{"text"}}},
		{name: "rule 5: without a text title, words are never dropped from the middle",
			p: func() evidence.Package {
				p := book([]evidence.FormatEvidence{epub("Dune: Messiah", []string{A}, []string{"de"}, []string{I})},
					lookup(I, []string{evidence.SeedFile}, ok(evidence.SourceDNB, rec("958577617", "Dune: Messiah", []string{A}, []string{"deu"}, I))))
				p.Calibre.Title = "Dune, Book Two: Messiah"
				return p
			}(), field: "title",
			v9:  v10Outcome{proposed: []string{"Dune: Messiah"}, roots: []string{"dnb", "file"}},
			v10: v10Outcome{}},
		{name: "rule 5: a title ending in ? joins its subtitle with a space",
			p: textTitle("Who?", "Sub"), field: "title",
			v9:  v10Outcome{proposed: []string{"Who?: Sub"}, roots: []string{"text"}},
			v10: v10Outcome{proposed: []string{"Who? Sub"}, roots: []string{"text"}}},
		{name: "rule 5: a title ending in : joins its subtitle with a space",
			p: textTitle("Title:", "Sub"), field: "title",
			v9:  v10Outcome{proposed: []string{"Title:: Sub"}, roots: []string{"text"}},
			v10: v10Outcome{proposed: []string{"Title: Sub"}, roots: []string{"text"}}},
		{name: "rule 5: other titles join with a colon",
			p: textTitle("Main", "Sub"), field: "title",
			v9:  v10Outcome{proposed: []string{"Main: Sub"}, roots: []string{"text"}},
			v10: v10Outcome{proposed: []string{"Main: Sub"}, roots: []string{"text"}}},
		{name: "rule 6: a publisher in capitals, no other spelling",
			p: v9Imprint("MIT PRESS", "", ""), field: "publisher",
			v9:  v10Outcome{proposed: []string{"Mit Press"}, roots: []string{"text"}},
			v10: v10Outcome{}},
		{name: "rule 6: a publisher in capitals, the file's spelling",
			p: v9Imprint("MIT PRESS", "MIT Press", ""), field: "publisher",
			v9:  v10Outcome{proposed: []string{"MIT Press"}, roots: []string{"file", "text"}},
			v10: v10Outcome{proposed: []string{"MIT Press"}, roots: []string{"file", "text"}}},
		{name: "rule 6: a publisher in mixed case",
			p: v9Imprint("Penguin Books", "", ""), field: "publisher",
			v9:  v10Outcome{proposed: []string{"Penguin Books"}, roots: []string{"text"}},
			v10: v10Outcome{proposed: []string{"Penguin Books"}, roots: []string{"text"}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for version, want := range map[int]v10Outcome{9: tt.v9, 10: tt.v10} {
				d, err := decide.AtVersion(t.Context(), tt.p, version)
				if err != nil {
					t.Fatalf("v%d: %v", version, err)
				}
				p := proposalOf(d, tt.field)
				switch {
				case want.proposed == nil && p != nil:
					t.Errorf("v%d: proposes %+v, want no %s proposal", version, *p, tt.field)
				case want.proposed != nil && (p == nil || !slices.Equal(p.Proposed, want.proposed) || !slices.Equal(p.Roots, want.roots)):
					t.Errorf("v%d: %s proposal %+v, want %q from %v", version, tt.field, p, want.proposed, want.roots)
				}
				if got := slices.Contains(d.Reasons, "text_roots_disagree"); got != want.disagree {
					t.Errorf("v%d: text_roots_disagree = %v, want %v (reasons %v)", version, got, want.disagree, d.Reasons)
				}
			}
		})
	}
}
