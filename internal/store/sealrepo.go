package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/felixapel/calibre-bookwarden/internal/decide"
	"github.com/felixapel/calibre-bookwarden/internal/findings"
	"github.com/felixapel/calibre-bookwarden/internal/report"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

var (
	_ report.SealStore        = (*ReportRepo)(nil)
	_ report.ProjectionReader = (*reportSnapshot)(nil)
)

// sealLockBase namespaces the transaction advisory lock that serialises two
// checks of one run ("seal" in ASCII, in the high half).
// foreignKeyViolation is Postgres's SQLSTATE for a missing referenced row.
const foreignKeyViolation = "23503"

const sealLockBase int64 = 0x7365616c << 32

// SaveSealCheck replaces the run's projection and upserts its check in one
// transaction, so a page never sees a check without its projection.
func (r *ReportRepo) SaveSealCheck(ctx context.Context, c report.SealCheck, books []report.BookProjection) error {
	err := r.saveSealCheck(ctx, c, books)
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == foreignKeyViolation {
		// The run was pruned while it was being checked.
		return fmt.Errorf("store: run %d: %w", c.RunID, verify.ErrNoSuchRun)
	}
	return err
}

func (r *ReportRepo) saveSealCheck(ctx context.Context, c report.SealCheck, books []report.BookProjection) error {
	tx, err := r.store.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: begin seal check: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, sealLockBase|(c.RunID&0xffffffff)); err != nil {
		return fmt.Errorf("store: lock seal check: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM book_projection WHERE run_id=$1`, c.RunID); err != nil {
		return fmt.Errorf("store: clear projection: %w", err)
	}
	bookRows := make([][]any, 0, len(books))
	var findingRows, fixRows [][]any
	for _, b := range books {
		bookRows = append(bookRows, []any{c.RunID, b.BookID, b.Title, b.Tier, len(b.Findings), len(b.Proposals), b.SearchText})
		for i, f := range b.Findings {
			findingRows = append(findingRows, []any{c.RunID, b.BookID, i, f.Code, f.Format, f.Field, f.Reason})
		}
		for i, p := range b.Proposals {
			raw, err := json.Marshal(p)
			if err != nil {
				return fmt.Errorf("store: book %d proposal: %w", b.BookID, err)
			}
			fixRows = append(fixRows, []any{c.RunID, b.BookID, i, p.Field, raw})
		}
	}
	for _, t := range []struct {
		table string
		cols  []string
		rows  [][]any
	}{
		{"book_projection", []string{"run_id", "book_id", "title", "tier", "findings_count", "proposals_count", "search_text"}, bookRows},
		{"finding_projection", []string{"run_id", "book_id", "ordinal", "code", "format", "field", "reason"}, findingRows},
		{"fix_projection", []string{"run_id", "book_id", "ordinal", "field", "proposal"}, fixRows},
	} {
		if len(t.rows) == 0 {
			continue
		}
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{t.table}, t.cols, pgx.CopyFromRows(t.rows)); err != nil {
			return fmt.Errorf("store: write %s: %w", t.table, err)
		}
	}
	// Only a run's first check carries rejections into it: a later one (the
	// sweep, a rules change) would back-fill a run a person already looked at.
	var checked bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM seal_checks WHERE run_id = $1)`, c.RunID).Scan(&checked); err != nil {
		return fmt.Errorf("store: read seal check: %w", err)
	}
	if !checked {
		if err := carryRejections(ctx, tx, c.RunID); err != nil {
			return err
		}
	}
	var first *int
	if c.FirstBroken > 0 {
		first = &c.FirstBroken
	}
	if _, err := tx.Exec(ctx, `INSERT INTO seal_checks (run_id, checked_at, books, broken, first_broken_book, detail, findings_version, decision_version)
 VALUES ($1, now(), $2, $3, $4, $5, $6, $7)
 ON CONFLICT (run_id) DO UPDATE SET checked_at=now(), books=$2, broken=$3, first_broken_book=$4, detail=$5, findings_version=$6, decision_version=$7`,
		c.RunID, c.Books, c.Broken, first, c.Detail, c.FindingsVersion, c.DecisionVersion); err != nil {
		return fmt.Errorf("store: write seal check: %w", err)
	}
	if _, err := tx.Exec(ctx, historyUpsert, c.RunID); err != nil {
		return fmt.Errorf("store: run %d ledger: %w", c.RunID, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: commit seal check: %w", err)
	}
	return nil
}

// fixPathSQL is a proposal's path as FixPath builds it: the sealed roots,
// sorted bytewise and joined with "+". Roots that aren't a JSON array (a
// proposal sealed with "roots": null) are no path.
const fixPathSQL = `CASE WHEN jsonb_typeof(%[1]s->'roots') = 'array'
  THEN COALESCE((SELECT string_agg(r, '+' ORDER BY r COLLATE "C") FROM jsonb_array_elements_text(%[1]s->'roots') r), '') ELSE '' END`

// carryRejections records a person's decision as already made for the run's
// projected fixes that are the same fix: the same library, book uuid, field,
// current and proposed values (compared exactly) and path. Only the latest
// person's decision on the fix in an earlier run counts: a rejection, or an
// approval not yet applied (Felix, 2026-11-24: an approval no longer dies at
// the next check), including one Apply retired as "superseded" because this
// run had already written the book's evidence; anything the agent decided
// never carries. A row the run
// already has is left alone. The copy keeps the original's decision time. An
// earlier approval that carried is retired as stale ("carried"), so Apply
// writes the fix once, from the newest run.
//
// The earlier decisions are reduced to one row per fix first (DISTINCT ON,
// newest run first) and then hash-joined to the run's projection, so the cost
// is one pass over the decided proposals plus one over the run's fixes, with
// no index on evidence (uuid) to maintain.
func carryRejections(ctx context.Context, tx pgx.Tx, runID int64) error {
	if _, err := tx.Exec(ctx, carrySQL, runID); err != nil {
		return fmt.Errorf("store: carry decisions into run %d: %w", runID, err)
	}
	if _, err := tx.Exec(ctx, retireCarriedSQL, runID); err != nil {
		return fmt.Errorf("store: retire approvals carried into run %d: %w", runID, err)
	}
	return nil
}

// carrySQL is the statement carryRejections runs, with the run id as $1.
var carrySQL = `WITH earlier AS (
  SELECT pe.uuid, p.field, p.proposal->'current' AS cur, p.proposal->'proposed' AS prop, ` + fmt.Sprintf(fixPathSQL, "p.proposal") + ` AS path,
   p.run_id, p.id, p.status, p.reason, p.decided_by, p.decided_at, p.carried_from
  FROM proposals p
  JOIN runs pr ON pr.id = p.run_id
  JOIN evidence pe ON pe.run_id = p.run_id AND pe.book_id = p.book_id
  WHERE pr.library_id = (SELECT library_id FROM runs WHERE id = $1) AND pr.id < $1 AND p.decision_mode <> 'agent'),
 latest AS (
  SELECT DISTINCT ON (uuid, field, cur, prop, path) uuid, field, cur, prop, path, id, status, reason, decided_by, decided_at, carried_from
  FROM earlier ORDER BY uuid, field, cur, prop, path, run_id DESC, id DESC)
