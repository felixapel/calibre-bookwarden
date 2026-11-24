package eval_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/felixapel/calibre-bookwarden/internal/decide"
	"github.com/felixapel/calibre-bookwarden/internal/eval"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/store"
	"github.com/felixapel/calibre-bookwarden/internal/testutil/pgtest"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

const secretTitle = "Zyxwv Fixture Saga"

func sortPtr(s string) *string { return &s }

// pkg is a book with a title that appears nowhere in the output. With
// wrongSort its title sort breaks Calibre's own rule, which decision version
// 4 proposes to fix and version 3 does not (ADR-028).
func pkg(b library.BookRef, wrongSort bool) evidence.Package {
	rec := evidence.Record{
		Title: secretTitle, Authors: []string{"Ada Reed"}, Languages: []string{"eng"}, Identifiers: map[string]string{},
		TitleSort: sortPtr("Zyxwv Fixture Saga"), AuthorSort: "Reed, Ada", AuthorSortMap: map[string]string{"Ada Reed": "Reed, Ada"},
	}
	if wrongSort {
		rec.TitleSort = sortPtr("Fixture Saga, Zyxwv")
	}
	return evidence.Package{
		SchemaVersion: evidence.SchemaVersion, LibraryID: "library", BookID: b.ID, UUID: b.UUID,
		Calibre: rec, Formats: []evidence.FormatEvidence{},
	}
}

// seed finishes a run of three books: 1 has the wrong sort, 2 and 3 are clean.
func seed(ctx context.Context, t *testing.T) (*store.Store, int64, string) {
	t.Helper()
	url := pgtest.URL(t)
	st, err := store.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	runs := store.NewRunRepo(st)
	books := []library.BookRef{{ID: 1, UUID: "u1"}, {ID: 2, UUID: "u2"}, {ID: 3, UUID: "u3"}}
	r, err := runs.CreateRun(ctx, verify.Scope{}, "library", "v1", books)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range books {
		raw, seal, err := decide.Seal(ctx, pkg(b, b.ID == 1))
		if err != nil {
			t.Fatal(err)
		}
		if err := runs.FinishBook(ctx, r.ID, b, verify.Outcome{State: verify.BookChecked, Evidence: raw, Seal: seal}); err != nil {
			t.Fatal(err)
		}
	}
	if err := runs.SetStatus(ctx, r.ID, verify.RunCompleted, ""); err != nil {
		t.Fatal(err)
	}
	return st, r.ID, url
}

func TestReplaySameVersionChangesNothing(t *testing.T) {
	ctx := context.Background()
	st, run, _ := seed(ctx, t)
	for _, o := range []eval.Options{
		{RunID: run, Version: decide.Version},
		{RunID: run, Version: decide.Version, Baseline: decide.Version},
	} {
		t.Run(fmt.Sprintf("baseline %d", o.Baseline), func(t *testing.T) {
			res, err := eval.Replay(ctx, store.NewReportRepo(st), o)
			if err != nil {
				t.Fatal(err)
			}
			if res.Compared != 3 || res.Broken != 0 || len(res.Books) != 0 {
				t.Fatalf("compared %d, broken %d, changed books %v", res.Compared, res.Broken, res.Books)
			}
			for from, row := range res.Matrix {
				for to, n := range row {
					if from != to && n != 0 {
						t.Fatalf("matrix %s->%s: %d", from, to, n)
					}
				}
			}
			for f, d := range res.Fields {
				if d.Added+d.Removed+d.Changed != 0 || d.Baseline != d.Version {
					t.Fatalf("field %s: %+v", f, d)
				}
			}
			for c, d := range res.Reasons {
				t.Fatalf("reason %s: %+v", c, d)
			}
		})
	}
}

