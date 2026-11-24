package store

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/findings"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

// Excerpts (ADR-032 part 2) are stored by the hash their package seals. The
// errors never carry text.
var (
	// ErrExcerptMissing: no excerpt has that hash.
	ErrExcerptMissing = errors.New("store: excerpt not found")
	// ErrExcerptCorrupt: the stored body doesn't match its hash or length.
	ErrExcerptCorrupt = errors.New("store: excerpt doesn't match its hash")

	errInvalidExcerpts = fmt.Errorf("store: excerpt bodies don't match the package: %w", findings.ErrInvalidEvidence)
)

// checkExcerptBodies requires the outcome to carry exactly the bodies the
// package refers to, each with its hash and length, as valid text Postgres
// can hold.
func checkExcerptBodies(p evidence.Package, bodies map[string]string) error {
	refs, err := evidence.ExcerptRefs(p)
	if err != nil || len(bodies) != len(refs) {
		return errInvalidExcerpts
	}
	for hash, chars := range refs {
		body, ok := bodies[hash]
		if !ok || !validExcerptBody(hash, chars, body) {
			return errInvalidExcerpts
		}
	}
	return nil
}

func validExcerptBody(hash string, chars int, body string) bool {
	return utf8.ValidString(body) && !strings.ContainsRune(body, 0) &&
		utf8.RuneCountInString(body) == chars && evidence.ExcerptHash(body) == hash
}

// insertExcerptLinks records which excerpts a book's package refers to, in
// the transaction that writes the package. See RunRepo.Prune for why they come
// before the bodies.
func insertExcerptLinks(ctx context.Context, tx pgx.Tx, runID int64, bookID int, bodies map[string]string) error {
	for _, hash := range slices.Sorted(maps.Keys(bodies)) {
		if _, err := tx.Exec(ctx, `INSERT INTO evidence_excerpts (run_id, book_id, sha256) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`, runID, bookID, hash); err != nil {
			return fmt.Errorf("store: excerpt link %.8s: %w", hash, err)
		}
	}
	return nil
}

// insertExcerpts writes the bodies. The body in hand was checked against its
// hash, so it also repairs a row that no longer matches. It writes them in hash order, so that two books that
// share excerpts never wait for each other in opposite orders.
func insertExcerpts(ctx context.Context, tx pgx.Tx, bodies map[string]string) error {
	for _, hash := range slices.Sorted(maps.Keys(bodies)) {
		body := bodies[hash]
		if _, err := tx.Exec(ctx, `INSERT INTO excerpts (sha256, body, chars) VALUES ($1, $2, $3) ON CONFLICT (sha256) DO UPDATE SET body = EXCLUDED.body, chars = EXCLUDED.chars
			WHERE excerpts.body IS DISTINCT FROM EXCLUDED.body OR excerpts.chars IS DISTINCT FROM EXCLUDED.chars`,
			hash, body, utf8.RuneCountInString(body)); err != nil {
			return fmt.Errorf("store: excerpt %.8s: %w", hash, err)
		}
	}
	return nil
}

// Excerpt returns the excerpt with the given hash. The body is hashed again
// on every read: a body that doesn't match its hash or its stored length is
// ErrExcerptCorrupt, and a hash nothing was stored under is ErrExcerptMissing.
func (r *ReportRepo) Excerpt(ctx context.Context, hash string) (string, error) {
	if !evidence.ValidExcerptHash(hash) {
		return "", ErrExcerptMissing
	}
	var body string
	var chars int
	err := r.store.Pool.QueryRow(ctx, `SELECT body, chars FROM excerpts WHERE sha256=$1`, hash).Scan(&body, &chars)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrExcerptMissing
	}
	if err != nil {
		return "", fmt.Errorf("store: read excerpt %.8s: %w", hash, err)
	}
	if !validExcerptBody(hash, chars, body) {
		return "", fmt.Errorf("%.8s: %w", hash, ErrExcerptCorrupt)
	}
	return body, nil
}

// ExcerptCheck is what CheckExcerpts found in one run: the excerpts its
// packages refer to, and how many of them are absent or no longer match.
type ExcerptCheck struct {
	Referenced, Missing, Corrupt int
}

