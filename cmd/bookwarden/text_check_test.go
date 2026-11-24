package main

import (
	"context"
	"errors"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/decide"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/extract"
	"github.com/felixapel/calibre-bookwarden/internal/extraction"
	"github.com/felixapel/calibre-bookwarden/internal/extractor"
	"github.com/felixapel/calibre-bookwarden/internal/fix"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/store"
	"github.com/felixapel/calibre-bookwarden/internal/testutil/pgtest"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

// A title fix that rests on the printed text is approved while its extraction
// re-verifies, and refused once a stored raw answer was changed (ADR-036
// section 5), with the same check verify-seals runs.
func TestApproveReverifiesTheText(t *testing.T) {
	ctx, st, fixer, run, book, raw, seal := textRun(t, nil)
	_ = st
	approve := func() error {
		return fixer.Approve(ctx, run, book, []string{"title"}, "felix", store.Basis{Mode: store.DecisionBulk})
	}
	if _, d, err := decide.Open(ctx, raw, seal); err != nil || len(d.Proposals) != 1 || d.Proposals[0].Roots[0] != evidence.RootText {
		t.Fatalf("the fixture does not propose from the text: %+v, %v", d, err)
	}
	if err := approve(); err != nil {
		t.Fatalf("Approve with the stored answers = %v", err)
	}
	// An answer changed after sealing no longer derives the sealed claims.
	if _, err := st.Pool.Exec(ctx, `UPDATE extraction_raw SET body = '{"claims":[]}'`); err != nil {
		t.Fatal(err)
	}
	err := approve()
	if !errors.Is(err, fix.ErrTextUnverified) || errors.Is(err, fix.ErrTextOldVerifier) {
		t.Fatalf("Approve with an altered answer = %v, want ErrTextUnverified only", err)
	}
}

// An extraction sealed under other verifier rules gets its own refusal: the
// book needs a new check, nothing was tampered with.
func TestApproveRefusesAnOlderVerifier(t *testing.T) {
	ctx, _, fixer, run, book, _, _ := textRun(t, func(x *evidence.Extraction) { x.ExtractVersion = extract.Version + 1 })
	err := fixer.Approve(ctx, run, book, []string{"title"}, "felix", store.Basis{Mode: store.DecisionBulk})
	if !errors.Is(err, fix.ErrTextOldVerifier) || !errors.Is(err, fix.ErrTextUnverified) {
		t.Fatalf("Approve = %v, want ErrTextOldVerifier", err)
	}
}

// textRun stores a finished run of one book whose text alone proposes the
// title, after mutate changed the sealed extraction.
func textRun(t *testing.T, mutate func(*evidence.Extraction)) (context.Context, *store.Store, *fix.Service, int64, int, []byte, string) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, pgtest.URL(t))
	if err != nil {
		t.Fatal(err)
	}
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
	if mutate != nil {
		mutate(x)
	}
	sealedEx := *ex
	sealedEx.Text = ""
	// Calibre's title is junk, so the text alone proposes the printed one.
	p := evidence.Package{
		SchemaVersion: evidence.SchemaVersion, LibraryID: "library", BookID: book.ID, UUID: book.UUID,
		Calibre: evidence.Record{Title: "scan.pdf", Authors: []string{"Jane Doe"}, Languages: []string{"eng"}, Identifiers: map[string]string{}},
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
	fixer := fix.New(store.NewFixRepo(st), runs, nil, nil)
	fixer.SetTextCheck(textCheck(store.NewReportRepo(st)))
	t.Cleanup(st.Close)
	return ctx, st, fixer, run.ID, book.ID, raw, seal
}
