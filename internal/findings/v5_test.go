package findings

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

var v5Codes = []string{"junk_title", "title_noise", "joined_authors", "junk_publisher"}

// deferredCodes are planned but not built; none may be catalogued or derived.
var deferredCodes = []string{"placeholder_pubdate", "role_as_author"}

// v5Only keeps the findings version 5 added.
func v5Only(fs []evidence.Finding) []evidence.Finding {
	var out []evidence.Finding
	for _, f := range fs {
		if slices.Contains(v5Codes, f.Code) {
			out = append(out, f)
		}
	}
	return out
}

func TestVersionFive(t *testing.T) {
	if Version < 5 {
		t.Fatalf("Version = %d, want at least 5", Version)
	}
	ctx := context.Background()
	if _, err := DeriveVersion(ctx, goodPackage(), Version+1); !errors.Is(err, ErrUnknownVersion) {
		t.Fatalf("version %d: %v", Version+1, err)
	}
	for _, v := range []int{1, 2, 3, 4, 5} {
		if _, err := DeriveVersion(ctx, goodPackage(), v); err != nil {
			t.Fatalf("version %d: %v", v, err)
		}
	}
	// A sealed package of each version opens, and one claiming findings it
	// can't derive doesn't.
	p := goodPackage()
	p.Calibre.Title, p.Formats[0].Metadata.Title = "Untitled", "Untitled"
	for _, v := range []int{4, 5} {
		p.FindingsVersion = v
		fs, err := DeriveVersion(ctx, p, v)
		if err != nil {
			t.Fatal(err)
		}
		p.Findings = fs
		raw, hash, err := evidence.Seal(p)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Open(ctx, raw, hash); err != nil {
			t.Fatalf("version %d: %v", v, err)
		}
	}
	p.FindingsVersion = 4
	p.Findings, _ = DeriveVersion(ctx, p, 5)
	raw, hash, err := evidence.Seal(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ctx, raw, hash); !errors.Is(err, ErrMismatch) {
		t.Fatalf("a version-4 package with a version-5 finding opened: %v", err)
	}
}

func TestV5CodesAreCatalogued(t *testing.T) {
	seen := map[string]Definition{}
	for _, d := range Catalog() {
		seen[d.Code] = d
	}
	for _, code := range deferredCodes {
		if _, ok := seen[code]; ok {
			t.Errorf("%s is catalogued but deferred", code)
		}
	}
	for _, code := range v5Codes {
		d, ok := seen[code]
		if !ok || d.Severity != "info" && d.Severity != "warning" || d.Label == "" {
			t.Errorf("%s: %+v", code, d)
		}
	}
}

