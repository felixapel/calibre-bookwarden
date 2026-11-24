package findings

// Helpers decision version 8 reads (ADR-036). They expose the version 5
// detectors without changing them.
//
// Frozen: decision versions 8 and 9 call these without a version, and their
// sealed packages are replayed with them. A change here changes those
// decisions: add the new behaviour behind a new decision version instead.

import (
	"regexp"
	"strings"
)

// JunkTitleReason names the artefact pattern a title matches (word_artefact,
// file_extension, untitled, number_only), or "". Decision version 8 reads it
// to tell a junk Calibre title from one that is merely noisy.
func JunkTitleReason(title string) string { return junkTitleReason(title) }

// JunkPublisherReason names the closed list a publisher is on (placeholder,
// website, scanner_site), or "".
func JunkPublisherReason(publisher string) string { return junkPublisherReason(publisher) }

// StripTitleNoise returns the title with exactly the kinds of noise that
// titleNoiseReasons finds removed, and those reasons. A title with no noise
// comes back tidied, with no reasons. Decision version 8 lets a title shorten
// only to this form (ADR-036 section 3.4).
func StripTitleNoise(title string, authors []string) (string, []string) {
	reasons := titleNoiseReasons(title, authors)
	t := tidy(title)
	if len(reasons) == 0 {
		return t, nil
	}
	for _, reason := range reasons {
		switch reason {
		case "series_imprint":
			t = seriesImprintAny.ReplaceAllString(t, "")
		case "translator_credit":
			t = translatorCredit.ReplaceAllString(t, "")
		case "edition_credit":
			t = editionCreditAny.ReplaceAllString(t, "")
		case "author_prefix":
			t = stripAuthorPrefix(t, authors)
		}
	}
	return tidy(strings.TrimRight(t, " -–—:/")), reasons
}

// The title noise patterns as they apply to the title as written rather than
// to its folded form: case-insensitive, with any apostrophe.
var (
	seriesImprintAny = regexp.MustCompile(`(?i)\(\s*(?:` + strings.Join(anyApostrophe(quoteAll(seriesImprints)), "|") + `)\s*\)`)
	editionCreditAny = regexp.MustCompile(`(?i)[(\[]\s*(?:` + strings.ReplaceAll(editionCredits, "'", "['’‘]") + `)\s*[)\]]`)
)

func anyApostrophe(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = strings.ReplaceAll(s, "'", "['’‘]")
	}
	return out
}

// authorPrefixShape splits "head - rest" at its first dash with one space on
// each side, the shapes of dashes.
var authorPrefixShape = regexp.MustCompile(`^(.+?) [-–—] (.+)$`)

// stripAuthorPrefix removes a leading "Surname, Given - " of one of the
// authors, matched case-insensitively like hasAuthorPrefix.
func stripAuthorPrefix(title string, authors []string) string {
	m := authorPrefixShape.FindStringSubmatch(title)
	if m == nil {
		return title
	}
	head := foldText(m[1])
	for _, a := range authors {
		sort := authorSort(a)
		if strings.Contains(sort, ", ") && !tooLong(a) && foldText(sort) == head {
			return m[2]
		}
	}
	return title
}
