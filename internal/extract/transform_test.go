package extract

import (
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

// All fixtures are synthetic: invented titles, people and publishers.

const titlePage = "THE LANTERN YEARS\nA Novel\nby Jane Doe\nTranslated from the French by John Roe\n" +
	"With an introduction by Mary Sample\nIllustrated by Tom Pencil\nEdited by Pat Keller"

func TestVerifyContributorRoles(t *testing.T) {
	x := excerptOf(t,
		unitText{evidence.LandmarkTitle, titlePage},
		unitText{evidence.LandmarkCopyright, "Copyright © Jane Doe, 2003\nHarbor House Books\nAnna Beispiel\nOtto Muster\nTranslated by\nNina Wende\nby Ann Lone and Bob Duo"},
	)
	tests := []struct {
		name         string
		field        string
		value, quote string
		role         string // claimed
		want         string
		wantRole     string
	}{
		{"by-line", FieldContributor, "Jane Doe", "by Jane Doe", RoleAuthor, StatusGrounded, RoleAuthor},
		{"by-line, role left to the text", FieldContributor, "Jane Doe", "by Jane Doe", "", StatusGrounded, RoleAuthor},
		{"translator with language", FieldContributor, "John Roe", "Translated from the French by John Roe", RoleTranslator, StatusGrounded, RoleTranslator},
		{"translator name only, phrase on the line", FieldContributor, "John Roe", "John Roe", RoleTranslator, StatusGrounded, RoleTranslator},
		{"translator is never an author", FieldContributor, "John Roe", "Translated from the French by John Roe", RoleAuthor, StatusRoleUnproven, ""},
		{"translator name only is never an author", FieldContributor, "John Roe", "John Roe", RoleAuthor, StatusRoleUnproven, ""},
		{"introduction", FieldContributor, "Mary Sample", "With an introduction by Mary Sample", RoleIntroduction, StatusGrounded, RoleIntroduction},
		{"introducer is not an author", FieldContributor, "Mary Sample", "Mary Sample", RoleAuthor, StatusRoleUnproven, ""},
		{"illustrator", FieldContributor, "Tom Pencil", "Illustrated by Tom Pencil", RoleIllustrator, StatusGrounded, RoleIllustrator},
		{"editor", FieldContributor, "Pat Keller", "Edited by Pat Keller", RoleEditor, StatusGrounded, RoleEditor},
		{"editor is not an author", FieldContributor, "Pat Keller", "Edited by Pat Keller", RoleAuthor, StatusRoleUnproven, ""},
		{"name with no phrase in a copyright unit", FieldContributor, "Jane Doe", "Jane Doe", RoleAuthor, StatusAmbiguous, ""}, // also in the title unit
		{"lone name on a copyright page", FieldContributor, "Anna Beispiel", "Anna Beispiel", RoleAuthor, StatusRoleUnproven, ""},
		{"role from the line before", FieldContributor, "Nina Wende", "Nina Wende", RoleTranslator, StatusGrounded, RoleTranslator},
		{"second name after a by", FieldContributor, "Bob Duo", "Bob Duo", RoleAuthor, StatusGrounded, RoleAuthor},
		{"two names in one value", FieldContributor, "Ann Lone and Bob Duo", "by Ann Lone and Bob Duo", RoleAuthor, StatusValueNotDerived, ""},
		{"wrong name", FieldContributor, "Jane Roe", "by Jane Doe", RoleAuthor, StatusValueNotDerived, ""},
		{"role left unclaimed, no phrase", FieldContributor, "Otto Muster", "Otto Muster", "", StatusRoleUnproven, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := Verify(x, Claim{Field: tt.field, Value: tt.value, Quote: tt.quote, Role: tt.role})
			if v.Status != tt.want {
				t.Fatalf("status = %q, want %q: %+v", v.Status, tt.want, v)
			}
			if tt.want == StatusGrounded && (v.Role != tt.wantRole || v.Value != tt.value) {
				t.Errorf("role/value = %q/%q, want %q/%q", v.Role, v.Value, tt.wantRole, tt.value)
			}
		})
	}
}

