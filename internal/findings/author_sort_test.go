package findings

import (
	"context"
	"slices"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

func TestAuthorSort(t *testing.T) {
	for _, tt := range []struct{ name, want string }{
		{"Geoffrey Parker", "Parker, Geoffrey"}, {"Ludwig von Mises", "von Mises, Ludwig"}, {"Ludwig van der Waals", "van der Waals, Ludwig"},
		{"Saint Thomas Aquinas", "Thomas Aquinas, Saint"}, {"St. Thomas Aquinas", "Thomas Aquinas, Saint"}, {"San Juan", "Juan, Saint"},
		{"Pope Benedict XVI", "Benedict XVI, Pope"}, {"Papa John", "John, Pope"},
		{"The Economist", "Economist, The"}, {"The New Yorker", "New Yorker, The"}, {"Financial Times", "Financial Times"},
		{"Der Spiegel", "Spiegel, Der"}, {"Spiegel Online", "Spiegel Online"}, {"The Wall Street Journal", "Wall Street Journal, The"},
		{"Nature", "Nature"}, {"Lonely Planet", "Lonely Planet"}, {"Harvard Business Review", "Harvard Business Review"},
		{"Mario Vargas Llosa", "Vargas Llosa, Mario"}, {"Gabriel Garcia Marquez", "García Márquez, Gabriel"}, {"Gabriel García Márquez", "García Márquez, Gabriel"},
		{"John Smith, Jr.", "Smith, John, Jr."}, {"Smith, John, Jr.", "Smith, John, Jr."},
		{"Al Gore", "Gore, Al"}, {"Van Morrison", "Morrison, Van"}, {"Robert de la Cruz", "de la Cruz, Robert"}, {"Ada Author", "Author, Ada"},
		{"", ""}, {"Plato", "Plato"},
		{"Saint \u00a0Thomas", "Thomas, Saint"}, {"Geoffrey\u001cParker", "Parker, Geoffrey"}, {"Name,ſr", "Name,ſr"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := authorSort(tt.name); got != tt.want {
				t.Fatalf("%q, want %q", got, tt.want)
			}
		})
	}
	if got := authorSorts([]string{"Geoffrey Parker", "", "Ludwig von Mises"}); got != "Parker, Geoffrey & von Mises, Ludwig" {
		t.Fatal(got)
	}
}

func TestDeriveAuthorSort(t *testing.T) {
	for _, tt := range []struct{ key, code string }{{"", "author_sort_missing"}, {"Ada Author", "author_sort_mismatch"}, {"Author, Ada", ""}} {
		p := goodPackage()
		p.Calibre.AuthorSort = tt.key
		got, err := Derive(context.Background(), p)
		var want []evidence.Finding
		if tt.code != "" {
			want = []evidence.Finding{{Code: tt.code, Field: "author_sort"}}
		}
		if err != nil || !slices.Equal(got, want) {
			t.Fatalf("%+v/%v; want %+v", got, err, want)
		}
	}
}