func TestJunkTitle(t *testing.T) {
	for _, tt := range []struct{ title, want string }{
		{"Microsoft Word - Chapter 1.doc", "word_artefact"},
		{"microsoft word - Document1", "word_artefact"},
		{"Microsoft Word – thesis_final.docx", "word_artefact"},
		{"Moby Dick.pdf", "file_extension"},
		{"report_final.DOCX", "file_extension"},
		{"scan0042.PDF", "file_extension"},
		{"layout.indd", "file_extension"},
		{"x.odt", "file_extension"},
		{"y.rtf", "file_extension"},
		{"z.mobi", "file_extension"},
		{"w.azw3", "file_extension"},
		{"v.doc", "file_extension"},
		{"book.epub", "file_extension"},
		{"  Padded Title.pdf  ", "file_extension"},
		{"Untitled", "untitled"},
		{"untitled document", "untitled"},
		{"Untitled Document", "untitled"},
		{"untitled-1", "untitled"},
		{"Untitled_2", "untitled"},
		{"UNTITLED 3", "untitled"},
		{"20190312_153045", "number_only"},
		{"0001_000123", "number_only"},
		{"2019-03-12-153045", "number_only"},
		{"123456_1", "number_only"}, // a run of exactly six digits

		// Real titles that look a little like artefacts.
		{"2001: A Space Odyssey", ""},
		{"1984", ""},
		{"Catch-22", ""},
		{"Fahrenheit 451", ""},
		{"S.", ""},
		{"V.", ""},
		{"11/22/63", ""},
		{"12345_1", ""}, // a run of five digits is too short
		{"123456", ""},  // digits only
		{"README.txt", ""},
		{"chapter1.txt", ""},
		{"000123", ""},
		{"100000", ""},
		{"1000000", ""},
		{"9780306406157", ""},
		{"2001-2010", ""},
		{"Space 1999", ""},
		{"100,000", ""},
		{"12345", ""},
		{"Untitled Poems", ""},
		{"Untitled 1: A Novel", ""},
		{"The PDF Reference", ""},
		{"Learning .NET", ""},
		{"How to Open .doc Files", ""},
		{"Microsoft Word 2019 Step by Step", ""},
		{"Microsoft Word - The Missing Manual", ""},
		{"Microsoft Word - Kapitel 3", ""},
		{"Microsoft Word", ""},
		{"Dr. Who.", ""},
		{"", ""},
		{".pdf", ""},
		{strings.Repeat("a", 600) + ".pdf", ""},
	} {
		t.Run(tt.title, func(t *testing.T) {
			if got := junkTitleReason(tt.title); got != tt.want {
				t.Fatalf("junkTitleReason(%q) = %q, want %q", tt.title, got, tt.want)
			}
		})
	}
}