func TestVerifyLoneNameOnTitlePage(t *testing.T) {
	titleUnit := excerptOf(t, unitText{evidence.LandmarkTitle, "THE LANTERN YEARS\nJane Doe\nHarbor House"})
	if v := Verify(titleUnit, Claim{Field: FieldContributor, Value: "Jane Doe", Quote: "Jane Doe", Role: RoleAuthor}); v.Status != StatusGrounded || v.Role != RoleAuthor {
		t.Errorf("lone name on a title page: %+v", v)
	}
	// The same line inside a longer line is not a lone name.
	inline := excerptOf(t, unitText{evidence.LandmarkTitle, "THE LANTERN YEARS\nA novel of Jane Doe and others\nHarbor House"})
	if v := Verify(inline, Claim{Field: FieldContributor, Value: "Jane Doe", Quote: "Jane Doe", Role: RoleAuthor}); v.Status != StatusRoleUnproven {
		t.Errorf("name inside a line: status = %q, want role_unproven", v.Status)
	}
	// A lone name in a front unit (not the title page) proves nothing.
	front := excerptOf(t, unitText{evidence.LandmarkFront, "THE LANTERN YEARS\nJane Doe"})
	if v := Verify(front, Claim{Field: FieldContributor, Value: "Jane Doe", Quote: "Jane Doe", Role: RoleAuthor}); v.Status != StatusRoleUnproven {
		t.Errorf("lone name in a front unit: status = %q, want role_unproven", v.Status)
	}
}

func TestVerifyContributorPhrases(t *testing.T) {
	tests := []struct {
		name, text, quote, value, role string
	}{
		{"by", "by Jane Doe", "by Jane Doe", "Jane Doe", RoleAuthor},
		{"BY upper case", "BY JANE DOE", "BY JANE DOE", "JANE DOE", RoleAuthor},
		{"translated by", "Translated by Jane Doe", "Translated by Jane Doe", "Jane Doe", RoleTranslator},
		{"translated from the X by", "Translated from the Ancient Greek by Jane Doe", "Translated from the Ancient Greek by Jane Doe", "Jane Doe", RoleTranslator},
		{"edited by", "Edited by Jane Doe", "Edited by Jane Doe", "Jane Doe", RoleEditor},
		{"with an introduction by", "With an introduction by Jane Doe", "With an introduction by Jane Doe", "Jane Doe", RoleIntroduction},
		{"introduction by", "Introduction by Jane Doe", "Introduction by Jane Doe", "Jane Doe", RoleIntroduction},
		{"foreword by", "Foreword by Jane Doe", "Foreword by Jane Doe", "Jane Doe", RoleIntroduction},
		{"illustrated by", "Illustrated by Jane Doe", "Illustrated by Jane Doe", "Jane Doe", RoleIllustrator},
		{"de: übersetzt von", "Übersetzt von Anna Beispiel", "Übersetzt von Anna Beispiel", "Anna Beispiel", RoleTranslator},
		{"de: aus dem Englischen von", "Aus dem Englischen von Anna Beispiel", "Aus dem Englischen von Anna Beispiel", "Anna Beispiel", RoleTranslator},
		{"de: herausgegeben von", "Herausgegeben von Anna Beispiel", "Herausgegeben von Anna Beispiel", "Anna Beispiel", RoleEditor},
		{"es: traducción de", "Traducción de Ana Ruiz", "Traducción de Ana Ruiz", "Ana Ruiz", RoleTranslator},
		{"es: traducido por", "Traducido por Ana Ruiz", "Traducido por Ana Ruiz", "Ana Ruiz", RoleTranslator},
		{"es: edición de", "Edición de Ana Ruiz", "Edición de Ana Ruiz", "Ana Ruiz", RoleEditor},
		{"fr: traduit de … par", "Traduit de l'anglais par Marie Modèle", "Traduit de l'anglais par Marie Modèle", "Marie Modèle", RoleTranslator},
		{"fr: traduit de … par, curly apostrophe", "Traduit de l’anglais par Marie Modèle", "Traduit de l’anglais par Marie Modèle", "Marie Modèle", RoleTranslator},
		{"fr: édité par", "Édité par Marie Modèle", "Édité par Marie Modèle", "Marie Modèle", RoleEditor},
		{"inverted name", "by Doe, Jane", "by Doe, Jane", "Jane Doe", RoleAuthor},
		{"initials", "by J. R. Doe", "by J. R. Doe", "J. R. Doe", RoleAuthor},
		{"particle", "by Ludwig van Beispiel", "by Ludwig van Beispiel", "Ludwig van Beispiel", RoleAuthor},
		{"apostrophe name", "by Kate O’Hara", "by Kate O'Hara", "Kate O'Hara", RoleAuthor},
		{"diacritics", "by Søren Dåne", "by Søren Dåne", "Søren Dåne", RoleAuthor},
		{"suffix kept", "by Jane Doe Jr.", "by Jane Doe Jr.", "Jane Doe Jr.", RoleAuthor},
		{"sentence period trimmed", "Translated by Jane Doe.", "Translated by Jane Doe.", "Jane Doe", RoleTranslator},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			x := single(t, evidence.LandmarkTitle, "THE LANTERN YEARS\n"+tt.text)
			v := Verify(x, Claim{Field: FieldContributor, Value: tt.value, Quote: tt.quote, Role: tt.role})
			if v.Status != StatusGrounded || v.Role != tt.role || v.Value != tt.value {
				t.Fatalf("got %+v, want grounded %q as %s", v, tt.value, tt.role)
			}
		})
	}
	// Names that are not a name, or that pack several people.
	for _, text := range []string{"by Jane Doe, translated by John Roe", "by Jane Doe; John Roe", "by Jane Doe & John Roe", "by Doe, Jane, Roe", "by the sea", "by 2019", "by Jane Doe (2019)"} {
		x := single(t, evidence.LandmarkTitle, "THE LANTERN YEARS\n"+text)
		v := Verify(x, Claim{Field: FieldContributor, Value: text[3:], Quote: text, Role: RoleAuthor})
		if v.Status != StatusValueNotDerived {
			t.Errorf("%q: status = %q, want value_not_derived", text, v.Status)
		}
	}
}

