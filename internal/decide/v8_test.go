package decide_test

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/decide"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/findings"
)

// v8Case is a book and what decision version 8 derives for it (ADR-036).
type v8Case struct {
	name    string
	p       evidence.Package
	tier    string
	reasons []string
	want    []evidence.Proposal
	// unsealable: the extraction states facts no answering extractor backs,
	// which the seal refuses; the decision still ignores them.
	unsealable bool
}

var trio = []string{"dnb", "file", "text"}

// wantProposal is the base book's proposal for a field, with the given roots
// (the three roots of the base book by default).
func wantProposal(field string, roots ...string) evidence.Proposal {
	if roots == nil {
		roots = trio
	}
	switch field {
	case "title":
		return prop(field, []string{"Old Title"}, []string{printedTitle}, roots)
	case "authors":
		return prop(field, []string{"Old Person"}, []string{printedMan}, roots)
	case "isbn":
		return prop(field, []string{I2}, []string{I}, roots)
	case "publisher":
		return prop(field, []string{}, []string{printedHouse}, roots)
	}
	panic("no such base field " + field)
}

func baseWant(except ...string) []evidence.Proposal {
	var out []evidence.Proposal
	for _, f := range []string{"title", "isbn", "publisher"} {
		if !slices.Contains(except, f) {
			out = append(out, wantProposal(f))
		}
	}
	return out
}

// oldAuthors gives Calibre the printed title and other authors, so that the
// authors are the only field of the base book that can be proposed (a title
// and authors that both differ would be another work).
func oldAuthors(p *evidence.Package) {
	p.Calibre.Title, p.Calibre.Authors = printedTitle, []string{"Old Person"}
}

// authorsWant is the authors proposal of an oldAuthors book, with the other
// base fields.
func authorsWant(roots ...string) []evidence.Proposal {
	return []evidence.Proposal{wantProposal("authors", roots...), wantProposal("isbn"), wantProposal("publisher")}
}

// replaced is want with the proposal of its field swapped for p.
func replaced(want []evidence.Proposal, p evidence.Proposal) []evidence.Proposal {
	out := slices.Clone(want)
	for i := range out {
		if out[i].Field == p.Field {
			out[i] = p
			return out
		}
	}
	panic("field " + p.Field + " not in want")
}

// noLookups removes every source record: no_record, and the file and the text
// are the only roots left.
func noLookups(p *evidence.Package) { p.Lookups = nil }

// alone leaves the text as the only root with a value for the title.
func titleAlone(p *evidence.Package) {
	p.Lookups = nil
	p.Formats[0].Metadata.Title = ""
}

// noISBNs leaves no ISBN anywhere but the text: Calibre and the file have none.
func noISBNs(p *evidence.Package) {
	noLookups(p)
	p.Formats[0].Metadata.ISBNs, p.Calibre.Identifiers = nil, nil
}

func backISBN(isbn string) evidence.AgreedValue {
	return evidence.AgreedValue{Value: isbn, Role: "back"}
}

func secondEPUB(p *evidence.Package, facts factSet) {
	second := p.Formats[0]
	second.Format = "ORIGINAL_EPUB"
	second.Excerpt, second.Extraction = facts.extraction()
	p.Formats = append(p.Formats, second)
}

