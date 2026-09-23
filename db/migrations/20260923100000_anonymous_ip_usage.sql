-- migrate:up

ALTER TABLE anonymous_usage
    ADD CONSTRAINT anonymous_usage_ip_hash_length
        CHECK (octet_length(ip_hash) = 32);

CREATE INDEX anonymous_usage_session_window_idx
    ON anonymous_usage (session_id, window_started_at);

CREATE TABLE anonymous_ip_usage (
    ip_hash bytea NOT NULL CHECK (octet_length(ip_hash) = 32),
    window_started_at timestamptz NOT NULL,
    sessions_created bigint NOT NULL DEFAULT 0 CHECK (sessions_created >= 0),
    uploads_started bigint NOT NULL DEFAULT 0 CHECK (uploads_started >= 0),
    jobs_created bigint NOT NULL DEFAULT 0 CHECK (jobs_created >= 0),
    bytes_committed bigint NOT NULL DEFAULT 0 CHECK (bytes_committed >= 0),
    PRIMARY KEY (ip_hash, window_started_at)
);

-- migrate:down

DROP TABLE IF EXISTS anonymous_ip_usage;
DROP INDEX IF EXISTS anonymous_usage_session_window_idx;
ALTER TABLE anonymous_usage
    DROP CONSTRAINT IF EXISTS anonymous_usage_ip_hash_length;
