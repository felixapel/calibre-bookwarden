package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/findings"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

// Raw extractor answers (M4 section 8) are stored by the hash their package
// seals. The errors never carry the body.
var (
	// ErrRawMissing: no raw answer has that hash.
	ErrRawMissing = errors.New("store: raw extraction output not found")
	// ErrRawCorrupt: the stored body doesn't match its hash.
	ErrRawCorrupt = errors.New("store: raw extraction output doesn't match its hash")

	errInvalidRaws = fmt.Errorf("store: raw extraction outputs don't match the package: %w", findings.ErrInvalidEvidence)
)

// rawHash is the sha256 of the body, in lowercase hex.
func rawHash(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

// validRawBody: text Postgres can hold, with the hash it is stored under.
func validRawBody(hash, body string) bool {
	return utf8.ValidString(body) && !strings.ContainsRune(body, 0) && rawHash(body) == hash
}

// checkRaws requires the outcome to carry exactly the raw outputs the package
// refers to (formats[].extraction.extractors[].raw_sha256), each hashing to
// its SHA256, and a well-formed cache key where one is set.
func checkRaws(p evidence.Package, raws []verify.RawOutput) error {
	refs := map[string]bool{}
	for _, f := range p.Formats {
		if f.Extraction == nil {
			continue
		}
		for _, e := range f.Extraction.Extractors {
			if e.RawSHA256 != "" {
				refs[e.RawSHA256] = true
			}
		}
	}
	have := map[string]bool{}
	for _, r := range raws {
		if !refs[r.SHA256] || !validRawBody(r.SHA256, r.Body) || r.Key != "" && !evidence.ValidExcerptHash(r.Key) {
			return errInvalidRaws
		}
		have[r.SHA256] = true
	}
	if len(have) != len(refs) {
		return errInvalidRaws
	}
	return nil
}

// insertRawLinks records which raw outputs a book's package refers to, in the
// transaction that writes the package. See RunRepo.Prune for why they come
// before the bodies.
func insertRawLinks(ctx context.Context, tx pgx.Tx, runID int64, bookID int, raws []verify.RawOutput) error {
	shas := make([]string, 0, len(raws))
	for _, r := range raws {
		shas = append(shas, r.SHA256)
	}
	slices.Sort(shas)
	for _, sha := range slices.Compact(shas) {
		if _, err := tx.Exec(ctx, `INSERT INTO evidence_extraction_raw (run_id, book_id, sha256) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`, runID, bookID, sha); err != nil {
			return fmt.Errorf("store: raw link %.8s: %w", sha, err)
		}
	}
	return nil
}

// insertRaws writes the bodies, then the cache rows. A raw is content
// addressed and was checked against its hash, so an existing row is left
// alone. A key that already points elsewhere moves to the newest answer.
// Both go in sorted order, so that two books that share answers never wait
// for each other in opposite orders.
func insertRaws(ctx context.Context, tx pgx.Tx, raws []verify.RawOutput) error {
	sorted := slices.Clone(raws)
	slices.SortFunc(sorted, func(a, b verify.RawOutput) int { return strings.Compare(a.SHA256, b.SHA256) })
	for _, r := range sorted {
		if _, err := tx.Exec(ctx, `INSERT INTO extraction_raw (sha256, body) VALUES ($1, $2) ON CONFLICT (sha256) DO NOTHING`, r.SHA256, r.Body); err != nil {
			return fmt.Errorf("store: raw %.8s: %w", r.SHA256, err)
		}
	}
	slices.SortFunc(sorted, func(a, b verify.RawOutput) int { return strings.Compare(a.Key, b.Key) })
	for _, r := range sorted {
		if r.Key == "" {
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO extraction_cache (key, sha256) VALUES ($1, $2)
			ON CONFLICT (key) DO UPDATE SET sha256 = EXCLUDED.sha256, created_at = now()`, r.Key, r.SHA256); err != nil {
			return fmt.Errorf("store: cache %.8s: %w", r.Key, err)
		}
	}
	return nil
}

// CachedRaw returns the raw answer cached under key. A miss, a malformed key
// and a stored body that no longer hashes to its hash are all ok=false: a
// corrupt cache row is simply not used.
func (r *RunRepo) CachedRaw(ctx context.Context, key string) (sha, body string, ok bool, err error) {
	if !evidence.ValidExcerptHash(key) {
		return "", "", false, nil
	}
	err = r.pool.QueryRow(ctx, `SELECT w.sha256, w.body FROM extraction_cache c JOIN extraction_raw w ON w.sha256 = c.sha256 WHERE c.key = $1`, key).Scan(&sha, &body)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, fmt.Errorf("store: cached raw %.8s: %w", key, err)
	}
	if !validRawBody(sha, body) {
		return "", "", false, nil
	}
	return sha, body, true, nil
}

// Raw returns the raw answer with the given hash. The body is hashed again on
// every read: one that doesn't match is ErrRawCorrupt, and a hash nothing was
// stored under is ErrRawMissing.
func (r *ReportRepo) Raw(ctx context.Context, sha string) (string, error) {
	if !evidence.ValidExcerptHash(sha) {
		return "", ErrRawMissing
	}
	var body string
	err := r.store.Pool.QueryRow(ctx, `SELECT body FROM extraction_raw WHERE sha256=$1`, sha).Scan(&body)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrRawMissing
	}
	if err != nil {
		return "", fmt.Errorf("store: read raw %.8s: %w", sha, err)
	}
	if !validRawBody(sha, body) {
		return "", fmt.Errorf("%.8s: %w", sha, ErrRawCorrupt)
	}
	return body, nil
}

// ExtractionRef is one format's sealed extraction with what re-verifying it
// needs: the excerpt's body and the raw answers it refers to, by hash.
// Missing says the body or a raw answer is absent or no longer matches its
// hash; then Body and Raws are not handed over.
type ExtractionRef struct {
	BookID     int
	Excerpt    evidence.Excerpt
	Body       string
	Extraction evidence.Extraction
	Raws       map[string]string
	Missing    bool
}

// Extractions calls visit for every sealed extraction of a run, in book
// order. A package whose seal doesn't hold is skipped; the seal check reports
// it. An unknown run is verify.ErrNoSuchRun.
func (r *ReportRepo) Extractions(ctx context.Context, runID int64, visit func(ExtractionRef) error) error {
	var exists bool
	if err := r.store.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM runs WHERE id=$1)`, runID).Scan(&exists); err != nil {
		return fmt.Errorf("store: run %d extractions: %w", runID, err)
	}
	if !exists {
		return verify.ErrNoSuchRun
	}
	return r.visitExtractions(ctx, runID, `SELECT book_id, package::text, seal FROM evidence WHERE run_id=$1 AND package::text LIKE '%"extraction"%' ORDER BY book_id`, []any{runID}, visit)
}

// BookExtractions is Extractions for one book: it calls visit for each of the
// book's sealed extractions in a run, and not at all for a book that has
// none, or in a run that has no such book. The fix path re-verifies a text
// fix with it (ADR-036 section 5).
func (r *ReportRepo) BookExtractions(ctx context.Context, runID int64, bookID int, visit func(ExtractionRef) error) error {
	return r.visitExtractions(ctx, runID, `SELECT book_id, package::text, seal FROM evidence WHERE run_id=$1 AND book_id=$2 AND package::text LIKE '%"extraction"%' ORDER BY book_id`, []any{runID, bookID}, visit)
}

func (r *ReportRepo) visitExtractions(ctx context.Context, runID int64, query string, args []any, visit func(ExtractionRef) error) error {
	rows, err := r.store.Pool.Query(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("store: run %d extractions: %w", runID, err)
	}
	type pkgRow struct {
		book      int
		raw, seal string
	}
	var packages []pkgRow
	for rows.Next() {
		var p pkgRow
		if err := rows.Scan(&p.book, &p.raw, &p.seal); err != nil {
			rows.Close()
			return fmt.Errorf("store: run %d extractions: %w", runID, err)
		}
		packages = append(packages, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: run %d extractions: %w", runID, err)
	}
	for _, row := range packages {
		p, err := evidence.Open([]byte(row.raw), row.seal)
		if err != nil {
			continue
		}
		for _, f := range p.Formats {
			if f.Extraction == nil || f.Excerpt == nil {
				continue
			}
			ref := ExtractionRef{BookID: row.book, Excerpt: *f.Excerpt, Extraction: *f.Extraction, Raws: map[string]string{}}
			if ref.Body, err = r.Excerpt(ctx, f.Excerpt.SHA256); err != nil {
				if !errors.Is(err, ErrExcerptMissing) && !errors.Is(err, ErrExcerptCorrupt) {
					return err
				}
				ref.Missing = true
			}
			for _, e := range f.Extraction.Extractors {
				if e.RawSHA256 == "" || ref.Missing {
					continue
				}
				body, err := r.Raw(ctx, e.RawSHA256)
				if errors.Is(err, ErrRawMissing) || errors.Is(err, ErrRawCorrupt) {
					ref.Missing = true
					continue
				}
				if err != nil {
					return err
				}
				ref.Raws[e.RawSHA256] = body
			}
			if ref.Missing {
				ref.Body, ref.Raws = "", map[string]string{}
			}
			if err := visit(ref); err != nil {
				return err
			}
		}
	}
	return nil
}

// ExtractorCalls counts the extractor calls a run made, from the packages it
// sealed: answers served from the cache are not calls (Cached), and failed
// are the entries whose state is not ok. That includes unavailable written
// when nothing was called (quota pause, per-run cap); the sealed data can't
// tell the two apart, so they count as not answered. A run without extraction counts 0, 0.
func (r *RunRepo) ExtractorCalls(ctx context.Context, runID int64) (calls, failed int, err error) {
	err = r.pool.QueryRow(ctx, `SELECT count(*)::int, count(*) FILTER (WHERE x->>'state' IS DISTINCT FROM 'ok')::int
		FROM evidence e
		CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(e.package->'formats') = 'array' THEN e.package->'formats' ELSE '[]'::jsonb END) f
		CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(f->'extraction'->'extractors') = 'array' THEN f->'extraction'->'extractors' ELSE '[]'::jsonb END) x
		WHERE e.run_id = $1 AND COALESCE((x->>'cached')::boolean, false) = false`, runID).Scan(&calls, &failed)
	if err != nil {
		return 0, 0, fmt.Errorf("count extractor calls: %w", err)
	}
	return calls, failed, nil
}
