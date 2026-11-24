-- The run ledger: one row per run that retention never deletes, so the
-- burn-in criterion "0 broken seals over 14 consecutive scheduled runs" and
-- the run-time targets can be read after the runs themselves are pruned.
--
-- Additive only. run_id has no foreign key on purpose: the row outlives the
-- run. The row is written when a run ends and again whenever its seal check
-- is written (a re-check overwrites the seal columns). stats is filled by a
-- later change and stays {} until then. No book data.

CREATE TABLE run_history (
    run_id          bigint PRIMARY KEY,
    library_id      text NOT NULL,
    kind            text NOT NULL CHECK (kind IN ('full', 'incremental', 'book')),
    scheduled_for   timestamptz,
    status          text NOT NULL,
    app_version     text NOT NULL,
    started_at      timestamptz NOT NULL,
    finished_at     timestamptz,
    books           integer NOT NULL DEFAULT 0 CHECK (books >= 0),
    reused          integer NOT NULL DEFAULT 0 CHECK (reused >= 0),
    seal_books      integer CHECK (seal_books >= 0),
    seal_broken     integer CHECK (seal_broken >= 0),
    seal_checked_at timestamptz,
    stats           jsonb NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX run_history_scheduled ON run_history (run_id DESC) WHERE scheduled_for IS NOT NULL;

-- Backfill the runs that still exist and have ended.
INSERT INTO run_history (run_id, library_id, kind, scheduled_for, status, app_version, started_at, finished_at,
    books, reused, seal_books, seal_broken, seal_checked_at)
SELECT r.id, r.library_id,
    CASE WHEN r.scope_book_id IS NOT NULL THEN 'book' WHEN r.incremental THEN 'incremental' ELSE 'full' END,
    r.scheduled_for, r.status, r.app_version, r.started_at, r.finished_at,
    (SELECT count(*) FROM run_books b WHERE b.run_id = r.id),
    (SELECT count(*) FROM run_books b WHERE b.run_id = r.id AND b.state = 'checked' AND b.reused),
    s.books, s.broken, s.checked_at
FROM runs r LEFT JOIN seal_checks s ON s.run_id = r.id
WHERE r.status IN ('completed', 'cancelled', 'failed');
