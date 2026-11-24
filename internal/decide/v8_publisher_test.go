package decide_test

import (
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/decide"
)

func TestPublisherEquivalent(t *testing.T) {
	for _, tt := range []struct {
		a, b string
		want bool
	}{
		{"Acme Press", "acme press", true},
		{"Acme", "Acme Verlag", true},
		{"Acme", "Acme Publishing Group", true},
		{"Acme Publishers", "ACME, Inc.", true},
		{"Acme S.A.", "Acme", true},
		{"Acme GmbH", "Acme Ltd", true},
		{"Acme LLC", "Acme", true},
		{"Acme S.L.", "Acme Publishing", true},
		{"Harper & Row", "Harper and Row", true},
		{"Harper Collins", "HarperCollins", true},
		{"Penguin Books", "Penguin", true},
		{"Acme Co.", "Acme Company", true},
		{"Acme Corp", "Acme", true},
		{"Acme", "Other", false},
		{"Penguin UK", "Penguin Books Ltd", false},
		{"Publishing", "Publishing Group", false},
		{"", "", false},
	} {
		if got := decide.PublisherEquivalent(tt.a, tt.b); got != tt.want {
			t.Errorf("PublisherEquivalent(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}
