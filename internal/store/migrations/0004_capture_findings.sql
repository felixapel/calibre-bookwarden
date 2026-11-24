ALTER TABLE evidence DROP CONSTRAINT evidence_projection_version_check;
ALTER TABLE evidence ADD CONSTRAINT evidence_projection_version_check
    CHECK (projection_version IN (0, 1, 2));
