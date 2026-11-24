package extract

import (
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

// unitText is one spine document of a synthetic excerpt.
type unitText struct{ landmark, text string }

// excerptOf builds an excerpt the way the inspector does, so units and offsets
// are the real ones. Every text here is synthetic.
func excerptOf(t testing.TB, units ...unitText) Excerpt {
	t.Helper()
	b := evidence.NewExcerptBuilder(evidence.ExcerptEPUBText)
	for i, u := range units {
		b.Add(i+1, u.landmark, u.text)
	}
	e := b.Excerpt()
	if e == nil {
		t.Fatal("empty excerpt")
	}
	return NewExcerpt(*e)
}

func single(t testing.TB, landmark, text string) Excerpt {
	return excerptOf(t, unitText{landmark, text})
}

func TestVerifyGroundsTitleWithOffsets(t *testing.T) {
	x := single(t, evidence.LandmarkTitle, "THE LANTERN YEARS\nA Novel\nby Jane Doe")
	v := Verify(x, Claim{Field: FieldTitle, Value: "THE LANTERN YEARS", Quote: "THE LANTERN YEARS", Source: "A"})
	if v.Status != StatusGrounded {
		t.Fatalf("status = %q, want grounded", v.Status)
	}
	if v.Unit != 1 || v.Start != 0 || v.End != 17 {
		t.Errorf("unit/start/end = %d/%d/%d, want 1/0/17", v.Unit, v.Start, v.End)
	}
	if got := string([]rune(x.Text)[v.Start:v.End]); got != "THE LANTERN YEARS" {
		t.Errorf("slice = %q", got)
	}
	if v.Source != "A" {
		t.Errorf("source lost: %q", v.Source)
	}
}

func TestVerifyQuoteSearch(t *testing.T) {
	front := excerptOf(t,
		unitText{evidence.LandmarkTitle, "THE LANTERN YEARS\nby Jane Doe"},
		unitText{evidence.LandmarkCopyright, "Copyright © 2019 Jane Doe\nPublished by Harbor House Books\nThe Lantern Years was set in Garamond."},
	)
	tests := []struct {
		name  string
		claim Claim
		want  string
		unit  int
	}{
		{"exact", Claim{Field: FieldTitle, Value: "THE LANTERN YEARS", Quote: "THE LANTERN YEARS"}, StatusGrounded, 1},
		{"quote wraps lines", Claim{Field: FieldContributor, Value: "Jane Doe", Quote: "by\nJane Doe"}, StatusGrounded, 1},
		{"a title doesn't wrap into its by-line", Claim{Field: FieldTitle, Value: "THE LANTERN YEARS by Jane Doe", Quote: "THE LANTERN YEARS\nby Jane Doe"}, StatusValueNotDerived, 1},
		{"absent", Claim{Field: FieldTitle, Value: "The Glass Orchard", Quote: "The Glass Orchard"}, StatusNotFound, 0},
		{"extra letter", Claim{Field: FieldTitle, Value: "THE LANTERNS YEARS", Quote: "THE LANTERNS YEARS"}, StatusNotFound, 0},
		{"case differs", Claim{Field: FieldTitle, Value: "The Lantern Years", Quote: "The Lantern Years"}, StatusGrounded, 2}, // the colophon line
		{"empty quote", Claim{Field: FieldImprint, Value: "x", Quote: ""}, StatusNotFound, 0},
		{"blank quote", Claim{Field: FieldImprint, Value: "x", Quote: "  \n "}, StatusNotFound, 0},
		{"one rune title", Claim{Field: FieldTitle, Value: "A", Quote: "A"}, StatusNotFound, 0},
		{"one rune subtitle", Claim{Field: FieldSubtitle, Value: "A", Quote: "A"}, StatusNotFound, 0},
		{"inside a word", Claim{Field: FieldImprint, Value: "Harbor Hou", Quote: "Harbor Hou"}, StatusNotFound, 0},
		{"start inside a word", Claim{Field: FieldImprint, Value: "arbor House Books", Quote: "arbor House Books"}, StatusNotFound, 0},
		{"occurs in two units", Claim{Field: FieldContributor, Value: "Jane Doe", Quote: "Jane Doe"}, StatusAmbiguous, 0}, // a role is read from around the quote
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := Verify(front, tt.claim)
			if v.Status != tt.want {
				t.Fatalf("status = %q, want %q (%+v)", v.Status, tt.want, v)
			}
			if tt.want == StatusGrounded && v.Unit != tt.unit {
				t.Errorf("unit = %d, want %d", v.Unit, tt.unit)
			}
		})
	}
}

