package extract

import (
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

// A refused value says why, for the reverify report only; a verified one says
// nothing.
func TestRefusalReason(t *testing.T) {
	for _, tt := range []struct {
		name, text, value, quote, want string
		field                          string // FieldTitle when empty
		landmark, label                string // LandmarkTitle when empty
	}{
		{"verifies", "Night Ferry", "Night Ferry", "Night Ferry", "", "", "", ""},
		{"another value", "Night Ferry", "Day Train", "Night Ferry", RefusedNotInQuote, "", "", ""},
		{"case only, now verified (v4)", "NIGHT FERRY", "Night Ferry", "NIGHT FERRY", "", "", "", ""},
		{"dash, now verified (v4)", "Night – Ferry", "Night - Ferry", "Night – Ferry", "", "", "", ""},
		{"format character, now verified (v4)", "Night\u200b Ferry", "Night Ferry", "Night\u200b Ferry", "", "", "", ""},
		{"edge punctuation", "Night Ferry", "Night Ferry!", "Night Ferry", RefusedEdgeTrim, "", "", ""},
		{"a role line", "Night Ferry\nTranslated by John Roe", "Night Ferry\nTranslated by John Roe", "Night Ferry\nTranslated by John Roe", RefusedRoleLine, "", "", ""},
		// Review of #154: a title or imprint must equal the trimmed quote, so
		// a value that is only part of it is no case difference.
		{"part of a title quote, in another case", "NIGHT FERRY: A NOVEL", "Night Ferry", "NIGHT FERRY: A NOVEL", RefusedOther, "", "", ""},
		{"part of an imprint quote, in another case", "HARBOR HOUSE PUBLISHING", "Harbor House", "HARBOR HOUSE PUBLISHING", RefusedOther, FieldImprint, "", ""},
		{"a role line in another case", "NIGHT FERRY\nTranslated by John Roe", "Night Ferry\nTranslated by John Roe", "NIGHT FERRY\nTranslated by John Roe", RefusedRoleLine, "", "", ""},
		{"a format character copied into the value, now verified (v4)", "Night\u200b Ferry", "Night\u200b Ferry", "Night\u200b Ferry", "", "", "", ""},
		{"edge punctuation in the quote", "Night Ferry!", "Night Ferry", "Night Ferry!", RefusedEdgeTrim, "", "", ""},
		// Server run 32 (R3): a value printed as is but refused for another
		// reason matched before any fold, and counted as case_only.
		{"a year printed as is, under another label", "Copyright © 2019", "2019", "Copyright © 2019", RefusedOther, FieldYear, evidence.LandmarkCopyright, LabelFirstPublished},
		{"an ISBN printed as is, under another label", "ISBN 978-0-306-40615-7 (ebook)", "978-0-306-40615-7", "ISBN 978-0-306-40615-7 (ebook)", RefusedOther, FieldISBN, evidence.LandmarkCopyright, "hardcover"},
		{"a title printed as is, with a label it can't have", "Night Ferry", "Night Ferry", "Night Ferry", RefusedOther, "", "", "x"},
		// Review of #161: a field parsed into a name, a series or an ISBN
		// compares as derive does — what folding case fixes is case_only,
		// what no fold fixes is other.
		{"a name in capitals, now verified (v4)", "THE X\nJANE DOE", "Jane Doe", "JANE DOE", "", FieldContributor, "", ""},
		{"an inverted name in capitals, now verified (v4)", "THE X\nDOE, JANE", "Jane Doe", "DOE, JANE", "", FieldContributor, "", ""},
		{"one name of a list in capitals", "JANE DOE AND JOHN ROE", "Jane Doe", "JANE DOE AND JOHN ROE", RefusedOther, FieldContributor, "", ""},
		{"a name with a role noun in capitals", "JANE DOE, EDITOR", "Jane Doe", "JANE DOE, EDITOR", RefusedOther, FieldContributor, "", ""},
		{"a series in capitals, now verified (v4)", "BOOK 2 OF THE NIGHT FERRY SAGA", "Night Ferry Saga", "BOOK 2 OF THE NIGHT FERRY SAGA", "", FieldSeries, "", ""},
		{"part of a series name in capitals", "BOOK 2 OF THE NIGHT FERRY SAGA", "Night Ferry", "BOOK 2 OF THE NIGHT FERRY SAGA", RefusedOther, FieldSeries, "", ""},
		{"a series index as a word", "BOOK TWO OF THE NIGHT FERRY SAGA", "Two", "BOOK TWO OF THE NIGHT FERRY SAGA", RefusedOther, FieldSeriesIndex, "", ""},
		{"an ISBN printed with en dashes, under another label", "ISBN 978–0–306–40615–7 (ebook)", "978-0-306-40615-7", "ISBN 978–0–306–40615–7 (ebook)", RefusedOther, FieldISBN, evidence.LandmarkCopyright, "hardcover"},
		// v4 (ADR-046) folds case, dashes and format characters itself, so a
		// value that matches with them was refused for something else; what
		// v4 doesn't fold for a field still says so.
		{"a title in another case, with a label it can't have", "NIGHT FERRY", "Night Ferry", "NIGHT FERRY", RefusedOther, "", "", "x"},
		{"two names joined by an em dash", "THE X\nJane Doe—John Roe", "Jane Doe-John Roe", "Jane Doe—John Roe", RefusedDash, FieldContributor, "", ""},
		{"a name with a zero-width space", "THE X\nJane Do\u200be", "Jane Doe", "Jane Do\u200be", RefusedFormatChar, FieldContributor, "", ""},
		{"an ISBN printed without hyphens, under another label", "ISBN 9780306406157 (ebook)", "978-0-306-40615-7", "ISBN 9780306406157 (ebook)", RefusedOther, FieldISBN, evidence.LandmarkCopyright, "hardcover"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			landmark := tt.landmark
			if landmark == "" {
				landmark = evidence.LandmarkTitle
			}
			x := excerptOf(t, unitText{landmark, tt.text})
			field := tt.field
			if field == "" {
				field = FieldTitle
			}
			v := Verify(x, Claim{Field: field, Value: tt.value, Quote: tt.quote, Label: tt.label})
			if tt.want == "" {
				if v.Status != StatusGrounded || v.Reason != "" {
					t.Fatalf("%+v, want grounded without a reason", v)
				}
				return
			}
			if v.Status != StatusValueNotDerived || v.Reason != tt.want {
				t.Fatalf("status %q reason %q, want value_not_derived %q", v.Status, v.Reason, tt.want)
			}
		})
	}
}
