package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/decide"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/report"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

// TestReportDecisionIntegrity: a stored decision that no longer matches its
// evidence, with an intact seal, is an integrity error, and FinishBook
// refuses such a package.
func TestReportDecisionIntegrity(t *testing.T) {
	ctx := context.Background()
	r, s := runRepo(t)
	books := refs(2)
	run, err := r.CreateRun(ctx, verify.Scope{}, "library", "v1", books)
	if err != nil {
		t.Fatal(err)
	}
	sealed := func(i int) ([]byte, string) {
		raw, seal, err := decide.Seal(ctx, cleanEvidence(books[i]))
		if err != nil {
			t.Fatal(err)
		}
		return raw, seal
	}
	tamper := func(raw []byte) ([]byte, string) {
		var p evidence.Package
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatal(err)
		}
		p.Tier, p.TierReasons = evidence.TierA, nil
		out, seal, err := evidence.Seal(p)
		if err != nil {
			t.Fatal(err)
		}
		return out, seal
	}
	raw, seal := sealed(0)
	if err := r.FinishBook(ctx, run.ID, books[0], verify.Outcome{State: verify.BookChecked, Evidence: raw, Seal: seal}); err != nil {
		t.Fatal(err)
	}
	reports := report.New(NewReportRepo(s))
	b, err := reports.Book(ctx, run.ID, books[0].ID)
	if err != nil || b.Decision.Tier != evidence.TierB || b.Package.Tier != evidence.TierB {
		t.Fatalf("Book = %+v, %v", b.Decision, err)
	}
	bad, badSeal := tamper(raw)
	if _, err := s.Pool.Exec(ctx, `UPDATE evidence SET package=$1::jsonb, seal=$2 WHERE run_id=$3 AND book_id=$4`, string(bad), badSeal, run.ID, books[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := reports.Book(ctx, run.ID, books[0].ID); !errors.Is(err, report.ErrIntegrity) {
		t.Fatalf("tampered decision: %v", err)
	}
	raw, _ = sealed(1)
	bad, badSeal = tamper(raw)
	if err := r.FinishBook(ctx, run.ID, books[1], verify.Outcome{State: verify.BookChecked, Evidence: bad, Seal: badSeal}); err == nil {
		t.Fatal("FinishBook stored a package whose decision doesn't match")
	}
	assertPendingWithoutEvidence(t, s, run.ID, books[1].ID)
}
