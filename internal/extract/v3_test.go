package extract

import (
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

// Verifier v3 (design §13.3): a context-free value printed alike in several
// units, and a back-matter copyright page that is the book's own.

func TestV3RepeatedContextFreeValues(t *testing.T) {
	x := excerptOf(t,
		unitText{evidence.LandmarkFront, "THE LANTERN YEARS"},
		unitText{evidence.LandmarkTitle, "THE LANTERN YEARS\nA Novel\nby Jane Doe\nHarbor House Books"},
		unitText{evidence.LandmarkCopyright, "The Lantern Years: A Novel\nCopyright © 2019 Jane Doe\nPublished by Harbor House Books\nJane Doe asserts the moral right"},
	)
	tests := []struct {
		name  string
		claim Claim
		want  string
		unit  int // position of the unit it grounds in
	}{
		{"title on half-title, title and copyright page", Claim{Field: FieldTitle, Value: "THE LANTERN YEARS", Quote: "THE LANTERN YEARS"}, StatusGrounded, 1},
		{"subtitle on two pages", Claim{Field: FieldSubtitle, Value: "A Novel", Quote: "A Novel"}, StatusGrounded, 2},
		{"imprint on two pages", Claim{Field: FieldImprint, Value: "Harbor House Books", Quote: "Harbor House Books"}, StatusGrounded, 2},
		// A contributor reads its role from around the quote, so two units
		// may say two things: still ambiguous.
		{"a contributor in two units", Claim{Field: FieldContributor, Value: "Jane Doe", Quote: "Jane Doe"}, StatusAmbiguous, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := Verify(x, tt.claim)
			if v.Status != tt.want {
				t.Fatalf("status = %q, want %q: %+v", v.Status, tt.want, v)
			}
			if tt.want == StatusGrounded && v.Unit != tt.unit {
				t.Errorf("unit = %d, want %d", v.Unit, tt.unit)
			}
		})
	}
	// A whole line in each unit grounds at the first.
	y := excerptOf(t,
		unitText{evidence.LandmarkTitle, "Night Ferry"},
		unitText{evidence.LandmarkCopyright, "Night Ferry\nby Jane Doe"},
	)
	if v := Verify(y, Claim{Field: FieldTitle, Value: "Night Ferry", Quote: "Night Ferry"}); v.Status != StatusGrounded || v.Unit != 1 {
		t.Errorf("one line in both units: %+v", v)
	}
}