func v8Cases() []v8Case {
	base := baseFacts()
	titleOnly := factSet{"title": base["title"]}
	return []v8Case{
		// --- the statement and the two-root rule ---
		{name: "all roots agree", p: v8Book(base, nil), tier: "A", want: baseWant()},
		{name: "title and subtitle", p: v8Book(base.with(agreedFact("subtitle", av(printedSub))), func(p *evidence.Package) {
			full := printedTitle + ": " + printedSub
			p.Formats[0].Metadata.Title = full
			p.Lookups[0].Results[0].Records[0].Title = full
		}), tier: "A", want: replaced(baseWant(), prop("title", []string{"Old Title"}, []string{printedTitle + ": " + printedSub}, trio))},
		{name: "file and text", p: v8Book(base, noLookups), tier: "B", reasons: []string{"no_record"},
			want: []evidence.Proposal{wantProposal("publisher", "file", "text")}},
		{name: "record and text", p: v8Book(base, func(p *evidence.Package) {
			p.Formats[0].Metadata.Title, p.Formats[0].Metadata.Authors = "", nil
		}), tier: "B", reasons: []string{"authors_incomplete", "title_incomplete"},
			want: []evidence.Proposal{wantProposal("title", "dnb", "text"), wantProposal("publisher")}},
		{name: "external record and text", p: v8Book(base, func(p *evidence.Package) {
			p.Formats[0].Metadata.Title = ""
			p.Lookups[0].Results = []evidence.SourceResult{ok(evidence.SourceGoogleBooks, rec("g1", printedTitle, []string{printedMan}, []string{"deu"}, I))}
		}), tier: "B", reasons: []string{"title_incomplete"},
			want: []evidence.Proposal{wantProposal("title", "googlebooks", "text"), wantProposal("publisher", "file", "text")}},
		{name: "text disagrees with file and record", p: v8Book(base.with(agreedFact("title", av("Another Printed Title"))), nil), tier: "A",
			reasons: []string{"text_roots_disagree"}, want: baseWant("title")},
		{name: "the text states Calibre's value, records dropped", p: v8Book(base.with(agreedFact("title", av("Old Title"))), nil), tier: "A", want: baseWant("title")},
		{name: "a split file root vetoes", p: v8Book(base, func(p *evidence.Package) {
			second := p.Formats[0]
			second.Format = "ORIGINAL_EPUB"
			second.Metadata = &evidence.FileMetadata{Title: "Printed Title", Authors: []string{printedMan, "Bo Extra"}, Languages: []string{"de"}, ISBNs: []string{I}, Publisher: printedHouse}
			p.Formats = append(p.Formats, second)
		}), tier: "C", reasons: []string{"formats_authors_conflict"}},

		// --- equivalence comes first ---
		{name: "title: ampersand against and", p: v8Book(base.with(agreedFact("title", av("Salt and Pepper"))), func(p *evidence.Package) {
			p.Calibre.Title = "Salt & Pepper"
			p.Formats[0].Metadata.Title = "Salt and Pepper"
			p.Lookups[0].Results[0].Records[0].Title = "Salt and Pepper"
		}), tier: "A", want: baseWant("title")},
		{name: "title: a leading article", p: v8Book(base.with(agreedFact("title", av("Precipice"))), func(p *evidence.Package) {
			p.Calibre.Title, p.Formats[0].Metadata.Title, p.Lookups[0].Results[0].Records[0].Title = "The Precipice", "Precipice", "Precipice"
			p.Calibre.Languages, p.Formats[0].Metadata.Languages = []string{"eng"}, []string{"en"}
			p.Lookups[0].Results[0].Records[0].Languages = []string{"eng"}
		}), tier: "A", want: baseWant("title")},
		{name: "authors: honorific", p: v8Book(base, func(p *evidence.Package) { p.Calibre.Authors = []string{"Dr. " + printedMan} }), tier: "A", want: baseWant()},
		{name: "publisher: corporate suffix", p: v8Book(base, func(p *evidence.Package) { p.Calibre.Publisher = printedHouse + " Verlag" }), tier: "A", want: baseWant("publisher")},
		{name: "isbn: the same ISBN-13", p: v8Book(base, func(p *evidence.Package) { p.Calibre.Identifiers = map[string]string{"isbn": "978-3-551-55167-2"} }), tier: "A", want: baseWant("isbn")},

		// --- disagree vetoes ---
		{name: "title disagree", p: v8Book(base.with(disagreeFact("title")), nil), tier: "A", want: baseWant("title")},
		{name: "subtitle disagree vetoes the title", p: v8Book(base.with(disagreeFact("subtitle")), nil), tier: "A", want: baseWant("title")},
		{name: "authors: all roots agree", p: v8Book(base, oldAuthors), tier: "A", want: authorsWant()},
		{name: "authors disagree", p: v8Book(base.with(disagreeFact("contributor")), oldAuthors), tier: "A", want: baseWant("title")},
		{name: "publisher disagree", p: v8Book(base.with(disagreeFact("imprint")), nil), tier: "A", want: baseWant("publisher")},
		{name: "isbn disagree", p: v8Book(base.with(disagreeFact("isbn")), nil), tier: "A", want: baseWant("isbn")},
		{name: "disagree in one of two formats", p: v8Book(base, func(p *evidence.Package) { secondEPUB(p, base.with(disagreeFact("title"))) }),
			tier: "A", want: baseWant("title")},

		// --- facts that give no statement leave the v7 rules ---
		{name: "single title", p: v8Book(base.with(singleFact("title", av(printedTitle))), nil), tier: "A",
			want: replaced(baseWant(), wantProposal("title", "dnb", "file"))},
		{name: "no facts at all", p: v8Book(factSet{}, nil), tier: "A",
			want: []evidence.Proposal{wantProposal("title", "dnb", "file"),
				wantProposal("isbn", "dnb", "file"), wantProposal("publisher", "dnb", "file")}},
		{name: "only a translator is printed", p: v8Book(base.with(agreedFact("contributor", person("Tom Trans", "translator"))), oldAuthors), tier: "A",
			want: authorsWant("dnb", "file")},
		{name: "translators are not authors", p: v8Book(base.with(agreedFact("contributor", person(printedMan, "author"), person("Tom Trans", "translator"))), oldAuthors),
			tier: "A", want: authorsWant()},
		{name: "two ebook ISBNs", p: v8Book(base.with(isbnFact(labelled(I, "ebook"), labelled(I3, "ebook"))), nil), tier: "A",
			want: replaced(baseWant(), wantProposal("isbn", "dnb", "file"))},
		{name: "two ISBNs, none labelled", p: v8Book(base.with(isbnFact(av(I), av(I3))), nil), tier: "A",
			want: replaced(baseWant(), wantProposal("isbn", "dnb", "file"))},
		{name: "an ISBN that is not valid", p: v8Book(base.with(isbnFact(av("9783551551673"))), nil), tier: "A",
			want: replaced(baseWant(), wantProposal("isbn", "dnb", "file"))},
		{name: "the ebook ISBN among others", p: v8Book(base.with(isbnFact(labelled(I3, "hardcover"), labelled(I, "ebook"))), nil), tier: "A", want: baseWant()},
		{name: "one ISBN, labelled hardcover, never counts", p: v8Book(base.with(isbnFact(labelled(I, "hardcover"))), nil), tier: "A",
			want: replaced(baseWant(), wantProposal("isbn", "dnb", "file"))},
		{name: "print, paperback and PDF ISBNs never count", p: v8Book(base.with(isbnFact(labelled(I, "print"), labelled(I3, "paperback"), labelled(I2, "pdf"))), nil), tier: "A",
			want: replaced(baseWant(), wantProposal("isbn", "dnb", "file"))},
		{name: "an unlabelled ISBN beside a hardcover one", p: v8Book(base.with(isbnFact(labelled(I3, "hardcover"), av(I))), nil), tier: "A", want: baseWant()},
		{name: "two ebook ISBNs and an unlabelled one", p: v8Book(base.with(isbnFact(labelled(I, "ebook"), labelled(I3, "ebook"), av(I2))), nil), tier: "A",
			want: replaced(baseWant(), wantProposal("isbn", "dnb", "file"))},
		{name: "ISBN of the text differs from the file's", p: v8Book(base.with(isbnFact(av(I3))), nil), tier: "A",
			reasons: []string{"text_roots_disagree"}, want: baseWant("isbn")},
		{name: "ISBN the same as Calibre's", p: v8Book(base, func(p *evidence.Package) { p.Calibre.Identifiers = map[string]string{"isbn": I} }), tier: "A", want: baseWant("isbn")},

		// --- the formats ---
		{name: "formats conflict on the title", p: v8Book(base, func(p *evidence.Package) { secondEPUB(p, base.with(agreedFact("title", av("Another Printed Title")))) }),
			tier: "B", reasons: []string{"formats_text_conflict"}, want: baseWant("title")},
		{name: "a format without a block is ignored", p: v8Book(base, func(p *evidence.Package) {
			second := p.Formats[0]
			second.Format = "ORIGINAL_EPUB"
			p.Formats = append(p.Formats, second)
			p.Formats[0].Excerpt, p.Formats[0].Extraction = base.extraction()
			p.Formats[1].Excerpt, p.Formats[1].Extraction = nil, nil
		}), tier: "A", want: baseWant()},
		{name: "a format whose extractors all failed is ignored", p: v8Book(base, func(p *evidence.Package) {
			secondEPUB(p, base.with(agreedFact("title", av("Another Printed Title"))))
			for i := range p.Formats[1].Extraction.Extractors {
				p.Formats[1].Extraction.Extractors[i].State, p.Formats[1].Extraction.Extractors[i].RawSHA256 = evidence.ExtractUnavailable, ""
			}
		}), tier: "A", want: baseWant(), unsealable: true},
		{name: "every extractor failed", p: v8Book(base, func(p *evidence.Package) {
			p.Formats[0].Excerpt, p.Formats[0].Extraction = base.extraction()
			for i := range p.Formats[0].Extraction.Extractors {
				p.Formats[0].Extraction.Extractors[i].State, p.Formats[0].Extraction.Extractors[i].RawSHA256 = evidence.ExtractTimeout, ""
			}
		}), tier: "A", unsealable: true, want: []evidence.Proposal{wantProposal("title", "dnb", "file"),
			wantProposal("isbn", "dnb", "file"), wantProposal("publisher", "dnb", "file")}},
		{name: "two formats agree", p: v8Book(base, func(p *evidence.Package) { secondEPUB(p, base) }), tier: "A", want: baseWant()},

		// --- one root ---
		{name: "junk title, the text alone", p: v8Book(titleOnly, func(p *evidence.Package) { titleAlone(p); p.Calibre.Title = "scan.pdf" }),
			tier: "B", reasons: []string{"no_record", "title_incomplete"},
			want: []evidence.Proposal{prop("title", []string{"scan.pdf"}, []string{printedTitle}, []string{"text"})}},
		{name: "junk title, an untitled name", p: v8Book(titleOnly, func(p *evidence.Package) { titleAlone(p); p.Calibre.Title = "Untitled Document" }),
			tier: "B", reasons: []string{"no_record", "title_incomplete"},
			want: []evidence.Proposal{prop("title", []string{"Untitled Document"}, []string{printedTitle}, []string{"text"})}},
		{name: "junk title, the file dissents", p: v8Book(titleOnly, func(p *evidence.Package) {
			noLookups(p)
			p.Formats[0].Metadata.Title, p.Calibre.Title = "Another Printed Title", "scan.pdf"
		}), tier: "B", reasons: []string{"no_record", "text_roots_disagree"}},
		{name: "junk title, the file has the same junk", p: v8Book(titleOnly, func(p *evidence.Package) {
			noLookups(p)
			p.Formats[0].Metadata.Title, p.Calibre.Title = "scan.pdf", "scan.pdf"
		}), tier: "B", reasons: []string{"no_record"},
			want: []evidence.Proposal{prop("title", []string{"scan.pdf"}, []string{printedTitle}, []string{"text"})}},
		{name: "junk title, a record dissents", p: v8Book(titleOnly, func(p *evidence.Package) {
			titleAlone(p)
			p.Lookups = []evidence.Lookup{lookup(I, []string{evidence.SeedFile}, ok(evidence.SourceGoogleBooks, rec("g1", "Another Printed Title", []string{printedMan}, []string{"deu"}, I)))}
			p.Calibre.Title = "scan.pdf"
		}), tier: "B", reasons: []string{"text_roots_disagree", "title_incomplete"}},
		{name: "junk title, an external record agrees", p: v8Book(titleOnly, func(p *evidence.Package) {
			titleAlone(p)
			r := rec("g1", printedTitle, []string{printedMan}, []string{"deu"}, I)
			p.Lookups = []evidence.Lookup{lookup(I, []string{evidence.SeedFile}, ok(evidence.SourceGoogleBooks, r))}
			p.Calibre.Title = "scan.pdf"
		}), tier: "B", reasons: []string{"title_incomplete"},
			want: []evidence.Proposal{prop("title", []string{"scan.pdf"}, []string{printedTitle}, []string{"googlebooks", "text"})}},
		{name: "a title that is not junk, the text alone", p: v8Book(titleOnly, func(p *evidence.Package) { titleAlone(p) }),
			tier: "B", reasons: []string{"no_record", "title_incomplete"}},
		{name: "title noise is not junk", p: v8Book(titleOnly, func(p *evidence.Package) {
			titleAlone(p)
			p.Calibre.Title = printedTitle + " (Penguin Classics)"
		}), tier: "B", reasons: []string{"no_record", "title_incomplete"}},
		{name: "authors: a placeholder, the text alone", p: v8Book(factSet{"contributor": base["contributor"]}, func(p *evidence.Package) {
			noLookups(p)
			p.Formats[0].Metadata.Authors, p.Calibre.Authors = nil, []string{"Unknown"}
		}), tier: "B", reasons: []string{"authors_incomplete", "no_record"},
			want: []evidence.Proposal{prop("authors", []string{"Unknown"}, []string{printedMan}, []string{"text"})}},
		{name: "authors: empty, the text alone", p: v8Book(factSet{"contributor": base["contributor"]}, func(p *evidence.Package) {
			noLookups(p)
			p.Formats[0].Metadata.Authors, p.Calibre.Authors = nil, nil
		}), tier: "B", reasons: []string{"authors_incomplete", "no_record"},
			want: []evidence.Proposal{prop("authors", []string{}, []string{printedMan}, []string{"text"})}},
		{name: "authors: anonymous is a real attribution", p: v8Book(factSet{"contributor": base["contributor"]}, func(p *evidence.Package) {
			noLookups(p)
			p.Formats[0].Metadata.Authors, p.Calibre.Authors = nil, []string{"Anonymous"}
		}), tier: "B", reasons: []string{"authors_incomplete", "no_record"}},
		{name: "authors: various is a real attribution", p: v8Book(factSet{"contributor": base["contributor"]}, func(p *evidence.Package) {
			noLookups(p)
			p.Formats[0].Metadata.Authors, p.Calibre.Authors = nil, []string{"Various"}
		}), tier: "B", reasons: []string{"authors_incomplete", "no_record"}},
		{name: "authors: the file dissents", p: v8Book(factSet{"contributor": base["contributor"]}, func(p *evidence.Package) {
			noLookups(p)
			p.Formats[0].Metadata.Authors, p.Calibre.Authors = []string{"Another Person"}, []string{"Unknown"}
		}), tier: "B", reasons: []string{"no_record", "text_roots_disagree"}},
		{name: "authors: the file has a placeholder too", p: v8Book(factSet{"contributor": base["contributor"]}, func(p *evidence.Package) {
			noLookups(p)
			p.Formats[0].Metadata.Authors, p.Calibre.Authors = []string{"Unknown"}, []string{"Unknown"}
		}), tier: "B", reasons: []string{"no_record"},
			want: []evidence.Proposal{prop("authors", []string{"Unknown"}, []string{printedMan}, []string{"text"})}},
		{name: "publisher: empty, the text alone", p: v8Book(factSet{"imprint": base["imprint"]}, func(p *evidence.Package) {
			noLookups(p)
			p.Formats[0].Metadata.Publisher, p.Calibre.Publisher = "", ""
		}), tier: "B", reasons: []string{"no_record"},
			want: []evidence.Proposal{prop("publisher", []string{}, []string{printedHouse}, []string{"text"})}},
		{name: "publisher: a placeholder the file shares", p: v8Book(factSet{"imprint": base["imprint"]}, func(p *evidence.Package) {
			noLookups(p)
			p.Formats[0].Metadata.Publisher, p.Calibre.Publisher = "calibre", "calibre"
		}), tier: "B", reasons: []string{"no_record"},
			want: []evidence.Proposal{prop("publisher", []string{"calibre"}, []string{printedHouse}, []string{"text"})}},
		{name: "publisher: a website", p: v8Book(factSet{"imprint": base["imprint"]}, func(p *evidence.Package) {
			noLookups(p)
			p.Formats[0].Metadata.Publisher, p.Calibre.Publisher = "", "www.example.org"
		}), tier: "B", reasons: []string{"no_record"},
			want: []evidence.Proposal{prop("publisher", []string{"www.example.org"}, []string{printedHouse}, []string{"text"})}},
		{name: "publisher: a scanner site", p: v8Book(factSet{"imprint": base["imprint"]}, func(p *evidence.Package) {
			noLookups(p)
			p.Formats[0].Metadata.Publisher, p.Calibre.Publisher = "", "Z-Library"
		}), tier: "B", reasons: []string{"no_record"},
			want: []evidence.Proposal{prop("publisher", []string{"Z-Library"}, []string{printedHouse}, []string{"text"})}},
		{name: "publisher: the file dissents", p: v8Book(factSet{"imprint": base["imprint"]}, func(p *evidence.Package) {
			noLookups(p)
			p.Formats[0].Metadata.Publisher, p.Calibre.Publisher = "Another House", "calibre"
		}), tier: "B", reasons: []string{"no_record", "text_roots_disagree"}},
		{name: "publisher: a real name, the text alone", p: v8Book(factSet{"imprint": base["imprint"]}, func(p *evidence.Package) {
			noLookups(p)
			p.Formats[0].Metadata.Publisher, p.Calibre.Publisher = "", "Old House"
		}), tier: "B", reasons: []string{"no_record"}},
		{name: "publisher: a dissent that is the same up to a suffix", p: v8Book(factSet{"imprint": base["imprint"]}, func(p *evidence.Package) {
			noLookups(p)
			p.Formats[0].Metadata.Publisher, p.Calibre.Publisher = printedHouse+" Ltd", ""
		}), tier: "B", reasons: []string{"no_record"},
			want: []evidence.Proposal{prop("publisher", []string{}, []string{printedHouse}, []string{"text"})}},
		{name: "isbn: Calibre has none, the file has none", p: v8Book(factSet{"isbn": base["isbn"]}, func(p *evidence.Package) {
			noLookups(p)
			p.Formats[0].Metadata.ISBNs, p.Calibre.Identifiers = nil, nil
		}), tier: "B", reasons: []string{"isbn_missing", "no_record"},
			want: []evidence.Proposal{prop("isbn", []string{}, []string{I}, []string{"text"})}},
		{name: "isbn: Calibre has one, the text alone", p: v8Book(factSet{"isbn": base["isbn"]}, func(p *evidence.Package) {
			noLookups(p)
			p.Formats[0].Metadata.ISBNs = nil
		}), tier: "B", reasons: []string{"isbn_missing", "no_record"}},
		{name: "isbn: the file has another one", p: v8Book(factSet{"isbn": isbnFact(av(I3))}, func(p *evidence.Package) {
			noLookups(p)
			p.Calibre.Identifiers = nil
		}), tier: "B", reasons: []string{"no_record", "text_roots_disagree"}},

		// --- the copyright page ISBN, now a text root ---
		{name: "front matter, record and text", p: func() evidence.Package {
			p := schema2(book([]evidence.FormatEvidence{hinted(T, copyrightISBN(I, evidence.QualifierEbook))},
				lookup(I, []string{evidence.HintEPUBText}, ok(evidence.SourceDNB, dnbT()))))
			p.Calibre.Identifiers = map[string]string{"isbn": I2}
			factSet{"isbn": base["isbn"]}.stamp(&p)
			return p
		}(), tier: "B", reasons: []string{"identity_frontmatter", "isbn_missing"},
			want: []evidence.Proposal{prop("isbn", []string{I2}, []string{I}, []string{"dnb", "text"})}},
		{name: "a record that does not match the book", p: v8Book(base, func(p *evidence.Package) {
			p.Lookups[0].Results[0].Records[0].Languages = []string{"eng"}
		}), tier: "B", reasons: []string{"records_disagree"},
			want: []evidence.Proposal{wantProposal("title"), wantProposal("publisher")}},

		// --- title noise ---
		{name: "noise: a series in parentheses", p: v8Book(base, func(p *evidence.Package) { p.Calibre.Title = printedTitle + " (Penguin Classics)" }), tier: "A",
			want: replaced(baseWant(), prop("title", []string{printedTitle + " (Penguin Classics)"}, []string{printedTitle}, trio))},
		{name: "noise: an edition credit", p: v8Book(base, func(p *evidence.Package) { p.Calibre.Title = printedTitle + " [Annotated Edition]" }), tier: "A",
			want: replaced(baseWant(), prop("title", []string{printedTitle + " [Annotated Edition]"}, []string{printedTitle}, trio))},
		{name: "noise: a translator credit", p: v8Book(base, func(p *evidence.Package) { p.Calibre.Title = printedTitle + ": Translated by John Smith" }), tier: "A",
			want: replaced(baseWant(), prop("title", []string{printedTitle + ": Translated by John Smith"}, []string{printedTitle}, trio))},
		{name: "noise: an author prefix", p: v8Book(base, func(p *evidence.Package) {
			p.Calibre.Title, p.Calibre.Authors = "Author, Ann - "+printedTitle, []string{printedMan}
		}), tier: "A", want: replaced(baseWant("authors"), prop("title", []string{"Author, Ann - " + printedTitle}, []string{printedTitle}, trio))},
		{name: "noise: the second root is missing", p: v8Book(base, func(p *evidence.Package) {
			noLookups(p)
			p.Formats[0].Metadata.Title = ""
			p.Calibre.Title = printedTitle + " (Penguin Classics)"
		}), tier: "B", reasons: []string{"no_record", "title_incomplete"},
			want: []evidence.Proposal{wantProposal("publisher", "file", "text")}},
		{name: "noise: more than the noise is gone", p: v8Book(base, func(p *evidence.Package) { p.Calibre.Title = printedTitle + " Extra (Penguin Classics)" }), tier: "A",
			want: baseWant("title")},
		{name: "a subtitle is never dropped", p: v8Book(base, func(p *evidence.Package) { p.Calibre.Title = printedTitle + ": A Real Subtitle" }), tier: "A",
			want: baseWant("title")},
		{name: "a junk title may shorten", p: v8Book(base, func(p *evidence.Package) { p.Calibre.Title = printedTitle + ".pdf" }), tier: "A",
			want: replaced(baseWant(), prop("title", []string{printedTitle + ".pdf"}, []string{printedTitle}, trio))},

		// --- other work from the text ---
		{name: "other work: the title and the authors differ", p: v8Book(base, func(p *evidence.Package) {
			noLookups(p)
			p.Calibre.Title, p.Calibre.Authors = "A Completely Different Book", []string{"Another Person"}
		}), tier: "C", reasons: []string{"no_record", "other_work"}},
		{name: "other work: only the title differs", p: v8Book(base, func(p *evidence.Package) {
			noLookups(p)
		}), tier: "B", reasons: []string{"no_record"},
			want: []evidence.Proposal{wantProposal("publisher", "file", "text")}},
		{name: "other work: Calibre's title is junk", p: v8Book(base, func(p *evidence.Package) {
			noLookups(p)
			p.Calibre.Title, p.Calibre.Authors = "scan.pdf", []string{"Another Person"}
		}), tier: "B", reasons: []string{"no_record"},
			want: []evidence.Proposal{prop("title", []string{"scan.pdf"}, []string{printedTitle}, []string{"file", "text"}),
				wantProposal("publisher", "file", "text")}},
		{name: "other work: Calibre's authors are a placeholder", p: v8Book(base, func(p *evidence.Package) {
			noLookups(p)
			p.Calibre.Title, p.Calibre.Authors = "A Completely Different Book", []string{"Anonymous"}
		}), tier: "B", reasons: []string{"no_record"},
			want: []evidence.Proposal{wantProposal("publisher", "file", "text")}},
		{name: "other work: the title differs by noise only", p: v8Book(base, func(p *evidence.Package) {
			noLookups(p)
			p.Calibre.Title, p.Calibre.Authors = printedTitle+" (Penguin Classics)", []string{"Another Person"}
		}), tier: "B", reasons: []string{"no_record"},
			want: []evidence.Proposal{prop("title", []string{printedTitle + " (Penguin Classics)"}, []string{printedTitle}, []string{"file", "text"}),
				wantProposal("publisher", "file", "text")}},
		{name: "other work: the authors differ by an honorific only", p: v8Book(base, func(p *evidence.Package) {
			noLookups(p)
			p.Calibre.Title, p.Calibre.Authors = "A Completely Different Book", []string{"Dr. " + printedMan}
		}), tier: "B", reasons: []string{"no_record"},
			want: []evidence.Proposal{wantProposal("publisher", "file", "text")}},
		{name: "other work: the text title is unclear", p: v8Book(base.with(disagreeFact("title")), func(p *evidence.Package) {
			noLookups(p)
			p.Calibre.Title, p.Calibre.Authors = "A Completely Different Book", []string{"Another Person"}
		}), tier: "B", reasons: []string{"no_record"},
			want: []evidence.Proposal{wantProposal("publisher", "file", "text")}},

		// --- the ISBN must be printed in the book, not in an ad (B1) ---
		{name: "ISBN only in an ad", p: v8Book(base.with(isbnFact(backISBN(I3))), nil), tier: "A",
			want: replaced(baseWant(), wantProposal("isbn", "dnb", "file"))},
		{name: "ISBN the extractors state and no parser grounds", p: v8Book(base.with(extractorOnlyISBN(av(I))), nil), tier: "A",
			want: replaced(baseWant(), wantProposal("isbn", "dnb", "file"))},
		{name: "ISBN printed in the front and in an ad", p: v8Book(base.with(isbnFact(av(I), backISBN(I3))), nil), tier: "A", want: baseWant()},
		// A record backs the text ISBN only under version 6's exact title key,
		// not strictMatcher's folds alone, so it can't replace a real ISBN.
		{name: "ISBN: the record's title adds a parenthetical", p: v8Book(base, func(p *evidence.Package) {
			p.Lookups[0].Results[0].Records[0].Title = printedTitle + " (Roman)"
		}), tier: "A", want: []evidence.Proposal{wantProposal("publisher")}},
		{name: "ISBN: the record's title adds an article", p: v8Book(base, func(p *evidence.Package) {
			p.Lookups[0].Results[0].Records[0].Title = "The " + printedTitle
		}), tier: "A", reasons: []string{"text_roots_disagree"}, want: []evidence.Proposal{wantProposal("publisher")}},
		{name: "ISBN with a print ISBN beside it, two roots", p: v8Book(base.with(isbnFact(av(I), labelled(I3, "print"))), nil), tier: "A", want: baseWant()},
		{name: "ISBN fill: one unlabelled ISBN, the only one", p: v8Book(factSet{"isbn": isbnFact(av(I))}, noISBNs), tier: "B",
			reasons: []string{"isbn_missing", "no_record"}, want: []evidence.Proposal{prop("isbn", []string{}, []string{I}, []string{"text"})}},
		{name: "ISBN fill: ebook-labelled beside a print one", p: v8Book(factSet{"isbn": isbnFact(labelled(I, "ebook"), labelled(I3, "print"))}, noISBNs), tier: "B",
			reasons: []string{"isbn_missing", "no_record"}, want: []evidence.Proposal{prop("isbn", []string{}, []string{I}, []string{"text"})}},
		{name: "ISBN fill: the only one in the front, another in an ad", p: v8Book(factSet{"isbn": isbnFact(av(I), backISBN(I3))}, noISBNs), tier: "B",
			reasons: []string{"isbn_missing", "no_record"}, want: []evidence.Proposal{prop("isbn", []string{}, []string{I}, []string{"text"})}},
		{name: "ISBN fill refused: unlabelled beside a print ISBN", p: v8Book(factSet{"isbn": isbnFact(av(I), labelled(I3, "print"))}, noISBNs), tier: "B",
			reasons: []string{"isbn_missing", "no_record"}},
		{name: "ISBN fill refused: an ISBN only in an ad", p: v8Book(factSet{"isbn": isbnFact(backISBN(I))}, noISBNs), tier: "B",
			reasons: []string{"isbn_missing", "no_record"}},

		// --- a publisher is only filled or replaced when junk (M1) ---
		{name: "publisher: a real value is never replaced", p: v8Book(base, func(p *evidence.Package) { p.Calibre.Publisher = "Old House" }), tier: "A", want: baseWant("publisher")},

		// --- title equivalence and shortening (M2, M3) ---
		{name: "title: punctuation only", p: v8Book(base.with(agreedFact("title", av("Hello World"))), func(p *evidence.Package) {
			p.Calibre.Title, p.Formats[0].Metadata.Title, p.Lookups[0].Results[0].Records[0].Title = "Hello, World!", "Hello World", "Hello World"
		}), tier: "A", want: baseWant("title")},
		{name: "title: curly quote and en dash", p: v8Book(base.with(agreedFact("title", av("Don't Panic - A Guide"))), func(p *evidence.Package) {
			p.Calibre.Title, p.Formats[0].Metadata.Title, p.Lookups[0].Results[0].Records[0].Title = "Don’t Panic – A Guide", "Don't Panic - A Guide", "Don't Panic - A Guide"
		}), tier: "A", want: baseWant("title")},
		{name: "title: words dropped from the middle", p: v8Book(base.with(agreedFact("title", av("Dune: Messiah"))), func(p *evidence.Package) {
			p.Calibre.Title, p.Formats[0].Metadata.Title, p.Lookups[0].Results[0].Records[0].Title = "Dune, Book Two: Messiah", "Dune: Messiah", "Dune: Messiah"
		}), tier: "A", want: baseWant("title")},

		// --- replacing a real value needs a record (M4) ---
		{name: "title: another volume by the same author, no record", p: v8Book(base.with(agreedFact("title", av("Book Two"))), func(p *evidence.Package) {
			noLookups(p)
			p.Calibre.Title, p.Formats[0].Metadata.Title = "Book One", "Book Two"
		}), tier: "B", reasons: []string{"no_record"}, want: []evidence.Proposal{wantProposal("publisher", "file", "text")}},
		{name: "authors: Dostoevsky against Dostoyevsky, no record", p: v8Book(base.with(agreedFact("contributor", person("Fyodor Dostoyevsky", "author"))), func(p *evidence.Package) {
			noLookups(p)
			p.Calibre.Title, p.Calibre.Authors, p.Formats[0].Metadata.Authors = printedTitle, []string{"Fyodor Dostoevsky"}, []string{"Fyodor Dostoyevsky"}
		}), tier: "B", reasons: []string{"no_record"}, want: []evidence.Proposal{wantProposal("publisher", "file", "text")}},
		{name: "authors: Dostoevsky against Dostoyevsky, with a record", p: v8Book(base.with(agreedFact("contributor", person("Fyodor Dostoyevsky", "author"))), func(p *evidence.Package) {
			p.Calibre.Title, p.Calibre.Authors, p.Formats[0].Metadata.Authors = printedTitle, []string{"Fyodor Dostoevsky"}, []string{"Fyodor Dostoyevsky"}
			p.Lookups[0].Results[0].Records[0].Authors = []string{"Fyodor Dostoyevsky"}
		}), tier: "A", want: []evidence.Proposal{prop("authors", []string{"Fyodor Dostoevsky"}, []string{"Fyodor Dostoyevsky"}, trio), wantProposal("isbn"), wantProposal("publisher")}},

		// --- authors dissent, junk text, surname escape (m1, m2, m5) ---
		{name: "authors: the file lists more people than the text", p: v8Book(factSet{"contributor": base["contributor"]}, func(p *evidence.Package) {
			noLookups(p)
			p.Formats[0].Metadata.Authors, p.Calibre.Authors = []string{printedMan, "Bo Two", "Cy Three"}, []string{"Unknown"}
		}), tier: "B", reasons: []string{"no_record", "text_roots_disagree"}},
		{name: "authors: the file lists fewer people than the text", p: v8Book(factSet{"contributor": agreedFact("contributor", person(printedMan, "author"), person("Bo Two", "author"))}, func(p *evidence.Package) {
			noLookups(p)
			p.Formats[0].Metadata.Authors, p.Calibre.Authors = []string{printedMan}, []string{"Unknown"}
		}), tier: "B", reasons: []string{"no_record"},
			want: []evidence.Proposal{prop("authors", []string{"Unknown"}, []string{printedMan, "Bo Two"}, []string{"text"})}},
		{name: "a junk text title gives no statement", p: v8Book(factSet{"title": agreedFact("title", av("scan.pdf"))}, func(p *evidence.Package) {
			titleAlone(p)
			p.Calibre.Title = "scan.pdf"
		}), tier: "B", reasons: []string{"no_record", "title_incomplete"}},
		{name: "a placeholder text publisher gives no statement", p: v8Book(factSet{"imprint": agreedFact("imprint", av("calibre"))}, func(p *evidence.Package) {
			noLookups(p)
			p.Formats[0].Metadata.Publisher = ""
		}), tier: "B", reasons: []string{"no_record"}},
		{name: "a placeholder text author gives no statement", p: v8Book(factSet{"contributor": agreedFact("contributor", person("Unknown", "author"))}, func(p *evidence.Package) {
			noLookups(p)
			p.Formats[0].Metadata.Authors, p.Calibre.Authors = nil, []string{"Unknown"}
		}), tier: "B", reasons: []string{"authors_incomplete", "no_record"}},
		{name: "other work: a shared surname", p: v8Book(base, func(p *evidence.Package) {
			noLookups(p)
			p.Calibre.Title, p.Calibre.Authors = "A Completely Different Book", []string{"A. Author"}
		}), tier: "B", reasons: []string{"no_record"}, want: []evidence.Proposal{wantProposal("publisher", "file", "text")}},

		// --- fields without a text statement keep the v7 rules ---
		{name: "languages and dates stay as in v7", p: v8Book(base, func(p *evidence.Package) {
			p.Calibre.Languages, p.Calibre.PubDate = nil, "2001-01-05T00:00:00+00:00"
			p.Formats[0].Metadata.Date = "2020-05-05"
			p.Lookups[0].Results[0].Records[0].Date = "2020-05-05"
		}), tier: "A", want: []evidence.Proposal{wantProposal("title"),
			prop("languages", nil, []string{"deu"}, []string{"dnb", "file"}), wantProposal("isbn"), wantProposal("publisher"),
			prop("pubdate", []string{"2001-01-05T00:00:00+00:00"}, []string{"2020-05-05"}, []string{"dnb", "file"})}},
	}
}

