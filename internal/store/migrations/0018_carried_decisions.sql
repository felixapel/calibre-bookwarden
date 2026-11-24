-- Carried rejections (ADR-034, carried decisions). A person's rejection of a
-- fix carries into a later run of the same library that proposes the same fix
-- again: the new run gets a rejected row with decision_mode 'carried', and
-- carried_from points at the proposal it copies. A carried row is never a
-- label.
--
-- The old CHECK came from 0013's ADD COLUMN, so Postgres named it
-- proposals_decision_mode_check. The new constraints are added NOT VALID and
-- then validated, which is what the migration-safety guard asks for. The
-- migration runs in one transaction, so the two steps don't shorten the lock
-- on proposals; the table holds one row per decision, so that is short.
ALTER TABLE proposals
    DROP CONSTRAINT proposals_decision_mode_check,
    ADD CONSTRAINT proposals_decision_mode_check
        CHECK (decision_mode IN ('individual', 'bulk', 'agent', 'carried')) NOT VALID;
ALTER TABLE proposals VALIDATE CONSTRAINT proposals_decision_mode_check;

ALTER TABLE proposals ADD COLUMN carried_from bigint;
ALTER TABLE proposals
    ADD CONSTRAINT proposals_carried_from_fkey
        FOREIGN KEY (carried_from) REFERENCES proposals (id) ON DELETE SET NULL NOT VALID;
ALTER TABLE proposals VALIDATE CONSTRAINT proposals_carried_from_fkey;

-- The Fixes search reads the folded title and authors of each book (a line
-- per name), computed in Go when the run is checked so that the projection
-- and a page walk fold identically. A projection written before this has no
-- such text, so every existing check is marked stale: its pages are walked
-- until the startup sweep checks the run again and fills it in.
ALTER TABLE book_projection ADD COLUMN search_text text;
UPDATE seal_checks SET checked_at = '-infinity';