INSERT INTO proposals (run_id, book_id, field, proposal, status, decided_by, decided_at, decision_mode, verdicts_shown, carried_from)
 SELECT x.run_id, x.book_id, x.field, x.proposal, CASE WHEN l.status = 'stale' THEN 'approved' ELSE l.status END, l.decided_by, l.decided_at, 'carried', true, COALESCE(l.carried_from, l.id)
 FROM fix_projection x
 JOIN evidence e ON e.run_id = x.run_id AND e.book_id = x.book_id
 JOIN latest l ON l.uuid = e.uuid AND l.field = x.field AND l.cur = x.proposal->'current' AND l.prop = x.proposal->'proposed'
  AND l.path = ` + fmt.Sprintf(fixPathSQL, "x.proposal") + `
 WHERE x.run_id = $1 AND (l.status IN ('rejected', 'approved') OR (l.status = 'stale' AND l.reason = 'superseded'))
 ON CONFLICT (run_id, book_id, field) DO NOTHING`

// retireCarriedSQL marks as stale ("carried") every earlier, unapplied
// person approval of a fix that carried into run $1 as approved: the same
// library, book uuid, field, values and path.
var retireCarriedSQL = `UPDATE proposals o SET status = 'stale', reason = 'carried'
 FROM proposals n
 JOIN evidence ne ON ne.run_id = n.run_id AND ne.book_id = n.book_id,
  evidence oe, runs orun
 WHERE n.run_id = $1 AND n.decision_mode = 'carried' AND n.status = 'approved'
  AND oe.run_id = o.run_id AND oe.book_id = o.book_id AND oe.uuid = ne.uuid
  AND orun.id = o.run_id AND orun.library_id = (SELECT library_id FROM runs WHERE id = $1) AND o.run_id < $1
  AND o.field = n.field AND o.status = 'approved' AND o.operation_id IS NULL AND o.decision_mode <> 'agent'
  AND o.proposal->'current' = n.proposal->'current' AND o.proposal->'proposed' = n.proposal->'proposed'
  AND ` + fmt.Sprintf(fixPathSQL, "o.proposal") + ` = ` + fmt.Sprintf(fixPathSQL, "n.proposal")

// FinishedRuns lists every finished run.
func (r *ReportRepo) FinishedRuns(ctx context.Context) ([]int64, error) {
	rows, err := r.store.Pool.Query(ctx, `SELECT id FROM runs WHERE status IN ('completed','cancelled','failed') AND finished_at IS NOT NULL ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("store: finished runs: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return nil, fmt.Errorf("store: finished runs: %w", err)
	}
	return ids, nil
}

