-- Verified projection (ADR-038). A whole-run check opens and verifies every
-- sealed package of a finished run once; the pages then read what the check
-- found instead of re-verifying the run on every request.
--
-- Additive only: four new tables, no existing table is rewritten. Runs that
-- finished before this migration have no check, so their pages keep
-- verifying until the first check (a run's end, the nightly sweep or
-- `bookwarden verify-seals`) fills these tables in.

-- The latest check of each run. first_broken_book is NULL when broken is 0.
CREATE TABLE seal_checks (
    run_id            bigint PRIMARY KEY REFERENCES runs (id) ON DELETE CASCADE,
    checked_at        timestamptz NOT NULL,
    books             integer NOT NULL CHECK (books >= 0),
    broken            integer NOT NULL CHECK (broken >= 0),
    first_broken_book integer,
    detail            text NOT NULL DEFAULT '',
    -- The rules the check verified under: a binary with other rules doesn't
    -- trust it, and the sweep checks the run again.
    findings_version  integer NOT NULL,
    decision_version  integer NOT NULL,
    CHECK ((broken = 0) = (first_broken_book IS NULL))
);

-- One row per checked book, written by the check from the verified book:
-- the displayed title, the re-derived tier and the item counts the cursors
-- are validated against. Rows exist only while the run's check passes.
CREATE TABLE book_projection (
    run_id          bigint NOT NULL,
    book_id         integer NOT NULL,
    title           text NOT NULL,
    tier            text NOT NULL,
    findings_count  integer NOT NULL CHECK (findings_count >= 0),
    proposals_count integer NOT NULL CHECK (proposals_count >= 0),
    PRIMARY KEY (run_id, book_id),
    FOREIGN KEY (run_id, book_id) REFERENCES evidence (run_id, book_id) ON DELETE CASCADE
);

-- The report's findings of each book (the current derivation, which differs
-- from the stored findings rows of an older projection version) and its
-- current proposals, in the ordinal order the cursors use.
CREATE TABLE finding_projection (
    run_id  bigint NOT NULL,
    book_id integer NOT NULL,
    ordinal integer NOT NULL CHECK (ordinal >= 0),
    code    text NOT NULL,
    format  text NOT NULL DEFAULT '',
    field   text NOT NULL DEFAULT '',
    reason  text NOT NULL DEFAULT '',
    PRIMARY KEY (run_id, book_id, ordinal),
    FOREIGN KEY (run_id, book_id) REFERENCES book_projection (run_id, book_id) ON DELETE CASCADE
);

CREATE TABLE fix_projection (
    run_id   bigint NOT NULL,
    book_id  integer NOT NULL,
    ordinal  integer NOT NULL CHECK (ordinal >= 0),
    field    text NOT NULL,
    proposal jsonb NOT NULL,
    PRIMARY KEY (run_id, book_id, ordinal),
    FOREIGN KEY (run_id, book_id) REFERENCES book_projection (run_id, book_id) ON DELETE CASCADE
);
