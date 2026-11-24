-- Possible duplicates (ADR-024): the rules a run was checked under, each
-- book's exact keys, and whether its cover is compared.
ALTER TABLE runs ADD COLUMN duplicates_version integer NOT NULL DEFAULT 0
    CHECK (duplicates_version >= 0);

CREATE TABLE duplicate_keys (
    run_id  bigint NOT NULL,
    book_id integer NOT NULL,
    kind    text NOT NULL CHECK (kind IN ('isbn', 'file', 'title_author')),
    value   text NOT NULL,
    detail  text NOT NULL DEFAULT '',
    PRIMARY KEY (run_id, kind, value, book_id),
    FOREIGN KEY (run_id, book_id) REFERENCES evidence (run_id, book_id) ON DELETE CASCADE,
    CHECK (CASE kind WHEN 'isbn' THEN value ~ '^97[89][0-9]{10}$' ELSE value ~ '^[0-9a-f]{64}$' END)
);
CREATE INDEX duplicate_keys_book ON duplicate_keys (run_id, book_id);

-- NULL for covers projected before M5.2.
ALTER TABLE covers ADD COLUMN comparable boolean;
