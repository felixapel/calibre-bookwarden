package extract

import (
	"strings"
	"testing"
)

// Extract v4 (ADR-046): a value that differs from the print only in letter
// case, dash style or an invisible format character verifies, and what is
// sealed is the print.
func TestV4Folds(t *testing.T) {
	title := func(v, q string) Claim { return Claim{Field: FieldTitle, Value: v, Quote: q} }
	imprint := func(v, q string) Claim { return Claim{Field: FieldImprint, Value: v, Quote: q} }
	series := func(f, v, q string) Claim { return Claim{Field: f, Value: v, Quote: q} }
	isbn := func(v, q string) Claim { return Claim{Field: FieldISBN, Value: v, Quote: q} }
	edition := func(v, q string) Claim { return Claim{Field: FieldEdition, Value: v, Quote: q} }
	runReview(t, []reviewCase{
		// Now verify; the sealed value is the print.
		{"a title in capitals", onTitlePage("THE HOBBIT\nby Jane Doe"), title("The Hobbit", "THE HOBBIT"), StatusGrounded, "THE HOBBIT", ""},
		{"an en dash against a hyphen", onTitlePage("Night – Ferry\nby Jane Doe"), title("Night - Ferry", "Night – Ferry"), StatusGrounded, "Night – Ferry", ""},
		{"a zero-width space inside a printed word", onTitlePage("Lan\u200btern Years\nby Jane Doe"), title("Lantern Years", "Lan\u200btern Years"), StatusGrounded, "Lantern Years", ""},
		{"a zero-width space copied into the value", onTitlePage("Lan\u200btern Years\nby Jane Doe"), title("Lan\u200btern Years", "Lan\u200btern Years"), StatusGrounded, "Lantern Years", ""},
		{"a word joiner and a BOM", onTitlePage("Night\u2060 Fer\ufeffry\nby Jane Doe"), title("Night Ferry", "Night\u2060 Fer\ufeffry"), StatusGrounded, "Night Ferry", ""},
		{"an imprint in capitals", onTitlePage("THE X\nHARBOR HOUSE"), imprint("Harbor House", "HARBOR HOUSE"), StatusGrounded, "HARBOR HOUSE", ""},
		{"a name in capitals", onTitlePage("THE X\nBY JANE DOE"), contrib("Jane Doe", "BY JANE DOE", ""), StatusGrounded, "JANE DOE", RoleAuthor},
		{"an inverted name in capitals", onTitlePage("THE X\nby DOE, JANE"), contrib("Jane Doe", "by DOE, JANE", ""), StatusGrounded, "JANE DOE", RoleAuthor},
		{"a series in capitals", onTitlePage("THE X\nBOOK 2 OF THE LANTERN CHRONICLES"), series(FieldSeries, "Lantern Chronicles", "BOOK 2 OF THE LANTERN CHRONICLES"), StatusGrounded, "LANTERN CHRONICLES", ""},
		{"an edition in capitals", onCopyrightPage("SECOND EDITION"), edition("Second Edition", "SECOND EDITION"), StatusGrounded, "SECOND EDITION", ""},
		{"an ISBN printed with em dashes", onCopyrightPage("ISBN 978—0—306—40615—7"), isbn("9780306406157", "ISBN 978—0—306—40615—7"), StatusGrounded, "9780306406157", ""},
		// Still refused.
		{"letters that match only after NFKC", onTitlePage("ﬁnal Days\nby Jane Doe"), title("final Days", "ﬁnal Days"), StatusValueNotDerived, "", ""},
		{"a Cyrillic look-alike", onTitlePage("THE НОВВIT\nby Jane Doe"), title("The Hobbit", "THE НОВВIT"), StatusValueNotDerived, "", ""},
		{"a value over a role line, in another case", onTitlePage("THE X\nBY JANE DOE"), title("The X by Jane Doe", "THE X BY JANE DOE"), StatusValueNotDerived, "", ""},
		{"a value only inside another word", onTitlePage("LANTERNS\nby Jane Doe"), title("Lantern", "LANTERNS"), StatusValueNotDerived, "", ""},
		{"a name only inside another word", onTitlePage("THE X\nBY JANET DOE"), contrib("Jane Doe", "BY JANET DOE", ""), StatusValueNotDerived, "", ""},
		{"two names joined by an em dash", onTitlePage("THE X\nJane Doe—John Roe"), contrib("Jane Doe-John Roe", "Jane Doe—John Roe", ""), StatusValueNotDerived, "", ""},
		// agy review of #166: an invisible character must not hide a role
		// line from the check that a title or imprint doesn't cross one.
		{"a title over a by-line behind a BOM", onTitlePage("THE HOBBIT\n\ufeffby Jane Doe"), title("The Hobbit by Jane Doe", "THE HOBBIT \ufeffby Jane Doe"), StatusValueNotDerived, "", ""},
		{"a title over a by-line split by a ZWSP", onTitlePage("THE HOBBIT\nB\u200bY JANE DOE"), title("The Hobbit BY JANE DOE", "THE HOBBIT B\u200bY JANE DOE"), StatusValueNotDerived, "", ""},
		{"an imprint over a role line behind a word joiner", onTitlePage("THE X\nHarbor House\n\u2060Translated by John Roe"), imprint("Harbor House Translated by John Roe", "Harbor House \u2060Translated by John Roe"), StatusValueNotDerived, "", ""},
		{"a zero-width space before a combining mark stays refused", onTitlePage("Cafe\u200b\u0301 Noir\nby Jane Doe"), title("Cafe\u0301 Noir", "Cafe\u200b\u0301 Noir"), StatusValueNotDerived, "", ""},
		// strings.ToLower is simple case mapping (ADR-046 §1).
		{"sharp s against SS", onTitlePage("STRASSE\nby Jane Doe"), title("Straße", "STRASSE"), StatusValueNotDerived, "", ""},
		{"a final sigma against a capital sigma", onTitlePage("ΟΔΟΣ\nby Jane Doe"), title("οδος", "ΟΔΟΣ"), StatusValueNotDerived, "", ""},
		{"a dotted capital I seals the print", onTitlePage("İSTANBUL\nby Jane Doe"), title("istanbul", "İSTANBUL"), StatusGrounded, "İSTANBUL", ""},
		{"an edition printed twice keeps the claimed spelling", onCopyrightPage("Second Edition; SECOND EDITION"), edition("SECOND EDITION", "Second Edition; SECOND EDITION"), StatusGrounded, "SECOND EDITION", ""},
		{"a bidi control", onTitlePage("\u202eTHE X\nby Jane"), title("THE X", "\u202eTHE X"), StatusValueNotDerived, "", ""},
		{"a superscript against a digit", onTitlePage("Room ²\nby Jane Doe"), title("Room 2", "Room ²"), StatusValueNotDerived, "", ""},
		{"an ISBN with a bad checksum, em dashes", onCopyrightPage("ISBN 978—0—306—40615—8"), isbn("9780306406158", "ISBN 978—0—306—40615—8"), StatusValueNotDerived, "", ""},
	})
}

