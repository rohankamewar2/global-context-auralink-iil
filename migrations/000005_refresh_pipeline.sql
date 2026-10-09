-- One durable local refresh task per accepted live event.
-- Existing accepted updates are backfilled on upgrade.
CREATE TABLE refresh_jobs (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    event_id bigint NOT NULL,
    scope_id bigint NOT NULL,
    state text NOT NULL DEFAULT 'queued' CHECK (state IN ('queued','running','done','dead')),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts>=0),
    next_attempt_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    lease_token text,
    lease_until timestamptz,
    error_code text,
    memory_version bigint,
    published_at timestamptz,
    file_ready_at timestamptz,
    freshness_ms double precision,
    UNIQUE(event_id,scope_id),
    FOREIGN KEY(event_id,scope_id) REFERENCES event_projections(event_id,scope_id),
    FOREIGN KEY(scope_id,memory_version) REFERENCES memory_snapshots(scope_id,memory_version)
);
INSERT INTO refresh_jobs(event_id,scope_id) SELECT event_id,scope_id FROM live_updates;

CREATE INDEX refresh_jobs_due_idx ON refresh_jobs(next_attempt_at) WHERE state IN ('queued','running');
