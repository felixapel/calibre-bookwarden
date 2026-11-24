package findings

import "slices"

// Definition contains only static presentation text for a stable code.
type Definition struct{ Code, Severity, Label string }

// Catalog returns a copy of the supported finding definitions.
func Catalog() []Definition { return slices.Clone(definitions) }

var definitions = []Definition{
	{"author_sort_missing", "warning", "Author sort key missing"}, {"author_sort_mismatch", "warning", "Author sort key differs from the known rules"},
	{"title_sort_missing", "warning", "Title sort key missing"}, {"title_sort_article_not_moved", "info", "Title sort keeps the leading article"},
	{"title_sort_mismatch", "warning", "Title sort key differs from the title"},
	{"no_formats", "warning", "No attached formats"},
	{"file_empty", "error", "Empty file"}, {"file_corrupt", "error", "Corrupt file"}, {"file_missing", "error", "File missing"},
	{"file_drm", "warning", "Reader-locked file"}, {"file_unsupported", "warning", "Unsupported file variant"}, {"file_uninspected", "info", "File not inspected"},
	{"format_mismatch", "warning", "Format does not match the content"},
	{"isbn_invalid", "warning", "Invalid ISBN"}, {"isbn_missing", "warning", "ISBN missing"}, {"isbn_ambiguous", "warning", "Multiple ISBN candidates"},
	{"isbn_mismatch", "warning", "File ISBN differs from Calibre"}, {"formats_isbn_conflict", "warning", "Formats have no shared ISBN"},
	{"title_missing", "warning", "Title missing"}, {"authors_missing", "warning", "Authors missing"},
	{"language_missing", "warning", "Language missing"}, {"language_invalid", "warning", "Invalid language"},
	{"title_mismatch", "warning", "File title differs from Calibre"}, {"authors_mismatch", "warning", "File authors differ from Calibre"}, {"language_mismatch", "warning", "File language differs from Calibre"},
	{"formats_title_conflict", "warning", "Format titles disagree"}, {"formats_authors_conflict", "warning", "Format authors disagree"}, {"formats_language_conflict", "warning", "Format languages disagree"},
	{"date_imprecise", "warning", "Publication date is imprecise"}, {"date_invalid", "warning", "Invalid publication date"}, {"date_mismatch", "warning", "File publication date differs from Calibre"},
	{"date_precision_unverified", "info", "January 1 date needs precision review"}, {"metadata_unverified", "info", "Metadata could not be compared"},
	{"cover_missing", "warning", "Cover missing"}, {"cover_corrupt", "error", "Corrupt cover"}, {"cover_uninspected", "info", "Cover not inspected"},
	{"cover_low_resolution", "warning", "Cover resolution is low"}, {"cover_placeholder", "warning", "Cover looks like a placeholder"},
	{"text_roots_disagree", "info", "The book's printed text and the other sources can't agree"},
	{"other_work", "warning", "ISBN records describe another work"},
	{"junk_title", "warning", "Title is a file name or application artefact"}, {"title_noise", "info", "Title carries a series, credit, edition or author prefix"},
	{"joined_authors", "warning", "One author entry holds several people"},
	{"junk_publisher", "warning", "Publisher is a placeholder, website or scanner"},
	{"cover_better_available", "info", "A better cover is available"},
	{"series_missing", "info", "File names a series that Calibre lacks"}, {"series_name_mismatch", "info", "File series name differs from Calibre"}, {"series_mismatch", "warning", "File series index differs from Calibre"},
}
