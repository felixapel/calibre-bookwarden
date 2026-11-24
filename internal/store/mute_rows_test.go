package store

import (
	"context"
	"slices"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/report"
)

func TestFixTypeMatchesReport(t *testing.T) {
	for _, p := range []evidence.Proposal{
		{Field: "title", Current: []string{"Old"}, Roots: []string{"file", "dnb"}},
		{Field: "publisher", Roots: []string{"text"}},
		{Field: "isbn", Current: []string{}},
	} {
		if got, want := FixTypeOf(p), FixType(report.TypeOf(p)); got != want {
			t.Errorf("FixTypeOf(%+v) = %+v, report says %+v", p, got, want)
		}
	}
}

// TestFixListMutedTypes: the muted filter and the default view split the
// projected fixes by type, in SQL, before they page; the hidden count says
// how many the default view left out.
func TestFixListMutedTypes(t *testing.T) {
	ctx := context.Background()
	e := newCarryEnv(t)
	fill := evidence.Proposal{Field: "publisher", Current: []string{}, Proposed: []string{"P"}, Roots: []string{"text"}}
	unsortedRoots := evidence.Proposal{Field: "title", Current: []string{"Old"}, Proposed: []string{"New"}, Roots: []string{"file", "dnb"}}
	sortedOrder := evidence.Proposal{Field: "title", Current: []string{"Old"}, Proposed: []string{"New"}, Roots: []string{"dnb", "file"}}
	run := listRun(t, e, []listBook{
		{10, "A", nil, "A", []evidence.Proposal{fill}},
		{20, "B", nil, "A", []evidence.Proposal{unsortedRoots}},
		{30, "C", nil, "A", []evidence.Proposal{sortedOrder, fill}},
	})
	svc := report.New(NewReportRepo(e.s))
	muted := []report.FixType{{Field: "publisher", Path: "text", Kind: "fill"}, {Field: "title", Path: "dnb+file", Kind: "replace"}}
	tests := []struct {
		name       string
		f          report.FixFilter
		want       []int
		wantHidden int
	}{
		{"no muted types", report.FixFilter{}, []int{10, 20, 30, 30}, 0},
		{"the muted filter without types keeps nothing", report.FixFilter{Muted: true}, nil, 0},
		{"default view", report.FixFilter{MutedTypes: muted}, nil, 4},
		{"muted view", report.FixFilter{Muted: true, MutedTypes: muted}, []int{10, 20, 30, 30}, 0},
		{"roots are sorted, whatever their stored order", report.FixFilter{MutedTypes: muted[1:]}, []int{10, 30}, 2},
		{"another filter narrows the count", report.FixFilter{Field: "publisher", MutedTypes: muted}, nil, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := svc.Fixes(ctx, run, tt.f, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got := ids(p); !slices.Equal(got, tt.want) || p.MutedHidden != tt.wantHidden {
				t.Fatalf("books %v, hidden %d; want %v, %d", got, p.MutedHidden, tt.want, tt.wantHidden)
			}
		})
	}
}