// RunsToCheck lists the finished runs that need a check.
func (r *ReportRepo) RunsToCheck(ctx context.Context, staleBefore time.Time) ([]int64, error) {
	rows, err := r.store.Pool.Query(ctx, `SELECT r.id FROM runs r LEFT JOIN seal_checks c ON c.run_id=r.id
 WHERE r.status IN ('completed','cancelled','failed') AND r.finished_at IS NOT NULL
 AND (c.run_id IS NULL OR c.checked_at < $1 OR c.checked_at < r.finished_at
  OR c.findings_version <> $2 OR c.decision_version <> $3) ORDER BY r.id`, staleBefore, findings.Version, decide.Version)
	if err != nil {
		return nil, fmt.Errorf("store: runs to check: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return nil, fmt.Errorf("store: runs to check: %w", err)
	}
	return ids, nil
}

func (s *reportSnapshot) SealCheck(ctx context.Context) (report.SealCheck, bool, error) {
	c := report.SealCheck{RunID: s.run.ID}
	err := s.tx.QueryRow(ctx, `SELECT c.checked_at, c.books, c.broken, COALESCE(c.first_broken_book, 0), c.detail
 FROM seal_checks c JOIN runs r ON r.id=c.run_id
 WHERE c.run_id=$1 AND r.finished_at IS NOT NULL AND c.checked_at >= r.finished_at
 AND c.findings_version=$2 AND c.decision_version=$3`, s.run.ID, findings.Version, decide.Version).
		Scan(&c.CheckedAt, &c.Books, &c.Broken, &c.FirstBroken, &c.Detail)
	if errors.Is(err, pgx.ErrNoRows) {
		return report.SealCheck{}, false, nil
	}
	if err != nil {
		return report.SealCheck{}, false, fmt.Errorf("store: seal check: %w", err)
	}
	return c, true, nil
}

func (s *reportSnapshot) ProjectedBook(ctx context.Context, bookID int) (report.ProjectedBook, error) {
	var b report.ProjectedBook
	err := s.tx.QueryRow(ctx, `SELECT p.book_id IS NOT NULL, COALESCE(p.findings_count, 0), COALESCE(p.proposals_count, 0)
 FROM run_books b LEFT JOIN book_projection p ON p.run_id=b.run_id AND p.book_id=b.book_id
 WHERE b.run_id=$1 AND b.book_id=$2`, s.run.ID, bookID).Scan(&b.Checked, &b.Findings, &b.Proposals)
	if errors.Is(err, pgx.ErrNoRows) {
		return report.ProjectedBook{}, nil
	}
	if err != nil {
		return report.ProjectedBook{}, fmt.Errorf("store: projected book: %w", err)
	}
	b.InRun = true
	return b, nil
}

// pageQuery builds the SELECT of a paged projection read from its filters.
type pageQuery struct {
	sql  strings.Builder
	args []any
}

func (q *pageQuery) arg(v any) string {
	q.args = append(q.args, v)
	return "$" + strconv.Itoa(len(q.args))
}

func (q *pageQuery) and(format string, v any) { q.sql.WriteString(" AND " + format + q.arg(v)) }

func (q *pageQuery) after(alias string, c *report.Cursor) {
	if c != nil {
		q.sql.WriteString(" AND (" + alias + ".book_id, " + alias + ".ordinal) > (" + q.arg(c.BookID) + ", " + q.arg(c.Ordinal) + ")")
	}
}

func (q *pageQuery) finish(alias string, limit int) {
	q.sql.WriteString(" ORDER BY " + alias + ".book_id, " + alias + ".ordinal")
	if limit > 0 {
		q.sql.WriteString(" LIMIT " + q.arg(limit))
	}
}

func (s *reportSnapshot) FindingRows(ctx context.Context, f report.Filter, after *report.Cursor, limit int, visit func(report.Row, int) error) error {
	var q pageQuery
	q.sql.WriteString(`SELECT f.book_id, f.ordinal, p.title, f.code, f.format, f.field, f.reason
 FROM finding_projection f JOIN book_projection p ON p.run_id=f.run_id AND p.book_id=f.book_id
 WHERE f.run_id=` + q.arg(s.run.ID))
	if f.BookID > 0 {
		q.and("f.book_id=", f.BookID)
	}
	if f.Code != "" {
		q.and("f.code=", f.Code)
	}
	if f.Format != "" {
		q.and("f.format=", f.Format)
	}
	q.after("f", after)
	q.finish("f", limit)
	rows, err := s.tx.Query(ctx, q.sql.String(), q.args...)
	if err != nil {
		return fmt.Errorf("store: projected findings: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		row := report.Row{RunID: s.run.ID}
		var ordinal int
		if err := rows.Scan(&row.BookID, &ordinal, &row.Title, &row.Finding.Code, &row.Finding.Format, &row.Finding.Field, &row.Finding.Reason); err != nil {
			return fmt.Errorf("store: projected finding: %w", err)
		}
		if err := visit(row, ordinal); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: projected findings: %w", err)
	}
	return nil
}

// FixRows lists the run's projected fixes. The search, the Decided filter
// and the sort all happen in SQL, before the page is cut.
func (s *reportSnapshot) FixRows(ctx context.Context, f report.FixFilter, after *report.Cursor, limit int, visit func(report.FixRow, int) error) error {
	var q pageQuery
	q.sql.WriteString(`SELECT x.book_id, x.ordinal, p.title, p.tier, x.proposal` + fixFrom + q.arg(s.run.ID))
	q.fixConditions(f)
	switch f.Sort {
	case report.SortField, report.SortTier, report.SortDecided:
		order := map[string]string{report.SortField: "x.field", report.SortTier: "p.tier", report.SortDecided: "d.decided_at DESC NULLS LAST"}[f.Sort]
		q.sql.WriteString(" ORDER BY " + order + ", x.book_id, x.ordinal")
		if limit > 0 {
			q.sql.WriteString(" LIMIT " + q.arg(limit))
		}
		if f.Skip > 0 {
			q.sql.WriteString(" OFFSET " + q.arg(f.Skip))
		}
	default:
		q.after("x", after)
		q.finish("x", limit)
	}
	rows, err := s.tx.Query(ctx, q.sql.String(), q.args...)
	if err != nil {
		return fmt.Errorf("store: projected fixes: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		row := report.FixRow{RunID: s.run.ID}
		var ordinal int
		var raw []byte
		if err := rows.Scan(&row.BookID, &ordinal, &row.Title, &row.Tier, &raw); err != nil {
			return fmt.Errorf("store: projected fix: %w", err)
		}
		if err := json.Unmarshal(raw, &row.Proposal); err != nil {
			return fmt.Errorf("store: projected proposal of book %d: %w", row.BookID, err)
		}
		if err := visit(row, ordinal); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: projected fixes: %w", err)
	}
	return nil
}

// MutedFixes counts the run's projected fixes of f.MutedTypes that the other
// filters keep.
func (s *reportSnapshot) MutedFixes(ctx context.Context, f report.FixFilter) (int, error) {
	f.Muted = true
	var q pageQuery
	q.sql.WriteString(`SELECT count(*)` + fixFrom + q.arg(s.run.ID))
	q.fixConditions(f)
	var n int
	if err := s.tx.QueryRow(ctx, q.sql.String(), q.args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: muted fixes: %w", err)
	}
	return n, nil
}

const fixFrom = `
 FROM fix_projection x JOIN book_projection p ON p.run_id=x.run_id AND p.book_id=x.book_id
 LEFT JOIN proposals d ON d.run_id=x.run_id AND d.book_id=x.book_id AND d.field=x.field
 WHERE x.run_id=`

// A fix's type as SQL: the sorted path of fixPathSQL, and "fill" for an empty
// current value. They mirror report.TypeOf.
const fixKindSQL = `CASE WHEN COALESCE(jsonb_array_length(
	CASE WHEN jsonb_typeof(x.proposal->'current') = 'array' THEN x.proposal->'current' ELSE '[]'::jsonb END), 0) = 0 THEN 'fill' ELSE 'replace' END`

// muted keeps the fixes of f.MutedTypes with Muted, and leaves them out
// without it. Muted with no types keeps nothing.
func (q *pageQuery) muted(f report.FixFilter) {
	if !f.Muted && len(f.MutedTypes) == 0 {
		return
	}
	if len(f.MutedTypes) == 0 {
		q.sql.WriteString(" AND false")
		return
	}
	fields := make([]string, len(f.MutedTypes))
	paths := make([]string, len(f.MutedTypes))
	kinds := make([]string, len(f.MutedTypes))
	for i, t := range f.MutedTypes {
		fields[i], paths[i], kinds[i] = t.Field, t.Path, t.Kind
	}
	op := "NOT IN"
	if f.Muted {
		op = "IN"
	}
	q.sql.WriteString(" AND (x.field, " + fmt.Sprintf(fixPathSQL, "x.proposal") + ", " + fixKindSQL + ") " + op +
		" (SELECT * FROM unnest(" + q.arg(fields) + "::text[], " + q.arg(paths) + "::text[], " + q.arg(kinds) + "::text[]))")
}

// fixConditions adds the filters of f other than the sort and the cursor.
func (q *pageQuery) fixConditions(f report.FixFilter) {
	q.muted(f)
	if f.BookID > 0 {
		q.and("x.book_id=", f.BookID)
	}
	if f.Field != "" {
		q.and("x.field=", f.Field)
	}
	if f.Tier != "" {
		q.and("p.tier=", f.Tier)
	}
	if f.Query != "" {
		// The folded title and authors the check stored; the query is folded
		// the same way, in Go.
		q.sql.WriteString(" AND position(" + q.arg(report.Fold(f.Query)) + "::text IN p.search_text) > 0")
	}
	switch f.Decided {
	case report.DecidedAgent:
		q.and("d.decided_by=", AgentActor)
	case report.DecidedPerson:
		q.sql.WriteString(" AND d.id IS NOT NULL")
		q.and("d.decided_by<>", AgentActor)
	case report.DecidedDisagree:
		books := make([]int, 0, len(f.Keys))
		fields := make([]string, 0, len(f.Keys))
		for _, k := range f.Keys {
			books, fields = append(books, k.BookID), append(fields, k.Field)
		}
		q.sql.WriteString(" AND d.id IS NULL AND (x.book_id, x.field) IN (SELECT * FROM unnest(" + q.arg(books) + "::integer[], " + q.arg(fields) + "::text[]))")
	}
}

func (s *reportSnapshot) BookRows(ctx context.Context, after, limit int) ([]report.BookSummary, error) {
	rows, err := s.tx.Query(ctx, `SELECT b.book_id, b.uuid, b.state, b.reason, COALESCE(p.title, '')
 FROM run_books b LEFT JOIN book_projection p ON p.run_id=b.run_id AND p.book_id=b.book_id
 WHERE b.run_id=$1 AND b.book_id>$2 ORDER BY b.book_id LIMIT $3`, s.run.ID, after, limit)
	if err != nil {
		return nil, fmt.Errorf("store: projected books: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (report.BookSummary, error) {
		var b report.BookSummary
		var state string
		err := row.Scan(&b.Ref.ID, &b.Ref.UUID, &state, &b.Reason, &b.Title)
		b.State = verify.BookState(state)
		b.Reason = report.Display(b.Reason)
		return b, err
	})
	if err != nil {
		return nil, fmt.Errorf("store: projected books: %w", err)
	}
	return out, nil
}
