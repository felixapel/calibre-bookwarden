// Package schedule decides when bookwarden starts a check on its own
// (ADR-039): a daily incremental run and a weekly full run, in the
// container's time zone. It is off unless configured.
//
// This file is the calendar: parsing the two settings and finding the slots
// between two instants. scheduler.go is the worker that acts on them.
package schedule

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"time"
	_ "time/tzdata" // the image has no zoneinfo; TZ=Europe/Berlin must still resolve
)

// Kind is what a slot starts.
type Kind string

const (
	Incremental Kind = "incremental"
	Full        Kind = "full"
)

// Slot is one scheduled start: its kind and the instant it is due.
type Slot struct {
	Kind Kind
	At   time.Time
}

// Daily is a local time of day.
type Daily struct{ Hour, Minute int }

func (d Daily) String() string { return fmt.Sprintf("%02d:%02d", d.Hour, d.Minute) }

// Weekly is a local weekday and time of day.
type Weekly struct {
	Weekday      time.Weekday
	Hour, Minute int
}

var weekdays = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

func (w Weekly) String() string {
	return fmt.Sprintf("%s %02d:%02d", weekdays[w.Weekday], w.Hour, w.Minute)
}

// Config is the schedule: nil means that kind is off. Loc is the time zone
// the times are read in.
type Config struct {
	Incremental *Daily
	Full        *Weekly
	Loc         *time.Location
}

// Environment variable names, used in the errors Parse returns.
const (
	EnvIncremental = "BOOKWARDEN_SCHEDULE_INCREMENTAL"
	EnvFull        = "BOOKWARDEN_SCHEDULE_FULL"
)

var (
	timeRE   = regexp.MustCompile(`^([01][0-9]|2[0-3]):([0-5][0-9])$`)
	weeklyRE = regexp.MustCompile(`^(sun|mon|tue|wed|thu|fri|sat) ([01][0-9]|2[0-3]):([0-5][0-9])$`)
)

// Parse reads the two settings strictly. Empty turns a kind off. An
// invalid value is an error that names its variable; both are checked, so
// one run of the program reports every mistake.
func Parse(incremental, full string, loc *time.Location) (Config, error) {
	c, err := parseConfig(incremental, full, loc)
	if err != nil {
		return Config{}, err
	}
	// Only a full run measures the files again. An incremental schedule alone
	// would reuse the first measurement for ever (ADR-039).
	if c.Incremental != nil && c.Full == nil {
		return Config{}, fmt.Errorf("%s %q needs %s as well: only a full check measures the files again, and an incremental schedule alone would reuse the first measurement for ever (for example sun 04:00)", EnvIncremental, incremental, EnvFull)
	}
	return c, nil
}

// parseConfig is Parse without that rule.
func parseConfig(incremental, full string, loc *time.Location) (Config, error) {
	if loc == nil {
		loc = time.UTC
	}
	c := Config{Loc: loc}
	var errs []error
	if incremental != "" {
		m := timeRE.FindStringSubmatch(incremental)
		if m == nil {
			errs = append(errs, fmt.Errorf("%s %q must be a daily local time such as 03:30 (24-hour HH:MM), or empty to turn it off", EnvIncremental, incremental))
		} else {
			c.Incremental = &Daily{Hour: atoi(m[1]), Minute: atoi(m[2])}
		}
	}
	if full != "" {
		m := weeklyRE.FindStringSubmatch(full)
		if m == nil {
			errs = append(errs, fmt.Errorf("%s %q must be a weekday and a local time such as \"sun 04:00\" (weekday sun, mon, tue, wed, thu, fri or sat in lower case, then 24-hour HH:MM), or empty to turn it off", EnvFull, full))
		} else {
			c.Full = &Weekly{Weekday: time.Weekday(slices.Index(weekdays, m[1])), Hour: atoi(m[2]), Minute: atoi(m[3])}
		}
	}
	if len(errs) > 0 {
		return Config{}, errors.Join(errs...)
	}
	return c, nil
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s) // the patterns only let digits through
	return n
}

// Enabled reports whether any kind is on.
func (c Config) Enabled() bool { return c.Incremental != nil || c.Full != nil }

// IncrementalText and FullText are the settings as read, "" when off.
func (c Config) IncrementalText() string {
	if c.Incremental == nil {
		return ""
	}
	return c.Incremental.String()
}

func (c Config) FullText() string {
	if c.Full == nil {
		return ""
	}
	return c.Full.String()
}

