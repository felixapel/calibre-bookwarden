package decide_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/decide"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/findings"
)

const (
	I2 = "9780306406157" // another valid ISBN-13
	I3 = "9780596520687" // a third
)

// frontPkg is a schema 2 book whose only format states no ISBN of its own
// and has front-matter hints. Calibre has no language and no ISBN, so a
// confirmed identity yields a languages and an isbn proposal.
func frontPkg(ctx []evidence.ISBNContext, results ...evidence.SourceResult) evidence.Package {
	var lookups []evidence.Lookup
	for _, isbn := range []string{I, I2} {
		var rs []evidence.SourceResult
		for _, r := range results {
			r.Records = slices.DeleteFunc(slices.Clone(r.Records), func(rc evidence.SourceRecord) bool { return !slices.Contains(rc.ISBNs, isbn) })
			if len(r.Records) > 0 || r.Outcome != evidence.SourceOK {
				rs = append(rs, r)
			}
		}
		if len(rs) > 0 {
			lookups = append(lookups, lookup(isbn, []string{evidence.HintEPUBText}, rs...))
		}
	}
	p := schema2(book([]evidence.FormatEvidence{hinted(T, ctx...)}, lookups...))
	p.Calibre.Languages, p.Calibre.Identifiers = nil, nil
	return p
}

func fields(ps []evidence.Proposal) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Field)
	}
	return out
}

func TestDecisionVersion(t *testing.T) {
	if decide.Version != 11 {
		t.Fatalf("Version = %d, want 11", decide.Version)
	}
}

