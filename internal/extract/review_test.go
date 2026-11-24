package extract

import (
	"slices"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

// The cases of the phase-1 review of #99: every probe that grounded a wrong
// value, a wrong role or a wrong label, and the controls that must keep
// grounding after the fixes.

func textLayer(t testing.TB, landmark, text string) Excerpt {
	t.Helper()
	b := evidence.NewExcerptBuilder(evidence.ExcerptTextLayer)
	b.Add(1, landmark, text)
	e := b.Excerpt()
	if e == nil {
		t.Fatal("empty excerpt")
	}
	return NewExcerpt(*e)
}

type reviewCase struct {
	name      string
	x         func(t testing.TB) Excerpt
	claim     Claim
	status    string
	value     string // checked when grounded
	roleLabel string // role or label, checked when grounded
}

func onTitlePage(s string) func(t testing.TB) Excerpt {
	return func(t testing.TB) Excerpt { return single(t, evidence.LandmarkTitle, s) }
}

func onCopyrightPage(s string) func(t testing.TB) Excerpt {
	return func(t testing.TB) Excerpt { return single(t, evidence.LandmarkCopyright, s) }
}

func runReview(t *testing.T, cases []reviewCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			x := tc.x(t)
			v := Verify(x, tc.claim)
			if v.Status != tc.status {
				t.Fatalf("status = %q (value %q, role %q, label %q), want %q", v.Status, v.Value, v.Role, v.Label, tc.status)
			}
			if v.Status != StatusGrounded {
				return
			}
			if v.Value != tc.value {
				t.Errorf("value = %q, want %q", v.Value, tc.value)
			}
			if got := v.Role + v.Label; got != tc.roleLabel {
				t.Errorf("role/label = %q, want %q", got, tc.roleLabel)
			}
		})
	}
}

func contrib(value, quote, role string) Claim {
	return Claim{Field: FieldContributor, Value: value, Quote: quote, Role: role}
}

// C1: "by" proves an author only as a line-initial by-line, and a role phrase
// that ends the line before gives the by-line its role.
func TestReviewByLines(t *testing.T) {
	runReview(t, []reviewCase{
		{"translated from, by on the next line", onTitlePage("THE X\nJane Doe\nTranslated from the French\nby John Roe"), contrib("John Roe", "by John Roe", ""), StatusGrounded, "John Roe", RoleTranslator},
		{"edited, by on the next line", onTitlePage("THE X\nEdited\nby Pat Keller"), contrib("Pat Keller", "by Pat Keller", ""), StatusGrounded, "Pat Keller", RoleEditor},
		{"with an introduction, by on the next line", onTitlePage("THE X\nby Jane Doe\nWith an Introduction\nby Mary Sample"), contrib("Mary Sample", "by Mary Sample", ""), StatusGrounded, "Mary Sample", RoleIntroduction},
		{"afterword, by on the next line", onTitlePage("THE X\nby Jane Doe\nAfterword\nby Mary Sample"), contrib("Mary Sample", "by Mary Sample", ""), StatusRoleUnproven, "", ""},
		{"edited claimed as author", onTitlePage("THE X\nEdited\nby Pat Keller"), contrib("Pat Keller", "by Pat Keller", RoleAuthor), StatusRoleUnproven, "", ""},
		{"translation by", onTitlePage("THE X\nby Jane Doe\nTranslation by John Roe"), contrib("John Roe", "John Roe", ""), StatusRoleUnproven, "", ""},
		{"translation by, quote with by", onTitlePage("THE X\nby Jane Doe\nTranslation by John Roe"), contrib("John Roe", "by John Roe", ""), StatusRoleUnproven, "", ""},
		{"afterword by", onTitlePage("THE X\nby Jane Doe\nAfterword by Mary Sample"), contrib("Mary Sample", "Mary Sample", ""), StatusRoleUnproven, "", ""},
		{"edited, with notes, by", onTitlePage("THE X\nEdited, with an introduction and notes, by Pat Keller"), contrib("Pat Keller", "Pat Keller", RoleAuthor), StatusRoleUnproven, "", ""},
		{"translation copyright by", onCopyrightPage("English translation copyright © 2003 by John Roe"), contrib("John Roe", "by John Roe", RoleAuthor), StatusRoleUnproven, "", ""},
		{"published by an imprint", onCopyrightPage("Published by Harbor House Books"), contrib("Harbor House Books", "by Harbor House Books", ""), StatusRoleUnproven, "", ""},
		{"cover design by", onCopyrightPage("Cover design by Ann Artist"), contrib("Ann Artist", "Ann Artist", ""), StatusRoleUnproven, "", ""},
		{"read by", onTitlePage("THE X\nby Jane Doe\nRead by Nick Narrator"), contrib("Nick Narrator", "Nick Narrator", ""), StatusRoleUnproven, "", ""},
		{"by X with Y: Y", onTitlePage("THE X\nby Jane Doe with John Roe"), contrib("John Roe", "John Roe", ""), StatusRoleUnproven, "", ""},
		{"a sentence end cuts the role", onCopyrightPage("Translated by John Roe. Mary Sample"), contrib("Mary Sample", "Mary Sample", ""), StatusRoleUnproven, "", ""},
		{"a semicolon cuts the role", onCopyrightPage("Translated by John Roe; Mary Sample"), contrib("Mary Sample", "Mary Sample", ""), StatusRoleUnproven, "", ""},
		{"a sentence end before a connector cuts the role", onCopyrightPage("Translated by John Roe. And Mary Sample"), contrib("Mary Sample", "Mary Sample", ""), StatusRoleUnproven, "", ""},
		{"a role word in a title line", onTitlePage("THE TRANSLATOR'S DAUGHTER\nby Jane Doe"), contrib("Jane Doe", "by Jane Doe", ""), StatusRoleUnproven, "", ""},
		// Controls.
		{"by-line", onTitlePage("THE X\nby Jane Doe"), contrib("Jane Doe", "by Jane Doe", ""), StatusGrounded, "Jane Doe", RoleAuthor},
		{"upper-case by-line", onTitlePage("THE X\nBY JANE DOE"), contrib("JANE DOE", "BY JANE DOE", ""), StatusGrounded, "JANE DOE", RoleAuthor},
		{"written by", onTitlePage("THE X\nWritten by Jane Doe"), contrib("Jane Doe", "Written by Jane Doe", ""), StatusGrounded, "Jane Doe", RoleAuthor},
		{"by X with Y: X", onTitlePage("THE X\nby Jane Doe with John Roe"), contrib("Jane Doe", "by Jane Doe", ""), StatusGrounded, "Jane Doe", RoleAuthor},
		{"by X and Y: Y", onTitlePage("THE X\nby Jane Doe and John Roe"), contrib("John Roe", "John Roe", ""), StatusGrounded, "John Roe", RoleAuthor},
		{"by on its own line", onTitlePage("THE X\nby\nJane Doe"), contrib("Jane Doe", "Jane Doe", ""), StatusGrounded, "Jane Doe", RoleAuthor},
		{"translators joined by and", onCopyrightPage("Translated by John Roe and Mary Sample"), contrib("Mary Sample", "Mary Sample", ""), StatusGrounded, "Mary Sample", RoleTranslator},
		{"translated by over the name", onTitlePage("THE X\nTranslated by\nJohn Roe"), contrib("John Roe", "John Roe", ""), StatusGrounded, "John Roe", RoleTranslator},
	})
}

