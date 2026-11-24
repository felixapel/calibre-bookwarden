package findings

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

func goodPackage() evidence.Package {
	return evidence.Package{SchemaVersion: 1, LibraryID: "library", BookID: 1, UUID: "u", Calibre: evidence.Record{
		Title: "Synthetic", Authors: []string{"Ada Author"}, AuthorSort: "Author, Ada", Languages: []string{"en"}, Identifiers: map[string]string{"isbn": "9780306406157"}, PubDate: "2020-02-03T00:00:00Z",
	}, Formats: []evidence.FormatEvidence{{Format: "EPUB", State: evidence.StateOK, Detected: "epub", Metadata: &evidence.FileMetadata{
		Title: "Synthetic", Authors: []string{"Ada Author"}, Languages: []string{"eng"}, ISBNs: []string{"0-306-40615-2"}, Date: "2020-02-03",
	}}}}
}

func TestDerive(t *testing.T) {
	for _, tt := range []struct {
		name string
		edit func(*evidence.Package)
		want []evidence.Finding
	}{
		{"clean", func(*evidence.Package) {}, nil},
		{"no formats", func(p *evidence.Package) { p.Formats = nil }, []evidence.Finding{{Code: "no_formats"}}},
		{"type", func(p *evidence.Package) { p.Formats[0].Detected = "pdf" }, []evidence.Finding{{Code: "format_mismatch", Format: "EPUB"}}},
		{"invalid ISBN", func(p *evidence.Package) { p.Formats[0].Metadata.ISBNs = []string{"9780306406158"} }, []evidence.Finding{{Code: "isbn_invalid", Format: "EPUB", Field: "isbn"}}},
		{"missing ISBN", func(p *evidence.Package) { p.Formats[0].Metadata.ISBNs = nil }, []evidence.Finding{{Code: "isbn_missing", Format: "EPUB", Field: "isbn"}}},
		{"calibre invalid", func(p *evidence.Package) { p.Calibre.Identifiers["isbn"] = "9780306406158" }, []evidence.Finding{{Code: "isbn_invalid", Field: "isbn"}}},
		{"calibre missing", func(p *evidence.Package) { p.Calibre.Identifiers = nil }, []evidence.Finding{{Code: "isbn_missing", Field: "isbn"}}},
		{"ambiguous", func(p *evidence.Package) { p.Formats[0].Metadata.ISBNs = []string{"9780306406157", "9780804429573"} }, []evidence.Finding{{Code: "isbn_ambiguous", Format: "EPUB", Field: "isbn"}}},
		{"ISBN mismatch", func(p *evidence.Package) { p.Formats[0].Metadata.ISBNs = []string{"9780804429573"} }, []evidence.Finding{{Code: "isbn_mismatch", Format: "EPUB", Field: "isbn"}}},
		{"title missing", func(p *evidence.Package) { p.Calibre.Title = "" }, []evidence.Finding{{Code: "title_missing", Field: "title"}}},
		{"authors missing", func(p *evidence.Package) { p.Formats[0].Metadata.Authors = nil }, []evidence.Finding{{Code: "authors_missing", Format: "EPUB", Field: "authors"}}},
		{"language missing", func(p *evidence.Package) { p.Calibre.Languages = nil }, []evidence.Finding{{Code: "language_missing", Field: "languages"}}},
		{"language invalid", func(p *evidence.Package) { p.Formats[0].Metadata.Languages = []string{"en?"} }, []evidence.Finding{{Code: "language_invalid", Format: "EPUB", Field: "languages"}}},
		{"language und", func(p *evidence.Package) { p.Formats[0].Metadata.Languages = []string{"und"} }, []evidence.Finding{{Code: "metadata_unverified", Format: "EPUB", Field: "languages", Reason: "language_undetermined"}}},
		{"title mismatch", func(p *evidence.Package) { p.Formats[0].Metadata.Title = "Other" }, []evidence.Finding{{Code: "title_mismatch", Format: "EPUB", Field: "title"}}},
		{"authors mismatch", func(p *evidence.Package) { p.Formats[0].Metadata.Authors = []string{"Other Author"} }, []evidence.Finding{{Code: "authors_mismatch", Format: "EPUB", Field: "authors"}}},
		{"language mismatch", func(p *evidence.Package) { p.Formats[0].Metadata.Languages = []string{"de"} }, []evidence.Finding{{Code: "language_mismatch", Format: "EPUB", Field: "languages"}}},
		{"year", func(p *evidence.Package) { p.Formats[0].Metadata.Date = "2020" }, []evidence.Finding{{Code: "date_imprecise", Format: "EPUB", Field: "pubdate"}}},
		{"month", func(p *evidence.Package) { p.Formats[0].Metadata.Date = "2020-02" }, []evidence.Finding{{Code: "date_imprecise", Format: "EPUB", Field: "pubdate"}}},
		{"invalid date", func(p *evidence.Package) { p.Formats[0].Metadata.Date = "2020-02-30" }, []evidence.Finding{{Code: "date_invalid", Format: "EPUB", Field: "pubdate"}}},
		{"date mismatch", func(p *evidence.Package) { p.Formats[0].Metadata.Date = "2020-02-04" }, []evidence.Finding{{Code: "date_mismatch", Format: "EPUB", Field: "pubdate"}}},
		{"timezone date", func(p *evidence.Package) { p.Formats[0].Metadata.Date = "2020-02-03T23:59:59-12:00" }, nil},
		{"January first", func(p *evidence.Package) { p.Calibre.PubDate = "2020-01-01"; p.Formats[0].Metadata.Date = "2020" }, []evidence.Finding{{Code: "date_imprecise", Format: "EPUB", Field: "pubdate"}, {Code: "date_precision_unverified", Field: "pubdate"}}},
		{"long title", func(p *evidence.Package) { p.Formats[0].Metadata.Title = strings.Repeat("x", 513) }, []evidence.Finding{{Code: "metadata_unverified", Format: "EPUB", Field: "title", Reason: "field_too_long"}}},
		{"encrypted", func(p *evidence.Package) { p.Formats[0].Reason = evidence.ReasonEncrypted; p.Formats[0].Metadata = nil }, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := goodPackage()
			tt.edit(&p)
			got, err := Derive(context.Background(), p)
			if err != nil || !slices.Equal(got, tt.want) {
				t.Fatalf("got %+v, %v; want %+v", got, err, tt.want)
			}
		})
	}
	for _, tt := range []struct {
		state        evidence.State
		code, reason string
	}{
		{evidence.StateEmpty, "file_empty", ""}, {evidence.StateCorrupt, "file_corrupt", ""},
		{evidence.StateMissing, "file_missing", evidence.ReasonMissingOnDisk}, {evidence.StateDRM, "file_drm", ""},
		{evidence.StateUnsupported, "file_unsupported", evidence.ReasonHuffCDIC}, {evidence.StateNotInspected, "file_uninspected", evidence.ReasonTimeout},
	} {
		t.Run(string(tt.state), func(t *testing.T) {
			p := goodPackage()
			p.Formats[0].State = tt.state
			p.Formats[0].Reason = tt.reason
			got, err := Derive(context.Background(), p)
			want := []evidence.Finding{{Code: tt.code, Format: "EPUB", Reason: tt.reason}}
			if err != nil || !slices.Equal(got, want) {
				t.Fatalf("%+v/%v; want %+v", got, err, want)
			}
		})
	}
	for _, tt := range []struct{ format, detected string }{
		{"KEPUB", "epub"}, {"ORIGINAL_EPUB", "epub"}, {"EPUB", "zip"}, {"EPUB", "unknown"},
		{"AZW", "mobi"}, {"AZW3", "palmdoc"}, {"PRC", "mobi"}, {"JPG", "jpeg"}, {"JPEG", "jpeg"}, {"CBZ", "zip"}, {"CBR", "rar"}, {"UNKNOWN", "pdf"},
	} {
		t.Run("alias/"+tt.format+"/"+tt.detected, func(t *testing.T) {
			p := goodPackage()
			p.Formats[0].Format = tt.format
			p.Formats[0].Detected = tt.detected
			got, err := Derive(context.Background(), p)
			if err != nil || len(got) != 0 {
				t.Fatalf("false alias finding %+v/%v", got, err)
			}
		})
	}
}

