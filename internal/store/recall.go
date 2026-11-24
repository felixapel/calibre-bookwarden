package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/felixapel/calibre-bookwarden/internal/decide"
	"github.com/felixapel/calibre-bookwarden/internal/findings"
)

// The fields a recall sample asks about (v1 design section 6.4), in the
// order the page shows them.
var RecallFields = []string{"title", "authors", "publisher", "pubdate", "isbn", "languages"}

// The answers to one field of one sampled book.
const (
	AnswerRight = "calibre_right"
	AnswerWrong = "calibre_wrong"
	AnswerSkip  = "skip"
)

// ErrNoCheckedRun means no finished whole-library run has a passed seal
// check, so there is no verified projection to sample.
var ErrNoCheckedRun = errors.New("store: no checked run to sample")

// ErrNoSuchSample means the sample, or its book, doesn't exist.
var ErrNoSuchSample = errors.New("store: no such sample")

// ErrSampleClosed means the sample's results were shown, so its answers are
// final.
var ErrSampleClosed = errors.New("store: the sample is closed")

// RecallRepo reads the library for a recall sample and keeps the sample and
// the confirmations. A confirmation is not a label (design section 6.4): this
// repository never reads or writes the proposals, the decision labels or any
// other decision table.
type RecallRepo struct{ pool *pgxpool.Pool }

// NewRecallRepo returns a RecallRepo over s.
func NewRecallRepo(s *Store) *RecallRepo { return &RecallRepo{pool: s.Pool} }

// RecallCandidate is one book of the sampled run as the strata see it.
type RecallCandidate struct {
	BookID int
	UUID   string
	Tier   string
	// HasFix is whether the verified projection has a fix for the book: open
	// or decided, which the projection doesn't tell apart.
	HasFix bool
	// Language is the first Calibre language code, "" when there is none.
	Language string
	Formats  []string
}

// CheckedRun is the run a sample is drawn from.
type CheckedRun struct {
	ID        int64
	LibraryID string
}

// LatestCheckedRun is the newest finished whole-library run whose seal check
// passed under this binary's rules and is not older than the run's end: the
// run the pages read from the verified projection.
func (r *RecallRepo) LatestCheckedRun(ctx context.Context) (CheckedRun, error) {
	var run CheckedRun
	err := r.pool.QueryRow(ctx, `SELECT r.id, r.library_id FROM runs r JOIN seal_checks c ON c.run_id = r.id
 WHERE r.status = 'completed' AND r.scope_book_id IS NULL AND r.finished_at IS NOT NULL
 AND c.broken = 0 AND c.checked_at >= r.finished_at AND c.findings_version = $1 AND c.decision_version = $2
 ORDER BY r.id DESC LIMIT 1`, findings.Version, decide.Version).Scan(&run.ID, &run.LibraryID)
	if errors.Is(err, pgx.ErrNoRows) {
		return CheckedRun{}, ErrNoCheckedRun
	}
	if err != nil {
		return CheckedRun{}, fmt.Errorf("store: latest checked run: %w", err)
	}
	return run, nil
}

// Candidates lists the checked books of a run with what the strata need.
// Tier and the fix count come from the projection; the language and formats
// from the sealed package of a run whose check passed.
func (r *RecallRepo) Candidates(ctx context.Context, runID int64) ([]RecallCandidate, error) {
	rows, err := r.pool.Query(ctx, `SELECT p.book_id, e.uuid, p.tier, p.proposals_count > 0,
 COALESCE(e.package->'calibre_record'->'languages'->>0, ''),
 ARRAY(SELECT f->>'format' FROM jsonb_array_elements(COALESCE(e.package->'formats', '[]'::jsonb)) f)
 FROM book_projection p JOIN evidence e ON e.run_id = p.run_id AND e.book_id = p.book_id
 WHERE p.run_id = $1 ORDER BY p.book_id`, runID)
	if err != nil {
		return nil, fmt.Errorf("store: recall candidates: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (RecallCandidate, error) {
		var c RecallCandidate
		err := row.Scan(&c.BookID, &c.UUID, &c.Tier, &c.HasFix, &c.Language, &c.Formats)
		return c, err
	})
	if err != nil {
		return nil, fmt.Errorf("store: recall candidates: %w", err)
	}
	return out, nil
}