// C2: a lone name on a title page is an author only with no role block, by-line
// or connector before it in its unit, and never as the unit's first line.
func TestReviewLoneNames(t *testing.T) {
	runReview(t, []reviewCase{
		{"the title line", onTitlePage("THE LANTERN YEARS\nby Jane Doe"), contrib("THE LANTERN YEARS", "THE LANTERN YEARS", RoleAuthor), StatusRoleUnproven, "", ""},
		{"a subtitle", onTitlePage("THE X\nA Novel\nby Jane Doe"), contrib("A Novel", "A Novel", RoleAuthor), StatusRoleUnproven, "", ""},
		{"an imprint after the by-line", onTitlePage("THE X\nby Jane Doe\nHarbor House"), contrib("Harbor House", "Harbor House", RoleAuthor), StatusRoleUnproven, "", ""},
		{"after a language line", onTitlePage("THE X\nJane Doe\nTranslated from the French\nJohn Roe"), contrib("John Roe", "John Roe", RoleAuthor), StatusRoleUnproven, "", ""},
		{"second name under translated by", onTitlePage("THE X\nTranslated by\nJohn Roe\nMary Sample"), contrib("Mary Sample", "Mary Sample", RoleAuthor), StatusRoleUnproven, "", ""},
		{"after an and line", onTitlePage("THE X\nTranslated by\nJohn Roe\nand\nMary Sample"), contrib("Mary Sample", "Mary Sample", RoleAuthor), StatusRoleUnproven, "", ""},
		{"after a by-line and a name", onTitlePage("THE X\nby\nJane Doe\nJohn Roe"), contrib("John Roe", "John Roe", RoleAuthor), StatusRoleUnproven, "", ""},
		{"X and Y, editors", onTitlePage("THE X\nJane Doe and John Roe, editors"), contrib("John Roe", "John Roe", RoleAuthor), StatusRoleUnproven, "", ""},
		{"the unit's first line", onTitlePage("Jane Doe\nTHE X"), contrib("Jane Doe", "Jane Doe", ""), StatusRoleUnproven, "", ""},
		{"after a bare language line", onTitlePage("THE X\nFrom the French\nJohn Roe"), contrib("John Roe", "John Roe", ""), StatusRoleUnproven, "", ""},
		{"Deutsch von", onTitlePage("DAS X\nDeutsch von Anna Beispiel"), contrib("Anna Beispiel", "Anna Beispiel", RoleAuthor), StatusRoleUnproven, "", ""},
		// Controls.
		{"lone name under the title", onTitlePage("THE X\nJane Doe"), contrib("Jane Doe", "Jane Doe", ""), StatusGrounded, "Jane Doe", RoleAuthor},
		{"two lone names", onTitlePage("THE X\nJane Doe\nJohn Roe"), contrib("John Roe", "John Roe", ""), StatusGrounded, "John Roe", RoleAuthor},
		{"lone name on a copyright page", onCopyrightPage("THE X\nJane Doe"), contrib("Jane Doe", "Jane Doe", ""), StatusRoleUnproven, "", ""},
	})
}

