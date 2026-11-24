package web

import (
	"context"

	"github.com/felixapel/calibre-bookwarden/internal/fix"
)

// Fixing is the guarded writer behind the Fixes, History, Incidents and
// Settings pages; *fix.Service implements it.
type Fixing interface {
	Approve(ctx context.Context, runID int64, bookID int, fields []string, by string, basis fix.Basis) error
	Reject(ctx context.Context, runID int64, bookID int, fields []string, by string, basis fix.Basis) error
	RunProposals(ctx context.Context, runID int64) ([]fix.ProposalRow, error)
	RecordReveals(ctx context.Context, runID int64, keys []fix.RevealKey) error
	ApprovedCount(ctx context.Context) (int, error)
	Precision(ctx context.Context) ([]fix.TypeLabels, error)
	MuteStats(ctx context.Context) ([]fix.TypeMute, error)
	Unmute(ctx context.Context, libraryID string, t fix.MuteType, by string) error
	StartApply(ctx context.Context, by string) error
	ApplyStatus() fix.ApplyStatus
	Operations(ctx context.Context, before int64, limit int) ([]fix.Operation, error)
	Operation(ctx context.Context, id int64) (fix.Operation, error)
	Events(ctx context.Context, id int64) ([]fix.Event, error)
	Undoable(ctx context.Context, id int64) (bool, error)
	Undo(ctx context.Context, id int64, by string) (int64, error)
	Incidents(ctx context.Context) ([]fix.Incident, error)
	Acknowledge(ctx context.Context, id int64, by string) error
	RetryRecovery(ctx context.Context) error
	Paused(ctx context.Context) (bool, error)
	WritesEnabled(ctx context.Context) (bool, error)
	SetWritesEnabled(ctx context.Context, on bool, by string) error
	SettingChanges(ctx context.Context, limit int) ([]fix.SettingChange, error)
}

var _ Fixing = (*fix.Service)(nil)
