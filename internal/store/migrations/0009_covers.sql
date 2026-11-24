ALTER TABLE evidence DROP CONSTRAINT evidence_projection_version_check;
ALTER TABLE evidence ADD CONSTRAINT evidence_projection_version_check
    CHECK (projection_version IN (0, 1, 2, 3, 4));

-- One row per book with a decoded cover (ADR-023), for duplicate checks.
CREATE TABLE covers (
    run_id  bigint NOT NULL,
    book_id integer NOT NULL,
    sha256  text NOT NULL CHECK (sha256 <> ''),
    dhash   bigint NOT NULL,
    width   integer NOT NULL CHECK (width > 0),
    height  integer NOT NULL CHECK (height > 0),
    PRIMARY KEY (run_id, book_id),
    FOREIGN KEY (run_id, book_id) REFERENCES evidence (run_id, book_id) ON DELETE CASCADE
);
CREATE INDEX covers_sha256 ON covers (run_id, sha256);