// I5, I6: the name must end where the quote ends, and credentials, role nouns
// and connectors are no part of a name.
func TestReviewNames(t *testing.T) {
	runReview(t, []reviewCase{
		{"given name only", onTitlePage("THE X\nby Jane Doe"), contrib("Jane", "by Jane", ""), StatusValueNotDerived, "", ""},
		{"cut at a hyphen", onTitlePage("THE X\nby Jane Doe-Smith"), contrib("Jane Doe", "by Jane Doe", ""), StatusValueNotDerived, "", ""},
		{"cut at an apostrophe", onTitlePage("THE X\nby Kate O'Hara"), contrib("Kate O", "by Kate O", ""), StatusValueNotDerived, "", ""},
		{"cut at an initial", onTitlePage("THE X\nby J. Doe"), contrib("J", "by J", ""), StatusValueNotDerived, "", ""},
		{"cut on its second line", onTitlePage("THE X\nby\nJane Doe"), contrib("Jane", "by\nJane", ""), StatusValueNotDerived, "", ""},
		{"a credential", onTitlePage("THE X\nby Jane Doe, PhD"), contrib("Jane Doe", "by Jane Doe, PhD", ""), StatusGrounded, "Jane Doe", RoleAuthor},
		{"a credential is not a given name", onTitlePage("THE X\nby Jane Doe, PhD"), contrib("PhD Jane Doe", "by Jane Doe, PhD", ""), StatusValueNotDerived, "", ""},
		{"a role noun", onTitlePage("THE X\nJane Doe, Editor"), contrib("Editor Jane Doe", "Jane Doe, Editor", ""), StatusValueNotDerived, "", ""},
		{"a role noun, name only", onTitlePage("THE X\nJane Doe, Editor"), contrib("Jane Doe", "Jane Doe, Editor", ""), StatusValueNotDerived, "", ""},
		{"upper-case AND", onTitlePage("THE X\nJANE DOE AND JOHN ROE"), contrib("JANE DOE AND JOHN ROE", "JANE DOE AND JOHN ROE", ""), StatusValueNotDerived, "", ""},
		{"by-line with upper-case AND", onTitlePage("THE X\nBY JANE DOE AND JOHN ROE"), contrib("JANE DOE AND JOHN ROE", "BY JANE DOE AND JOHN ROE", ""), StatusValueNotDerived, "", ""},
		{"three comma parts", onTitlePage("THE X\nby Doe, Jane, Roe"), contrib("Jane Doe", "by Doe, Jane, Roe", ""), StatusValueNotDerived, "", ""},
		{"cut before a suffix", onTitlePage("THE X\nby Jane Doe, Jr."), contrib("Jane Doe", "by Jane Doe", ""), StatusValueNotDerived, "", ""},
		{"an abbreviated role", onTitlePage("THE X\nJane Doe, ed."), contrib("Jane Doe", "Jane Doe, ed.", ""), StatusValueNotDerived, "", ""},
		{"Ed is a given name", onTitlePage("THE X\nby Ed McBain"), contrib("Ed McBain", "by Ed McBain", ""), StatusGrounded, "Ed McBain", RoleAuthor},
		{"with a suffix", onTitlePage("THE X\nby Jane Doe, Jr."), contrib("Jane Doe, Jr.", "by Jane Doe, Jr.", ""), StatusGrounded, "Jane Doe, Jr.", RoleAuthor},
		// Controls.
		{"apostrophe name", onTitlePage("THE X\nby Kate O'Hara"), contrib("Kate O'Hara", "by Kate O'Hara", ""), StatusGrounded, "Kate O'Hara", RoleAuthor},
		{"double-barrelled name", onTitlePage("THE X\nby Jane Doe-Smith"), contrib("Jane Doe-Smith", "by Jane Doe-Smith", ""), StatusGrounded, "Jane Doe-Smith", RoleAuthor},
		{"name before a comma", onTitlePage("THE X\nby Jane Doe, PhD"), contrib("Jane Doe", "by Jane Doe", ""), StatusGrounded, "Jane Doe", RoleAuthor},
		{"inverted name", onTitlePage("THE X\nby Doe, Jane"), contrib("Jane Doe", "by Doe, Jane", ""), StatusGrounded, "Jane Doe", RoleAuthor},
		{"Ortega y Gasset", onTitlePage("THE X\nby José Ortega y Gasset"), contrib("José Ortega y Gasset", "by José Ortega y Gasset", ""), StatusGrounded, "José Ortega y Gasset", RoleAuthor},
	})
}

