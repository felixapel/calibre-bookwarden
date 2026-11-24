package decide_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/decide"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

func prop(field string, current, proposed, roots []string) evidence.Proposal {
	return evidence.Proposal{Field: field, Current: current, Proposed: proposed, Roots: roots}
}

func equalProposals(a, b []evidence.Proposal) bool {
	return slices.EqualFunc(a, b, func(x, y evidence.Proposal) bool {
		return x.Field == y.Field && slices.Equal(x.Current, y.Current) && slices.Equal(x.Proposed, y.Proposed) && slices.Equal(x.Roots, y.Roots)
	})
}

// calibreTitle is tierA with Calibre's title replaced: the file and DNB say T.
func calibreTitle(title string) func() evidence.Package {
	return func() evidence.Package {
		p := tierA()
		p.Calibre.Title = title
		return p
	}
}

// calibreAuthors is tierA with Calibre's authors replaced: the file and DNB say [A].
func calibreAuthors(names ...string) func() evidence.Package {
	return func() evidence.Package {
		p := tierA()
		p.Calibre.Authors = names
		return p
	}
}

// sourcesAuthors is tierA with the file's and DNB's authors replaced: Calibre says [A].
func sourcesAuthors(names ...string) func() evidence.Package {
	return func() evidence.Package {
		p := tierA()
		p.Formats[0].Metadata.Authors = names
		p.Lookups[0].Results[0].Records[0].Authors = names
		return p
	}
}