// TestFrontMatterIdentity covers ADR-035 part 2: with no metadata ISBN, an
// unambiguous copyright or title page ISBN selects whose records are used,
// once a record's title matches the files' or Calibre's.
func TestFrontMatterIdentity(t *testing.T) {
	ebook := evidence.QualifierEbook
	dnb := ok(evidence.SourceDNB, dnbT())
	i2 := func(title string) evidence.SourceRecord { return rec("2", title, []string{A}, []string{"deu"}, I2) }
	landmark := func(isbn, lm, q string) evidence.ISBNContext {
		return evidence.ISBNContext{ISBN: isbn, Landmark: lm, Qualifier: q}
	}
	for _, tt := range []struct {
		name       string
		p          func() evidence.Package
		reasons    []string
		tier       string
		proposed   []string // proposal fields
		isbnChosen string   // the proposed ISBN, when there is an isbn proposal
	}{
		{"metadata ISBN present: the v5 path, hints ignored", func() evidence.Package {
			p := frontPkg([]evidence.ISBNContext{copyrightISBN(I2, ebook)}, dnb)
			p.Formats[0].Metadata.ISBNs = []string{I}
			return p
		}, nil, evidence.TierA, []string{"languages", "isbn"}, I},
		{"one copyright ebook ISBN, confirmed by a DNB title", func() evidence.Package {
			return frontPkg([]evidence.ISBNContext{copyrightISBN(I, ebook)}, dnb)
		}, []string{"identity_frontmatter", "isbn_missing"}, evidence.TierB, []string{"languages"}, ""},
		{"an unqualified title page ISBN counts", func() evidence.Package {
			return frontPkg([]evidence.ISBNContext{landmark(I, evidence.LandmarkTitle, "")}, dnb)
		}, []string{"identity_frontmatter", "isbn_missing"}, evidence.TierB, []string{"languages"}, ""},
		{"confirmed by Calibre's title when the file has none", func() evidence.Package {
			p := frontPkg([]evidence.ISBNContext{copyrightISBN(I, ebook)}, dnb)
			p.Formats[0].Metadata.Title = ""
			return p
		}, []string{"identity_frontmatter", "isbn_missing", "title_incomplete"}, evidence.TierB, []string{"languages"}, ""},
		{"the record title doesn't match", func() evidence.Package {
			r := dnbT()
			r.Title = "Another Title"
			return frontPkg([]evidence.ISBNContext{copyrightISBN(I, ebook)}, ok(evidence.SourceDNB, r))
		}, []string{"frontmatter_isbn_unconfirmed", "isbn_missing", "no_record"}, evidence.TierB, nil, ""},
		{"a hardcover and an ebook ISBN: the ebook one", func() evidence.Package {
			return frontPkg([]evidence.ISBNContext{copyrightISBN(I, evidence.QualifierHardcover), copyrightISBN(I2, ebook)}, ok(evidence.SourceDNB, i2(T)))
		}, []string{"identity_frontmatter", "isbn_missing"}, evidence.TierB, []string{"languages"}, ""},
		{"a print ISBN and an unqualified one: the unqualified one", func() evidence.Package {
			return frontPkg([]evidence.ISBNContext{copyrightISBN(I, evidence.QualifierPrint), copyrightISBN(I2, "")}, ok(evidence.SourceDNB, i2(T)))
		}, []string{"identity_frontmatter", "isbn_missing"}, evidence.TierB, []string{"languages"}, ""},
		{"a paperback ISBN and a pdf ISBN only", func() evidence.Package {
			return frontPkg([]evidence.ISBNContext{copyrightISBN(I, evidence.QualifierPaperback), copyrightISBN(I2, evidence.QualifierPDF)}, dnb)
		}, []string{"frontmatter_isbn_missing", "isbn_missing", "no_record"}, evidence.TierB, nil, ""},
		{"two ebook ISBNs are ambiguous", func() evidence.Package {
			return frontPkg([]evidence.ISBNContext{copyrightISBN(I, ebook), copyrightISBN(I2, ebook)}, dnb)
		}, []string{"frontmatter_isbn_ambiguous", "isbn_missing", "no_record"}, evidence.TierB, nil, ""},
		{"two unqualified ISBNs are ambiguous", func() evidence.Package {
			return frontPkg([]evidence.ISBNContext{copyrightISBN(I, ""), landmark(I2, evidence.LandmarkTitle, "")}, dnb)
		}, []string{"frontmatter_isbn_ambiguous", "isbn_missing", "no_record"}, evidence.TierB, nil, ""},
		{"back and front ISBNs only", func() evidence.Package {
			return frontPkg([]evidence.ISBNContext{landmark(I, evidence.LandmarkBack, ""), landmark(I2, evidence.LandmarkFront, evidence.QualifierEbook)}, dnb)
		}, []string{"frontmatter_isbn_missing", "isbn_missing", "no_record"}, evidence.TierB, nil, ""},
		{"a failed hint is ignored", func() evidence.Package {
			p := frontPkg([]evidence.ISBNContext{copyrightISBN(I, ebook)}, dnb)
			p.Formats[0].ISBNHints.State = evidence.HintFailed
			return p
		}, []string{"isbn_missing", "no_record"}, evidence.TierB, nil, ""},
		{"a PDF OCR hint is not front matter", func() evidence.Package {
			p := frontPkg([]evidence.ISBNContext{copyrightISBN(I, ebook)}, dnb)
			p.Formats[0].ISBNHints.Method = evidence.HintOCR
			return p
		}, []string{"isbn_missing", "no_record"}, evidence.TierB, nil, ""},
		{"an ambiguous metadata ISBN on another format", func() evidence.Package {
			p := frontPkg([]evidence.ISBNContext{copyrightISBN(I, ebook)}, dnb)
			p.Formats = append(p.Formats, format("MOBI", "mobi", T, []string{A}, []string{"de"}, []string{I, I3}))
			return p
		}, []string{"isbn_ambiguous", "isbn_missing", "no_record"}, evidence.TierB, nil, ""},
		{"a flagged metadata ISBN on another format", func() evidence.Package {
			p := frontPkg([]evidence.ISBNContext{copyrightISBN(I, ebook)}, dnb)
			m := format("MOBI", "mobi", T, []string{A}, []string{"de"}, nil)
			m.Metadata.Limits = &evidence.MetadataLimits{Version: 1, ISBNs: true}
			p.Formats = append(p.Formats, m)
			return p
		}, []string{"isbn_missing", "no_record"}, evidence.TierB, nil, ""},
		{"another format's metadata ISBN decides: the v5 path", func() evidence.Package {
			p := frontPkg([]evidence.ISBNContext{copyrightISBN(I, ebook)}, dnb)
			p.Formats = append(p.Formats, format("MOBI", "mobi", T, []string{A}, []string{"de"}, []string{I3}))
			return p
		}, []string{"isbn_missing", "no_record"}, evidence.TierB, nil, ""},
		{"only Wikidata and Hardcover records", func() evidence.Package {
			return frontPkg([]evidence.ISBNContext{copyrightISBN(I, ebook)},
				ok(evidence.SourceWikidata, rec("Q1", T, []string{A}, []string{"deu"}, I)),
				ok(evidence.SourceHardcover, rec("1", T, []string{A}, []string{"deu"}, I)))
		}, []string{"frontmatter_isbn_unconfirmed", "isbn_missing", "no_record"}, evidence.TierB, nil, ""},
		{"hints on a second EPUB agree", func() evidence.Package {
			p := frontPkg([]evidence.ISBNContext{copyrightISBN(I, ebook)}, dnb)
			second := hinted(T, copyrightISBN(I, ebook))
			second.Format = "KEPUB"
			p.Formats = append(p.Formats, second)
			return p
		}, []string{"identity_frontmatter", "isbn_missing"}, evidence.TierB, []string{"languages"}, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d, err := decide.Decide(t.Context(), tt.p())
			if err != nil {
				t.Fatal(err)
			}
			if d.Tier != tt.tier || !slices.Equal(d.Reasons, tt.reasons) {
				t.Fatalf("tier %s %v, want %s %v", d.Tier, d.Reasons, tt.tier, tt.reasons)
			}
			if got := fields(d.Proposals); !slices.Equal(got, tt.proposed) {
				t.Fatalf("proposal fields %v, want %v", got, tt.proposed)
			}
			for _, pr := range d.Proposals {
				if pr.Field == "isbn" && !slices.Equal(pr.Proposed, []string{tt.isbnChosen}) {
					t.Fatalf("proposed ISBN %v, want %s", pr.Proposed, tt.isbnChosen)
				}
			}
		})
	}
}

