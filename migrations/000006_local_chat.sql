-- Chat turns are a private journal, not automatically accepted memory facts.
CREATE TABLE chat_turns (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    session_id text NOT NULL,
    scope_id bigint NOT NULL,
    principal_id text NOT NULL,
    message_id text NOT NULL CHECK(length(message_id) BETWEEN 1 AND 128),
    request_sha256 text NOT NULL CHECK(request_sha256 ~ '^[0-9a-f]{64}$'),
    message text NOT NULL CHECK(length(message)>0),
    response jsonb NOT NULL CHECK(jsonb_typeof(response)='object'),
    memory_version bigint NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE(session_id,message_id),
    FOREIGN KEY(session_id,scope_id,principal_id) REFERENCES context_sessions(id,scope_id,principal_id),
    FOREIGN KEY(scope_id,memory_version) REFERENCES memory_snapshots(scope_id,memory_version)
);
CREATE INDEX chat_turns_session_idx ON chat_turns(session_id,id DESC);
