-- Where a run's time went (B5): seconds per stage summed over the workers,
-- catalogue lookups by source and outcome, and extractor calls. Each attempt
-- of a run adds to it; run_history.stats (0024) gets a copy when the run
-- ends. Additive only, a constant default, so no table rewrite. No book data.

ALTER TABLE runs ADD COLUMN stats jsonb NOT NULL DEFAULT '{}'::jsonb;
