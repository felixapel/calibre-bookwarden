ALTER TABLE evidence ADD COLUMN projection_version integer NOT NULL DEFAULT 0
    CHECK (projection_version IN (0, 1));

CREATE TABLE findings (
    run_id  bigint NOT NULL,
    book_id integer NOT NULL,
    ordinal integer NOT NULL CHECK (ordinal >= 0),
    code    text NOT NULL,
    format  text NOT NULL DEFAULT '',
    field   text NOT NULL DEFAULT '',
    reason  text NOT NULL DEFAULT '',
    PRIMARY KEY (run_id, book_id, ordinal),
    FOREIGN KEY (run_id, book_id) REFERENCES evidence (run_id, book_id) ON DELETE CASCADE
);
