package fix

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/store"
)

// Undo writes back the values a completed operation replaced, as an operation
// of its own (spec section 8.4). The book must be unchanged since the
// operation, and the operation not undone already. It returns the undo's id;
// ErrPaused when the undo itself didn't complete.
func (s *Service) Undo(ctx context.Context, opID int64, by string) (int64, error) {
	s.undoing.Add(1)
	defer s.undoing.Add(-1)
	var id int64
	err := s.session(ctx, func(ctx context.Context) error {
		// A closed page mustn't interrupt an undo half-way.
		ctx = context.WithoutCancel(ctx)
		orig, err := s.fixes.Operation(ctx, opID)
		if errors.Is(err, store.ErrNoOperation) {
			return fmt.Errorf("fix: operation %d: %w", opID, ErrNotUndoable)
		}
		if err != nil {
			return err
		}
		if orig.State != store.OpCompleted || orig.After == nil {
			return fmt.Errorf("fix: operation %d is %s: %w", opID, orig.State, ErrNotUndoable)
		}
		undone, err := s.fixes.HasUndo(ctx, opID)
		if err != nil {
			return err
		}
		if undone {
			return fmt.Errorf("fix: operation %d is undone already: %w", opID, ErrNotUndoable)
		}
		cur, err := s.readBook(ctx, orig.LibraryID, orig.BookID)
		if errors.Is(err, library.ErrBookNotFound) {
			return fmt.Errorf("fix: book %d is gone: %w", orig.BookID, ErrChanged)
		}
		if err != nil {
			return err
		}
		now := evidence.RecordFrom(cur)
		if cur.UUID != orig.UUID || !sameRecord(evidence.RecordFrom(reloaded(cur, orig.After.LastModified)), *orig.After) {
			return fmt.Errorf("fix: book %d: %w", orig.BookID, ErrChanged)
		}
		changes, missing := restoreChanges(orig.Changes, orig.Before)
		if len(missing) > 0 {
			return fmt.Errorf("fix: operation %d can't put back %s: %w", opID, strings.Join(missing, ", "), ErrBlocked)
		}
		for _, c := range changes {
			if !storable(c) {
				return fmt.Errorf("fix: operation %d %s: %w", opID, c.Field, ErrBlocked)
			}
		}
		collides, err := s.renames(ctx, orig.LibraryID, changes)
		if err != nil {
			return err
		}
		if collides {
			return fmt.Errorf("fix: operation %d would rename a shared name: %w", opID, ErrBlocked)
		}
		op := store.Operation{
			Kind: store.OpUndo, Undoes: &opID, LibraryID: orig.LibraryID, BookID: orig.BookID, UUID: orig.UUID,
			Changes: changes, Before: now, RequestedBy: by,
		}
		if op.ID, err = s.fixes.StartOperation(ctx, op, nil); errors.Is(err, store.ErrUndoExists) {
			return fmt.Errorf("fix: operation %d is undone already: %w", opID, ErrNotUndoable)
		} else if err != nil {
			return err
		}
		id = op.ID
		completed, err := s.execute(ctx, op)
		if err != nil {
			return err
		}
		if !completed {
			return fmt.Errorf("fix: undo %d on book %d didn't complete: %w", op.ID, op.BookID, ErrPaused)
		}
		return nil
	})
	return id, err
}