// One extractor quoting a person twice (the cover and the title page) agrees
// with one that quotes the title page only (audit Q3-B): the first
// occurrence per name and role is the list.
func TestAgreeOnePersonQuotedTwice(t *testing.T) {
	jane := func(source string, unit, start int) Verified {
		return Verified{Claim: Claim{Field: FieldContributor, Value: "Jane Doe", Role: RoleAuthor, Source: source}, Status: StatusGrounded, Unit: unit, Start: start, End: start + 8}
	}
	roe := func(source string, unit, start int) Verified {
		return Verified{Claim: Claim{Field: FieldContributor, Value: "John Roe", Role: RoleTranslator, Source: source}, Status: StatusGrounded, Unit: unit, Start: start, End: start + 8}
	}
	facts := Agree([]Verified{
		jane("a", 1, 10), jane("a", 2, 10), roe("a", 2, 40),
		jane("b", 2, 10), roe("b", 2, 40),
	}, []string{"a", "b"})
	var got *Fact
	for i := range facts {
		if facts[i].Key == FieldContributor {
			got = &facts[i]
		}
	}
	if got == nil || got.Outcome != OutcomeAgreed || len(got.Values) != 2 || got.Values[0].Value != "Jane Doe" || got.Values[1].Value != "John Roe" {
		t.Fatalf("contributors = %+v, want agreed [Jane Doe, John Roe]", got)
	}
	// Two different people stay a disagreement.
	facts = Agree([]Verified{jane("a", 1, 10), roe("a", 2, 40), jane("b", 2, 10)}, []string{"a", "b"})
	for _, f := range facts {
		if f.Key == FieldContributor && f.Outcome != OutcomeDisagree {
			t.Fatalf("a list with a translator against one without: %+v", f)
		}
	}
}

