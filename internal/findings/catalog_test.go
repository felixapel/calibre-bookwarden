package findings

import (
	"context"
	"testing"
)

func TestCatalog(t *testing.T) {
	seen := map[string]bool{}
	for _, d := range Catalog() {
		if seen[d.Code] || d.Code == "" || d.Label == "" || (d.Severity != "info" && d.Severity != "warning" && d.Severity != "error") {
			t.Fatalf("invalid definition %+v", d)
		}
		seen[d.Code] = true
	}
	for _, code := range []string{"no_formats", "file_empty", "file_corrupt", "file_missing", "file_drm", "file_unsupported", "file_uninspected", "format_mismatch", "isbn_invalid", "isbn_missing", "isbn_ambiguous", "isbn_mismatch", "formats_isbn_conflict", "title_missing", "authors_missing", "language_missing", "language_invalid", "title_mismatch", "authors_mismatch", "language_mismatch", "formats_title_conflict", "formats_authors_conflict", "formats_language_conflict", "date_imprecise", "date_invalid", "date_mismatch", "date_precision_unverified", "metadata_unverified", "cover_missing", "cover_corrupt", "cover_uninspected", "cover_low_resolution", "cover_placeholder", "other_work", "junk_title", "title_noise", "joined_authors", "junk_publisher"} {
		if !seen[code] {
			t.Errorf("no definition for %s", code)
		}
	}
	p := goodPackage()
	p.Formats = nil
	fs, err := Derive(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fs {
		if !seen[f.Code] {
			t.Fatalf("uncatalogued finding %+v", f)
		}
	}
}