func TestDeriveFormatConflicts(t *testing.T) {
	p := goodPackage()
	p.Formats = append(p.Formats, evidence.FormatEvidence{Format: "PDF", State: evidence.StateOK, Detected: "pdf", Metadata: &evidence.FileMetadata{Title: "Other", Authors: []string{"Other Author"}, Languages: []string{"de"}, ISBNs: []string{"9780804429573"}}})
	got, err := Derive(context.Background(), p)
	want := []evidence.Finding{
		{Code: "authors_mismatch", Format: "PDF", Field: "authors"}, {Code: "formats_authors_conflict", Field: "authors"},
		{Code: "formats_isbn_conflict", Field: "isbn"}, {Code: "formats_language_conflict", Field: "languages"}, {Code: "formats_title_conflict", Field: "title"},
		{Code: "isbn_mismatch", Format: "PDF", Field: "isbn"}, {Code: "language_mismatch", Format: "PDF", Field: "languages"}, {Code: "title_mismatch", Format: "PDF", Field: "title"},
	}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("got %+v/%v; want %+v", got, err, want)
	}
	p.Formats[1].State = evidence.StateNotInspected
	p.Formats[1].Reason = evidence.ReasonNoInspector
	got, err = Derive(context.Background(), p)
	if err != nil || !slices.Equal(got, []evidence.Finding{{Code: "file_uninspected", Format: "PDF", Reason: evidence.ReasonNoInspector}}) {
		t.Fatalf("unavailable metadata established conflict: %+v/%v", got, err)
	}
}

func TestDeriveDeterministic(t *testing.T) {
	p := goodPackage()
	p.Calibre.Identifiers["other"] = "irrelevant"
	p.Formats[0].Metadata.ISBNs = []string{"9780804429573", "9780306406157", "9780306406158", "9780804429573"}
	p.Formats = append(p.Formats, evidence.FormatEvidence{Format: "PDF", State: evidence.StateMissing, Reason: evidence.ReasonMissingOnDisk})
	before, _ := json.Marshal(p)
	first, err := Derive(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(p)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("input was mutated")
	}
	slices.Reverse(p.Formats)
	slices.Reverse(p.Formats[1].Metadata.ISBNs)
	p.Calibre.Identifiers = map[string]string{"other": "irrelevant", "isbn": "9780306406157"}
	second, err := Derive(context.Background(), p)
	if err != nil || !slices.Equal(first, second) {
		t.Fatalf("nondeterministic: %+v / %+v / %v", first, second, err)
	}
}

func TestDeriveInvalidEvidence(t *testing.T) {
	for _, edit := range []func(*evidence.Package){
		func(p *evidence.Package) { p.Formats = append(p.Formats, p.Formats[0]) },
		func(p *evidence.Package) { p.Formats[0].Format = "" },
		func(p *evidence.Package) { p.Formats[0].State = "invented" },
		func(p *evidence.Package) { p.Formats[0].Reason = "private title" },
	} {
		p := goodPackage()
		edit(&p)
		if _, err := Derive(context.Background(), p); !errors.Is(err, ErrInvalidEvidence) {
			t.Fatalf("invalid evidence accepted: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Derive(ctx, goodPackage()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}