// I1: a year is the first one after its label, in the same sentence, and not
// a street number or a group of an ISBN.
func TestReviewYears(t *testing.T) {
	const page = "First published 1925. Reprinted 1926, 1930, 2019\nPublished by Random House, Inc., 1745 Broadway, New York, NY 10019\n" +
		"This edition published 2019. Reprinted 2021\nFirst published in this edition 2018\nCopyright © 2019 Harbor House. ISBN 978 0 1990 0000 0\n" +
		"This edition first published 2020 and reissued 1066"
	year := func(v, q, label string) Claim { return Claim{Field: FieldYear, Value: v, Quote: q, Label: label} }
	runReview(t, []reviewCase{
		{"a reprint after a sentence end", onCopyrightPage(page), year("2019", "First published 1925. Reprinted 1926, 1930, 2019", ""), StatusValueNotDerived, "", ""},
		{"a street number", onCopyrightPage(page), year("1745", "Published by Random House, Inc., 1745", ""), StatusValueNotDerived, "", ""},
		{"a reprint of this edition", onCopyrightPage(page), year("2021", "This edition published 2019. Reprinted 2021", ""), StatusValueNotDerived, "", ""},
		{"an ISBN group", onCopyrightPage(page), year("1990", "Copyright © 2019 Harbor House. ISBN 978 0 1990", ""), StatusValueNotDerived, "", ""},
		{"a second year", onCopyrightPage("Copyright © 2019 Harbor House, 2020"), year("2020", "Copyright © 2019 Harbor House, 2020", ""), StatusValueNotDerived, "", ""},
		{"one year, two labels", onCopyrightPage("First published 2019, this edition 2019"), year("2019", "First published 2019, this edition 2019", ""), StatusValueNotDerived, "", ""},
		{"before the range", onCopyrightPage("© 1399"), year("1399", "© 1399", ""), StatusValueNotDerived, "", ""},
		{"after the range", onCopyrightPage("© 2100"), year("2100", "© 2100", ""), StatusValueNotDerived, "", ""},
		{"a semicolon between", onCopyrightPage("Published in London; 2019 reprint"), year("2019", "Published in London; 2019", ""), StatusValueNotDerived, "", ""},
		{"a sentence end between", onCopyrightPage("First published in London. Reissued 2019"), year("2019", "First published in London. Reissued 2019", ""), StatusValueNotDerived, "", ""},
		{"a house number between", onCopyrightPage("Published at 375 Hudson Street 2019"), year("2019", "Published at 375 Hudson Street 2019", ""), StatusValueNotDerived, "", ""},
		// Controls.
		{"in this edition", onCopyrightPage(page), year("2018", "First published in this edition 2018", ""), StatusGrounded, "2018", LabelThisEdition},
		{"one year, the label named", onCopyrightPage("First published 2019, this edition 2019"), year("2019", "First published 2019, this edition 2019", LabelFirstPublished), StatusGrounded, "2019", LabelFirstPublished},
		{"a list of copyright years", onCopyrightPage("© 2003, 2019 Jane Doe"), year("2019", "© 2003, 2019", ""), StatusGrounded, "2019", LabelCopyright},
		{"holder then year", onCopyrightPage("Copyright © Jane Doe, 2003"), year("2003", "© Jane Doe, 2003", ""), StatusGrounded, "2003", LabelCopyright},
		{"year then holder", onCopyrightPage("© 2018 Beispiel Verlag GmbH"), year("2018", "© 2018", ""), StatusGrounded, "2018", LabelCopyright},
		{"first in the range", onCopyrightPage("© 1400"), year("1400", "© 1400", ""), StatusGrounded, "1400", LabelCopyright},
		{"last in the range", onCopyrightPage("© 2099"), year("2099", "© 2099", ""), StatusGrounded, "2099", LabelCopyright},
		{"words between", onCopyrightPage("First published in Great Britain in 1925 by Chatto"), year("1925", "First published in Great Britain in 1925", ""), StatusGrounded, "1925", LabelFirstPublished},
	})
	x := single(t, evidence.LandmarkCopyright, page)
	got := parsed(x)
	for _, w := range []string{"year=1925/first_published", "year=2019/this_edition", "year=2019/copyright", "year=2018/this_edition", "year=2020/this_edition"} {
		if !slices.Contains(got, w) {
			t.Errorf("parser: missing %q in %q", w, got)
		}
	}
	for _, c := range Parse(x) {
		if c.Field != FieldYear {
			continue
		}
		switch b := brief(c); b {
		case "year=1925/first_published", "year=2019/this_edition", "year=2019/copyright", "year=2018/this_edition", "year=2020/this_edition":
		default:
			t.Errorf("parser: unexpected %q (quote %q)", b, c.Quote)
		}
	}
}

