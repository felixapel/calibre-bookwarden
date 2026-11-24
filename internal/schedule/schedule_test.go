package schedule

import (
	"strings"
	"testing"
	"time"
)

func berlin(t testing.TB) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatalf("Europe/Berlin: %v", err)
	}
	return loc
}

func local(loc *time.Location, y int, m time.Month, d, hh, mm int) time.Time {
	return time.Date(y, m, d, hh, mm, 0, 0, loc)
}

func TestParse(t *testing.T) {
	loc := time.UTC
	tests := []struct {
		name       string
		inc, full  string
		wantInc    string
		wantFull   string
		wantErrHas []string
	}{
		{name: "both off"},
		{name: "daily with a weekly", inc: "03:30", full: "sun 04:00", wantInc: "03:30", wantFull: "sun 04:00"},
		{name: "weekly only", full: "sun 04:00", wantFull: "sun 04:00"},
		{name: "both", inc: "00:00", full: "sat 23:59", wantInc: "00:00", wantFull: "sat 23:59"},
		{name: "midnight", inc: "00:00", full: "mon 00:00", wantInc: "00:00", wantFull: "mon 00:00"},
		{name: "hour out of range", inc: "24:00", wantErrHas: []string{"BOOKWARDEN_SCHEDULE_INCREMENTAL", "24:00", "HH:MM"}},
		{name: "minute out of range", inc: "03:60", wantErrHas: []string{"BOOKWARDEN_SCHEDULE_INCREMENTAL"}},
		{name: "no leading zero", inc: "3:30", wantErrHas: []string{"BOOKWARDEN_SCHEDULE_INCREMENTAL"}},
		{name: "seconds", inc: "03:30:00", wantErrHas: []string{"BOOKWARDEN_SCHEDULE_INCREMENTAL"}},
		{name: "am/pm", inc: "3:30pm", wantErrHas: []string{"BOOKWARDEN_SCHEDULE_INCREMENTAL"}},
		{name: "a weekday where a time goes", inc: "sun 03:30", wantErrHas: []string{"BOOKWARDEN_SCHEDULE_INCREMENTAL"}},
		{name: "a time without a weekday", full: "04:00", wantErrHas: []string{"BOOKWARDEN_SCHEDULE_FULL", "weekday"}},
		{name: "a long weekday name", full: "sunday 04:00", wantErrHas: []string{"BOOKWARDEN_SCHEDULE_FULL", "sun"}},
		{name: "an upper-case weekday", full: "Sun 04:00", wantErrHas: []string{"BOOKWARDEN_SCHEDULE_FULL"}},
		{name: "two spaces", full: "sun  04:00", wantErrHas: []string{"BOOKWARDEN_SCHEDULE_FULL"}},
		{name: "a bad time after a weekday", full: "sun 4:00", wantErrHas: []string{"BOOKWARDEN_SCHEDULE_FULL"}},
		{name: "a typo is not a weekday", full: "sub 04:00", wantErrHas: []string{"BOOKWARDEN_SCHEDULE_FULL"}},
		{name: "both wrong are both reported", inc: "x", full: "y", wantErrHas: []string{"BOOKWARDEN_SCHEDULE_INCREMENTAL", "BOOKWARDEN_SCHEDULE_FULL"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := Parse(tt.inc, tt.full, loc)
			if len(tt.wantErrHas) > 0 {
				if err == nil {
					t.Fatalf("Parse(%q, %q) accepted it", tt.inc, tt.full)
				}
				for _, s := range tt.wantErrHas {
					if !strings.Contains(err.Error(), s) {
						t.Errorf("error %q lacks %q", err, s)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := c.IncrementalText(); got != tt.wantInc {
				t.Errorf("incremental = %q, want %q", got, tt.wantInc)
			}
			if got := c.FullText(); got != tt.wantFull {
				t.Errorf("full = %q, want %q", got, tt.wantFull)
			}
			if c.Enabled() != (tt.wantInc != "" || tt.wantFull != "") {
				t.Errorf("Enabled() = %v", c.Enabled())
			}
		})
	}
}

func TestDue(t *testing.T) {
	loc := berlin(t)
	mustParse := func(inc, full string) Config {
		c, err := parseLoose(inc, full, loc)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	// 2026-11-01 is a Sunday, 2026-11-02 a Monday.
	sun4 := local(loc, 2026, 11, 1, 4, 0)
	mon4 := local(loc, 2026, 11, 2, 4, 0)
	tests := []struct {
		name        string
		cfg         Config
		after, now  time.Time
		want        Kind
		wantAt      time.Time
		wantNothing bool
	}{
		{name: "off", cfg: mustParse("", ""), after: mon4.Add(-time.Hour), now: mon4.Add(time.Hour), wantNothing: true},
		{name: "before the slot", cfg: mustParse("04:00", ""), after: mon4.Add(-2 * time.Hour), now: mon4.Add(-time.Second), wantNothing: true},
		{name: "exactly at the slot", cfg: mustParse("04:00", ""), after: mon4.Add(-time.Minute), now: mon4, want: Incremental, wantAt: mon4},
		{name: "the slot was in the previous window", cfg: mustParse("04:00", ""), after: mon4, now: mon4.Add(time.Minute), wantNothing: true},
		{name: "a window that ends a second after the slot", cfg: mustParse("04:00", ""), after: mon4.Add(-time.Hour), now: mon4.Add(time.Second), want: Incremental, wantAt: mon4},
		{name: "full on its day", cfg: mustParse("", "sun 04:00"), after: sun4.Add(-time.Minute), now: sun4, want: Full, wantAt: sun4},
		{name: "full on another day", cfg: mustParse("", "sun 04:00"), after: mon4.Add(-time.Minute), now: mon4, wantNothing: true},
		{name: "both at the same time: full wins", cfg: mustParse("04:00", "sun 04:00"), after: sun4.Add(-time.Minute), now: sun4, want: Full, wantAt: sun4},
		{name: "both on one day, incremental earlier: full wins the day", cfg: mustParse("02:00", "sun 04:00"), after: sun4.Add(-3 * time.Hour), now: sun4, want: Full, wantAt: sun4},
		{name: "the incremental of a full day is not run at all", cfg: mustParse("06:00", "sun 04:00"), after: sun4.Add(time.Minute), now: sun4.Add(3 * time.Hour), wantNothing: true},
		{name: "the other days still get the incremental", cfg: mustParse("04:00", "sun 04:00"), after: mon4.Add(-time.Minute), now: mon4, want: Incremental, wantAt: mon4},
		{name: "a long window gives the latest slot only", cfg: mustParse("04:00", "sun 04:00"), after: sun4.Add(-30 * time.Hour), now: mon4.Add(time.Hour), want: Incremental, wantAt: mon4},
		{name: "a long window ending on the full day", cfg: mustParse("04:00", "sun 04:00"), after: sun4.Add(-30 * time.Hour), now: sun4.Add(time.Hour), want: Full, wantAt: sun4},
		{name: "the clock went backwards", cfg: mustParse("04:00", ""), after: mon4.Add(time.Hour), now: mon4.Add(-time.Hour), wantNothing: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := tt.cfg.Due(tt.after, tt.now)
			if tt.wantNothing {
				if ok {
					t.Fatalf("Due() = %+v, want nothing", got)
				}
				return
			}
			if !ok || got.Kind != tt.want || !got.At.Equal(tt.wantAt) {
				t.Fatalf("Due() = %+v, %v; want %s at %v", got, ok, tt.want, tt.wantAt)
			}
		})
	}
}

// Walking the clock across a day in one-minute steps, a daily slot fires
// exactly once per local calendar day, including the days Europe/Berlin
// changes its clocks.
func TestDueAcrossDST(t *testing.T) {
	loc := berlin(t)
	tests := []struct {
		name  string
		slot  string
		first time.Time // local midnight to start walking from
		days  int
		// want is the instants, in UTC, the slot fires at.
		want []string
	}{
		{
			name: "spring forward 2027-03-28: 02:30 does not exist that day",
			slot: "02:30", first: local(loc, 2027, 3, 27, 0, 0), days: 3,
			want: []string{"2027-03-27T01:30:00Z", "2027-03-28T01:30:00Z", "2027-03-29T00:30:00Z"},
		},
		{
			name: "spring forward: 03:30 is after the gap",
			slot: "03:30", first: local(loc, 2027, 3, 27, 0, 0), days: 3,
			want: []string{"2027-03-27T02:30:00Z", "2027-03-28T01:30:00Z", "2027-03-29T01:30:00Z"},
		},
		{
			name: "fall back 2026-10-25: 02:30 happens twice, the first one counts",
			slot: "02:30", first: local(loc, 2026, 10, 24, 0, 0), days: 3,
			want: []string{"2026-10-24T00:30:00Z", "2026-10-25T00:30:00Z", "2026-10-26T01:30:00Z"},
		},
		{
			name: "fall back: 04:00 is after the repeated hour",
			slot: "04:00", first: local(loc, 2026, 10, 24, 0, 0), days: 3,
			want: []string{"2026-10-24T02:00:00Z", "2026-10-25T03:00:00Z", "2026-10-26T03:00:00Z"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := parseLoose(tt.slot, "", loc)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			prev := tt.first.UTC()
			end := tt.first.AddDate(0, 0, tt.days+1).UTC()
			for now := prev.Add(time.Minute); now.Before(end); now = now.Add(time.Minute) {
				if s, ok := c.Due(prev, now); ok {
					got = append(got, s.At.UTC().Format(time.RFC3339))
				}
				prev = now
			}
			// The walk covers tt.days+1 local days; the slots of the first
			// tt.days of them are the ones listed.
			if len(got) < len(tt.want) {
				t.Fatalf("fired %v, want %v", got, tt.want)
			}
			for i, w := range tt.want {
				if got[i] != w {
					t.Errorf("slot %d = %s, want %s (all: %v)", i, got[i], w, got)
				}
			}
			seen := map[string]bool{}
			for _, g := range got {
				if seen[g] {
					t.Errorf("slot %s fired twice", g)
				}
				seen[g] = true
			}
		})
	}
}

func TestNext(t *testing.T) {
	loc := berlin(t)
	c, err := Parse("04:00", "sun 05:00", loc)
	if err != nil {
		t.Fatal(err)
	}
	// Saturday 2026-10-31 12:00.
	now := local(loc, 2026, 10, 31, 12, 0)
	inc, full := c.Next(now)
	if want := local(loc, 2026, 11, 2, 4, 0); !inc.Equal(want) {
		t.Errorf("next incremental = %v, want %v (Sunday belongs to the full run)", inc, want)
	}
	if want := local(loc, 2026, 11, 1, 5, 0); !full.Equal(want) {
		t.Errorf("next full = %v, want %v", full, want)
	}
	off, _ := Parse("", "", loc)
	if a, b := off.Next(now); !a.IsZero() || !b.IsZero() {
		t.Errorf("Next() with the scheduler off = %v, %v", a, b)
	}
	// The slot of this very moment is already due, not next.
	at := local(loc, 2026, 11, 2, 4, 0)
	if inc, _ := c.Next(at); !inc.Equal(at.AddDate(0, 0, 1)) {
		t.Errorf("next after the slot = %v", inc)
	}
}
