package findings

// Findings version 5 (ADR-035, "Findings v5") adds detectors for Calibre
// values that ADR-031 says are junk. They only add findings: nothing here
// proposes a value, authorizes a fix or queries a source. Each reads Calibre's
// record as sealed in the package and nothing else, never the wall clock, and
// each uses a closed list or pattern that lives in this file. When a value is
// ambiguous, no finding is derived.

import (
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

// calibreHygiene derives the version-5 findings of one Calibre record.
func (d *derivation) calibreHygiene(rec evidence.Record) {
	if r := junkTitleReason(rec.Title); r != "" {
		d.add("junk_title", "", "title", r)
	}
	authors := rec.Authors
	if listTooLong(authors) {
		authors = nil
	}
	for _, r := range titleNoiseReasons(rec.Title, authors) {
		d.add("title_noise", "", "title", r)
	}
	// A joined entry is a finding only. The fix path can't write it back:
	// fix/rules.go refuses author names holding "&" or "|", because Calibre's
	// set-fields rewrites them and the before-value couldn't be restored.
	for _, a := range authors {
		if r := joinedAuthorReason(a, rec.Publisher); r != "" {
			d.add("joined_authors", "", "authors", r)
		}
	}
	if r := junkPublisherReason(rec.Publisher); r != "" {
		d.add("junk_publisher", "", "publisher", r)
	}
}

// tidy collapses whitespace and normalizes to NFC, keeping case.
func tidy(s string) string { return norm.NFC.String(strings.Join(strings.Fields(s), " ")) }

// foldText is tidy, case-folded, with curly apostrophes made straight.
func foldText(s string) string {
	return strings.NewReplacer("’", "'", "‘", "'").Replace(cases.Fold().String(tidy(s)))
}

func tooLong(s string) bool { return utf8.RuneCountInString(s) > maxFieldRunes }

// startsUpper reports whether a word, ignoring opening brackets and quotes,
// begins with an uppercase letter.
func startsUpper(word string) bool {
	r, _ := utf8.DecodeRuneInString(strings.TrimLeft(word, "([{\"'“‘"))
	return unicode.IsUpper(r)
}

// ---- junk_title ----

// .txt is not on the list: "README.txt" and the like are too often real.
const documentExtensions = `(?:pdf|epub|mobi|azw3|doc|docx|rtf|indd|odt)`

// junkTitleRules is the closed list of artefact patterns, tried in order; the
// first match is the finding's reason.
//
//   - word_artefact: Word's PDF export names the document "Microsoft Word -
//     <file name>". The prefix alone is not enough, because "Microsoft Word -
//     The Missing Manual" is a plausible real title: the rest must be a file
//     name (it ends in a document extension) or Word's default "Document1".
//   - file_extension: the title ends in a document extension glued to a name
//     (.pdf .epub .mobi .azw3 .doc .docx .rtf .indd .odt).
//   - untitled: an editor's default name: Untitled, Untitled Document,
//     untitled-1.
//   - number_only: digits and at least one of "_", "-", ".", with a run of six
//     digits or more: a date stamp or scan number used as a name. A value of
//     digits only is never flagged ("1984", "100000", an ISBN), nor are
//     "2001-2010" or "11/22/63".
//
// Frozen: decision versions 8 and 9 read these rules through JunkTitleReason;
// a change here changes those decisions.
var junkTitleRules = []struct {
	reason string
	match  func(string) bool
}{
	{"word_artefact", func(t string) bool {
		m := wordArtefact.FindStringSubmatch(t)
		return m != nil && (fileNameEnd.MatchString(m[1]) || wordDefaultName.MatchString(m[1]))
	}},
	{"file_extension", fileNameEnd.MatchString},
	{"untitled", regexp.MustCompile(`(?i)^untitled(?:[ _-]?document)?(?:[ _-]?[0-9]+)?$`).MatchString},
	{"number_only", func(t string) bool {
		return numberShape.MatchString(t) && strings.ContainsAny(t, "_-.") && digitRun.MatchString(t)
	}},
}

var (
	wordArtefact    = regexp.MustCompile(`(?i)^microsoft word\s*[-–—]\s*(.+)$`)
	wordDefaultName = regexp.MustCompile(`(?i)^document[0-9]*$`)
	fileNameEnd     = regexp.MustCompile(`(?i)\S\.` + documentExtensions + `$`)
	numberShape     = regexp.MustCompile(`^[0-9_.-]+$`)
	digitRun        = regexp.MustCompile(`[0-9]{6}`)
)

// junkTitleReason names the artefact pattern a Calibre title matches, or "".
func junkTitleReason(title string) string {
	if tooLong(title) {
		return ""
	}
	t := tidy(title)
	for _, rule := range junkTitleRules {
		if t != "" && rule.match(t) {
			return rule.reason
		}
	}
	return ""
}

// ---- title_noise ----

// seriesImprints is the closed list of publisher and imprint series (ADR-031,
// Title 2 and Series 1) that count as noise only in parentheses. A narrative
// series such as "(Dune Chronicles #1)" is not on it: that moves to the series
// fields, which is a proposal and out of scope here. Lower case, straight
// apostrophes.
var seriesImprints = []string{
	"penguin classics", "penguin modern classics", "oxford world's classics", "everyman's library",
	"vintage international", "vintage classics", "signet classics", "signet books", "dover thrift editions", "modern library classics",
}

// SeriesImprints returns the closed list of publisher and imprint series, in
// lower case with straight apostrophes. Decision version 7 reads it to ignore
// an imprint name in a title (ADR-030 amendment); extending it needs an ADR
// change and, for the decision, a new version.
//
// Frozen: decision versions 7 to 9 call these without a version, and their
// sealed packages are replayed with them. A change here changes those
// decisions: add the new behaviour behind a new decision version instead.
func SeriesImprints() []string { return slices.Clone(seriesImprints) }

// editionCredits is the closed set of parenthesised edition credits, in
// parentheses or square brackets only: a printed title such as "The Annotated
// Alice" is not matched.
const editionCredits = `annotated(?: edition)?|illustrated(?: edition)?|kindle edition|` +
	`(?:revised|expanded|revised and expanded|special|deluxe|collector's) edition|` +
	`[0-9]{1,3}(?:st|nd|rd|th) anniversary(?: edition)?`

var (
	seriesImprint = regexp.MustCompile(`\(\s*(?:` + strings.Join(quoteAll(seriesImprints), "|") + `)\s*\)`)
	editionCredit = regexp.MustCompile(`[(\[]\s*(?:` + editionCredits + `)\s*[)\]]`)
	// translatorCredit finds the credit, which must sit in parentheses or
	// brackets or follow " - ", ": " or " / ", so "Lost Words Translated By
	// Machine Learning" is not one. The two words after it must look like a
	// name (both capitalised), so "(Translated by Heart)" is not either.
	translatorCredit = regexp.MustCompile(`(?i)(?:[(\[]\s*|\s[-–—]\s+|:\s+|\s/\s+)(?:translated by|übersetzt von|traducido por)\s+(\S+)\s+(\S+)`)
	dashes           = []string{" - ", " – ", " — "}
)

func quoteAll(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = regexp.QuoteMeta(s)
	}
	return out
}