// I2: generic heads, numbers and bestseller lines are no series.
func TestReviewSeries(t *testing.T) {
	x := single(t, evidence.LandmarkTitle, "THE X\nBook 2 of 3 in a box set\nPart 1 of 2\nFirst in the series\nSecond of three novels\n"+
		"Volume 1\n#1 in the UK\n#1\nThe Lantern Years: Part Two\nNo. 1 New York Times bestseller\nBook 3 of the Trilogy")
	var got []string
	for _, c := range Parse(x) {
		if c.Field == FieldSeries || c.Field == FieldSeriesIndex {
			got = append(got, brief(c))
		}
	}
	if want := []string{"series=The Lantern Years", "series_index=2"}; !slices.Equal(got, want) {
		t.Errorf("parser series claims = %q, want %q", got, want)
	}
	series := func(f, v, q string) Claim { return Claim{Field: f, Value: v, Quote: q} }
	runReview(t, []reviewCase{
		{"a bare hash index", onTitlePage("THE X\n#1\nNEW YORK TIMES BESTSELLER"), series(FieldSeriesIndex, "1", "#1"), StatusValueNotDerived, "", ""},
		{"a bare volume", onTitlePage("THE X\nVolume 1"), series(FieldSeriesIndex, "1", "Volume 1"), StatusValueNotDerived, "", ""},
		{"of a number", onTitlePage("THE X\nPart 1 of 2"), series(FieldSeries, "2", "Part 1 of 2"), StatusValueNotDerived, "", ""},
		{"hash in a country", onTitlePage("THE X\n#1 in the UK"), series(FieldSeriesIndex, "1", "#1 in the UK"), StatusValueNotDerived, "", ""},
		{"an indefinite head", onTitlePage("THE X\nVolume 2 in a Lantern series"), series(FieldSeries, "a Lantern", "Volume 2 in a Lantern series"), StatusValueNotDerived, "", ""},
		{"an ordinal head", onTitlePage("THE X\nBook 2 of Third Novels"), series(FieldSeries, "Third Novels", "Book 2 of Third Novels"), StatusValueNotDerived, "", ""},
		// Controls.
		{"brackets in the name", onTitlePage("THE X\nThe Lantern (Saga), Book 2"), series(FieldSeries, "The Lantern (Saga)", "The Lantern (Saga), Book 2"), StatusGrounded, "The Lantern (Saga)", ""},
		{"hash in a series", onTitlePage("THE X\n#3 in the Lantern series"), series(FieldSeriesIndex, "3", "#3 in the Lantern series"), StatusGrounded, "3", ""},
		{"ordinal book in", onTitlePage("THE X\nThird book in the Lantern Saga"), series(FieldSeries, "Lantern Saga", "Third book in the Lantern Saga"), StatusGrounded, "Lantern Saga", ""},
		{"book of the", onTitlePage("THE X\nBook 2 of the Lantern Chronicles"), series(FieldSeries, "Lantern Chronicles", "Book 2 of the Lantern Chronicles"), StatusGrounded, "Lantern Chronicles", ""},
	})
}

// I3: a label after an ISBN is not its own when another ISBN follows on the
// line, and an ISBN in a back unit has no qualifier.
func TestReviewISBNQualifiers(t *testing.T) {
	x := single(t, evidence.LandmarkCopyright, "ISBN 978-0-306-40615-7 Print ISBN 978-1-4028-9462-6\nThe Glass Orchard (paperback) ISBN 978-0-14-118776-1")
	got := parsed(x)
	for _, w := range []string{"isbn=9780306406157", "isbn=9781402894626", "isbn=9780141187761/paperback"} {
		if !slices.Contains(got, w) {
			t.Errorf("missing %q in %q", w, got)
		}
	}
	back := func(t testing.TB) Excerpt {
		return excerptOf(t, unitText{evidence.LandmarkTitle, "THE X\nby Jane Doe"},
			unitText{evidence.LandmarkBack, "Night Press\nISBN 978-0-7432-7356-5 (ebook)"})
	}
	isbn := func(v, q, label string) Claim { return Claim{Field: FieldISBN, Value: v, Quote: q, Label: label} }
	runReview(t, []reviewCase{
		{"print belongs to the next", onCopyrightPage("ISBN 978-0-306-40615-7 Print ISBN 978-1-4028-9462-6"), isbn("9780306406157", "ISBN 978-0-306-40615-7", "print"), StatusValueNotDerived, "", ""},
		{"back unit, label claimed", back, isbn("9780743273565", "ISBN 978-0-7432-7356-5", "ebook"), StatusValueNotDerived, "", ""},
		{"back unit, no label", back, isbn("9780743273565", "ISBN 978-0-7432-7356-5", ""), StatusGrounded, "9780743273565", ""},
		{"brackets after each", onCopyrightPage("ISBN 978-1-4028-9462-6 (print) ISBN 978-0-306-40615-7 (ebook)"), isbn("9781402894626", "ISBN 978-1-4028-9462-6", ""), StatusGrounded, "9781402894626", evidence.QualifierPrint},
		{"label before each", onCopyrightPage("Paperback ISBN 978-1-4028-9462-6 eBook ISBN 978-0-306-40615-7"), isbn("9780306406157", "ISBN 978-0-306-40615-7", ""), StatusGrounded, "9780306406157", evidence.QualifierEbook},
	})
	// After an ad's heading the ISBN is another book's and is excluded.
	ad := excerptOf(t, unitText{evidence.LandmarkBack, "Look for Jane Doe's next novel, coming in hardcover\nISBN 978-0-7432-7356-5 (ebook)"})
	if v := Verify(ad, isbn("9780743273565", "ISBN 978-0-7432-7356-5", "")); v.Status != StatusExcludedSection {
		t.Errorf("ad ISBN: status = %q, want excluded", v.Status)
	}
	for _, c := range Parse(back(t)) {
		if c.Field == FieldISBN && c.Label != "" {
			t.Errorf("parser gave a back-unit ISBN the label %q", c.Label)
		}
	}
}