func TestReplayOlderBaseline(t *testing.T) {
	ctx := context.Background()
	st, run, _ := seed(ctx, t)
	res, err := eval.Replay(ctx, store.NewReportRepo(st), eval.Options{RunID: run, Version: 4, Baseline: 3})
	if err != nil {
		t.Fatal(err)
	}
	if res.Compared != 3 || res.Broken != 0 {
		t.Fatalf("compared %d, broken %d", res.Compared, res.Broken)
	}
	if got := res.Fields["title_sort"]; got != (eval.FieldDelta{Version: 1, Added: 1}) {
		t.Fatalf("title_sort: %+v", got)
	}
	if len(res.Books) != 1 || res.Books[0].Book != 1 || len(res.Books[0].Added) != 1 || res.Books[0].Added[0] != "title_sort" {
		t.Fatalf("changed books: %+v", res.Books)
	}
	total := 0
	for _, row := range res.Matrix {
		for _, n := range row {
			total += n
		}
	}
	b := res.Books[0]
	if total != 3 || res.Matrix[b.From][b.To] == 0 {
		t.Fatalf("matrix %v for %+v", res.Matrix, b)
	}
	want := eval.FixType{Field: "title_sort", Path: evidence.RootCalibreRule, Kind: "replace", Count: 1}
	if len(res.FixTypes) != 1 || res.FixTypes[0] != want {
		t.Fatalf("fix types: %+v, want %+v", res.FixTypes, want)
	}
	// The same fixture the other way round loses the proposal.
	back, err := eval.Replay(ctx, store.NewReportRepo(st), eval.Options{RunID: run, Version: 3, Baseline: 4})
	if err != nil {
		t.Fatal(err)
	}
	if got := back.Fields["title_sort"]; got != (eval.FieldDelta{Baseline: 1, Removed: 1}) {
		t.Fatalf("title_sort backwards: %+v", got)
	}
}

func TestReplayBrokenSeal(t *testing.T) {
	ctx := context.Background()
	st, run, _ := seed(ctx, t)
	if _, err := st.Pool.Exec(ctx, `UPDATE evidence SET package=jsonb_set(package,'{calibre_record,title}','"Edited"') WHERE run_id=$1 AND book_id=3`, run); err != nil {
		t.Fatal(err)
	}
	res, err := eval.Replay(ctx, store.NewReportRepo(st), eval.Options{RunID: run, Version: 4, Baseline: 3})
	if err != nil {
		t.Fatal(err)
	}
	if res.Compared != 2 || res.Broken != 1 || len(res.BrokenBooks) != 1 || res.BrokenBooks[0] != 3 {
		t.Fatalf("compared %d, broken %d %v", res.Compared, res.Broken, res.BrokenBooks)
	}
	var out strings.Builder
	res.WriteText(&out, false)
	if !strings.Contains(out.String(), "books with a broken seal: 1") {
		t.Fatalf("summary: %s", out.String())
	}
}

func TestReplayOutputHasNoLibraryText(t *testing.T) {
	ctx := context.Background()
	st, run, _ := seed(ctx, t)
	res, err := eval.Replay(ctx, store.NewReportRepo(st), eval.Options{RunID: run, Version: 4, Baseline: 3})
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	res.WriteText(&text, true)
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text.String(), "book 1: ") {
		t.Fatalf("the book line is missing: %s", text.String())
	}
	for _, out := range []string{text.String(), string(raw)} {
		for _, leak := range []string{secretTitle, "Zyxwv", "Fixture", "Ada Reed", "Reed"} {
			if strings.Contains(out, leak) {
				t.Fatalf("output leaks %q: %s", leak, out)
			}
		}
	}
}

func TestOptionsValidate(t *testing.T) {
	for _, tt := range []struct {
		o  eval.Options
		ok bool
	}{
		{eval.Options{Version: 1}, true},
		{eval.Options{Version: decide.Version, Baseline: 1}, true},
		{eval.Options{Version: 0}, false},
		{eval.Options{Version: -1}, false},
		{eval.Options{Version: decide.Version + 1}, false},
		{eval.Options{Version: 1, Baseline: decide.Version + 1}, false},
		{eval.Options{Version: 1, Baseline: -1}, false},
	} {
		if err := tt.o.Validate(); (err == nil) != tt.ok {
			t.Errorf("%+v: %v", tt.o, err)
		}
	}
}

func TestReadOnlyStore(t *testing.T) {
	ctx := context.Background()
	_, run, url := seed(ctx, t)
	ro, err := store.OpenReadOnly(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if _, err := eval.Replay(ctx, store.NewReportRepo(ro), eval.Options{RunID: run, Version: 4, Baseline: 3}); err != nil {
		t.Fatal(err)
	}
	// The connection itself is read-only.
	_, err = ro.Pool.Exec(ctx, `DELETE FROM runs`)
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); !ok || pgErr.Code != "25006" {
		t.Fatalf("a write on the connection: %v", err)
	}
}

func TestReplayJSONWithoutBrokenSeals(t *testing.T) {
	ctx := context.Background()
	st, run, _ := seed(ctx, t)
	res, err := eval.Replay(ctx, store.NewReportRepo(st), eval.Options{RunID: run, Version: 4, Baseline: 3})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "broken_books") {
		t.Errorf("no broken seal, yet broken_books is in %s", raw)
	}
}
