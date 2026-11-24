-- The recall sample (v1 design section 6.4): 200 books drawn from the latest
-- checked run, stratified by format, language, tier and whether the book has
-- a fix. Felix answers blind, from Calibre's value and the book's front-matter
-- excerpt, never Bookwarden's proposal.
--
-- The answers are confirmations, not labels. They measure recall and never
-- certify anything, so nothing here feeds the labels, the decisions or the
-- proposals tables, and no code path copies one into them.
--
-- Additive only. run_id has no foreign key on purpose: the sample keeps the
-- run it was drawn from (CreateSample pins it so retention keeps it), and a
-- later change of retention must not delete a sample's history.

CREATE TABLE recall_samples (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    library_id text NOT NULL,
    run_id     bigint NOT NULL,
    seed       bigint NOT NULL,
    size       integer NOT NULL CHECK (size > 0),
    -- The rules the run's seal check ran under when the sample was drawn.
    findings_version integer NOT NULL,
    decision_version integer NOT NULL,
    -- Each stratum's share of the library at the draw:
    -- {"EPUB|eng|A|fix": {"library": 312, "sampled": 4}, ...}
    strata     jsonb NOT NULL,
    created_by text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE recall_sample_books (
    sample_id bigint NOT NULL REFERENCES recall_samples (id),
    book_id   integer NOT NULL,
    uuid      text NOT NULL,
    stratum   text NOT NULL,
    -- Calibre's first language code at the draw: the title equivalence folds
    -- articles by it.
    language  text NOT NULL DEFAULT '',
    position  integer NOT NULL CHECK (position >= 1),
    PRIMARY KEY (sample_id, position),
    UNIQUE (sample_id, book_id)
);

-- The fixes the sample's run had for each sampled book, copied at the draw.
-- The recall reads only these, so pruning, a broken seal or a rebuilt
-- projection can't change a result afterwards. A book without a fix has no
-- row.
CREATE TABLE recall_sample_facts (
    sample_id bigint NOT NULL,
    book_id   integer NOT NULL,
    field     text NOT NULL,
    proposed  jsonb NOT NULL,
    PRIMARY KEY (sample_id, book_id, field),
    FOREIGN KEY (sample_id, book_id) REFERENCES recall_sample_books (sample_id, book_id)
);

-- The first time a sample's results were shown. Once they have been, the
-- sample is closed and takes no more answers: the answers stay blind to
-- Bookwarden's proposals until then, and can't be revised after seeing them.
CREATE TABLE recall_closures (
    sample_id bigint PRIMARY KEY REFERENCES recall_samples (id),
    closed_at timestamptz NOT NULL DEFAULT now()
);

-- One row per answer; a later answer on the same (sample, book, field) wins.
CREATE TABLE confirmations (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    sample_id   bigint NOT NULL,
    book_id     integer NOT NULL,
    uuid        text NOT NULL,
    field       text NOT NULL CHECK (field IN ('title', 'authors', 'publisher', 'pubdate', 'isbn', 'languages')),
    answer      text NOT NULL CHECK (answer IN ('calibre_right', 'calibre_wrong', 'skip')),
    typed_value text,
    answered_by text NOT NULL,
    answered_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (sample_id, book_id) REFERENCES recall_sample_books (sample_id, book_id),
    CHECK (typed_value IS NULL OR answer = 'calibre_wrong')
);
CREATE INDEX confirmations_latest ON confirmations (sample_id, book_id, field, id DESC);

CREATE TRIGGER recall_samples_append_only BEFORE UPDATE OR DELETE ON recall_samples
    FOR EACH ROW EXECUTE FUNCTION reject_change();
CREATE TRIGGER recall_sample_books_append_only BEFORE UPDATE OR DELETE ON recall_sample_books
    FOR EACH ROW EXECUTE FUNCTION reject_change();
CREATE TRIGGER recall_sample_facts_append_only BEFORE UPDATE OR DELETE ON recall_sample_facts
    FOR EACH ROW EXECUTE FUNCTION reject_change();
CREATE TRIGGER recall_closures_append_only BEFORE UPDATE OR DELETE ON recall_closures
    FOR EACH ROW EXECUTE FUNCTION reject_change();
CREATE TRIGGER confirmations_append_only BEFORE UPDATE OR DELETE ON confirmations
    FOR EACH ROW EXECUTE FUNCTION reject_change();