// I4: headings found on the lines as printed, numbered, mid-line or letter-
// spaced, in more languages.
func TestReviewSections(t *testing.T) {
	for _, h := range []string{
		"Books by Jane Doe", "ALSO AVAILABLE", "More from Jane Doe", "Also by the author", "THE END. Also by Jane Doe",
		"Vom selben Autor", //nolint:misspell // German
		"Ebenfalls erschienen", "Ebenfalls von Jane Doe", "También de Jane Doe", "Dello stesso autore",
		"Also\u200bby Jane Doe", "A L S O  B Y", "Other Works", "Look for", "Don't miss", "Also by Jane Doe:", "1. Also by Jane Doe",
	} {
		x := single(t, evidence.LandmarkFront, "Harbor House Books\n"+h+"\nThe Glass Orchard")
		if v := Verify(x, Claim{Field: FieldTitle, Value: "The Glass Orchard", Quote: "The Glass Orchard"}); v.Status != StatusExcludedSection {
			t.Errorf("%q: status = %q, want excluded", h, v.Status)
		}
	}
	// A hyphen join must not swallow the heading of a text layer.
	x := textLayer(t, evidence.LandmarkFront, "Harbor House Books x-\nAlso by Jane Doe\nThe Glass Orchard")
	if v := Verify(x, Claim{Field: FieldTitle, Value: "The Glass Orchard", Quote: "The Glass Orchard"}); v.Status != StatusExcludedSection {
		t.Errorf("joined heading: status = %q, want excluded", v.Status)
	}
	// Control: the heading words inside a line are no heading.
	y := single(t, evidence.LandmarkFront, "Harbor House Books\nA story also by night\nThe Glass Orchard")
	if v := Verify(y, Claim{Field: FieldTitle, Value: "The Glass Orchard", Quote: "The Glass Orchard"}); v.Status != StatusGrounded {
		t.Errorf("no heading: status = %q, want grounded", v.Status)
	}
}