func TestV4Version(t *testing.T) {
	if Version != 4 {
		t.Fatalf("Version = %d, want 4 (ADR-046)", Version)
	}
}

// Review of #166: an invisible character must not hide a role phrase or an
// excluded heading from the readers around a quote either.
func TestInvisibleCharactersHideNoContext(t *testing.T) {
	title := func(v, q string) Claim { return Claim{Field: FieldTitle, Value: v, Quote: q} }
	runReview(t, []reviewCase{
		{"an edited line split by a ZWSP", onTitlePage("THE X\nEdi\u200bted\nby Pat Keller"), contrib("Pat Keller", "by Pat Keller", ""), StatusGrounded, "Pat Keller", RoleEditor},
		{"a translated-from line split by a ZWSP", onTitlePage("THE X\nJane Doe\nTrans\u200blated from the French\nby John Roe"), contrib("John Roe", "by John Roe", ""), StatusGrounded, "John Roe", RoleTranslator},
		{"an introduction line split by a word joiner", onTitlePage("THE X\nby Jane Doe\nWith an Intro\u2060duction\nby Mary Sample"), contrib("Mary Sample", "by Mary Sample", ""), StatusGrounded, "Mary Sample", RoleIntroduction},
		{"translated by over the name, split by a BOM", onTitlePage("THE X\nTrans\ufefflated by\nJohn Roe"), contrib("John Roe", "John Roe", ""), StatusGrounded, "John Roe", RoleTranslator},
		{"a second name under a split translated by", onTitlePage("THE X\nTrans\u200blated by\nJohn Roe\nMary Sample"), contrib("Mary Sample", "Mary Sample", RoleAuthor), StatusRoleUnproven, "", ""},
		{"an Also by heading split inside a word", onTitlePage("THE X\nAl\u200bso by Jane Doe\nThe Glass Orchard"), title("The Glass Orchard", "The Glass Orchard"), StatusExcludedSection, "", ""},
		// Review of #167: dropped characters must not move the "by" check.
		{"many invisibles before a by that isn't line-initial", onTitlePage("THE X\nby Jane Doe\n" + strings.Repeat("\u200b", 14) + "Cover design by\nJohn Smith"), contrib("John Smith", "John Smith", ""), StatusRoleUnproven, "", ""},
		// Control: a ZWSP between the heading's words still reads as a space.
		{"an Also by heading joined by a ZWSP", onTitlePage("THE X\nAlso\u200bby Jane Doe\nThe Glass Orchard"), title("The Glass Orchard", "The Glass Orchard"), StatusExcludedSection, "", ""},
	})
}

