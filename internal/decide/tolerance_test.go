package decide

import (
	"strings"
	"testing"
)

func TestCanonTitleV7(t *testing.T) {
	for _, tt := range []struct{ name, in, lang, want string }{
		{"definite article dropped", "the precipice", "en", "precipice"},
		{"no article", "precipice", "en", "precipice"},
		{"indefinite article before a phrase", "a sport of nature", "en", "sport of nature"},
		{"an before a phrase", "an essay on man", "en", "essay on man"},
		{"indefinite article before one word stays (ruling)", "a man", "en", "a man"},
		{"definite article before one word folds", "the man", "en", "man"},
		{"bare the", "the", "en", "the"},
		{"bare a", "a", "en", "a"},
		{"bare an", "an", "en", "an"},
		{"article then article", "the the", "en", "the the"},
		{"article before a subtitle", "the complete survival shelters handbook : a step-by-step guide", "en", "complete survival shelters handbook: step-by-step guide"},
		{"no article before a subtitle", "complete survival shelters handbook : a step-by-step guide", "en", "complete survival shelters handbook: step-by-step guide"},
		{"subtitle with a definite article", "x: the y", "en", "x: y"},
		{"ampersand", "food & fun", "en", "food and fun"},
		{"und in German", "essen und trinken", "de", "essen and trinken"},
		{"y in Spanish", "tom y jerry", "es", "tom and jerry"},
		{"et in French", "tom et jerry", "fr", "tom and jerry"},
		{"e in Italian", "tom e jerry", "it", "tom and jerry"},
		{"y is a word in English", "tom y jerry", "en", "tom y jerry"},
		{"mojibake apostrophe", "a homeowner?s guide", "en", "homeowner's guide"},
		{"question mark between words stays", "why? because", "en", "why? because"},
		{"final question mark stays", "what?", "en", "what?"},
		{"curly apostrophe", "homeowner’s guide", "en", "homeowner's guide"},
		{"modifier apostrophe", "homeownerʼs guide", "en", "homeowner's guide"},
		{"imprint prefix", "penguin classics passage to india", "en", "passage to india"},
		{"imprint suffix", "a passage to india (penguin classics)", "en", "passage to india"},
		{"bare imprint stays", "penguin classics", "en", "penguin classics"},
		{"imprint with curly apostrophe", "oxford world’s classics emma", "en", "emma"},
		{"year in parentheses", "arrowsmith (1925)", "en", "arrowsmith"},
		{"volume in parentheses stays", "history (vol. 1)", "en", "history (vol. 1)"},
		{"long parenthetical subtitle", "the obesity code (why fasting is the key to weight loss)", "en", "obesity code"},
		{"French l'", "l'etranger", "fr", "etranger"},
		{"French le", "le petit prince", "fr", "petit prince"},
		{"French un before one word", "un homme", "fr", "un homme"},
		{"French une before a phrase", "une vie tranquille", "fr", "vie tranquille"},
		{"German der", "der process", "de", "process"},
		{"German ein before one word", "ein mann", "de", "ein mann"},
		{"German die", "die verwandlung", "de", "verwandlung"},
		{"Spanish el", "el aleph", "es", "aleph"},
		{"Spanish una before a phrase", "una historia de amor", "es", "historia de amor"},
		{"Italian il", "il nome della rosa", "it", "nome della rosa"},
		{"Italian i", "i promessi sposi", "it", "promessi sposi"},
		{"Italian l'", "l'amica geniale", "it", "amica geniale"},
		{"unknown language folds no article", "the precipice", "", "the precipice"},
		{"other language's article stays", "le chat", "en", "le chat"},
		{"english article in a German book stays", "the precipice", "de", "the precipice"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := canonTitleV7(tt.in, tt.lang); got != tt.want {
				t.Fatalf("canonTitleV7(%q, %q) = %q, want %q", tt.in, tt.lang, got, tt.want)
			}
		})
	}
}

func TestTitlesEquivalentV7(t *testing.T) {
	for _, tt := range []struct {
		name, a, b, lang string
		want             bool
	}{
		{"article dropped", "precipice", "the precipice", "en", true},
		{"article dropped before a phrase", "sport of nature", "a sport of nature", "en", true},
		{"ampersand", "build a pond for food & fun", "build a pond for food and fun", "en", true},
		{"mojibake", "a homeowner?s guide", "a homeowner's guide", "en", true},
		{"imprint prefix against suffix", "penguin classics passage to india", "a passage to india (penguin classics)", "en", true},
		{"man against a man", "man", "a man", "en", false},
		{"the road against road to serfdom", "the road", "road to serfdom", "en", false},
		{"dune against dune messiah", "dune", "dune messiah", "en", false},
		{"tom and jerry against a longer title", "tom & jerry", "tom and jerry's big adventure", "en", false},
		{"volume", "history of rome (band 1)", "history of rome", "de", false},
		{"bare article against another", "the", "a", "en", false},
		{"bare article against nothing", "the", "", "en", false},
		{"bare article against itself", "the", "the", "en", true},
		{"same words in a different order", "the red house", "house the red", "en", false},
		{"unknown language, article", "precipice", "the precipice", "", false},
		{"imprint alone", "penguin classics", "penguin classics x", "en", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := titlesEquivalentV7(tt.a, tt.b, tt.lang); got != tt.want {
				t.Fatalf("titlesEquivalentV7(%q, %q, %q) = %v, want %v", tt.a, tt.b, tt.lang, got, tt.want)
			}
			if got := titlesEquivalentV7(tt.b, tt.a, tt.lang); got != tt.want {
				t.Fatalf("not symmetric: (%q, %q)", tt.b, tt.a)
			}
		})
	}
}

