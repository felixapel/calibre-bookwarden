package extract

import (
	"slices"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

// brief renders a parser claim as "field=value[/role-or-label]" for tables.
func brief(c Claim) string {
	s := c.Field + "=" + c.Value
	switch {
	case c.Role != "":
		s += "/" + c.Role
	case c.Label != "":
		s += "/" + c.Label
	}
	return s
}

func parsed(x Excerpt) []string {
	var out []string
	for _, c := range Parse(x) {
		out = append(out, brief(c))
	}
	return out
}

func TestParse(t *testing.T) {
	tests := []struct {
		name  string
		units []unitText
		want  []string // every one must be found
		not   []string // none may be found
	}{
		{
			name: "typical Penguin-style copyright page",
			units: []unitText{{evidence.LandmarkCopyright, "Harbor House Books\nAn imprint of Example Publishing Group\n" +
				"First published in Great Britain by Cape Lantern Ltd 2003\nThis edition published 2021\nCopyright © Jane Doe, 2003\n" +
				"Translated from the French by John Roe\nISBN 978-1-4028-9462-6 (paperback)\nISBN 978-0-306-40615-7 (ebook)\n" +
				"Book 2 of the Lantern Chronicles\nSecond Edition"}},
			want: []string{
				"year=2003/first_published", "year=2021/this_edition", "year=2003/copyright",
				"contributor=John Roe/translator",
				"isbn=9781402894626/paperback", "isbn=9780306406157/ebook",
				"series=Lantern Chronicles", "series_index=2",
			},
			not: []string{"edition=Second Edition", "contributor=Jane Doe/author"},
		},
		{
			name: "German Impressum",
			units: []unitText{{evidence.LandmarkCopyright, "Impressum\nErstausgabe 2018\n© 2018 Beispiel Verlag GmbH\n" +
				"Aus dem Englischen von Anna Beispiel\nISBN 978-3-16-148410-0 (Taschenbuch)\nDie Glasgarten-Saga, Band 5"}},
			want: []string{
				"year=2018/first_published", "year=2018/copyright", "contributor=Anna Beispiel/translator",
				"isbn=9783161484100/paperback", "series=Die Glasgarten-Saga", "series_index=5",
			},
		},
		{
			name: "Spanish página legal",
			units: []unitText{{evidence.LandmarkCopyright, "Primera edición: marzo de 2017\n© Ana Ruiz, 2017\nTraducción de Luis Ejemplo\n" +
				"ISBN: 978-84-376-0494-7 (tapa blanda)"}},
			want: []string{"year=2017/first_published", "year=2017/copyright", "contributor=Luis Ejemplo/translator", "isbn=9788437604947/paperback"},
		},
		{
			name: "title page with by-line, translators and introduction",
			units: []unitText{{evidence.LandmarkTitle, "THE LANTERN YEARS\nby Jane Doe\nTranslated by Nina Wende and Pablo Dos\n" +
				"With an introduction by Mary Sample"}},
			want: []string{"contributor=Nina Wende/translator", "contributor=Pablo Dos/translator"},
			not:  []string{"contributor=Jane Doe/author", "contributor=Mary Sample/introduction"},
		},
		{
			name:  "a suffix after the comma",
			units: []unitText{{evidence.LandmarkCopyright, "Translated by J. R. Doe, Jr."}},
			want:  []string{"contributor=J. R. Doe, Jr./translator"},
			not:   []string{"contributor=J. R. Doe/translator"},
		},
		{
			name:  "curly apostrophe in a name",
			units: []unitText{{evidence.LandmarkCopyright, "Translated by Kate O’Hara"}},
			want:  []string{"contributor=Kate O'Hara/translator"},
		},
		{
			name:  "multiple ISBNs on one line, ebook and print",
			units: []unitText{{evidence.LandmarkCopyright, "ISBN 978-1-4028-9462-6 (print) ISBN 978-0-306-40615-7 (ebook)"}},
			want:  []string{"isbn=9781402894626/print", "isbn=9780306406157/ebook"},
		},
		{
			name:  "ISBN-10 and ISBN-13 forms",
			units: []unitText{{evidence.LandmarkCopyright, "ISBN-10: 0-19-852663-6\nISBN-13: 978-0-306-40615-7"}},
			want:  []string{"isbn=9780198526636", "isbn=9780306406157"},
		},
		{
			name:  "invalid checksum and a bare ten digits give nothing",
			units: []unitText{{evidence.LandmarkCopyright, "ISBN 978-1-4028-9462-7\nCatalogue 0198526636\nOrder 1234567890123"}},
			not:   []string{"isbn=9781402894627", "isbn=9780198526636"},
		},
		{
			name:  "series statements",
			units: []unitText{{evidence.LandmarkTitle, "Volume 3 in the Glass Orchard series\nCinder Road #2.5\nBook Three\n#1 New York Times Bestseller\nBand drei"}},
			want:  []string{"series=Glass Orchard", "series_index=3", "series=Cinder Road", "series_index=2.5"},
			not:   []string{"series_index=1"},
		},
		{
			name:  "years need labels",
			units: []unitText{{evidence.LandmarkCopyright, "Printed in 1999\nReprinted 2005\nPublished 2015"}},
			want:  []string{"year=2015/this_edition"},
			not:   []string{"year=1999", "year=2005"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			x := excerptOf(t, tt.units...)
			got := parsed(x)
			for _, w := range tt.want {
				if !slices.Contains(got, w) {
					t.Errorf("missing %q in %q", w, got)
				}
			}
			for _, n := range tt.not {
				if slices.Contains(got, n) {
					t.Errorf("unexpected %q in %q", n, got)
				}
			}
			for _, c := range Parse(x) {
				if c.Source != SourceParser {
					t.Errorf("%s: source = %q", brief(c), c.Source)
				}
				// Parser claims go through the verifier like any other.
				if v := Verify(x, c); v.Status != StatusGrounded {
					t.Errorf("%s (quote %q): status = %q", brief(c), c.Quote, v.Status)
				}
			}
		})
	}
}

func TestParseClaimsInExcludedSectionsAreDropped(t *testing.T) {
	x := excerptOf(t,
		unitText{evidence.LandmarkCopyright, "Copyright © Jane Doe, 2003\nISBN 978-1-4028-9462-6 (paperback)\nAlso by Jane Doe\nThe Glass Orchard ISBN 978-0-14-118776-1\nBook 1 of the Orchard Cycle\n© 1999 Old Press"},
		unitText{evidence.LandmarkFront, "Praise for The Lantern Years\nTranslated by Pat Reviewer\n© 2004 The Daily Example"},
		unitText{evidence.LandmarkBack, "Night Press\n© 2020 Night Press\nISBN 978-0-7432-7356-5 (hardcover)"},
	)
	want := map[string]string{
		"year=2003/copyright":                 StatusGrounded,
		"isbn=9781402894626/paperback":        StatusGrounded,
		"isbn=9780141187761":                  StatusExcludedSection,
		"series=Orchard Cycle":                StatusExcludedSection,
		"year=1999/copyright":                 StatusExcludedSection,
		"contributor=Pat Reviewer/translator": StatusExcludedSection,
		"year=2004/copyright":                 StatusExcludedSection,
		"year=2020/copyright":                 StatusExcludedSection, // back unit: every field but ISBN
		"isbn=9780743273565":                  StatusGrounded,        // back unit: no qualifier
	}
	seen := map[string]bool{}
	for _, c := range Parse(x) {
		v := Verify(x, c)
		k := brief(c)
		seen[k] = true
		if w, ok := want[k]; ok && v.Status != w {
			t.Errorf("%s: status = %q, want %q", k, v.Status, w)
		}
	}
	for k := range want {
		if !seen[k] {
			t.Errorf("parser did not claim %q: %v", k, parsed(x))
		}
	}
}

func TestParseTextLayerJoinsHyphenation(t *testing.T) {
	if got := parsed(textLayer(t, evidence.LandmarkCopyright, "Trans-\nlated by Jane Doe")); !slices.Contains(got, "contributor=Jane Doe/translator") {
		t.Errorf("text layer: %q", got)
	}
	// In EPUB text a line break is a block boundary: nothing is joined.
	if got := parsed(single(t, evidence.LandmarkCopyright, "Trans-\nlated by Jane Doe")); len(got) != 0 {
		t.Errorf("EPUB text: %q", got)
	}
}

func TestParseDeduplicates(t *testing.T) {
	x := single(t, evidence.LandmarkCopyright, "ISBN 978-1-4028-9462-6\nISBN 978-1-4028-9462-6")
	if got := parsed(x); len(got) != 1 {
		t.Errorf("claims = %q, want one", got)
	}
}

func TestParseEmpty(t *testing.T) {
	if got := Parse(Excerpt{}); len(got) != 0 {
		t.Errorf("empty excerpt gave %v", got)
	}
	if got := Parse(Excerpt{Text: strings.Repeat("x ", 50)}); len(got) != 0 {
		t.Errorf("plain text gave %v", got)
	}
}

func FuzzParse(f *testing.F) {
	for _, s := range []string{
		"First published 2003\nTranslated by Jane Doe and John Roe\nISBN 978-1-4028-9462-6 (ebook)\nBook 2 of the Lantern Chronicles",
		"© 2018\nAus dem Englischen von Anna Beispiel\nBand 5\n#3",
		"Trans-\nlated by J. R. Doe, Jr.\nAlso by X\n978-0-306-40615-7",
		"", "-\n-\n", "© © © 2019 2020 2021", "Translated by", "by by by by",
		"© 2003, 2019 Jane Doe\nISBN 978-0-306-40615-7 Print ISBN 978-1-4028-9462-6",
	} {
		f.Add(s, false)
		f.Add(s, true)
	}
	f.Fuzz(func(t *testing.T, text string, layer bool) {
		text = strings.ToValidUTF8(text, "�")
		x := Excerpt{Text: text}
		if layer {
			x.Method = evidence.ExcerptTextLayer
		}
		claims := Parse(x)
		runes := []rune(text)
		for _, c := range claims {
			if c.Source != SourceParser || c.Quote == "" || c.Field == "" {
				t.Fatalf("bad claim %+v", c)
			}
			v := Verify(x, c)
			// A parser's quote comes from the text, so it is found; whether it
			// is ambiguous or excluded depends on the text. Otherwise the
			// verifier derives exactly what the parser claimed.
			switch v.Status {
			case StatusAmbiguous, StatusExcludedSection:
				continue
			case StatusGrounded:
			default:
				t.Fatalf("parser claim %+v: %s in %q", c, v.Status, text)
			}
			if v.Value != c.Value || v.Role != c.Role || v.Label != c.Label {
				t.Fatalf("parser claim %+v grounded as %q/%q/%q", c, v.Value, v.Role, v.Label)
			}
			if v.Start < 0 || v.End > len(runes) || v.Start >= v.End {
				t.Fatalf("offsets %d-%d of %d", v.Start, v.End, len(runes))
			}
		}
	})
}
