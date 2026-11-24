package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/findings"
	"github.com/felixapel/calibre-bookwarden/internal/inspect"
	"github.com/felixapel/calibre-bookwarden/internal/inspect/inspecttest"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/report"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

func TestInspectedMetadataReports(t *testing.T) {
	for _, tt := range []struct {
		name, title, stored string
		unverified          bool
	}{
		{"truncated", strings.Repeat("a", 512) + "hidden suffix", strings.Repeat("a", 512), true},
		{"pending_space", strings.Repeat("a", 511) + " hidden suffix", strings.Repeat("a", 511), true},
		{"complete_boundary", strings.Repeat("a", 512), strings.Repeat("a", 512), false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			data := inspecttest.EPUB(inspecttest.EPUBOptions{Title: tt.title, Authors: []string{"Ada Reed"}, Language: "eng", ISBN: "9780306406157", Date: "2020-05-03", Text: "Synthetic"})
			res := inspect.Inspect(ctx, "EPUB", bytes.NewReader(data), int64(len(data)), inspect.DefaultLimits)
			if res.State != evidence.StateOK || res.Metadata == nil || res.Metadata.Title != tt.stored {
				t.Fatalf("inspection=%+v", res)
			}
			r, s := runRepo(t)
			ref := refs(1)[0]
			run, err := r.CreateRun(ctx, verify.Scope{}, "library", "v1", []library.BookRef{ref})
			if err != nil {
				t.Fatal(err)
			}
			p := cleanEvidence(ref)
			p.Calibre.Title = tt.stored
			p.Formats[0].Metadata = res.Metadata
			out := findingOutcome(t, p, false)
			opened, err := findings.Open(ctx, out.Evidence, out.Seal)
			want := evidence.Finding{Code: "metadata_unverified", Format: "EPUB", Field: "title", Reason: "field_too_long"}
			if err != nil || slices.Contains(opened.Findings, want) != tt.unverified {
				t.Fatalf("sealed findings=%+v/%v; title unverified=%v", opened.Findings, err, tt.unverified)
			}
			if opened.FindingsVersion != findings.Version {
				t.Fatalf("new derivation version=%d, want %d", opened.FindingsVersion, findings.Version)
			}
			if err := r.FinishBook(ctx, run.ID, ref, out); err != nil {
				t.Fatal(err)
			}
			page, err := report.New(NewReportRepo(s)).Findings(ctx, run.ID, report.Filter{Code: "metadata_unverified", Format: "EPUB"}, nil)
			if err != nil || (len(page.Rows) == 1) != tt.unverified || tt.unverified && page.Rows[0].Finding != want {
				t.Fatalf("current report=%+v/%v", page, err)
			}
		})
	}
}

func TestInspectedCollectionReports(t *testing.T) {
	authors, isbns := make([]string, 33), make([]string, 17)
	for i := range authors {
		authors[i] = fmt.Sprintf("a%d", i)
	}
	for i := range isbns {
		isbns[i] = fmt.Sprintf("978000000%04d", i)
	}
	mobi := inspecttest.CalibreMOBI(6)
	mobi.EXTH[104] = isbns
	for _, tt := range []struct {
		name, format, field string
		data                []byte
	}{
		{"authors", "EPUB", "authors", inspecttest.EPUB(inspecttest.EPUBOptions{Title: "Binary Tides", Authors: authors, Language: "eng", ISBN: "9780306406157", Text: "Synthetic"})},
		{"isbns", "MOBI", "isbn", inspecttest.MOBI(mobi)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			res := inspect.Inspect(ctx, tt.format, bytes.NewReader(tt.data), int64(len(tt.data)), inspect.DefaultLimits)
			if res.State != evidence.StateOK || res.Metadata == nil {
				t.Fatalf("inspection=%+v", res)
			}
			r, s := runRepo(t)
			ref := refs(1)[0]
			run, err := r.CreateRun(ctx, verify.Scope{}, "library", "v1", []library.BookRef{ref})
			if err != nil {
				t.Fatal(err)
			}
			p := cleanEvidence(ref)
			p.Formats[0].Format, p.Formats[0].Detected, p.Formats[0].Metadata = tt.format, res.Detected, res.Metadata
			out := findingOutcome(t, p, false)
			opened, err := findings.Open(ctx, out.Evidence, out.Seal)
			want := evidence.Finding{Code: "metadata_unverified", Format: tt.format, Field: tt.field, Reason: "field_too_long"}
			if err != nil || !slices.Contains(opened.Findings, want) {
				t.Fatalf("capture qualification lost: %+v/%v", opened.Findings, err)
			}
			for _, f := range opened.Findings {
				if f.Field == tt.field && f != want {
					t.Fatalf("incomplete collection was compared: %+v", opened.Findings)
				}
			}
			if err := r.FinishBook(ctx, run.ID, ref, out); err != nil {
				t.Fatal(err)
			}
			page, err := report.New(NewReportRepo(s)).Findings(ctx, run.ID, report.Filter{Code: "metadata_unverified", Format: tt.format}, nil)
			if err != nil || len(page.Rows) != 1 || page.Rows[0].Finding != want {
				t.Fatalf("current collection report=%+v/%v", page, err)
			}
		})
	}
}

