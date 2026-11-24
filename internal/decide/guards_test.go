package decide

import "testing"

func TestShortens(t *testing.T) {
	for _, tt := range []struct {
		calibre, proposed string
		want              bool
	}{
		{"Battle Cry of Freedom: The Civil War Era", "Battle Cry of Freedom", true},
		{"The Art of Loving", "Art of Loving", true},
		{"Café: Roman", "café", true},
		{"Recognitions", "The Recognitions", false},
		{"Socialism the Active Utopia Routledge Revivals", "Socialism the Active Utopia (Routledge Revivals)", false},
		{"Power and Market", "Power & Market", false},
		{"Dispossessed, The 50th Anniversary Edition", "The Dispossessed", false},
		{"Art of Loving", "Art of Loving", false},
		{"Foo", "—", false},
		{"किताब घर", "कीताब", false}, // combining vowel signs belong to their word
		{"किताब घर", "किताब", true},
	} {
		if got := shortens(tt.calibre, tt.proposed); got != tt.want {
			t.Errorf("shortens(%q, %q) = %v, want %v", tt.calibre, tt.proposed, got, tt.want)
		}
	}
}

func TestReformsOnly(t *testing.T) {
	for _, tt := range []struct {
		calibre, proposed []string
		want              bool
	}{
		{[]string{"Will Durant", "Ariel Durant"}, []string{"Will Durant"}, true},
		{[]string{"Walter Scott"}, []string{"Sir Walter Scott"}, true},
		{[]string{"Ursula K. le Guin", "Charlie Jane Anders"}, []string{"Ursula K. Le Guin"}, true},
		{[]string{"Walter Lippmann", "Hans Eysenck"}, []string{"Hans Eysenck"}, true},
		{[]string{"Ludwig von Mises"}, []string{"Bettina Bien Greaves"}, false},
		{[]string{"J. K. Rowling"}, []string{"J. K. Rowling", "Klaus Fritz"}, false},
		{[]string{"Rowling, J. K."}, []string{"J. K. Rowling"}, false},
		{nil, []string{"J. K. Rowling"}, false},
		{[]string{"J. K. Rowling"}, nil, false},
		{[]string{"Will Durant"}, []string{"Will Durant", "Ariel Durant"}, false}, // a co-author with the same surname
		{[]string{"Ariel Durant", "Will Durant"}, []string{"Will Durant", "Ariel Durant"}, true},
	} {
		if got := reformsOnly(tt.calibre, tt.proposed); got != tt.want {
			t.Errorf("reformsOnly(%q, %q) = %v, want %v", tt.calibre, tt.proposed, got, tt.want)
		}
	}
}
