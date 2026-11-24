package decide_test

import (
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/decide"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

const rowlingSort = "Rowling, J. K."

// sorted is tierA with Calibre's title sort and author sort table set; nil
// leaves the field uncaptured.
func sorted(titleSort *string, authorSort string, table map[string]string) func() evidence.Package {
	return func() evidence.Package {
		p := tierA()
		p.Calibre.TitleSort, p.Calibre.AuthorSort, p.Calibre.AuthorSortMap = titleSort, authorSort, table
		return p
	}
}

func str(s string) *string { return &s }

func sortProps(t *testing.T, p evidence.Package, version int) []evidence.Proposal {
	t.Helper()
	d, err := decide.AtVersion(t.Context(), p, version)
	if err != nil {
		t.Fatal(err)
	}
	var out []evidence.Proposal
	for _, pr := range d.Proposals {
		if pr.Field == "title_sort" || pr.Field == "author_sort" {
			out = append(out, pr)
		}
	}
	return out
}

func TestSortProposals(t *testing.T) {
	rule := []string{evidence.RootCalibreRule}
	table := map[string]string{A: rowlingSort}
	wrongTitleSort := sorted(str("Stein der Weisen"), rowlingSort, table)
	for _, tt := range []struct {
		name    string
		p       func() evidence.Package
		version int
		want    []evidence.Proposal
	}{
		{"wrong title sort", wrongTitleSort, decide.Version,
			[]evidence.Proposal{prop("title_sort", []string{"Stein der Weisen"}, []string{T}, rule)}},
		{"title proposal too", func() evidence.Package {
			p := wrongTitleSort()
			p.Calibre.Title = "Harry Potter"
			return p
		}, decide.Version, nil},
		{"tier C", func() evidence.Package {
			p := wrongTitleSort()
			p.Formats = append(p.Formats, format("MOBI", "mobi", "Other", []string{A}, []string{"de"}, []string{I}))
			return p
		}, decide.Version, []evidence.Proposal{prop("title_sort", []string{"Stein der Weisen"}, []string{T}, rule)}},
		{"wrong author sort", sorted(str(T), "Road, The", table), decide.Version,
			[]evidence.Proposal{prop("author_sort", []string{"Road, The"}, []string{rowlingSort}, rule)}},
		{"author sort lists one of two", func() evidence.Package {
			p := sorted(str(T), rowlingSort, map[string]string{A: rowlingSort, "Klaus Fritz": "Fritz, Klaus"})()
			p.Calibre.Authors = []string{A, "Klaus Fritz"}
			return p
		}, decide.Version, []evidence.Proposal{prop("author_sort", []string{rowlingSort}, []string{rowlingSort + " & Fritz, Klaus"}, rule)}},
		{"table entry not inverted", sorted(str(T), "Road, The", map[string]string{A: A}), decide.Version, nil},
		{"table entry customised", sorted(str(T), "Road, The", map[string]string{A: "Rowling, Joanne K."}), decide.Version, nil},
		{"author missing from the table", sorted(str(T), "Road, The", map[string]string{"Klaus Fritz": "Fritz, Klaus"}), decide.Version, nil},
		{"table not captured", sorted(str(T), "Road, The", nil), decide.Version, nil},
		{"author sort right", sorted(str(T), rowlingSort, table), decide.Version, nil},
		{"empty author sort", sorted(str(T), "", table), decide.Version,
			[]evidence.Proposal{prop("author_sort", []string{}, []string{rowlingSort}, rule)}},
		{"authors proposal too", func() evidence.Package {
			p := sorted(str(T), "Road, The", map[string]string{"Joanne Smith": "Smith, Joanne"})()
			p.Calibre.Authors = []string{"Joanne Smith"}
			return p
		}, decide.Version, nil},
		{"languages proposal too", func() evidence.Package {
			p := wrongTitleSort()
			p.Calibre.Languages = []string{}
			return p
		}, decide.Version, nil},
		{"version 3", wrongTitleSort, 3, nil},
		{"version 3, wrong author sort", sorted(str(T), "Road, The", table), 3, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := sortProps(t, tt.p(), tt.version); !equalProposals(got, tt.want) {
				t.Fatalf("sort proposals = %+v, want %+v", got, tt.want)
			}
		})
	}
}