func TestV3BackCopyrightPage(t *testing.T) {
	back := "THE LANTERN YEARS\nCopyright © 2019 Jane Doe\nAll rights reserved.\nPublished by Harbor House Books\nFirst edition 2019\nAlso by Jane Doe\nThe Glass Orchard"
	tests := []struct {
		name  string
		units []unitText
		claim Claim
		want  string
	}{
		{"its year", []unitText{{evidence.LandmarkFront, "Chapter One"}, {evidence.LandmarkBack, back}},
			Claim{Field: FieldYear, Value: "2019", Quote: "Copyright © 2019", Label: LabelCopyright}, StatusGrounded},
		{"its imprint", []unitText{{evidence.LandmarkFront, "Chapter One"}, {evidence.LandmarkBack, back}},
			Claim{Field: FieldImprint, Value: "Harbor House Books", Quote: "Harbor House Books"}, StatusGrounded},
		{"its title, above the © line", []unitText{{evidence.LandmarkFront, "Chapter One"}, {evidence.LandmarkBack, back}},
			Claim{Field: FieldTitle, Value: "THE LANTERN YEARS", Quote: "THE LANTERN YEARS"}, StatusExcludedSection},
		{"after its Also by heading", []unitText{{evidence.LandmarkFront, "Chapter One"}, {evidence.LandmarkBack, back}},
			Claim{Field: FieldTitle, Value: "The Glass Orchard", Quote: "The Glass Orchard"}, StatusExcludedSection},
		{"in German", []unitText{{evidence.LandmarkBack, "© 2019 Jane Doe\nAlle Rechte vorbehalten.\nVerlag Hafenhaus"}},
			Claim{Field: FieldImprint, Value: "Verlag Hafenhaus", Quote: "Verlag Hafenhaus"}, StatusGrounded},
		// Not the book's own copyright page:
		{"the book has one in front", []unitText{{evidence.LandmarkCopyright, "© 2019 Jane Doe\nAll rights reserved."}, {evidence.LandmarkBack, "© 2021 Pat Roe\nAll rights reserved.\nNight Press"}},
			Claim{Field: FieldImprint, Value: "Night Press", Quote: "Night Press"}, StatusExcludedSection},
		{"a front unit carries the signature", []unitText{{evidence.LandmarkFront, "Copyright 2019 Jane Doe. All rights reserved."}, {evidence.LandmarkBack, "© 2021 Pat Roe\nAll rights reserved.\nNight Press"}},
			Claim{Field: FieldImprint, Value: "Night Press", Quote: "Night Press"}, StatusExcludedSection},
		{"two back copyright pages", []unitText{{evidence.LandmarkBack, "© 2019 Jane Doe\nAll rights reserved.\nHarbor House Books"}, {evidence.LandmarkBack, "© 2021 Pat Roe\nAll rights reserved.\nNight Press"}},
			Claim{Field: FieldImprint, Value: "Night Press", Quote: "Night Press"}, StatusExcludedSection},
		{"two back copyright pages, the first one's imprint", []unitText{{evidence.LandmarkBack, "© 2019 Jane Doe\nAll rights reserved.\nHarbor House Books"}, {evidence.LandmarkBack, "© 2021 Pat Roe\nAll rights reserved.\nNight Press"}},
			Claim{Field: FieldImprint, Value: "Harbor House Books", Quote: "Harbor House Books"}, StatusExcludedSection},
		{"no rights phrase", []unitText{{evidence.LandmarkBack, "© 2020 Night Press\nNight Press"}},
			Claim{Field: FieldYear, Value: "2020", Quote: "© 2020", Label: LabelCopyright}, StatusExcludedSection},
		{"no copyright year", []unitText{{evidence.LandmarkBack, "All rights reserved.\nNight Press"}},
			Claim{Field: FieldImprint, Value: "Night Press", Quote: "Night Press"}, StatusExcludedSection},
		{"another back unit", []unitText{{evidence.LandmarkBack, back}, {evidence.LandmarkBack, "About the Author\nHarbor Lights"}},
			Claim{Field: FieldTitle, Value: "Harbor Lights", Quote: "Harbor Lights"}, StatusExcludedSection},
		// Review of v3: the book's own © line in front, whatever its rights
		// wording, means a back one is another book's.
		{"a front © line with other rights wording", []unitText{
			{evidence.LandmarkTitle, "THE LANTERN YEARS\nby Jane Doe\nHarbor House Books"},
			{evidence.LandmarkFront, "Copyright © 2019 Jane Doe\nNo part of this book may be reproduced without written permission."},
			{evidence.LandmarkBack, "NIGHT HARBOR\nCopyright © 2021 Pat Roe\nAll rights reserved.\nTranslated by John Roe\nBook 2 of the Harbor Cycle"}},
			Claim{Field: FieldContributor, Value: "John Roe", Quote: "Translated by John Roe", Role: RoleTranslator}, StatusExcludedSection},
		{"a French front page", []unitText{
			{evidence.LandmarkFront, "© Éditions du Port, 2019\nTous droits de traduction, de reproduction et d'adaptation réservés pour tous pays."},
			{evidence.LandmarkBack, "© 2021 Pat Roe\nAll rights reserved.\nNight Press"}},
			Claim{Field: FieldImprint, Value: "Night Press", Quote: "Night Press"}, StatusExcludedSection},
		// A permissions credit is not a copyright page.
		{"a lyrics credit", []unitText{{evidence.LandmarkBack, "Lyrics from Yesterday\nWords and music by John Lennon and Paul McCartney\n© 1965 Northern Songs. All rights reserved. Used by permission."}},
			Claim{Field: FieldImprint, Value: "Northern Songs", Quote: "Northern Songs"}, StatusExcludedSection},
		// Only the page from its © line on is the copyright page.
		{"an About the Author text above the © line", []unitText{{evidence.LandmarkBack, "About the Author\nJane Doe wrote The Glass Orchard.\nCopyright © 2019 by Jane Doe. All rights reserved.\nHarbor House Books"}},
			Claim{Field: FieldTitle, Value: "The Glass Orchard", Quote: "The Glass Orchard"}, StatusExcludedSection},
		{"the imprint below the © line", []unitText{{evidence.LandmarkBack, "About the Author\nJane Doe wrote The Glass Orchard.\nCopyright © 2019 by Jane Doe. All rights reserved.\nHarbor House Books"}},
			Claim{Field: FieldImprint, Value: "Harbor House Books", Quote: "Harbor House Books"}, StatusGrounded},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if v := Verify(excerptOf(t, tt.units...), tt.claim); v.Status != tt.want {
				t.Fatalf("status = %q, want %q: %+v", v.Status, tt.want, v)
			}
		})
	}
}

func TestV3Version(t *testing.T) {
	if Version < 3 {
		t.Fatalf("Version = %d: v3's rules ground more, so sealed claims of v2 must not be re-verified as v3", Version)
	}
}

func TestV3KeepsFrontYears(t *testing.T) {
	// A back copyright page that is not the book's own must not make the
	// front one's year ambiguous.
	x := excerptOf(t,
		unitText{evidence.LandmarkFront, "© 2019 Jane Doe\nHarbor House Books"},
		unitText{evidence.LandmarkBack, "Copyright © 2019 Jane Doe\nAll rights reserved."},
	)
	if v := Verify(x, Claim{Field: FieldYear, Value: "2019", Quote: "© 2019", Label: LabelCopyright}); v.Status != StatusGrounded || v.Unit != 1 {
		t.Errorf("front year: %+v", v)
	}
}

func TestV3MalformedUnitsDoNotPanic(t *testing.T) {
	x := Excerpt{Text: "abc", Units: []evidence.ExcerptUnit{{Pos: 1, Landmark: evidence.LandmarkBack, Start: 5, End: 9}, {Pos: 2, Landmark: evidence.LandmarkBack, Start: 2, End: 1}}}
	if v := Verify(x, Claim{Field: FieldTitle, Value: "abc", Quote: "abc"}); v.Status == StatusGrounded {
		t.Errorf("a quote in no unit grounded: %+v", v)
	}
}
