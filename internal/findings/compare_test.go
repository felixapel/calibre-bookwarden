package findings

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

func TestKeys(t *testing.T) {
	if a, b := TitleKey(" Harry  Potter "), TitleKey("harry potter"); a != b || !a.Valid {
		t.Fatalf("title keys %+v %+v", a, b)
	}
	if k := TitleKey(strings.Repeat("x", 513)); k.Valid {
		t.Fatal("a 513-rune title is valid")
	}
	if k := TitleKey("  "); k.Valid {
		t.Fatal("a blank title is valid")
	}
	if a, b := AuthorsKey([]string{"A", "B"}), AuthorsKey([]string{"B", "A"}); a == b || !a.Valid {
		t.Fatalf("author order ignored: %+v %+v", a, b)
	}
	ger, deu, de := LanguagesKey([]string{"ger"}), LanguagesKey([]string{"deu"}), LanguagesKey([]string{"de"})
	if ger != deu || deu != de || !de.Valid {
		t.Fatalf("language keys %+v %+v %+v", ger, deu, de)
	}
	for _, bad := range [][]string{{"und"}, {"en?"}, nil} {
		if k := LanguagesKey(bad); k.Valid {
			t.Fatalf("LanguagesKey(%q) is valid", bad)
		}
	}
	if k := DateKey("2000"); k.Valid {
		t.Fatal("a year is a valid date key")
	}
	if k := DateKey("2000-07-26T00:00:00+00:00"); !k.Valid || k.Value != "2000-07-26" {
		t.Fatalf("DateKey = %+v", k)
	}
	if a, b := PublisherKey("Carlsen"), PublisherKey(" carlsen "); a != b || !a.Valid {
		t.Fatalf("publisher keys %+v %+v", a, b)
	}
	if got := LanguageCodes(LanguagesKey([]string{"ger", "spa"})); !slices.Equal(got, []string{"deu", "spa"}) {
		t.Fatalf("LanguageCodes = %v", got)
	}
	if got := LanguageCodes(LanguagesKey([]string{"und"})); got != nil {
		t.Fatalf("LanguageCodes(invalid) = %v", got)
	}
}

// TestFieldsMatchesDerive pins the exported keys to the derivation: a format
// whose keys differ from Calibre's has the mismatch finding, and one whose
// keys are equal has none.
func TestFieldsMatchesDerive(t *testing.T) {
	for _, tt := range []struct {
		name string
		edit func(*evidence.Package)
	}{
		{"clean", func(*evidence.Package) {}},
		{"case and space only", func(p *evidence.Package) { p.Formats[0].Metadata.Title = " SYNTHETIC " }},
		{"title", func(p *evidence.Package) { p.Formats[0].Metadata.Title = "Other" }},
		{"authors", func(p *evidence.Package) { p.Formats[0].Metadata.Authors = []string{"Other Author"} }},
		{"authors order", func(p *evidence.Package) {
			p.Calibre.Authors = []string{"A", "B"}
			p.Formats[0].Metadata.Authors = []string{"B", "A"}
		}},
		{"languages", func(p *evidence.Package) { p.Formats[0].Metadata.Languages = []string{"de"} }},
		{"B language code", func(p *evidence.Package) {
			p.Calibre.Languages = []string{"deu"}
			p.Formats[0].Metadata.Languages = []string{"ger"}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := goodPackage()
			tt.edit(&p)
			got, err := Derive(context.Background(), p)
			if err != nil {
				t.Fatal(err)
			}
			ff, err := Fields(p.Formats[0])
			if err != nil || !ff.Eligible {
				t.Fatalf("Fields = %+v, %v", ff, err)
			}
			for _, c := range []struct {
				code      string
				file, cal Key
			}{
				{"title_mismatch", ff.Title, TitleKey(p.Calibre.Title)},
				{"authors_mismatch", ff.Authors, AuthorsKey(p.Calibre.Authors)},
				{"language_mismatch", ff.Languages, LanguagesKey(p.Calibre.Languages)},
			} {
				has := slices.ContainsFunc(got, func(f evidence.Finding) bool { return f.Code == c.code })
				if has != (c.file != c.cal) {
					t.Fatalf("%s: finding %v, keys %+v vs %+v", c.code, has, c.file, c.cal)
				}
			}
		})
	}
}

func TestFieldsCapture(t *testing.T) {
	f := goodPackage().Formats[0]
	ff, err := Fields(f)
	if err != nil || !ff.Title.Valid || !ff.Date.Valid || ff.Date.Value != "2020-02-03" || !slices.Equal(ff.ISBNs, []string{"9780306406157"}) || ff.ISBNFlagged {
		t.Fatalf("clean fields = %+v, %v", ff, err)
	}
	f.Metadata.Limits = &evidence.MetadataLimits{Version: 1, Title: true}
	if ff, err := Fields(f); err != nil || ff.Title.Valid || !ff.Authors.Valid {
		t.Fatalf("flagged title: %+v, %v", ff, err)
	}
	f.Metadata.Limits = &evidence.MetadataLimits{Version: 1, ISBNs: true}
	if ff, err := Fields(f); err != nil || !ff.ISBNFlagged || ff.ISBNs != nil {
		t.Fatalf("flagged ISBNs: %+v, %v", ff, err)
	}
	f.Metadata.Limits = &evidence.MetadataLimits{Version: 2}
	if _, err := Fields(f); !errors.Is(err, ErrInvalidEvidence) {
		t.Fatalf("unknown marker: %v", err)
	}
	f.Metadata.Limits = nil
	f.Metadata.Title = strings.Repeat("y", 511) // at the capture boundary, without a marker
	if ff, _ := Fields(f); ff.Title.Valid {
		t.Fatal("an unmarked boundary title is valid")
	}
	f.Metadata.ISBNs = []string{"9780306406157", "0-306-40615-2", "9780804429573", "nonsense"}
	if ff, _ := Fields(f); !slices.Equal(ff.ISBNs, []string{"9780306406157", "9780804429573"}) {
		t.Fatalf("ISBNs = %v", ff.ISBNs)
	}
	f.State = evidence.StateCorrupt
	if ff, err := Fields(f); err != nil || ff.Eligible || ff.Title.Valid || ff.ISBNs != nil {
		t.Fatalf("corrupt format: %+v, %v", ff, err)
	}
}
