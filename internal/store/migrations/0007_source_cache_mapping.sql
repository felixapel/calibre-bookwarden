-- The field-mapping rules a cached record was built with (ADR-020). Rows from
-- other rules are misses, so a parser change never replays old records.
ALTER TABLE source_cache ADD COLUMN mapping_version integer NOT NULL DEFAULT 1;