func TestDecisionV8(t *testing.T) {
	for _, tt := range v8Cases() {
		t.Run(tt.name, func(t *testing.T) {
			d, err := decide.AtVersion(t.Context(), tt.p, 8)
			if err != nil {
				t.Fatal(err)
			}
			if d.Tier != tt.tier || !slices.Equal(d.Reasons, tt.reasons) {
				t.Errorf("tier %s %v, want %s %v", d.Tier, d.Reasons, tt.tier, tt.reasons)
			}
			if !equalProposals(d.Proposals, tt.want) {
				got, _ := json.Marshal(d.Proposals)
				want, _ := json.Marshal(tt.want)
				t.Errorf("proposals\n got %s\nwant %s", got, want)
			}
		})
	}
}

// TestV8CasesAtV7: the same packages derive at version 7 what they derive
// without any extraction. Version 7 and older ignore the text.
func TestV8CasesAtV7(t *testing.T) {
	for _, tt := range v8Cases() {
		t.Run(tt.name, func(t *testing.T) {
			for version := 1; version <= 7; version++ {
				with, err := decide.AtVersion(t.Context(), tt.p, version)
				if err != nil {
					t.Fatalf("v%d: %v", version, err)
				}
				without, err := decide.AtVersion(t.Context(), withoutText(tt.p), version)
				if err != nil {
					t.Fatalf("v%d without text: %v", version, err)
				}
				a, _ := json.Marshal(with)
				b, _ := json.Marshal(without)
				if string(a) != string(b) {
					t.Errorf("v%d: the text changed the decision\nwith:    %s\nwithout: %s", version, a, b)
				}
			}
		})
	}
}

