package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/decide"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/findings"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/report"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

// syntheticBook is the evidence of book number i (1-based): i%5 formats
// missing in bulk (so some books span several pages), a wrong title sort on
// every third book and a wrong author sort on every fourth, sealed as a
// legacy package (projection versions 0 to 3, or the current one) on every
// other book.
func syntheticBook(t *testing.T, ref library.BookRef, i int) (verify.Outcome, int) {
	t.Helper()
	p := cleanEvidence(ref)
	p.Calibre.Title = fmt.Sprintf("Synthetic Title %d", i)
	if i%3 == 0 {
		p.Calibre.TitleSort = new(string)
		*p.Calibre.TitleSort = "Wrong Sort"
	}
	if i%4 == 0 {
		p.Calibre.Authors = []string{"Ada Reed"}
		p.Calibre.AuthorSort = "Road, The"
		p.Calibre.AuthorSortMap = map[string]string{"Ada Reed": "Reed, Ada"}
	}
	p.Formats = nil
	for j := range (i % 5) * 20 {
		p.Formats = append(p.Formats, evidence.FormatEvidence{Format: fmt.Sprintf("F%03d", j), State: evidence.StateMissing})
	}
	version := findings.Version
	if i == 11 {
		// Another work: the ISBN's record states a title and authors that
		// both differ from Calibre's, and the file's own EPUB is readable.
		clean := cleanEvidence(ref)
		p.Formats = clean.Formats
		p.Lookups = []evidence.Lookup{{ISBN: "9780306406157", Seeds: []string{evidence.SeedFile}, Results: []evidence.SourceResult{{
			Source: evidence.SourceDNB, Outcome: evidence.SourceOK,
			Records: []evidence.SourceRecord{{ID: "1", Title: "Binary Tides", Authors: []string{"Cy Placeholder"}, Languages: []string{"eng"}, ISBNs: []string{"9780306406157"}}},
		}}}}
	}
	switch i {
	case 7:
		// Sealed with an older findings version: a page re-derives it.
		fs, err := findings.DeriveVersion(context.Background(), p, 2)
		if err != nil {
			t.Fatal(err)
		}
		p.FindingsVersion, p.Findings = 2, fs
		raw, seal, err := evidence.Seal(p)
		if err != nil {
			t.Fatal(err)
		}
		return verify.Outcome{State: verify.BookChecked, Evidence: raw, Seal: seal}, version
	case 9:
		// Sealed with a decision (a non-zero decision version).
		raw, seal, err := decide.Seal(context.Background(), p)
		if err != nil {
			t.Fatal(err)
		}
		return verify.Outcome{State: verify.BookChecked, Evidence: raw, Seal: seal}, version
	}
	if i%2 == 0 {
		version = i / 2 % (findings.Version + 1) // 0..findings.Version
		return findingOutcome(t, p, true), version
	}
	return findingOutcome(t, p, false), version
}

