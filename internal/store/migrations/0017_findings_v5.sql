-- Findings version 5 (ADR-035, "Findings v5"): the projection version a book's
-- findings rows were written under may now be 5.
--
-- The new set contains the old one, so every existing row satisfies it. Rows
-- written under older versions keep their version and are re-derived by the
-- reader that wrote them (DeriveVersion), never rewritten here.
--
-- NOT VALID then VALIDATE only satisfies the migration guard: both run in this
-- one transaction, so the table lock taken by the DROP is held throughout and
-- the validation scan is not lighter than a plain ADD would be.
ALTER TABLE evidence DROP CONSTRAINT evidence_projection_version_check;
ALTER TABLE evidence ADD CONSTRAINT evidence_projection_version_check
    CHECK (projection_version IN (0, 1, 2, 3, 4, 5)) NOT VALID;
ALTER TABLE evidence VALIDATE CONSTRAINT evidence_projection_version_check;
