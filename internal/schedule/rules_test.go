package schedule

import (
	"context"
	"strings"
	"testing"
	"time"
)

// parseLoose is Parse without the rule that an incremental schedule needs a
// full one, for tests of the calendar alone.
func parseLoose(inc, full string, loc *time.Location) (Config, error) {
	return parseConfig(inc, full, loc)
}

// An incremental schedule alone would reuse evidence for ever: only a full
// run measures files again (ADR-039).
func TestParseNeedsAFullScheduleWithAnIncrementalOne(t *testing.T) {
	_, err := Parse("03:30", "", time.UTC)
	if err == nil {
		t.Fatal("an incremental schedule without a full one was accepted")
	}
	for _, want := range []string{EnvIncremental, EnvFull, "03:30"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	for _, ok := range [][2]string{{"", ""}, {"", "sun 04:00"}, {"03:30", "sun 04:00"}} {
		if _, err := Parse(ok[0], ok[1], time.UTC); err != nil {
			t.Errorf("Parse(%q, %q) = %v", ok[0], ok[1], err)
		}
	}
}

// Asia/Beirut jumps from 00:00 to 01:00 on 2026-03-29, so 00:30 doesn't exist
// that day. Reading the day's start offset from the instant after the jump
// would put that slot on the day before.
func TestDueWhenMidnightIsSkipped(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Beirut")
	if err != nil {
		t.Fatal(err)
	}
	c, err := parseLoose("00:30", "", loc)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 3, 26, 12, 0, 0, 0, loc)
	prev := start.UTC()
	perDay := map[string][]string{}
	for now := prev.Add(time.Minute); now.Before(start.AddDate(0, 0, 6)); now = now.Add(time.Minute) {
		if s, ok := c.Due(prev, now); ok {
			day := s.At.In(loc).Format("2006-01-02")
			perDay[day] = append(perDay[day], s.At.UTC().Format(time.RFC3339))
		}
		prev = now
	}
	for _, day := range []string{"2026-03-27", "2026-03-28", "2026-03-29", "2026-03-30", "2026-03-31"} {
		if len(perDay[day]) != 1 {
			t.Errorf("%s: slots %v, want exactly one", day, perDay[day])
		}
	}
	// 00:30 on the 29th is read with the offset the day before ended with
	// (+2), which is 01:30 after the jump.
	if got := perDay["2026-03-29"]; len(got) == 1 && got[0] != "2026-03-28T22:30:00Z" {
		t.Errorf("the slot of the jump day = %v, want 2026-03-28T22:30:00Z", got)
	}
}

// Settings shows the last scheduled check's slot, and the next ones, in the
// zone the schedule is read in, and names a zone that Go calls "Local".
func TestStatusUsesTheConfiguredZone(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := Parse("04:00", "sun 05:00", loc)
	if err != nil {
		t.Fatal(err)
	}
	utcSlot := time.Date(2026, 11, 1, 4, 0, 0, 0, time.UTC) // as the database returns it
	h := &world{started: []Slot{{Full, utcSlot}}}
	s := &Scheduler{Config: cfg, Runs: h, History: h, Now: func() time.Time { return time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC) }}
	st, err := s.Status(context.Background())
	if err != nil || st.Last == nil {
		t.Fatalf("Status() = %+v, %v", st, err)
	}
	if st.Last.Slot.At.Location() != loc || st.Last.Slot.At.Format("15:04 MST") != "05:00 CET" {
		t.Errorf("last slot = %v in %v, want 05:00 CET", st.Last.Slot.At, st.Last.Slot.At.Location())
	}
	s.skip(Slot{Incremental, utcSlot}, utcSlot, "busy")
	if st, _ := s.Status(context.Background()); st.Skipped.Slot.At.Location() != loc {
		t.Errorf("skipped slot is in %v", st.Skipped.Slot.At.Location())
	}

	local, err := Parse("04:00", "sun 05:00", time.Local)
	if err != nil {
		t.Fatal(err)
	}
	s = &Scheduler{Config: local, Runs: h, History: h, Now: func() time.Time { return utcSlot }}
	if st, _ := s.Status(context.Background()); st.Zone == "Local" || st.Zone == "" {
		t.Errorf("Zone = %q: name the zone, not Go's placeholder", st.Zone)
	}
}
