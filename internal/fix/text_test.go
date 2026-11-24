package fix

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/decide"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/store"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

// textRun stores a run of book 2 whose EPUB has an extraction that agrees on
// the title, so the title fix has the text among its roots (ADR-036).
func textRun(t *testing.T, e *env) int64 {
	t.Helper()
	ctx := context.Background()
	p := pkgFor(t, e.cal.book(2), sourceSays{})
	b := evidence.NewExcerptBuilder(evidence.ExcerptEPUBText)
	b.Add(1, evidence.LandmarkTitle, strings.Repeat("x", 100))
	ex := b.Excerpt()
	body := ex.Text
	ex.Text = ""
	hash := func(s string) string { sum := sha256.Sum256([]byte(s)); return hex.EncodeToString(sum[:]) }
	rawA, rawB := `{"claims":[]}`, `{"claims":[ ]}`
	voices := []string{"glm", "luna"}
	x := &evidence.Extraction{
		PromptVersion: "extract-2", ExtractVersion: 2, ExcerptSHA256: ex.SHA256,
		Extractors: []evidence.ExtractorRun{
			{Name: "luna", Family: "a", Model: "m", State: evidence.ExtractOK, RawSHA256: hash(rawA)},
			{Name: "glm", Family: "b", Model: "m", State: evidence.ExtractOK, RawSHA256: hash(rawB)},
		},
		Agreed: []evidence.AgreedFact{{Key: "title", Outcome: evidence.AgreeAgreed, Values: []evidence.AgreedValue{{Value: "Lighthouse Signals"}}, Voices: voices}},
	}
	for _, v := range voices {
		x.Claims = append(x.Claims, evidence.ExtractedClaim{Field: "title", Value: "Lighthouse Signals", Source: v, Status: "grounded", Unit: 1, Start: 0, End: 1})
	}
	p.Formats[0].Excerpt, p.Formats[0].Extraction = ex, x
	ref := library.BookRef{ID: p.BookID, UUID: p.UUID}
	run, err := e.runs.CreateRun(ctx, verify.Scope{}, "library", "v1", []library.BookRef{ref})
	if err != nil {
		t.Fatal(err)
	}
	raw, seal, err := decide.Seal(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	out := verify.Outcome{State: verify.BookChecked, Evidence: raw, Seal: seal, Excerpts: map[string]string{ex.SHA256: body},
		Raws: []verify.RawOutput{{SHA256: hash(rawA), Body: rawA}, {SHA256: hash(rawB), Body: rawB}}}
	if err := e.runs.FinishBook(ctx, run.ID, ref, out); err != nil {
		t.Fatal(err)
	}
	if err := e.runs.SetStatus(ctx, run.ID, verify.RunCompleted, ""); err != nil {
		t.Fatal(err)
	}
	return run.ID
}

// A fix that rests on the text is approved and applied only when the check
// passes; any other fix never asks.
func TestTextFixesNeedTheTextCheck(t *testing.T) {
	ctx := context.Background()
	unverified := errors.New("differs")
	for _, tt := range []struct {
		name    string
		check   TextCheck
		fields  []string
		wantErr error
	}{
		{"no check is set", nil, []string{"title"}, ErrTextUnverified},
		{"the check refuses", func(context.Context, int64, int) error { return unverified }, []string{"title"}, unverified},
		{"the check passes", func(context.Context, int64, int) error { return nil }, []string{"title"}, nil},
		{"a fix without the text never asks", func(context.Context, int64, int) error { return unverified }, []string{"languages"}, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := setup(t)
			run := textRun(t, e)
			e.svc.SetTextCheck(tt.check)
			err := e.svc.Approve(ctx, run, 2, tt.fields, "felix", store.Basis{Mode: store.DecisionBulk})
			if !errors.Is(err, tt.wantErr) && (tt.wantErr != nil || err != nil) {
				t.Fatalf("Approve = %v, want %v", err, tt.wantErr)
			}
			if got := e.statuses(run, 2)[tt.fields[0]]; (tt.wantErr == nil) != (got == store.ProposalApproved) {
				t.Errorf("status %q after %v", got, err)
			}
			// Rejecting never needs the text.
			if err := e.svc.Reject(ctx, run, 2, []string{"authors"}, "felix", store.Basis{Mode: store.DecisionBulk}); err != nil {
				t.Errorf("Reject: %v", err)
			}
		})
	}
}

func TestTextFixesAreCheckedAgainWhenApplied(t *testing.T) {
	ctx := context.Background()
	e := setup(t)
	run := textRun(t, e)
	e.svc.SetTextCheck(func(context.Context, int64, int) error { return nil })
	e.approve(run, 2, "title")
	// The raw answer is altered after the approval: Apply refuses.
	var calls int
	e.svc.SetTextCheck(func(context.Context, int64, int) error { calls++; return ErrTextUnverified })
	sum, err := e.svc.Apply(ctx, "felix")
	if err != nil || sum != (Summary{Blocked: 1}) || calls != 1 {
		t.Fatalf("Apply = %+v, %v after %d checks", sum, err, calls)
	}
	if e.cal.setCount() != 0 || e.cal.book(2).Title != "Incorrect Catalogue Title" {
		t.Fatalf("Calibre was written: %+v", e.cal.book(2))
	}
	if got := e.statuses(run, 2)["title"]; got != "blocked/text_unverified" {
		t.Fatalf("title is %q", got)
	}
}

// A check that couldn't run leaves the approval for the next apply.
func TestTextCheckFailureLeavesApproval(t *testing.T) {
	ctx := context.Background()
	e := setup(t)
	run := textRun(t, e)
	e.svc.SetTextCheck(func(context.Context, int64, int) error { return nil })
	e.approve(run, 2, "title")
	down := errors.New("database down")
	e.svc.SetTextCheck(func(context.Context, int64, int) error { return down })
	if _, err := e.svc.Apply(ctx, "felix"); !errors.Is(err, down) {
		t.Fatalf("Apply = %v", err)
	}
	if got := e.statuses(run, 2)["title"]; got != store.ProposalApproved {
		t.Fatalf("title is %q", got)
	}
}

// A failed text check at apply blocks only the fields that rest on the text,
// with a reason that tells an older verifier from a mismatch; the book's
// other approved fields still apply.
func TestTextCheckBlocksOnlyTextFields(t *testing.T) {
	ctx := context.Background()
	for _, tt := range []struct {
		name string
		err  error
		want string
	}{
		{"the text differs", ErrTextUnverified, "blocked/text_unverified"},
		{"an older verifier", ErrTextOldVerifier, "blocked/text_old_verifier"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := setup(t)
			run := textRun(t, e)
			e.svc.SetTextCheck(func(context.Context, int64, int) error { return nil })
			e.approve(run, 2, "title", "languages")
			e.svc.SetTextCheck(func(context.Context, int64, int) error { return tt.err })
			sum, err := e.svc.Apply(ctx, "felix")
			if err != nil || sum != (Summary{Applied: 1, Blocked: 1}) {
				t.Fatalf("Apply = %+v, %v", sum, err)
			}
			st := e.statuses(run, 2)
			if st["title"] != tt.want || st["languages"] != store.ProposalApplied {
				t.Fatalf("statuses %v", st)
			}
			if e.cal.book(2).Title != "Incorrect Catalogue Title" {
				t.Fatalf("the title was written: %q", e.cal.book(2).Title)
			}
		})
	}
}
