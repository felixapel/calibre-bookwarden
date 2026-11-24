package findings

import "testing"

// A valid date key is its own key.
func FuzzDateKey(f *testing.F) {
	for _, s := range []string{"2005-03-24", " 2005-03-24 ", "2005-03-24T23:00:00+00:00", "2005-02-30", "2005", ""} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		k := DateKey(s)
		if !k.Valid {
			return
		}
		if again := DateKey(k.Value); again != k {
			t.Fatalf("DateKey(%q) = %+v, but DateKey(%q) = %+v", s, k, k.Value, again)
		}
	})
}

// A canonical ISBN is thirteen digits and canonical itself.
func FuzzCanonicalISBN(f *testing.F) {
	for _, s := range []string{"9783551551672", "978-3-551-55167-2", "3551551677", "0-306-40615-2", "080442957X", "isbn 9780306406157", ""} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		c, ok := CanonicalISBN(s)
		if !ok {
			return
		}
		if len(c) != 13 || c[0] != '9' {
			t.Fatalf("CanonicalISBN(%q) = %q, not an ISBN-13", s, c)
		}
		if again, ok := CanonicalISBN(c); !ok || again != c {
			t.Fatalf("CanonicalISBN(%q) = %q, but CanonicalISBN(%q) = %q, %v", s, c, c, again, ok)
		}
	})
}