// TestFrontMatterNeedsTwoRoots: a front-matter ISBN only selects records;
// the values still need the usual two independent roots (spec section 7).
func TestFrontMatterNeedsTwoRoots(t *testing.T) {
	gb := rec("gb1", T, []string{A}, []string{"deu"}, I)
	p := frontPkg([]evidence.ISBNContext{copyrightISBN(I, evidence.QualifierEbook)}, ok(evidence.SourceOpenLibrary, gb))
	p.Formats[0].Metadata.Authors = nil
	p.Calibre.Title = T
	p.Calibre.Authors = []string{"Unknown"}
	d, err := decide.Decide(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(fields(d.Proposals), "authors") {
		t.Fatalf("one external record proposed values: %+v", d.Proposals)
	}
}

// TestOtherWorkGuard: when the identity's records agree on a title and an
// authors list that both differ from Calibre's, the ISBN belongs to another
// work: no proposals at all, and Tier C.
func TestOtherWorkGuard(t *testing.T) {
	wrongSort := "x"
	for _, tt := range []struct {
		name      string
		p         func() evidence.Package
		version   int
		otherWork bool
		proposes  bool // a normal run of proposals is expected
	}{
		{"title and authors differ, the files side with Calibre (v5 identity path)", func() evidence.Package {
			p := tierA()
			p.Calibre.Title, p.Calibre.Authors, p.Calibre.TitleSort = "Another Book", []string{"Someone Else"}, &wrongSort
			p.Formats[0].Metadata.Title, p.Formats[0].Metadata.Authors = "Another Book", []string{"Someone Else"}
			return p
		}, 6, true, false},
		{"title and authors differ, the files and records agree (front-matter path)", func() evidence.Package {
			p := frontPkg([]evidence.ISBNContext{copyrightISBN(I, evidence.QualifierEbook)}, ok(evidence.SourceDNB, dnbT()))
			p.Calibre.Title, p.Calibre.Authors, p.Calibre.TitleSort = "Another Book", []string{"Someone Else"}, &wrongSort
			return p
		}, 6, true, false},
		{"files and records agree on B by Y, Calibre says A by X", func() evidence.Package {
			p := tierA()
			p.Calibre.Title, p.Calibre.Authors = "Another Book", []string{"Someone Else"}
			return p
		}, 6, true, false},
		{"a dropped subtitle and an author-form variant are the same work", func() evidence.Package {
			p := tierA()
			p.Calibre.Title = T + ": Roman"
			p.Calibre.Authors = []string{"J.K. Rowling"}
			return p
		}, 6, false, false},
		{"a different title by the same surname is the same author", func() evidence.Package {
			p := tierA()
			p.Calibre.Title, p.Calibre.Authors = "Another Book", []string{"Joanne Rowling"}
			return p
		}, 6, false, true},
		{"the same with Calibre's default author Unknown", func() evidence.Package {
			p := tierA()
			p.Calibre.Title, p.Calibre.Authors = "Another Book", []string{"Unknown"}
			return p
		}, 6, false, true},
		{"the same with a placeholder author in another language", func() evidence.Package {
			p := tierA()
			p.Calibre.Title, p.Calibre.Authors = "Another Book", []string{"Autor desconocido"} //nolint:misspell // Spanish
			return p
		}, 6, false, true},
		{"the same with no Calibre authors", func() evidence.Package {
			p := tierA()
			p.Calibre.Title, p.Calibre.Authors = "Another Book", nil
			return p
		}, 6, false, true},
		{"two Calibre authors, one a placeholder, is not a placeholder", func() evidence.Package {
			p := tierA()
			p.Calibre.Title, p.Calibre.Authors = "Another Book", []string{"Unknown", "Someone Else"}
			return p
		}, 6, true, false},
		{"only the title differs", func() evidence.Package {
			p := tierA()
			p.Calibre.Title = "Another Book"
			return p
		}, 6, false, true},
		{"only the authors differ", func() evidence.Package {
			p := tierA()
			p.Calibre.Authors = []string{"Someone Else"}
			return p
		}, 6, false, true},
		{"roots disagree among themselves", func() evidence.Package {
			p := tierA()
			p.Calibre.Title, p.Calibre.Authors = "Another Book", []string{"Someone Else"}
			p.Lookups[0].Results = append(p.Lookups[0].Results, ok(evidence.SourceBNE, rec("b", "Third", []string{"Third Person"}, []string{"deu"}, I)))
			return p
		}, 6, false, false},
		{"Calibre has no authors: nothing to differ from", func() evidence.Package {
			p := tierA()
			p.Calibre.Title, p.Calibre.Authors = "Another Book", nil
			return p
		}, 6, false, false},
		{"version 5 doesn't have the guard", func() evidence.Package {
			p := tierA()
			p.Calibre.Title, p.Calibre.Authors = "Another Book", []string{"Someone Else"}
			return p
		}, 5, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d, err := decide.AtVersion(t.Context(), tt.p(), tt.version)
			if err != nil {
				t.Fatal(err)
			}
			if got := slices.Contains(d.Reasons, "other_work"); got != tt.otherWork {
				t.Fatalf("other_work = %v, want %v (tier %s %v)", got, tt.otherWork, d.Tier, d.Reasons)
			}
			if tt.otherWork {
				if d.Tier != evidence.TierC || len(d.Proposals) != 0 {
					t.Fatalf("tier %s with proposals %+v", d.Tier, d.Proposals)
				}
				if got := d.Findings(); len(got) != 1 || got[0].Code != "other_work" {
					t.Fatalf("findings %+v", got)
				}
			} else if len(d.Findings()) != 0 {
				t.Fatalf("findings %+v", d.Findings())
			}
			if tt.proposes && len(d.Proposals) == 0 {
				t.Fatalf("expected normal proposals, got none (tier %s %v)", d.Tier, d.Reasons)
			}
		})
	}
}

