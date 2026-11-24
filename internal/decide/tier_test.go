package decide_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/decide"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

const (
	I = "9783551551672"
	T = "Harry Potter und der Stein der Weisen"
	A = "J. K. Rowling"
)

func format(name, detected, title string, authors, languages, isbns []string) evidence.FormatEvidence {
	return evidence.FormatEvidence{Format: name, State: evidence.StateOK, Detected: detected, Metadata: &evidence.FileMetadata{
		Title: title, Authors: authors, Languages: languages, ISBNs: isbns,
	}}
}

func epub(title string, authors, languages, isbns []string) evidence.FormatEvidence {
	return format("EPUB", "epub", title, authors, languages, isbns)
}

func rec(id, title string, authors, languages []string, isbns ...string) evidence.SourceRecord {
	return evidence.SourceRecord{ID: id, Title: title, Authors: authors, Languages: languages, ISBNs: isbns}
}

func ok(source string, recs ...evidence.SourceRecord) evidence.SourceResult {
	return evidence.SourceResult{Source: source, Outcome: evidence.SourceOK, Records: recs}
}

func lookup(isbn string, seeds []string, results ...evidence.SourceResult) evidence.Lookup {
	return evidence.Lookup{ISBN: isbn, Seeds: seeds, Results: results}
}

// book is a package whose Calibre record is {T, [A], [deu], isbn I}.
func book(formats []evidence.FormatEvidence, lookups ...evidence.Lookup) evidence.Package {
	return evidence.Package{
		SchemaVersion: 1, LibraryID: "library", BookID: 1, UUID: "u",
		Calibre: evidence.Record{Title: T, Authors: []string{A}, Languages: []string{"deu"}, Identifiers: map[string]string{"isbn": I}},
		Formats: formats, Lookups: lookups,
	}
}

func dnbT() evidence.SourceRecord { return rec("958577617", T, []string{A}, []string{"deu"}, I) }

// tierA is the first TestTiers row: one EPUB and one agreeing DNB record.
func tierA() evidence.Package {
	return book([]evidence.FormatEvidence{epub(T, []string{A}, []string{"de"}, []string{I})},
		lookup(I, []string{evidence.SeedFile}, ok(evidence.SourceDNB, dnbT())))
}

