package store

import (
	"context"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/findings"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/report"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

// TestVersionFiveProjectionReplays: a schema 4 package stored under findings
// version 5 is still read by a version-6 binary. Its stored rows match the v5
// derivation, a seal check passes, and the stored projection version stays 5.
// The migration that allows projection version 6 is what lets a version-6
// binary write its own rows.
func TestVersionFiveProjectionReplays(t *testing.T) {
	ctx := context.Background()
	r, s := runRepo(t)
	ref := refs(1)[0]
	run, err := r.CreateRun(ctx, verify.Scope{}, "library", "v1", []library.BookRef{ref})
	if err != nil {
		t.Fatal(err)
	}
	p := cleanEvidence(ref)
	p.SchemaVersion = evidence.SchemaVersionV4
	p.Calibre.Title = "Untitled"
	p.FindingsVersion = 5
	if p.Findings, err = findings.DeriveVersion(ctx, p, 5); err != nil {
		t.Fatal(err)
	}
	raw, seal, err := evidence.Seal(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE run_books SET state='checked' WHERE run_id=$1`, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `INSERT INTO evidence(run_id,book_id,uuid,schema_version,package,seal,projection_version) VALUES($1,$2,$3,$4,$5::jsonb,$6,5)`,
		run.ID, ref.ID, ref.UUID, p.SchemaVersion, string(raw), seal); err != nil {
		t.Fatal(err)
	}
	for i, f := range p.Findings {
		if _, err := s.Pool.Exec(ctx, `INSERT INTO findings(run_id,book_id,ordinal,code,format,field,reason) VALUES($1,$2,$3,$4,$5,$6,$7)`, run.ID, ref.ID, i, f.Code, f.Format, f.Field, f.Reason); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.SetStatus(ctx, run.ID, verify.RunCompleted, ""); err != nil {
		t.Fatal(err)
	}
	svc := report.New(NewReportRepo(s))
	book, err := svc.Book(ctx, run.ID, ref.ID)
	if err != nil || book.Package.FindingsVersion != 5 || book.Package.SchemaVersion != evidence.SchemaVersionV4 {
		t.Fatalf("Book of a version-5 projection: %v", err)
	}
	if check, err := svc.CheckSeals(ctx, run.ID); err != nil || check.Broken != 0 || check.Books != 1 {
		t.Fatalf("CheckSeals = %+v, %v", check, err)
	}
	var pv int
	if err := s.Pool.QueryRow(ctx, `SELECT projection_version FROM evidence WHERE run_id=$1`, run.ID).Scan(&pv); err != nil || pv != 5 {
		t.Fatalf("stored projection version %d, %v", pv, err)
	}
	// A version-6 binary may write projection version 6, and nothing past it.
	for v, wantOK := range map[int]bool{6: true, 7: false} {
		_, err := s.Pool.Exec(ctx, `UPDATE evidence SET projection_version=$2 WHERE run_id=$1`, run.ID, v)
		if (err == nil) != wantOK {
			t.Errorf("projection version %d: %v", v, err)
		}
	}
}