// titleNoiseReasons lists, in a fixed order, which closed kinds of noise a
// Calibre title carries: series_imprint, translator_credit, author_prefix
// (the title starts with "Surname, Given - " for one of the book's Calibre
// authors) and edition_credit.
func titleNoiseReasons(title string, authors []string) []string {
	if tooLong(title) {
		return nil
	}
	t := tidy(title)
	folded := foldText(t)
	var reasons []string
	if seriesImprint.MatchString(folded) {
		reasons = append(reasons, "series_imprint")
	}
	for _, m := range translatorCredit.FindAllStringSubmatch(t, -1) {
		if startsUpper(m[1]) && startsUpper(m[2]) {
			reasons = append(reasons, "translator_credit")
			break
		}
	}
	if hasAuthorPrefix(folded, authors) {
		reasons = append(reasons, "author_prefix")
	}
	if editionCredit.MatchString(folded) {
		reasons = append(reasons, "edition_credit")
	}
	return reasons
}

// hasAuthorPrefix: the folded title begins with an author's sort form, which
// is "Surname, Given", then a dash and some title text. The sort form comes
// from the same table as the author-sort finding.
func hasAuthorPrefix(folded string, authors []string) bool {
	for _, a := range authors {
		sort := authorSort(a)
		if !strings.Contains(sort, ", ") || tooLong(a) {
			continue
		}
		key := foldText(sort)
		for _, dash := range dashes {
			if rest, ok := strings.CutPrefix(folded, key+dash); ok && strings.TrimSpace(rest) != "" {
				return true
			}
		}
	}
	return false
}