func TestTiers(t *testing.T) {
	long := strings.Repeat("x", 512)
	for _, tt := range []struct {
		name    string
		p       func() evidence.Package
		tier    string
		reasons []string // nil means no reasons
	}{
		{"tier A", tierA, evidence.TierA, nil},
		{"B code agrees", func() evidence.Package {
			p := tierA()
			p.Formats[0].Metadata.Languages = []string{"ger"}
			return p
		}, evidence.TierA, nil},
		{"no records", func() evidence.Package { p := tierA(); p.Lookups = nil; return p }, evidence.TierB, []string{"no_record"}},
		{"records disagree", func() evidence.Package {
			p := tierA()
			p.Lookups[0].Results[0].Records[0].Title = "Other"
			return p
		}, evidence.TierB, []string{"records_disagree"}},
		{"ISBN only in Calibre", func() evidence.Package {
			return book([]evidence.FormatEvidence{epub(T, []string{A}, []string{"de"}, nil)},
				lookup(I, []string{evidence.SeedCalibre}, ok(evidence.SourceDNB, dnbT())))
		}, evidence.TierB, []string{"isbn_missing", "no_record"}},
		{"ISBN only from OCR", func() evidence.Package {
			pdf := format("PDF", "pdf", T, []string{A}, []string{"de"}, nil)
			pdf.ISBNHints = &evidence.ISBNHints{Method: evidence.HintOCR, State: evidence.HintOK, Pages: []int{1}, ISBNs: []string{I}}
			return book([]evidence.FormatEvidence{pdf}, lookup(I, []string{evidence.HintOCR}, ok(evidence.SourceDNB, dnbT())))
		}, evidence.TierB, []string{"isbn_missing", "no_record"}},
		{"ambiguous", func() evidence.Package {
			p := tierA()
			p.Formats[0].Metadata.ISBNs = []string{I, "9780306406157"}
			return p
		}, evidence.TierB, []string{"isbn_ambiguous", "no_record"}},
		{"unreadable second format", func() evidence.Package {
			p := tierA()
			p.Formats = append(p.Formats, evidence.FormatEvidence{Format: "CBZ", State: evidence.StateOK, Detected: "zip"})
			return p
		}, evidence.TierB, []string{"format_unreadable"}},
		{"flagged file title", func() evidence.Package {
			p := tierA()
			p.Formats[0].Metadata.Limits = &evidence.MetadataLimits{Version: 1, Title: true}
			return p
		}, evidence.TierB, []string{"title_incomplete"}},
		{"record title at the cap", func() evidence.Package {
			p := tierA()
			p.Formats[0].Metadata.Title = long
			p.Formats[0].Metadata.Limits = &evidence.MetadataLimits{Version: 1}
			p.Lookups[0].Results[0].Records[0].Title = long
			return p
		}, evidence.TierB, []string{"records_disagree"}},
		{"formats conflict", func() evidence.Package {
			p := tierA()
			p.Formats = append(p.Formats, format("MOBI", "mobi", "Other", []string{A}, []string{"de"}, []string{I}))
			return p
		}, evidence.TierC, []string{"formats_title_conflict"}},
		{"ISBN conflict", func() evidence.Package {
			p := tierA()
			p.Formats = append(p.Formats, format("MOBI", "mobi", T, []string{A}, []string{"de"}, []string{"9780306406157"}))
			return p
		}, evidence.TierC, []string{"formats_isbn_conflict", "no_record"}},
		{"records conflict", func() evidence.Package {
			p := tierA()
			p.Lookups[0].Results = append(p.Lookups[0].Results, ok(evidence.SourceOpenLibrary, rec("/books/OL1M", "Other", nil, nil, I)))
			return p
		}, evidence.TierC, []string{"records_title_conflict"}},
		{"one source, two records", func() evidence.Package {
			p := tierA()
			a, b := rec("1", T, []string{A}, []string{"deu"}, I), rec("2", T, []string{A}, []string{"deu"}, I)
			a.Publisher, b.Publisher = "Salamandra", "Emecé"
			p.Lookups[0].Results = append(p.Lookups[0].Results, ok(evidence.SourceBNE, a, b))
			return p
		}, evidence.TierA, nil},
		{"BNE split on title", func() evidence.Package {
			p := tierA()
			p.Lookups[0].Results = append(p.Lookups[0].Results, ok(evidence.SourceBNE, rec("1", T, []string{A}, []string{"deu"}, I), rec("2", "Other", []string{A}, []string{"deu"}, I)))
			return p
		}, evidence.TierC, []string{"records_title_conflict"}},
		{"BNE split alone", func() evidence.Package {
			p := tierA()
			p.Lookups[0].Results = []evidence.SourceResult{ok(evidence.SourceBNE, rec("1", T, []string{A}, []string{"deu"}, I), rec("2", "Other", []string{A}, []string{"deu"}, I))}
			return p
		}, evidence.TierC, []string{"records_title_conflict"}},
		{"no formats", func() evidence.Package { return book(nil) }, evidence.TierB, []string{"no_formats", "no_record"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d, err := decide.Decide(t.Context(), tt.p())
			if err != nil {
				t.Fatal(err)
			}
			if d.Tier != tt.tier || !slices.Equal(d.Reasons, tt.reasons) {
				t.Fatalf("Decide = %s %v, want %s %v", d.Tier, d.Reasons, tt.tier, tt.reasons)
			}
		})
	}
}

// TestSharedRoot: an Open Library record that shares DNB's root counts once
// for independence, but disagreeing with DNB is still a records conflict.
func TestSharedRoot(t *testing.T) {
	restore := decide.SetSharedRoot(func(r evidence.SourceRecord) string {
		if r.ID == "/books/OL1M" {
			return evidence.SourceDNB
		}
		return ""
	})
	defer restore()
	p := book([]evidence.FormatEvidence{epub("", []string{A}, []string{"de"}, []string{I})},
		lookup(I, []string{"file"}, ok(evidence.SourceDNB, dnbT()), ok(evidence.SourceOpenLibrary, rec("/books/OL1M", T, []string{A}, []string{"deu"}, I))))
	p.Calibre.Title = "Unknown"
	d, err := decide.Decide(t.Context(), p)
	if err != nil || len(d.Proposals) != 0 {
		t.Fatalf("DNB and an Open Library record sharing its root counted as two roots: %+v %v", d.Proposals, err)
	}
	p.Lookups[0].Results[1].Records[0].Title = "Other"
	if d, err := decide.Decide(t.Context(), p); err != nil || d.Tier != evidence.TierC || !slices.Contains(d.Reasons, "records_title_conflict") {
		t.Fatalf("a disagreeing record in DNB's root: %+v %v", d, err)
	}
}

