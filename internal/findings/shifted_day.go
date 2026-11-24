package findings

import "time"

// ShiftedDay mirrors Calibre's fix_only_date: day 1 becomes day 2, and the
// last day of a month other than December moves back one day. Calibre would
// store such a date differently, so it is neither written (fix) nor proposed
// (decide, from version 10).
func ShiftedDay(t time.Time) bool {
	next := t.AddDate(0, 0, 1)
	return t.Day() == 1 || (next.Month() != t.Month() && t.Month() != time.December)
}