// I4 (EPUB), I7 and the minors: normalisation and trimming.
func TestReviewNormaliseAndTrim(t *testing.T) {
	title := func(v, q string) Claim { return Claim{Field: FieldTitle, Value: v, Quote: q} }
	runReview(t, []reviewCase{
		{"EPUB: no hyphen join", onTitlePage("THE X\nby Mary Smith-\nJones"), contrib("Mary SmithJones", "by Mary SmithJones", ""), StatusNotFound, "", ""},
		{"text layer: hyphen join", func(t testing.TB) Excerpt {
			return textLayer(t, evidence.LandmarkCopyright, "Trans-\nlated by Jane Doe")
		}, contrib("Jane Doe", "Translated by Jane Doe", ""), StatusGrounded, "Jane Doe", RoleTranslator},
		{"zero-width space, dropped from the value (v4)", onTitlePage("THE\u200b X\nby Jane"), title("THE\u200b X", "THE\u200b X"), StatusGrounded, "THE X", ""},
		{"bidi override", onTitlePage("\u202eTHE X\nby Jane"), title("\u202eTHE X", "\u202eTHE X"), StatusValueNotDerived, "", ""},
		{"modifier apostrophe is a letter", onTitlePage("Hawaiʼi Nights\nby Jane"), title("Hawai'i Nights", "Hawai'i Nights"), StatusNotFound, "", ""},
		{"soft hyphen before a combining mark", func(testing.TB) Excerpt { return Excerpt{Text: "Cafe\u00ad\u0301 Noir"} }, title("Café Noir", "Café Noir"), StatusNotFound, "", ""},
		{"a one-rune title", onTitlePage("X\nby Jane Doe"), title("X", "X"), StatusNotFound, "", ""},
		{"a title over a by-line", onTitlePage("THE X\nby Jane Doe"), title("THE X by Jane Doe", "THE X by Jane Doe"), StatusValueNotDerived, "", ""},
		{"an imprint over a role line", onTitlePage("THE X\nHarbor House\nTranslated by John Roe"), Claim{Field: FieldImprint, Value: "Harbor House Translated by John Roe", Quote: "Harbor House Translated by John Roe"}, StatusValueNotDerived, "", ""},
		{"a title over two lines", onTitlePage("THE LANTERN\nYEARS\nby Jane Doe"), title("THE LANTERN YEARS", "THE LANTERN YEARS"), StatusGrounded, "THE LANTERN YEARS", ""},
		// Controls.
		{"modifier apostrophe as printed", onTitlePage("Hawaiʼi Nights\nby Jane"), title("Hawaiʼi Nights", "Hawaiʼi Nights"), StatusGrounded, "Hawaiʼi Nights", ""},
		{"closing bracket", onTitlePage("WHO MOVED THE LANTERN (AGAIN)\nby Jane Doe"), title("WHO MOVED THE LANTERN (AGAIN)", "WHO MOVED THE LANTERN (AGAIN)"), StatusGrounded, "WHO MOVED THE LANTERN (AGAIN)", ""},
		{"ellipsis", onTitlePage("And Then There Were…\nby Jane Doe"), title("And Then There Were…", "And Then There Were…"), StatusGrounded, "And Then There Were…", ""},
		{"three dots", onTitlePage("And Then There Were...\nby Jane Doe"), title("And Then There Were...", "And Then There Were..."), StatusGrounded, "And Then There Were...", ""},
		{"closing quote", onTitlePage("The \"Lantern\"\nby Jane Doe"), title("The \"Lantern\"", "The \"Lantern\""), StatusGrounded, "The \"Lantern\"", ""},
		{"wrapping quotes go", onTitlePage("\"The Lantern\"\nby Jane Doe"), title("The Lantern", "\"The Lantern\""), StatusGrounded, "The Lantern", ""},
		{"unbalanced opener goes", onTitlePage("(The Lantern\nby Jane Doe"), title("The Lantern", "(The Lantern"), StatusGrounded, "The Lantern", ""},
		{"a leading bracket with its partner stays", onTitlePage("(The Lantern) Saga\nby Jane Doe"), title("(The Lantern) Saga", "(The Lantern) Saga"), StatusGrounded, "(The Lantern) Saga", ""},
		{"imprint in brackets", onTitlePage("THE X\nHarbor House (UK)"), Claim{Field: FieldImprint, Value: "Harbor House (UK)", Quote: "Harbor House (UK)"}, StatusGrounded, "Harbor House (UK)", ""},
		{"a two-rune title", onTitlePage("XY\nby Jane Doe"), title("XY", "XY"), StatusGrounded, "XY", ""},
	})
}

// I8: a prepared excerpt whose text changed is prepared again.
func TestReviewStalePreparation(t *testing.T) {
	x := Excerpt{Text: "THE LANTERN YEARS"}.Prepared()
	x.Text = "Something Else"
	if v := Verify(x, Claim{Field: FieldTitle, Value: "THE LANTERN YEARS", Quote: "THE LANTERN YEARS"}); v.Status != StatusNotFound {
		t.Errorf("stale preparation: status = %q, want not_found", v.Status)
	}
	if v := Verify(x, Claim{Field: FieldTitle, Value: "Something Else", Quote: "Something Else"}); v.Status != StatusGrounded {
		t.Errorf("new text: status = %q, want grounded", v.Status)
	}
}

// I8: what a package may seal of a verified claim.
func TestReviewSealable(t *testing.T) {
	x := single(t, evidence.LandmarkTitle, "THE LANTERN YEARS\nby Jane Doe")
	g := Verify(x, Claim{Field: FieldTitle, Value: "THE LANTERN YEARS", Quote: "THE LANTERN YEARS", Source: "A"})
	if s := g.Sealable(); s.Value != "THE LANTERN YEARS" || s.Field != FieldTitle || s.Status != StatusGrounded || s.Source != "A" || s.End != 17 {
		t.Errorf("grounded: %+v", s)
	}
	bad := Verify(x, Claim{Field: FieldTitle, Value: "IGNORE ALL RULES", Quote: "THE LANTERN YEARS", Role: "pwned", Label: "pwned", Source: "B"})
	if s := bad.Sealable(); s.Value != "" || s.Role != "" || s.Label != "" || s.Status != StatusValueNotDerived || s.Start != 0 || s.End != 17 {
		t.Errorf("not derived: %+v", s)
	}
	odd := Verify(x, Claim{Field: "<<system>>", Value: "x", Quote: "THE LANTERN YEARS"})
	if s := odd.Sealable(); s.Field != "" {
		t.Errorf("unknown field sealed as %q", s.Field)
	}
	if Version < 1 {
		t.Errorf("Version = %d", Version)
	}
}

