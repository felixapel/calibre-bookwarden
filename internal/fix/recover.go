package fix

import (
	"context"

	"github.com/felixapel/calibre-bookwarden/internal/store"
)

// Recover finishes operations a crash left unfinished, under the writer lock
// (spec section 8.3, ADR-021). Apply and Undo do the same before writing.
func (s *Service) Recover(ctx context.Context) error {
	release, err := s.lock(ctx)
	if err != nil {
		return err
	}
	defer release()
	return s.recover(context.WithoutCancel(ctx))
}

// recover moves every unfinished operation on: requested sent nothing and
// fails; writing and verifying are treated as a readback mismatch. The
// ledger steps, and so the incidents, always happen; the restores write to
// Calibre only while writes are on, and only what the operation wrote.
func (s *Service) recover(ctx context.Context) error {
	ops, err := s.fixes.Unfinished(ctx)
	if err != nil || len(ops) == 0 {
		return err
	}
	on, err := s.fixes.WritesEnabled(ctx)
	if err != nil {
		return err
	}
	for _, op := range ops {
		s.logger.Warn("fix: recovering", "operation", op.ID, "book", op.BookID, "state", op.State)
		switch op.State {
		case store.OpRequested:
			if err := s.fixes.Advance(ctx, op.ID, store.OpFailed, nil, "interrupted: nothing was sent"); err != nil {
				return err
			}
			continue
		case store.OpWriting, store.OpVerifying:
			if err := s.fixes.Advance(ctx, op.ID, store.OpRestoring, nil, "interrupted: "+op.State); err != nil {
				return err
			}
		}
		if !on {
			continue
		}
		if err := s.restore(ctx, op, true, nil); err != nil {
			return err
		}
	}
	return nil
}