// TestV8Seals: every v8 case seals at the current version, opens again and
// derives the decision the case expects. Version 10 changes one: a record in
// another language no longer backs replacing a real title (ADR-045 rule 4).
func TestV8Seals(t *testing.T) {
	atCurrent := map[string][]evidence.Proposal{
		"a record that does not match the book": {wantProposal("publisher")},
	}
	for _, tt := range v8Cases() {
		if tt.unsealable {
			continue
		}
		t.Run(tt.name, func(t *testing.T) {
			want := decide.Decision{Tier: tt.tier, Proposals: tt.want}
			if p, ok := atCurrent[tt.name]; ok {
				want.Proposals = p
			}
			raw, seal, err := decide.Seal(t.Context(), tt.p)
			if err != nil {
				t.Fatal(err)
			}
			p, d, err := decide.Open(t.Context(), raw, seal)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			if p.DecisionVersion != decide.Version || d.Tier != want.Tier || !equalProposals(d.Proposals, want.Proposals) {
				t.Errorf("version %d, tier %s, proposals %+v", p.DecisionVersion, d.Tier, d.Proposals)
			}
		})
	}
}

func TestV8Reasons(t *testing.T) {
	for _, code := range []string{"formats_text_conflict", "text_roots_disagree"} {
		if decide.ReasonLabel(code) == code {
			t.Errorf("no label for %s", code)
		}
	}
	known := false
	for _, def := range findings.Catalog() {
		known = known || def.Code == "text_roots_disagree"
	}
	if !known {
		t.Error("text_roots_disagree is not in the findings catalogue")
	}
}

