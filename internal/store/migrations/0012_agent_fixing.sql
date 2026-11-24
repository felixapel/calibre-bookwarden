-- Agent fixing (ADR-029): a panel of three LLM judges reviews a run's open
-- proposals. The mode is a setting like the writes switch.
ALTER TABLE settings DROP CONSTRAINT settings_name_check;
ALTER TABLE settings ADD CONSTRAINT settings_name_check
    CHECK (name IN ('writes_enabled', 'agent_fixing'));

-- One review judges one run. A review goes with its run when the run is
-- pruned; runs with decided proposals are never pruned.
CREATE TABLE reviews (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    run_id       bigint NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    kind         text NOT NULL CHECK (kind IN ('auto', 'manual', 'calibration')),
    mode         text NOT NULL CHECK (mode IN ('shadow', 'on')),
    state        text NOT NULL DEFAULT 'queued' CHECK (state IN ('queued', 'running', 'done', 'stopped')),
    stop_reason  text NOT NULL DEFAULT '',
    requested_by text NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    started_at   timestamptz,
    finished_at  timestamptz
);
CREATE UNIQUE INDEX reviews_auto_once ON reviews (run_id) WHERE kind = 'auto';
CREATE INDEX reviews_open ON reviews (id) WHERE state IN ('queued', 'running');

-- One judge's verdict on one proposal, or why it gave none. Insert-once.
CREATE TABLE verdicts (
    review_id      bigint NOT NULL REFERENCES reviews (id) ON DELETE CASCADE,
    run_id         bigint NOT NULL,
    book_id        integer NOT NULL,
    field          text NOT NULL,
    seat           text NOT NULL,
    prompt_version text NOT NULL,
    proposal_hash  text NOT NULL,
    packet_hash    text NOT NULL,
    model          text NOT NULL,
    answer         text NOT NULL CHECK (answer IN ('approve', 'reject', 'unsure', '')),
    reason         text NOT NULL DEFAULT '',
    error_code     text NOT NULL DEFAULT '',
    input_tokens   bigint NOT NULL DEFAULT 0,
    output_tokens  bigint NOT NULL DEFAULT 0,
    millis         bigint NOT NULL DEFAULT 0,
    created_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (review_id, run_id, book_id, field, seat, prompt_version),
    CHECK ((answer = '') <> (error_code = ''))
);
CREATE INDEX verdicts_run ON verdicts (run_id, prompt_version);

-- What a review did with each proposal it judged. Insert-once.
CREATE TABLE review_outcomes (
    review_id  bigint NOT NULL REFERENCES reviews (id) ON DELETE CASCADE,
    run_id     bigint NOT NULL,
    book_id    integer NOT NULL,
    field      text NOT NULL,
    outcome    text NOT NULL CHECK (outcome IN ('approved', 'rejected', 'left')),
    reason     text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (review_id, run_id, book_id, field)
);
CREATE INDEX review_outcomes_run ON review_outcomes (run_id);

CREATE TRIGGER verdicts_append_only BEFORE UPDATE ON verdicts
    FOR EACH ROW EXECUTE FUNCTION reject_change();
CREATE TRIGGER review_outcomes_append_only BEFORE UPDATE ON review_outcomes
    FOR EACH ROW EXECUTE FUNCTION reject_change();