// TestISBNProposalNeedsFullMatch: before version 6 a record whose title
// matches vouches for the ISBN even when its authors don't; from 6 the
// record must match the files' title, authors and language (ADR-030).
func TestISBNProposalNeedsFullMatch(t *testing.T) {
	build := func(edit func(*evidence.SourceRecord)) evidence.Package {
		p := tierA()
		p.Calibre.Identifiers = map[string]string{"isbn": "978-0-306-40615-7"}
		edit(&p.Lookups[0].Results[0].Records[0])
		return p
	}
	for _, tt := range []struct {
		name   string
		edit   func(*evidence.SourceRecord)
		v5, v6 bool // an isbn proposal is made
	}{
		{"matching record", func(*evidence.SourceRecord) {}, true, true},
		{"authors differ", func(r *evidence.SourceRecord) { r.Authors = []string{"Someone Else"} }, true, false},
		{"language differs", func(r *evidence.SourceRecord) { r.Languages = []string{"eng"} }, true, false},
		{"record adds a translator", func(r *evidence.SourceRecord) { r.Authors = []string{A, "Klaus Fritz"} }, true, true},
		{"record states no language", func(r *evidence.SourceRecord) { r.Languages = nil }, true, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for version, want := range map[int]bool{5: tt.v5, 6: tt.v6} {
				d, err := decide.AtVersion(t.Context(), build(tt.edit), version)
				if err != nil {
					t.Fatal(err)
				}
				if got := slices.Contains(fields(d.Proposals), "isbn"); got != want {
					t.Errorf("v%d: isbn proposal = %v, want %v (%+v)", version, got, want, d)
				}
			}
		})
	}
}

