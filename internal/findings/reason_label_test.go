package findings

import "testing"

func TestReasonLabel(t *testing.T) {
	for _, tt := range []struct{ code, reason, want string }{
		{"joined_authors", "y", "two names joined by 'y'"},
		{"joined_authors", "ampersand", "two names joined by '&'"},
		{"junk_title", "file_extension", "ends in a file extension"},
		{"title_noise", "edition_credit", "edition credit in parentheses"},
		{"junk_publisher", "scanner_site", "scanner or shadow-library name"},
		{"file_missing", "missing_on_disk", "missing_on_disk"}, // older codes show as stored
		{"junk_title", "unknown", "unknown"},
		{"junk_title", "", ""},
	} {
		if got := ReasonLabel(tt.code, tt.reason); got != tt.want {
			t.Errorf("ReasonLabel(%q, %q) = %q, want %q", tt.code, tt.reason, got, tt.want)
		}
	}
	// Every closed reason has a label, and the label says more than the code.
	for _, k := range [][2]string{
		{"junk_title", "word_artefact"}, {"junk_title", "file_extension"}, {"junk_title", "untitled"}, {"junk_title", "number_only"},
		{"title_noise", "series_imprint"}, {"title_noise", "translator_credit"}, {"title_noise", "author_prefix"}, {"title_noise", "edition_credit"},
		{"joined_authors", "and"}, {"joined_authors", "und"}, {"joined_authors", "et"}, {"joined_authors", "y"}, {"joined_authors", "ampersand"}, {"joined_authors", "semicolon"},
		{"junk_publisher", "placeholder"}, {"junk_publisher", "website"}, {"junk_publisher", "scanner_site"},
	} {
		if got := ReasonLabel(k[0], k[1]); got == k[1] {
			t.Errorf("%v has no label", k)
		}
	}
}
