package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/decide"
	"github.com/felixapel/calibre-bookwarden/internal/eval"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/store"
	"github.com/felixapel/calibre-bookwarden/internal/testutil/pgtest"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

func TestEvalCommand(t *testing.T) {
	ctx := context.Background()
	url := pgtest.URL(t)
	st, err := store.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	runs := store.NewRunRepo(st)
	books := []library.BookRef{{ID: 1, UUID: "u1"}, {ID: 2, UUID: "u2"}}
	r, err := runs.CreateRun(ctx, verify.Scope{}, "library", "v1", books)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range books {
		title := "Zyxwv Fixture Saga"
		wrong := "Fixture Saga, Zyxwv"
		p := evidence.Package{
			SchemaVersion: evidence.SchemaVersion, LibraryID: "library", BookID: b.ID, UUID: b.UUID,
			Calibre: evidence.Record{Title: title, Authors: []string{"Ada Reed"}, Languages: []string{"eng"}, Identifiers: map[string]string{},
				TitleSort: &title, AuthorSort: "Reed, Ada", AuthorSortMap: map[string]string{"Ada Reed": "Reed, Ada"}},
			Formats: []evidence.FormatEvidence{},
		}
		if b.ID == 1 {
			p.Calibre.TitleSort = &wrong
		}
		raw, seal, err := decide.Seal(ctx, p)
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
	// Only the database URL is set: no Calibre or agent settings.
	env := map[string]string{"BOOKWARDEN_DATABASE_URL": url}
	call := func(args ...string) (int, string, string) {
		var stdout, stderr bytes.Buffer
		code := run(ctx, append([]string{"eval"}, args...), func(k string) string { return env[k] }, strings.NewReader(""), &stdout, &stderr)
		return code, stdout.String(), stderr.String()
	}
	id := fmt.Sprint(r.ID)

	code, out, errOut := call("--run", id, "--version", "4", "--baseline", "3")
	if code != 0 || errOut != "" || !strings.Contains(out, "books compared: 2") || !strings.Contains(out, "title_sort") || strings.Contains(out, "changed books") {
		t.Fatalf("summary: code %d, stdout %q, stderr %q", code, out, errOut)
	}
	code, out, _ = call("--run", id, "--version", "4", "--baseline", "3", "--books")
	if code != 0 || !strings.Contains(out, "book 1: ") || strings.Contains(out, "book 2: ") || strings.Contains(out, "Zyxwv") || strings.Contains(out, "Fixture") {
		t.Fatalf("books: code %d, stdout %q", code, out)
	}
	code, out, _ = call("--run", id, "--version", "4", "--baseline", "3", "--json")
	var res eval.Result
	if err := json.Unmarshal([]byte(out), &res); err != nil || code != 0 || res.Compared != 2 || res.Fields["title_sort"].Added != 1 || strings.Contains(out, "Zyxwv") {
		t.Fatalf("json: code %d, err %v, stdout %q", code, err, out)
	}
	if code, out, _ = call("--run", id, "--version", fmt.Sprint(decide.Version)); code != 0 || strings.Contains(out, "broken books") {
		t.Fatalf("same version: code %d, stdout %q", code, out)
	}
	// A broken seal is reported and decides the exit code.
	if _, err := st.Pool.Exec(ctx, `UPDATE evidence SET package=jsonb_set(package,'{calibre_record,title}','"Edited"') WHERE run_id=$1 AND book_id=2`, r.ID); err != nil {
		t.Fatal(err)
	}
	code, out, _ = call("--run", id, "--version", "4", "--baseline", "3")
	if code != 1 || !strings.Contains(out, "books with a broken seal: 1") || !strings.Contains(out, "books compared: 1") || !strings.Contains(out, "broken books: 2") {
		t.Fatalf("broken seal: code %d, stdout %q", code, out)
	}
	if code, _, errOut = call("--run", "9999", "--version", "4"); code != 1 || !strings.Contains(errOut, "eval: ") {
		t.Fatalf("missing run: code %d, stderr %q", code, errOut)
	}
	const usage = "usage: bookwarden eval --run N --version V [--baseline B] [--books] [--json]"
	for _, args := range [][]string{
		{}, {"--run", id}, {"--version", "4"}, {"--run", id, "--version", "0"}, {"--run", id, "--version", fmt.Sprint(decide.Version + 1)},
		{"--run", id, "--version", "4", "--baseline", fmt.Sprint(decide.Version + 1)}, {"--run", id, "--version", "4", "--baseline", "-1"},
		{"--run", id, "--version", "4", "extra"},
	} {
		if code, _, errOut := call(args...); code != 2 || !strings.Contains(errOut, usage) {
			t.Fatalf("%v: code %d, stderr %q", args, code, errOut)
		}
	}
	// The extraction report: these packages have none.
	code, out, errOut = call("--run", id, "--extraction")
	if code != 1 || errOut != "" || !strings.Contains(out, "shadow extraction of 0 formats") || !strings.Contains(out, "broken books: 2") {
		t.Fatalf("extraction: code %d, stdout %q, stderr %q", code, out, errOut)
	}
	code, out, _ = call("--run", id, "--extraction", "--json")
	var xr eval.ExtractionReport
	if err := json.Unmarshal([]byte(out), &xr); err != nil || code != 1 || xr.Broken != 1 || strings.Contains(out, "Zyxwv") {
		t.Fatalf("extraction json: code %d, err %v, stdout %q", code, err, out)
	}
	// --reverify: no extraction in these packages, so nothing to re-derive;
	// the broken seal still makes the exit code 1.
	code, out, errOut = call("--run", id, "--extraction", "--reverify")
	if code != 1 || errOut != "" || !strings.Contains(out, "re-verification of 0 formats") || !strings.Contains(out, "broken books: 2") {
		t.Fatalf("reverify: code %d, stdout %q, stderr %q", code, out, errOut)
	}
	for _, args := range [][]string{
		{"--extraction"}, {"--run", id, "--extraction", "--version", "4"}, {"--run", id, "--extraction", "--baseline", "3"}, {"--run", id, "--extraction", "--books"},
		{"--run", id, "--reverify"}, {"--run", id, "--version", "4", "--reverify"},
	} {
		if code, _, errOut := call(args...); code != 2 || !strings.Contains(errOut, "bookwarden eval --run N --extraction [--reverify] [--json]") {
			t.Fatalf("%v: code %d, stderr %q", args, code, errOut)
		}
	}
	// It never wrote: the run is as the test left it.
	var status string
	if err := st.Pool.QueryRow(ctx, `SELECT status FROM runs WHERE id=$1`, r.ID).Scan(&status); err != nil || status != string(verify.RunCompleted) {
		t.Fatalf("run status %q, %v", status, err)
	}
}