// CheckExcerpts opens every package of a run that refers to an excerpt and
// checks that each one exists and matches its hash. A package that doesn't
// open is not counted here; the seal check reports it.
func (r *ReportRepo) CheckExcerpts(ctx context.Context, runID int64) (ExcerptCheck, error) {
	rows, err := r.store.Pool.Query(ctx, `SELECT package::text, seal FROM evidence WHERE run_id=$1 AND package::text LIKE '%"excerpt"%' ORDER BY book_id`, runID)
	if err != nil {
		return ExcerptCheck{}, fmt.Errorf("store: run %d excerpts: %w", runID, err)
	}
	type pkgRow struct{ raw, seal string }
	var packages []pkgRow
	for rows.Next() {
		var p pkgRow
		if err := rows.Scan(&p.raw, &p.seal); err != nil {
			rows.Close()
			return ExcerptCheck{}, fmt.Errorf("store: run %d excerpts: %w", runID, err)
		}
		packages = append(packages, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return ExcerptCheck{}, fmt.Errorf("store: run %d excerpts: %w", runID, err)
	}
	var c ExcerptCheck
	for _, row := range packages {
		p, err := evidence.Open([]byte(row.raw), row.seal)
		if err != nil {
			continue
		}
		refs, err := evidence.ExcerptRefs(p)
		if err != nil {
			continue
		}
		for _, hash := range slices.Sorted(maps.Keys(refs)) {
			c.Referenced++
			switch _, err := r.Excerpt(ctx, hash); {
			case errors.Is(err, ErrExcerptMissing):
				c.Missing++
			case errors.Is(err, ErrExcerptCorrupt):
				c.Corrupt++
			case err != nil:
				return ExcerptCheck{}, err
			}
		}
	}
	return c, nil
}

// StoredExcerpt is an EPUB excerpt of one book in a run, with its body.
type StoredExcerpt struct {
	BookID int
	Method string
	Units  []evidence.ExcerptUnit
	Text   string
}

// SampleExcerpts returns up to n EPUB excerpts of a run, one per distinct
// body, in an order fixed by seed: the same seed gives the same sample. Bodies
// are hashed again on read. It only reads (extract-bench, design section 4a).
func (r *ReportRepo) SampleExcerpts(ctx context.Context, runID int64, n int, seed string) ([]StoredExcerpt, error) {
	var exists bool
	if err := r.store.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM runs WHERE id=$1)`, runID).Scan(&exists); err != nil {
		return nil, fmt.Errorf("store: sample excerpts: %w", err)
	}
	if !exists {
		return nil, verify.ErrNoSuchRun
	}
	rows, err := r.store.Pool.Query(ctx, `SELECT book_id, package::text, seal FROM evidence
		WHERE run_id=$1 AND package::text LIKE '%"excerpt"%' ORDER BY md5($2 || book_id::text)`, runID, seed)
	if err != nil {
		return nil, fmt.Errorf("store: sample excerpts: %w", err)
	}
	type ref struct {
		book int
		ex   evidence.Excerpt
	}
	var refs []ref
	seen := map[string]bool{}
	for rows.Next() && len(refs) < n {
		var book int
		var raw, seal string
		if err := rows.Scan(&book, &raw, &seal); err != nil {
			rows.Close()
			return nil, fmt.Errorf("store: sample excerpts: %w", err)
		}
		// Units are read only from a package whose seal holds.
		pkg, err := evidence.Open([]byte(raw), seal)
		if err != nil {
			continue
		}
		for _, f := range pkg.Formats {
			if e := f.Excerpt; e != nil && e.Method == evidence.ExcerptEPUBText && !seen[e.SHA256] && len(refs) < n {
				seen[e.SHA256] = true
				refs = append(refs, ref{book: book, ex: *e})
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: sample excerpts: %w", err)
	}
	out := make([]StoredExcerpt, 0, len(refs))
	for _, rf := range refs {
		body, err := r.Excerpt(ctx, rf.ex.SHA256)
		if err != nil {
			return nil, err
		}
		out = append(out, StoredExcerpt{BookID: rf.book, Method: rf.ex.Method, Units: rf.ex.Units, Text: body})
	}
	return out, nil
}
