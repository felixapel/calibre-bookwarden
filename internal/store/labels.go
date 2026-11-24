package store

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

// CertificationLabels is how many labels with zero misses one fix type needs
// before it can earn automation: the one-sided 95 % lower bound on precision
// reaches 99.5 % (ADR-034, v2 design section 5).
const CertificationLabels = 598

// TypeLabels is the labels one fix type has earned over all runs. A fix type
// is a field, the evidence path (the sealed roots, sorted, joined with "+")
// and whether the proposal fills an empty value or replaces one.
type TypeLabels struct {
	Field, Path, Kind string
	Labels, Misses    int
}

// Lower is the one-sided 95 % Clopper-Pearson lower bound on the type's
// precision; ok is false without labels.
func (t TypeLabels) Lower() (float64, bool) { return LowerBound(t.Labels, t.Misses) }

// FixKind is "fill" when the proposal's current value is empty, otherwise
// "replace".
func FixKind(p evidence.Proposal) string {
	if len(p.Current) == 0 {
		return "fill"
	}
	return "replace"
}

// FixPath is the proposal's sealed roots, sorted and joined with "+".
func FixPath(p evidence.Proposal) string {
	return strings.Join(slices.Sorted(slices.Values(p.Roots)), "+")
}

// Precision counts the labels per fix type: individual decisions made with
// the agents' verdicts hidden. Approved, applied, stale, blocked and failed
// proposals are correct, since the person approved them; rejected ones are
// misses. Bulk, shown and agent decisions never count.
func (r *FixRepo) Precision(ctx context.Context) ([]TypeLabels, error) {
	// A fix proposed again in a later run is the same fix: it counts once,
	// judged by its first decision, since every later one may have seen its
	// verdicts. Any person's rejection of it, in any run, makes it a miss.
	rows, err := r.pool.Query(ctx, `WITH firsts AS (
			SELECT DISTINCT ON (r.library_id, p.book_id, p.proposal) r.library_id, p.book_id, p.proposal, p.decision_mode, p.verdicts_shown
			FROM proposals p JOIN runs r ON r.id = p.run_id
			WHERE p.decision_mode <> 'carried'
			ORDER BY r.library_id, p.book_id, p.proposal, p.run_id, p.id)
		SELECT f.proposal, EXISTS (SELECT 1 FROM proposals q JOIN runs qr ON qr.id = q.run_id
			WHERE qr.library_id = f.library_id AND q.book_id = f.book_id AND q.proposal = f.proposal
				AND q.status = 'rejected' AND q.decision_mode NOT IN ('agent', 'carried'))
		FROM firsts f WHERE f.decision_mode = 'individual' AND NOT f.verdicts_shown`)
	if err != nil {
		return nil, fmt.Errorf("store: precision labels: %w", err)
	}
	defer rows.Close()
	type key struct{ field, path, kind string }
	counts := map[key]*TypeLabels{}
	for rows.Next() {
		var raw []byte
		var missed bool
		if err := rows.Scan(&raw, &missed); err != nil {
			return nil, fmt.Errorf("store: scan label: %w", err)
		}
		var p evidence.Proposal
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, fmt.Errorf("store: decode label: %w", err)
		}
		k := key{p.Field, FixPath(p), FixKind(p)}
		t := counts[k]
		if t == nil {
			t = &TypeLabels{Field: k.field, Path: k.path, Kind: k.kind}
			counts[k] = t
		}
		t.Labels++
		if missed {
			t.Misses++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read labels: %w", err)
	}
	out := make([]TypeLabels, 0, len(counts))
	for _, t := range counts {
		out = append(out, *t)
	}
	slices.SortFunc(out, func(a, b TypeLabels) int {
		return strings.Compare(a.Field+"\x00"+a.Path+"\x00"+a.Kind, b.Field+"\x00"+b.Path+"\x00"+b.Kind)
	})
	return out, nil
}

// LowerBound is the one-sided 95 % Clopper-Pearson lower bound on precision
// after n labels with k misses; ok is false for n = 0. With no misses it is
// 0.05^(1/n); otherwise it solves, by bisection, for the failure rate whose
// chance of k or fewer misses in n labels is 5 %.
func LowerBound(n, k int) (float64, bool) {
	if n <= 0 {
		return 0, false
	}
	if k <= 0 {
		return math.Pow(0.05, 1/float64(n)), true
	}
	if k >= n {
		return 0, true
	}
	lo, hi := 0.0, 1.0 // the failure rate: the tail falls as it grows
	for range 100 {
		mid := (lo + hi) / 2
		if binomialTail(n, k, mid) > 0.05 {
			lo = mid
		} else {
			hi = mid
		}
	}
	return 1 - (lo+hi)/2, true
}

// binomialTail is P(X <= k) for X ~ Binomial(n, p).
func binomialTail(n, k int, p float64) float64 {
	if p <= 0 {
		return 1
	}
	if p >= 1 {
		return 0
	}
	lgN, _ := math.Lgamma(float64(n + 1))
	sum := 0.0
	for i := 0; i <= k; i++ {
		lgI, _ := math.Lgamma(float64(i + 1))
		lgR, _ := math.Lgamma(float64(n - i + 1))
		sum += math.Exp(lgN - lgI - lgR + float64(i)*math.Log(p) + float64(n-i)*math.Log1p(-p))
	}
	return sum
}

// RevealKey is one fix whose agents' verdicts were put on screen.
type RevealKey struct {
	BookID int
	Field  string
}

// RecordReveals remembers that the verdicts of these fixes were shown, so a
// later decision on them is never counted as blind (ADR-034).
func (r *FixRepo) RecordReveals(ctx context.Context, runID int64, keys []RevealKey) error {
	if len(keys) == 0 {
		return nil
	}
	books := make([]int, len(keys))
	fields := make([]string, len(keys))
	for i, k := range keys {
		books[i], fields[i] = k.BookID, k.Field
	}
	if _, err := r.pool.Exec(ctx, `INSERT INTO verdict_reveals (run_id, book_id, field)
		SELECT $1, b, f FROM unnest($2::integer[], $3::text[]) AS k (b, f)
		ON CONFLICT DO NOTHING`, runID, books, fields); err != nil {
		return fmt.Errorf("store: record reveals: %w", err)
	}
	return nil
}