// text_roots_disagree adds no tier and shows with the findings, per field.
func TestTextRootsDisagreeFinding(t *testing.T) {
	base := baseFacts()
	p := v8Book(base.with(agreedFact("title", av("Another Printed Title")), agreedFact("imprint", av("Another House"))), nil)
	d, err := decide.AtVersion(t.Context(), p, 8)
	if err != nil {
		t.Fatal(err)
	}
	if d.Tier != evidence.TierA || !slices.Equal(d.Reasons, []string{"text_roots_disagree"}) {
		t.Fatalf("tier %s, reasons %v", d.Tier, d.Reasons)
	}
	var got []string
	for _, f := range d.Findings() {
		if f.Code != "text_roots_disagree" {
			t.Errorf("finding %+v", f)
		}
		got = append(got, f.Field)
	}
	if !slices.Equal(got, []string{"title", "publisher"}) {
		t.Errorf("fields %v, want title and publisher", got)
	}
	// A Tier C book gets no proposals, so no finding either.
	p.Formats[0].Metadata.Title = "Another File Title"
	p.Formats = append(p.Formats, p.Formats[0])
	p.Formats[1].Format = "ORIGINAL_EPUB"
	p.Formats[1].Metadata = &evidence.FileMetadata{Title: "Third Title", Authors: []string{printedMan}, Languages: []string{"de"}, ISBNs: []string{I}}
	c, err := decide.AtVersion(t.Context(), p, 8)
	if err != nil {
		t.Fatal(err)
	}
	if c.Tier != evidence.TierC || len(c.Findings()) != 0 || slices.Contains(c.Reasons, "text_roots_disagree") {
		t.Errorf("Tier C: %s %v %v", c.Tier, c.Reasons, c.Findings())
	}
}

