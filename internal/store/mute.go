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

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

// Muting (v1 design section 6.3) is part of the review layer: it hides the
// fixes of a type whose decisions show poor precision. It is computed from
// the decisions each time, never stored; only a person's unmute is.
//
// It is kept apart from Precision (ADR-034), which counts only blind
// individual labels. Muting counts every person's individual or bulk
// decision, whether or not the verdicts were shown, since it only ever hides.
const (
	// MuteMinFixes is how many counted fixes a type needs before it can be
	// muted.
	MuteMinFixes = 10
	// MuteBelow is the upper bound on precision under which a type is muted.
	MuteBelow = 0.5
	// UnmuteHold is how many fixes of a type must be decided after an unmute
	// before the rule applies to the type again.
	UnmuteHold = 10
)

// ErrNotMuted: the type to unmute isn't muted.
var ErrNotMuted = errors.New("store: that fix type is not muted")

// FixType is a field, the proposal's roots sorted and joined with "+" (the
// path of the labels and of carried rejections), and "fill" or "replace".
type FixType struct{ Field, Path, Kind string }

// FixTypeOf is the type of a proposal, with the same path as the labels.
func FixTypeOf(p evidence.Proposal) FixType {
	return FixType{Field: p.Field, Path: FixPath(p), Kind: FixKind(p)}
}

// MuteBound is the one-sided 95 % Clopper-Pearson upper bound on precision of
// a type with n counted fixes and k misses, 1 − LowerBound(n, n−k), and
// whether the rule mutes it: n ≥ MuteMinFixes and the bound below MuteBelow.
// The bound is 1 for n = 0.
func MuteBound(n, k int) (upper float64, muted bool) {
	lower, ok := LowerBound(n, n-k)
	if !ok {
		return 1, false
	}
	upper = 1 - lower
	return upper, n >= MuteMinFixes && upper < MuteBelow
}

// Unmute is an admin's decision to show a muted type again.
type Unmute struct {
	By string
	At time.Time
}

// TypeMute is one fix type of one library as muting sees it.
type TypeMute struct {
	LibraryID string
	FixType
	// Fixes and Misses count the unique fixes, each by its latest person
	// decision.
	Fixes, Misses int
	// Upper is the upper bound on precision.
	Upper float64
	// Muted is whether the type is muted now. Held is whether the rule would
	// mute it but an unmute still holds.
	Muted, Held bool
	// Unmute is the newest unmute of the type, and Since how many fixes of
	// the type were decided after it.
	Unmute *Unmute
	Since  int
	// MutedAt and MutedBy say when and by what the type became muted. They
	// are derived, not stored: MutedAt is the decision time of the fix that
	// made the rule true, counting from the newest unmute, and MutedBy is
	// MutedByRule. Both are zero while the type is not muted.
	MutedAt time.Time
	MutedBy string
}

// MutedByRule is who mutes a type: the rule, never a person.
const MutedByRule = "rule"

// countedFix is one unique fix with its latest person decision.
type countedFix struct {
	libraryID string
	typ       FixType
	miss      bool
	decidedAt time.Time
}

type libraryType struct {
	lib string
	typ FixType
}

// muteStats applies the rule to the counted fixes. unmutes holds the newest
// unmute of each type. The result is ordered by library, field, path and kind.
//
// A fix decided again the same way counts again toward the hold: Decide
// stamps a new decision time and only the latest is kept, so the first one
// can't be told from a new fix.
func muteStats(fixes []countedFix, unmutes map[libraryType]Unmute) []TypeMute {
	byType := map[libraryType][]countedFix{}
	for _, f := range fixes {
		key := libraryType{f.libraryID, f.typ}
		byType[key] = append(byType[key], f)
	}
	out := make([]TypeMute, 0, len(byType))
	for key, fs := range byType {
		t := TypeMute{LibraryID: key.lib, FixType: key.typ}
		if u, ok := unmutes[key]; ok {
			t.Unmute = &u
		}
		slices.SortStableFunc(fs, func(a, b countedFix) int { return a.decidedAt.Compare(b.decidedAt) })
		// Walk the decisions in order. A streak starts at the decision that
		// makes the rule true and ends when it stops being true; the type is
		// muted since the streak's start if it lasts to the end.
		var streak time.Time
		inStreak := false
		for _, f := range fs {
			t.Fixes++
			if f.miss {
				t.Misses++
			}
			if t.Unmute != nil && f.decidedAt.After(t.Unmute.At) {
				t.Since++
			}
			_, rule := MuteBound(t.Fixes, t.Misses)
			switch {
			case !rule || (t.Unmute != nil && t.Since < UnmuteHold):
				inStreak = false
			case !inStreak:
				inStreak, streak = true, f.decidedAt
			}
		}
		var rule bool
		t.Upper, rule = MuteBound(t.Fixes, t.Misses)
		t.Held = rule && t.Unmute != nil && t.Since < UnmuteHold
		t.Muted = rule && !t.Held
		if t.Muted {
			t.MutedAt, t.MutedBy = streak, MutedByRule
		}
		out = append(out, t)
	}
	slices.SortFunc(out, func(a, b TypeMute) int {
		return strings.Compare(a.LibraryID+"\x00"+a.Field+"\x00"+a.Path+"\x00"+a.Kind, b.LibraryID+"\x00"+b.Field+"\x00"+b.Path+"\x00"+b.Kind)
	})
	return out
}

