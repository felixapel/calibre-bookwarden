package store

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/findings"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

func findingOutcome(t *testing.T, p evidence.Package, legacy bool) verify.Outcome {
	t.Helper()
	var raw []byte
	var seal string
	var err error
	if legacy {
		raw, seal, err = evidence.Seal(p)
	} else {
		raw, seal, err = findings.Seal(context.Background(), p)
	}
	if err != nil {
		t.Fatal(err)
	}
	return verify.Outcome{State: verify.BookChecked, Evidence: raw, Seal: seal}
}

func cleanEvidence(ref library.BookRef) evidence.Package {
	return evidence.Package{
		SchemaVersion: evidence.SchemaVersion, LibraryID: "library", BookID: ref.ID, UUID: ref.UUID,
		Calibre: evidence.Record{Title: "Binary Tides", Authors: []string{"Ada Reed"}, AuthorSort: "Reed, Ada", Languages: []string{"eng"}, Identifiers: map[string]string{"isbn": "9780306406157"}, PubDate: "2020-05-03"},
		Formats: []evidence.FormatEvidence{{Format: "EPUB", State: evidence.StateOK, Detected: "epub", Metadata: &evidence.FileMetadata{Title: "Binary Tides", Authors: []string{"Ada Reed"}, Languages: []string{"eng"}, ISBNs: []string{"9780306406157"}, Date: "2020-05-03"}}},
	}
}

func assertPendingWithoutEvidence(t *testing.T, s *Store, runID int64, bookID int) {
	t.Helper()
	var state string
	var n int
	if err := s.Pool.QueryRow(context.Background(), `SELECT state, (SELECT count(*) FROM evidence WHERE run_id=$1 AND book_id=$2) FROM run_books WHERE run_id=$1 AND book_id=$2`, runID, bookID).Scan(&state, &n); err != nil {
		t.Fatal(err)
	}
	if state != "pending" || n != 0 {
		t.Fatalf("rejected completion left state=%s evidence=%d", state, n)
	}
}

func TestFinishBookIdentity(t *testing.T) {
	ctx := context.Background()
	for _, name := range []string{"ref_uuid", "book_id", "library", "uuid", "seal", "findings", "failed_ref_uuid"} {
		t.Run(name, func(t *testing.T) {
			r, s := runRepo(t)
			ref := refs(1)[0]
			run, err := r.CreateRun(ctx, verify.Scope{}, "library", "v1", []library.BookRef{ref})
			if err != nil {
				t.Fatal(err)
			}
			p := cleanEvidence(ref)
			supplied := ref
			switch name {
			case "ref_uuid", "failed_ref_uuid":
				supplied.UUID = "different"
			case "book_id":
				p.BookID++
			case "library":
				p.LibraryID = "different"
			case "uuid":
				p.UUID = "different"
			}
			out := findingOutcome(t, p, false)
			if name == "seal" {
				out.Seal = strings.Repeat("0", 64)
			}
			if name == "findings" {
				p.FindingsVersion = 1
				p.Findings = []evidence.Finding{{Code: "file_missing", Format: "EPUB"}}
				out = findingOutcome(t, p, true)
			}
			if name == "failed_ref_uuid" {
				out = verify.Outcome{State: verify.BookFailed}
			}
			if err := r.FinishBook(ctx, run.ID, supplied, out); err == nil {
				t.Fatal("accepted mismatched completion")
			}
			assertPendingWithoutEvidence(t, s, run.ID, ref.ID)
		})
	}
}

