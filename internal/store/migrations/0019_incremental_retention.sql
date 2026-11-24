-- Incremental runs, scheduled runs and retention (ADR-039).
--
-- Additive only. Every ADD COLUMN has a constant default, so Postgres records
-- it in the catalog and rewrites nothing. The statements are written to be
-- run twice, which the backfill test does.

-- A run that may reuse a book's file evidence (ADR-039 part 1), and the slot
-- a scheduled run was started for. scheduled_for is NULL for a run somebody
-- started by hand; the scheduler reads it to see which slots are served.
ALTER TABLE runs ADD COLUMN IF NOT EXISTS incremental boolean NOT NULL DEFAULT false;
ALTER TABLE runs ADD COLUMN IF NOT EXISTS scheduled_for timestamptz;

-- Whether the book's file evidence was reused from an earlier package. It is
-- not sealed: a reused package is byte-identical to a full check's.
ALTER TABLE run_books ADD COLUMN IF NOT EXISTS reused boolean NOT NULL DEFAULT false;

-- The inspector build the file evidence of this package came from, as the
-- verify package's key (inspect.Version and the limits it ran under). It is
-- not sealed. '' (every package from before this migration) is never reused.
ALTER TABLE evidence ADD COLUMN IF NOT EXISTS inspect_version text NOT NULL DEFAULT '';

-- When the file evidence of this package was measured from the files: the
-- check's own time, or, for a reused package, the time carried forward from
-- the package it came from. Incremental runs never reuse evidence measured
-- more than 8 days ago. NULL (every package from before this migration) is
-- never reused. Not sealed.
ALTER TABLE evidence ADD COLUMN IF NOT EXISTS measured_at timestamptz;

-- A run the admin keeps whatever retention says, such as the baseline of a
-- rule-change measurement (bookwarden eval). Pruning never removes it.
ALTER TABLE runs ADD COLUMN IF NOT EXISTS pinned boolean NOT NULL DEFAULT false;

-- "The newest package of this book" is one index probe instead of a scan of
-- every run's evidence.
CREATE INDEX IF NOT EXISTS evidence_book_run ON evidence (book_id, run_id DESC);

-- Which excerpts each package refers to, so that retention finds the ones no
-- package refers to without opening every jsonb package. Written in the
-- transaction that writes the package; the rows go with it.
CREATE TABLE IF NOT EXISTS evidence_excerpts (
    run_id  bigint  NOT NULL,
    book_id integer NOT NULL,
    sha256  text    NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    PRIMARY KEY (run_id, book_id, sha256),
    FOREIGN KEY (run_id, book_id) REFERENCES evidence (run_id, book_id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS evidence_excerpts_sha ON evidence_excerpts (sha256);

-- Backfill from the packages already stored. A hash that is not 64 lowercase
-- hex characters is skipped; no sealed package has one.
INSERT INTO evidence_excerpts (run_id, book_id, sha256)
SELECT DISTINCT e.run_id, e.book_id, h.v #>> '{}'
FROM evidence e
CROSS JOIN LATERAL jsonb_path_query(e.package, '$.formats[*].excerpt.sha256') AS h(v)
WHERE e.package @? '$.formats[*].excerpt.sha256'
  AND (h.v #>> '{}') ~ '^[0-9a-f]{64}$'
ON CONFLICT DO NOTHING;