// TestRecordMatchV5: decision version 5 accepts a record that lacks a
// language, lists extra people, states the files' subtitle in its own
// subtitle field, or differs by a trailing parenthetical, a leading English
// article or "&" for "and"; version 4 keeps its stricter match (ADR-030).
func TestRecordMatchV5(t *testing.T) {
	const en = "The Color Purple"
	withRecord := func(file evidence.FormatEvidence, r evidence.SourceRecord) func() evidence.Package {
		return func() evidence.Package {
			return book([]evidence.FormatEvidence{file}, lookup(I, []string{evidence.SeedFile}, ok(evidence.SourceOpenLibrary, r)))
		}
	}
	fileOf := func(title string, authors []string) evidence.FormatEvidence {
		return epub(title, authors, []string{"en"}, []string{I})
	}
	olRec := func(title string, authors, languages []string) evidence.SourceRecord {
		return rec("/books/OL9M", title, authors, languages, I)
	}
	withSubtitle := func(r evidence.SourceRecord, sub string) evidence.SourceRecord { r.Subtitle = sub; return r }
	walker := []string{"Alice Walker"}
	for _, tt := range []struct {
		name string
		p    func() evidence.Package
		v5   string // tier under version 5; version 4 is always B, records_disagree
	}{
		{"record lacks languages", withRecord(fileOf(en, walker), olRec(en, walker, nil)), evidence.TierA},
		{"record lacks languages and authors", withRecord(fileOf(en, walker), olRec(en, nil, nil)), evidence.TierB},
		{"record lists a translator too", withRecord(fileOf(en, walker), olRec(en, []string{"Alice Walker", "Klaus Fritz"}, []string{"eng"})), evidence.TierA},
		{"record misses a file author", withRecord(fileOf(en, []string{"Alice Walker", "Klaus Fritz"}), olRec(en, walker, []string{"eng"})), evidence.TierB},
		{"record names the author differently", withRecord(fileOf(en, []string{"Walker, Alice"}), olRec(en, walker, []string{"eng"})), evidence.TierB},
		{"subtitle from the record's subtitle field", withRecord(fileOf("Working Backwards: Insights from Amazon", walker), withSubtitle(olRec("Working Backwards", walker, []string{"eng"}), "Insights from Amazon")), evidence.TierA},
		{"subtitle the record doesn't state", withRecord(fileOf("Working Backwards: Insights from Amazon", walker), olRec("Working Backwards", walker, []string{"eng"})), evidence.TierB},
		{"another subtitle than the record's", withRecord(fileOf("Series: Book Two", walker), withSubtitle(olRec("Series", walker, []string{"eng"}), "Book One")), evidence.TierB},
		{"trailing year in the file", withRecord(fileOf("Arrowsmith (1925)", walker), olRec("Arrowsmith", walker, []string{"eng"})), evidence.TierA},
		{"trailing series in the record", withRecord(fileOf("Arrowsmith", walker), olRec("Arrowsmith (Signet Classics)", walker, []string{"eng"})), evidence.TierA},
		{"volume in parentheses", withRecord(fileOf("A History of the Crusades (Vol. 1)", walker), olRec("A History of the Crusades", walker, []string{"eng"})), evidence.TierB},
		{"roman volume in parentheses", withRecord(fileOf("A History of the Crusades (III)", walker), olRec("A History of the Crusades", walker, []string{"eng"})), evidence.TierB},
		{"leading article dropped by the record", withRecord(fileOf(en, walker), olRec("Color Purple", walker, []string{"eng"})), evidence.TierA},
		{"leading article a", withRecord(fileOf("Clockwork Orange", walker), olRec("A Clockwork Orange", walker, []string{"eng"})), evidence.TierA},
		{"ampersand", withRecord(fileOf("Crime & Punishment", walker), olRec("Crime and Punishment", walker, []string{"eng"})), evidence.TierA},
		{"another title", withRecord(fileOf(en, walker), olRec("Meridian", walker, []string{"eng"})), evidence.TierB},
		{"record in another language", withRecord(fileOf(en, walker), olRec(en, walker, []string{"fre"})), evidence.TierB},
		{"band in parentheses", withRecord(fileOf("Der Zauberberg (Band 2)", walker), olRec("Der Zauberberg", walker, []string{"eng"})), evidence.TierB},
		{"teil in parentheses", withRecord(fileOf("Faust (Teil 1)", walker), olRec("Faust", walker, []string{"eng"})), evidence.TierB},
		{"livre in parentheses", withRecord(fileOf("Les Misérables (Livre II)", walker), olRec("Les Misérables", walker, []string{"eng"})), evidence.TierB},
		{"number sign in parentheses", withRecord(fileOf("Leviathan Wakes (Expanse #1)", walker), olRec("Leviathan Wakes", walker, []string{"eng"})), evidence.TierB},
		{"t. in parentheses", withRecord(fileOf("Histoire de France (T. 1)", walker), olRec("Histoire de France", walker, []string{"eng"})), evidence.TierB},
		{"pt. in parentheses", withRecord(fileOf("The Histories (Pt. 1)", walker), olRec("The Histories", walker, []string{"eng"})), evidence.TierB},
		{"bare number in parentheses", withRecord(fileOf("The Histories (2)", walker), olRec("The Histories", walker, []string{"eng"})), evidence.TierB},
		{"nested parentheses", withRecord(fileOf("Arrowsmith (Signet (Classics))", walker), olRec("Arrowsmith", walker, []string{"eng"})), evidence.TierB},
		{"leading article an", withRecord(fileOf("An Essay on Man", walker), olRec("Essay on Man", walker, []string{"eng"})), evidence.TierA},
		{"record languages undetermined", withRecord(fileOf(en, walker), olRec(en, walker, []string{"und"})), evidence.TierB},
		{"record languages blank", withRecord(fileOf(en, walker), olRec(en, walker, []string{"", " "})), evidence.TierB},
		{"title and subtitle at the cap", func() evidence.Package {
			title, sub := strings.Repeat("x", 255), strings.Repeat("y", 254)
			f := fileOf(title+": "+sub, walker)
			f.Metadata.Limits = &evidence.MetadataLimits{Version: 1}
			return withRecord(f, withSubtitle(olRec(title, walker, []string{"eng"}), sub))()
		}, evidence.TierB},
		{"everything at once", withRecord(fileOf("The Color Purple (Penguin Modern Classics)", walker), olRec("Color Purple", []string{"Alice Walker", "Sapphire"}, nil)), evidence.TierA},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d4, err := decide.AtVersion(t.Context(), tt.p(), 4)
			if err != nil || d4.Tier != evidence.TierB || !slices.Equal(d4.Reasons, []string{"records_disagree"}) {
				t.Fatalf("version 4 = %s %v %v, want B [records_disagree]", d4.Tier, d4.Reasons, err)
			}
			d5, err := decide.AtVersion(t.Context(), tt.p(), 5)
			if err != nil {
				t.Fatal(err)
			}
			want := []string(nil)
			if tt.v5 == evidence.TierB {
				want = []string{"records_disagree"}
			}
			if d5.Tier != tt.v5 || !slices.Equal(d5.Reasons, want) {
				t.Fatalf("version 5 = %s %v, want %s %v", d5.Tier, d5.Reasons, tt.v5, want)
			}
		})
	}
}