func TestVerifyRepeatInOneUnitUsesFirst(t *testing.T) {
	x := single(t, evidence.LandmarkCopyright, "Published by Harbor House Books.\nHarbor House Books is a trademark.")
	v := Verify(x, Claim{Field: FieldImprint, Value: "Harbor House Books", Quote: "Harbor House Books"})
	if v.Status != StatusGrounded || v.Start != 13 {
		t.Fatalf("got %+v, want grounded at the first occurrence (13)", v)
	}
}

func TestVerifyNormalisedMatches(t *testing.T) {
	tests := []struct {
		name, text, quote, value string
		layer                    bool // a PDF text layer, which joins hyphenation
	}{
		{"hyphenated line break in a text layer", "The Chronicles of Nar-\nnia\nby Jane Doe", "The Chronicles of Narnia", "The Chronicles of Narnia", true},
		{"curly apostrophe", "The Angel’s Orchard\nby Jane Doe", "The Angel's Orchard", "The Angel's Orchard", false},
		{"curly quote in the quote", "The Angel's Orchard", "The Angel’s Orchard", "The Angel’s Orchard", false},
		{"soft hyphen", "Lantern\u00adkeeper\nby Jane Doe", "Lanternkeeper", "Lanternkeeper", false},
		{"double spaces", "Harbor   House\tBooks", "Harbor House Books", "Harbor House Books", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			x := single(t, evidence.LandmarkTitle, tt.text)
			if tt.layer {
				x = textLayer(t, evidence.LandmarkTitle, tt.text)
			}
			v := Verify(x, Claim{Field: FieldTitle, Value: tt.value, Quote: tt.quote})
			if v.Status != StatusGrounded {
				t.Fatalf("status = %q: %+v", v.Status, v)
			}
			// The span, normalised, is the quote: the offsets point at the book's own text.
			orig := []rune(x.Text)[v.Start:v.End]
			if got, want := fold(string(orig)), fold(tt.quote); got != want {
				t.Errorf("slice %q folds to %q, want %q", string(orig), got, want)
			}
		})
	}
}

func TestVerifyNeverChangesLettersOrDigits(t *testing.T) {
	tests := []struct {
		name, text, quote string
	}{
		{"superscript is not 2", "Lantern²\nby Jane Doe", "Lantern2"},
		{"superscript quote against digit", "Lantern2\nby Jane Doe", "Lantern²"},
		{"ligature is not fi", "ﬁnal Harvest\nby Jane Doe", "final Harvest"},
		{"ligature in quote", "final Harvest\nby Jane Doe", "ﬁnal Harvest"},
		{"fullwidth digits", "Volume １２\nby Jane Doe", "Volume 12"},
		{"quote stops before superscript", "Lantern²\nby Jane Doe", "Lantern"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			x := single(t, evidence.LandmarkTitle, tt.text)
			v := Verify(x, Claim{Field: FieldTitle, Value: tt.quote, Quote: tt.quote})
			if v.Status != StatusNotFound {
				t.Fatalf("status = %q, want not_found: %+v", v.Status, v)
			}
		})
	}
}