func TestTitleNoise(t *testing.T) {
	mann := []string{"Thomas Mann"}
	for _, tt := range []struct {
		title   string
		authors []string
		want    []string
	}{
		{"Emma (Penguin Classics)", nil, []string{"series_imprint"}},
		{"Pride and Prejudice (Oxford World’s Classics)", nil, []string{"series_imprint"}},
		{"Meditations (Everyman's Library)", nil, []string{"series_imprint"}},
		{"Invisible Man (Vintage International)", nil, []string{"series_imprint"}},
		{"1984 (Signet Classics)", nil, []string{"series_imprint"}},
		{"Emma (penguin classics)", nil, []string{"series_imprint"}},
		{"Dubliners (Penguin Modern Classics)", nil, []string{"series_imprint"}},

		{"The Trial (Translated by Willa Muir)", nil, []string{"translator_credit"}},
		{"Der Prozess - Übersetzt von Max Brod", nil, []string{"translator_credit"}},
		{"Cien años de soledad: Traducido por Gregory Rabassa", nil, []string{"translator_credit"}},
		{"Anna Karenina / Translated by A. J. Hubback", nil, []string{"translator_credit"}},
		{"Anna Karenina [translated by Constance Garnett]", nil, []string{"translator_credit"}},
		{"Der Zauberberg. Translated by Helen Lowe-Porter", nil, nil},
		{"War and Peace, translated by Louise Maude and Aylmer Maude", nil, nil},
		{"Lost Words Translated By Machine Learning", nil, nil},

		{"Mann, Thomas - Der Zauberberg", mann, []string{"author_prefix"}},
		{"Mann, Thomas – Der Zauberberg", mann, []string{"author_prefix"}},
		{"mann,  thomas - Der Zauberberg", mann, []string{"author_prefix"}},
		{"Mann, Thomas - Der Zauberberg", []string{"Mann, Thomas"}, []string{"author_prefix"}},
		{"van Beethoven, Ludwig - Letters", []string{"Ludwig van Beethoven"}, []string{"author_prefix"}},
		{"Mann, Thomas - Der Zauberberg", []string{"Heinrich Mann", "Thomas Mann"}, []string{"author_prefix"}},

		{"Emma (Annotated)", nil, []string{"edition_credit"}},
		{"Dracula (20th Anniversary Edition)", nil, []string{"edition_credit"}},
		{"Dune [Deluxe Edition]", nil, []string{"edition_credit"}},
		{"Ulysses (Kindle Edition)", nil, []string{"edition_credit"}},
		{"Emma (Illustrated Edition)", nil, []string{"edition_credit"}},
		{"The Hobbit (Annotated Edition)", nil, []string{"edition_credit"}},
		{"Gatsby (75th Anniversary)", nil, []string{"edition_credit"}},

		{"Mann, Thomas - Der Zauberberg (Penguin Classics)", mann, []string{"series_imprint", "author_prefix"}},
		{"Emma (Penguin Classics) (Annotated)", nil, []string{"series_imprint", "edition_credit"}},

		// Titles that merely resemble noise.
		{"Penguin Classics Guide", nil, nil},
		{"The Everyman's Library Story", nil, nil},
		{"Dune (Dune Chronicles #1)", nil, nil},
		{"Emma (Penguin Classics Deluxe Edition)", nil, nil},
		{"The Annotated Alice", nil, nil},
		{"20th Anniversary Edition Planning", nil, nil},
		{"Emma Annotated", nil, nil},
		{"Fifty Shades (Anniversary Party)", nil, nil},
		{"Hamlet (Folger Shakespeare Library)", nil, nil},
		{"Alice (Annotated by Martin Gardner)", nil, nil},
		{"Translated by Heart", nil, nil},
		{"Lost in Translation", nil, nil},
		{"The Translated Man", nil, nil},
		{"Translated by", nil, nil},
		{"Translated by the sea", nil, nil},
		{"Mann, Thomas - Der Zauberberg", nil, nil},
		{"Mann, Thomas - Der Zauberberg", []string{"Heinrich Mann"}, nil},
		{"Mann, Thomas", mann, nil},
		{"Mann, Thomas - ", mann, nil},
		{"Mann, Thomas: Der Zauberberg", mann, nil},
		{"Thomas Mann - Der Zauberberg", mann, nil},
		{"Der Zauberberg", mann, nil},
		{"", mann, nil},
		{strings.Repeat("a", 600) + " (Penguin Classics)", nil, nil},
	} {
		t.Run(tt.title, func(t *testing.T) {
			got := titleNoiseReasons(tt.title, tt.authors)
			if !slices.Equal(got, tt.want) {
				t.Fatalf("titleNoiseReasons(%q, %v) = %v, want %v", tt.title, tt.authors, got, tt.want)
			}
		})
	}
}

