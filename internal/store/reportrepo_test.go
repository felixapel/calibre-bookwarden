package store

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/report"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

func reportFixture(t *testing.T, legacy bool) (*RunRepo, *Store, verify.Run, library.BookRef) {
	t.Helper()
	r, s := runRepo(t)
	ref := refs(1)[0]
	run, err := r.CreateRun(context.Background(), verify.Scope{}, "library", "v1", []library.BookRef{ref})
	if err != nil {
		t.Fatal(err)
	}
	p := cleanEvidence(ref)
	p.Formats = nil
	if err := r.FinishBook(context.Background(), run.ID, ref, findingOutcome(t, p, legacy)); err != nil {
		t.Fatal(err)
	}
	return r, s, run, ref
}

func TestReportIntegrity(t *testing.T) {
	ctx := context.Background()
	for _, change := range []string{"none", "missing", "seal", "row_uuid", "schema", "package_uuid", "package_book", "package_library", "delete", "change", "extra", "marker", "older_marker", "unknown_marker", "unexpected", "legacy", "legacy_materialized_corrupt"} {
		t.Run(change, func(t *testing.T) {
			legacy := change == "legacy" || change == "legacy_materialized_corrupt"
			_, s, run, ref := reportFixture(t, legacy)
			query := ""
			switch change {
			case "missing":
				query = `DELETE FROM evidence`
			case "seal":
				query = `UPDATE evidence SET seal=repeat('0',64)`
			case "row_uuid":
				query = `UPDATE evidence SET uuid='different'`
			case "schema":
				query = `UPDATE evidence SET schema_version=99`
			case "delete", "legacy_materialized_corrupt":
				query = `DELETE FROM findings`
			case "change":
				query = `UPDATE findings SET reason='invented'`
			case "extra":
				query = `INSERT INTO findings SELECT run_id,book_id,9,'no_formats','','','' FROM evidence`
			case "marker":
				query = `UPDATE evidence SET projection_version=0`
			case "older_marker":
				query = `UPDATE evidence SET projection_version=1`
			case "unknown_marker":
				query = `ALTER TABLE evidence DROP CONSTRAINT evidence_projection_version_check; UPDATE evidence SET projection_version=99`
			case "unexpected":
				query = `UPDATE run_books SET state='failed'`
			case "legacy":
				query = `DELETE FROM findings; UPDATE evidence SET projection_version=0`
			case "package_uuid", "package_book", "package_library":
				p := cleanEvidence(ref)
				p.Formats = nil
				if change == "package_uuid" {
					p.UUID = "different"
				}
				if change == "package_book" {
					p.BookID++
				}
				if change == "package_library" {
					p.LibraryID = "different"
				}
				out := findingOutcome(t, p, false)
				if _, err := s.Pool.Exec(ctx, `UPDATE evidence SET package=$1::jsonb,seal=$2`, string(out.Evidence), out.Seal); err != nil {
					t.Fatal(err)
				}
			}
			if query != "" {
				if _, err := s.Pool.Exec(ctx, query); err != nil {
					t.Fatal(err)
				}
			}
			service := report.New(NewReportRepo(s))
			b, err := service.Book(ctx, run.ID, ref.ID)
			if change == "none" || change == "legacy" {
				if err != nil || b.Package == nil || len(b.Package.Findings) != 1 || b.Package.Findings[0].Code != "no_formats" {
					t.Fatalf("valid=%+v/%v", b, err)
				}
				if change == "legacy" {
					var v, n int
					if err := s.Pool.QueryRow(ctx, `SELECT projection_version,(SELECT count(*) FROM findings) FROM evidence`).Scan(&v, &n); err != nil || v != 0 || n != 0 {
						t.Fatalf("GET wrote projection %d/%d/%v", v, n, err)
					}
				}
				if _, err := service.Book(ctx, run.ID, 999); !errors.Is(err, report.ErrNoSuchBook) {
					t.Fatalf("missing book=%v", err)
				}
				if _, err := service.Book(ctx, 999, ref.ID); !errors.Is(err, verify.ErrNoSuchRun) {
					t.Fatalf("missing run=%v", err)
				}
			} else if !errors.Is(err, report.ErrIntegrity) {
				t.Fatalf("corruption %s=%v", change, err)
			}
		})
	}
}

func TestReportOperational(t *testing.T) {
	ctx := context.Background()
	r, s := runRepo(t)
	books := refs(3)
	run, err := r.CreateRun(ctx, verify.Scope{}, "library", "v1", books)
	if err != nil {
		t.Fatal(err)
	}
	for i, state := range []verify.BookState{verify.BookFailed, verify.BookSourceChanged} {
		if err := r.FinishBook(ctx, run.ID, books[i], verify.Outcome{State: state, Reason: "qualified"}); err != nil {
			t.Fatal(err)
		}
	}
	service := report.New(NewReportRepo(s))
	for _, ref := range books {
		b, err := service.Book(ctx, run.ID, ref.ID)
		if err != nil || b.Package != nil || b.Ref != ref {
			t.Fatalf("operational=%+v/%v", b, err)
		}
	}
}

