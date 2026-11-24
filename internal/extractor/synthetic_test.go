package extractor

import (
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/extract"
)

// Every truth fact grounds through the verifier as itself, with the role or
// label the text proves, so the benchmark scores the models and not the
// verifier.
func TestSyntheticTruthIsGroundable(t *testing.T) {
	cases := Synthetic(1, 60)
	if len(cases) != 60 {
		t.Fatalf("%d cases", len(cases))
	}
	for _, c := range cases {
		x := c.Excerpt.Prepared()
		for _, f := range c.Truth {
			v := extract.Verify(x, extract.Claim{Field: f.Field, Value: f.Value, Quote: f.Quote})
			if v.Status != extract.StatusGrounded || v.Value != f.Value || v.Role+v.Label != f.RoleOrLabel {
				t.Errorf("%s: %s %q (quote %q): %s, value %q, role/label %q, want %q",
					c.Name, f.Field, f.Value, f.Quote, v.Status, v.Value, v.Role+v.Label, f.RoleOrLabel)
			}
		}
	}
}

func TestSyntheticIsDeterministic(t *testing.T) {
	a, b := Synthetic(7, 12), Synthetic(7, 12)
	for i := range a {
		if a[i].Excerpt.Text != b[i].Excerpt.Text || len(a[i].Truth) != len(b[i].Truth) {
			t.Fatalf("case %d differs", i)
		}
	}
	if Synthetic(8, 12)[0].Excerpt.Text == a[0].Excerpt.Text {
		t.Error("another seed gives the same text")
	}
}

// A trap is never also the truth: a model that finds the book's own title
// must not be scored as falling for an ad.
func TestSyntheticTrapsAreNotTruth(t *testing.T) {
	for seed := uint64(0); seed < 300; seed++ {
		for _, c := range Synthetic(seed, 30) {
			for _, f := range c.Truth {
				for _, trap := range c.Traps {
					if trap == f.Value || trap == f.Value+"/"+f.RoleOrLabel {
						t.Fatalf("seed %d, %s: truth %s %q is also a trap", seed, c.Name, f.Field, f.Value)
					}
				}
			}
		}
	}
}
