package store

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/report"
)

var foldBooks = []listBook{
	{10, "Søren Kierkegaard: Frygt og Bæven", []string{"Ørsted"}, "A", prop("title")},
	{20, "Die Straße", []string{"Ärztin Müller"}, "A", prop("title")},
	{30, "Łódź", []string{"Đorđe Marković"}, "A", prop("title")},
	{40, "Cœur et Æon", []string{"José Saramago"}, "A", prop("title")},
	{50, "Plain", []string{"Ada Reed"}, "A", prop("title")},
	{60, "Line", []string{"Break"}, "A", prop("title")},
}

var foldQueries = []struct {
	q    string
	want []int
}{
	{"soren", []int{10}},
	{"SØREN", []int{10}},
	{"ørsted", []int{10}},
	{"orsted", []int{10}},
	{"ØRSTED", []int{10}},
	{"baeven", []int{10}},
	{"strasse", []int{20}},
	{"STRASSE", []int{20}},
	{"straße", []int{20}},
	{"arztin", []int{20}},
	{"lodz", []int{30}},
	{"dorde", []int{30}},
	{"marković", []int{30}},
	{"coeur", []int{40}},
	{"aeon", []int{40}},
	{"jose", []int{40}},
	{"  jose   sara ", []int{40}},
	{"line break", nil}, // never across the title and the author
	{"e\nb", nil},
	{"zzz", nil},
}

// The projection searches the text folded when the run was checked. The same
// queries are answered by a page walk in the report package, which folds with
// the same report.Fold.
func TestFixListSearchFolds(t *testing.T) {
	ctx := context.Background()
	e := newCarryEnv(t)
	run := listRun(t, e, foldBooks)
	svc := report.New(NewReportRepo(e.s))
	for _, tc := range foldQueries {
		p, err := svc.Fixes(ctx, run, report.FixFilter{Query: tc.q}, nil)
		if err != nil {
			t.Fatal(tc.q, err)
		}
		got := ids(p)
		slices.Sort(got)
		if !slices.Equal(got, tc.want) {
			t.Errorf("q=%q: books %v, want %v", tc.q, got, tc.want)
		}
	}
}

// A projection written before search_text existed has none. Migration 0018
// marks every existing check stale, so such a run is walked, with the same
// results, until the sweep checks it again.
func TestFixListSearchOfARunProjectedBeforeSearchText(t *testing.T) {
	ctx := context.Background()
	e := newCarryEnv(t)
	run := listRun(t, e, foldBooks)
	svc := report.New(NewReportRepo(e.s))
	for _, q := range []string{`UPDATE book_projection SET search_text = NULL WHERE run_id = $1`, `UPDATE seal_checks SET checked_at = '-infinity' WHERE run_id = $1`} {
		if _, err := e.s.Pool.Exec(ctx, q, run); err != nil {
			t.Fatal(err)
		}
	}
	// The stale check sends the page to the walk, which never reads search_text.
	if _, err := svc.Fixes(ctx, run, report.FixFilter{Query: "STRASSE"}, nil); err != nil {
		t.Fatal(err)
	}
	due, err := NewReportRepo(e.s).RunsToCheck(ctx, time.Now().Add(-24*time.Hour))
	if err != nil || !slices.Contains(due, run) {
		t.Fatalf("the run is not due a check: %v, %v", due, err)
	}
}
