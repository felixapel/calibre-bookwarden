package findings

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

// TestTitleSortGolden checks the port against Calibre 9.16 itself
// (scripts/title-sort-golden.sh).
func TestTitleSortGolden(t *testing.T) {
	raw, err := os.ReadFile("testdata/title_sort_golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Title string  `json:"title"`
		Lang  *string `json:"lang"`
		Sort  string  `json:"sort"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 150 {
		t.Fatalf("only %d golden cases", len(cases))
	}
	for _, c := range cases {
		lang := ""
		if c.Lang != nil {
			lang = *c.Lang
		}
		if got := titleSort(c.Title, lang); got != c.Sort {
			t.Errorf("titleSort(%q, %q) = %q, Calibre says %q", c.Title, lang, got, c.Sort)
		}
	}
}

func ptr(s string) *string { return &s }

func titleSortFindings(t *testing.T, version int, p evidence.Package) []evidence.Finding {
	t.Helper()
	fs, err := DeriveVersion(context.Background(), p, version)
	if err != nil {
		t.Fatal(err)
	}
	var out []evidence.Finding
	for _, f := range fs {
		if f.Field == "title_sort" {
			out = append(out, f)
		}
	}
	return out
}

func TestTitleSortFindings(t *testing.T) {
	long := strings.Repeat("a", 513)
	missing := []evidence.Finding{{Code: "title_sort_missing", Field: "title_sort"}}
	notMoved := []evidence.Finding{{Code: "title_sort_article_not_moved", Field: "title_sort"}}
	mismatch := []evidence.Finding{{Code: "title_sort_mismatch", Field: "title_sort"}}
	tooLong := []evidence.Finding{{Code: "metadata_unverified", Field: "title_sort", Reason: "field_too_long"}}
	for _, tt := range []struct {
		name, title string
		langs       []string
		sort        *string
		want        []evidence.Finding
	}{
		{"not captured", "The Book", []string{"eng"}, nil, nil},
		{"English article moved", "The Clockwork Garden", []string{"eng"}, ptr("Clockwork Garden, The"), nil},
		{"empty", "Synthetic", []string{"eng"}, ptr(""), missing},
		{"blank", "Synthetic", []string{"eng"}, ptr("  "), missing},
		{"German article moved", "Die Welt", []string{"deu"}, ptr("Welt, Die"), nil},
		{"German article kept", "Die Welt", []string{"deu"}, ptr("Die Welt"), notMoved},
		{"English article kept", "The Book", []string{"eng"}, ptr("The Book"), notMoved},
		{"no German article", "The Book", []string{"deu"}, ptr("The Book"), nil},
		{"English table for a German book", "The Book", []string{"deu"}, ptr("Book, The"), nil},
		{"stale after a retitle", "Der Prozess", []string{"deu"}, ptr("Welt, Die"), mismatch},
		{"no language", "Die Welt", nil, ptr("Die Welt"), nil},
		{"no language, German table", "Die Welt", nil, ptr("Welt, Die"), nil},
		{"normalized", "Die Welt", []string{"deu"}, ptr("welt,  die"), nil},
		{"long title", long, []string{"eng"}, ptr("x"), tooLong},
		{"long sort", "Synthetic", []string{"eng"}, ptr(long), tooLong},
		{"no title", "", []string{"eng"}, ptr("x"), nil},
		{"first language counts", "Das Boot", []string{"deu", "eng"}, ptr("Boot, Das"), nil},
		{"two-letter language", "Die Welt", []string{"de"}, ptr("Die Welt"), notMoved},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := goodPackage()
			p.Calibre.Title, p.Calibre.Languages, p.Calibre.TitleSort = tt.title, tt.langs, tt.sort
			if got := titleSortFindings(t, Version, p); !slices.Equal(got, tt.want) {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestDeriveVersionsTitleSort(t *testing.T) {
	p := goodPackage()
	p.Calibre.TitleSort = ptr("Something Else")
	for _, v := range []int{1, 2} {
		if got := titleSortFindings(t, v, p); got != nil {
			t.Fatalf("version %d derived %+v", v, got)
		}
	}
	if got := titleSortFindings(t, 3, p); len(got) != 1 || got[0].Code != "title_sort_mismatch" {
		t.Fatalf("version 3 derived %+v", got)
	}
	raw, hash, err := Seal(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := Open(context.Background(), raw, hash)
	if err != nil || opened.FindingsVersion != Version {
		t.Fatalf("sealed version %d/%v", opened.FindingsVersion, err)
	}
}

func TestOpenVersions(t *testing.T) {
	ctx := context.Background()
	p := goodPackage()
	p.Calibre.TitleSort = ptr("Something Else")
	for _, v := range []int{1, 2, 3} {
		p.FindingsVersion = v
		fs, err := DeriveVersion(ctx, p, v)
		if err != nil {
			t.Fatal(err)
		}
		p.Findings = fs
		raw, hash, err := evidence.Seal(p)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Open(ctx, raw, hash); err != nil {
			t.Fatalf("version %d: %v", v, err)
		}
	}
	p.FindingsVersion = 2
	p.Findings, _ = DeriveVersion(ctx, p, 3)
	raw, hash, err := evidence.Seal(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ctx, raw, hash); !errors.Is(err, ErrMismatch) {
		t.Fatalf("a version-2 package with a version-3 finding opened: %v", err)
	}
}

func TestTitleSortProposal(t *testing.T) {
	sort := func(s string) *string { return &s }
	for _, tc := range []struct {
		name      string
		title     string
		titleSort *string
		want      string
		ok        bool
	}{
		{"mismatch", "The Captive Mind", sort("Innocent Captive"), "Captive Mind, The", true},
		{"article not moved", "The Pickwick Papers", sort("The Pickwick Papers"), "Pickwick Papers, The", true},
		{"another table language", "Die Welt", sort("Welt, Die"), "", false},
		{"correct", "The Captive Mind", sort("Captive Mind, The"), "", false},
		{"not captured", "The Captive Mind", nil, "", false},
		{"empty title", "", sort("Captive Mind, The"), "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := evidence.Record{Title: tc.title, TitleSort: tc.titleSort, Languages: []string{"eng"}}
			got, ok := TitleSortProposal(rec)
			if ok != tc.ok || (ok && got != tc.want) {
				t.Fatalf("TitleSortProposal = (%q, %v), want (%q, %v)", got, ok, tc.want, tc.ok)
			}
		})
	}
}

// TestTitleSortMissingWithoutTitle pins the M2.4 order: an empty sort is
// title_sort_missing even when the title itself is empty.
func TestTitleSortMissingWithoutTitle(t *testing.T) {
	empty := ""
	d := derivation{ctx: context.Background(), found: map[evidence.Finding]struct{}{}}
	d.titleSortFinding(evidence.Record{Title: "", TitleSort: &empty})
	if _, ok := d.found[evidence.Finding{Code: "title_sort_missing", Field: "title_sort"}]; !ok || len(d.found) != 1 {
		t.Fatalf("findings = %v", d.found)
	}
	if _, ok := TitleSortProposal(evidence.Record{Title: "", TitleSort: &empty}); ok {
		t.Fatal("a sort proposed for an empty title")
	}
}