func TestProposals(t *testing.T) {
	noTitle := func() evidence.FormatEvidence { return epub("", []string{A}, []string{"de"}, []string{I}) }
	olT := func() evidence.SourceRecord { return rec("/books/OL1M", T, []string{A}, []string{"deu"}, I) }
	names := make([]string, 16)
	for i := range names {
		names[i] = "Author " + string(rune('A'+i))
	}
	for _, tt := range []struct {
		name string
		p    func() evidence.Package
		want []evidence.Proposal
	}{
		{"language from file + DNB", func() evidence.Package {
			p := tierA()
			p.Calibre.Languages = nil
			return p
		}, []evidence.Proposal{prop("languages", nil, []string{"deu"}, []string{"dnb", "file"})}},
		{"title from two sources, no file title", func() evidence.Package {
			p := book([]evidence.FormatEvidence{noTitle()}, lookup(I, []string{"file"}, ok(evidence.SourceDNB, dnbT()), ok(evidence.SourceOpenLibrary, olT())))
			p.Calibre.Title = "Unknown"
			return p
		}, []evidence.Proposal{prop("title", []string{"Unknown"}, []string{T}, []string{"dnb", "openlibrary"})}},
		{"file vetoes", func() evidence.Package {
			p := book([]evidence.FormatEvidence{epub("X", []string{A}, []string{"de"}, []string{I})},
				lookup(I, []string{"file"}, ok(evidence.SourceDNB, dnbT()), ok(evidence.SourceOpenLibrary, olT())))
			p.Calibre.Title = "X"
			return p
		}, nil},
		{"two externals only", func() evidence.Package {
			gb := olT()
			gb.ID = "gb1"
			p := book([]evidence.FormatEvidence{noTitle()}, lookup(I, []string{"file"}, ok(evidence.SourceOpenLibrary, olT()), ok(evidence.SourceGoogleBooks, gb)))
			p.Calibre.Title = "Unknown"
			return p
		}, nil},
		{"case only", func() evidence.Package {
			p := tierA()
			p.Calibre.Title = strings.ToLower(T)
			return p
		}, nil},
		{"display precedence", func() evidence.Package {
			p := tierA()
			p.Formats[0].Metadata.Title = strings.ToLower(T)
			p.Calibre.Title = "Harry Potter 1"
			return p
		}, []evidence.Proposal{prop("title", []string{"Harry Potter 1"}, []string{T}, []string{"dnb", "file"})}},
		{"title: Calibre has a subtitle", calibreTitle(T + ": Roman"), nil},
		{"title: Calibre has a leading article", calibreTitle("Der " + T), nil},
		{"title: Calibre has words on both sides", calibreTitle("Neu: " + T + " (Band 1)"), nil},
		{"title: sources add words", calibreTitle("Stein der Weisen"),
			[]evidence.Proposal{prop("title", []string{"Stein der Weisen"}, []string{T}, []string{"dnb", "file"})}},
		{"title: punctuation only", calibreTitle("Harry Potter, und der Stein der Weisen"),
			[]evidence.Proposal{prop("title", []string{"Harry Potter, und der Stein der Weisen"}, []string{T}, []string{"dnb", "file"})}},
		{"title: a typo fix that loses the subtitle", calibreTitle("Hary Potter und der Stein der Weisen: Roman"),
			[]evidence.Proposal{prop("title", []string{"Hary Potter und der Stein der Weisen: Roman"}, []string{T}, []string{"dnb", "file"})}},
		{"authors: Calibre lists a translator too", calibreAuthors(A, "Klaus Fritz"), nil},
		{"authors: another form of the same name", calibreAuthors("Joanne K. Rowling"), nil},
		{"authors: another person", calibreAuthors("Joanne Smith"),
			[]evidence.Proposal{prop("authors", []string{"Joanne Smith"}, []string{A}, []string{"dnb", "file"})}},
		{"authors: Calibre's name inverted", calibreAuthors("Rowling, J. K."),
			[]evidence.Proposal{prop("authors", []string{"Rowling, J. K."}, []string{A}, []string{"dnb", "file"})}},
		{"authors: sources add a co-author", sourcesAuthors(A, "Klaus Fritz"),
			[]evidence.Proposal{prop("authors", []string{A}, []string{A, "Klaus Fritz"}, []string{"dnb", "file"})}},
		{"authors: sources add a co-author with the same surname", func() evidence.Package {
			p := sourcesAuthors("Will Durant", "Ariel Durant")()
			p.Calibre.Authors = []string{"Will Durant"}
			return p
		}, []evidence.Proposal{prop("authors", []string{"Will Durant"}, []string{"Will Durant", "Ariel Durant"}, []string{"dnb", "file"})}},
		{"year-only date", func() evidence.Package {
			p := tierA()
			p.Formats[0].Metadata.Date = "2000"
			p.Lookups[0].Results[0].Records[0].Date = "2000"
			return p
		}, nil},
		{"full date", func() evidence.Package {
			p := tierA()
			p.Formats[0].Metadata.Date = "2000-07-26"
			gb := rec("gb1", T, []string{A}, []string{"deu"}, I)
			gb.Date = "2000-07-26"
			p.Lookups[0].Results = append(p.Lookups[0].Results, ok(evidence.SourceGoogleBooks, gb))
			// Mid-month: from version 11 an undo must be able to restore it.
			p.Calibre.PubDate = "2001-03-14T00:00:00+00:00"
			return p
		}, []evidence.Proposal{prop("pubdate", []string{"2001-03-14T00:00:00+00:00"}, []string{"2000-07-26"}, []string{"file", "googlebooks"})}},
		{"isbn", func() evidence.Package {
			p := tierA()
			p.Calibre.Identifiers = map[string]string{"isbn": "978-0-306-40615-7"}
			return p
		}, []evidence.Proposal{prop("isbn", []string{"978-0-306-40615-7"}, []string{I}, []string{"dnb", "file"})}},
		{"isbn already right", func() evidence.Package {
			p := tierA()
			p.Calibre.Identifiers = map[string]string{"isbn": "978-3-551-55167-2"}
			return p
		}, nil},
		{"isbn uncorroborated", func() evidence.Package {
			p := tierA()
			p.Calibre.Identifiers = map[string]string{"isbn": "978-0-306-40615-7"}
			p.Lookups[0].Results[0].Records[0].Title = "Other"
			return p
		}, nil},
		{"records for Calibre's ISBN", func() evidence.Package {
			p := book([]evidence.FormatEvidence{epub(T, []string{A}, nil, nil)}, lookup(I, []string{"calibre"}, ok(evidence.SourceDNB, dnbT())))
			p.Calibre.Languages = nil
			return p
		}, nil},
		{"two DNB records are one root", func() evidence.Package {
			second := dnbT()
			second.ID = "2"
			p := book([]evidence.FormatEvidence{noTitle()}, lookup(I, []string{"file"}, ok(evidence.SourceDNB, dnbT(), second)))
			p.Calibre.Title = "Unknown"
			return p
		}, nil},
		{"split BNE vetoes the publisher", func() evidence.Package {
			p := tierA()
			p.Formats[0].Metadata.Publisher = "Salamandra"
			a, b, o := rec("1", T, []string{A}, []string{"deu"}, I), rec("2", T, []string{A}, []string{"deu"}, I), olT()
			a.Publisher, b.Publisher, o.Publisher = "Salamandra", "Emecé", "Salamandra"
			p.Lookups[0].Results = append(p.Lookups[0].Results, ok(evidence.SourceBNE, a, b), ok(evidence.SourceOpenLibrary, o))
			return p
		}, nil},
		{"author list at the cap", func() evidence.Package {
			p := tierA()
			p.Formats[0].Metadata.Authors = names
			p.Lookups[0].Results[0].Records[0].Authors = names
			return p
		}, nil},
		{"tier C", func() evidence.Package {
			p := tierA()
			p.Calibre.Languages = nil
			p.Lookups[0].Results = append(p.Lookups[0].Results, ok(evidence.SourceOpenLibrary, rec("/books/OL1M", "Other", nil, nil, I)))
			return p
		}, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d, err := decide.Decide(t.Context(), tt.p())
			if err != nil {
				t.Fatal(err)
			}
			if !equalProposals(d.Proposals, tt.want) {
				t.Fatalf("proposals (tier %s %v):\n got %+v\nwant %+v", d.Tier, d.Reasons, d.Proposals, tt.want)
			}
		})
	}
}