// MuteStats returns the muting state of every fix type that has a counted
// fix, per library.
//
// A unique fix is a library, a book uuid, a field and the current and
// proposed values; its latest decision by a person, individual or bulk, is
// the one counted. Carried and agent decisions are not, except a carried
// approval that was applied: it keeps the original's decision time and
// outranks it by id, so undoing the write counts as a miss (ADR-034). Rejected is a miss,
// and so is an applied fix that a completed undo reverted; approved, applied,
// stale, blocked and failed are correct.
func (r *FixRepo) MuteStats(ctx context.Context) ([]TypeMute, error) {
	return readMuteStats(ctx, r.pool)
}

// querier is a pool or a transaction.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func readMuteStats(ctx context.Context, q querier) ([]TypeMute, error) {
	rows, err := q.Query(ctx, `SELECT d.library_id, d.proposal, d.decided_at,
			d.status = 'rejected' OR (d.status = 'applied' AND EXISTS (
				SELECT 1 FROM operations u
				JOIN LATERAL (SELECT state FROM operation_events WHERE operation_id = u.id ORDER BY seq DESC LIMIT 1) e ON true
				WHERE u.undoes = d.operation_id AND e.state = 'completed'))
		FROM (SELECT DISTINCT ON (r.library_id, e.uuid, p.field, p.proposal->'current', p.proposal->'proposed')
				r.library_id, p.proposal, p.status, p.operation_id, p.decided_at
			FROM proposals p
			JOIN runs r ON r.id = p.run_id
			JOIN evidence e ON e.run_id = p.run_id AND e.book_id = p.book_id
			WHERE (p.decision_mode IN ('individual', 'bulk') OR (p.decision_mode = 'carried' AND p.status = 'applied')) AND p.decided_by <> $1
			ORDER BY r.library_id, e.uuid, p.field, p.proposal->'current', p.proposal->'proposed', p.decided_at DESC, p.id DESC) d`, AgentActor)
	if err != nil {
		return nil, fmt.Errorf("store: muting counts: %w", err)
	}
	defer rows.Close()
	var fixes []countedFix
	for rows.Next() {
		var f countedFix
		var raw []byte
		if err := rows.Scan(&f.libraryID, &raw, &f.decidedAt, &f.miss); err != nil {
			return nil, fmt.Errorf("store: scan counted fix: %w", err)
		}
		var p evidence.Proposal
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, fmt.Errorf("store: decode counted fix: %w", err)
		}
		f.typ = FixTypeOf(p)
		fixes = append(fixes, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read counted fixes: %w", err)
	}
	urows, err := q.Query(ctx, `SELECT DISTINCT ON (library_id, field, path, kind) library_id, field, path, kind, unmuted_by, unmuted_at
		FROM fix_type_unmutes ORDER BY library_id, field, path, kind, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("store: unmutes: %w", err)
	}
	defer urows.Close()
	unmutes := map[libraryType]Unmute{}
	for urows.Next() {
		var k libraryType
		var u Unmute
		if err := urows.Scan(&k.lib, &k.typ.Field, &k.typ.Path, &k.typ.Kind, &u.By, &u.At); err != nil {
			return nil, fmt.Errorf("store: scan unmute: %w", err)
		}
		unmutes[k] = u
	}
	if err := urows.Err(); err != nil {
		return nil, fmt.Errorf("store: read unmutes: %w", err)
	}
	return muteStats(fixes, unmutes), nil
}

// Unmute records that by showed the type of a library again. It returns
// ErrNotMuted, and stores nothing, when the type isn't muted now. The check
// and the insert run in one transaction under a lock on the type, so two
// requests for the same type record one unmute.
func (r *FixRepo) Unmute(ctx context.Context, libraryID string, t FixType, by string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: begin unmute: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
		strings.Join([]string{"unmute", libraryID, t.Field, t.Path, t.Kind}, "\x1f")); err != nil {
		return fmt.Errorf("store: lock unmute %s/%s/%s: %w", t.Field, t.Path, t.Kind, err)
	}
	stats, err := readMuteStats(ctx, tx)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(stats, func(s TypeMute) bool { return s.Muted && s.LibraryID == libraryID && s.FixType == t }) {
		return ErrNotMuted
	}
	if _, err := tx.Exec(ctx, `INSERT INTO fix_type_unmutes (library_id, field, path, kind, unmuted_by) VALUES ($1, $2, $3, $4, $5)`,
		libraryID, t.Field, t.Path, t.Kind, by); err != nil {
		return fmt.Errorf("store: unmute %s/%s/%s: %w", t.Field, t.Path, t.Kind, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: commit unmute: %w", err)
	}
	return nil
}