func TestJoinedAuthor(t *testing.T) {
	for _, tt := range []struct{ entry, publisher, want string }{
		{"John Smith and Jane Doe", "", "and"},
		{"Hans Müller und Eva Weber", "", "und"},
		{"Jean Dupont et Marie Curie", "", "et"},
		{"Juan Pérez y María López", "", "y"},
		{"John Smith & Jane Doe", "", "ampersand"},
		{"A. Smith; B. Jones", "", "semicolon"},
		{"Adam Ant; Bea Bee", "", "semicolon"},
		{"Smith, John & Doe, Jane", "", "ampersand"},
		{"Dr. John Smith and Prof. Jane Doe", "", "and"},
		{"Hans Müller und Eva Weber und Karl Lang", "", "und"},
		{"A. Smith; B. Jones; C. Brown", "", "semicolon"},
		{"Mary Shelley and Percy Bysshe Shelley", "", "and"},

		// Publishers and companies in one slot.
		{"Simon & Schuster", "", ""},
		{"Procter & Gamble", "", ""},
		{"Barnes & Noble", "", ""},
		{"Harper & Row", "", ""},
		{"Hodder & Stoughton", "", ""},
		{"Black & Decker", "", ""},
		{"Farrar, Straus & Giroux", "", ""},
		{"simon & schuster", "", ""},
		{"Smith & Co.", "", ""},
		{"Smith & Sons", "", ""},
		{"Wiley & Sons", "", ""},
		{"Smith & Jones", "Smith & Jones", ""},
		{"Smith & Jones", "", ""},
		{"Strunk & White", "", ""},
		{"Taylor & Francis", "", ""},
		{"Faber & Faber", "", ""},
		{"Thames & Hudson", "", ""},
		{"Rowman & Littlefield", "", ""},
		{"Simon & Schuster UK", "", ""},
		{"Ramón del Valle-Inclán y de la Peña", "", ""},
		{"Emilia Pardo Bazán y de la Rúa", "", ""},
		{"Hans Weber und von Braun Wernher", "", ""},
		{"John Smith and van Gogh Vincent", "", ""},
		{"Jean Dupont et du Pont Marie", "", ""},

		// One person whose name holds a joining word.
		{"José Ortega y Gasset", "", ""},
		{"Santiago Ramón y Cajal", "", ""},
		{"Smith and Jones", "", ""},
		{"Pride and Prejudice", "", ""},
		{"Sense and Sensibility", "", ""},

		// Not two names, or too uncertain.
		{"John Smith and others", "", ""},
		{"John Smith et al.", "", ""},
		{"John Smith and jane doe", "", ""},
		{"John Smith & 12 Monkeys", "", ""},
		{"John Smith and ", "", ""},
		{"; Smith", "", ""},
		{"John Smith and Mary Ann Elizabeth Jane Doe", "", ""},
		{"Smith, John, Jr. & Doe, Jane", "", ""},
		{"Smith, John", "", ""},
		{"John Smith, Jr.", "", ""},
		{"Alexandre Dumas, Fils", "", ""},
		{"Plato", "", ""},
		{"", "", ""},
		{"AT&T", "", ""},
		{"Smith&Jones", "", ""},
		{strings.Repeat("Abc ", 200) + "and Jane Doe", "", ""},
	} {
		t.Run(tt.entry, func(t *testing.T) {
			if got := joinedAuthorReason(tt.entry, tt.publisher); got != tt.want {
				t.Fatalf("joinedAuthorReason(%q, %q) = %q, want %q", tt.entry, tt.publisher, got, tt.want)
			}
		})
	}
}

func TestJunkPublisher(t *testing.T) {
	for _, tt := range []struct{ publisher, want string }{
		{"Unknown", "placeholder"},
		{"UNKNOWN", "placeholder"},
		{"[Unknown]", "placeholder"},
		{"Unbekannt", "placeholder"},
		{"Desconocido", "placeholder"},
		{"N/A", "placeholder"},
		{"n/a", "placeholder"},
		{"None", "placeholder"},
		{"Publisher", "placeholder"},
		{"Calibre", "placeholder"},
		{"  calibre  ", "placeholder"},
		{"www.ebooksgratis.com", "website"},
		{"www.example", "website"},
		{"WWW.Example.Org", "website"},
		{"http://www.example.com", "website"},
		{"https://example.net", "website"},
		{"www.Bookboon.com", "website"},
		{"Z-Library", "scanner_site"},
		{"z-lib.org", "scanner_site"},
		{"LibGen", "scanner_site"},
		{"libgen.rs", "scanner_site"},
		{"Scan", "scanner_site"},
		{"SCAN", "scanner_site"},

		{"Smashwords", ""},
		{"Smashwords, Inc.", ""},
		{"CreateSpace", ""},
		{"CreateSpace Independent Publishing Platform", ""},
		{"Lulu.com", ""},
		{"www.lulu.com", ""},
		{"http://lulu.com", ""},
		{"Bookboon.com", ""},
		{"Baen.com", ""},
		{"iUniverse.com", ""},
		{"Xlibris.com", ""},
		{"Leanpub.com", ""},
		{"Blurb.com", ""},
		{"Fictionwise.com", ""},
		{"Rosettabooks.com", ""},
		{"InfoQ.com", ""},
		{"ebook-hunter.net", ""},
		{"freebooks.org", ""},
		{"bookzz.ru", ""},
		{"knigi.to", ""},
		{"Muñoz-Libros", ""},
		{"Ediciones Ortiz-Librería", ""},
		{"Ruiz-Librería Editores", ""}, //nolint:misspell // Spanish
		{"Kranz-Libri", ""},
		{"Tor.com", ""},
		{"Penguin Random House", ""},
		{"Publishers Weekly", ""},
		{"Scandinavian Press", ""},
		{"Scanlan", ""},
		{"Calibre Press", ""},
		{"Unknown Pleasures Press", ""},
		{"Smith & Jones.com Press", ""},
		{"Vintage", ""},
		{"", ""},
		{"   ", ""},
	} {
		t.Run(tt.publisher, func(t *testing.T) {
			if got := junkPublisherReason(tt.publisher); got != tt.want {
				t.Fatalf("junkPublisherReason(%q) = %q, want %q", tt.publisher, got, tt.want)
			}
		})
	}
}

