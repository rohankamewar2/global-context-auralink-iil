-- Receipts bind retries to their authenticated session and immutable evidence.
CREATE TABLE live_updates (
    session_id text NOT NULL,
    scope_id bigint NOT NULL,
    principal_id text NOT NULL,
    idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 128),
    request_sha256 text NOT NULL CHECK (request_sha256 ~ '^[0-9a-f]{64}$'),
    event_id bigint NOT NULL,
    PRIMARY KEY (session_id, idempotency_key),
    FOREIGN KEY (session_id,scope_id,principal_id) REFERENCES context_sessions(id,scope_id,principal_id),
    FOREIGN KEY (event_id,scope_id) REFERENCES event_projections(event_id,scope_id)
);
