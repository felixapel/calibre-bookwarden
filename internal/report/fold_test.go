package report

import "testing"

func TestFold(t *testing.T) {
	for in, want := range map[string]string{"Café Müller": "cafe muller", "JOSÉ": "jose", "Łódź Søren Đ": "lodz soren d", "Straße ẞ Æon Cœur": "strasse ss aeon coeur", "plain": "plain", "": ""} {
		if got := Fold(in); got != want {
			t.Errorf("Fold(%q) = %q, want %q", in, got, want)
		}
	}
}