func TestVerifyTitleTransform(t *testing.T) {
	x := single(t, evidence.LandmarkTitle, "“Who Moved the Lantern?”\n— A Field Guide —\n(Untitled)\nThe Glass Orchard.\n— —")
	tests := []struct {
		name  string
		claim Claim
		want  string
		value string // the derived value, when grounded
	}{
		{"trims quotes, keeps the question mark", Claim{Field: FieldTitle, Value: "Who Moved the Lantern?", Quote: "“Who Moved the Lantern?”"}, StatusGrounded, "Who Moved the Lantern?"},
		{"value with the quote marks still derives", Claim{Field: FieldTitle, Value: "\"Who Moved the Lantern?\"", Quote: "“Who Moved the Lantern?”"}, StatusGrounded, "Who Moved the Lantern?"},
		{"trims dashes", Claim{Field: FieldSubtitle, Value: "A Field Guide", Quote: "— A Field Guide —"}, StatusGrounded, "A Field Guide"},
		{"trims a wrapping bracket", Claim{Field: FieldTitle, Value: "Untitled", Quote: "(Untitled)"}, StatusGrounded, "Untitled"},
		{"trims a full stop", Claim{Field: FieldTitle, Value: "The Glass Orchard", Quote: "The Glass Orchard."}, StatusGrounded, "The Glass Orchard"},
		{"case folds, the print is kept (v4)", Claim{Field: FieldTitle, Value: "The glass orchard", Quote: "The Glass Orchard."}, StatusGrounded, "The Glass Orchard"},
		{"a word dropped", Claim{Field: FieldTitle, Value: "Glass Orchard", Quote: "The Glass Orchard."}, StatusValueNotDerived, ""},
		{"a word added", Claim{Field: FieldTitle, Value: "The Glass Orchard Revisited", Quote: "The Glass Orchard."}, StatusValueNotDerived, ""},
		{"empty value", Claim{Field: FieldTitle, Value: "", Quote: "The Glass Orchard."}, StatusValueNotDerived, ""},
		{"quote with no letters", Claim{Field: FieldTitle, Value: "—", Quote: "— —"}, StatusValueNotDerived, ""},
		{"unknown field derives nothing", Claim{Field: "language", Value: "en", Quote: "The Glass Orchard."}, StatusValueNotDerived, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := Verify(x, tt.claim)
			if v.Status != tt.want {
				t.Fatalf("status = %q, want %q: %+v", v.Status, tt.want, v)
			}
			if tt.want == StatusGrounded && v.Value != tt.value {
				t.Errorf("derived value = %q, want %q", v.Value, tt.value)
			}
		})
	}
}

func TestVerifyImprintTransform(t *testing.T) {
	x := single(t, evidence.LandmarkCopyright, "Published by Harbor House Books, Inc.\nPrinted by North Press Ltd.\nSecond line.")
	tests := []struct {
		name, quote, value, want string
	}{
		{"keeps the corporate suffix", "Harbor House Books, Inc.", "Harbor House Books, Inc.", StatusGrounded},
		{"suffix dropped from the value", "Harbor House Books, Inc.", "Harbor House Books", StatusValueNotDerived},
		{"keeps Ltd.", "North Press Ltd.", "North Press Ltd.", StatusGrounded},
		{"sentence period is trimmed", "Second line.", "Second line", StatusGrounded},
		{"parent company swapped in", "Harbor House Books, Inc.", "Penguin Random House", StatusValueNotDerived},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := Verify(x, Claim{Field: FieldImprint, Value: tt.value, Quote: tt.quote})
			if v.Status != tt.want {
				t.Fatalf("status = %q, want %q: %+v", v.Status, tt.want, v)
			}
		})
	}
}

func TestVerifySectionFilter(t *testing.T) {
	x := excerptOf(t,
		unitText{evidence.LandmarkTitle, "THE LANTERN YEARS\nby Jane Doe"},
		unitText{evidence.LandmarkCopyright, "Published by Harbor House Books\nAlso by Jane Doe\nThe Glass Orchard\nNight Ferry\n"},
		unitText{evidence.LandmarkFront, "Praise for The Lantern Years\nA luminous debut. The Daily Example\nOtro Libro"},
		unitText{evidence.LandmarkBack, "About the Author\nJane Doe lives by the sea.\nHarbor House Books\n"},
	)
	tests := []struct {
		name  string
		claim Claim
		want  string
	}{
		{"before the heading", Claim{Field: FieldImprint, Value: "Harbor House Books", Quote: "Harbor House Books"}, StatusGrounded}, // the back unit's copy is excluded, so no evidence either way
		{"before the heading, unique", Claim{Field: FieldTitle, Value: "THE LANTERN YEARS", Quote: "THE LANTERN YEARS"}, StatusGrounded},
		{"after Also by", Claim{Field: FieldTitle, Value: "The Glass Orchard", Quote: "The Glass Orchard"}, StatusExcludedSection},
		{"the heading line itself", Claim{Field: FieldTitle, Value: "Also by Jane Doe", Quote: "Also by Jane Doe"}, StatusExcludedSection},
		{"after Praise for", Claim{Field: FieldTitle, Value: "A luminous debut", Quote: "A luminous debut."}, StatusExcludedSection},
		{"back unit", Claim{Field: FieldTitle, Value: "About the Author", Quote: "About the Author"}, StatusExcludedSection},
		{"quote spans into the heading", Claim{Field: FieldTitle, Value: "Harbor House Books Also by Jane Doe", Quote: "Harbor House Books\nAlso by Jane Doe"}, StatusExcludedSection},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if v := Verify(x, tt.claim); v.Status != tt.want {
				t.Fatalf("status = %q, want %q: %+v", v.Status, tt.want, v)
			}
		})
	}
}