func TestVerifyYear(t *testing.T) {
	x := single(t, evidence.LandmarkCopyright, "First published in Great Britain by Cape Lantern Ltd 2003\nThis edition published 2021\n"+
		"Copyright © Jane Doe, 2003\n(c) 2019 Harbor House\nCopyright 2018\nErstausgabe 2017\nPrimera edición, 2016\nPublished 2015\n"+
		"First published 2001, this edition published 2022\nPrinted in 1999\nISBN 978-1-4028-9462-6\n© ISBN 0-1411-1877-6\nReprinted 2005 © 2006")
	tests := []struct {
		name         string
		value, quote string
		label        string // claimed
		want         string
		wantLabel    string
	}{
		{"first published after words", "2003", "First published in Great Britain by Cape Lantern Ltd 2003", "", StatusGrounded, LabelFirstPublished},
		{"this edition", "2021", "This edition published 2021", "", StatusGrounded, LabelThisEdition},
		{"copyright", "2003", "Copyright © Jane Doe, 2003", "", StatusGrounded, LabelCopyright},
		{"(c)", "2019", "(c) 2019", "", StatusGrounded, LabelCopyright},
		{"Copyright word", "2018", "Copyright 2018", "", StatusGrounded, LabelCopyright},
		{"Erstausgabe", "2017", "Erstausgabe 2017", "", StatusGrounded, LabelFirstPublished},
		{"Primera edición", "2016", "Primera edición, 2016", "", StatusGrounded, LabelFirstPublished},
		{"Published", "2015", "Published 2015", "", StatusGrounded, LabelThisEdition},
		{"two labelled years, first", "2001", "First published 2001, this edition published 2022", "", StatusGrounded, LabelFirstPublished},
		{"two labelled years, second", "2022", "First published 2001, this edition published 2022", "", StatusGrounded, LabelThisEdition},
		{"claimed label matches", "2022", "First published 2001, this edition published 2022", LabelThisEdition, StatusGrounded, LabelThisEdition},
		{"claimed label does not", "2022", "First published 2001, this edition published 2022", LabelFirstPublished, StatusValueNotDerived, ""},
		{"year with no label", "1999", "Printed in 1999", "", StatusValueNotDerived, ""},
		{"bare year", "2003", "2003", "", StatusValueNotDerived, ""},
		{"year not in the quote", "2020", "This edition published 2021", "", StatusValueNotDerived, ""},
		{"value not a year", "twenty twenty-one", "This edition published 2021", "", StatusValueNotDerived, ""},
		{"label after the year does not count", "2005", "Reprinted 2005 © 2006", "", StatusValueNotDerived, ""},
		{"digits of an ISBN are not a year", "1877", "© ISBN 0-1411-1877-6", "", StatusValueNotDerived, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := Verify(x, Claim{Field: FieldYear, Value: tt.value, Quote: tt.quote, Label: tt.label})
			if v.Status != tt.want {
				t.Fatalf("status = %q, want %q: %+v", v.Status, tt.want, v)
			}
			if tt.want == StatusGrounded && (v.Label != tt.wantLabel || v.Value != tt.value) {
				t.Errorf("label/value = %q/%q, want %q/%q", v.Label, v.Value, tt.wantLabel, tt.value)
			}
		})
	}
}

