package extract

import (
	"slices"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

func TestNormalize(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"plain", "Hello world", "Hello world"},
		{"whitespace runs", "a  b\t\tc  d", "a b c d"},
		{"newlines are spaces", "Translated by\nJane Doe\n\nNext", "Translated by Jane Doe Next"},
		{"curly apostrophes; U+02BC is a letter", "O’Brien’s ‘book’ ʼx", "O'Brien's 'book' ʼx"},
		{"curly double quotes", "“Dune” and ”x”", `"Dune" and "x"`},
		{"soft hyphen", "co\u00adoperate", "cooperate"},
		{"line break hyphenation", "inter-\nnational", "international"},
		{"hyphenation crlf", "inter-\r\nnational", "international"},
		{"hyphen kept before digit", "1-\n2", "1- 2"},
		{"hyphen kept after space", "word -\nnext", "word - next"},
		{"hyphen kept over blank line", "a-\n\nb", "a- b"},
		{"plain hyphen kept", "well-known", "well-known"},
		{"NFC composes", "Café", "Café"},
		{"NFC singleton decomposition", "x\u0f43y", "x\u0f42\u0fb7y"},
		{"NFC composes a pair", "e\u0301", "\u00e9"},
		{"superscript stays", "x² y", "x² y"},
		{"ligature stays", "ﬁnal", "ﬁnal"},
		{"digits stay", "2019 ① ٣", "2019 ① ٣"},
		{"empty", "", ""},
		{"only spaces", " \n\t ", " "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := string(Normalize(tt.in).Runes); got != tt.want {
				t.Errorf("Normalize(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestNormalizeOffsets(t *testing.T) {
	in := "Jane co\u00adop-\nerate  “Doe”\n\nÉcole"
	n := Normalize(in)
	orig := []rune(in)
	tests := []struct {
		span, want string // the normalised span, and the original text it comes from
	}{
		{"Jane", "Jane"},
		{"cooperate", "co\u00adop-\nerate"},
		{"coop", "co\u00adop"},
		{"erate", "erate"},
		{"te \"Doe\"", "te  “Doe”"},
		{"Doe\" É", "Doe”\n\nÉ"},
	}
	whole := string(n.Runes)
	for _, tt := range tests {
		i := strings.Index(whole, tt.span)
		if i < 0 {
			t.Fatalf("span %q not in %q", tt.span, whole)
		}
		s := utf8.RuneCountInString(whole[:i])
		e := s + utf8.RuneCountInString(tt.span)
		os, oe, ok := n.Span(s, e)
		if !ok {
			t.Fatalf("Span(%q) not valid", tt.span)
		}
		if got := string(orig[os:oe]); got != tt.want {
			t.Errorf("Span(%q) = %q, want %q", tt.span, got, tt.want)
		}
	}
}

func TestNormalizeSpanRejectsInsideComposition(t *testing.T) {
	// "q" + combining diaeresis has no composed form: the base and its mark
	// are one unit, so a span may not stop between them.
	n := Normalize("aq̈b")
	if _, _, ok := n.Span(1, 2); ok {
		t.Error("span ending between a base and its mark must be refused")
	}
	if _, _, ok := n.Span(2, 3); ok {
		t.Error("span starting at a lone mark must be refused")
	}
	if _, _, ok := n.Span(1, 3); !ok {
		t.Error("span over base and mark must be valid")
	}
}

func TestNormalizeLines(t *testing.T) {
	n := Normalize("one two\nthree\n\nfour")
	var got []string
	for i := 0; i <= len(n.Runes); {
		s, e := n.LineBounds(i)
		got = append(got, string(n.Runes[s:e]))
		i = e + 1
	}
	want := []string{"one two", "three", "four"}
	if !slices.Equal(got, want) {
		t.Errorf("lines = %q, want %q", got, want)
	}
}

// checkOffsets is the property FuzzNormalizeOffsets and the table share:
// every valid span maps to a slice of the original that normalises to it.
func checkOffsets(t testing.TB, in string, pairs [][2]int) {
	t.Helper()
	n := Normalize(in)
	orig := []rune(in)
	if len(n.start) != len(n.Runes) || len(n.end) != len(n.Runes) || len(n.valid) != len(n.Runes)+1 {
		t.Fatalf("table sizes disagree for %q", in)
	}
	for i := range n.Runes {
		if n.start[i] < 0 || n.end[i] > len(orig) || n.start[i] >= n.end[i] {
			t.Fatalf("rune %d maps to [%d,%d) of %d", i, n.start[i], n.end[i], len(orig))
		}
		if i > 0 && n.start[i] < n.end[i-1] && n.start[i] != n.start[i-1] {
			t.Fatalf("rune %d overlaps the one before", i)
		}
	}
	for _, p := range pairs {
		s, e := p[0], p[1]
		os, oe, ok := n.Span(s, e)
		if !ok {
			continue
		}
		if os < 0 || oe > len(orig) || os > oe {
			t.Fatalf("Span(%d,%d) = [%d,%d) of %d for %q", s, e, os, oe, len(orig), in)
		}
		// A kept soft hyphen depends on the combining mark after it
		// (normalize.go), which lies outside a span that ends on the hyphen:
		// the slice normalised on its own drops it. So for such a span the
		// slice must end on that hyphen, the mark must follow it, and the
		// rest of the slice must normalise to the rest of the span.
		want := n.Runes[s:e]
		if e > s && n.Runes[e-1] == 0xAD {
			if orig[oe-1] != 0xAD || oe >= len(orig) || !unicode.IsMark(orig[oe]) {
				t.Fatalf("Span(%d,%d) = [%d,%d) of %q doesn't end on a kept soft hyphen", s, e, os, oe, in)
			}
			want = n.Runes[s : e-1]
			oe--
		}
		if got := string(Normalize(string(orig[os:oe])).Runes); got != string(want) {
			t.Fatalf("slice %q of %q normalises to %q, want %q", string(orig[os:oe]), in, got, string(want))
		}
	}
}

func TestNormalizeOffsetsProperty(t *testing.T) {
	inputs := []string{
		"Jane co\u00adop-\nerate  “Doe”\n\nÉcole",
		"Café au lait  — x²",
		"a \u00ad b-\n\u00adc",
		"- \n -\nx\r\n-\r\ny",
		"ẹ́ q̈",
		"xy\u00ad\u0489", "\u00ad\u0489", "a\u00ad\u0301\u00ad\u0302b \u00ad\u0489",
	}
	for _, in := range inputs {
		n := Normalize(in)
		var pairs [][2]int
		for s := 0; s <= len(n.Runes); s++ {
			for e := s; e <= len(n.Runes); e++ {
				pairs = append(pairs, [2]int{s, e})
			}
		}
		checkOffsets(t, in, pairs)
	}
}

func FuzzNormalizeOffsets(f *testing.F) {
	for _, s := range []string{
		"Jane co\u00adop-\nerate  “Doe”", "Café\n\nx²", "a-\nb", "ẹ́ q̈", "", "\u00ad-\n\u00ad",
	} {
		f.Add(s, 0, 3)
	}
	f.Fuzz(func(t *testing.T, in string, a, b int) {
		in = strings.ToValidUTF8(in, "�")
		n := Normalize(in)
		m := len(n.Runes) + 1
		a, b = (a%m+m)%m, (b%m+m)%m
		if a > b {
			a, b = b, a
		}
		pairs := [][2]int{{a, b}, {0, len(n.Runes)}, {a, a}}
		if len(n.Runes) < 40 {
			for s := 0; s <= len(n.Runes); s++ {
				for e := s; e <= len(n.Runes); e++ {
					pairs = append(pairs, [2]int{s, e})
				}
			}
		}
		checkOffsets(t, in, pairs)
		// Idempotent: normalising twice changes nothing more.
		if again := string(Normalize(string(n.Runes)).Runes); again != string(n.Runes) {
			t.Fatalf("not idempotent: %q then %q", string(n.Runes), again)
		}
	})
}
