package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AgentActor is who agent decisions are recorded as (ADR-029).
const AgentActor = "agent"

// Agent fixing modes, review kinds and states, and what a review did with a
// proposal (ADR-029).
const (
	ModeOff    = "off"
	ModeShadow = "shadow"
	// ModeOn was retired on 2026-11-04 (ADR-029): it is kept only so that
	// older rows read as shadow. Nothing sets it.
	ModeOn = "on"

	ReviewAuto        = "auto"
	ReviewManual      = "manual"
	ReviewCalibration = "calibration"

	ReviewQueued  = "queued"
	ReviewRunning = "running"
	ReviewDone    = "done"
	ReviewStopped = "stopped"

	OutcomeApproved = "approved"
	OutcomeRejected = "rejected"
	OutcomeLeft     = "left"
)

// Review is one agent review of a run's proposals.
type Review struct {
	ID, RunID                     int64
	Kind, Mode, State, StopReason string
	RequestedBy                   string
	CreatedAt                     time.Time
	FinishedAt                    *time.Time
}

// Verdict is one judge's answer on one proposal, or the error code when it
// gave none.
type Verdict struct {
	ReviewID, RunID                   int64
	BookID                            int
	Field, Seat, PromptVersion        string
	ProposalHash, PacketHash, Model   string
	Answer, Reason, ErrorCode         string
	InputTokens, OutputTokens, Millis int64
	CreatedAt                         time.Time
}

// Outcome is what a review did with one proposal.
type Outcome struct {
	ReviewID, RunID int64
	BookID          int
	Field, Outcome  string
	Reason          string
	CreatedAt       time.Time
}

// AgentRepo stores the agent fixing mode, reviews, verdicts and outcomes.
type AgentRepo struct {
	pool *pgxpool.Pool
}

// NewAgentRepo returns the agent repository for s.
func NewAgentRepo(s *Store) *AgentRepo { return &AgentRepo{pool: s.Pool} }

// Mode reads the agent fixing mode; off when never set. A stored "on" from
// before the mode was retired reads as shadow.
func (r *AgentRepo) Mode(ctx context.Context) (string, error) {
	var raw []byte
	err := r.pool.QueryRow(ctx, `SELECT value FROM settings WHERE name = 'agent_fixing' ORDER BY id DESC LIMIT 1`).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return ModeOff, nil
	}
	if err != nil {
		return "", fmt.Errorf("store: read the agent mode: %w", err)
	}
	var m string
	if err := json.Unmarshal(raw, &m); err != nil {
		return "", fmt.Errorf("store: decode the agent mode: %w", err)
	}
	if m == ModeOn {
		return ModeShadow, nil
	}
	return m, nil
}

// SetMode records a change of the agent fixing mode: off or shadow.
func (r *AgentRepo) SetMode(ctx context.Context, mode, by string) error {
	if mode != ModeOff && mode != ModeShadow {
		return fmt.Errorf("store: invalid agent mode %q", mode)
	}
	raw, _ := json.Marshal(mode)
	if _, err := r.pool.Exec(ctx, `INSERT INTO settings (name, value, changed_by) VALUES ('agent_fixing', $1::jsonb, $2)`, string(raw), by); err != nil {
		return fmt.Errorf("store: set the agent mode: %w", err)
	}
	return nil
}

const reviewColumns = `id, run_id, kind, mode, state, stop_reason, requested_by, created_at, finished_at`

func scanReview(row pgx.Row) (Review, error) {
	var rv Review
	err := row.Scan(&rv.ID, &rv.RunID, &rv.Kind, &rv.Mode, &rv.State, &rv.StopReason, &rv.RequestedBy, &rv.CreatedAt, &rv.FinishedAt)
	return rv, err
}

// Queue adds a review of a run. ok is false, and nothing is added, when the
// run already has an automatic review (kind auto), or a queued or running
// review of the same kind. The NOT EXISTS guard covers a database that hasn't
// run migration 0015 (the agent-review CLI doesn't migrate); the unique indexes
// reviews_auto_once and reviews_open_once decide a race, so concurrent callers
// can't both win.
func (r *AgentRepo) Queue(ctx context.Context, runID int64, kind, mode, by string) (Review, bool, error) {
	rv, err := scanReview(r.pool.QueryRow(ctx, `INSERT INTO reviews (run_id, kind, mode, requested_by)
		SELECT $1, $2, $3, $4
		WHERE NOT EXISTS (SELECT 1 FROM reviews WHERE run_id = $1 AND kind = $2 AND (kind = 'auto' OR state IN ('queued', 'running')))
		ON CONFLICT DO NOTHING
		RETURNING `+reviewColumns, runID, kind, mode, by))
	if errors.Is(err, pgx.ErrNoRows) {
		return Review{}, false, nil
	}
	if err != nil {
		return Review{}, false, fmt.Errorf("store: queue a review of run %d: %w", runID, err)
	}
	return rv, true, nil
}

