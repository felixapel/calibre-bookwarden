package metrics_test

import (
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/metrics"
)

func TestWriteSnapshot(t *testing.T) {
	snap := metrics.Snapshot{
		RunsByStatus:     map[string]int{"completed": 4, "failed": 1},
		LastRun:          &metrics.LastRun{DurationSeconds: 93.5, Kind: "incremental", Books: map[string]int{"checked": 980, "failed": 2}},
		ProposalsOpen:    map[string]int{"title": 12, "isbn": 3},
		FixesApplied:     40,
		FixesUndone:      2,
		IncidentsOpen:    1,
		SealChecksBroken: 0,
		Labels: []metrics.LabelCount{
			{Field: "title", Kind: "replace", Labels: 120, Misses: 1},
			{Field: "publisher", Kind: "fill", Labels: 7, Misses: 0},
		},
	}
	var sb strings.Builder
	w := metrics.NewWriter(&sb)
	w.BuildInfo("v1.2.3")
	metrics.WriteSnapshot(w, snap)
	if w.Err() != nil {
		t.Fatal(w.Err())
	}
	want := []string{
		`bookwarden_build_info{version="v1.2.3"} 1`,
		// Every status has a series, so a rule on a missing one doesn't go silent.
		`bookwarden_runs{status="completed"} 4`,
		`bookwarden_runs{status="failed"} 1`,
		`bookwarden_runs{status="cancelled"} 0`,
		`bookwarden_runs{status="running"} 0`,
		`bookwarden_runs{status="waiting"} 0`,
		`bookwarden_run_duration_seconds{kind="incremental"} 93.5`,
		`bookwarden_run_books{state="checked"} 980`,
		`bookwarden_run_books{state="failed"} 2`,
		`bookwarden_run_books{state="pending"} 0`,
		`bookwarden_run_books{state="source_changed"} 0`,
		`bookwarden_proposals_open{field="isbn"} 3`,
		`bookwarden_proposals_open{field="title"} 12`,
		`bookwarden_fixes_applied_total 40`,
		`bookwarden_fixes_undone_total 2`,
		`bookwarden_incidents_open 1`,
		`bookwarden_seal_checks_broken 0`,
		`bookwarden_labels{field="publisher",kind="fill"} 7`,
		`bookwarden_labels{field="title",kind="replace"} 120`,
		`bookwarden_label_misses{field="publisher",kind="fill"} 0`,
		`bookwarden_label_misses{field="title",kind="replace"} 1`,
		"# TYPE bookwarden_build_info gauge",
		"# TYPE bookwarden_runs gauge",
		"# TYPE bookwarden_run_duration_seconds gauge",
		"# TYPE bookwarden_fixes_applied_total counter",
		"# TYPE bookwarden_fixes_undone_total counter",
		"# TYPE bookwarden_incidents_open gauge",
		"# TYPE bookwarden_seal_checks_broken gauge",
		"# TYPE bookwarden_labels gauge",
	}
	for _, line := range want {
		if !strings.Contains(sb.String(), line+"\n") {
			t.Errorf("missing %q in:\n%s", line, sb.String())
		}
	}
}

func TestWriteSnapshotWithoutAFinishedRun(t *testing.T) {
	var sb strings.Builder
	w := metrics.NewWriter(&sb)
	metrics.WriteSnapshot(w, metrics.Snapshot{})
	out := sb.String()
	for _, absent := range []string{"bookwarden_run_duration_seconds ", "bookwarden_run_books{"} {
		if strings.Contains(out, absent) {
			t.Errorf("%q written without a finished run:\n%s", absent, out)
		}
	}
	if !strings.Contains(out, "bookwarden_incidents_open 0\n") {
		t.Errorf("zero gauges are missing:\n%s", out)
	}
}