// An invisible character in the text around a quote changes no verdict: each
// page reads as the same page without it.
func TestInvisibleCharactersAroundAQuoteChangeNothing(t *testing.T) {
	cases := []struct {
		page, invisible string
		claim           Claim
	}{
		{"THE X\nEdi|ted\nPat Keller", "\u200b", contrib("Pat Keller", "Pat Keller", "")},
		{"THE X\nEdi|ted\nPat Keller", "\u2060", contrib("Pat Keller", "Pat Keller", RoleAuthor)},
		{"THE X\nFrom the Fre|nch\nJohn Roe", "\ufeff", contrib("John Roe", "John Roe", "")},
		{"THE X\n|and\nJohn Roe", "\u200b", contrib("John Roe", "John Roe", "")},
		{"THE X\nTrans|lated by\nJohn Roe", "\u200b", contrib("John Roe", "John Roe", "")},
		{"THE X\nWith an Intro|duction\nby Mary Sample", "\u2060", contrib("Mary Sample", "by Mary Sample", "")},
		{"THE X\nAl|so by Jane Doe\nThe Glass Orchard", "\u200b", Claim{Field: FieldTitle, Value: "The Glass Orchard", Quote: "The Glass Orchard"}},
		{"THE X\nby Jane Doe\n|Cover design by\nJohn Smith", strings.Repeat("\u200b", 14), contrib("John Smith", "John Smith", "")},
		{"THE X\n|Illustrated by\nJohn Smith", strings.Repeat("\u2060", 3), contrib("John Smith", "John Smith", "")},
	}
	for _, tc := range cases {
		plain := Verify(single(t, "title", strings.ReplaceAll(tc.page, "|", "")), tc.claim)
		hidden := Verify(single(t, "title", strings.ReplaceAll(tc.page, "|", tc.invisible)), tc.claim)
		if plain.Status != hidden.Status || plain.Role != hidden.Role || plain.Value != hidden.Value {
			t.Errorf("%q with %U: %s %q %q, without it: %s %q %q", tc.page, []rune(tc.invisible)[0], hidden.Status, hidden.Value, hidden.Role, plain.Status, plain.Value, plain.Role)
		}
	}
}

// Review of #167: an invisible character must not hide a year's label
// either; the label that governs the year is read through it.
func TestInvisibleCharactersHideNoYearLabel(t *testing.T) {
	year := func(v, q string) Claim { return Claim{Field: FieldYear, Value: v, Quote: q} }
	runReview(t, []reviewCase{
		{"a this-edition label split by ZWSPs", onCopyrightPage("First published by Harbor House and this edi\u200btion pub\u200blished 2003"), year("2003", "First published by Harbor House and this edi\u200btion pub\u200blished 2003"), StatusGrounded, "2003", LabelThisEdition},
		{"a ZWSP right after the year", onCopyrightPage("© 2019\u200b Jane Doe"), year("2019", "© 2019"), StatusGrounded, "2019", LabelCopyright},
		{"a year split by a ZWSP is no year", onCopyrightPage("© 20\u200b03 Jane Doe"), year("2003", "© 20\u200b03"), StatusValueNotDerived, "", ""},
		{"a word joiner inside a first-published label", onCopyrightPage("First pub\u2060lished 1925"), year("1925", "First pub\u2060lished 1925"), StatusGrounded, "1925", LabelFirstPublished},
	})
	cases := []struct{ page, invisible string }{
		{"First published by Harbor House and this edi|tion published 2003", "\u200b"},
		{"© 2019| Jane Doe", strings.Repeat("\u2060", 5)},
		{"|First published 1925. Reprinted 2019", strings.Repeat("\ufeff", 9)},
	}
	for _, tc := range cases {
		for _, y := range []string{"2003", "2019", "1925"} {
			c := Claim{Field: FieldYear, Value: y, Quote: strings.ReplaceAll(tc.page, "|", "")}
			plain := Verify(single(t, "copyright", strings.ReplaceAll(tc.page, "|", "")), c)
			c.Quote = strings.ReplaceAll(tc.page, "|", tc.invisible)
			hidden := Verify(single(t, "copyright", c.Quote), c)
			if plain.Status != hidden.Status || plain.Label != hidden.Label {
				t.Errorf("%q year %s with %U: %s %q, without it: %s %q", tc.page, y, []rune(tc.invisible)[0], hidden.Status, hidden.Label, plain.Status, plain.Label)
			}
		}
	}
}
