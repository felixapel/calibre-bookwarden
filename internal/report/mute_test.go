package report

import (
	"context"
	"fmt"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

func TestTypeOf(t *testing.T) {
	for _, tt := range []struct {
		name string
		p    evidence.Proposal
		want FixType
	}{
		{"replace sorts the roots", evidence.Proposal{Field: "title", Current: []string{"Old"}, Roots: []string{"file", "dnb"}}, FixType{"title", "dnb+file", "replace"}},
		{"empty current is a fill", evidence.Proposal{Field: "publisher", Current: []string{}, Roots: []string{"text"}}, FixType{"publisher", "text", "fill"}},
		{"nil current is a fill", evidence.Proposal{Field: "isbn"}, FixType{"isbn", "", "fill"}},
	} {
		if got := TypeOf(tt.p); got != tt.want {
			t.Errorf("%s: %+v, want %+v", tt.name, got, tt.want)
		}
	}
}

// TestFixesMutedWalked: a run without a check is walked, and the muted
// filter and the default view apply to what the walk finds.
func TestFixesMutedWalked(t *testing.T) {
	ctx := context.Background()
	s := New(&fakeStore{books: []Book{fixBook(1, "B", "title", "languages"), fixBook(2, "A", "isbn", "title")}})
	muted := []FixType{{Field: "title", Path: "dnb+file", Kind: "fill"}}
	for _, tt := range []struct {
		name       string
		f          FixFilter
		want       []fixKey
		wantHidden int
	}{
		{"nothing muted", FixFilter{}, []fixKey{{1, "title"}, {1, "languages"}, {2, "isbn"}, {2, "title"}}, 0},
		{"default view", FixFilter{MutedTypes: muted}, []fixKey{{1, "languages"}, {2, "isbn"}}, 2},
		{"muted view", FixFilter{Muted: true, MutedTypes: muted}, []fixKey{{1, "title"}, {2, "title"}}, 0},
		{"muted view without types", FixFilter{Muted: true}, nil, 0},
		{"a type that doesn't match", FixFilter{MutedTypes: []FixType{{Field: "title", Path: "dnb+file", Kind: "replace"}}}, []fixKey{{1, "title"}, {1, "languages"}, {2, "isbn"}, {2, "title"}}, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p, err := s.Fixes(ctx, 7, tt.f, nil)
			if err != nil || fmt.Sprint(fixKeys(p)) != fmt.Sprint(tt.want) || p.MutedHidden != tt.wantHidden {
				t.Fatalf("Fixes = %+v, %v; want %v, hidden %d", p, err, tt.want, tt.wantHidden)
			}
		})
	}
}
