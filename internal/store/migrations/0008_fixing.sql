-- Guided fixing (spec section 8, ADR-021).

-- The writes switch: an append-only log; the newest row wins and none means
-- off (spec section 4).
CREATE TABLE settings (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name       text NOT NULL CHECK (name IN ('writes_enabled')),
    value      jsonb NOT NULL,
    changed_by text NOT NULL,
    changed_at timestamptz NOT NULL DEFAULT now()
);

-- The write ledger: an immutable header per operation, then one row per
-- state, each committed before the next step starts. A restore that writes
-- adds a second restoring row first, holding the book as it found it.
CREATE TABLE operations (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    kind         text NOT NULL CHECK (kind IN ('apply', 'undo')),
    undoes       bigint REFERENCES operations (id),
    run_id       bigint,
    library_id   text NOT NULL,
    book_id      integer NOT NULL,
    uuid         text NOT NULL,
    changes      jsonb NOT NULL,
    before       jsonb NOT NULL,
    requested_by text NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    CHECK ((kind = 'apply') = (run_id IS NOT NULL) AND (kind = 'undo') = (undoes IS NOT NULL)),
    FOREIGN KEY (run_id, book_id) REFERENCES evidence (run_id, book_id)
);
CREATE INDEX operations_undoes ON operations (undoes) WHERE undoes IS NOT NULL;
CREATE INDEX operations_run ON operations (run_id) WHERE run_id IS NOT NULL;

CREATE TABLE operation_events (
    operation_id bigint NOT NULL REFERENCES operations (id),
    seq          integer NOT NULL,
    state        text NOT NULL CHECK (state IN ('requested', 'writing', 'verifying', 'completed', 'failed', 'restoring', 'restored')),
    after        jsonb,
    detail       text NOT NULL DEFAULT '',
    at           timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (operation_id, seq)
);

-- Approvals of sealed proposals, one per run, book and field. The values
-- are copied from the verified decision.
CREATE TABLE proposals (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    run_id       bigint NOT NULL,
    book_id      integer NOT NULL,
    field        text NOT NULL CHECK (field IN ('title', 'authors', 'languages', 'isbn', 'publisher', 'pubdate')),
    proposal     jsonb NOT NULL,
    status       text NOT NULL CHECK (status IN ('approved', 'rejected', 'applied', 'stale', 'blocked', 'failed')),
    reason       text NOT NULL DEFAULT '',
    operation_id bigint REFERENCES operations (id),
    decided_by   text NOT NULL,
    decided_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (run_id, book_id, field),
    CHECK (proposal->>'field' = field),
    FOREIGN KEY (run_id, book_id) REFERENCES evidence (run_id, book_id)
);
CREATE INDEX proposals_approved ON proposals (run_id, book_id) WHERE status = 'approved' AND operation_id IS NULL;
CREATE INDEX proposals_operation ON proposals (operation_id) WHERE operation_id IS NOT NULL;

-- One incident per operation that doesn't complete; an open one pauses all
-- writes.
CREATE TABLE incidents (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    operation_id    bigint NOT NULL UNIQUE REFERENCES operations (id),
    reason          text NOT NULL CHECK (reason IN ('write_error', 'readback_mismatch', 'interrupted')),
    detail          text NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now(),
    acknowledged_by text,
    acknowledged_at timestamptz,
    CHECK ((acknowledged_by IS NULL) = (acknowledged_at IS NULL))
);
CREATE INDEX incidents_open ON incidents (id) WHERE acknowledged_at IS NULL;

CREATE FUNCTION reject_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION '% is append-only', TG_TABLE_NAME;
END
$$;
CREATE TRIGGER settings_append_only BEFORE UPDATE OR DELETE ON settings
    FOR EACH ROW EXECUTE FUNCTION reject_change();
CREATE TRIGGER operations_append_only BEFORE UPDATE OR DELETE ON operations
    FOR EACH ROW EXECUTE FUNCTION reject_change();
CREATE TRIGGER operation_events_append_only BEFORE UPDATE OR DELETE ON operation_events
    FOR EACH ROW EXECUTE FUNCTION reject_change();
