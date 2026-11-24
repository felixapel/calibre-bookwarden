package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/felixapel/calibre-bookwarden/internal/decide"
	"github.com/felixapel/calibre-bookwarden/internal/duplicates"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/findings"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/report"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

type ReportRepo struct{ store *Store }

func NewReportRepo(s *Store) *ReportRepo { return &ReportRepo{s} }

type reportSnapshot struct {
	tx  pgx.Tx
	run verify.Run
}

func (r *ReportRepo) Snapshot(ctx context.Context, id int64) (report.Snapshot, error) {
	tx, err := r.store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, fmt.Errorf("store: report snapshot: %w", err)
	}
	run, err := scanRun(tx.QueryRow(ctx, `SELECT `+runColumns+` FROM runs WHERE id=$1`, id))
	if err != nil {
		_ = tx.Rollback(ctx)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, verify.ErrNoSuchRun
		}
		return nil, fmt.Errorf("store: report run: %w", err)
	}
	return &reportSnapshot{tx: tx, run: run}, nil
}
func (s *reportSnapshot) Run() verify.Run { return s.run }
func (s *reportSnapshot) Close(context.Context) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := s.tx.Rollback(ctx)
	if errors.Is(err, pgx.ErrTxClosed) {
		return nil
	}
	return err
}
func (s *reportSnapshot) NextBook(ctx context.Context, after int) (report.Book, bool, error) {
	var id int
	err := s.tx.QueryRow(ctx, `SELECT book_id FROM run_books WHERE run_id=$1 AND book_id>$2 ORDER BY book_id LIMIT 1`, s.run.ID, after).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return report.Book{}, false, nil
	}
	if err != nil {
		return report.Book{}, false, fmt.Errorf("store: report next book: %w", err)
	}
	b, err := s.Book(ctx, id)
	if err != nil {
		return report.Book{Ref: library.BookRef{ID: id}}, true, fmt.Errorf("book %d: %w", id, err)
	}
	return b, true, nil
}
func (s *reportSnapshot) Book(ctx context.Context, id int) (report.Book, error) {
	var b report.Book
	var state string
	var raw, seal, uuid *string
	var schema, version *int
	err := s.tx.QueryRow(ctx, `SELECT b.book_id,b.uuid,b.state,b.reason,e.package::text,e.seal,e.uuid,e.schema_version,e.projection_version
 FROM run_books b LEFT JOIN evidence e ON e.run_id=b.run_id AND e.book_id=b.book_id WHERE b.run_id=$1 AND b.book_id=$2`, s.run.ID, id).
		Scan(&b.Ref.ID, &b.Ref.UUID, &state, &b.Reason, &raw, &seal, &uuid, &schema, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return report.Book{}, report.ErrNoSuchBook
	}
	if err != nil {
		return report.Book{}, fmt.Errorf("store: report book: %w", err)
	}
	b.State = verify.BookState(state)
	switch b.State {
	case verify.BookPending, verify.BookFailed, verify.BookSourceChanged:
		if raw != nil {
			return report.Book{}, report.ErrIntegrity
		}
		return b, nil
	case verify.BookChecked:
	default:
		return report.Book{}, report.ErrIntegrity
	}
	if raw == nil || seal == nil || uuid == nil || schema == nil || version == nil {
		return report.Book{}, report.ErrIntegrity
	}
	p, decision, err := decide.Open(ctx, []byte(*raw), *seal)
	if err != nil {
		if ctx.Err() != nil {
			return report.Book{}, ctx.Err()
		}
		return report.Book{}, report.ErrIntegrity
	}
	if p.LibraryID != s.run.LibraryID || p.BookID != b.Ref.ID || p.UUID != b.Ref.UUID || *uuid != b.Ref.UUID || *schema != p.SchemaVersion {
		return report.Book{}, report.ErrIntegrity
	}
	if *version < 0 || *version > findings.Version || p.FindingsVersion > *version {
		return report.Book{}, report.ErrIntegrity
	}
	current := p.Findings
	if p.FindingsVersion != 0 && p.FindingsVersion < findings.Version {
		current, err = findings.Derive(ctx, p)
		if err != nil {
			if ctx.Err() != nil {
				return report.Book{}, ctx.Err()
			}
			return report.Book{}, report.ErrIntegrity
		}
	}
	var projected []evidence.Finding
	switch *version {
	case findings.Version:
		projected = current
	case 1, 2, 3, 4, 5:
		projected, err = findings.DeriveVersion(ctx, p, *version)
		if err != nil {
			if ctx.Err() != nil {
				return report.Book{}, ctx.Err()
			}
			return report.Book{}, report.ErrIntegrity
		}
	}
	rows, err := s.tx.Query(ctx, `SELECT ordinal,code,format,field,reason FROM findings WHERE run_id=$1 AND book_id=$2 ORDER BY ordinal`, s.run.ID, id)
	if err != nil {
		return report.Book{}, fmt.Errorf("store: report projection: %w", err)
	}
	defer rows.Close()
	i := 0
	for rows.Next() {
		var ordinal int
		var code, format, field, reason string
		if err := rows.Scan(&ordinal, &code, &format, &field, &reason); err != nil {
			return report.Book{}, fmt.Errorf("store: report finding: %w", err)
		}
		if *version == 0 || i >= len(projected) {
			return report.Book{}, report.ErrIntegrity
		}
		expected := projected[i]
		if ordinal != i || code != expected.Code || format != expected.Format || field != expected.Field || reason != expected.Reason {
			return report.Book{}, report.ErrIntegrity
		}
		i++
	}
	if err := rows.Err(); err != nil {
		return report.Book{}, fmt.Errorf("store: report findings: %w", err)
	}
	if *version != 0 && i != len(projected) {
		return report.Book{}, report.ErrIntegrity
	}
	b.Package = &p
	b.Decision = decision
	b.FindingsVersion = findings.Version
	// Decision-derived entries are shown with the findings but never sealed.
	b.Findings = append(slices.Clone(current), decision.Findings()...)
	return b, nil
}