func TestVerifySectionHeadings(t *testing.T) {
	headings := []string{
		"Also by Jane Doe", "ALSO BY JANE DOE", "By the same author", "Other books by Jane Doe", "Other titles by Jane Doe",
		"Praise for The Lantern Years", "Coming soon", "Read on for a preview", "Excerpt from The Next Book", "A preview of The Next Book",
		"Weitere Titel von Jane Doe", "Weitere Bücher", "Otros libros de Jane Doe", "Du même auteur",
		"Del mismo autor", //nolint:misspell // Spanish
		"* Also by Jane Doe *",
	}
	for _, h := range headings {
		t.Run(h, func(t *testing.T) {
			x := single(t, evidence.LandmarkFront, "Harbor House Books\n"+h+"\nThe Glass Orchard")
			v := Verify(x, Claim{Field: FieldTitle, Value: "The Glass Orchard", Quote: "The Glass Orchard"})
			if v.Status != StatusExcludedSection {
				t.Errorf("status = %q, want excluded_section", v.Status)
			}
			if v := Verify(x, Claim{Field: FieldImprint, Value: "Harbor House Books", Quote: "Harbor House Books"}); v.Status != StatusGrounded {
				t.Errorf("a line before the heading: status = %q, want grounded", v.Status)
			}
		})
	}
	// Not a heading: the phrase is inside a sentence, or only a longer word.
	for _, text := range []string{"He wrote it, also by hand.\nThe Glass Orchard", "Alsobyte Press\nThe Glass Orchard", "Praised for it\nThe Glass Orchard"} {
		x := single(t, evidence.LandmarkFront, text)
		if v := Verify(x, Claim{Field: FieldTitle, Value: "The Glass Orchard", Quote: "The Glass Orchard"}); v.Status != StatusGrounded {
			t.Errorf("%q: status = %q, want grounded", text, v.Status)
		}
	}
}

func TestVerifyHeadingEndsAtItsUnit(t *testing.T) {
	x := excerptOf(t,
		unitText{evidence.LandmarkFront, "Also by Jane Doe\nThe Glass Orchard"},
		unitText{evidence.LandmarkTitle, "THE LANTERN YEARS\nby Jane Doe"},
	)
	if v := Verify(x, Claim{Field: FieldTitle, Value: "THE LANTERN YEARS", Quote: "THE LANTERN YEARS"}); v.Status != StatusGrounded {
		t.Errorf("next unit after an Also by list: status = %q, want grounded", v.Status)
	}
	if v := Verify(x, Claim{Field: FieldTitle, Value: "The Glass Orchard", Quote: "The Glass Orchard"}); v.Status != StatusExcludedSection {
		t.Errorf("list entry: status = %q, want excluded_section", v.Status)
	}
}

func TestVerifyWithoutUnits(t *testing.T) {
	x := Excerpt{Text: "THE LANTERN YEARS\nAlso by Jane Doe\nThe Glass Orchard"}
	if v := Verify(x, Claim{Field: FieldTitle, Value: "THE LANTERN YEARS", Quote: "THE LANTERN YEARS"}); v.Status != StatusGrounded || v.Unit != 0 {
		t.Errorf("no units: %+v", v)
	}
	if v := Verify(x, Claim{Field: FieldTitle, Value: "The Glass Orchard", Quote: "The Glass Orchard"}); v.Status != StatusExcludedSection {
		t.Errorf("no units, after heading: %+v", v)
	}
}
