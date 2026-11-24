-- M4 grounded extraction, shadow phase (spec sections 4 and 8).
--
-- Additive only, and written to be run twice (IF NOT EXISTS), as 0019.

-- The raw answers of the extractors, by the sha256 of the answer. A package
-- seals only the hash; the answer is private and never shown or logged.
CREATE TABLE IF NOT EXISTS extraction_raw (
    sha256     text        PRIMARY KEY CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    body       text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- The extraction cache: the cache key (excerpt, prompt version, provider
-- host, model and effort) to the raw answer it produced. A row goes with its
-- answer.
CREATE TABLE IF NOT EXISTS extraction_cache (
    key        text        PRIMARY KEY CHECK (key ~ '^[0-9a-f]{64}$'),
    sha256     text        NOT NULL REFERENCES extraction_raw (sha256) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- Which raw answers each package refers to, so that retention finds the ones
-- no package refers to with one indexed anti-join. Written in the transaction
-- that writes the package; the rows go with it.
CREATE TABLE IF NOT EXISTS evidence_extraction_raw (
    run_id  bigint  NOT NULL,
    book_id integer NOT NULL,
    sha256  text    NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    PRIMARY KEY (run_id, book_id, sha256),
    FOREIGN KEY (run_id, book_id) REFERENCES evidence (run_id, book_id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS evidence_extraction_raw_sha ON evidence_extraction_raw (sha256);
