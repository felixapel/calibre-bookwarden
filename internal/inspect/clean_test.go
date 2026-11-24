package inspect_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/inspect"
)

func TestClean(t *testing.T) {
	var controls strings.Builder
	for r := rune(1); r <= 0x9F; r++ {
		if r < 0x20 && r != '\t' && r != '\n' && r != '\v' && r != '\f' && r != '\r' || r >= 0x7F && r != 0x85 {
			controls.WriteRune(r)
		}
	}
	tests := []struct{ in, want string }{
		{"a\x00b", "ab"},
		{"\xffx", "�x"},
		{" a \n\t b ", "a b"},
		{"x" + controls.String() + "y", "xy"},
		{"René", "René"},
		{"", ""},
		{" \t\n", ""},
	}
	for _, tt := range tests {
		if got := inspect.Clean(tt.in); got != tt.want {
			t.Errorf("Clean(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
	long := inspect.Clean(strings.Repeat("é", 600))
	if n := utf8.RuneCountInString(long); n != 512 || !utf8.ValidString(long) {
		t.Errorf("Clean(600 runes) has %d runes, valid %v; want 512, true", n, utf8.ValidString(long))
	}
}

func TestCleanList(t *testing.T) {
	got := inspect.CleanList([]string{" Ada  Example ", "", "\x00", "Bea", "Ada Example"})
	if want := []string{"Ada Example", "Bea"}; !reflect.DeepEqual(got, want) {
		t.Errorf("CleanList = %q, want %q", got, want)
	}
	many := make([]string, 40)
	for i := range many {
		many[i] = fmt.Sprint("author ", i)
	}
	if got := inspect.CleanList(many); len(got) != 32 || got[31] != "author 31" {
		t.Errorf("CleanList(40) kept %d, last %q; want 32, author 31", len(got), got[len(got)-1])
	}
	if got := inspect.CleanList(nil); got != nil {
		t.Errorf("CleanList(nil) = %q, want nil", got)
	}
}

func TestISBNCandidates(t *testing.T) {
	twenty := make([]string, 20)
	var want16 []string
	for i := range twenty {
		twenty[i] = fmt.Sprintf("97800000001%02d", i)
		if i < 16 {
			want16 = append(want16, twenty[i])
		}
	}
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{"urn", []string{"urn:isbn:978-0-00-000002-6"}, []string{"9780000000026"}},
		{"ISBN prefix", []string{"ISBN 0-306-40615-2"}, []string{"0306406152"}},
		{"isbn colon", []string{"isbn:9780000000026"}, []string{"9780000000026"}},
		{"lower-case x", []string{"080442957x"}, []string{"080442957X"}},
		{"invalid checksum kept", []string{"9780000000020"}, []string{"9780000000020"}},
		{"spaces", []string{" 978 0 00 000002 6 "}, []string{"9780000000026"}},
		{"uuid", []string{"urn:uuid:0c57e75c-16aa-4d88-ae0e-916c8f0e8b8e"}, nil},
		{"too short", []string{"12345"}, nil},
		{"X in the middle", []string{"08044X9575"}, nil},
		{"words after the prefix", []string{"ISBN 0-306-40615-2 (hardcover, paperback)"}, nil},
		{"duplicates", []string{"9780000000026", "978-0-00-000002-6"}, []string{"9780000000026"}},
		{"capped", twenty, want16},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := inspect.ISBNCandidates(tt.in...); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ISBNCandidates(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestNewMeta(t *testing.T) {
	if got := inspect.NewMeta(evidence.FileMetadata{Title: " \x00 ", ISBNs: []string{"urn:uuid:x"}}); got != nil {
		t.Errorf("NewMeta(nothing usable) = %+v, want nil", got)
	}
	got := inspect.NewMeta(evidence.FileMetadata{
		Title: "  Binary\tTides ", Authors: []string{"Cy", "Cy", ""}, Languages: []string{"en"},
		ISBNs: []string{"uuid", "urn:isbn:9780000000026"}, Publisher: "P\x00ress", Date: " 2019-05-04 ",
	})
	want := &evidence.FileMetadata{
		Limits: &evidence.MetadataLimits{Version: 1},
		Title:  "Binary Tides", Authors: []string{"Cy"}, Languages: []string{"en"},
		ISBNs: []string{"9780000000026"}, Publisher: "Press", Date: "2019-05-04",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("NewMeta = %+v, want %+v", got, want)
	}
}