func (s *reportSnapshot) DuplicateRows(ctx context.Context) ([]duplicates.KeyRow, []duplicates.Cover, error) {
	rows, err := s.tx.Query(ctx, `SELECT book_id, kind, value, detail FROM duplicate_keys k WHERE run_id=$1
 AND EXISTS (SELECT 1 FROM duplicate_keys o WHERE o.run_id=k.run_id AND o.kind=k.kind AND o.value=k.value AND o.book_id<>k.book_id)
 ORDER BY kind, value, book_id`, s.run.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("store: duplicate keys: %w", err)
	}
	keys, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (duplicates.KeyRow, error) {
		var k duplicates.KeyRow
		err := row.Scan(&k.BookID, &k.Kind, &k.Value, &k.Detail)
		return k, err
	})
	if err != nil {
		return nil, nil, fmt.Errorf("store: duplicate keys: %w", err)
	}
	rows, err = s.tx.Query(ctx, `SELECT book_id, dhash FROM covers WHERE run_id=$1 AND comparable ORDER BY book_id`, s.run.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("store: comparable covers: %w", err)
	}
	covers, err := pgx.CollectRows(rows, scanCover)
	if err != nil {
		return nil, nil, fmt.Errorf("store: comparable covers: %w", err)
	}
	return keys, covers, nil
}

func (s *reportSnapshot) BookKeys(ctx context.Context, id int) ([]duplicates.Key, *duplicates.Cover, error) {
	rows, err := s.tx.Query(ctx, `SELECT kind, value, detail FROM duplicate_keys WHERE run_id=$1 AND book_id=$2 ORDER BY kind, value`, s.run.ID, id)
	if err != nil {
		return nil, nil, fmt.Errorf("store: book duplicate keys: %w", err)
	}
	keys, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (duplicates.Key, error) {
		var k duplicates.Key
		err := row.Scan(&k.Kind, &k.Value, &k.Detail)
		return k, err
	})
	if err != nil {
		return nil, nil, fmt.Errorf("store: book duplicate keys: %w", err)
	}
	rows, err = s.tx.Query(ctx, `SELECT book_id, dhash FROM covers WHERE run_id=$1 AND book_id=$2 AND comparable`, s.run.ID, id)
	if err != nil {
		return nil, nil, fmt.Errorf("store: book cover: %w", err)
	}
	covers, err := pgx.CollectRows(rows, scanCover)
	if err != nil || len(covers) == 0 {
		return keys, nil, err
	}
	return keys, &covers[0], nil
}

func scanCover(row pgx.CollectableRow) (duplicates.Cover, error) {
	var c duplicates.Cover
	var h int64
	err := row.Scan(&c.BookID, &h)
	c.Hash = uint64(h) //nolint:gosec // the bigint holds the hash's bits
	return c, err
}

func (s *reportSnapshot) BookGroupRows(ctx context.Context, id int) ([]duplicates.KeyRow, error) {
	rows, err := s.tx.Query(ctx, `SELECT k.book_id, k.kind, k.value, k.detail FROM duplicate_keys k
 JOIN duplicate_keys mine ON mine.run_id=k.run_id AND mine.kind=k.kind AND mine.value=k.value
 WHERE k.run_id=$1 AND mine.book_id=$2 ORDER BY k.kind, k.value, k.book_id`, s.run.ID, id)
	if err != nil {
		return nil, fmt.Errorf("store: book duplicate groups: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (duplicates.KeyRow, error) {
		var k duplicates.KeyRow
		err := row.Scan(&k.BookID, &k.Kind, &k.Value, &k.Detail)
		return k, err
	})
	if err != nil {
		return nil, fmt.Errorf("store: book duplicate groups: %w", err)
	}
	return out, nil
}

func (s *reportSnapshot) NearCovers(ctx context.Context, id int, h uint64) ([]duplicates.Cover, error) {
	rows, err := s.tx.Query(ctx, `SELECT book_id, dhash FROM covers WHERE run_id=$1 AND comparable AND book_id<>$2
 AND bit_count((dhash # $3)::bit(64)) <= $4 ORDER BY book_id`, s.run.ID, id, int64(h), duplicates.MaxCoverDistance) //nolint:gosec // the bigint holds the hash's bits
	if err != nil {
		return nil, fmt.Errorf("store: near covers: %w", err)
	}
	out, err := pgx.CollectRows(rows, scanCover)
	if err != nil {
		return nil, fmt.Errorf("store: near covers: %w", err)
	}
	return out, nil
}