// ---- joined_authors ----

// joinSeparators is the closed list. Every separator but "; " needs a full
// name (two words or more) on each side: "Ortega y Gasset" is one Spanish name,
// "Pride and Prejudice" is not a pair of people, and "Taylor & Francis" or
// "Strunk & White" can't be told from a firm. "; " may join single surnames.
// For the word separators a right side that starts with a lower-case name
// particle ("y de la Peña") continues one name and gets no finding. An entry holding two different separators is
// ambiguous and gets no finding.
type joinSeparator struct {
	reason, sep string
	surnames    bool
}

var joinSeparators = []joinSeparator{
	{"semicolon", "; ", true}, {"ampersand", " & ", false},
	{"und", " und ", false}, {"and", " and ", false}, {"et", " et ", false}, {"y", " y ", false},
}

// corporateAuthors are folded names that contain a joining word and name one
// body: publishers and firms that appear in Calibre's author slot.
var corporateAuthors = map[string]bool{
	"simon & schuster": true, "procter & gamble": true, "barnes & noble": true, "harper & row": true,
	"marks & spencer": true, "johnson & johnson": true, "black & decker": true, "ernst & young": true,
	"hodder & stoughton": true, "farrar, straus & giroux": true, "little, brown & co": true, "little, brown & company": true,
}

// notNameWords mark a side as a company or a "and others" tail rather than a
// person. Folded, without trailing dots.
var notNameWords = map[string]bool{
	"co": true, "company": true, "inc": true, "ltd": true, "llc": true, "gmbh": true, "sons": true, "son": true,
	"brothers": true, "bros": true, "associates": true, "partners": true, "others": true,
	"press": true, "publishers": true, "publishing": true, "verlag": true, "editors": true, "staff": true, "team": true,
}

// nameParticles may be lower case inside a name.
var nameParticles = map[string]bool{
	"van": true, "von": true, "der": true, "den": true, "de": true, "la": true, "le": true, "du": true, "di": true, "da": true,
	"del": true, "della": true, "dos": true, "das": true, "ter": true, "ten": true, "bin": true, "ibn": true, "al": true, "el": true,
}

const maxNameWords = 4

// wordy: the separator is a word, not "&" or ";".
func (s joinSeparator) wordy() bool { return s.reason != "semicolon" && s.reason != "ampersand" }

// startsWithParticle: the part begins with a lower-case name particle.
func startsWithParticle(part string) bool {
	first, _, _ := strings.Cut(strings.TrimSpace(part), " ")
	return !startsUpper(first) && nameParticles[foldText(first)]
}