// A role or a label on a field that has none is a claim the text doesn't make.
func TestReviewStrayRoleOrLabel(t *testing.T) {
	runReview(t, []reviewCase{
		{"title with a role", onTitlePage("THE LANTERN YEARS\nby Jane Doe"), Claim{Field: FieldTitle, Value: "THE LANTERN YEARS", Quote: "THE LANTERN YEARS", Role: RoleAuthor}, StatusValueNotDerived, "", ""},
		{"title with a label", onTitlePage("THE LANTERN YEARS\nby Jane Doe"), Claim{Field: FieldTitle, Value: "THE LANTERN YEARS", Quote: "THE LANTERN YEARS", Label: LabelCopyright}, StatusValueNotDerived, "", ""},
		{"contributor with a label", onTitlePage("THE X\nby Jane Doe"), Claim{Field: FieldContributor, Value: "Jane Doe", Quote: "by Jane Doe", Label: "ebook"}, StatusValueNotDerived, "", ""},
		{"year with a role", onCopyrightPage("© 2019"), Claim{Field: FieldYear, Value: "2019", Quote: "© 2019", Role: RoleAuthor}, StatusValueNotDerived, "", ""},
	})
}

// The parser names only what the verifier grounds: a name that stops at a
// stray word is no claim.
func TestReviewParserNameEnds(t *testing.T) {
	x := textLayer(t, evidence.LandmarkCopyright, "Aus dem A von A 0\nTranslated by John Roe 2nd\nTranslated by J AA and J")
	// An ISBN printed twice: the second, with no label, is the first's quote.
	y := single(t, evidence.LandmarkCopyright, "ISBN 978-1-4028-9462-6 (ebook)\nISBN 978-1-4028-9462-6")
	for _, c := range Parse(y) {
		if v := Verify(y, c); v.Status != StatusGrounded || v.Label != c.Label {
			t.Errorf("parser claim %s: %s/%q", brief(c), v.Status, v.Label)
		}
	}
	for _, c := range Parse(x) {
		if c.Field == FieldContributor && c.Value != "J AA" {
			t.Errorf("parser claimed %s from %q", brief(c), c.Quote)
		}
	}
}

// An occurrence inside an excluded section (another book's ad, an "Also by"
// page) is no evidence either way: the quote's other occurrence decides.
func TestExcludedOccurrencesDontMakeAQuoteAmbiguous(t *testing.T) {
	x := excerptOf(t,
		unitText{evidence.LandmarkFront, "Also by Priya Brandt\nThe Salt Road"},
		unitText{evidence.LandmarkTitle, "THE X\nby Priya Brandt"},
		unitText{evidence.LandmarkBack, "THE X\nby Priya Brandt"},
	)
	v := Verify(x, Claim{Field: FieldContributor, Value: "Priya Brandt", Quote: "by Priya Brandt"})
	if v.Status != StatusGrounded || v.Role != RoleAuthor || v.Unit != 2 {
		t.Fatalf("by-line beside an Also-by page: %+v", v)
	}
	// Every occurrence excluded: excluded, not ambiguous.
	y := excerptOf(t,
		unitText{evidence.LandmarkFront, "Also by Priya Brandt\nThe Salt Road"},
		unitText{evidence.LandmarkBack, "Praise for The Salt Road"},
	)
	if v := Verify(y, Claim{Field: FieldTitle, Value: "The Salt Road", Quote: "The Salt Road"}); v.Status != StatusExcludedSection {
		t.Fatalf("only excluded occurrences: %+v", v)
	}
	// Two kept occurrences in two units stay ambiguous.
	z := excerptOf(t, unitText{evidence.LandmarkTitle, "THE X\nby Priya Brandt"}, unitText{evidence.LandmarkCopyright, "Translated by Priya Brandt"})
	if v := Verify(z, Claim{Field: FieldContributor, Value: "Priya Brandt", Quote: "Priya Brandt"}); v.Status != StatusAmbiguous {
		t.Fatalf("two kept occurrences: %+v", v)
	}
}

// The parser claims where the verifier rules: an earlier copy in an excluded
// section doesn't stop the claim.
func TestParserClaimsPastAnExcludedCopy(t *testing.T) {
	x := excerptOf(t,
		unitText{evidence.LandmarkFront, "Also by Jane Doe\nBook 2 of the Lantern Chronicles"},
		unitText{evidence.LandmarkTitle, "THE X\nBook 2 of the Lantern Chronicles"},
	)
	var got []string
	for _, c := range Parse(x) {
		if v := Verify(x, c); v.Status == StatusGrounded {
			got = append(got, brief(c))
		}
	}
	if !slices.Contains(got, "series=Lantern Chronicles") {
		t.Errorf("grounded parser claims = %q", got)
	}
}

func TestParserLabelsWhereTheVerifierRules(t *testing.T) {
	x := excerptOf(t,
		unitText{evidence.LandmarkFront, "Also by Jane Doe\nISBN 978-1-4028-9462-6 (hardcover)"},
		unitText{evidence.LandmarkCopyright, "ISBN 978-1-4028-9462-6 (ebook)"},
	)
	for _, c := range Parse(x) {
		if c.Field != FieldISBN {
			continue
		}
		if v := Verify(x, c); v.Status != StatusGrounded || v.Label != c.Label || c.Label != "ebook" {
			t.Errorf("parser claim %s: %s/%q", brief(c), v.Status, v.Label)
		}
	}
}