// StratumCount is a stratum's size in the library and in the sample.
type StratumCount struct {
	Library int `json:"library"`
	Sampled int `json:"sampled"`
}

// RecallSample is one drawn sample.
type RecallSample struct {
	ID int64
	// FindingsVersion and DecisionVersion are the rules the run's check ran
	// under when the sample was drawn.
	FindingsVersion, DecisionVersion int
	// ClosedAt is when the results were first shown; nil while the sample
	// still takes answers.
	ClosedAt  *time.Time
	LibraryID string
	RunID     int64
	Seed      int64
	Size      int
	Strata    map[string]StratumCount
	CreatedBy string
	CreatedAt time.Time
}

// SampleBook is one book of a sample; Position counts from 1.
type SampleBook struct {
	BookID   int
	UUID     string
	Stratum  string
	Language string
	Position int
}

// CreateSample stores a sample and its books, copies the run's fixes for
// them (the recall reads only that copy), and pins the run so the blind pages
// can still read their excerpts. The pin is the run's one boolean: a second
// sample of the same run adds nothing. The versions are the ones the run's
// seal check ran under, so a run without a check can't be sampled.
func (r *RecallRepo) CreateSample(ctx context.Context, s RecallSample, books []SampleBook) (int64, error) {
	strata, err := json.Marshal(s.Strata)
	if err != nil {
		return 0, fmt.Errorf("store: sample strata: %w", err)
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("store: begin sample: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var id int64
	if err := tx.QueryRow(ctx, `INSERT INTO recall_samples (library_id, run_id, seed, size, strata, created_by, findings_version, decision_version)
 SELECT $1, $2, $3, $4, $5, $6, c.findings_version, c.decision_version FROM seal_checks c WHERE c.run_id = $2 RETURNING id`,
		s.LibraryID, s.RunID, s.Seed, s.Size, strata, s.CreatedBy).Scan(&id); err != nil {
		return 0, fmt.Errorf("store: insert sample of run %d: %w", s.RunID, err)
	}
	batch := &pgx.Batch{}
	for _, b := range books {
		batch.Queue(`INSERT INTO recall_sample_books (sample_id, book_id, uuid, stratum, language, position) VALUES ($1, $2, $3, $4, $5, $6)`,
			id, b.BookID, b.UUID, b.Stratum, b.Language, b.Position)
	}
	batch.Queue(`INSERT INTO recall_sample_facts (sample_id, book_id, field, proposed)
 SELECT b.sample_id, b.book_id, x.field, x.proposal->'proposed'
 FROM recall_sample_books b JOIN fix_projection x ON x.run_id = $2 AND x.book_id = b.book_id WHERE b.sample_id = $1`, id, s.RunID)
	res := tx.SendBatch(ctx, batch)
	for range batch.Len() {
		if _, err := res.Exec(); err != nil {
			_ = res.Close()
			return 0, fmt.Errorf("store: insert sample books: %w", err)
		}
	}
	if err := res.Close(); err != nil {
		return 0, fmt.Errorf("store: insert sample books: %w", err)
	}
	var pinned int64
	if err := tx.QueryRow(ctx, `UPDATE runs SET pinned = true WHERE id = $1 RETURNING id`, s.RunID).Scan(&pinned); err != nil {
		return 0, fmt.Errorf("store: pin run %d: %w", s.RunID, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("store: commit sample: %w", err)
	}
	return id, nil
}

func scanSample(row pgx.Row) (RecallSample, error) {
	var s RecallSample
	var strata []byte
	err := row.Scan(&s.ID, &s.LibraryID, &s.RunID, &s.Seed, &s.Size, &strata, &s.CreatedBy, &s.CreatedAt, &s.FindingsVersion, &s.DecisionVersion, &s.ClosedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return RecallSample{}, ErrNoSuchSample
	}
	if err != nil {
		return RecallSample{}, fmt.Errorf("store: sample: %w", err)
	}
	if err := json.Unmarshal(strata, &s.Strata); err != nil {
		return RecallSample{}, fmt.Errorf("store: sample strata: %w", err)
	}
	return s, nil
}

const sampleColumns = `id, library_id, run_id, seed, size, strata, created_by, created_at, findings_version, decision_version,
 (SELECT closed_at FROM recall_closures WHERE sample_id = recall_samples.id)`

// LatestSample is the newest sample; ErrNoSuchSample when there is none.
func (r *RecallRepo) LatestSample(ctx context.Context) (RecallSample, error) {
	return scanSample(r.pool.QueryRow(ctx, `SELECT `+sampleColumns+` FROM recall_samples ORDER BY id DESC LIMIT 1`))
}

// Sample is one sample by id.
func (r *RecallRepo) Sample(ctx context.Context, id int64) (RecallSample, error) {
	return scanSample(r.pool.QueryRow(ctx, `SELECT `+sampleColumns+` FROM recall_samples WHERE id = $1`, id))
}

// SampleBooks lists a sample's books by position.
func (r *RecallRepo) SampleBooks(ctx context.Context, sampleID int64) ([]SampleBook, error) {
	rows, err := r.pool.Query(ctx, `SELECT book_id, uuid, stratum, language, position FROM recall_sample_books WHERE sample_id = $1 ORDER BY position`, sampleID)
	if err != nil {
		return nil, fmt.Errorf("store: sample books: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (SampleBook, error) {
		var b SampleBook
		err := row.Scan(&b.BookID, &b.UUID, &b.Stratum, &b.Language, &b.Position)
		return b, err
	})
	if err != nil {
		return nil, fmt.Errorf("store: sample books: %w", err)
	}
	return out, nil
}

// SampleBookAt is the book at a position of a sample.
func (r *RecallRepo) SampleBookAt(ctx context.Context, sampleID int64, position int) (SampleBook, error) {
	var b SampleBook
	err := r.pool.QueryRow(ctx, `SELECT book_id, uuid, stratum, language, position FROM recall_sample_books WHERE sample_id = $1 AND position = $2`, sampleID, position).
		Scan(&b.BookID, &b.UUID, &b.Stratum, &b.Language, &b.Position)
	if errors.Is(err, pgx.ErrNoRows) {
		return SampleBook{}, ErrNoSuchSample
	}
	if err != nil {
		return SampleBook{}, fmt.Errorf("store: sample book: %w", err)
	}
	return b, nil
}

// recallLockBase namespaces the advisory lock that orders a sample's answers
// against its closing ("recl" in ASCII, in the high half).
const recallLockBase int64 = 0x7265636c << 32

// Close records that the sample's results were shown, the first time only,
// and returns when. After it Confirm refuses. It is the caller's job to close
// only a complete sample.
func (r *RecallRepo) Close(ctx context.Context, sampleID int64) (time.Time, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return time.Time{}, fmt.Errorf("store: begin close: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, recallLockBase|sampleID); err != nil {
		return time.Time{}, fmt.Errorf("store: lock sample: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO recall_closures (sample_id) VALUES ($1) ON CONFLICT (sample_id) DO NOTHING`, sampleID); err != nil {
		return time.Time{}, fmt.Errorf("store: close sample: %w", err)
	}
	var at time.Time
	if err := tx.QueryRow(ctx, `SELECT closed_at FROM recall_closures WHERE sample_id = $1`, sampleID).Scan(&at); err != nil {
		return time.Time{}, fmt.Errorf("store: read closure: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return time.Time{}, fmt.Errorf("store: commit close: %w", err)
	}
	return at, nil
}

// Confirmation is one answer about one field of one sampled book. Typed is
// set only with AnswerWrong.
type Confirmation struct {
	SampleID int64
	BookID   int
	UUID     string
	Field    string
	Answer   string
	Typed    *string
	By       string
	At       time.Time
}

// Confirm records answers for the book at a position of a sample, in one
// transaction. Nothing is updated: a later answer to the same field wins by
// being newer.
func (r *RecallRepo) Confirm(ctx context.Context, sampleID int64, position int, answers []Confirmation) error {
	b, err := r.SampleBookAt(ctx, sampleID, position)
	if err != nil {
		return err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: begin confirm: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, recallLockBase|sampleID); err != nil {
		return fmt.Errorf("store: lock sample: %w", err)
	}
	var closed bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM recall_closures WHERE sample_id = $1)`, sampleID).Scan(&closed); err != nil {
		return fmt.Errorf("store: read closure: %w", err)
	}
	if closed {
		return ErrSampleClosed
	}
	for _, a := range answers {
		if _, err := tx.Exec(ctx, `INSERT INTO confirmations (sample_id, book_id, uuid, field, answer, typed_value, answered_by) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			sampleID, b.BookID, b.UUID, a.Field, a.Answer, a.Typed, a.By); err != nil {
			return fmt.Errorf("store: confirm %s: %w", a.Field, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: commit confirm: %w", err)
	}
	return nil
}

// LatestConfirmations returns the newest answer of each (book, field) of a
// sample.
func (r *RecallRepo) LatestConfirmations(ctx context.Context, sampleID int64) ([]Confirmation, error) {
	rows, err := r.pool.Query(ctx, `SELECT DISTINCT ON (book_id, field) sample_id, book_id, uuid, field, answer, typed_value, answered_by, answered_at
 FROM confirmations WHERE sample_id = $1 ORDER BY book_id, field, id DESC`, sampleID)
	if err != nil {
		return nil, fmt.Errorf("store: confirmations: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Confirmation, error) {
		var c Confirmation
		err := row.Scan(&c.SampleID, &c.BookID, &c.UUID, &c.Field, &c.Answer, &c.Typed, &c.By, &c.At)
		return c, err
	})
	if err != nil {
		return nil, fmt.Errorf("store: confirmations: %w", err)
	}
	return out, nil
}

// SampleFacts is what the sample's run said about one sampled book when the
// sample was drawn: the Calibre language the title equivalence folds
// articles by, and the proposed values of its fixes by field.
type SampleFacts struct {
	Language string
	Fixes    map[string][]string
}

// SampleFacts reads the facts frozen at the draw. It reads no run, evidence or
// projection, so nothing that happens to the run afterwards changes it. Books
// without a fix are present with none.
func (r *RecallRepo) SampleFacts(ctx context.Context, sampleID int64) (map[int]SampleFacts, error) {
	rows, err := r.pool.Query(ctx, `SELECT b.book_id, b.language, f.field, f.proposed
 FROM recall_sample_books b LEFT JOIN recall_sample_facts f ON f.sample_id = b.sample_id AND f.book_id = b.book_id
 WHERE b.sample_id = $1 ORDER BY b.book_id, f.field`, sampleID)
	if err != nil {
		return nil, fmt.Errorf("store: sample facts: %w", err)
	}
	defer rows.Close()
	out := map[int]SampleFacts{}
	for rows.Next() {
		var id int
		var lang string
		var field *string
		var proposed []byte
		if err := rows.Scan(&id, &lang, &field, &proposed); err != nil {
			return nil, fmt.Errorf("store: sample facts: %w", err)
		}
		f, ok := out[id]
		if !ok {
			f = SampleFacts{Language: lang, Fixes: map[string][]string{}}
		}
		if field != nil {
			var values []string
			if err := json.Unmarshal(proposed, &values); err != nil {
				return nil, fmt.Errorf("store: sample facts: book %d proposal: %w", id, err)
			}
			f.Fixes[*field] = values
		}
		out[id] = f
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: sample facts: %w", err)
	}
	return out, nil
}
