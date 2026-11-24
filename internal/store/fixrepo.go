package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/library"
)

// writerLockID is an arbitrary constant ("bookwrit" in ASCII): the session
// advisory lock held by the one writer (spec section 8.3).
const writerLockID int64 = 0x626f6f6b77726974

// Proposal statuses, operation states and kinds (ADR-021).
const (
	ProposalApproved = "approved"
	ProposalRejected = "rejected"
	ProposalApplied  = "applied"
	ProposalStale    = "stale"
	ProposalBlocked  = "blocked"
	ProposalFailed   = "failed"

	OpRequested = "requested"
	OpWriting   = "writing"
	OpVerifying = "verifying"
	OpCompleted = "completed"
	OpFailed    = "failed"
	OpRestoring = "restoring"
	OpRestored  = "restored"

	OpApply = "apply"
	OpUndo  = "undo"
)

var (
	// ErrClosed: a proposal is final, in flight, or not approved.
	ErrClosed = errors.New("store: proposal is closed")
	// ErrDecided: an OnlyOpen decision met a fix that already has one.
	ErrDecided = errors.New("store: proposal is already decided")
	// ErrBadTransition: the ledger doesn't allow that state next.
	ErrBadTransition = errors.New("store: operation state transition not allowed")
	ErrNoOperation   = errors.New("store: no such operation")
	// ErrNoIncident: unknown, already acknowledged, or its operation is
	// unfinished.
	ErrNoIncident = errors.New("store: no open incident to acknowledge")
	// ErrUndoExists: the operation already has an undo that is unfinished
	// or completed.
	ErrUndoExists = errors.New("store: the operation has an undo already")
)

// transitions is the write-ahead ledger's state machine.
var transitions = map[string][]string{
	OpRequested: {OpWriting, OpFailed},
	// writing → failed is a write Calibre refused with the book unchanged
	// (calibre_refused_write): nothing to restore, so no incident.
	OpWriting:   {OpVerifying, OpRestoring, OpFailed},
	OpVerifying: {OpCompleted, OpRestoring},
	// restoring → restoring records the book found before a restore writes.
	OpRestoring: {OpRestoring, OpRestored, OpFailed},
}

func terminal(state string) bool { return transitions[state] == nil }

// ProposalRow is an approval decision on one sealed proposal.
type ProposalRow struct {
	ID, RunID   int64
	BookID      int
	Proposal    evidence.Proposal
	Status      string
	Reason      string
	OperationID *int64
	DecidedBy   string
	DecidedAt   time.Time
	// DecisionMode and VerdictsShown say how it was decided (ADR-034).
	DecisionMode  string
	VerdictsShown bool
	// CarriedFrom is the proposal a carried rejection copies, and
	// CarriedFromRun the run that proposal is in (ADR-034, carried decisions).
	CarriedFrom    *int64
	CarriedFromRun int64
	// CarriedFromBook is that proposal's book id.
	CarriedFromBook int
}

// How a proposal was decided (ADR-034). Only a person's individual decision
// made with the agents' verdicts hidden is a label.
const (
	DecisionIndividual = "individual"
	DecisionBulk       = "bulk"
	DecisionAgent      = "agent"
	// DecisionCarried is a person's rejection copied into a later run as the
	// same fix. It is never a label.
	DecisionCarried = "carried"
)

// Basis is how a person decided: alone on one field or in a batch, and
// whether the agents' verdicts were on screen.
type Basis struct {
	Mode          string // DecisionIndividual or DecisionBulk
	VerdictsShown bool
	// OnlyOpen decides only fixes nobody has decided yet: Decide returns
	// ErrDecided, and stores nothing, when one already has a decision. It
	// is never stored.
	OnlyOpen bool
}