// applyProjectionVersion rewrites a legacy book's findings rows as an older
// projection version stored them.
func applyProjectionVersion(ctx context.Context, t *testing.T, s *Store, runID int64, ref library.BookRef, version int) {
	t.Helper()
	if version == findings.Version {
		return
	}
	raw, seal, err := (&RunRepo{pool: s.Pool}).Evidence(ctx, runID, ref.ID)
	if err != nil {
		t.Fatal(err)
	}
	p, err := findings.Open(ctx, raw, seal)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `DELETE FROM findings WHERE run_id=$1 AND book_id=$2`, runID, ref.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE evidence SET projection_version=$3 WHERE run_id=$1 AND book_id=$2`, runID, ref.ID, version); err != nil {
		t.Fatal(err)
	}
	if version == 0 {
		return
	}
	rows, err := findings.DeriveVersion(ctx, p, version)
	if err != nil {
		t.Fatal(err)
	}
	for i, f := range rows {
		if _, err := s.Pool.Exec(ctx, `INSERT INTO findings (run_id,book_id,ordinal,code,format,field,reason) VALUES ($1,$2,$3,$4,$5,$6,$7)`, runID, ref.ID, i, f.Code, f.Format, f.Field, f.Reason); err != nil {
			t.Fatal(err)
		}
	}
}

// syntheticRun stores a finished run of n books, with a failed and a pending
// book among them.
func syntheticRun(t *testing.T, n int) (*RunRepo, *Store, verify.Run, []library.BookRef) {
	t.Helper()
	ctx := context.Background()
	r, s := runRepo(t)
	books := refs(n)
	run, err := r.CreateRun(ctx, verify.Scope{}, "library", "v1", books)
	if err != nil {
		t.Fatal(err)
	}
	for i, ref := range books {
		switch i + 1 {
		case 2:
			if err := r.FinishBook(ctx, run.ID, ref, verify.Outcome{State: verify.BookFailed, Reason: "calibre_unreachable"}); err != nil {
				t.Fatal(err)
			}
			continue
		case 5:
			continue // stays pending
		}
		out, version := syntheticBook(t, ref, i+1)
		if err := r.FinishBook(ctx, run.ID, ref, out); err != nil {
			t.Fatal(err)
		}
		applyProjectionVersion(ctx, t, s, run.ID, ref, version)
	}
	if err := r.SetStatus(ctx, run.ID, verify.RunCompleted, ""); err != nil {
		t.Fatal(err)
	}
	run, err = r.Run(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	return r, s, run, books
}

// result is what one request returned, for comparing the walk with the
// projection.
type result struct {
	Pages any
	Err   string
}

func pagesOf(t *testing.T, name string, next func(c *report.Cursor) (any, *report.Cursor, error)) result {
	t.Helper()
	var all []any
	var c *report.Cursor
	for range 200 {
		page, n, err := next(c)
		if err != nil {
			return result{Pages: all, Err: err.Error()}
		}
		all = append(all, page)
		if n == nil {
			return result{Pages: all}
		}
		c = n
	}
	t.Fatalf("%s: more than 200 pages", name)
	return result{}
}

func collect(ctx context.Context, t *testing.T, svc *report.Service, run verify.Run, books []library.BookRef) map[string]result {
	t.Helper()
	out := map[string]result{}
	id := run.ID
	for _, f := range []report.Filter{{}, {Code: "file_missing"}, {Format: "F001"}, {Code: "file_missing", Format: "F002"}, {BookID: books[3].ID}, {BookID: books[1].ID}, {BookID: 999}, {Code: "no_formats"}, {Code: "title_sort_mismatch"}, {Code: "other_work"}} {
		name := fmt.Sprintf("findings %+v", f)
		out[name] = pagesOf(t, name, func(c *report.Cursor) (any, *report.Cursor, error) {
			p, err := svc.Findings(ctx, id, f, c)
			return p.Rows, p.Next, err
		})
		var buf bytes.Buffer
		err := svc.CSV(ctx, id, f, &buf, report.CSVLimits{})
		res := result{Pages: buf.String()}
		if err != nil {
			res.Err = err.Error()
		}
		out["csv "+name] = res
	}
	for _, f := range []report.FixFilter{{}, {Field: "title_sort"}, {Field: "author_sort"}, {Tier: evidence.TierA}, {Tier: evidence.TierB}, {Tier: evidence.TierC}, {BookID: books[2].ID}, {BookID: books[3].ID}, {BookID: books[0].ID}, {Field: "title_sort", Tier: evidence.TierC}} {
		name := fmt.Sprintf("fixes %+v", f)
		out[name] = pagesOf(t, name, func(c *report.Cursor) (any, *report.Cursor, error) {
			p, err := svc.Fixes(ctx, id, f, c)
			return p.Rows, p.Next, err
		})
	}
	out["books"] = func() result {
		var all []any
		after := 0
		for range 200 {
			p, err := svc.Books(ctx, id, after)
			if err != nil {
				return result{Pages: all, Err: err.Error()}
			}
			all = append(all, p)
			if p.NextBookID == 0 {
				break
			}
			after = p.NextBookID
		}
		return result{Pages: all}
	}()
	// Cursors at the edges and invalid ones.
	for _, c := range []report.Cursor{
		{RunID: id, BookID: books[3].ID, Ordinal: 0},
		{RunID: id, BookID: books[3].ID, Ordinal: 79},
		{RunID: id, BookID: books[3].ID, Ordinal: 80}, // book 4 has 80 findings (+1 sort), see below
		{RunID: id, BookID: books[1].ID, Ordinal: 0},  // failed book
		{RunID: id, BookID: books[4].ID, Ordinal: 0},  // pending book
		{RunID: id, BookID: 12345, Ordinal: 0},
		{RunID: id + 1, BookID: books[3].ID, Ordinal: 0},
		{RunID: id, BookID: books[8].ID, Ordinal: 1},
		{RunID: id, BookID: books[8].ID, Ordinal: 2},
	} {
		c := c
		p, err := svc.Findings(ctx, id, report.Filter{}, &c)
		res := result{Pages: p}
		if err != nil {
			res.Err = err.Error()
		}
		out[fmt.Sprintf("findings cursor %+v", c)] = res
		fp, err := svc.Fixes(ctx, id, report.FixFilter{}, &c)
		fp.Walked = false // the only difference between the two paths
		res = result{Pages: fp}
		if err != nil {
			res.Err = err.Error()
		}
		out[fmt.Sprintf("fixes cursor %+v", c)] = res
	}
	return out
}

func TestProjectionMatchesWalk(t *testing.T) {
	ctx := context.Background()
	_, s, run, books := syntheticRun(t, 24)
	svc := report.New(NewReportRepo(s))

	walked := collect(ctx, t, svc, run, books)
	// Without a check the pages walk, and a walk finds real content to compare.
	if len(walked["findings {Code: Format: BookID:0}"].Pages.([]any)) < 3 {
		t.Fatalf("the fixture is too small: %+v", walked["findings {Code: Format: BookID:0}"])
	}
	if got := walked["findings {Code:other_work Format: BookID:0}"].Pages.([]any); len(got) == 0 || len(got[0].([]report.Row)) == 0 {
		t.Fatalf("the fixture has no other_work book: %+v", got)
	}
	if got := walked["fixes {Field: Tier: BookID:0 Decided: Query: Sort: Keys:[] Skip:0 Muted:false MutedTypes:[]}"].Pages.([]any); len(got) == 0 {
		t.Fatalf("the fixture has no proposals")
	}
	var tiers int
	for _, tier := range []string{evidence.TierA, evidence.TierB, evidence.TierC} {
		if len(walked[fmt.Sprintf("fixes {Field: Tier:%s BookID:0 Decided: Query: Sort: Keys:[] Skip:0 Muted:false MutedTypes:[]}", tier)].Pages.([]any)) > 0 {
			tiers++
		}
	}
	if tiers == 0 {
		t.Fatalf("no tier filter matched anything")
	}

	check, err := svc.CheckSeals(ctx, run.ID)
	if err != nil || check.Broken != 0 || check.Books != 24 || check.FirstBroken != 0 {
		t.Fatalf("CheckSeals = %+v, %v", check, err)
	}
	// Prove the next reads come from the projection: the stored findings
	// rows are not consulted, so damaging them goes unseen until the next check.
	if _, err := s.Pool.Exec(ctx, `UPDATE findings SET reason='edited after the check' WHERE ctid IN (SELECT ctid FROM findings LIMIT 1)`); err != nil {
		t.Fatal(err)
	}
	projected := collect(ctx, t, svc, run, books)
	for name, want := range walked {
		if !reflect.DeepEqual(projected[name], want) {
			t.Errorf("%s:\n projection %+v\n walk       %+v", name, projected[name], want)
		}
	}
	if len(projected) != len(walked) {
		t.Errorf("%d results, want %d", len(projected), len(walked))
	}
}

func damagePages(ctx context.Context, t *testing.T, svc *report.Service, run verify.Run, want error) {
	t.Helper()
	if _, err := svc.Findings(ctx, run.ID, report.Filter{}, nil); !errors.Is(err, want) {
		t.Errorf("Findings = %v, want %v", err, want)
	}
	if _, err := svc.Fixes(ctx, run.ID, report.FixFilter{}, nil); !errors.Is(err, want) {
		t.Errorf("Fixes = %v, want %v", err, want)
	}
	if _, err := svc.Books(ctx, run.ID, 0); !errors.Is(err, want) {
		t.Errorf("Books = %v, want %v", err, want)
	}
	if err := svc.CSV(ctx, run.ID, report.Filter{}, &bytes.Buffer{}, report.CSVLimits{}); !errors.Is(err, want) {
		t.Errorf("CSV = %v, want %v", err, want)
	}
}

func TestCheckSealsCatchesDamage(t *testing.T) {
	for _, tc := range []struct {
		name   string
		damage string
	}{
		{"tampered package", `UPDATE evidence SET package=jsonb_set(package,'{calibre_record,title}','"Edited"') WHERE book_id=40`},
		{"deleted findings row", `DELETE FROM findings WHERE book_id=40 AND ordinal=0`},
		{"edited findings row", `UPDATE findings SET reason='invented' WHERE book_id=40 AND ordinal=0`},
		{"wrong seal", `UPDATE evidence SET seal=repeat('0',64) WHERE book_id=40`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			_, s, run, _ := syntheticRun(t, 12)
			svc := report.New(NewReportRepo(s))
			if _, err := svc.CheckSeals(ctx, run.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.Findings(ctx, run.ID, report.Filter{}, nil); err != nil {
				t.Fatalf("before the damage: %v", err)
			}
			if _, err := s.Pool.Exec(ctx, tc.damage); err != nil {
				t.Fatal(err)
			}
			// Until the next check, a page reads the projection of the
			// last passing one.
			if _, err := svc.Findings(ctx, run.ID, report.Filter{}, nil); err != nil {
				t.Fatalf("between checks: %v", err)
			}
			// A second book is damaged too: the walk goes on, counts, and
			// names the lowest broken one.
			if _, err := s.Pool.Exec(ctx, `UPDATE evidence SET seal=repeat('0',64) WHERE book_id=100`); err != nil {
				t.Fatal(err)
			}
			check, err := svc.CheckSeals(ctx, run.ID)
			if err != nil || check.Broken != 2 || check.FirstBroken != 40 || check.Books != 12 || !strings.Contains(check.Detail, "book 40") {
				t.Fatalf("CheckSeals = %+v, %v", check, err)
			}
			damagePages(ctx, t, svc, run, report.ErrIntegrity)
			_, err = svc.Findings(ctx, run.ID, report.Filter{}, nil)
			if err == nil || !strings.Contains(err.Error(), "book 40") {
				t.Fatalf("the error doesn't name the first broken book: %v", err)
			}
			// A page for one intact book walks that book only.
			if _, err := svc.Findings(ctx, run.ID, report.Filter{BookID: 30}, nil); err != nil {
				t.Fatalf("an intact book's page: %v", err)
			}
			if _, err := svc.Findings(ctx, run.ID, report.Filter{BookID: 40}, nil); !errors.Is(err, report.ErrIntegrity) {
				t.Fatalf("the broken book's page: %v", err)
			}
			var n int
			if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM book_projection`).Scan(&n); err != nil || n != 0 {
				t.Fatalf("a damaged run keeps %d projected books (%v)", n, err)
			}
		})
	}
}

