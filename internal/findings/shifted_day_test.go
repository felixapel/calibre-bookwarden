package findings

import (
	"testing"
	"time"
)

// Calibre's fix_only_date moves day 1 to day 2 and the last day of a month
// other than December back one day.
func TestShiftedDay(t *testing.T) {
	for _, year := range []int{2004, 2005} { // a leap year and a common one
		for m := time.January; m <= time.December; m++ {
			first := time.Date(year, m, 1, 0, 0, 0, 0, time.UTC)
			last := first.AddDate(0, 1, -1)
			for _, tt := range []struct {
				day  time.Time
				want bool
			}{
				{first, true},
				{first.AddDate(0, 0, 1), false},
				{last.AddDate(0, 0, -1), false},
				{last, m != time.December},
			} {
				if got := ShiftedDay(tt.day); got != tt.want {
					t.Errorf("ShiftedDay(%s) = %v, want %v", tt.day.Format(time.DateOnly), got, tt.want)
				}
			}
		}
	}
}