// Operation is one write to one book: its immutable header and its newest
// state.
type Operation struct {
	ID          int64
	Kind        string
	Undoes      *int64 // undo only
	RunID       *int64 // apply only
	LibraryID   string
	BookID      int
	UUID        string
	Changes     []library.Change
	Before      evidence.Record
	RequestedBy string
	CreatedAt   time.Time
	State       string           // from the newest event
	After       *evidence.Record // from the newest event that has one
	Detail      string           // from the newest event
}

// Incident is a write that didn't complete. While one is open, writes pause.
type Incident struct {
	ID, OperationID int64
	Reason, Detail  string
	CreatedAt       time.Time
	AcknowledgedBy  string
	AcknowledgedAt  *time.Time
	// OperationState is the operation's latest state; restoring means the
	// writer is still putting the old values back.
	OperationState string
}

// Event is one step of an operation.
type Event struct {
	Seq    int
	State  string
	After  *evidence.Record
	Detail string
	At     time.Time
}

// SettingChange is one change of the writes switch.
type SettingChange struct {
	Value     bool
	ChangedBy string
	ChangedAt time.Time
}

// FixRepo stores approvals, the write ledger, incidents and the writes
// switch.
type FixRepo struct {
	pool *pgxpool.Pool
}

// NewFixRepo returns the fixing repository for s.
func NewFixRepo(s *Store) *FixRepo { return &FixRepo{pool: s.Pool} }

// LockWriter takes the writer's session advisory lock without waiting. ok is
// false when another session holds it.
func (r *FixRepo) LockWriter(ctx context.Context) (func(), bool, error) {
	return tryAdvisoryLock(ctx, r.pool, writerLockID, "writer")
}

// WritesEnabled reads the "Allow changes to Calibre" switch; off when never
// set.
func (r *FixRepo) WritesEnabled(ctx context.Context) (bool, error) {
	var raw string
	err := r.pool.QueryRow(ctx, `SELECT value::text FROM settings WHERE name = 'writes_enabled' ORDER BY id DESC LIMIT 1`).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("store: read the writes switch: %w", err)
	}
	return raw == "true", nil
}

// SetWritesEnabled records a change of the switch.
func (r *FixRepo) SetWritesEnabled(ctx context.Context, on bool, by string) error {
	if _, err := r.pool.Exec(ctx, `INSERT INTO settings (name, value, changed_by) VALUES ('writes_enabled', $1::jsonb, $2)`,
		fmt.Sprint(on), by); err != nil {
		return fmt.Errorf("store: set the writes switch: %w", err)
	}
	return nil
}

// NewestEvidenceRun is the newest run with checked evidence for the book, or
// 0 when there is none.
func (r *FixRepo) NewestEvidenceRun(ctx context.Context, libraryID string, bookID int, uuid string) (int64, error) {
	var id *int64
	if err := r.pool.QueryRow(ctx, `SELECT max(e.run_id) FROM evidence e JOIN runs r ON r.id = e.run_id
		WHERE r.library_id = $1 AND e.book_id = $2 AND e.uuid = $3`, libraryID, bookID, uuid).Scan(&id); err != nil {
		return 0, fmt.Errorf("store: newest evidence for book %d: %w", bookID, err)
	}
	if id == nil {
		return 0, nil
	}
	return *id, nil
}

