package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/felixapel/calibre-bookwarden/internal/testutil/pgtest"
)

// recordHandler keeps every log record and calls hook for each one.
type recordHandler struct {
	mu   sync.Mutex
	msgs []string
	hook func(msg string)
}

func (h *recordHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *recordHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Message)
	r.Attrs(func(a slog.Attr) bool {
		fmt.Fprintf(&b, " %s=%v", a.Key, a.Value)
		return true
	})
	h.mu.Lock()
	h.msgs = append(h.msgs, b.String())
	hook := h.hook
	h.mu.Unlock()
	if hook != nil {
		hook(b.String())
	}
	return nil
}
func (h *recordHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *recordHandler) WithGroup(string) slog.Handler      { return h }
func (h *recordHandler) count(sub string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, m := range h.msgs {
		if strings.Contains(m, sub) {
			n++
		}
	}
	return n
}

func TestMigrateLockWaitGivesUp(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s := openTest(t)
	h := &recordHandler{}
	s.Logger = slog.New(h)
	s.MigrateLockWait = 700 * time.Millisecond
	s.lockPoll = 20 * time.Millisecond
	s.lockLogEvery = 100 * time.Millisecond

	holder, err := s.Pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Release()
	if _, err := holder.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationLockID); err != nil {
		t.Fatal(err)
	}
	var holderPID int
	if err := holder.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&holderPID); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	err = s.Migrate(ctx)
	if err == nil {
		t.Fatal("Migrate succeeded while another session held the lock")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Migrate waited %s, want about 700ms", elapsed)
	}
	if !strings.Contains(err.Error(), "migration lock") || !strings.Contains(err.Error(), fmt.Sprint(holderPID)) {
		t.Fatalf("error should name the lock and the holder pid %d: %v", holderPID, err)
	}
	if h.count(fmt.Sprintf("pid=%d", holderPID)) == 0 {
		t.Fatalf("no log line names the holder pid %d: %v", holderPID, h.msgs)
	}

	// Released in time, the same Store gets through.
	if _, err := holder.Exec(ctx, "SELECT pg_advisory_unlock($1)", migrationLockID); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate after release: %v", err)
	}
}

func TestMigrateLockWaitSucceedsWhenReleased(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	s.MigrateLockWait = 10 * time.Second
	s.lockPoll = 20 * time.Millisecond
	holder, err := s.Pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Release()
	if _, err := holder.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationLockID); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		time.Sleep(200 * time.Millisecond)
		_, _ = holder.Exec(ctx, "SELECT pg_advisory_unlock($1)", migrationLockID)
	}()
	err = s.Migrate(ctx)
	<-done
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
}

func TestMigrateRefusesNewerSchema(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	if _, err := s.Pool.Exec(ctx, `CREATE TABLE schema_migrations (
		version integer PRIMARY KEY, name text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now());
		INSERT INTO schema_migrations (version, name) VALUES (9999, '9999_from_the_future.sql')`); err != nil {
		t.Fatal(err)
	}
	ms, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	err = s.Migrate(ctx)
	if err == nil {
		t.Fatal("Migrate accepted a schema newer than the binary")
	}
	for _, want := range []string{"9999", fmt.Sprint(ms[len(ms)-1].version), "newer"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	var applied int
	if err := s.Pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&applied); err != nil {
		t.Fatal(err)
	}
	var users bool
	if err := s.Pool.QueryRow(ctx, "SELECT to_regclass('users') IS NOT NULL").Scan(&users); err != nil {
		t.Fatal(err)
	}
	if applied != 1 || users {
		t.Fatalf("Migrate applied something: %d rows in schema_migrations, users table %v", applied, users)
	}
}

