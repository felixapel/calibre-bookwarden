package store

import (
	"math"
	"testing"
)

func TestClopperPearson(t *testing.T) {
	tests := []struct {
		n, k         int
		lower, upper float64
		ok           bool
	}{
		{10, 0, 0, 0.3085, true},
		{10, 5, 0.1871, 0.8129, true},
		{10, 10, 0.6915, 1, true},
		{20, 1, 0.0013, 0.2487, true},
		{0, 0, 0, 0, false},
		{5, 6, 0, 0, false},
	}
	for _, tt := range tests {
		lo, hi, ok := ClopperPearson(tt.n, tt.k)
		if ok != tt.ok || math.Abs(lo-tt.lower) > 0.0005 || math.Abs(hi-tt.upper) > 0.0005 {
			t.Errorf("ClopperPearson(%d, %d) = %.4f, %.4f, %v; want %.4f, %.4f, %v", tt.n, tt.k, lo, hi, ok, tt.lower, tt.upper, tt.ok)
		}
	}
}
