package store

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/decide"
	"github.com/felixapel/calibre-bookwarden/internal/duplicates"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/report"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

const detailedHash = 0x0f0f_0f0f_0f0f_0f0f

// duplicateRun checks three books sharing an ISBN and a title and authors.
// Books 10 and 20 share a file and a comparable cover; book 30 has its own
// file and a placeholder cover.
func duplicateRun(t *testing.T, r *RunRepo) (verify.Run, []library.BookRef, []evidence.Package) {
	t.Helper()
	ctx := context.Background()
	books := refs(3)
	run, err := r.CreateRun(ctx, verify.Scope{}, "library", "v1", books)
	if err != nil {
		t.Fatal(err)
	}
	var pkgs []evidence.Package
	for i, ref := range books {
		p := cleanEvidence(ref)
		p.Formats[0].Size, p.Formats[0].SHA256 = 10, strings.Repeat("a", 64)
		p.Calibre.HasCover = true
		p.Cover = &evidence.CoverEvidence{State: evidence.StateOK, Bytes: 10, SHA256: strings.Repeat("c", 64), Detected: "jpeg", Width: 800, Height: 1200, DHash: "0f0f0f0f0f0f0f0f", Flatness: 400}
		if i == 2 {
			p.Formats[0].SHA256 = strings.Repeat("b", 64)
			p.Cover.Flatness = 5
		}
		raw, seal, err := decide.Seal(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		if err := r.FinishBook(ctx, run.ID, ref, verify.Outcome{State: verify.BookChecked, Evidence: raw, Seal: seal}); err != nil {
			t.Fatal(err)
		}
		pkgs = append(pkgs, p)
	}
	return run, books, pkgs
}

func TestDuplicateProjection(t *testing.T) {
	ctx := context.Background()
	r, s := runRepo(t)
	run, books, pkgs := duplicateRun(t, r)
	if got, err := r.Run(ctx, run.ID); err != nil || run.DuplicatesVersion != duplicates.Version || got.DuplicatesVersion != duplicates.Version {
		t.Fatalf("duplicates version %d/%d, %v", run.DuplicatesVersion, got.DuplicatesVersion, err)
	}

	snap, err := NewReportRepo(s).Snapshot(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = snap.Close(ctx) }()
	rows, covers, err := snap.DuplicateRows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var want []duplicates.KeyRow
	for i, p := range pkgs {
		for _, k := range duplicates.Keys(p) {
			if k.Kind == duplicates.KindFile && i == 2 {
				continue // book 30's file is its own
			}
			want = append(want, duplicates.KeyRow{BookID: books[i].ID, Key: k})
		}
	}
	sortRows := func(rs []duplicates.KeyRow) {
		slices.SortFunc(rs, func(a, b duplicates.KeyRow) int {
			return strings.Compare(a.Kind+a.Value+string(rune(a.BookID)), b.Kind+b.Value+string(rune(b.BookID)))
		})
	}
	sortRows(rows)
	sortRows(want)
	if len(want) != 8 || !slices.Equal(rows, want) {
		t.Fatalf("DuplicateRows keys\n got %+v\nwant %+v", rows, want)
	}
	if !slices.Equal(covers, []duplicates.Cover{{BookID: 10, Hash: detailedHash}, {BookID: 20, Hash: detailedHash}}) {
		t.Fatalf("comparable covers %+v", covers)
	}
	keys, cover, err := snap.BookKeys(ctx, 30)
	if err != nil || !slices.Equal(keys, duplicates.Keys(pkgs[2])) || cover != nil {
		t.Fatalf("BookKeys(30) = %+v, %+v, %v", keys, cover, err)
	}
	if _, cover, err := snap.BookKeys(ctx, 10); err != nil || cover == nil || *cover != (duplicates.Cover{BookID: 10, Hash: detailedHash}) {
		t.Fatalf("BookKeys(10) cover %+v, %v", cover, err)
	}

	if near, err := snap.NearCovers(ctx, 10, detailedHash); err != nil || !slices.Equal(near, []duplicates.Cover{{BookID: 20, Hash: detailedHash}}) {
		t.Fatalf("NearCovers(10) = %+v, %v", near, err)
	}
	if near, err := snap.NearCovers(ctx, 10, detailedHash^0xf); err != nil || len(near) != 1 {
		t.Fatalf("NearCovers at distance 4 = %+v, %v", near, err)
	}
	if near, err := snap.NearCovers(ctx, 10, detailedHash^0x1f); err != nil || len(near) != 0 {
		t.Fatalf("NearCovers at distance 5 = %+v, %v", near, err)
	}
	// Book 30 shares its ISBN and its title and authors with 10 and 20; its
	// file is its own.
	group, err := snap.BookGroupRows(ctx, 30)
	if err != nil || len(group) != 7 {
		t.Fatalf("BookGroupRows(30) = %d rows %+v, %v", len(group), group, err)
	}
	if _, err := s.Pool.Exec(ctx, `DELETE FROM runs WHERE id=$1`, run.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM duplicate_keys`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("duplicate keys left after the run: %d, %v", n, err)
	}
}

func TestDuplicateKeysChecked(t *testing.T) {
	ctx := context.Background()
	r, s := runRepo(t)
	run, books, _ := duplicateRun(t, r)
	for _, tt := range []struct{ kind, value string }{
		{"isbn", "9780306406158x"},
		{"file", "ABC"},
		{"title_author", strings.Repeat("A", 64)},
		{"series", strings.Repeat("a", 64)},
	} {
		if _, err := s.Pool.Exec(ctx, `INSERT INTO duplicate_keys (run_id, book_id, kind, value, detail) VALUES ($1,$2,$3,$4,'')`, run.ID, books[0].ID, tt.kind, tt.value); err == nil {
			t.Errorf("stored a %s key %q", tt.kind, tt.value)
		}
	}
}

func TestDuplicatesTampered(t *testing.T) {
	ctx := context.Background()
	for name, tamper := range map[string]string{
		"a key added":       `INSERT INTO duplicate_keys (run_id, book_id, kind, value, detail) VALUES ($1, 30, 'file', repeat('a', 64), 'EPUB')`,
		"a cover hash edit": `UPDATE covers SET dhash = dhash # 1 WHERE run_id=$1 AND book_id=20`,
	} {
		t.Run(name, func(t *testing.T) {
			r, s := runRepo(t)
			run, _, _ := duplicateRun(t, r)
			service := report.New(NewReportRepo(s))
			if _, err := service.Duplicates(ctx, run.ID, report.DuplicateFilter{Page: 1}); err != nil {
				t.Fatalf("before: %v", err)
			}
			if _, err := s.Pool.Exec(ctx, tamper, run.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := service.Duplicates(ctx, run.ID, report.DuplicateFilter{Page: 1}); !errors.Is(err, report.ErrIntegrity) {
				t.Fatalf("after: %v", err)
			}
		})
	}
}
