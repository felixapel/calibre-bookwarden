package decide_test

import (
	"maps"
	"slices"
	"strings"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

// Synthetic packages for decision version 8 (ADR-036): books whose EPUB
// formats carry a sealed extraction block. Nothing here is real book data.

const (
	printedTitle = "Printed Title"
	printedSub   = "A Printed Subtitle"
	printedMan   = "Ann Author"
	printedHouse = "Acme Press"
)

var voices = []string{"glm", "luna"}

func av(value string) evidence.AgreedValue { return evidence.AgreedValue{Value: value} }

func person(name, role string) evidence.AgreedValue {
	return evidence.AgreedValue{Value: name, Role: role}
}

func labelled(isbn, label string) evidence.AgreedValue {
	return evidence.AgreedValue{Value: isbn, Label: label}
}

// agreedFact is a fact both extractors state.
func agreedFact(key string, vals ...evidence.AgreedValue) evidence.AgreedFact {
	return evidence.AgreedFact{Key: key, Outcome: evidence.AgreeAgreed, Values: vals, Voices: voices}
}

// isbnFact is an ISBN fact the parser and one extractor agree on, as M4
// agrees patterned fields (design section 7). A value with Role "back" is
// grounded in the excerpt's back unit (an ad); the marker is test-only.
func isbnFact(vals ...evidence.AgreedValue) evidence.AgreedFact {
	return evidence.AgreedFact{Key: "isbn", Outcome: evidence.AgreeAgreed, Values: vals, Voices: []string{"luna", "parser"}}
}

// frontISBNs is an ISBN fact whose extractors agree but no parser grounds.
func extractorOnlyISBN(vals ...evidence.AgreedValue) evidence.AgreedFact {
	return evidence.AgreedFact{Key: "isbn", Outcome: evidence.AgreeAgreed, Values: vals, Voices: voices}
}

// singleFact is a fact one extractor states.
func singleFact(key string, vals ...evidence.AgreedValue) evidence.AgreedFact {
	return evidence.AgreedFact{Key: key, Outcome: evidence.AgreeSingle, Values: vals, Voices: []string{"luna"}}
}

// disagreeFact is a fact the extractors state differently.
func disagreeFact(key string) evidence.AgreedFact {
	return evidence.AgreedFact{Key: key, Outcome: evidence.AgreeDisagree, Voices: voices}
}

// factSet is a book's agreed facts by key.
type factSet map[string]evidence.AgreedFact

// baseFacts is what the printed pages of the base book say.
func baseFacts() factSet {
	fs := factSet{}
	for _, f := range []evidence.AgreedFact{
		agreedFact("title", av(printedTitle)),
		agreedFact("contributor", person(printedMan, "author")),
		agreedFact("imprint", av(printedHouse)),
		isbnFact(av(I)),
	} {
		fs[f.Key] = f
	}
	return fs
}

func (fs factSet) with(facts ...evidence.AgreedFact) factSet {
	out := maps.Clone(fs)
	for _, f := range facts {
		out[f.Key] = f
	}
	return out
}

// extraction seals fs as the extraction of one excerpt: both extractors
// answered, and every value of every voice has a grounded claim.
func (fs factSet) extraction() (*evidence.Excerpt, *evidence.Extraction) {
	b := evidence.NewExcerptBuilder(evidence.ExcerptEPUBText)
	b.Add(1, evidence.LandmarkTitle, strings.Repeat("x", 200))
	b.Add(2, evidence.LandmarkBack, strings.Repeat("y", 200))
	ex := b.Excerpt()
	ex.Text = ""
	x := &evidence.Extraction{
		PromptVersion: "extract-2", ExtractVersion: 2, ExcerptSHA256: ex.SHA256,
		Extractors: []evidence.ExtractorRun{
			{Name: "luna", Family: "openai", Model: "m", State: evidence.ExtractOK, RawSHA256: strings.Repeat("ab", 32)},
			{Name: "glm", Family: "zhipu", Model: "m", State: evidence.ExtractOK, RawSHA256: strings.Repeat("cd", 32)},
		},
	}
	for _, key := range slices.Sorted(maps.Keys(fs)) { // extract.Agree sorts facts by key
		f := fs[key]
		x.Agreed = append(x.Agreed, f)
		for _, v := range f.Values {
			for _, voice := range f.Voices {
				c := evidence.ExtractedClaim{Value: v.Value, Role: v.Role, Label: v.Label, Source: voice, Status: "grounded", Unit: 1, Start: 0, End: 1}
				if f.Key == "isbn" {
					c.Role = ""
					if v.Role == "back" {
						c.Unit, c.Start, c.End = 2, ex.Units[1].Start, ex.Units[1].Start+1
					}
				}
				switch {
				case strings.HasPrefix(f.Key, "year/"):
					c.Field, c.Label = "year", strings.TrimPrefix(f.Key, "year/")
				default:
					c.Field = f.Key
				}
				x.Claims = append(x.Claims, c)
			}
		}
	}
	return ex, x
}

// stamp gives every format that has no extraction yet fs as its extraction.
func (fs factSet) stamp(p *evidence.Package) {
	p.SchemaVersion = evidence.SchemaVersion
	for i := range p.Formats {
		if p.Formats[i].Extraction != nil {
			continue
		}
		p.Formats[i].Excerpt, p.Formats[i].Extraction = fs.extraction()
	}
}

// v8Base is a book whose file, DNB and printed pages agree on the title,
// authors, publisher and ISBN, while Calibre states others.
func v8Base() evidence.Package {
	file := epub(printedTitle, []string{printedMan}, []string{"de"}, []string{I})
	file.Metadata.Publisher = printedHouse
	dnb := rec("958577617", printedTitle, []string{printedMan}, []string{"deu"}, I)
	dnb.Publisher = printedHouse
	p := book([]evidence.FormatEvidence{file}, lookup(I, []string{evidence.SeedFile}, ok(evidence.SourceDNB, dnb)))
	p.Calibre.Title, p.Calibre.Authors, p.Calibre.Publisher = "Old Title", []string{printedMan}, ""
	p.Calibre.Identifiers = map[string]string{"isbn": I2}
	return p
}

// v8Book is v8Base after mutate, with the printed pages facts on every
// format that has no extraction.
func v8Book(facts factSet, mutate func(*evidence.Package)) evidence.Package {
	p := v8Base()
	if mutate != nil {
		mutate(&p)
	}
	facts.stamp(&p)
	return p
}

// withoutText is p with every extraction and excerpt removed.
func withoutText(p evidence.Package) evidence.Package {
	out := p
	out.Formats = nil
	for _, f := range p.Formats {
		f.Extraction, f.Excerpt = nil, nil
		out.Formats = append(out.Formats, f)
	}
	return out
}
