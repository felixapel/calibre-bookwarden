package decide

import "testing"

func TestSameValue(t *testing.T) {
	tests := []struct {
		name     string
		field    string
		typed    string
		proposed []string
		lang     string
		want     bool
	}{
		{"title case and article", "title", "the Hobbit", []string{"Hobbit"}, "eng", true},
		{"title punctuation", "title", "Dune: Messiah", []string{"Dune Messiah"}, "eng", true},
		{"title ampersand", "title", "Salt & Pepper", []string{"Salt and Pepper"}, "", true},
		{"title case only", "title", "binary tides ii", []string{"Binary Tides II"}, "eng", true},
		{"authors case only", "authors", "ada reed", []string{"Ada Reed"}, "", true},
		{"publisher case only", "publisher", "ACME", []string{"Acme Books"}, "", true},
		{"title different", "title", "Dune", []string{"Emma"}, "eng", false},
		{"title empty typed", "title", " ", []string{"Emma"}, "eng", false},
		{"authors initials", "authors", "E. M. Forster", []string{"E.M. Forster"}, "", true},
		{"authors split on ampersand", "authors", "Ada Reed & Cy Hale", []string{"Ada Reed", "Cy Hale"}, "", true},
		{"authors one extra name", "authors", "Ada Reed", []string{"Ada Reed", "Cy Hale"}, "", true},
		{"authors other person", "authors", "Ada Reed", []string{"Bo Lane"}, "", false},
		{"publisher suffix", "publisher", "Acme", []string{"Acme Books"}, "", true},
		{"publisher ampersand", "publisher", "Smith & Co.", []string{"Smith and Company"}, "", true},
		{"publisher different", "publisher", "Acme", []string{"Zenith"}, "", false},
		{"pubdate same year", "pubdate", "2001", []string{"2001-07-01T00:00:00+00:00"}, "", true},
		{"pubdate other year", "pubdate", "2002", []string{"2001-07-01"}, "", false},
		{"isbn hyphens", "isbn", "978-0-306-40615-7", []string{"9780306406157"}, "", true},
		{"isbn 10 form", "isbn", "0306406152", []string{"9780306406157"}, "", true},
		{"isbn invalid", "isbn", "9780306406158", []string{"9780306406158"}, "", false},
		{"languages", "languages", "en", []string{"eng"}, "", true},
		{"languages other", "languages", "de", []string{"eng"}, "", false},
		{"unknown field", "series", "x", []string{"x"}, "", false},
		{"no proposal", "title", "x", nil, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SameValue(tt.field, tt.typed, tt.proposed, tt.lang); got != tt.want {
				t.Errorf("SameValue(%s, %q, %q) = %v, want %v", tt.field, tt.typed, tt.proposed, got, tt.want)
			}
		})
	}
}