func TestVerifyEdition(t *testing.T) {
	x := single(t, evidence.LandmarkCopyright, "Second Edition\n2nd edition\nRevised edition\n25th Anniversary Edition\nAnniversary Edition\n"+
		"Erste Auflage\nZweite, überarbeitete Auflage\nPrimera edición\nSegunda edición revisada\nThis is the second printing\nDeluxe Edition, with extras\nPremière édition")
	tests := []struct {
		name         string
		value, quote string
		want         string
	}{
		{"Second Edition", "Second Edition", "Second Edition", StatusGrounded},
		{"2nd edition", "2nd edition", "2nd edition", StatusGrounded},
		{"Revised edition", "Revised edition", "Revised edition", StatusGrounded},
		{"25th Anniversary Edition", "25th Anniversary Edition", "25th Anniversary Edition", StatusGrounded},
		{"Anniversary Edition", "Anniversary Edition", "Anniversary Edition", StatusGrounded},
		{"Erste Auflage", "Erste Auflage", "Erste Auflage", StatusGrounded},
		{"Zweite, überarbeitete Auflage", "Zweite, überarbeitete Auflage", "Zweite, überarbeitete Auflage", StatusGrounded},
		{"Primera edición", "Primera edición", "Primera edición", StatusGrounded},
		{"Segunda edición revisada", "Segunda edición revisada", "Segunda edición revisada", StatusGrounded},
		{"Première édition", "Première édition", "Première édition", StatusGrounded},
		{"statement inside a longer quote", "Deluxe Edition", "Deluxe Edition, with extras", StatusGrounded},
		{"another edition", "Third Edition", "Second Edition", StatusValueNotDerived},
		{"case folds (v4)", "second edition", "Second Edition", StatusGrounded},
		{"a printing is not an edition", "second printing", "This is the second printing", StatusValueNotDerived},
		{"outside the grammar", "This is the second printing", "This is the second printing", StatusValueNotDerived},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := Verify(x, Claim{Field: FieldEdition, Value: tt.value, Quote: tt.quote})
			if v.Status != tt.want {
				t.Fatalf("status = %q, want %q: %+v", v.Status, tt.want, v)
			}
			// The value is the print (v4): the claimed words, as printed.
			if tt.want == StatusGrounded && (!strings.EqualFold(v.Value, tt.value) || !strings.Contains(tt.quote, v.Value)) {
				t.Errorf("value = %q", v.Value)
			}
		})
	}
}