func TestNameKeyV7(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"dr. jason fung", "jason fung"},
		{"dr jason fung", "jason fung"},
		{"prof. dr. x y", "x y"},
		{"sir arthur conan doyle", "arthur conan doyle"},
		{"dame edna", "edna"},
		{"rev. martin luther king", "martin luther king"},
		{"mr. rogers", "rogers"},
		{"mrs. dalloway", "dalloway"},
		{"ms. x", "x"},
		{"sir", "sir"},
		{"dr.", "dr"},
		{"e. m. forster", "e m forster"},
		{"e.m. forster", "e m forster"},
		{"e m forster", "e m forster"},
		{"j. r. r. tolkien", "j r r tolkien"},
		{"hollingdale, r. j.", "hollingdale, r j"},
		{"...", "..."},
		{"doctor who", "doctor who"},
	} {
		if got := nameKeyV7(tt.in); got != tt.want {
			t.Errorf("nameKeyV7(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestAuthorsConsistentV7(t *testing.T) {
	l := func(names ...string) []string { return names }
	for _, tt := range []struct {
		name string
		a, b []string
		want bool
	}{
		{"equal", l("a b"), l("a b"), true},
		{"initials punctuation", l("e m forster"), l("e. m. forster"), true},
		{"honorific", l("dr. jason fung"), l("jason fung"), true},
		{"one extra: Freud and Gay", l("sigmund freud", "peter gay"), l("sigmund freud"), true},
		{"Zweig and Bell", l("stefan zweig", "anthea bell"), l("stefan zweig"), true},
		{"Dahl and Blake", l("roald dahl", "quentin blake"), l("roald dahl"), true},
		{"Bellow and Coetzee", l("saul bellow", "j. m. coetzee"), l("saul bellow"), true},
		{"Schopenhauer and Hollingdale", l("arthur schopenhauer", "hollingdale, r. j."), l("arthur schopenhauer"), true},
		{"Joyce and Gabler Gabler", l("james joyce", "hans walter gabler gabler"), l("james joyce"), true},
		{"two extras", l("a b", "c d", "e f"), l("a b"), true},
		{"three extras", l("a b", "c d", "e f", "g h"), l("a b"), false},
		{"different first author, superset", l("c d", "a b"), l("a b"), false},
		{"different people", l("a b"), l("c d"), false},
		{"same size, one differs", l("a b", "c d"), l("a b", "e f"), false},
		{"same names, other order", l("a b", "c d"), l("c d", "a b"), false},
		{"superset with a honorific on the first", l("dr. a b", "c d"), l("a b"), true},
		{"empty", nil, l("a b"), false},
		{"both empty", nil, nil, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := authorsConsistentV7(tt.a, tt.b); got != tt.want {
				t.Fatalf("authorsConsistentV7(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
			if got := authorsConsistentV7(tt.b, tt.a); got != tt.want {
				t.Fatalf("not symmetric: (%v, %v)", tt.b, tt.a)
			}
		})
	}
}

var fuzzLangs = []string{"", "en", "de", "es", "fr", "it", "xx"}

func FuzzCanonTitleV7(f *testing.F) {
	for _, s := range []string{
		"the precipice", "a man", "the the x", "x (a) (b)", "the penguin classics the x", "l'l'x", "x : the : y",
		"a? b", "a?b", "& & &", "und und", "penguin classics penguin classics x", "(", ")", " (", "x ()", "", "   ", "é?é", "i i i",
	} {
		for i := range fuzzLangs {
			f.Add(s, uint8(i))
		}
	}
	f.Fuzz(func(t *testing.T, s string, li uint8) {
		lang := fuzzLangs[int(li)%len(fuzzLangs)]
		once := canonTitleV7(s, lang)
		if twice := canonTitleV7(once, lang); twice != once {
			t.Fatalf("not idempotent for %q (%s): %q then %q", s, lang, once, twice)
		}
		if strings.TrimSpace(s) != "" && once == "" {
			t.Fatalf("%q folded to nothing", s)
		}
	})
}

func FuzzNameKeyV7(f *testing.F) {
	for _, s := range []string{"dr. jason fung", "e. m. forster", "dr. dr.", "sir", "...", "", "dr dr x", "mr. . x", "ms.ms. x"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		once := nameKeyV7(s)
		if twice := nameKeyV7(once); twice != once {
			t.Fatalf("not idempotent for %q: %q then %q", s, once, twice)
		}
		if strings.TrimSpace(s) != "" && once == "" {
			t.Fatalf("%q folded to nothing", s)
		}
	})
}