// Decide approves or rejects proposals of one book's evidence. It returns
// ErrClosed, and stores nothing, when any of them is final or in flight, and
// with basis.OnlyOpen ErrDecided when any of them has a decision.
func (r *FixRepo) Decide(ctx context.Context, runID int64, bookID int, ps []evidence.Proposal, status, by string, basis Basis) error {
	if status != ProposalApproved && status != ProposalRejected {
		return fmt.Errorf("store: decide: invalid status %q", status)
	}
	if basis.Mode != DecisionIndividual && basis.Mode != DecisionBulk {
		return fmt.Errorf("store: decide: invalid decision mode %q", basis.Mode)
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: begin decide: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, p := range ps {
		raw, err := json.Marshal(p)
		if err != nil {
			return fmt.Errorf("store: encode proposal: %w", err)
		}
		tag, err := tx.Exec(ctx, `INSERT INTO proposals (run_id, book_id, field, proposal, status, decided_by, decision_mode, verdicts_shown)
			VALUES ($1, $2, $3, $4::jsonb, $5, $6, $7,
				$8 OR EXISTS (SELECT 1 FROM verdict_reveals v WHERE v.run_id = $1 AND v.book_id = $2 AND v.field = $3))
			ON CONFLICT (run_id, book_id, field) DO UPDATE
			SET proposal = EXCLUDED.proposal, status = EXCLUDED.status, reason = '', decided_by = EXCLUDED.decided_by, decided_at = now(),
				-- Deciding a fix the same way again changes nothing, so it keeps
				-- the basis of the decision that stands (ADR-034).
				decision_mode = CASE WHEN proposals.status = EXCLUDED.status THEN proposals.decision_mode ELSE EXCLUDED.decision_mode END,
				verdicts_shown = CASE WHEN proposals.status = EXCLUDED.status THEN proposals.verdicts_shown ELSE EXCLUDED.verdicts_shown END,
				-- A changed decision is a normal one, no longer a copy.
				carried_from = CASE WHEN proposals.status = EXCLUDED.status THEN proposals.carried_from END
			WHERE proposals.status IN ('approved', 'rejected') AND proposals.operation_id IS NULL AND NOT $9`,
			runID, bookID, p.Field, string(raw), status, by, basis.Mode, basis.VerdictsShown, basis.OnlyOpen)
		if err != nil {
			return fmt.Errorf("store: decide book %d %s: %w", bookID, p.Field, err)
		}
		if tag.RowsAffected() != 1 {
			if basis.OnlyOpen {
				return fmt.Errorf("store: decide book %d %s: %w", bookID, p.Field, ErrDecided)
			}
			return fmt.Errorf("store: decide book %d %s: %w", bookID, p.Field, ErrClosed)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: commit decide: %w", err)
	}
	return nil
}

const proposalColumns = `id, run_id, book_id, proposal, status, reason, operation_id, decided_by, decided_at, decision_mode, verdicts_shown,
	carried_from, COALESCE((SELECT c.run_id FROM proposals c WHERE c.id = proposals.carried_from), 0),
	COALESCE((SELECT c.book_id FROM proposals c WHERE c.id = proposals.carried_from), 0)`

func scanProposals(rows pgx.Rows) ([]ProposalRow, error) {
	defer rows.Close()
	var out []ProposalRow
	for rows.Next() {
		var p ProposalRow
		var raw []byte
		if err := rows.Scan(&p.ID, &p.RunID, &p.BookID, &raw, &p.Status, &p.Reason, &p.OperationID, &p.DecidedBy, &p.DecidedAt, &p.DecisionMode, &p.VerdictsShown, &p.CarriedFrom, &p.CarriedFromRun, &p.CarriedFromBook); err != nil {
			return nil, fmt.Errorf("store: scan proposal: %w", err)
		}
		if err := json.Unmarshal(raw, &p.Proposal); err != nil {
			return nil, fmt.Errorf("store: decode proposal %d: %w", p.ID, err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read proposals: %w", err)
	}
	return out, nil
}

// Proposals returns the decisions on one book's evidence, by field.
func (r *FixRepo) Proposals(ctx context.Context, runID int64, bookID int) ([]ProposalRow, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+proposalColumns+` FROM proposals WHERE run_id = $1 AND book_id = $2 ORDER BY field`, runID, bookID)
	if err != nil {
		return nil, fmt.Errorf("store: proposals: %w", err)
	}
	return scanProposals(rows)
}

// Approved returns approved proposals not yet taken by an operation, by run,
// book and field. An approval made by the agent before mode on was retired is
// never returned: only a person's approval is applied (ADR-029).
func (r *FixRepo) Approved(ctx context.Context) ([]ProposalRow, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+proposalColumns+` FROM proposals
		WHERE status = 'approved' AND operation_id IS NULL AND decided_by IS DISTINCT FROM 'agent' ORDER BY run_id, book_id, field`)
	if err != nil {
		return nil, fmt.Errorf("store: approved proposals: %w", err)
	}
	return scanProposals(rows)
}

// Close marks approved proposals stale or blocked. Nothing changes, and it
// returns ErrClosed, when any of them isn't approved and free.
func (r *FixRepo) Close(ctx context.Context, ids []int64, status, reason string) error {
	if status != ProposalStale && status != ProposalBlocked {
		return fmt.Errorf("store: close: invalid status %q", status)
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: begin close: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `UPDATE proposals SET status = $2, reason = $3
		WHERE id = ANY($1) AND status = 'approved' AND operation_id IS NULL`, ids, status, reason)
	if err != nil {
		return fmt.Errorf("store: close proposals: %w", err)
	}
	if tag.RowsAffected() != int64(len(slices.Compact(slices.Sorted(slices.Values(ids))))) {
		return fmt.Errorf("store: close proposals: %w", ErrClosed)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: commit close: %w", err)
	}
	return nil
}

// StartOperation records an operation in state "requested" and links the
// approved proposals it writes. It returns ErrClosed when one isn't approved
// and free.
func (r *FixRepo) StartOperation(ctx context.Context, op Operation, proposalIDs []int64) (int64, error) {
	changes, err := json.Marshal(op.Changes)
	if err != nil {
		return 0, fmt.Errorf("store: encode changes: %w", err)
	}
	before, err := json.Marshal(op.Before)
	if err != nil {
		return 0, fmt.Errorf("store: encode before: %w", err)
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("store: begin operation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if op.Undoes != nil {
		// One live undo per operation, also across processes and after a
		// lost advisory lock: the original's row lock serialises two undos.
		// The check is a statement of its own, after the lock: under READ
		// COMMITTED only a new statement sees an undo the other transaction
		// committed while this one waited. A failed or restored undo doesn't
		// count, so the undo can be tried again (as HasUndo).
		if _, err := tx.Exec(ctx, `SELECT 1 FROM operations WHERE id = $1 FOR UPDATE`, *op.Undoes); err != nil {
			return 0, fmt.Errorf("store: lock operation %d: %w", *op.Undoes, err)
		}
		var live bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM operations o
			JOIN LATERAL (SELECT state FROM operation_events WHERE operation_id = o.id ORDER BY seq DESC LIMIT 1) e ON true
			WHERE o.undoes = $1 AND e.state NOT IN ('restored', 'failed'))`, *op.Undoes).Scan(&live); err != nil {
			return 0, fmt.Errorf("store: undo of operation %d: %w", *op.Undoes, err)
		}
		if live {
			return 0, fmt.Errorf("store: operation %d: %w", *op.Undoes, ErrUndoExists)
		}
	}
	var id int64
	if err := tx.QueryRow(ctx, `INSERT INTO operations (kind, undoes, run_id, library_id, book_id, uuid, changes, before, requested_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, $8::jsonb, $9) RETURNING id`,
		op.Kind, op.Undoes, op.RunID, op.LibraryID, op.BookID, op.UUID, string(changes), string(before), op.RequestedBy).Scan(&id); err != nil {
		return 0, fmt.Errorf("store: record operation for book %d: %w", op.BookID, err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO operation_events (operation_id, seq, state) VALUES ($1, 1, 'requested')`, id); err != nil {
		return 0, fmt.Errorf("store: record operation %d: %w", id, err)
	}
	if len(proposalIDs) > 0 {
		tag, err := tx.Exec(ctx, `UPDATE proposals SET operation_id = $2
			WHERE id = ANY($1) AND status = 'approved' AND operation_id IS NULL`, proposalIDs, id)
		if err != nil {
			return 0, fmt.Errorf("store: link proposals to operation %d: %w", id, err)
		}
		if tag.RowsAffected() != int64(len(slices.Compact(slices.Sorted(slices.Values(proposalIDs))))) {
			return 0, fmt.Errorf("store: link proposals to operation %d: %w", id, ErrClosed)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("store: commit operation: %w", err)
	}
	return id, nil
}

// Advance appends the operation's next state. Entering restoring, or failed
// straight from requested, opens the operation's incident, whose reason is
// detail's leading code ("readback_mismatch: title"). A terminal state closes the linked proposals:
// applied on completed, failed otherwise.
func (r *FixRepo) Advance(ctx context.Context, id int64, state string, after *evidence.Record, detail string) error {
	var afterJSON *string
	if after != nil {
		raw, err := json.Marshal(after)
		if err != nil {
			return fmt.Errorf("store: encode after: %w", err)
		}
		s := string(raw)
		afterJSON = &s
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: begin advance: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var seq int
	var current string
	err = tx.QueryRow(ctx, `SELECT e.seq, e.state FROM operations o
		JOIN operation_events e ON e.operation_id = o.id
		WHERE o.id = $1 ORDER BY e.seq DESC LIMIT 1 FOR UPDATE OF o`, id).Scan(&seq, &current)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("store: operation %d: %w", id, ErrNoOperation)
	}
	if err != nil {
		return fmt.Errorf("store: lock operation %d: %w", id, err)
	}
	if !slices.Contains(transitions[current], state) {
		return fmt.Errorf("store: operation %d %s → %s: %w", id, current, state, ErrBadTransition)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO operation_events (operation_id, seq, state, after, detail) VALUES ($1, $2, $3, $4::jsonb, $5)`,
		id, seq+1, state, afterJSON, detail); err != nil {
		return fmt.Errorf("store: operation %d → %s: %w", id, state, err)
	}
	code, _, _ := strings.Cut(detail, ":")
	// The incident opens on the first step away from a write: restoring, or
	// failed straight from requested. A failed restore keeps it.
	if (state == OpRestoring && current != OpRestoring) || (state == OpFailed && current == OpRequested) {
		if _, err := tx.Exec(ctx, `INSERT INTO incidents (operation_id, reason, detail) VALUES ($1, $2, $3)
			ON CONFLICT (operation_id) DO NOTHING`, id, strings.TrimSpace(code), detail); err != nil {
			return fmt.Errorf("store: incident for operation %d: %w", id, err)
		}
	}
	if terminal(state) {
		status, reason := ProposalFailed, strings.TrimSpace(code)
		if state == OpCompleted {
			status, reason = ProposalApplied, ""
		} else if reason == "" {
			// A restore records no code of its own; the incident says why.
			if err := tx.QueryRow(ctx, `SELECT coalesce((SELECT reason FROM incidents WHERE operation_id = $1), '')`, id).Scan(&reason); err != nil {
				return fmt.Errorf("store: incident reason for operation %d: %w", id, err)
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE proposals SET status = $2, reason = $3 WHERE operation_id = $1`, id, status, reason); err != nil {
			return fmt.Errorf("store: close proposals of operation %d: %w", id, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: commit operation %d → %s: %w", id, state, err)
	}
	return nil
}

const operationQuery = `SELECT o.id, o.kind, o.undoes, o.run_id, o.library_id, o.book_id, o.uuid, o.changes, o.before,
	o.requested_by, o.created_at, e.state, e.detail,
	(SELECT a.after FROM operation_events a WHERE a.operation_id = o.id AND a.after IS NOT NULL ORDER BY a.seq DESC LIMIT 1)
	FROM operations o
	JOIN LATERAL (SELECT state, detail FROM operation_events WHERE operation_id = o.id ORDER BY seq DESC LIMIT 1) e ON true`

func scanOperations(rows pgx.Rows) ([]Operation, error) {
	defer rows.Close()
	var out []Operation
	for rows.Next() {
		var op Operation
		var changes, before, after []byte
		if err := rows.Scan(&op.ID, &op.Kind, &op.Undoes, &op.RunID, &op.LibraryID, &op.BookID, &op.UUID, &changes, &before,
			&op.RequestedBy, &op.CreatedAt, &op.State, &op.Detail, &after); err != nil {
			return nil, fmt.Errorf("store: scan operation: %w", err)
		}
		if err := json.Unmarshal(changes, &op.Changes); err != nil {
			return nil, fmt.Errorf("store: decode operation %d changes: %w", op.ID, err)
		}
		if err := json.Unmarshal(before, &op.Before); err != nil {
			return nil, fmt.Errorf("store: decode operation %d before: %w", op.ID, err)
		}
		if after != nil {
			op.After = &evidence.Record{}
			if err := json.Unmarshal(after, op.After); err != nil {
				return nil, fmt.Errorf("store: decode operation %d after: %w", op.ID, err)
			}
		}
		out = append(out, op)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read operations: %w", err)
	}
	return out, nil
}

// Operation returns one operation with its newest state.
func (r *FixRepo) Operation(ctx context.Context, id int64) (Operation, error) {
	rows, err := r.pool.Query(ctx, operationQuery+` WHERE o.id = $1`, id)
	if err != nil {
		return Operation{}, fmt.Errorf("store: operation %d: %w", id, err)
	}
	ops, err := scanOperations(rows)
	if err != nil {
		return Operation{}, err
	}
	if len(ops) == 0 {
		return Operation{}, fmt.Errorf("store: operation %d: %w", id, ErrNoOperation)
	}
	return ops[0], nil
}

// Unfinished returns operations whose newest state isn't terminal, oldest
// first.
func (r *FixRepo) Unfinished(ctx context.Context) ([]Operation, error) {
	rows, err := r.pool.Query(ctx, operationQuery+` WHERE e.state NOT IN ('completed', 'restored', 'failed') ORDER BY o.id`)
	if err != nil {
		return nil, fmt.Errorf("store: unfinished operations: %w", err)
	}
	return scanOperations(rows)
}

// HasUndo reports whether an undo of the operation completed or is
// unfinished.
func (r *FixRepo) HasUndo(ctx context.Context, id int64) (bool, error) {
	var has bool
	if err := r.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM operations o
		JOIN LATERAL (SELECT state FROM operation_events WHERE operation_id = o.id ORDER BY seq DESC LIMIT 1) e ON true
		WHERE o.undoes = $1 AND e.state NOT IN ('restored', 'failed'))`, id).Scan(&has); err != nil {
		return false, fmt.Errorf("store: undo of operation %d: %w", id, err)
	}
	return has, nil
}

// Paused reports whether an incident is unacknowledged.
func (r *FixRepo) Paused(ctx context.Context) (bool, error) {
	var paused bool
	if err := r.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM incidents WHERE acknowledged_at IS NULL)`).Scan(&paused); err != nil {
		return false, fmt.Errorf("store: open incidents: %w", err)
	}
	return paused, nil
}

// Incidents returns every incident, newest first.
func (r *FixRepo) Incidents(ctx context.Context) ([]Incident, error) {
	rows, err := r.pool.Query(ctx, `SELECT i.id, i.operation_id, i.reason, i.detail, i.created_at, coalesce(i.acknowledged_by, ''), i.acknowledged_at,
		coalesce((SELECT state FROM operation_events WHERE operation_id = i.operation_id ORDER BY seq DESC LIMIT 1), '')
		FROM incidents i ORDER BY i.id DESC`)
	if err != nil {
		return nil, fmt.Errorf("store: incidents: %w", err)
	}
	defer rows.Close()
	var out []Incident
	for rows.Next() {
		var i Incident
		if err := rows.Scan(&i.ID, &i.OperationID, &i.Reason, &i.Detail, &i.CreatedAt, &i.AcknowledgedBy, &i.AcknowledgedAt, &i.OperationState); err != nil {
			return nil, fmt.Errorf("store: scan incident: %w", err)
		}
		out = append(out, i)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read incidents: %w", err)
	}
	return out, nil
}

// Acknowledge closes an open incident whose operation has finished, which
// lifts the pause once no other incident is open.
func (r *FixRepo) Acknowledge(ctx context.Context, id int64, by string) error {
	tag, err := r.pool.Exec(ctx, `UPDATE incidents i SET acknowledged_by = $2, acknowledged_at = now()
		WHERE i.id = $1 AND i.acknowledged_at IS NULL
		AND (SELECT state FROM operation_events WHERE operation_id = i.operation_id ORDER BY seq DESC LIMIT 1)
			IN ('completed', 'restored', 'failed')`, id, by)
	if err != nil {
		return fmt.Errorf("store: acknowledge incident %d: %w", id, err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("store: incident %d: %w", id, ErrNoIncident)
	}
	return nil
}

// Operations returns up to limit operations older than before (0: from the
// newest), newest first.
func (r *FixRepo) Operations(ctx context.Context, before int64, limit int) ([]Operation, error) {
	if before <= 0 {
		before = 1<<63 - 1
	}
	rows, err := r.pool.Query(ctx, operationQuery+` WHERE o.id < $1 ORDER BY o.id DESC LIMIT $2`, before, limit)
	if err != nil {
		return nil, fmt.Errorf("store: operations: %w", err)
	}
	return scanOperations(rows)
}

// Events returns an operation's steps in order.
func (r *FixRepo) Events(ctx context.Context, opID int64) ([]Event, error) {
	rows, err := r.pool.Query(ctx, `SELECT seq, state, after, detail, at FROM operation_events WHERE operation_id = $1 ORDER BY seq`, opID)
	if err != nil {
		return nil, fmt.Errorf("store: operation %d events: %w", opID, err)
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var after []byte
		if err := rows.Scan(&e.Seq, &e.State, &after, &e.Detail, &e.At); err != nil {
			return nil, fmt.Errorf("store: scan event: %w", err)
		}
		if after != nil {
			e.After = &evidence.Record{}
			if err := json.Unmarshal(after, e.After); err != nil {
				return nil, fmt.Errorf("store: decode operation %d event %d: %w", opID, e.Seq, err)
			}
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read events: %w", err)
	}
	return out, nil
}

// RunProposals returns every decision on a run's proposals, by book and
// field.
func (r *FixRepo) RunProposals(ctx context.Context, runID int64) ([]ProposalRow, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+proposalColumns+` FROM proposals WHERE run_id = $1 ORDER BY book_id, field`, runID)
	if err != nil {
		return nil, fmt.Errorf("store: run proposals: %w", err)
	}
	return scanProposals(rows)
}

// ApprovedCount counts the approved proposals Approved returns.
func (r *FixRepo) ApprovedCount(ctx context.Context) (int, error) {
	var n int
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM proposals WHERE status = 'approved' AND operation_id IS NULL AND decided_by IS DISTINCT FROM 'agent'`).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count approved proposals: %w", err)
	}
	return n, nil
}

// SettingChanges returns the newest changes of the writes switch.
func (r *FixRepo) SettingChanges(ctx context.Context, limit int) ([]SettingChange, error) {
	rows, err := r.pool.Query(ctx, `SELECT value::text, changed_by, changed_at FROM settings
		WHERE name = 'writes_enabled' ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: switch log: %w", err)
	}
	defer rows.Close()
	var out []SettingChange
	for rows.Next() {
		var c SettingChange
		var v string
		if err := rows.Scan(&v, &c.ChangedBy, &c.ChangedAt); err != nil {
			return nil, fmt.Errorf("store: scan switch change: %w", err)
		}
		c.Value = v == "true"
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read switch log: %w", err)
	}
	return out, nil
}
