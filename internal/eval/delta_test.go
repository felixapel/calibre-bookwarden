package eval

import (
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/decide"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

// No pair of real decision versions changes a proposal's value or roots on a
// fixture we know of, so the "changed" accounting is checked on decisions
// built by hand.
func TestAddCountsChangedValuesAndRoots(t *testing.T) {
	prop := func(field string, proposed []string, roots ...string) evidence.Proposal {
		return evidence.Proposal{Field: field, Current: []string{"old"}, Proposed: proposed, Roots: roots}
	}
	newResult := func() *Result {
		r := &Result{Matrix: map[string]map[string]int{}, Fields: map[string]FieldDelta{}, Reasons: map[string]ReasonDelta{}}
		for _, a := range Tiers {
			r.Matrix[a] = map[string]int{}
		}
		return r
	}
	for _, tt := range []struct {
		name       string
		base, next []evidence.Proposal
		want       FieldDelta
		changed    bool
	}{
		{"same", []evidence.Proposal{prop("title", []string{"X"}, "dnb")}, []evidence.Proposal{prop("title", []string{"X"}, "dnb")}, FieldDelta{Baseline: 1, Version: 1}, false},
		{"value changes", []evidence.Proposal{prop("title", []string{"X"}, "dnb")}, []evidence.Proposal{prop("title", []string{"Y"}, "dnb")}, FieldDelta{Baseline: 1, Version: 1, Changed: 1}, true},
		{"roots change", []evidence.Proposal{prop("title", []string{"X"}, "dnb")}, []evidence.Proposal{prop("title", []string{"X"}, "dnb", "bne")}, FieldDelta{Baseline: 1, Version: 1, Changed: 1}, true},
		{"the current value alone is not a change", []evidence.Proposal{prop("title", []string{"X"}, "dnb")}, []evidence.Proposal{{Field: "title", Current: []string{"other"}, Proposed: []string{"X"}, Roots: []string{"dnb"}}}, FieldDelta{Baseline: 1, Version: 1}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := newResult()
			types := map[FixType]int{}
			r.add(7, decide.Decision{Tier: "A", Proposals: tt.base}, decide.Decision{Tier: "A", Proposals: tt.next}, types)
			if got := r.Fields["title"]; got != tt.want {
				t.Fatalf("title: %+v, want %+v", got, tt.want)
			}
			if listed := len(r.Books) == 1; listed != tt.changed {
				t.Fatalf("book listed = %v, want %v", listed, tt.changed)
			}
		})
	}
}
