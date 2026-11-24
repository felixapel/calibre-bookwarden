package decide

import "testing"

func TestTitlesEquivalentParentheticalsV7(t *testing.T) {
	for _, tt := range []struct {
		a, b string
		want bool
	}{
		{"diaries (1914-1918)", "diaries (1939-1945)", false},
		{"diaries (one)", "diaries (two)", false},
		{"diaries (first of two)", "diaries (second of two)", false},
		{"diaries (the early years)", "diaries (the later years)", false},
		{"history (abridged)", "history (unabridged)", false},
		{"history (2nd edition)", "history (3rd edition)", false},
		{"history (abridged)", "history (abridged)", true},
		{"history (abridged)", "history", true},
		{"history", "history (illustrated edition)", true},
		{"history (abridged) (q)", "history", true},
	} {
		if got := titlesEquivalentV7(tt.a, tt.b, "en"); got != tt.want {
			t.Errorf("titlesEquivalentV7(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
		if got := titlesEquivalentV7(tt.b, tt.a, "en"); got != tt.want {
			t.Errorf("not symmetric: (%q, %q)", tt.b, tt.a)
		}
	}
}
