package store

import (
	"context"
	"slices"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/findings"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/report"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

// TestVersionFourProjectionReplays: a book stored under findings version 4
// (evidence sealed at 4, findings rows from DeriveVersion 4) is still read
// by a version-5 binary. Its stored rows match the v4 derivation, the page
// shows the current (v5) findings, and a seal check writes v5 projection rows
// without touching the stored evidence or findings rows. The title and
// publisher are junk, so a reader that compared the stored v4 rows with the v5
// derivation would report damage.
func TestVersionFourProjectionReplays(t *testing.T) {
	ctx := context.Background()
	r, s := runRepo(t)
	ref := refs(1)[0]
	run, err := r.CreateRun(ctx, verify.Scope{}, "library", "v1", []library.BookRef{ref})
	if err != nil {
		t.Fatal(err)
	}
	p := cleanEvidence(ref)
	p.Calibre.Title, p.Calibre.Publisher = "Untitled", "Unknown"
	p.FindingsVersion = 4
	if p.Findings, err = findings.DeriveVersion(ctx, p, 4); err != nil {
		t.Fatal(err)
	}
	for _, f := range p.Findings {
		if f.Code == "junk_title" || f.Code == "junk_publisher" {
			t.Fatalf("version 4 derived %+v", f)
		}
	}
	raw, seal, err := evidence.Seal(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE run_books SET state='checked' WHERE run_id=$1`, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `INSERT INTO evidence(run_id,book_id,uuid,schema_version,package,seal,projection_version) VALUES($1,$2,$3,$4,$5::jsonb,$6,4)`,
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
	if err != nil {
		t.Fatalf("Book of a version-4 projection: %v", err)
	}
	codes := func(fs []evidence.Finding) []string {
		var out []string
		for _, f := range fs {
			out = append(out, f.Code)
		}
		return out
	}
	if got := codes(book.Findings); !slices.Contains(got, "junk_title") || !slices.Contains(got, "junk_publisher") || book.Package.FindingsVersion != 4 {
		t.Fatalf("current view %v at sealed version %d", got, book.Package.FindingsVersion)
	}

	check, err := svc.CheckSeals(ctx, run.ID)
	if err != nil || check.Broken != 0 || check.Books != 1 {
		t.Fatalf("CheckSeals = %+v, %v", check, err)
	}
	var projected []string
	rows, err := s.Pool.Query(ctx, `SELECT code FROM finding_projection WHERE run_id=$1 AND book_id=$2 ORDER BY ordinal`, run.ID, ref.ID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var code string
		if err := rows.Scan(&code); err != nil {
			t.Fatal(err)
		}
		projected = append(projected, code)
	}
	rows.Close()
	if !slices.Contains(projected, "junk_title") || !slices.Contains(projected, "junk_publisher") {
		t.Fatalf("projection rows %v", projected)
	}
	var version, stored int
	if err := s.Pool.QueryRow(ctx, `SELECT c.findings_version, (SELECT count(*) FROM findings WHERE run_id=$1 AND code LIKE 'junk\_%')
		FROM seal_checks c WHERE c.run_id=$1`, run.ID).Scan(&version, &stored); err != nil || version != findings.Version || stored != 0 {
		t.Fatalf("check at version %d with %d stored junk rows, %v", version, stored, err)
	}
	var pv int
	if err := s.Pool.QueryRow(ctx, `SELECT projection_version FROM evidence WHERE run_id=$1`, run.ID).Scan(&pv); err != nil || pv != 4 {
		t.Fatalf("stored projection version %d, %v", pv, err)
	}
}