// Next returns the running review, else the oldest queued one.
func (r *AgentRepo) Next(ctx context.Context) (Review, bool, error) {
	rv, err := scanReview(r.pool.QueryRow(ctx, `SELECT `+reviewColumns+` FROM reviews
		WHERE state IN ('queued', 'running') ORDER BY state = 'running' DESC, id LIMIT 1`))
	if errors.Is(err, pgx.ErrNoRows) {
		return Review{}, false, nil
	}
	if err != nil {
		return Review{}, false, fmt.Errorf("store: next review: %w", err)
	}
	return rv, true, nil
}

// Reviewing reports whether a review is running now. A queued one is not: it
// may wait for a judge to be configured again, for as long as that takes.
func (r *AgentRepo) Reviewing(ctx context.Context) (bool, error) {
	var ok bool
	if err := r.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM reviews WHERE state = 'running')`).Scan(&ok); err != nil {
		return false, fmt.Errorf("store: review running: %w", err)
	}
	return ok, nil
}

// Start marks a queued review running.
func (r *AgentRepo) Start(ctx context.Context, id int64) error {
	if _, err := r.pool.Exec(ctx, `UPDATE reviews SET state = 'running', started_at = coalesce(started_at, now())
		WHERE id = $1 AND state IN ('queued', 'running')`, id); err != nil {
		return fmt.Errorf("store: start review %d: %w", id, err)
	}
	return nil
}

// Finish ends a review, done or stopped with a reason.
func (r *AgentRepo) Finish(ctx context.Context, id int64, state, reason string) error {
	if state != ReviewDone && state != ReviewStopped {
		return fmt.Errorf("store: finish review: invalid state %q", state)
	}
	if _, err := r.pool.Exec(ctx, `UPDATE reviews SET state = $2, stop_reason = $3, finished_at = now() WHERE id = $1`, id, state, reason); err != nil {
		return fmt.Errorf("store: finish review %d: %w", id, err)
	}
	return nil
}

// Reviews lists a run's reviews, oldest first.
func (r *AgentRepo) Reviews(ctx context.Context, runID int64) ([]Review, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+reviewColumns+` FROM reviews WHERE run_id = $1 ORDER BY id`, runID)
	if err != nil {
		return nil, fmt.Errorf("store: reviews of run %d: %w", runID, err)
	}
	defer rows.Close()
	var out []Review
	for rows.Next() {
		rv, err := scanReview(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan review: %w", err)
		}
		out = append(out, rv)
	}
	return out, rows.Err()
}

// AddVerdict records a verdict once; a verdict with the same key is kept as
// it was.
func (r *AgentRepo) AddVerdict(ctx context.Context, v Verdict) error {
	if _, err := r.pool.Exec(ctx, `INSERT INTO verdicts (review_id, run_id, book_id, field, seat, prompt_version,
		proposal_hash, packet_hash, model, answer, reason, error_code, input_tokens, output_tokens, millis)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15) ON CONFLICT DO NOTHING`,
		v.ReviewID, v.RunID, v.BookID, v.Field, v.Seat, v.PromptVersion, v.ProposalHash, v.PacketHash, v.Model,
		v.Answer, v.Reason, v.ErrorCode, v.InputTokens, v.OutputTokens, v.Millis); err != nil {
		return fmt.Errorf("store: add verdict for book %d %s: %w", v.BookID, v.Field, err)
	}
	return nil
}

// Verdicts returns a run's verdicts under a prompt version, from every
// review, oldest first.
func (r *AgentRepo) Verdicts(ctx context.Context, runID int64, promptVersion string) ([]Verdict, error) {
	rows, err := r.pool.Query(ctx, `SELECT review_id, run_id, book_id, field, seat, prompt_version, proposal_hash, packet_hash,
		model, answer, reason, error_code, input_tokens, output_tokens, millis, created_at
		FROM verdicts WHERE run_id = $1 AND prompt_version = $2 ORDER BY created_at, review_id, book_id, field, seat`, runID, promptVersion)
	if err != nil {
		return nil, fmt.Errorf("store: verdicts of run %d: %w", runID, err)
	}
	defer rows.Close()
	var out []Verdict
	for rows.Next() {
		var v Verdict
		if err := rows.Scan(&v.ReviewID, &v.RunID, &v.BookID, &v.Field, &v.Seat, &v.PromptVersion, &v.ProposalHash, &v.PacketHash,
			&v.Model, &v.Answer, &v.Reason, &v.ErrorCode, &v.InputTokens, &v.OutputTokens, &v.Millis, &v.CreatedAt); err != nil {
			return nil, fmt.Errorf("store: scan verdict: %w", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// AddOutcome records what a review did with a proposal, once.
func (r *AgentRepo) AddOutcome(ctx context.Context, o Outcome) error {
	if _, err := r.pool.Exec(ctx, `INSERT INTO review_outcomes (review_id, run_id, book_id, field, outcome, reason)
		VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT DO NOTHING`,
		o.ReviewID, o.RunID, o.BookID, o.Field, o.Outcome, o.Reason); err != nil {
		return fmt.Errorf("store: add outcome for book %d %s: %w", o.BookID, o.Field, err)
	}
	return nil
}

// Outcomes returns the newest outcome per book and field of a run.
func (r *AgentRepo) Outcomes(ctx context.Context, runID int64) ([]Outcome, error) {
	rows, err := r.pool.Query(ctx, `SELECT DISTINCT ON (book_id, field) review_id, run_id, book_id, field, outcome, reason, created_at
		FROM review_outcomes WHERE run_id = $1 ORDER BY book_id, field, review_id DESC`, runID)
	if err != nil {
		return nil, fmt.Errorf("store: outcomes of run %d: %w", runID, err)
	}
	defer rows.Close()
	var out []Outcome
	for rows.Next() {
		var o Outcome
		if err := rows.Scan(&o.ReviewID, &o.RunID, &o.BookID, &o.Field, &o.Outcome, &o.Reason, &o.CreatedAt); err != nil {
			return nil, fmt.Errorf("store: scan outcome: %w", err)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// ReviewOutcomes returns what one review did, by book and field.
func (r *AgentRepo) ReviewOutcomes(ctx context.Context, reviewID int64) ([]Outcome, error) {
	rows, err := r.pool.Query(ctx, `SELECT review_id, run_id, book_id, field, outcome, reason, created_at
		FROM review_outcomes WHERE review_id = $1 ORDER BY book_id, field`, reviewID)
	if err != nil {
		return nil, fmt.Errorf("store: outcomes of review %d: %w", reviewID, err)
	}
	defer rows.Close()
	var out []Outcome
	for rows.Next() {
		var o Outcome
		if err := rows.Scan(&o.ReviewID, &o.RunID, &o.BookID, &o.Field, &o.Outcome, &o.Reason, &o.CreatedAt); err != nil {
			return nil, fmt.Errorf("store: scan outcome: %w", err)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// Tokens sums input and output tokens per seat over a run's reviews.
func (r *AgentRepo) Tokens(ctx context.Context, runID int64) (map[string]int64, error) {
	rows, err := r.pool.Query(ctx, `SELECT seat, sum(input_tokens + output_tokens) FROM verdicts WHERE run_id = $1 GROUP BY seat`, runID)
	if err != nil {
		return nil, fmt.Errorf("store: tokens of run %d: %w", runID, err)
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var seat string
		var n int64
		if err := rows.Scan(&seat, &n); err != nil {
			return nil, fmt.Errorf("store: scan tokens: %w", err)
		}
		out[seat] = n
	}
	return out, rows.Err()
}

// ProposalBooks lists the books of a run whose package has proposals.
func (r *AgentRepo) ProposalBooks(ctx context.Context, runID int64) ([]int, error) {
	rows, err := r.pool.Query(ctx, `SELECT book_id FROM evidence
		WHERE run_id = $1 AND jsonb_array_length(coalesce(package->'proposals', '[]'::jsonb)) > 0 ORDER BY book_id`, runID)
	if err != nil {
		return nil, fmt.Errorf("store: proposal books of run %d: %w", runID, err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int])
	if err != nil {
		return nil, fmt.Errorf("store: proposal books of run %d: %w", runID, err)
	}
	return ids, nil
}