func TestHistoricalProjectionCapture(t *testing.T) {
	for _, tt := range []struct {
		name       string
		version    int
		completion bool
	}{
		{"legacy_materialized_v1", 0, false},
		{"sealed_v1_projection", 1, false},
		{"sealed_v1_new_completion", 1, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			r, s := runRepo(t)
			ref := refs(1)[0]
			run, err := r.CreateRun(ctx, verify.Scope{}, "library", "v1", []library.BookRef{ref})
			if err != nil {
				t.Fatal(err)
			}
			p := cleanEvidence(ref)
			p.SchemaVersion = evidence.SchemaVersionV1 // the row below is historical
			p.Formats[0].Metadata.Title = strings.Repeat("a", 512)
			old := evidence.Finding{Code: "title_mismatch", Format: "EPUB", Field: "title"}
			p.FindingsVersion = tt.version
			if tt.version == 1 {
				p.Findings = []evidence.Finding{old}
			}
			raw, seal, err := evidence.Seal(p)
			if err != nil {
				t.Fatal(err)
			}
			marker := 1
			if tt.completion {
				marker = findings.Version
				if err := r.FinishBook(ctx, run.ID, ref, verify.Outcome{State: verify.BookChecked, Evidence: raw, Seal: seal}); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := s.Pool.Exec(ctx, `UPDATE run_books SET state='checked' WHERE run_id=$1`, run.ID); err != nil {
					t.Fatal(err)
				}
				if _, err := s.Pool.Exec(ctx, `INSERT INTO evidence(run_id,book_id,uuid,schema_version,package,seal,projection_version) VALUES($1,$2,$3,1,$4::jsonb,$5,1)`, run.ID, ref.ID, ref.UUID, string(raw), seal); err != nil {
					t.Fatal(err)
				}
				if _, err := s.Pool.Exec(ctx, `INSERT INTO findings(run_id,book_id,ordinal,code,format,field,reason) VALUES($1,$2,0,$3,$4,$5,'')`, run.ID, ref.ID, old.Code, old.Format, old.Field); err != nil {
					t.Fatal(err)
				}
			}
			service := report.New(NewReportRepo(s))
			want := evidence.Finding{Code: "metadata_unverified", Format: "EPUB", Field: "title", Reason: "capture_limit_unverified"}
			book, err := service.Book(ctx, run.ID, ref.ID)
			if err != nil || book.Package == nil || book.FindingsVersion != findings.Version || !slices.Equal(book.Findings, []evidence.Finding{want}) {
				t.Fatalf("current book=%+v/%v", book, err)
			}
			if tt.version == 1 && !slices.Equal(book.Package.Findings, []evidence.Finding{old}) {
				t.Fatalf("historical v1 findings changed: %+v", book.Package.Findings)
			}
			var csv bytes.Buffer
			if err := service.CSV(ctx, run.ID, report.Filter{Code: want.Code}, &csv, report.DefaultCSVLimits()); err != nil || !strings.Contains(csv.String(), want.Reason) {
				t.Fatalf("CSV omitted current qualification: %q/%v", csv.String(), err)
			}
			stored, storedSeal, err := r.Evidence(ctx, run.ID, ref.ID)
			canonical, canonErr := evidence.Canonical(json.RawMessage(stored))
			if err != nil || canonErr != nil || storedSeal != seal || !bytes.Equal(canonical, raw) {
				t.Fatal("completion/report changed original historical payload")
			}
			var v, n int
			if err := s.Pool.QueryRow(ctx, `SELECT projection_version,(SELECT count(*) FROM findings) FROM evidence`).Scan(&v, &n); err != nil || v != marker || n != 1 {
				t.Fatalf("report changed projection: %d/%d/%v", v, n, err)
			}
			if _, err := s.Pool.Exec(ctx, `DELETE FROM findings`); err != nil {
				t.Fatal(err)
			}
			if _, err := service.Findings(ctx, run.ID, report.Filter{Code: want.Code}, nil); !errors.Is(err, report.ErrIntegrity) {
				t.Fatalf("current filter hid corrupt historical projection: %v", err)
			}
		})
	}
}

