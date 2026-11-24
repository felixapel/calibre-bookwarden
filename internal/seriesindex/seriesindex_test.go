package seriesindex_test

import (
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/seriesindex"
)

func TestNormalize(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{"", "", true},
		{"  ", "", true},
		{"1", "1", true},
		{"1.0", "1", true},
		{"2.50", "2.5", true},
		{"2.5", "2.5", true},
		{" 3.25 ", "3.25", true},
		{"007", "7", true},
		{"0", "0", true},
		{"0.0", "0", true},
		{".5", "0.5", true},
		{"5.", "5", true},
		{"100000", "100000", true},
		{"123456.123456", "123456.123456", true},
		{".", "", false},
		{"1,5", "", false},
		{"-1", "", false},
		{"+1", "", false},
		{"1e3", "1000", true},
		{"1e-05", "0.00001", true},
		{"1e-07", "", false},
		{"2e0", "2", true},
		{"1e400", "", false},
		{"NaN", "", false},
		{"Infinity", "", false},
		{"0x1p-2", "", false},
		{"-1e3", "", false},
		{"abc", "", false},
		{"1/2", "", false},
		{"I", "", false},
		{"1.2.3", "", false},
		{"1000000", "", false},
		{"1.1234567", "", false},
		{"１", "", false},
	}
	for _, tt := range tests {
		got, ok := seriesindex.Normalize(tt.in)
		if got != tt.want || ok != tt.ok {
			t.Errorf("Normalize(%q) = %q, %v; want %q, %v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

func TestCompare(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"1", "1", 0}, {"1", "2", -1}, {"2", "1", 1}, {"9", "10", -1}, {"10", "9", 1},
		{"2.5", "2.25", 1}, {"2.25", "2.5", -1}, {"2", "2.5", -1}, {"0.5", "1", -1}, {"3.5", "3.5", 0},
	}
	for _, tt := range tests {
		if got := seriesindex.Compare(tt.a, tt.b); got != tt.want {
			t.Errorf("Compare(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}
