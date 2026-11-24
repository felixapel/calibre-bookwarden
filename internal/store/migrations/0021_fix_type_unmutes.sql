-- Unmuting a fix type (v1 design section 6.3). A type is muted by a rule over
-- the decisions, so a mute is computed and never stored; only an admin's
-- unmute is. It holds until 10 more fixes of the type have been decided.
-- Append-only like the other ledgers.
CREATE TABLE fix_type_unmutes (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    library_id text NOT NULL,
    field      text NOT NULL,
    path       text NOT NULL,
    kind       text NOT NULL CHECK (kind IN ('fill', 'replace')),
    unmuted_by text NOT NULL,
    unmuted_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX fix_type_unmutes_type ON fix_type_unmutes (library_id, field, path, kind, id DESC);

CREATE TRIGGER fix_type_unmutes_append_only BEFORE UPDATE OR DELETE ON fix_type_unmutes
    FOR EACH ROW EXECUTE FUNCTION reject_change();
