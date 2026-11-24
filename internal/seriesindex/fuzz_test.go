package seriesindex_test

import (
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/seriesindex"
)

// A normalized index is its own normal form.
func FuzzSeriesIndexNormalize(f *testing.F) {
	for _, s := range []string{"1", "1.0", "2.50", "007", ".5", "0", " 3 ", "1e2", "-1", "1,5", ""} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		n, ok := seriesindex.Normalize(s)
		if !ok {
			return
		}
		if again, ok := seriesindex.Normalize(n); !ok || again != n {
			t.Fatalf("Normalize(%q) = %q, but Normalize(%q) = %q, %v", s, n, n, again, ok)
		}
	})
}
