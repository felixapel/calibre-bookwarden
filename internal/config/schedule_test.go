package config

import (
	"strings"
	"testing"
)

func TestRetentionAndScheduleFromEnv(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantErr []string
		check   func(t *testing.T, c Config)
	}{
		{
			name: "defaults: ten runs, no schedule",
			check: func(t *testing.T, c Config) {
				if c.RetainRuns != 10 || c.Schedule.Enabled() {
					t.Errorf("RetainRuns = %d, schedule on = %v", c.RetainRuns, c.Schedule.Enabled())
				}
			},
		},
		{name: "keep all", env: map[string]string{"BOOKWARDEN_RETAIN_RUNS": "0"}, check: func(t *testing.T, c Config) {
			if c.RetainRuns != 0 {
				t.Errorf("RetainRuns = %d, want 0", c.RetainRuns)
			}
		}},
		{name: "keep 25", env: map[string]string{"BOOKWARDEN_RETAIN_RUNS": "25"}, check: func(t *testing.T, c Config) {
			if c.RetainRuns != 25 {
				t.Errorf("RetainRuns = %d", c.RetainRuns)
			}
		}},
		{name: "negative", env: map[string]string{"BOOKWARDEN_RETAIN_RUNS": "-1"}, wantErr: []string{"BOOKWARDEN_RETAIN_RUNS must be a whole number of 0 or more"}},
		{name: "not a number", env: map[string]string{"BOOKWARDEN_RETAIN_RUNS": "ten"}, wantErr: []string{"BOOKWARDEN_RETAIN_RUNS"}},
		{name: "a fraction", env: map[string]string{"BOOKWARDEN_RETAIN_RUNS": "1.5"}, wantErr: []string{"BOOKWARDEN_RETAIN_RUNS"}},
		{
			name: "both schedules in Berlin",
			env:  map[string]string{"BOOKWARDEN_SCHEDULE_INCREMENTAL": "03:30", "BOOKWARDEN_SCHEDULE_FULL": "sun 04:00", "TZ": "Europe/Berlin"},
			check: func(t *testing.T, c Config) {
				if c.Schedule.IncrementalText() != "03:30" || c.Schedule.FullText() != "sun 04:00" || c.Schedule.Loc.String() != "Europe/Berlin" {
					t.Errorf("Schedule = %q, %q in %v", c.Schedule.IncrementalText(), c.Schedule.FullText(), c.Schedule.Loc)
				}
			},
		},
		{
			name: "a POSIX style TZ with a leading colon",
			env:  map[string]string{"BOOKWARDEN_SCHEDULE_INCREMENTAL": "03:30", "BOOKWARDEN_SCHEDULE_FULL": "sun 04:00", "TZ": ":Europe/Berlin"},
			check: func(t *testing.T, c Config) {
				if c.Schedule.Loc.String() != "Europe/Berlin" {
					t.Errorf("zone = %v", c.Schedule.Loc)
				}
			},
		},
		{
			name: "no TZ: the process's local zone",
			env:  map[string]string{"BOOKWARDEN_SCHEDULE_INCREMENTAL": "03:30", "BOOKWARDEN_SCHEDULE_FULL": "sun 04:00"},
			check: func(t *testing.T, c Config) {
				if c.Schedule.Loc == nil {
					t.Error("no zone")
				}
			},
		},
		{
			name:    "an incremental schedule without a full one is a startup error",
			env:     map[string]string{"BOOKWARDEN_SCHEDULE_INCREMENTAL": "03:30"},
			wantErr: []string{"BOOKWARDEN_SCHEDULE_INCREMENTAL", "BOOKWARDEN_SCHEDULE_FULL"},
		},
		{
			name:    "an invalid incremental time is a startup error",
			env:     map[string]string{"BOOKWARDEN_SCHEDULE_INCREMENTAL": "25:00"},
			wantErr: []string{"BOOKWARDEN_SCHEDULE_INCREMENTAL", "25:00"},
		},
		{
			name:    "an invalid full slot is a startup error",
			env:     map[string]string{"BOOKWARDEN_SCHEDULE_FULL": "sunday 4am"},
			wantErr: []string{"BOOKWARDEN_SCHEDULE_FULL", "sunday 4am"},
		},
		{
			name:    "an unknown time zone, with a schedule on",
			env:     map[string]string{"BOOKWARDEN_SCHEDULE_INCREMENTAL": "03:30", "TZ": "Mars/Olympus"},
			wantErr: []string{"TZ", "Mars/Olympus"},
		},
		{
			name: "an unknown time zone is ignored while the scheduler is off",
			env:  map[string]string{"TZ": "Mars/Olympus"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := baseEnv()
			for k, v := range tt.env {
				env[k] = v
			}
			c, err := FromEnv(func(k string) string { return env[k] })
			if len(tt.wantErr) > 0 {
				if err == nil {
					t.Fatal("FromEnv accepted it")
				}
				for _, s := range tt.wantErr {
					if !strings.Contains(err.Error(), s) {
						t.Errorf("error %q lacks %q", err, s)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tt.check != nil {
				tt.check(t, c)
			}
		})
	}
}