func TestPagesWithoutACheckVerify(t *testing.T) {
	ctx := context.Background()
	_, s, run, _ := syntheticRun(t, 12)
	svc := report.New(NewReportRepo(s))
	if _, err := s.Pool.Exec(ctx, `UPDATE evidence SET seal=repeat('0',64) WHERE book_id=120`); err != nil {
		t.Fatal(err)
	}
	// Book 120 is the last frozen book: a page that stops long before it
	// still fails, as ADR-017 says.
	damagePages(ctx, t, svc, run, report.ErrIntegrity)
	if n := rowCount(t, s, `seal_checks`); n != 0 {
		t.Fatalf("a page wrote %d checks", n)
	}
}

func rowCount(t *testing.T, s *Store, table string) int {
	t.Helper()
	var n int
	if err := s.Pool.QueryRow(context.Background(), `SELECT count(*) FROM `+table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCheckNewerThanTheRunsEnd(t *testing.T) {
	ctx := context.Background()
	r, s, run, _ := syntheticRun(t, 12)
	svc := report.New(NewReportRepo(s))
	if _, err := svc.CheckSeals(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	// A check older than the run's end doesn't count: damage is walked.
	if _, err := s.Pool.Exec(ctx, `UPDATE seal_checks SET checked_at=$2 WHERE run_id=$1`, run.ID, run.FinishedAt.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE evidence SET seal=repeat('0',64) WHERE book_id=120`); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Findings(ctx, run.ID, report.Filter{}, nil); !errors.Is(err, report.ErrIntegrity) {
		t.Fatalf("a stale check was trusted: %v", err)
	}
	if ids, err := NewReportRepo(s).RunsToCheck(ctx, time.Now().Add(-24*time.Hour)); err != nil || len(ids) != 1 || ids[0] != run.ID {
		t.Fatalf("RunsToCheck = %v, %v", ids, err)
	}
	// A run that hasn't ended can't be checked.
	active, err := r.CreateRun(ctx, verify.Scope{}, "library", "v1", refs(1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CheckSeals(ctx, active.ID); !errors.Is(err, report.ErrRunNotFinished) {
		t.Fatalf("CheckSeals of a running run = %v", err)
	}
}

func TestRunsToCheck(t *testing.T) {
	ctx := context.Background()
	r, s, first, _ := syntheticRun(t, 3)
	svc := report.New(NewReportRepo(s))
	repo := NewReportRepo(s)
	books := refs(3)
	mk := func(status verify.RunStatus) int64 {
		run, err := r.CreateRun(ctx, verify.Scope{}, "library", "v1", books)
		if err != nil {
			t.Fatal(err)
		}
		if err := r.SetStatus(ctx, run.ID, status, ""); err != nil {
			t.Fatal(err)
		}
		return run.ID
	}
	second, third := mk(verify.RunCancelled), mk(verify.RunFailed)
	if _, err := r.CreateRun(ctx, verify.Scope{}, "library", "v1", books); err != nil { // running: never listed
		t.Fatal(err)
	}
	cut := time.Now().Add(-24 * time.Hour)
	if ids, err := repo.RunsToCheck(ctx, cut); err != nil || !reflect.DeepEqual(ids, []int64{first.ID, second, third}) {
		t.Fatalf("missing checks: %v, %v", ids, err)
	}
	for _, id := range []int64{first.ID, second} {
		if _, err := svc.CheckSeals(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	if ids, err := repo.RunsToCheck(ctx, cut); err != nil || !reflect.DeepEqual(ids, []int64{third}) {
		t.Fatalf("after checking two: %v, %v", ids, err)
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE seal_checks SET checked_at=now()-interval '25 hours' WHERE run_id=$1`, second); err != nil {
		t.Fatal(err)
	}
	if ids, err := repo.RunsToCheck(ctx, cut); err != nil || !reflect.DeepEqual(ids, []int64{second, third}) {
		t.Fatalf("with one stale: %v, %v", ids, err)
	}
	// Every finished run, whatever its check and the clock: the running run
	// isn't one.
	if ids, err := repo.FinishedRuns(ctx); err != nil || !reflect.DeepEqual(ids, []int64{first.ID, second, third}) {
		t.Fatalf("FinishedRuns = %v, %v", ids, err)
	}
}

// TestProjectionTiming times a page of a 3,000-book run through the
// projection and through the walk (ADR-038's target is 300 ms), and prints
// the plans of the paging queries.
func TestProjectionTiming(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a 3,000-book run")
	}
	ctx := context.Background()
	r, s := runRepo(t)
	books := refs(3000)
	run, err := r.CreateRun(ctx, verify.Scope{}, "library", "v1", books)
	if err != nil {
		t.Fatal(err)
	}
	for i, ref := range books {
		p := cleanEvidence(ref)
		p.Calibre.Title = fmt.Sprintf("Synthetic Title %d", i)
		if i%3 == 0 {
			p.Calibre.TitleSort = new(string)
			*p.Calibre.TitleSort = "Wrong Sort"
		}
		p.Formats = nil
		for j := range i % 4 {
			p.Formats = append(p.Formats, evidence.FormatEvidence{Format: fmt.Sprintf("F%03d", j), State: evidence.StateMissing})
		}
		if err := r.FinishBook(ctx, run.ID, ref, findingOutcome(t, p, false)); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.SetStatus(ctx, run.ID, verify.RunCompleted, ""); err != nil {
		t.Fatal(err)
	}
	svc := report.New(NewReportRepo(s))
	page := func() (time.Duration, int, int) {
		start := time.Now()
		fixes, err := svc.Fixes(ctx, run.ID, report.FixFilter{}, nil)
		if err != nil {
			t.Fatal(err)
		}
		found, err := svc.Findings(ctx, run.ID, report.Filter{Code: "file_missing"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return time.Since(start), len(fixes.Rows), len(found.Rows)
	}
	walkTime, nf, nr := page()
	if nf == 0 || nr == 0 {
		t.Fatalf("the run has no proposals (%d) or findings (%d)", nf, nr)
	}
	start := time.Now()
	check, err := svc.CheckSeals(ctx, run.ID)
	if err != nil || check.Broken != 0 {
		t.Fatalf("CheckSeals = %+v, %v", check, err)
	}
	checkTime := time.Since(start)
	var best time.Duration
	for i := range 5 {
		d, f2, r2 := page()
		if f2 != nf || r2 != nr {
			t.Fatalf("the projection returned %d/%d rows, the walk %d/%d", f2, r2, nf, nr)
		}
		if i == 0 || d < best {
			best = d
		}
	}
	t.Logf("3000 books: Fixes + Findings page by walk %v, by projection %v (best of 5); whole-run check %v", walkTime, best, checkTime)
	if best > 300*time.Millisecond {
		t.Errorf("a projected page takes %v, want at most 300ms", best)
	}
	for _, q := range []string{
		`SELECT x.book_id, x.ordinal, p.title, p.tier, x.proposal FROM fix_projection x JOIN book_projection p ON p.run_id=x.run_id AND p.book_id=x.book_id WHERE x.run_id=$1 AND p.tier='B' AND x.field='title_sort' AND (x.book_id, x.ordinal) > (20000, 0) ORDER BY x.book_id, x.ordinal LIMIT 51`,
		`SELECT f.book_id, f.ordinal, p.title, f.code, f.format, f.field, f.reason FROM finding_projection f JOIN book_projection p ON p.run_id=f.run_id AND p.book_id=f.book_id WHERE f.run_id=$1 AND f.code='file_missing' AND f.format='F002' AND (f.book_id, f.ordinal) > (20000, 0) ORDER BY f.book_id, f.ordinal LIMIT 51`,
		`SELECT b.book_id, b.uuid, b.state, b.reason, COALESCE(p.title, '') FROM run_books b LEFT JOIN book_projection p ON p.run_id=b.run_id AND p.book_id=b.book_id WHERE b.run_id=$1 AND b.book_id>20000 ORDER BY b.book_id LIMIT 51`,
	} {
		rows, err := s.Pool.Query(ctx, `EXPLAIN (ANALYZE, COSTS OFF, TIMING OFF) `+q, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		var plan []string
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, line)
		}
		rows.Close()
		t.Logf("%s\n  %s", q[:60], strings.Join(plan, "\n  "))
	}
}

// TestCheckIsTiedToCodeVersions: a check made under other findings or
// decision rules than the running binary's is not served, and is checked again.
func TestCheckIsTiedToCodeVersions(t *testing.T) {
	for _, column := range []string{"findings_version", "decision_version"} {
		t.Run(column, func(t *testing.T) {
			ctx := context.Background()
			_, s, run, _ := syntheticRun(t, 12)
			svc := report.New(NewReportRepo(s))
			if _, err := svc.CheckSeals(ctx, run.ID); err != nil {
				t.Fatal(err)
			}
			cut := time.Now().Add(-24 * time.Hour)
			if ids, err := NewReportRepo(s).RunsToCheck(ctx, cut); err != nil || len(ids) != 0 {
				t.Fatalf("a current check is listed: %v, %v", ids, err)
			}
			// The last book is damaged after the check: only a walk sees it.
			if _, err := s.Pool.Exec(ctx, `UPDATE evidence SET seal=repeat('0',64) WHERE book_id=120`); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.Findings(ctx, run.ID, report.Filter{}, nil); err != nil {
				t.Fatalf("a current check: %v", err)
			}
			if _, err := s.Pool.Exec(ctx, `UPDATE seal_checks SET `+column+`=`+column+`-1`); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.Findings(ctx, run.ID, report.Filter{}, nil); !errors.Is(err, report.ErrIntegrity) {
				t.Fatalf("a check under other %s was served: %v", column, err)
			}
			if ids, err := NewReportRepo(s).RunsToCheck(ctx, cut); err != nil || len(ids) != 1 || ids[0] != run.ID {
				t.Fatalf("RunsToCheck = %v, %v", ids, err)
			}
		})
	}
}

// TestSaveSealCheckOfAPrunedRun: a run pruned while it was being checked is
// gone, which the caller can tell from a failure.
func TestSaveSealCheckOfAPrunedRun(t *testing.T) {
	_, s, _, _ := syntheticRun(t, 3)
	err := NewReportRepo(s).SaveSealCheck(context.Background(), report.SealCheck{RunID: 99999}, nil)
	if !errors.Is(err, verify.ErrNoSuchRun) {
		t.Fatalf("SaveSealCheck of a missing run = %v", err)
	}
	// A projection of a book the run doesn't have is the same fault.
	err = NewReportRepo(s).SaveSealCheck(context.Background(), report.SealCheck{RunID: 99999}, []report.BookProjection{{BookID: 10}})
	if !errors.Is(err, verify.ErrNoSuchRun) {
		t.Fatalf("SaveSealCheck of a missing run's book = %v", err)
	}
}