// TestRecordConflictV5: the relaxed match never hides two records that
// disagree with each other; that stays Tier C.
func TestRecordConflictV5(t *testing.T) {
	p := tierA()
	p.Lookups[0].Results = append(p.Lookups[0].Results, ok(evidence.SourceOpenLibrary, rec("/books/OL1M", T+" (Band 1)", []string{A}, nil, I)))
	d, err := decide.AtVersion(t.Context(), p, 5)
	if err != nil || d.Tier != evidence.TierC || !slices.Contains(d.Reasons, "records_title_conflict") {
		t.Fatalf("version 5 = %+v %v, want C with records_title_conflict", d, err)
	}
	// A record without a language can't vouch for the files' language when
	// another record for the ISBN states a different one.
	walker, en := []string{"Alice Walker"}, "The Color Purple"
	q := book([]evidence.FormatEvidence{epub(en, walker, []string{"en"}, []string{I})},
		lookup(I, []string{evidence.SeedFile}, ok(evidence.SourceOpenLibrary,
			rec("/books/OL1M", en, walker, nil, I), rec("/books/OL2M", en, walker, []string{"fre"}, I))))
	if d, err := decide.AtVersion(t.Context(), q, 5); err != nil || d.Tier != evidence.TierB || !slices.Equal(d.Reasons, []string{"records_disagree"}) {
		t.Fatalf("version 5 with a French record beside one without a language = %s %v %v, want B [records_disagree]", d.Tier, d.Reasons, err)
	}
}
