package findings_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/findings"
)

// isbn13 completes a 12-digit 978 stem with its check digit.
func isbn13(stem string) string {
	sum := 0
	for i, c := range stem {
		w := 1
		if i%2 == 1 {
			w = 3
		}
		sum += int(c-'0') * w
	}
	return stem + fmt.Sprint((10-sum%10)%10)
}

func TestExtractISBNs(t *testing.T) {
	var many []string
	for i := range 20 {
		many = append(many, isbn13(fmt.Sprintf("978100000%03d", i)))
	}
	tests := []struct {
		name, in string
		want     []string
	}{
		{"labelled", "ISBN 978-0-00-000004-0", []string{"9780000000040"}},
		{"spaces", "978 0 00 000004 0", []string{"9780000000040"}},
		{"en dash", "978–0–00–000004–0", []string{"9780000000040"}},
		{"isbn-10 labelled", "ISBN-10: 0-00-000004-3", []string{"9780000000040"}},
		{"isbn-10 lower label", "isbn 0000000043", []string{"9780000000040"}},
		{"isbn-10 unlabelled", "0000000043", nil},
		{"isbn-10 label too far", "ISBN" + strings.Repeat(".", 31) + "0000000043", nil},
		{"inside a longer number", "19780000000040", nil},
		{"bad checksum", "978-0-00-000004-1", nil},
		{"double separator", "978--0-00-000004-0", nil},
		{"two spaces", "978  0 00 000004 0", nil},
		{"a space before a hyphen", "ISBN 978 -0-00-000004 -0", []string{"9780000000040"}},
		{"a space after a hyphen", "ISBN 978- 0- 00-000004-0", []string{"9780000000040"}},
		{"a spaced en dash", "978 – 0 – 00 – 000004 – 0", []string{"9780000000040"}},
		{"isbn-10 X after a hyphen", "ISBN: 0-00-000006-X", []string{"9780000000064"}},
		{"isbn-10 X after a hyphen, lower case", "ISBN 0-00-000006-x.", []string{"9780000000064"}},
		{"isbn-10 X after a hyphen, unlabelled", "0-00-000006-X", nil},
		{"isbn-10 X starting a word", "ISBN 0-00-000006-Xa", nil},
		{"duplicates", "9780000000040 and ISBN 0000000043", []string{"9780000000040"}},
		{"order kept", "ISBN 978-0-00-000006-4, 978-0-00-000004-0", []string{"9780000000064", "9780000000040"}},
		{"capped", strings.Join(many, " "), many[:16]},
		{"empty", "", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := findings.ExtractISBNs(tc.in); !slices.Equal(got, tc.want) {
				t.Fatalf("ExtractISBNs(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func FuzzExtractISBNs(f *testing.F) {
	f.Add("ISBN 978-0-00-000004-0")
	f.Add("ISBN-10: 0-00-000004-3 978 0 00 000006 4")
	f.Add("ISBN 0-00-000006-X, 978 -0- 00 – 000004-0")
	f.Fuzz(func(t *testing.T, s string) {
		got := findings.ExtractISBNs(s)
		if len(got) > 16 {
			t.Fatalf("%d ISBNs", len(got))
		}
		for _, isbn := range got {
			if c, ok := findings.CanonicalISBN(isbn); !ok || c != isbn {
				t.Fatalf("%q is not canonical", isbn)
			}
		}
	})
}