func TestVerifySeries(t *testing.T) {
	x := single(t, evidence.LandmarkTitle, "THE LANTERN YEARS\nBook 2 of the Lantern Chronicles\nVolume 3 in the Glass Orchard series\nThe Night Ferry Cycle, Book 4\n"+
		"Die Glasgarten-Saga, Band 5\nCinder Road #2.5\nBook Three of the Lantern Chronicles\nThird in the Lantern Chronicles series\n"+
		"3rd in the Lantern Chronicles series\nBand 4\n#7\n#1 New York Times Bestseller\nBook Thirteen\nLibro siete\nBand zwölf\nTomo cuatro\nBook 02 of the Lantern Chronicles")
	tests := []struct {
		name         string
		field        string
		value, quote string
		want         string
	}{
		{"series: Book N of the X", FieldSeries, "Lantern Chronicles", "Book 2 of the Lantern Chronicles", StatusGrounded},
		{"index: Book N of the X", FieldSeriesIndex, "2", "Book 2 of the Lantern Chronicles", StatusGrounded},
		{"series: Volume N in the X series", FieldSeries, "Glass Orchard", "Volume 3 in the Glass Orchard series", StatusGrounded},
		{"index: Volume N in", FieldSeriesIndex, "3", "Volume 3 in the Glass Orchard series", StatusGrounded},
		{"series: X, Book N", FieldSeries, "The Night Ferry Cycle", "The Night Ferry Cycle, Book 4", StatusGrounded},
		{"index: X, Book N", FieldSeriesIndex, "4", "The Night Ferry Cycle, Book 4", StatusGrounded},
		{"series: X, Band N", FieldSeries, "Die Glasgarten-Saga", "Die Glasgarten-Saga, Band 5", StatusGrounded},
		{"index: X, Band N", FieldSeriesIndex, "5", "Die Glasgarten-Saga, Band 5", StatusGrounded},
		{"series: X #N", FieldSeries, "Cinder Road", "Cinder Road #2.5", StatusGrounded},
		{"index: X #N is a decimal", FieldSeriesIndex, "2.5", "Cinder Road #2.5", StatusGrounded},
		{"index: number word", FieldSeriesIndex, "3", "Book Three of the Lantern Chronicles", StatusGrounded},
		{"series: number word", FieldSeries, "Lantern Chronicles", "Book Three of the Lantern Chronicles", StatusGrounded},
		{"index: Nth in the X series, word", FieldSeriesIndex, "3", "Third in the Lantern Chronicles series", StatusGrounded},
		{"series: Nth in the X series", FieldSeries, "Lantern Chronicles", "Third in the Lantern Chronicles series", StatusGrounded},
		{"index: Nth in the X series, digits", FieldSeriesIndex, "3", "3rd in the Lantern Chronicles series", StatusGrounded},
		{"index: Band N alone grounds nothing", FieldSeriesIndex, "4", "Band 4", StatusValueNotDerived},
		{"index: #N alone grounds nothing", FieldSeriesIndex, "7", "#7", StatusValueNotDerived},
		{"index: Libro word es alone", FieldSeriesIndex, "7", "Libro siete", StatusValueNotDerived},
		{"index: Band word de alone", FieldSeriesIndex, "12", "Band zwölf", StatusValueNotDerived},
		{"index: Tomo word es alone", FieldSeriesIndex, "4", "Tomo cuatro", StatusValueNotDerived},
		{"index: leading zero is canonical", FieldSeriesIndex, "2", "Book 02 of the Lantern Chronicles", StatusGrounded},
		{"index: value with zeros", FieldSeriesIndex, "2.0", "Book 2 of the Lantern Chronicles", StatusGrounded},
		{"wrong index", FieldSeriesIndex, "3", "Book 2 of the Lantern Chronicles", StatusValueNotDerived},
		{"wrong series", FieldSeries, "Lantern Chronicle", "Book 2 of the Lantern Chronicles", StatusValueNotDerived},
		{"series from a bare index", FieldSeries, "Band", "Band 4", StatusValueNotDerived},
		{"a bestseller badge is not an index", FieldSeriesIndex, "1", "#1", StatusValueNotDerived},
		{"thirteen is beyond the number words", FieldSeriesIndex, "13", "Book Thirteen", StatusValueNotDerived},
		{"index is not decimal", FieldSeriesIndex, "two", "Book 2 of the Lantern Chronicles", StatusValueNotDerived},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := Verify(x, Claim{Field: tt.field, Value: tt.value, Quote: tt.quote})
			if v.Status != tt.want {
				t.Fatalf("status = %q, want %q: %+v", v.Status, tt.want, v)
			}
			if tt.want == StatusGrounded && tt.name != "index: value with zeros" && v.Value != tt.value {
				t.Errorf("derived value = %q, want %q", v.Value, tt.value)
			}
		})
	}
}