// Wikidata and Hardcover stay recorded-only at versions 8 and 9 (ADR-036 section 6).
func TestRecordedOnlyAtV8(t *testing.T) {
	p := v8Book(baseFacts(), func(p *evidence.Package) {
		p.Lookups[0].Results = append(p.Lookups[0].Results,
			ok(evidence.SourceWikidata, rec("Q1", "Another Title", []string{"Someone Else"}, []string{"eng"}, I)),
			ok(evidence.SourceHardcover, rec("1", "Another Title", []string{"Someone Else"}, []string{"eng"}, I)))
	})
	for _, version := range []int{8, 9} {
		got, err := decide.AtVersion(t.Context(), p, version)
		if err != nil {
			t.Fatal(err)
		}
		want, err := decide.AtVersion(t.Context(), v8Book(baseFacts(), nil), version)
		if err != nil {
			t.Fatal(err)
		}
		a, _ := json.Marshal(got)
		b, _ := json.Marshal(want)
		if string(a) != string(b) {
			t.Errorf("v%d: recorded-only sources changed the decision\n%s\n%s", version, a, b)
		}
	}
}

func TestFormatsTextConflictIsTierB(t *testing.T) {
	p := v8Book(baseFacts(), func(p *evidence.Package) {
		secondEPUB(p, baseFacts().with(agreedFact("contributor", person("Another Person", "author"))))
	})
	d, err := decide.AtVersion(t.Context(), p, 8)
	if err != nil {
		t.Fatal(err)
	}
	if d.Tier != evidence.TierB || !slices.Equal(d.Reasons, []string{"formats_text_conflict"}) {
		t.Fatalf("tier %s, reasons %v", d.Tier, d.Reasons)
	}
}
