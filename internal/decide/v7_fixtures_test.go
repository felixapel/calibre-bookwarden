package decide_test

import (
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

// v7Row is a book whose files agree with one source record while a second
// record, from another source, spells the title or the authors differently.
// Run 10 had 40 of these moved from tier A to C on a records conflict.
type v7Row struct {
	name        string
	lang        string // the files' language, as a two-letter code
	fileTitle   string
	fileAuthors []string
	olTitle     string
	olAuthors   []string
	gbTitle     string
	gbAuthors   []string
	// reasons the book had up to version 6, which all conflicts shared.
	v6 []string
	// want7 is the verdict from version 7 on: tier A when true, else the
	// version 6 decision stays.
	want7 bool
}

const (
	titleConflict   = "records_title_conflict"
	authorsConflict = "records_authors_conflict"
)

func names(n ...string) []string { return n }

// v7Rows are the run 10 shapes, positives first, then the near misses that
// must stay conflicts.
func v7Rows() []v7Row {
	freud, zweig, dahl := "Sigmund Freud", "Stefan Zweig", "Roald Dahl"
	return []v7Row{
		{name: "leading article dropped by Open Library", lang: "en", fileTitle: "The Precipice", fileAuthors: names("Toby Ord"),
			olTitle: "Precipice", olAuthors: names("Toby Ord"), gbTitle: "The Precipice", gbAuthors: names("Toby Ord"),
			v6: []string{titleConflict}, want7: true},
		{name: "the before a single word", lang: "en", fileTitle: "The Secret", fileAuthors: names("Rhonda Byrne"),
			olTitle: "Secret", olAuthors: names("Rhonda Byrne"), gbTitle: "The Secret", gbAuthors: names("Rhonda Byrne"),
			v6: []string{titleConflict}, want7: true},
		{name: "a before a phrase", lang: "en", fileTitle: "A Sport of Nature", fileAuthors: names("Nadine Gordimer"),
			olTitle: "Sport of Nature", olAuthors: names("Nadine Gordimer"), gbTitle: "A Sport of Nature", gbAuthors: names("Nadine Gordimer"),
			v6: []string{titleConflict}, want7: true},
		{name: "article before the subtitle", lang: "en", fileTitle: "The Complete Survival Shelters Handbook: A Step-By-Step Guide", fileAuthors: names("Dave Canterbury"),
			olTitle: "Complete Survival Shelters Handbook : A Step-By-Step Guide", olAuthors: names("Dave Canterbury"),
			gbTitle: "The Complete Survival Shelters Handbook: A Step-By-Step Guide", gbAuthors: names("Dave Canterbury"),
			v6: []string{titleConflict}, want7: true},
		{name: "German article", lang: "de", fileTitle: "Der Prozess", fileAuthors: names("Franz Kafka"),
			olTitle: "Prozess", olAuthors: names("Franz Kafka"), gbTitle: "Der Prozess", gbAuthors: names("Franz Kafka"),
			v6: []string{titleConflict}, want7: true},
		{name: "ampersand against and", lang: "en", fileTitle: "Build a Pond for Food and Fun", fileAuthors: names("Tom Wild"),
			olTitle: "Build a Pond for Food & Fun", olAuthors: names("Tom Wild"), gbTitle: "Build a Pond for Food and Fun", gbAuthors: names("Tom Wild"),
			v6: []string{titleConflict}, want7: true},
		{name: "mojibake apostrophe", lang: "en", fileTitle: "A Homeowner's Guide", fileAuthors: names("Pat Roe"),
			olTitle: "A Homeowner?s Guide", olAuthors: names("Pat Roe"), gbTitle: "A Homeowner's Guide", gbAuthors: names("Pat Roe"),
			v6: []string{titleConflict}, want7: true},
		{name: "curly apostrophe", lang: "en", fileTitle: "A Homeowner's Guide", fileAuthors: names("Pat Roe"),
			olTitle: "A Homeowner’s Guide", olAuthors: names("Pat Roe"), gbTitle: "A Homeowner's Guide", gbAuthors: names("Pat Roe"),
			v6: []string{titleConflict}, want7: true},
		{name: "honorific", lang: "en", fileTitle: "The Obesity Code", fileAuthors: names("Jason Fung"),
			olTitle: "The Obesity Code", olAuthors: names("Dr. Jason Fung"), gbTitle: "The Obesity Code", gbAuthors: names("Jason Fung"),
			v6: []string{authorsConflict}, want7: true},
		{name: "initials punctuation", lang: "en", fileTitle: "A Passage to India", fileAuthors: names("E. M. Forster"),
			olTitle: "A Passage to India", olAuthors: names("E M Forster"), gbTitle: "A Passage to India", gbAuthors: names("E. M. Forster"),
			v6: []string{authorsConflict}, want7: true},
		{name: "imprint in the title, initials", lang: "en", fileTitle: "A Passage to India", fileAuthors: names("E. M. Forster"),
			olTitle: "Penguin Classics Passage To India", olAuthors: names("E M Forster"), gbTitle: "A Passage to India (Penguin Classics)", gbAuthors: names("E. M. Forster"),
			v6: []string{authorsConflict, titleConflict}, want7: true},
		{name: "extra parenthetical subtitle from Google", lang: "en", fileTitle: "The Obesity Code", fileAuthors: names("Jason Fung"),
			olTitle: "The Obesity Code", olAuthors: names("Jason Fung"),
			gbTitle: "The Obesity Code (Why Fasting Is the Key to Weight Loss)", gbAuthors: names("Jason Fung"),
			v6: []string{titleConflict}, want7: true},
		{name: "contributor: Freud and Gay", lang: "en", fileTitle: "Civilization", fileAuthors: names(freud),
			olTitle: "Civilization", olAuthors: names(freud, "Peter Gay"), gbTitle: "Civilization", gbAuthors: names(freud),
			v6: []string{authorsConflict}, want7: true},
		{name: "contributor: Zweig and Bell", lang: "en", fileTitle: "Chess Story", fileAuthors: names(zweig),
			olTitle: "Chess Story", olAuthors: names(zweig, "Anthea Bell"), gbTitle: "Chess Story", gbAuthors: names(zweig),
			v6: []string{authorsConflict}, want7: true},
		{name: "contributor: Dahl and Blake", lang: "en", fileTitle: "The BFG", fileAuthors: names(dahl),
			olTitle: "The BFG", olAuthors: names(dahl, "Quentin Blake"), gbTitle: "The BFG", gbAuthors: names(dahl),
			v6: []string{authorsConflict}, want7: true},
		{name: "contributor: Bellow and Coetzee", lang: "en", fileTitle: "Herzog", fileAuthors: names("Saul Bellow"),
			olTitle: "Herzog", olAuthors: names("Saul Bellow", "J. M. Coetzee"), gbTitle: "Herzog", gbAuthors: names("Saul Bellow"),
			v6: []string{authorsConflict}, want7: true},
		{name: "contributor: Schopenhauer and Hollingdale", lang: "en", fileTitle: "Essays and Aphorisms", fileAuthors: names("Arthur Schopenhauer"),
			olTitle: "Essays and Aphorisms", olAuthors: names("Arthur Schopenhauer", "Hollingdale, R. J."), gbTitle: "Essays and Aphorisms", gbAuthors: names("Arthur Schopenhauer"),
			v6: []string{authorsConflict}, want7: true},
		{name: "contributor: Joyce and Gabler Gabler", lang: "en", fileTitle: "Ulysses", fileAuthors: names("James Joyce"),
			olTitle: "Ulysses", olAuthors: names("James Joyce", "Hans Walter Gabler Gabler"), gbTitle: "Ulysses", gbAuthors: names("James Joyce"),
			v6: []string{authorsConflict}, want7: true},
		{name: "two contributors", lang: "en", fileTitle: "Ulysses", fileAuthors: names("James Joyce"),
			olTitle: "Ulysses", olAuthors: names("James Joyce", "Hans Walter Gabler", "Wolfhard Steppe"), gbTitle: "Ulysses", gbAuthors: names("James Joyce"),
			v6: []string{authorsConflict}, want7: true},

		// Near misses: version 7 must derive exactly what version 6 did.
		{name: "Man against A Man", lang: "en", fileTitle: "A Man", fileAuthors: names("Oriana Fallaci"),
			olTitle: "Man", olAuthors: names("Oriana Fallaci"), gbTitle: "A Man", gbAuthors: names("Oriana Fallaci"),
			v6: []string{titleConflict}},
		{name: "The Road against Road to Serfdom", lang: "en", fileTitle: "The Road", fileAuthors: names("Cormac McCarthy"),
			olTitle: "Road to Serfdom", olAuthors: names("Cormac McCarthy"), gbTitle: "The Road", gbAuthors: names("Cormac McCarthy"),
			v6: []string{titleConflict}},
		{name: "Dune against Dune Messiah", lang: "en", fileTitle: "Dune", fileAuthors: names("Frank Herbert"),
			olTitle: "Dune Messiah", olAuthors: names("Frank Herbert"), gbTitle: "Dune", gbAuthors: names("Frank Herbert"),
			v6: []string{titleConflict}},
		{name: "Tom and Jerry against a longer title", lang: "en", fileTitle: "Tom & Jerry", fileAuthors: names("W. Hanna"),
			olTitle: "Tom and Jerry's Big Adventure", olAuthors: names("W. Hanna"), gbTitle: "Tom & Jerry", gbAuthors: names("W. Hanna"),
			v6: []string{titleConflict}},
		{name: "volume in parentheses", lang: "de", fileTitle: "Der Zauberberg", fileAuthors: names("Thomas Mann"),
			olTitle: "Der Zauberberg (Band 1)", olAuthors: names("Thomas Mann"), gbTitle: "Der Zauberberg", gbAuthors: names("Thomas Mann"),
			v6: []string{titleConflict}},
		{name: "different first author, superset", lang: "en", fileTitle: "Civilization", fileAuthors: names(freud),
			olTitle: "Civilization", olAuthors: names("Peter Gay", freud), gbTitle: "Civilization", gbAuthors: names(freud),
			v6: []string{authorsConflict}},
		{name: "three extra names", lang: "en", fileTitle: "Ulysses", fileAuthors: names("James Joyce"),
			olTitle: "Ulysses", olAuthors: names("James Joyce", "A One", "B Two", "C Three"), gbTitle: "Ulysses", gbAuthors: names("James Joyce"),
			v6: []string{authorsConflict}},
		{name: "another author", lang: "en", fileTitle: "Ulysses", fileAuthors: names("James Joyce"),
			olTitle: "Ulysses", olAuthors: names("Homer"), gbTitle: "Ulysses", gbAuthors: names("James Joyce"),
			v6: []string{authorsConflict}},
		{name: "the same size, one name differs", lang: "en", fileTitle: "Ulysses", fileAuthors: names("James Joyce", "Stuart Gilbert"),
			olTitle: "Ulysses", olAuthors: names("James Joyce", "Declan Kiberd"), gbTitle: "Ulysses", gbAuthors: names("James Joyce", "Stuart Gilbert"),
			v6: []string{authorsConflict}},
		{name: "two different parentheticals (1914-1918) (1939-1945)", lang: "en", fileTitle: "Diaries (1939-1945)", fileAuthors: names("Tom Wild"),
			olTitle: "Diaries (1914-1918)", olAuthors: names("Tom Wild"), gbTitle: "Diaries (1939-1945)", gbAuthors: names("Tom Wild"),
			v6: []string{titleConflict}},
		{name: "two different parentheticals (One) (Two)", lang: "en", fileTitle: "Diaries (Two)", fileAuthors: names("Tom Wild"),
			olTitle: "Diaries (One)", olAuthors: names("Tom Wild"), gbTitle: "Diaries (Two)", gbAuthors: names("Tom Wild"),
			v6: []string{titleConflict}},
		{name: "two different parentheticals (First of Two) (Second of Two)", lang: "en", fileTitle: "Diaries (Second of Two)", fileAuthors: names("Tom Wild"),
			olTitle: "Diaries (First of Two)", olAuthors: names("Tom Wild"), gbTitle: "Diaries (Second of Two)", gbAuthors: names("Tom Wild"),
			v6: []string{titleConflict}},
		{name: "two different parentheticals (The Early Years) (The Later Years)", lang: "en", fileTitle: "Diaries (The Later Years)", fileAuthors: names("Tom Wild"),
			olTitle: "Diaries (The Early Years)", olAuthors: names("Tom Wild"), gbTitle: "Diaries (The Later Years)", gbAuthors: names("Tom Wild"),
			v6: []string{titleConflict}},
		{name: "two different parentheticals (Abridged) (Unabridged)", lang: "en", fileTitle: "Diaries (Unabridged)", fileAuthors: names("Tom Wild"),
			olTitle: "Diaries (Abridged)", olAuthors: names("Tom Wild"), gbTitle: "Diaries (Unabridged)", gbAuthors: names("Tom Wild"),
			v6: []string{titleConflict}},
		{name: "two different parentheticals (2nd Edition) (3rd Edition)", lang: "en", fileTitle: "Diaries (3rd Edition)", fileAuthors: names("Tom Wild"),
			olTitle: "Diaries (2nd Edition)", olAuthors: names("Tom Wild"), gbTitle: "Diaries (3rd Edition)", gbAuthors: names("Tom Wild"),
			v6: []string{titleConflict}},
		{name: "file [A,C], Open Library [A,B], Google [A]", lang: "en", fileTitle: "Stories", fileAuthors: names("Ann Able", "Cy Cole"),
			olTitle: "Stories", olAuthors: names("Ann Able", "Bo Bell"), gbTitle: "Stories", gbAuthors: names("Ann Able"),
			v6: []string{authorsConflict, "records_disagree"}},
	}
}

func (r v7Row) pkg() evidence.Package {
	file := epub(r.fileTitle, r.fileAuthors, []string{r.lang}, []string{I})
	file.Metadata.Publisher = "Acme Press"
	ol := rec("/books/OL9M", r.olTitle, r.olAuthors, []string{"eng"}, I)
	gb := rec("g1", r.gbTitle, r.gbAuthors, []string{"eng"}, I)
	if r.lang != "en" {
		ol.Languages, gb.Languages = []string{r.lang}, []string{r.lang}
	}
	ol.Publisher, gb.Publisher = "Acme Press", "Acme Press"
	p := book([]evidence.FormatEvidence{file}, lookup(I, []string{evidence.SeedFile},
		ok(evidence.SourceOpenLibrary, ol), ok(evidence.SourceGoogleBooks, gb)))
	p.Calibre.Title, p.Calibre.Authors, p.Calibre.Languages = r.fileTitle, r.fileAuthors, []string{"eng"}
	if r.lang != "en" {
		p.Calibre.Languages = []string{r.lang}
	}
	p.Calibre.Publisher = "Old House"
	return p
}