func TestVerifyISBN(t *testing.T) {
	const print13, ebook13 = "9781402894626", "9780306406157"
	x := excerptOf(t,
		unitText{evidence.LandmarkCopyright, "ISBN 978-1-4028-9462-6 (paperback)\nISBN 978-0-306-40615-7 (ebook)\neBook ISBN: 978-0-19-852663-6\nISBN-10: 0-19-852663-6\nISBN 978-1-4028-9462-7"},
		unitText{evidence.LandmarkFront, "Also by Jane Doe\nThe Glass Orchard ISBN 978-0-14-118776-1"},
		unitText{evidence.LandmarkBack, "Harbor House Books\nISBN 978-0-7432-7356-5 (hardcover)"},
	)
	// ISBN-10 0-19-852663-6 is ISBN-13 978-0-19-852663-6.
	const other, ten = "9780198526636", "9780198526636"
	tests := []struct {
		name         string
		value, quote string
		label        string
		want         string
		wantLabel    string
	}{
		{"print ISBN, label after", print13, "ISBN 978-1-4028-9462-6", "", StatusGrounded, evidence.QualifierPaperback},
		{"ebook ISBN, label after", ebook13, "ISBN 978-0-306-40615-7", "", StatusGrounded, evidence.QualifierEbook},
		{"label before, outside the quote", other, "978-0-19-852663-6", "", StatusGrounded, evidence.QualifierEbook},
		{"hyphenated value", "978-1-4028-9462-6", "ISBN 978-1-4028-9462-6", "", StatusGrounded, evidence.QualifierPaperback},
		{"claimed qualifier matches", ebook13, "ISBN 978-0-306-40615-7", evidence.QualifierEbook, StatusGrounded, evidence.QualifierEbook},
		{"claimed qualifier does not", ebook13, "ISBN 978-0-306-40615-7", evidence.QualifierPrint, StatusValueNotDerived, ""},
		{"ISBN-10 converts to ISBN-13", ten, "ISBN-10: 0-19-852663-6", "", StatusGrounded, ""},
		{"bad checksum", "9781402894627", "ISBN 978-1-4028-9462-7", "", StatusValueNotDerived, ""},
		{"value is another ISBN", ebook13, "ISBN 978-1-4028-9462-6", "", StatusValueNotDerived, ""},
		{"value is not an ISBN", "12345", "ISBN 978-1-4028-9462-6", "", StatusValueNotDerived, ""},
		{"in an Also by list", "9780141187761", "ISBN 978-0-14-118776-1", "", StatusExcludedSection, ""},
		{"back unit keeps ISBNs, not their qualifiers", "9780743273565", "ISBN 978-0-7432-7356-5", "", StatusGrounded, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := Verify(x, Claim{Field: FieldISBN, Value: tt.value, Quote: tt.quote, Label: tt.label})
			if v.Status != tt.want {
				t.Fatalf("status = %q, want %q: %+v", v.Status, tt.want, v)
			}
			if tt.want == StatusGrounded && v.Label != tt.wantLabel {
				t.Errorf("qualifier = %q, want %q", v.Label, tt.wantLabel)
			}
		})
	}
	// An imprint line that only the back unit prints is dropped, ISBNs are not.
	only := excerptOf(t, unitText{evidence.LandmarkTitle, "THE LANTERN YEARS"}, unitText{evidence.LandmarkBack, "Night Press\nISBN 978-0-7432-7356-5"})
	if v := Verify(only, Claim{Field: FieldImprint, Value: "Night Press", Quote: "Night Press"}); v.Status != StatusExcludedSection {
		t.Errorf("imprint in a back unit: %+v", v)
	}
	if v := Verify(only, Claim{Field: FieldISBN, Value: "9780743273565", Quote: "ISBN 978-0-7432-7356-5"}); v.Status != StatusGrounded {
		t.Errorf("ISBN in a back unit: %+v", v)
	}
}
