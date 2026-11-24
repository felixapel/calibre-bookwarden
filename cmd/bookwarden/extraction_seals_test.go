package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/agent"
	"github.com/felixapel/calibre-bookwarden/internal/decide"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/extraction"
	"github.com/felixapel/calibre-bookwarden/internal/extractor"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/store"
	"github.com/felixapel/calibre-bookwarden/internal/testutil/pgtest"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

// titleSeat answers every excerpt with its first line as the title.
type titleSeat struct{ model string }

func (titleSeat) Name() string    { return "x" }
func (s titleSeat) Model() string { return s.model }
func (s titleSeat) Judge(context.Context, string, string) (agent.Reply, error) {
	return agent.Reply{Text: `{"claims":[{"field":"title","value":"The Lantern Years","quote":"The Lantern Years"}]}`, Model: s.model}, nil
}

// verify-seals re-derives every sealed extraction from the stored excerpt
// and raw answers (M4 §8).
func TestVerifySealsReverifiesExtractions(t *testing.T) {
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
	book := library.BookRef{ID: 1, UUID: "u1"}
	run, err := runs.CreateRun(ctx, verify.Scope{}, "library", "v1", []library.BookRef{book})
	if err != nil {
		t.Fatal(err)
	}
	eb := evidence.NewExcerptBuilder(evidence.ExcerptEPUBText)
	eb.Add(1, evidence.LandmarkTitle, "The Lantern Years\nby Jane Doe")
	ex := eb.Excerpt()
	svc := extraction.New(extraction.Config{
		Extractors: []extractor.Extractor{extractor.NewWithSeat("a", "fa", "m-a", "", titleSeat{"m-a"}), extractor.NewWithSeat("b", "fb", "m-b", "", titleSeat{"m-b"})},
		Hosts:      map[string]string{"a": "h", "b": "h"}, MaxCalls: map[string]int{"a": 1, "b": 1},
	})
	svc.BeginRun(run.ID)
	x, raws, err := svc.Extract(ctx, *ex, ex.Text)
	if err != nil {
		t.Fatal(err)
	}
	sealedEx := *ex
	sealedEx.Text = ""
	p := evidence.Package{
		SchemaVersion: evidence.SchemaVersion, LibraryID: "library", BookID: book.ID, UUID: book.UUID,
		Calibre: evidence.Record{Title: "The Lantern Years", Authors: []string{"Jane Doe"}, Languages: []string{"eng"}, Identifiers: map[string]string{}},
		Formats: []evidence.FormatEvidence{{Format: "EPUB", State: evidence.StateOK, Detected: "epub", Excerpt: &sealedEx, Extraction: x}},
	}
	raw, seal, err := decide.Seal(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	out := verify.Outcome{State: verify.BookChecked, Evidence: raw, Seal: seal, Excerpts: map[string]string{ex.SHA256: ex.Text}, Raws: raws}
	if err := runs.FinishBook(ctx, run.ID, book, out); err != nil {
		t.Fatal(err)
	}
	if err := runs.SetStatus(ctx, run.ID, verify.RunCompleted, ""); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"BOOKWARDEN_DATABASE_URL": url}
	call := func() (int, string) {
		var stdout, stderr bytes.Buffer
		code := run0(ctx, []string{"verify-seals", "--run", fmt.Sprint(run.ID)}, func(k string) string { return env[k] }, &stdout, &stderr)
		return code, stdout.String() + stderr.String()
	}
	if code, out := call(); code != 0 || !strings.Contains(out, fmt.Sprintf("run %d: extractions ok, 1 re-verified\n", run.ID)) {
		t.Fatalf("clean run: code %d, output %q", code, out)
	}
	// The same answer, re-seeded with another claim, no longer derives what
	// was sealed.
	if _, err := st.Pool.Exec(ctx, `UPDATE extraction_raw SET body = '{"claims":[]}'`); err != nil {
		t.Fatal(err)
	}
	if code, out := call(); code != 1 || !strings.Contains(out, fmt.Sprintf("run %d: EXTRACTIONS DAMAGED, 0 differ, 1 missing their text or answers, of 1; first book 1\n", run.ID)) {
		t.Fatalf("altered answer: code %d, output %q", code, out)
	}
}
