package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/decide"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/store"
	"github.com/felixapel/calibre-bookwarden/internal/testutil/pgtest"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

// verify-seals also checks that every excerpt a package refers to exists and
// matches its hash (ADR-032 part 2).
func TestVerifySealsChecksExcerpts(t *testing.T) {
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
	run, err := runs.CreateRun(ctx, verify.Scope{}, "library", "v1", books)
	if err != nil {
		t.Fatal(err)
	}
	var hashes []string
	for i, b := range books {
		eb := evidence.NewExcerptBuilder(evidence.ExcerptEPUBText)
		eb.Add(1, evidence.LandmarkFront, fmt.Sprintf("Synthetic front matter %d", i))
		ex := eb.Excerpt()
		hashes = append(hashes, ex.SHA256)
		p := evidence.Package{
			SchemaVersion: evidence.SchemaVersion, LibraryID: "library", BookID: b.ID, UUID: b.UUID,
			Calibre: evidence.Record{Title: "Binary Tides", Authors: []string{"Ada Reed"}, Languages: []string{"eng"}, Identifiers: map[string]string{}},
			Formats: []evidence.FormatEvidence{{Format: "EPUB", State: evidence.StateOK, Detected: "epub", Excerpt: ex}},
		}
		raw, seal, err := decide.Seal(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		out := verify.Outcome{State: verify.BookChecked, Evidence: raw, Seal: seal, Excerpts: map[string]string{ex.SHA256: ex.Text}}
		if err := runs.FinishBook(ctx, run.ID, b, out); err != nil {
			t.Fatal(err)
		}
	}
	if err := runs.SetStatus(ctx, run.ID, verify.RunCompleted, ""); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"BOOKWARDEN_DATABASE_URL": url}
	call := func() (int, string, string) {
		var stdout, stderr bytes.Buffer
		code := run0(ctx, []string{"verify-seals", "--run", fmt.Sprint(run.ID)}, func(k string) string { return env[k] }, &stdout, &stderr)
		return code, stdout.String(), stderr.String()
	}
	code, out, errOut := call()
	want := fmt.Sprintf("run %d: ok, 2 books\nrun %d: excerpts ok, 2 referenced\n", run.ID, run.ID)
	if code != 0 || out != want {
		t.Fatalf("clean run: code %d, stdout %q, stderr %q", code, out, errOut)
	}
	if _, err := st.Pool.Exec(ctx, `DELETE FROM excerpts WHERE sha256=$1`, hashes[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool.Exec(ctx, `UPDATE excerpts SET body='tampered' WHERE sha256=$1`, hashes[1]); err != nil {
		t.Fatal(err)
	}
	code, out, _ = call()
	if code != 1 || !strings.Contains(out, fmt.Sprintf("run %d: EXCERPTS DAMAGED, 1 missing, 1 corrupt of 2 referenced\n", run.ID)) {
		t.Fatalf("damaged excerpts: code %d, stdout %q", code, out)
	}
	if strings.Contains(out, "Synthetic") || strings.Contains(out, "tampered") {
		t.Errorf("the output carries excerpt text: %q", out)
	}
}

// run0 calls run with no stdin.
func run0(ctx context.Context, args []string, getenv func(string) string, stdout, stderr *bytes.Buffer) int {
	return run(ctx, args, getenv, strings.NewReader(""), stdout, stderr)
}