// slotTime is the instant the wall-clock time hh:mm of the local date y-m-d
// refers to in loc. It does not depend on how time.Date resolves the two odd
// cases:
//   - A time that happens twice (clocks going back) is the first time.
//   - A time that doesn't happen (clocks going forward) is read with the
//     offset the day before ended with, so it falls as far past the jump as it
//     was before it: 02:30 on a day Berlin jumps 02:00 to 03:00 is 03:30. (The
//     offset at the day's own midnight would be wrong where the jump is at
//     midnight, as in Asia/Beirut: that midnight is already after it.)
//
// Each local date therefore has exactly one slot, so there is no double run
// and no skipped day.
func slotTime(loc *time.Location, y int, m time.Month, d, hh, mm int) time.Time {
	wall := time.Date(y, m, d, hh, mm, 0, 0, time.UTC)
	_, startOffset := time.Date(y, m, d-1, 23, 59, 0, 0, loc).Zone()
	offsets := []int{startOffset}
	for _, probe := range []time.Time{time.Date(y, m, d, 12, 0, 0, 0, loc), time.Date(y, m, d, 23, 59, 0, 0, loc)} {
		if _, o := probe.Zone(); !slices.Contains(offsets, o) {
			offsets = append(offsets, o)
		}
	}
	var best time.Time
	for _, o := range offsets {
		inst := wall.Add(-time.Duration(o) * time.Second)
		l := inst.In(loc)
		if l.Year() == y && l.Month() == m && l.Day() == d && l.Hour() == hh && l.Minute() == mm && (best.IsZero() || inst.Before(best)) {
			best = inst
		}
	}
	if best.IsZero() {
		best = wall.Add(-time.Duration(startOffset) * time.Second)
	}
	return best
}

// maxWindow bounds the days Due looks back, so a clock that jumped far
// can't make it enumerate years.
const maxWindow = 8 * 24 * time.Hour

// slotsOn returns the slots of one local date: the incremental one, unless
// the full run's weekday is this date's (the full run replaces it), and the
// full one.
func (c Config) slotsOn(y int, m time.Month, d int) []Slot {
	wd := time.Date(y, m, d, 12, 0, 0, 0, c.Loc).Weekday()
	var out []Slot
	fullDay := c.Full != nil && c.Full.Weekday == wd
	if c.Incremental != nil && !fullDay {
		out = append(out, Slot{Incremental, slotTime(c.Loc, y, m, d, c.Incremental.Hour, c.Incremental.Minute)})
	}
	if fullDay {
		out = append(out, Slot{Full, slotTime(c.Loc, y, m, d, c.Full.Hour, c.Full.Minute)})
	}
	return out
}

// Due returns the latest slot in (after, now], if there is one. One run
// serves every slot before it, so a window that holds several (after a long
// pause) still starts one run.
func (c Config) Due(after, now time.Time) (Slot, bool) {
	if !c.Enabled() || !now.After(after) {
		return Slot{}, false
	}
	if now.Sub(after) > maxWindow {
		after = now.Add(-maxWindow)
	}
	var best Slot
	var found bool
	from := after.In(c.Loc)
	to := now.In(c.Loc)
	day := time.Date(from.Year(), from.Month(), from.Day(), 12, 0, 0, 0, c.Loc)
	last := time.Date(to.Year(), to.Month(), to.Day(), 12, 0, 0, 0, c.Loc)
	for ; !day.After(last); day = day.AddDate(0, 0, 1) {
		for _, s := range c.slotsOn(day.Year(), day.Month(), day.Day()) {
			if s.At.After(after) && !s.At.After(now) && (!found || s.At.After(best.At)) {
				best, found = s, true
			}
		}
	}
	return best, found
}

// Next returns the next incremental and the next full slot after now, zero
// for a kind that is off. An incremental slot on the full run's weekday does
// not exist, so it is not counted.
func (c Config) Next(now time.Time) (incremental, full time.Time) {
	from := now.In(c.Loc)
	day := time.Date(from.Year(), from.Month(), from.Day(), 12, 0, 0, 0, c.Loc)
	for i := 0; i < 9 && (incremental.IsZero() && c.Incremental != nil || full.IsZero() && c.Full != nil); i++ {
		for _, s := range c.slotsOn(day.Year(), day.Month(), day.Day()) {
			if !s.At.After(now) {
				continue
			}
			switch {
			case s.Kind == Incremental && incremental.IsZero():
				incremental = s.At
			case s.Kind == Full && full.IsZero():
				full = s.At
			}
		}
		day = day.AddDate(0, 0, 1)
	}
	return incremental, full
}