func TestDeriveV5(t *testing.T) {
	titled := func(title string) func(*evidence.Package) {
		return func(p *evidence.Package) { p.Calibre.Title, p.Formats[0].Metadata.Title = title, title }
	}
	authored := func(authors ...string) func(*evidence.Package) {
		return func(p *evidence.Package) {
			p.Calibre.Authors, p.Formats[0].Metadata.Authors = authors, authors
			p.Calibre.AuthorSort = authorSorts(authors)
		}
	}
	for _, tt := range []struct {
		name string
		edit func(*evidence.Package)
		want []evidence.Finding
	}{
		{"clean", func(*evidence.Package) {}, nil},
		{"junk title", titled("Microsoft Word - report.doc"), []evidence.Finding{{Code: "junk_title", Field: "title", Reason: "word_artefact"}}},
		{"junk file title", titled("scan0042.pdf"), []evidence.Finding{{Code: "junk_title", Field: "title", Reason: "file_extension"}}},
		{"real numeric title", titled("1984"), nil},
		{"title noise", titled("Emma (Penguin Classics)"), []evidence.Finding{{Code: "title_noise", Field: "title", Reason: "series_imprint"}}},
		{"title noise two kinds", titled("Emma (Penguin Classics) (Annotated)"), []evidence.Finding{
			{Code: "title_noise", Field: "title", Reason: "edition_credit"}, {Code: "title_noise", Field: "title", Reason: "series_imprint"},
		}},
		{"author prefix", func(p *evidence.Package) {
			authored("Thomas Mann")(p)
			titled("Mann, Thomas - Der Zauberberg")(p)
		}, []evidence.Finding{{Code: "title_noise", Field: "title", Reason: "author_prefix"}}},
		{"joined authors", authored("John Smith and Jane Doe"), []evidence.Finding{{Code: "joined_authors", Field: "authors", Reason: "and"}}},
		{"joined authors, one finding per kind", authored("A. Smith; B. Jones", "C. Brown; D. Green"),
			[]evidence.Finding{{Code: "joined_authors", Field: "authors", Reason: "semicolon"}}},
		{"two real authors", authored("John Smith", "Jane Doe"), nil},
		{"publisher in the authors", func(p *evidence.Package) {
			authored("Simon & Schuster")(p)
		}, nil},
		{"junk publisher", func(p *evidence.Package) { p.Calibre.Publisher = "Unknown" }, []evidence.Finding{{Code: "junk_publisher", Field: "publisher", Reason: "placeholder"}}},
		{"website publisher", func(p *evidence.Package) { p.Calibre.Publisher = "www.example.com" }, []evidence.Finding{{Code: "junk_publisher", Field: "publisher", Reason: "website"}}},
		{"real distributor", func(p *evidence.Package) { p.Calibre.Publisher = "Smashwords" }, nil},
		{"title too long to judge", titled(strings.Repeat("a", 600) + ".pdf"), nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := goodPackage()
			tt.edit(&p)
			fs, err := DeriveVersion(context.Background(), p, 5)
			if err != nil {
				t.Fatal(err)
			}
			got := v5Only(fs)
			if !slices.Equal(got, tt.want) {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
			// Nothing here fires below version 5, and Derive is version 5.
			for v := 1; v <= 4; v++ {
				old, err := DeriveVersion(context.Background(), p, v)
				if err != nil {
					t.Fatal(err)
				}
				if extra := v5Only(old); len(extra) != 0 {
					t.Fatalf("version %d derived %+v", v, extra)
				}
			}
			if cur, err := Derive(context.Background(), p); err != nil || !slices.Equal(cur, fs) {
				t.Fatalf("Derive differs from version 5: %+v, %v", cur, err)
			}
		})
	}
}