func TestMigrateLockTimeoutRetries(t *testing.T) {
	tests := []struct {
		name        string
		releaseAt   int // release the table lock at this retry log line; 0 means never
		wantErr     bool
		wantRetries int
	}{
		{"lock never released", 0, true, 3},
		{"released after the first retry", 1, false, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			s := openTest(t)
			if _, err := s.Pool.Exec(ctx, "CREATE TABLE locked_tbl (id integer)"); err != nil {
				t.Fatal(err)
			}
			holder, err := s.Pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			released := false
			release := func() {
				if !released {
					released = true
					_ = holder.Rollback(ctx)
				}
			}
			defer release()
			if _, err := holder.Exec(ctx, "LOCK TABLE locked_tbl IN ACCESS SHARE MODE"); err != nil {
				t.Fatal(err)
			}
			h := &recordHandler{}
			retries := 0
			h.hook = func(msg string) {
				if strings.Contains(msg, "lock timeout") {
					retries++
					if retries == tc.releaseAt {
						release()
					}
				}
			}
			s.Logger = slog.New(h)
			s.lockTimeout = 100 * time.Millisecond
			s.lockRetryBackoff = 20 * time.Millisecond

			ms := []migration{{version: 1, name: "0001_alter.sql", sql: "ALTER TABLE locked_tbl ADD COLUMN extra integer"}}
			start := time.Now()
			err = s.applyMigrations(ctx, ms)
			if elapsed := time.Since(start); elapsed > 5*time.Second {
				t.Fatalf("took %s, want it bounded by the lock timeout", elapsed)
			}
			if tc.wantErr {
				var pgErr *pgconn.PgError
				if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
					t.Fatalf("err = %v, want a wrapped 55P03", err)
				}
			} else if err != nil {
				t.Fatalf("err = %v", err)
			}
			if got := h.count("lock timeout"); got != tc.wantRetries {
				t.Fatalf("retry log lines = %d, want %d: %v", got, tc.wantRetries, h.msgs)
			}
			var hasCol bool
			if err := s.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'locked_tbl' AND column_name = 'extra')`).Scan(&hasCol); err != nil {
				t.Fatal(err)
			}
			if hasCol == tc.wantErr {
				t.Fatalf("column present = %v with wantErr %v", hasCol, tc.wantErr)
			}
		})
	}
}

// seedReview inserts a review row with the given key and state.
func seedReview(t *testing.T, s *Store, runID int64, kind, state string) int64 {
	t.Helper()
	var id int64
	if err := s.Pool.QueryRow(context.Background(), `INSERT INTO reviews (run_id, kind, mode, state, requested_by)
		VALUES ($1, $2, 'shadow', $3, 'test') RETURNING id`, runID, kind, state).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestUniqueOpenReviewsMigration(t *testing.T) {
	ctx := context.Background()
	_, r, s := fixRepo(t)
	run1 := checkedRun(t, r, refs(1))
	run2 := checkedRun(t, r, refs(1))
	// Put the schema back as it was before 0015, then seed what the race made.
	if _, err := s.Pool.Exec(ctx, `DROP INDEX reviews_open_once`); err != nil {
		t.Fatal(err)
	}
	// A running review is the one Next resumes, so it outranks an older queued one.
	dupQueued1 := seedReview(t, s, run1, "manual", "queued")
	keepRunning := seedReview(t, s, run1, "manual", "running")
	dupQueued2 := seedReview(t, s, run1, "manual", "queued")
	dupRunning := seedReview(t, s, run1, "manual", "running")
	finished := seedReview(t, s, run1, "manual", "done")
	otherKind := seedReview(t, s, run1, "calibration", "queued")
	keepOldest := seedReview(t, s, run2, "manual", "queued")
	dupLater := seedReview(t, s, run2, "manual", "queued")
	runningFirst := seedReview(t, s, run2, "calibration", "running")
	dupAfterRunning := seedReview(t, s, run2, "calibration", "queued")

	raw, err := migrationFiles.ReadFile("migrations/0015_unique_open_reviews.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, string(raw)); err != nil {
		t.Fatalf("0015 on a database with duplicates: %v", err)
	}
	state := func(id int64) (string, string, bool) {
		var st, reason string
		var finishedAt *time.Time
		if err := s.Pool.QueryRow(ctx, "SELECT state, stop_reason, finished_at FROM reviews WHERE id = $1", id).Scan(&st, &reason, &finishedAt); err != nil {
			t.Fatal(err)
		}
		return st, reason, finishedAt != nil
	}
	untouched := map[int64]string{keepRunning: "running", otherKind: "queued", keepOldest: "queued", runningFirst: "running", finished: "done"}
	for id, want := range untouched {
		if st, reason, fin := state(id); st != want || reason != "" || fin {
			t.Errorf("review %d changed: %s %q finished=%v", id, st, reason, fin)
		}
	}
	for _, id := range []int64{dupQueued1, dupQueued2, dupRunning, dupLater, dupAfterRunning} {
		if st, reason, fin := state(id); st != "stopped" || reason == "" || !fin {
			t.Errorf("surplus review %d = %s %q finished=%v, want stopped with a reason", id, st, reason, fin)
		}
	}
	var n int
	if err := s.Pool.QueryRow(ctx, "SELECT count(*) FROM reviews").Scan(&n); err != nil || n != 10 {
		t.Fatalf("reviews = %d, %v; a migration must never delete rows", n, err)
	}
	// The index now refuses a second open review of the same kind.
	if _, err := s.Pool.Exec(ctx, `INSERT INTO reviews (run_id, kind, mode, requested_by) VALUES ($1, 'manual', 'on', 'x')`, run1); err == nil {
		t.Fatal("a second open manual review was accepted")
	}
}

func TestQueueConcurrentIsExclusive(t *testing.T) {
	ctx := context.Background()
	a, _, r := agentRepo(t)
	for _, kind := range []string{ReviewManual, ReviewCalibration, ReviewAuto} {
		t.Run(kind, func(t *testing.T) {
			run := checkedRun(t, r, refs(1))
			const n = 16
			var wg sync.WaitGroup
			start := make(chan struct{})
			oks := make(chan bool, n)
			errs := make(chan error, n)
			for range n {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					_, ok, err := a.Queue(ctx, run, kind, ModeShadow, "t")
					oks <- ok
					errs <- err
				}()
			}
			close(start)
			wg.Wait()
			close(oks)
			close(errs)
			for err := range errs {
				if err != nil {
					t.Fatalf("Queue: %v", err)
				}
			}
			won := 0
			for ok := range oks {
				if ok {
					won++
				}
			}
			if won != 1 {
				t.Fatalf("%d Queue calls succeeded, want exactly 1", won)
			}
		})
	}
}

// reportrepo's duplicate self-join looks keys up by (run_id, kind, value); the
// primary key of duplicate_keys already leads with those columns.
func TestDuplicateKeysJoinIsIndexed(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := s.Pool.Query(ctx, `SELECT array_agg(a.attname::text ORDER BY k.ord)
		FROM pg_index i
		JOIN LATERAL unnest(i.indkey::int2[]) WITH ORDINALITY AS k(attnum, ord) ON true
		JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = k.attnum
		WHERE i.indrelid = 'duplicate_keys'::regclass AND i.indisvalid
		GROUP BY i.indexrelid`)
	if err != nil {
		t.Fatal(err)
	}
	cols, err := pgx.CollectRows(rows, pgx.RowTo[[]string])
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cols {
		if len(c) >= 3 && c[0] == "run_id" && c[1] == "kind" && c[2] == "value" {
			return
		}
	}
	t.Fatalf("no index on duplicate_keys leads with (run_id, kind, value): %v", cols)
}

var (
	addCheck = regexp.MustCompile(`(?is)ADD\s+(?:CONSTRAINT\s+\S+\s+)?CHECK\b[^;]*`)
	addFK    = regexp.MustCompile(`(?is)ADD\s+(?:CONSTRAINT\s+\S+\s+)?FOREIGN\s+KEY\b[^;]*`)
	notValid = regexp.MustCompile(`(?i)\bNOT\s+VALID\b`)
)

// unsafeConstraints returns the statements in sql that add a CHECK or foreign
// key constraint without NOT VALID, which scans the whole table under lock.
// Blind spot, kept on purpose: a column-level constraint (ADD COLUMN ... CHECK
// or ... REFERENCES) is not recognised.
func unsafeConstraints(sql string) []string {
	var bad []string
	for _, re := range []*regexp.Regexp{addCheck, addFK} {
		for _, m := range re.FindAllString(sql, -1) {
			if !notValid.MatchString(m) {
				bad = append(bad, strings.TrimSpace(m))
			}
		}
	}
	return bad
}

func TestUnsafeConstraintsDetector(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		bad  int
	}{
		{"check", "ALTER TABLE t ADD CONSTRAINT c CHECK (a > 0);", 1},
		{"check lower", "alter table t add check (a > 0);", 1},
		{"check not valid", "ALTER TABLE t ADD CONSTRAINT c CHECK (a > 0) NOT VALID;", 0},
		{"fk", "ALTER TABLE t ADD CONSTRAINT f FOREIGN KEY (a) REFERENCES u (id);", 1},
		{"fk not valid then validate", "ALTER TABLE t ADD FOREIGN KEY (a) REFERENCES u (id)\n  NOT VALID;\nALTER TABLE t VALIDATE CONSTRAINT t_a_fkey;", 0},
		{"column check in create table", "CREATE TABLE t (a int CHECK (a > 0));", 0},
		{"multi-line", "ALTER TABLE t\n  ADD CONSTRAINT c\n  CHECK (a > 0);", 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := unsafeConstraints(tc.sql); len(got) != tc.bad {
				t.Fatalf("unsafeConstraints = %v, want %d", got, tc.bad)
			}
		})
	}
}

func TestNewMigrationsAddConstraintsNotValid(t *testing.T) {
	ms, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.version < 15 {
			continue // applied in production already
		}
		if bad := unsafeConstraints(m.sql); len(bad) > 0 {
			t.Errorf("%s adds a constraint without NOT VALID (add it NOT VALID, then VALIDATE CONSTRAINT): %q", m.name, bad)
		}
	}
}

// The agent-review CLI opens the store without migrating, so Queue keeps its
// own guard for a database that hasn't run 0015.
func TestQueueGuardWithoutIndex(t *testing.T) {
	ctx := context.Background()
	a, _, r := agentRepo(t)
	if _, err := a.pool.Exec(ctx, `DROP INDEX reviews_open_once`); err != nil {
		t.Fatal(err)
	}
	run := checkedRun(t, r, refs(1))
	if _, ok, err := a.Queue(ctx, run, ReviewManual, ModeShadow, "t"); err != nil || !ok {
		t.Fatalf("first Queue = %v, %v", ok, err)
	}
	if _, ok, err := a.Queue(ctx, run, ReviewManual, ModeShadow, "t"); err != nil || ok {
		t.Fatalf("second Queue = %v, %v; want false without the index too", ok, err)
	}
}

func TestMigrateErrorWithoutLockTimeoutIsNotRetried(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s := openTest(t)
	h := &recordHandler{}
	s.Logger = slog.New(h)
	s.lockRetryBackoff = 10 * time.Millisecond
	ms := []migration{{version: 1, name: "0001_bad.sql", sql: "ALTER TABLE nowhere ADD COLUMN"}}
	if err := s.applyMigrations(ctx, ms); err == nil {
		t.Fatal("a broken migration succeeded")
	}
	if n := h.count("retrying"); n != 0 {
		t.Fatalf("%d retry lines for a syntax error: %v", n, h.msgs)
	}
	var applied int
	if err := s.Pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&applied); err != nil || applied != 0 {
		t.Fatalf("schema_migrations rows = %d, %v", applied, err)
	}
}

func TestMigrateCancelledDuringLockWaitReleasesNothing(t *testing.T) {
	bg := context.Background()
	s := openTest(t)
	s.lockPoll = 20 * time.Millisecond
	holder, err := s.Pool.Acquire(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Release()
	if _, err := holder.Exec(bg, "SELECT pg_advisory_lock($1)", migrationLockID); err != nil {
		t.Fatal(err)
	}
	var holderPID int
	if err := holder.QueryRow(bg, "SELECT pg_backend_pid()").Scan(&holderPID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(bg, 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := s.Migrate(ctx); err == nil {
		t.Fatal("Migrate succeeded while the lock was held")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("cancelled Migrate took %s", elapsed)
	}
	// Only this test's own database: the other packages' tests share the server
	// and hold advisory locks of their own.
	var others int
	if err := holder.QueryRow(bg, `SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND pid <> $1
		AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`, holderPID).Scan(&others); err != nil {
		t.Fatal(err)
	}
	if others != 0 {
		t.Fatalf("%d advisory locks held by other sessions after the cancelled Migrate", others)
	}
}

// A session in another database can hold the same advisory key; it is not
// blocking us and must not be reported as the holder.
func TestMigrateLockHolderIsFilteredByDatabase(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s := openTest(t)
	other, err := Open(ctx, pgtest.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	foreign, err := other.Pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer foreign.Release()
	var foreignPID int
	if err := foreign.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&foreignPID); err != nil {
		t.Fatal(err)
	}
	if _, err := foreign.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationLockID); err != nil {
		t.Fatal(err)
	}
	holder, err := s.Pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Release()
	var localPID int
	if err := holder.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&localPID); err != nil {
		t.Fatal(err)
	}
	if _, err := holder.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationLockID); err != nil {
		t.Fatal(err)
	}
	pids, err := lockHolders(ctx, holder)
	if err != nil {
		t.Fatal(err)
	}
	if len(pids) != 1 || pids[0] != localPID {
		t.Fatalf("lockHolders = %v, want only %d (not %d from another database)", pids, localPID, foreignPID)
	}
}