// joinedAuthorReason says which closed separator joins two or more people in
// one Calibre author entry, or "". publisher is Calibre's publisher: an entry
// that equals it is the publisher in the author slot, not two people.
func joinedAuthorReason(entry, publisher string) string {
	if tooLong(entry) {
		return ""
	}
	e := tidy(entry)
	folded := foldText(e)
	if folded == "" || corporateAuthors[folded] || folded == foldText(publisher) {
		return ""
	}
	found := -1
	for i, s := range joinSeparators {
		if strings.Contains(e, s.sep) {
			if found >= 0 {
				return ""
			}
			found = i
		}
	}
	if found < 0 {
		return ""
	}
	sep := joinSeparators[found]
	for i, part := range strings.Split(e, sep.sep) {
		if !looksLikeName(part, sep.surnames) || i > 0 && sep.wordy() && startsWithParticle(part) {
			return ""
		}
	}
	return sep.reason
}

// looksLikeName: one to four words made of letters, ".", "-" and apostrophes
// (a "Surname, Given" part has one comma and at least two words), with at
// least one capitalised word, no digits and no company word. Lower-case words
// must be name particles.
func looksLikeName(part string, singleWord bool) bool {
	part = strings.TrimSpace(part)
	commas := strings.Count(part, ",")
	if part == "" || commas > 1 {
		return false
	}
	words := strings.Fields(strings.ReplaceAll(part, ",", " "))
	if len(words) == 0 || len(words) > maxNameWords || (len(words) == 1 && (!singleWord || commas > 0)) {
		return false
	}
	capitalised := false
	for _, w := range words {
		for _, r := range w {
			if !unicode.IsLetter(r) && !strings.ContainsRune(".-'’", r) {
				return false
			}
		}
		key := strings.TrimRight(foldText(w), ".")
		switch {
		case notNameWords[key]:
			return false
		case startsUpper(w):
			capitalised = true
		case nameParticles[key] || strings.HasPrefix(key, "d'") || strings.HasPrefix(key, "l'"):
		default:
			return false
		}
	}
	return capitalised
}

// ---- junk_publisher ----

// Closed lists of junk publishers (ADR-031, Publisher 3). Folded values.
var (
	// placeholderPublishers are stand-ins for "no publisher". Only a value
	// equal to one of them, ignoring brackets and edge punctuation, counts:
	// "Calibre Press" and "Unknown Pleasures Press" are not matched.
	placeholderPublishers = map[string]bool{
		"unknown": true, "unbekannt": true, "desconocido": true, "n/a": true, "none": true, "publisher": true, "calibre": true,
	}
	// scannerSite matches shadow-library names at the start of the value or
	// after a non-letter, so "Muñoz-Libros" (...z-Libros) and "Kranz-Libri" are
	// not matches. "scan" counts only as the whole value.
	scannerSite = regexp.MustCompile(`(?:^|[^\p{L}])(?:z-?lib(?:rary)?|libgen|library genesis)`)
	// realDomainPublishers are real names that would otherwise read as a
	// website once "www." or a scheme is stripped ("www.lulu.com" is Lulu).
	realDomainPublishers = map[string]bool{
		"lulu.com": true, "tor.com": true, "smashwords.com": true, "createspace.com": true, "amazon.com": true,
		"draft2digital.com": true, "bookbaby.com": true, "kobo.com": true,
	}
)

// junkPublisherReason names the closed list Calibre's publisher is on:
// placeholder, website (written as "www.…" or "http(s)://…") or scanner_site;
// or "". A bare domain such as "Baen.com" is an imprint as printed (ADR-031) and
// is not flagged.
func junkPublisherReason(publisher string) string {
	if tooLong(publisher) {
		return ""
	}
	p := strings.Trim(foldText(publisher), "[](){}<> .-_\"'")
	if p == "" {
		return ""
	}
	if p == "scan" {
		return "scanner_site"
	}
	if scannerSite.MatchString(p) {
		return "scanner_site"
	}
	if placeholderPublishers[p] {
		return "placeholder"
	}
	bare := strings.TrimPrefix(strings.TrimPrefix(p, "https://"), "http://")
	written := bare != p
	if rest, ok := strings.CutPrefix(bare, "www."); ok {
		bare, written = rest, true
	}
	if written && !realDomainPublishers[bare] {
		return "website"
	}
	return ""
}