func FuzzDecide(f *testing.F) {
	// tierA has no extraction; the v8 books carry one on their format, the
	// second fills the ISBN from the text alone.
	for _, p := range []evidence.Package{
		tierA(),
		v8Book(baseFacts(), nil),
		v8Book(factSet{"isbn": isbnFact(av(I))}, noISBNs),
	} {
		seed, err := json.Marshal(p)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		var p evidence.Package
		if json.Unmarshal(raw, &p) != nil {
			return
		}
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("Decide panicked: %v", r)
			}
		}()
		d, err := decide.Decide(t.Context(), p)
		if err != nil {
			return
		}
		if d.Tier != evidence.TierA && d.Tier != evidence.TierB && d.Tier != evidence.TierC {
			t.Fatalf("tier %q", d.Tier)
		}
		if d.Tier == evidence.TierC && len(d.Proposals) > 0 {
			t.Fatalf("Tier C with proposals: %+v", d.Proposals)
		}
		// A proposal has two roots, one of them an authority (DNB, BNE or
		// the text), or the text alone fills or replaces an empty or junk
		// value (ADR-036 section 3.3).
		authority := func(r string) bool {
			return r == evidence.SourceDNB || r == evidence.SourceBNE || r == evidence.RootText
		}
		for _, pr := range d.Proposals {
			textAlone := slices.Equal(pr.Roots, []string{evidence.RootText}) && decide.CalibreEmptyOrJunk(p, pr.Field)
			twoRoots := len(pr.Roots) >= 2 && slices.ContainsFunc(pr.Roots, authority)
			if !twoRoots && !textAlone || len(pr.Proposed) == 0 {
				t.Fatalf("proposal %+v", pr)
			}
		}
		again, err := decide.Decide(t.Context(), p)
		if err != nil || again.Tier != d.Tier || !slices.Equal(again.Reasons, d.Reasons) || !equalProposals(again.Proposals, d.Proposals) {
			t.Fatalf("not deterministic: %+v vs %+v (%v)", d, again, err)
		}
	})
}
