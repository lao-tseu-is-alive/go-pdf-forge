-- migrate:up

ALTER TABLE pdf_job
    ADD COLUMN progress_message text NOT NULL DEFAULT '',
    ADD COLUMN deletion_requested_at timestamptz,
    ADD CONSTRAINT pdf_job_owner_user_positive
        CHECK (owner_user_id IS NULL OR owner_user_id > 0),
    ADD CONSTRAINT pdf_job_creator_snapshot_consistency
        CHECK (
            (owner_kind = 'anonymous'
                AND created_by_external_id IS NULL
                AND created_by_login IS NULL
                AND created_by_name IS NULL
                AND created_by_email IS NULL)
            OR
            (owner_kind = 'authenticated'
                AND created_by_external_id IS NOT NULL
                AND created_by_external_id > 0
                AND created_by_login IS NOT NULL
                AND length(btrim(created_by_login)) > 0
                AND octet_length(created_by_login) <= 255
                AND created_by_name IS NOT NULL
                AND length(btrim(created_by_name)) > 0
                AND octet_length(created_by_name) <= 255
                AND (created_by_email IS NULL
                    OR (length(btrim(created_by_email)) > 0
                        AND octet_length(created_by_email) <= 320)))
        ),
    ADD CONSTRAINT pdf_job_input_metadata_valid
        CHECK (
            length(btrim(original_filename)) > 0
            AND octet_length(original_filename) <= 255
            AND length(btrim(input_object_key)) > 0
            AND octet_length(input_object_key) <= 1024
            AND input_sha256 ~ '^[0-9a-f]{64}$'
        ),
    ADD CONSTRAINT pdf_job_output_metadata_valid
        CHECK (
            (output_object_key IS NULL
                OR (length(btrim(output_object_key)) > 0
                    AND octet_length(output_object_key) <= 1024))
            AND (output_sha256 IS NULL OR output_sha256 ~ '^[0-9a-f]{64}$')
        ),
    ADD CONSTRAINT pdf_job_text_metadata_bounded
        CHECK (
            octet_length(progress_message) <= 512
            AND (lease_owner IS NULL OR octet_length(lease_owner) <= 255)
            AND (error_code IS NULL OR octet_length(error_code) <= 128)
            AND (error_message IS NULL OR octet_length(error_message) <= 1024)
        ),
    ADD CONSTRAINT pdf_job_timestamp_order
        CHECK (
            updated_at >= created_at
            AND available_at >= created_at
            AND expires_at > created_at
            AND (started_at IS NULL OR started_at >= created_at)
            AND (completed_at IS NULL OR completed_at >= created_at)
            AND (heartbeat_at IS NULL OR heartbeat_at >= created_at)
            AND (cancel_requested_at IS NULL OR cancel_requested_at >= created_at)
            AND (deletion_requested_at IS NULL OR deletion_requested_at >= created_at)
        );

CREATE INDEX pdf_job_deletion_idx
    ON pdf_job (deletion_requested_at)
    WHERE deletion_requested_at IS NOT NULL AND status <> 'expired';

-- migrate:down

DROP INDEX IF EXISTS pdf_job_deletion_idx;

ALTER TABLE pdf_job
    DROP CONSTRAINT IF EXISTS pdf_job_timestamp_order,
    DROP CONSTRAINT IF EXISTS pdf_job_text_metadata_bounded,
    DROP CONSTRAINT IF EXISTS pdf_job_output_metadata_valid,
    DROP CONSTRAINT IF EXISTS pdf_job_input_metadata_valid,
    DROP CONSTRAINT IF EXISTS pdf_job_creator_snapshot_consistency,
    DROP CONSTRAINT IF EXISTS pdf_job_owner_user_positive,
    DROP COLUMN IF EXISTS deletion_requested_at,
    DROP COLUMN IF EXISTS progress_message;
