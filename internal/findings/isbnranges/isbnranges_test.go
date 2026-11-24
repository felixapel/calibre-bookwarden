package isbnranges_test

import (
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/findings/isbnranges"
)

func TestHyphenate(t *testing.T) {
	tests := []struct {
		name, isbn, want string
		ok               bool
	}{
		{"english", "9780765326355", "978-0-7653-2635-5", true},
		{"german", "9783551551672", "978-3-551-55167-2", true},
		{"spanish two-digit group", "9788478884452", "978-84-7888-445-2", true},
		{"979 France", "9791000000008", "979-10-00-00000-8", true},
		{"five-digit group", "9789990100006", "978-99901-00-00-6", true},
		{"979-8, three-digit registrant", "9798030000008", "979-8-030-00000-8", true},
		{"979-8, four-digit registrant", "9798140000004", "979-8-1400-0000-4", true},
		{"979-8, unassigned registrant range", "9798000000007", "", false},
		{"group length known but missing from RegistrationGroups", "9786490000003", "", false},
		{"Wikipedia's example", "9783161484100", "978-3-16-148410-0", true},
		{"unassigned group range", "9786700000007", "", false},
		{"unassigned 979 prefix range", "9790000000001", "", false},
		{"bad checksum", "9780765326356", "", false},
		{"too short", "978076532635", "", false},
		{"not digits", "97807653263X5", "", false},
		{"not an ISBN prefix", "9770765326355", "", false},
		{"empty", "", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := isbnranges.Hyphenate(tc.isbn)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("Hyphenate(%q) = %q, %v; want %q, %v", tc.isbn, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestHyphenate10(t *testing.T) {
	tests := []struct {
		name, isbn, want string
		ok               bool
	}{
		{"english", "9780765326355", "0-7653-2635-3", true},
		{"german", "9783551551672", "3-551-55167-7", true},
		{"check digit X", "9783161484100", "3-16-148410-X", true},
		{"five-digit group", "9789990100006", "99901-00-00-4", true},
		{"979 has no ISBN-10", "9791000000008", "", false},
		{"unassigned", "9786700000007", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := isbnranges.Hyphenate10(tc.isbn)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("Hyphenate10(%q) = %q, %v; want %q, %v", tc.isbn, got, ok, tc.want, tc.ok)
			}
		})
	}
}