func TestHistoricalCaptureReports(t *testing.T) {
	for _, version := range []int{0, 1} {
		t.Run(string(rune('0'+version)), func(t *testing.T) {
			ctx := context.Background()
			r, s := runRepo(t)
			ref := refs(1)[0]
			run, err := r.CreateRun(ctx, verify.Scope{}, "library", "v1", []library.BookRef{ref})
			if err != nil {
				t.Fatal(err)
			}
			p := cleanEvidence(ref)
			p.SchemaVersion = evidence.SchemaVersionV1 // the row below is historical
			p.Calibre.Title = strings.Repeat("a", 512)
			p.Formats[0].Metadata.Title = p.Calibre.Title
			p.FindingsVersion = version
			raw, seal, err := evidence.Seal(p)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Pool.Exec(ctx, `UPDATE run_books SET state='checked' WHERE run_id=$1`, run.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Pool.Exec(ctx, `INSERT INTO evidence(run_id,book_id,uuid,schema_version,package,seal,projection_version) VALUES($1,$2,$3,1,$4::jsonb,$5,$6)`, run.ID, ref.ID, ref.UUID, string(raw), seal, version); err != nil {
				t.Fatal(err)
			}
			opened, err := findings.Open(ctx, raw, seal)
			if err != nil || version == 1 && len(opened.Findings) != 0 {
				t.Fatalf("historical derivation changed=%+v/%v", opened.Findings, err)
			}
			service := report.New(NewReportRepo(s))
			page, err := service.Findings(ctx, run.ID, report.Filter{Code: "metadata_unverified", Format: "EPUB"}, nil)
			want := evidence.Finding{Code: "metadata_unverified", Format: "EPUB", Field: "title", Reason: "capture_limit_unverified"}
			if err != nil || len(page.Rows) != 1 || page.Rows[0].Finding != want {
				t.Fatalf("historical current view=%+v/%v", page, err)
			}
			book, err := service.Book(ctx, run.ID, ref.ID)
			if err != nil || book.Package == nil || version == 1 && len(book.Package.Findings) != 0 {
				t.Fatalf("historical package changed=%+v/%v", book, err)
			}
			stored, storedSeal, err := r.Evidence(ctx, run.ID, ref.ID)
			if err != nil || storedSeal != seal {
				t.Fatalf("stored seal=%s/%v", storedSeal, err)
			}
			canonical, err := evidence.Canonical(json.RawMessage(stored))
			if err != nil || !bytes.Equal(canonical, raw) {
				t.Fatal("report changed historical bytes")
			}
			var marker, n int
			if err := s.Pool.QueryRow(ctx, `SELECT projection_version,(SELECT count(*) FROM findings) FROM evidence`).Scan(&marker, &n); err != nil || marker != version || n != 0 {
				t.Fatalf("report wrote projection=%d/%d/%v", marker, n, err)
			}
		})
	}
}

func TestHistoricalV2Reports(t *testing.T) {
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
	p.FindingsVersion = 2
	if p.Findings, err = findings.DeriveV2(ctx, p); err != nil || len(p.Findings) != 1 {
		t.Fatalf("v2 findings %+v/%v", p.Findings, err)
	}
	raw, seal, err := evidence.Seal(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE run_books SET state='checked' WHERE run_id=$1`, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `INSERT INTO evidence(run_id,book_id,uuid,schema_version,package,seal,projection_version) VALUES($1,$2,$3,1,$4::jsonb,$5,2)`, run.ID, ref.ID, ref.UUID, string(raw), seal); err != nil {
		t.Fatal(err)
	}
	f := p.Findings[0]
	if _, err := s.Pool.Exec(ctx, `INSERT INTO findings(run_id,book_id,ordinal,code,format,field,reason) VALUES($1,$2,0,$3,$4,$5,$6)`, run.ID, ref.ID, f.Code, f.Format, f.Field, f.Reason); err != nil {
		t.Fatal(err)
	}
	service := report.New(NewReportRepo(s))
	page, err := service.Findings(ctx, run.ID, report.Filter{Code: "file_missing"}, nil)
	if err != nil || len(page.Rows) != 1 || page.Rows[0].Finding != f {
		t.Fatalf("v2 report=%+v/%v", page, err)
	}
	book, err := service.Book(ctx, run.ID, ref.ID)
	current, derr := findings.Derive(ctx, p)
	if err != nil || derr != nil || !slices.Equal(book.Findings, current) || book.Package.FindingsVersion != 2 {
		t.Fatalf("v2 book=%+v/%v/%v", book, err, derr)
	}
	stored, storedSeal, err := r.Evidence(ctx, run.ID, ref.ID)
	if err != nil || storedSeal != seal {
		t.Fatalf("stored seal=%s/%v", storedSeal, err)
	}
	if canonical, err := evidence.Canonical(json.RawMessage(stored)); err != nil || !bytes.Equal(canonical, raw) {
		t.Fatal("report changed historical bytes")
	}
	var marker int
	if err := s.Pool.QueryRow(ctx, `SELECT projection_version FROM evidence`).Scan(&marker); err != nil || marker != 2 {
		t.Fatalf("marker=%d/%v", marker, err)
	}
}
