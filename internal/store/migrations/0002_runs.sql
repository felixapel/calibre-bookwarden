-- Checks of the library. The partial unique index on a constant allows one
-- running or waiting run at a time.
CREATE TABLE runs (
    id               bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    scope_book_id    integer,
    library_id       text NOT NULL,
    status           text NOT NULL CHECK (status IN ('running', 'waiting', 'completed', 'cancelled', 'failed')),
    error            text NOT NULL DEFAULT '',
    cancel_requested boolean NOT NULL DEFAULT false,
    app_version      text NOT NULL,
    started_at       timestamptz NOT NULL DEFAULT now(),
    finished_at      timestamptz
);
CREATE UNIQUE INDEX runs_one_active ON runs ((true)) WHERE status IN ('running', 'waiting');

-- Each run's frozen book list, and each book's result.
CREATE TABLE run_books (
    run_id      bigint NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    book_id     integer NOT NULL,
    uuid        text NOT NULL,
    position    integer NOT NULL,
    state       text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'checked', 'source_changed', 'failed')),
    reason      text NOT NULL DEFAULT '',
    finished_at timestamptz,
    PRIMARY KEY (run_id, book_id)
);
CREATE INDEX run_books_state ON run_books (run_id, state);

-- One sealed evidence package per checked book. The seal is re-checked on
-- every load, so an edited package is detected.
CREATE TABLE evidence (
    run_id         bigint NOT NULL,
    book_id        integer NOT NULL,
    uuid           text NOT NULL,
    schema_version integer NOT NULL,
    package        jsonb NOT NULL,
    seal           text NOT NULL CHECK (seal ~ '^[0-9a-f]{64}$'),
    created_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (run_id, book_id),
    FOREIGN KEY (run_id, book_id) REFERENCES run_books (run_id, book_id) ON DELETE CASCADE
);
