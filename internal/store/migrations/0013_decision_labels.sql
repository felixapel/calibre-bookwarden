-- Decision labels (ADR-034): how a person decided a proposal, and whether the
-- agents' verdicts were on screen when they did. Only an individual decision
-- made with the verdicts hidden is a label that can earn automation.
--
-- A decision made before this migration has an unknown mode, so it reads as
-- bulk (never a label) and its verdicts as shown (unknown, not blind). The
-- agent's own decisions are marked as such.
ALTER TABLE proposals
    ADD COLUMN decision_mode text NOT NULL DEFAULT 'bulk'
        CHECK (decision_mode IN ('individual', 'bulk', 'agent')),
    ADD COLUMN verdicts_shown boolean NOT NULL DEFAULT true;
UPDATE proposals SET decision_mode = 'agent' WHERE decided_by = 'agent';

-- Fixes whose verdicts were put on screen before they were decided. The
-- reveal outlives the request, so hiding the verdicts again doesn't make a
-- later decision on these fixes blind.
CREATE TABLE verdict_reveals (
    run_id      bigint NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    book_id     integer NOT NULL,
    field       text NOT NULL,
    revealed_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (run_id, book_id, field)
);
