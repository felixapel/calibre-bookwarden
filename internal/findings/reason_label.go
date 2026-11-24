package findings

// reasonLabels is the readable text of the closed reasons the version-5
// detectors give. Reasons of older codes are shown as stored.
var reasonLabels = map[[2]string]string{
	{"junk_title", "word_artefact"}:      "Word export name",
	{"junk_title", "file_extension"}:     "ends in a file extension",
	{"junk_title", "untitled"}:           "editor default name",
	{"junk_title", "number_only"}:        "only a number or date stamp",
	{"title_noise", "series_imprint"}:    "publisher series in parentheses",
	{"title_noise", "translator_credit"}: "translator credit",
	{"title_noise", "author_prefix"}:     "author name prefix",
	{"title_noise", "edition_credit"}:    "edition credit in parentheses",
	{"joined_authors", "and"}:            "two names joined by 'and'",
	{"joined_authors", "und"}:            "two names joined by 'und'",
	{"joined_authors", "et"}:             "two names joined by 'et'",
	{"joined_authors", "y"}:              "two names joined by 'y'",
	{"joined_authors", "ampersand"}:      "two names joined by '&'",
	{"joined_authors", "semicolon"}:      "two names joined by ';'",
	{"junk_publisher", "placeholder"}:    "placeholder publisher",
	{"junk_publisher", "website"}:        "website as publisher",
	{"junk_publisher", "scanner_site"}:   "scanner or shadow-library name",
	{"cover_better_available", "epub"}:   "the cover inside the EPUB",
}

// ReasonLabel returns readable text for a finding's reason, or the reason
// itself when it has none.
func ReasonLabel(code, reason string) string {
	if label, ok := reasonLabels[[2]string{code, reason}]; ok {
		return label
	}
	return reason
}
