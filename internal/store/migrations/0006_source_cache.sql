-- Validated source records per ISBN (ADR-019). Only definite outcomes are
-- cached; failures are retried on the next run.
CREATE TABLE source_cache (
    source     text NOT NULL,
    isbn       text NOT NULL,
    outcome    text NOT NULL CHECK (outcome IN ('ok', 'no_match')),
    records    jsonb NOT NULL,
    fetched_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    PRIMARY KEY (source, isbn)
);

CREATE INDEX source_cache_expires_at ON source_cache (expires_at);