// TestV5LeavesOtherFindingsAlone: a version-5 finding never removes or
// changes a finding older rules derive.
func TestV5LeavesOtherFindingsAlone(t *testing.T) {
	p := goodPackage()
	p.Calibre.Title = "Untitled"
	p.Calibre.Publisher = "Unknown"
	p.Formats = nil
	v4, err := DeriveVersion(context.Background(), p, 4)
	if err != nil {
		t.Fatal(err)
	}
	v5, err := DeriveVersion(context.Background(), p, 5)
	if err != nil {
		t.Fatal(err)
	}
	var rest []evidence.Finding
	for _, f := range v5 {
		if !slices.Contains(v5Codes, f.Code) {
			rest = append(rest, f)
		}
	}
	if !slices.Equal(rest, v4) || len(v5Only(v5)) != 2 {
		t.Fatalf("v4 %+v\nv5 %+v", v4, v5)
	}
}

func FuzzTitleNoise(f *testing.F) {
	for _, s := range []string{"Mann, Thomas - Der Zauberberg", "Emma (Penguin Classics)", "Translated by A B", "x (Annotated)", "Übersetzt von", "(", "[", " - ", "20th Anniversary Edition", "\xff"} {
		f.Add(s, "Thomas Mann")
	}
	closed := []string{"series_imprint", "translator_credit", "author_prefix", "edition_credit"}
	f.Fuzz(func(t *testing.T, title, author string) {
		got := titleNoiseReasons(title, []string{author})
		for i, r := range got {
			if !slices.Contains(closed, r) || slices.Contains(got[:i], r) {
				t.Fatalf("reasons %v", got)
			}
		}
		if !slices.Equal(got, titleNoiseReasons(title, []string{author})) {
			t.Fatal("not deterministic")
		}
		if r := junkTitleReason(title); r != "" && !slices.Contains([]string{"word_artefact", "file_extension", "untitled", "number_only"}, r) {
			t.Fatalf("junk reason %q", r)
		}
	})
}

func FuzzJoinedAuthor(f *testing.F) {
	for _, s := range []string{"John Smith and Jane Doe", "Smith & Jones", "A; B", "José Ortega y Gasset", "Simon & Schuster", " und ", ";", "&", "\xff and \xfe"} {
		f.Add(s, "Simon & Schuster")
	}
	f.Fuzz(func(t *testing.T, entry, publisher string) {
		got := joinedAuthorReason(entry, publisher)
		if got != "" && !slices.Contains([]string{"and", "und", "et", "y", "ampersand", "semicolon"}, got) {
			t.Fatalf("reason %q for %q", got, entry)
		}
		if got != "" && !strings.ContainsAny(entry, "&;") && !strings.Contains(entry, " ") {
			t.Fatalf("flagged %q", entry)
		}
		if got != joinedAuthorReason(entry, publisher) {
			t.Fatal("not deterministic")
		}
		if r := junkPublisherReason(entry); r != "" && !slices.Contains([]string{"placeholder", "website", "scanner_site"}, r) {
			t.Fatalf("publisher reason %q", r)
		}
	})
}
