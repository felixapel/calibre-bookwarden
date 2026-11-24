-- Front-matter excerpts (ADR-032 part 2). A sealed package holds only the
-- SHA-256 and the unit map of each format's excerpt; the text itself is here,
-- addressed by that hash, so that the package stays small and the seal covers
-- the text through the hash. Readers re-hash a body on every read.
--
-- Written in the transaction that writes the book's package, with
-- ON CONFLICT DO NOTHING: two formats or books with the same text share a row.
-- Nothing deletes a row yet; unreferenced excerpts are pruned with the
-- retention work (M7).
--
-- Additive only. The number is 0016 because 0015 belongs to another change;
-- migrations need not be contiguous.
CREATE TABLE excerpts (
    sha256     text PRIMARY KEY CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    body       text NOT NULL,
    chars      integer NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
