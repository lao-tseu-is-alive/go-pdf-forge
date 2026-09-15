-- migrate:up

CREATE TABLE anonymous_session (
    id uuid PRIMARY KEY,
    secret_hash bytea NOT NULL UNIQUE,
    initial_ip_hash bytea NOT NULL,
    last_ip_hash bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    CHECK (expires_at > created_at)
);

CREATE INDEX anonymous_session_expiry_idx
    ON anonymous_session (expires_at)
    WHERE revoked_at IS NULL;

CREATE TABLE upload_session (
    id uuid PRIMARY KEY,
    owner_kind text NOT NULL CHECK (owner_kind IN ('authenticated', 'anonymous')),
    owner_user_id bigint,
    anonymous_session_id uuid REFERENCES anonymous_session (id),
    original_filename text NOT NULL,
    content_type text NOT NULL,
    declared_size bigint NOT NULL CHECK (declared_size > 0),
    chunk_size integer NOT NULL CHECK (chunk_size > 0),
    expected_chunks integer NOT NULL CHECK (expected_chunks > 0),
    received_chunks integer NOT NULL DEFAULT 0 CHECK (received_chunks >= 0),
    received_bytes bigint NOT NULL DEFAULT 0 CHECK (received_bytes >= 0),
    status text NOT NULL DEFAULT 'uploading'
        CHECK (status IN ('uploading', 'committing', 'committed', 'aborted', 'expired')),
    object_key text NOT NULL UNIQUE,
    multipart_upload_id text,
    committed_sha256 char(64),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    committed_at timestamptz,
    expires_at timestamptz NOT NULL,
    CHECK (received_chunks <= expected_chunks),
    CHECK (received_bytes <= declared_size),
    CHECK (
        (owner_kind = 'authenticated' AND owner_user_id IS NOT NULL AND anonymous_session_id IS NULL)
        OR
        (owner_kind = 'anonymous' AND owner_user_id IS NULL AND anonymous_session_id IS NOT NULL)
    )
);

CREATE INDEX upload_session_owner_user_idx
    ON upload_session (owner_user_id, created_at DESC)
    WHERE owner_user_id IS NOT NULL;
CREATE INDEX upload_session_anonymous_idx
    ON upload_session (anonymous_session_id, created_at DESC)
    WHERE anonymous_session_id IS NOT NULL;
CREATE INDEX upload_session_expiry_idx
    ON upload_session (expires_at)
    WHERE status IN ('uploading', 'committing');

CREATE TABLE upload_part (
    upload_id uuid NOT NULL REFERENCES upload_session (id) ON DELETE CASCADE,
    part_index integer NOT NULL CHECK (part_index >= 0),
    byte_size integer NOT NULL CHECK (byte_size > 0),
    sha256 char(64) NOT NULL,
    backend_part_id text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (upload_id, part_index)
);

CREATE TABLE pdf_job (
    id uuid PRIMARY KEY,
    upload_id uuid NOT NULL UNIQUE REFERENCES upload_session (id),
    owner_kind text NOT NULL CHECK (owner_kind IN ('authenticated', 'anonymous')),
    owner_user_id bigint,
    anonymous_session_id uuid REFERENCES anonymous_session (id),
    created_by_external_id bigint,
    created_by_login text,
    created_by_name text,
    created_by_email text,
    original_filename text NOT NULL,
    input_object_key text NOT NULL UNIQUE,
    input_sha256 char(64) NOT NULL,
    input_bytes bigint NOT NULL CHECK (input_bytes > 0),
    output_object_key text UNIQUE,
    output_sha256 char(64),
    output_bytes bigint CHECK (output_bytes > 0),
    page_count integer CHECK (page_count > 0),
    selected_profile text CHECK (selected_profile IN ('original', 'ebook', 'screen')),
    target_bytes bigint NOT NULL CHECK (target_bytes > 0),
    target_met boolean,
    status text NOT NULL DEFAULT 'queued'
        CHECK (status IN ('queued', 'analyzing', 'optimizing', 'validating', 'completed', 'failed', 'cancelled', 'expired')),
    progress_percent smallint NOT NULL DEFAULT 0 CHECK (progress_percent BETWEEN 0 AND 100),
    attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    max_attempts integer NOT NULL DEFAULT 3 CHECK (max_attempts > 0),
    available_at timestamptz NOT NULL DEFAULT now(),
    lease_owner text,
    lease_expires_at timestamptz,
    heartbeat_at timestamptz,
    cancel_requested_at timestamptz,
    error_code text,
    error_message text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    started_at timestamptz,
    completed_at timestamptz,
    expires_at timestamptz NOT NULL,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    CHECK (
        (owner_kind = 'authenticated' AND owner_user_id IS NOT NULL AND anonymous_session_id IS NULL)
        OR
        (owner_kind = 'anonymous' AND owner_user_id IS NULL AND anonymous_session_id IS NOT NULL)
    ),
    CHECK (
        owner_kind = 'authenticated'
        OR (created_by_external_id IS NULL AND created_by_login IS NULL
            AND created_by_name IS NULL AND created_by_email IS NULL)
    )
);

CREATE INDEX pdf_job_claim_idx
    ON pdf_job (available_at, created_at)
    WHERE status = 'queued';
CREATE INDEX pdf_job_lease_idx
    ON pdf_job (lease_expires_at)
    WHERE status IN ('analyzing', 'optimizing', 'validating');
CREATE INDEX pdf_job_owner_user_idx
    ON pdf_job (owner_user_id, created_at DESC)
    WHERE owner_user_id IS NOT NULL;
CREATE INDEX pdf_job_anonymous_idx
    ON pdf_job (anonymous_session_id, created_at DESC)
    WHERE anonymous_session_id IS NOT NULL;
CREATE INDEX pdf_job_expiry_idx
    ON pdf_job (expires_at)
    WHERE status <> 'expired';

CREATE TABLE anonymous_usage (
    session_id uuid NOT NULL REFERENCES anonymous_session (id) ON DELETE CASCADE,
    ip_hash bytea NOT NULL,
    window_started_at timestamptz NOT NULL,
    uploads_started integer NOT NULL DEFAULT 0 CHECK (uploads_started >= 0),
    jobs_created integer NOT NULL DEFAULT 0 CHECK (jobs_created >= 0),
    bytes_committed bigint NOT NULL DEFAULT 0 CHECK (bytes_committed >= 0),
    PRIMARY KEY (session_id, ip_hash, window_started_at)
);

CREATE INDEX anonymous_usage_ip_window_idx
    ON anonymous_usage (ip_hash, window_started_at DESC);

CREATE TABLE outbox_event (
    id uuid PRIMARY KEY,
    aggregate_type text NOT NULL,
    aggregate_id uuid NOT NULL,
    event_type text NOT NULL,
    payload jsonb NOT NULL,
    status text NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'processing', 'delivered', 'failed')),
    attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    available_at timestamptz NOT NULL DEFAULT now(),
    lease_owner text,
    lease_expires_at timestamptz,
    last_error text,
    created_at timestamptz NOT NULL DEFAULT now(),
    delivered_at timestamptz
);

CREATE INDEX outbox_event_dispatch_idx
    ON outbox_event (available_at, created_at)
    WHERE status IN ('pending', 'failed');

-- migrate:down

DROP TABLE IF EXISTS outbox_event;
DROP TABLE IF EXISTS anonymous_usage;
DROP TABLE IF EXISTS pdf_job;
DROP TABLE IF EXISTS upload_part;
DROP TABLE IF EXISTS upload_session;
DROP TABLE IF EXISTS anonymous_session;
