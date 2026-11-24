package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

// DecideOpen creates a decision the way the agent did before mode on was
// retired, so tests can build old agent rows. Production code no longer
// decides for the agent.
func (r *FixRepo) DecideOpen(ctx context.Context, runID int64, bookID int, p evidence.Proposal, status, by string) (bool, error) {
	if status != ProposalApproved && status != ProposalRejected {
		return false, fmt.Errorf("store: decide open: invalid status %q", status)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return false, fmt.Errorf("store: encode proposal: %w", err)
	}
	tag, err := r.pool.Exec(ctx, `INSERT INTO proposals (run_id, book_id, field, proposal, status, decided_by, decision_mode)
		VALUES ($1, $2, $3, $4::jsonb, $5, $6, 'agent') ON CONFLICT (run_id, book_id, field) DO NOTHING`,
		runID, bookID, p.Field, string(raw), status, by)
	if err != nil {
		return false, fmt.Errorf("store: decide book %d %s: %w", bookID, p.Field, err)
	}
	return tag.RowsAffected() == 1, nil
}
