package store

import (
	"context"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

func TestMeasuredAtRoundTrips(t *testing.T) {
	ctx := context.Background()
	r, s := runRepo(t)
	ref := refs(1)[0]
	at := time.Date(2026, 11, 2, 4, 0, 0, 123000000, time.UTC)
	run, err := r.CreateRun(ctx, verify.Scope{}, "library", "v1", []library.BookRef{ref})
	if err != nil {
		t.Fatal(err)
	}
	out := sealed(t, ref.ID, ref.UUID)
	out.InspectKey, out.MeasuredAt = "k", at
	if err := r.FinishBook(ctx, run.ID, ref, out); err != nil {
		t.Fatal(err)
	}
	prior, ok, err := r.PriorEvidence(ctx, "library", ref)
	if err != nil || !ok || !prior.MeasuredAt.Equal(at) {
		t.Fatalf("PriorEvidence() = %v, %v, %v; want measured_at %v", prior.MeasuredAt, ok, err, at)
	}
	// A package stored without the time (every one from before it was kept)
	// reads back as zero, which is never reused.
	if _, err := s.Pool.Exec(ctx, `UPDATE evidence SET measured_at = NULL`); err != nil {
		t.Fatal(err)
	}
	prior, ok, err = r.PriorEvidence(ctx, "library", ref)
	if err != nil || !ok || !prior.MeasuredAt.IsZero() {
		t.Errorf("PriorEvidence() = %v, %v, %v; want a zero time", prior.MeasuredAt, ok, err)
	}
}