// TestSealOpenV6 round-trips a front-matter identity and an other-work book
// through Seal and Open at version 6.
func TestSealOpenV6(t *testing.T) {
	wrongSort := "x"
	for name, p := range map[string]evidence.Package{
		"front matter": frontPkg([]evidence.ISBNContext{copyrightISBN(I, evidence.QualifierEbook)}, ok(evidence.SourceDNB, dnbT())),
		"other work": func() evidence.Package {
			p := tierA()
			p.Calibre.Title, p.Calibre.Authors, p.Calibre.TitleSort = "Another Book", []string{"Someone Else"}, &wrongSort
			p.Formats[0].Metadata.Title, p.Formats[0].Metadata.Authors = "Another Book", []string{"Someone Else"}
			return p
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			raw, seal, err := decide.Seal(t.Context(), p)
			if err != nil {
				t.Fatal(err)
			}
			got, d, err := decide.Open(t.Context(), raw, seal)
			if err != nil || got.DecisionVersion != decide.Version || got.Tier != d.Tier || !slices.Equal(got.TierReasons, d.Reasons) {
				t.Fatalf("Open = %+v, %+v, %v", got, d, err)
			}
			bad, badSeal := reseal(t, raw, func(p *evidence.Package) { p.TierReasons = nil })
			if _, _, err := decide.Open(t.Context(), bad, badSeal); !errors.Is(err, decide.ErrMismatch) {
				t.Fatalf("Open without the reasons: %v, want ErrMismatch", err)
			}
			if _, err := findings.Derive(t.Context(), got); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestReasonLabelsV6(t *testing.T) {
	for _, code := range []string{"identity_frontmatter", "frontmatter_isbn_missing", "frontmatter_isbn_ambiguous", "frontmatter_isbn_unconfirmed", "other_work"} {
		if decide.ReasonLabel(code) == code {
			t.Errorf("no label for %s", code)
		}
	}
}