func TestFindingsProjection(t *testing.T) {
	ctx := context.Background()
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "new", true: "legacy"}[legacy], func(t *testing.T) {
			r, s := runRepo(t)
			books := refs(4)
			run, err := r.CreateRun(ctx, verify.Scope{}, "library", "v1", books)
			if err != nil {
				t.Fatal(err)
			}
			for i := range 2 {
				p := cleanEvidence(books[i])
				if i == 1 {
					p.Formats[0].State = evidence.StateMissing
					p.Formats[0].Reason = evidence.ReasonMissingOnDisk
				}
				out := findingOutcome(t, p, legacy)
				if err := r.FinishBook(ctx, run.ID, books[i], out); err != nil {
					t.Fatal(err)
				}
				raw, seal, err := r.Evidence(ctx, run.ID, books[i].ID)
				if err != nil {
					t.Fatal(err)
				}
				original, err := evidence.Open(raw, seal)
				if err != nil {
					t.Fatal(err)
				}
				canonical, sameSeal, err := evidence.Seal(original)
				if err != nil || sameSeal != out.Seal || !bytes.Equal(canonical, out.Evidence) {
					t.Fatal("completion changed sealed package")
				}
				opened, err := findings.Open(ctx, raw, seal)
				if err != nil {
					t.Fatal(err)
				}
				var version int
				if err := s.Pool.QueryRow(ctx, `SELECT projection_version FROM evidence WHERE run_id=$1 AND book_id=$2`, run.ID, books[i].ID).Scan(&version); err != nil || version != findings.Version {
					t.Fatalf("projection marker=%d/%v", version, err)
				}
				rows, err := s.Pool.Query(ctx, `SELECT ordinal,code,format,field,reason FROM findings WHERE run_id=$1 AND book_id=$2 ORDER BY ordinal`, run.ID, books[i].ID)
				if err != nil {
					t.Fatal(err)
				}
				var got []evidence.Finding
				for rows.Next() {
					var ordinal int
					var f evidence.Finding
					if err := rows.Scan(&ordinal, &f.Code, &f.Format, &f.Field, &f.Reason); err != nil {
						t.Fatal(err)
					}
					if ordinal != len(got) {
						t.Fatalf("ordinal=%d", ordinal)
					}
					got = append(got, f)
				}
				rows.Close()
				if err := rows.Err(); err != nil {
					t.Fatal(err)
				}
				if !slices.Equal(got, opened.Findings) || len(got) != i {
					t.Fatalf("projection=%+v want %+v", got, opened.Findings)
				}
			}
			for i, state := range []verify.BookState{verify.BookFailed, verify.BookSourceChanged} {
				if err := r.FinishBook(ctx, run.ID, books[i+2], verify.Outcome{State: state}); err != nil {
					t.Fatal(err)
				}
			}
			var n int
			if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM evidence WHERE run_id=$1`, run.ID).Scan(&n); err != nil || n != 2 {
				t.Fatalf("evidence=%d/%v", n, err)
			}
			if err := r.SetStatus(ctx, run.ID, verify.RunCompleted, ""); err != nil {
				t.Fatal(err)
			}
			if _, err := r.pruneAll(ctx); err != nil {
				t.Fatal(err)
			}
			if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM findings`).Scan(&n); err != nil || n != 0 {
				t.Fatalf("prune left findings=%d/%v", n, err)
			}
		})
	}
}

func TestFindingsInsertRollback(t *testing.T) {
	ctx := context.Background()
	r, s := runRepo(t)
	ref := refs(1)[0]
	run, err := r.CreateRun(ctx, verify.Scope{}, "library", "v1", []library.BookRef{ref})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Pool.Exec(ctx, `CREATE FUNCTION reject_findings() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected'; END $$; CREATE TRIGGER reject_findings BEFORE INSERT ON findings FOR EACH ROW EXECUTE FUNCTION reject_findings()`)
	if err != nil {
		t.Fatal(err)
	}
	p := cleanEvidence(ref)
	p.Formats = nil
	if err := r.FinishBook(ctx, run.ID, ref, findingOutcome(t, p, false)); err == nil {
		t.Fatal("injected insert succeeded")
	}
	assertPendingWithoutEvidence(t, s, run.ID, ref.ID)
}

func TestFinishBookConcurrent(t *testing.T) {
	ctx := context.Background()
	r, _ := runRepo(t)
	ref := refs(1)[0]
	run, err := r.CreateRun(ctx, verify.Scope{}, "library", "v1", []library.BookRef{ref})
	if err != nil {
		t.Fatal(err)
	}
	out := findingOutcome(t, cleanEvidence(ref), false)
	done := make(chan error, 2)
	start := make(chan struct{})
	for range 2 {
		go func() { <-start; done <- r.FinishBook(ctx, run.ID, ref, out) }()
	}
	close(start)
	a, b := <-done, <-done
	once := (a == nil && errors.Is(b, verify.ErrBookFinished)) || (b == nil && errors.Is(a, verify.ErrBookFinished))
	if !once {
		t.Fatalf("concurrent completion=%v/%v", a, b)
	}
}
