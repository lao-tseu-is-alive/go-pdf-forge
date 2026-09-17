-- migrate:up

ALTER TABLE anonymous_session
    ADD CONSTRAINT anonymous_session_secret_hash_length
        CHECK (octet_length(secret_hash) = 32),
    ADD CONSTRAINT anonymous_session_initial_ip_hash_length
        CHECK (octet_length(initial_ip_hash) = 32),
    ADD CONSTRAINT anonymous_session_last_ip_hash_length
        CHECK (octet_length(last_ip_hash) = 32);

-- migrate:down

ALTER TABLE anonymous_session
    DROP CONSTRAINT IF EXISTS anonymous_session_last_ip_hash_length,
    DROP CONSTRAINT IF EXISTS anonymous_session_initial_ip_hash_length,
    DROP CONSTRAINT IF EXISTS anonymous_session_secret_hash_length;
