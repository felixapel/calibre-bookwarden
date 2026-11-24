package store

import (
	"context"
	"slices"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/decide"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/findings"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/report"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

func TestCoversProjection(t *testing.T) {
	ctx := context.Background()
	r, s := runRepo(t)
	books := refs(2)
	run, err := r.CreateRun(ctx, verify.Scope{}, "library", "v1", books)
	if err != nil {
		t.Fatal(err)
	}
	for i, ref := range books {
		p := cleanEvidence(ref)
		p.Calibre.HasCover = true
		p.Cover = &evidence.CoverEvidence{State: evidence.StateOK, Bytes: 10, SHA256: "aa", Detected: "jpeg", Width: 800, Height: 1200, DHash: "ffffffffffffffff", Flatness: 400}
		if i == 1 {
			p.Cover = &evidence.CoverEvidence{State: evidence.StateCorrupt, Bytes: 3, SHA256: "bb", Flatness: -1}
		}
		raw, seal, err := decide.Seal(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		if err := r.FinishBook(ctx, run.ID, ref, verify.Outcome{State: verify.BookChecked, Evidence: raw, Seal: seal}); err != nil {
			t.Fatal(err)
		}
	}
	var n, book, w, h int
	var sha string
	var dhash int64
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) OVER (), book_id, sha256, dhash, width, height FROM covers WHERE run_id=$1`, run.ID).Scan(&n, &book, &sha, &dhash, &w, &h); err != nil {
		t.Fatal(err)
	}
	if n != 1 || book != books[0].ID || sha != "aa" || dhash != -1 || w != 800 || h != 1200 {
		t.Fatalf("covers row: n=%d book=%d sha=%s dhash=%d %dx%d", n, book, sha, dhash, w, h)
	}
	var version int
	if err := s.Pool.QueryRow(ctx, `SELECT DISTINCT projection_version FROM evidence WHERE run_id=$1`, run.ID).Scan(&version); err != nil || version != findings.Version {
		t.Fatalf("projection version %d, %v", version, err)
	}
	if _, err := s.Pool.Exec(ctx, `DELETE FROM runs WHERE id=$1`, run.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM covers`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("covers left after the run: %d, %v", n, err)
	}
}

func TestHistoricalV3Reports(t *testing.T) {
	ctx := context.Background()
	r, s := runRepo(t)
	ref := refs(1)[0]
	run, err := r.CreateRun(ctx, verify.Scope{}, "library", "v1", []library.BookRef{ref})
	if err != nil {
		t.Fatal(err)
	}
	p := cleanEvidence(ref)
	p.SchemaVersion = evidence.SchemaVersionV1 // the row below is historical
	p.Formats[0].State, p.Formats[0].Reason, p.Formats[0].Metadata = evidence.StateMissing, evidence.ReasonMissingOnDisk, nil
	p.FindingsVersion = 3
	if p.Findings, err = findings.DeriveVersion(ctx, p, 3); err != nil || len(p.Findings) != 1 {
		t.Fatalf("v3 findings %+v/%v", p.Findings, err)
	}
	raw, seal, err := evidence.Seal(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE run_books SET state='checked' WHERE run_id=$1`, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `INSERT INTO evidence(run_id,book_id,uuid,schema_version,package,seal,projection_version) VALUES($1,$2,$3,1,$4::jsonb,$5,3)`, run.ID, ref.ID, ref.UUID, string(raw), seal); err != nil {
		t.Fatal(err)
	}
	f := p.Findings[0]
	if _, err := s.Pool.Exec(ctx, `INSERT INTO findings(run_id,book_id,ordinal,code,format,field,reason) VALUES($1,$2,0,$3,$4,$5,$6)`, run.ID, ref.ID, f.Code, f.Format, f.Field, f.Reason); err != nil {
		t.Fatal(err)
	}
	service := report.New(NewReportRepo(s))
	page, err := service.Findings(ctx, run.ID, report.Filter{Code: "file_missing"}, nil)
	if err != nil || len(page.Rows) != 1 || page.Rows[0].Finding != f {
		t.Fatalf("v3 report=%+v/%v", page, err)
	}
	book, err := service.Book(ctx, run.ID, ref.ID)
	current, derr := findings.Derive(ctx, p)
	if err != nil || derr != nil || !slices.Equal(book.Findings, current) || book.Package.FindingsVersion != 3 {
		t.Fatalf("v3 book=%+v/%v/%v", book, err, derr)
	}
}