func TestFilteredStoredProjectionCorruption(t *testing.T) {
	ctx := context.Background()
	r, s := runRepo(t)
	books := refs(3)
	run, err := r.CreateRun(ctx, verify.Scope{}, "library", "v1", books)
	if err != nil {
		t.Fatal(err)
	}
	for i, ref := range books {
		p := cleanEvidence(ref)
		p.Formats = nil
		for j := range 60 {
			p.Formats = append(p.Formats, evidence.FormatEvidence{Format: fmt.Sprintf("F%d", j), State: evidence.StateMissing})
		}
		if i != 1 {
			p.Formats = nil
		}
		if err := r.FinishBook(ctx, run.ID, ref, findingOutcome(t, p, false)); err != nil {
			t.Fatal(err)
		}
	}
	service := report.New(NewReportRepo(s))
	for _, ref := range []library.BookRef{books[0], books[2]} {
		tx, err := s.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		// Commit the mutation so a new report snapshot sees it, then restore its tuple.
		if _, err := tx.Exec(ctx, `UPDATE findings SET reason='changed' WHERE run_id=$1 AND book_id=$2`, run.ID, ref.ID); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		for _, cursor := range []*report.Cursor{nil, {RunID: run.ID, BookID: books[1].ID, Ordinal: 0}} {
			if _, err := service.Findings(ctx, run.ID, report.Filter{Code: "file_missing"}, cursor); !errors.Is(err, report.ErrIntegrity) {
				t.Fatalf("hidden corruption=%v", err)
			}
		}
		if _, err := s.Pool.Exec(ctx, `UPDATE findings SET reason='' WHERE run_id=$1 AND book_id=$2`, run.ID, ref.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Pool.Exec(ctx, `DELETE FROM findings WHERE run_id=$1 AND book_id=$2`, run.ID, books[1].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Findings(ctx, run.ID, report.Filter{Code: "file_missing"}, nil); !errors.Is(err, report.ErrIntegrity) {
		t.Fatalf("deleted matching rows=%v", err)
	}
}

func TestSnapshotConsistency(t *testing.T) {
	ctx := context.Background()
	r, s, run, ref := reportFixture(t, false)
	snap, err := NewReportRepo(s).Snapshot(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = snap.Close(ctx) }()
	if err := r.SetStatus(ctx, run.ID, verify.RunCompleted, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := r.pruneAll(ctx); err != nil {
		t.Fatal(err)
	}
	b, err := snap.Book(ctx, ref.ID)
	if err != nil || b.Package == nil || snap.Run().Status != verify.RunRunning {
		t.Fatalf("snapshot drift=%+v/%v", b, err)
	}
	if _, err := report.New(NewReportRepo(s)).Book(ctx, run.ID, ref.ID); !errors.Is(err, verify.ErrNoSuchRun) {
		t.Fatalf("fresh snapshot=%v", err)
	}
}

func TestSnapshotCompletion(t *testing.T) {
	ctx := context.Background()
	r, s := runRepo(t)
	ref := refs(1)[0]
	run, err := r.CreateRun(ctx, verify.Scope{}, "library", "v1", []library.BookRef{ref})
	if err != nil {
		t.Fatal(err)
	}
	snap, err := NewReportRepo(s).Snapshot(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = snap.Close(ctx) }()
	if err := r.FinishBook(ctx, run.ID, ref, findingOutcome(t, cleanEvidence(ref), false)); err != nil {
		t.Fatal(err)
	}
	b, err := snap.Book(ctx, ref.ID)
	if err != nil || b.State != verify.BookPending || b.Package != nil {
		t.Fatalf("completion drift=%+v/%v", b, err)
	}
}

// TestSnapshotIsReadOnly: a report snapshot refuses writes, so the eval
// command can't change a run even through a read-write pool (SQLSTATE 25006).
func TestSnapshotIsReadOnly(t *testing.T) {
	ctx := context.Background()
	_, s, run, _ := reportFixture(t, false)
	snap, err := NewReportRepo(s).Snapshot(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = snap.Close(ctx) }()
	_, err = snap.(*reportSnapshot).tx.Exec(ctx, `DELETE FROM runs`)
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); !ok || pgErr.Code != "25006" {
		t.Fatalf("a write inside the snapshot: %v", err)
	}
}

// TestReportOtherWorkFinding: a book whose identity records describe another
// work shows on the Findings page, though the sealed findings don't list it.
func TestReportOtherWorkFinding(t *testing.T) {
	ctx := context.Background()
	r, s := runRepo(t)
	ref := refs(1)[0]
	run, err := r.CreateRun(ctx, verify.Scope{}, "library", "v1", []library.BookRef{ref})
	if err != nil {
		t.Fatal(err)
	}
	p := cleanEvidence(ref)
	p.Calibre.Title, p.Calibre.Authors = "Another Book", []string{"Someone Else"}
	p.Formats[0].Metadata.Title, p.Formats[0].Metadata.Authors = "Another Book", []string{"Someone Else"}
	p.Lookups = []evidence.Lookup{{ISBN: "9780306406157", Seeds: []string{evidence.SeedFile}, Results: []evidence.SourceResult{{
		Source: evidence.SourceDNB, Outcome: evidence.SourceOK,
		Records: []evidence.SourceRecord{{ID: "1", Title: "Binary Tides", Authors: []string{"Ada Reed"}, Languages: []string{"eng"}, ISBNs: []string{"9780306406157"}}},
	}}}}
	if err := r.FinishBook(ctx, run.ID, ref, findingOutcome(t, p, false)); err != nil {
		t.Fatal(err)
	}
	service := report.New(NewReportRepo(s))
	page, err := service.Findings(ctx, run.ID, report.Filter{Code: "other_work"}, nil)
	if err != nil || len(page.Rows) != 1 || page.Rows[0].BookID != ref.ID || page.Rows[0].Finding.Field != "isbn" {
		t.Fatalf("other_work findings = %+v, %v", page, err)
	}
	b, err := service.Book(ctx, run.ID, ref.ID)
	if err != nil || b.Decision.Tier != evidence.TierC || len(b.Decision.Proposals) != 0 {
		t.Fatalf("book = %+v, %v", b.Decision, err)
	}
}
