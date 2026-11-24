package findings

import (
	"encoding/json"
	"os"
	"testing"
)

// TestCalibreAuthorSortGolden checks the port against Calibre 9.16 itself
// (scripts/author-sort-golden.sh).
func TestCalibreAuthorSortGolden(t *testing.T) {
	raw, err := os.ReadFile("testdata/author_sort_golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var golden struct {
		Tweaks struct {
			Method   string `json:"author_sort_copy_method"`
			Prefixes bool   `json:"author_use_surname_prefixes"`
		} `json:"tweaks"`
		Cases []struct {
			Name string `json:"name"`
			Sort string `json:"sort"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatal(err)
	}
	if golden.Tweaks.Method != "comma" || golden.Tweaks.Prefixes {
		t.Fatalf("golden made with other tweaks: %+v", golden.Tweaks)
	}
	if len(golden.Cases) < 60 {
		t.Fatalf("only %d golden cases", len(golden.Cases))
	}
	for _, c := range golden.Cases {
		if got := CalibreAuthorSort(c.Name); got != c.Sort {
			t.Errorf("CalibreAuthorSort(%q) = %q, Calibre says %q", c.Name, got, c.Sort)
		}
	}
}

func TestCalibreAuthorSort(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{"John le Carré", "Carré, John le"},
		{"Frederick P. Brooks Jr.", "Brooks, Frederick P. Jr."},
		{"Dr. John Smith", "Smith, John"},
		{"Lonely Planet", "Planet, Lonely"},
		{"Bill Gates (Microsoft)", "Gates, Bill"},
		{"Prof. Dr. A B", "B, A"},
		{"Smith Jr.", "Smith Jr."},
		{"Mr", "Mr"},
		{"Society of Friends", "Society of Friends"},
		{"Rowling, J. K.", "Rowling, J. K."},
		{"A.  B", "B, A."},
	} {
		if got := CalibreAuthorSort(tc.name); got != tc.want {
			t.Errorf("CalibreAuthorSort(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}
