package findings

import (
	"slices"
	"testing"
)

func TestStripTitleNoise(t *testing.T) {
	for _, tt := range []struct {
		name, title string
		authors     []string
		want        string
		reasons     []string
	}{
		{"no noise", "Printed Title", nil, "Printed Title", nil},
		{"no noise is only tidied", "  Printed   Title ", nil, "Printed Title", nil},
		{"series in parentheses", "Printed Title (Penguin Classics)", nil, "Printed Title", []string{"series_imprint"}},
		{"series with a curly apostrophe", "Printed Title (Oxford World’s Classics)", nil, "Printed Title", []string{"series_imprint"}},
		{"series in other case", "Printed Title (PENGUIN CLASSICS)", nil, "Printed Title", []string{"series_imprint"}},
		{"edition credit in brackets", "Printed Title [Annotated Edition]", nil, "Printed Title", []string{"edition_credit"}},
		{"translator after a colon", "Printed Title: Translated by John Smith", nil, "Printed Title", []string{"translator_credit"}},
		{"translator in parentheses", "Printed Title (Translated by John Smith)", nil, "Printed Title", []string{"translator_credit"}},
		{"author prefix", "Author, Ann - Printed Title", []string{"Ann Author"}, "Printed Title", []string{"author_prefix"}},
		{"two kinds", "Author, Ann - Printed Title (Penguin Classics)", []string{"Ann Author"}, "Printed Title", []string{"series_imprint", "author_prefix"}},
		{"a real subtitle stays", "Printed Title: A Real Subtitle", nil, "Printed Title: A Real Subtitle", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, reasons := StripTitleNoise(tt.title, tt.authors)
			if got != tt.want || !slices.Equal(reasons, tt.reasons) {
				t.Errorf("StripTitleNoise(%q) = %q, %v; want %q, %v", tt.title, got, reasons, tt.want, tt.reasons)
			}
		})
	}
}

func TestJunkExports(t *testing.T) {
	for title, want := range map[string]string{"scan.pdf": "file_extension", "Untitled": "untitled", "Printed Title": "", "Printed Title (Penguin Classics)": ""} {
		if got := JunkTitleReason(title); got != want {
			t.Errorf("JunkTitleReason(%q) = %q, want %q", title, got, want)
		}
	}
	for publisher, want := range map[string]string{"calibre": "placeholder", "www.example.org": "website", "Acme Press": ""} {
		if got := JunkPublisherReason(publisher); got != want {
			t.Errorf("JunkPublisherReason(%q) = %q, want %q", publisher, got, want)
		}
	}
}
