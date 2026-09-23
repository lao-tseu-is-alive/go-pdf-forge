-- migrate:up

ALTER TABLE upload_session
    ADD CONSTRAINT upload_session_filename_not_empty
        CHECK (length(btrim(original_filename)) > 0 AND octet_length(original_filename) <= 255),
    ADD CONSTRAINT upload_session_content_type_not_empty
        CHECK (length(btrim(content_type)) > 0 AND octet_length(content_type) <= 255),
    ADD CONSTRAINT upload_session_object_key_not_empty
        CHECK (length(btrim(object_key)) > 0 AND octet_length(object_key) <= 1024),
    ADD CONSTRAINT upload_session_multipart_id_length
        CHECK (multipart_upload_id IS NULL OR octet_length(multipart_upload_id) BETWEEN 1 AND 2048),
    ADD CONSTRAINT upload_session_committed_sha256_format
        CHECK (committed_sha256 IS NULL OR committed_sha256 ~ '^[0-9a-f]{64}$'),
    ADD CONSTRAINT upload_session_commit_consistency
        CHECK (
            (status = 'committed' AND committed_sha256 IS NOT NULL AND committed_at IS NOT NULL)
            OR
            (status <> 'committed' AND committed_sha256 IS NULL AND committed_at IS NULL)
        ),
    ADD CONSTRAINT upload_session_timestamp_order
        CHECK (updated_at >= created_at AND expires_at > created_at);

ALTER TABLE upload_part
    ADD CONSTRAINT upload_part_sha256_format
        CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    ADD CONSTRAINT upload_part_backend_id_not_empty
        CHECK (length(btrim(backend_part_id)) > 0 AND octet_length(backend_part_id) <= 2048);

-- migrate:down

ALTER TABLE upload_part
    DROP CONSTRAINT IF EXISTS upload_part_backend_id_not_empty,
    DROP CONSTRAINT IF EXISTS upload_part_sha256_format;

ALTER TABLE upload_session
    DROP CONSTRAINT IF EXISTS upload_session_timestamp_order,
    DROP CONSTRAINT IF EXISTS upload_session_commit_consistency,
    DROP CONSTRAINT IF EXISTS upload_session_committed_sha256_format,
    DROP CONSTRAINT IF EXISTS upload_session_object_key_not_empty,
    DROP CONSTRAINT IF EXISTS upload_session_multipart_id_length,
    DROP CONSTRAINT IF EXISTS upload_session_content_type_not_empty,
    DROP CONSTRAINT IF EXISTS upload_session_filename_not_empty;
