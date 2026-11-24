package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/eval"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/store"
	"github.com/felixapel/calibre-bookwarden/internal/testutil/pgtest"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

func TestEvalRecallCommand(t *testing.T) {
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
	env := map[string]string{"BOOKWARDEN_DATABASE_URL": url}
	call := func(args ...string) (int, string, string) {
		var stdout, stderr bytes.Buffer
		code := run(ctx, append([]string{"eval"}, args...), func(k string) string { return env[k] }, strings.NewReader(""), &stdout, &stderr)
		return code, stdout.String(), stderr.String()
	}

	// No sample yet: a failure, not a usage error.
	if code, _, errOut := call("--recall"); code != 1 || !strings.Contains(errOut, "eval: ") {
		t.Fatalf("no sample: code %d, stderr %q", code, errOut)
	}

	books := []library.BookRef{{ID: 1, UUID: "u1"}, {ID: 2, UUID: "u2"}}
	r, err := store.NewRunRepo(st).CreateRun(ctx, verify.Scope{}, "library", "v1", books)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool.Exec(ctx, `UPDATE runs SET status='completed', finished_at=now() WHERE id=$1`, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool.Exec(ctx, `INSERT INTO seal_checks (run_id, checked_at, books, broken, findings_version, decision_version) VALUES ($1, now(), 0, 0, 1, 1)`, r.ID); err != nil {
		t.Fatal(err)
	}
	repo := store.NewRecallRepo(st)
	id, err := repo.CreateSample(ctx, store.RecallSample{LibraryID: "library", RunID: r.ID, Seed: 3, Size: 2, CreatedBy: "admin",
		Strata: map[string]store.StratumCount{"s": {Library: 2, Sampled: 2}}},
		[]store.SampleBook{{BookID: 1, UUID: "u1", Stratum: "s", Position: 1}, {BookID: 2, UUID: "u2", Stratum: "s", Position: 2}})
	if err != nil {
		t.Fatal(err)
	}
	secret := "Zyxwv Typed Title"
	if err := repo.Confirm(ctx, id, 1, []store.Confirmation{{Field: "title", Answer: store.AnswerWrong, Typed: &secret, By: "admin"}, {Field: "isbn", Answer: store.AnswerRight, By: "admin"}}); err != nil {
		t.Fatal(err)
	}

	// Half answered: progress only, no table.
	code, out, errOut := call("--recall")
	if code != 0 || errOut != "" || !strings.Contains(out, "1 of 2 books answered") || !strings.Contains(out, "withheld") || strings.Contains(out, "false alarms") || strings.Contains(out, "Zyxwv") {
		t.Fatalf("open sample: code %d, stdout %q, stderr %q", code, out, errOut)
	}
	// Complete but never shown on the page: still withheld, and not closed by the CLI.
	if err := repo.Confirm(ctx, id, 2, []store.Confirmation{{Field: "title", Answer: store.AnswerSkip, By: "admin"}}); err != nil {
		t.Fatal(err)
	}
	if code, out, _ = call("--recall"); code != 0 || !strings.Contains(out, "withheld") {
		t.Fatalf("complete, not shown: code %d, stdout %q", code, out)
	}
	if s, err := repo.Sample(ctx, id); err != nil || s.ClosedAt != nil {
		t.Fatalf("the read-only CLI closed the sample: %v, %v", s.ClosedAt, err)
	}
	// The page shows the results and closes the sample; then the CLI prints them.
	if status, err := eval.NewRecallService(repo).Status(ctx, id, true); err != nil || status.Result == nil {
		t.Fatalf("Status = %+v, %v", status, err)
	}
	code, out, errOut = call("--recall")
	if code != 0 || errOut != "" || !strings.Contains(out, fmt.Sprintf("Recall sample %d", id)) || !strings.Contains(out, "2 of 2 books answered") || strings.Contains(out, "Zyxwv") {
		t.Fatalf("recall: code %d, stdout %q, stderr %q", code, out, errOut)
	}
	if code, out2, _ := call("--recall", "--sample", fmt.Sprint(id)); code != 0 || out2 != out {
		t.Fatalf("--sample: code %d, stdout %q", code, out2)
	}
	code, out, _ = call("--recall", "--json")
	var res eval.RecallResult
	if err := json.Unmarshal([]byte(out), &res); err != nil || code != 0 || res.Sample != id || res.Answered != 2 || strings.Contains(out, "Zyxwv") {
		t.Fatalf("json: code %d, err %v, stdout %q", code, err, out)
	}
	if code, _, errOut := call("--recall", "--sample", "9999"); code != 1 || !strings.Contains(errOut, "eval: ") {
		t.Fatalf("missing sample: code %d, stderr %q", code, errOut)
	}
	for _, args := range [][]string{
		{"--recall", "--run", "1"}, {"--recall", "--version", "4"}, {"--recall", "--books"}, {"--recall", "--extraction"}, {"--recall", "extra"},
		{"--recall", "--sample", "-1"}, {"--sample", "1"},
	} {
		if code, _, errOut := call(args...); code != 2 || !strings.Contains(errOut, "bookwarden eval --recall [--sample N] [--json]") {
			t.Fatalf("%v: code %d, stderr %q", args, code, errOut)
		}
	}
}
