package job

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

var _ QueueStore = (*PostgresStore)(nil)

// Claim atomically leases the oldest available queued job with
// FOR UPDATE SKIP LOCKED so concurrent workers cannot receive the same row.
func (store *PostgresStore) Claim(ctx context.Context, workerID string, observedAt, leaseExpiresAt time.Time) (Job, error) {
	if err := validateClaim(workerID, observedAt, leaseExpiresAt); err != nil {
		return Job{}, err
	}
	claimed, err := scanJob(store.database.QueryRow(ctx, `
WITH candidate AS (
    SELECT id
    FROM pdf_job
    WHERE status = 'queued'
      AND available_at <= $2
      AND expires_at > $2
      AND attempt_count < max_attempts
      AND cancel_requested_at IS NULL
      AND deletion_requested_at IS NULL
    ORDER BY available_at, created_at, id
    FOR UPDATE SKIP LOCKED
    LIMIT 1
), updated AS (
    UPDATE pdf_job AS job
    SET status = 'analyzing',
        attempt_count = attempt_count + 1,
        lease_owner = $1,
        lease_expires_at = $3,
        heartbeat_at = $2,
        started_at = COALESCE(started_at, GREATEST($2, created_at)),
        updated_at = GREATEST(updated_at, $2),
        version = version + 1
    FROM candidate
    WHERE job.id = candidate.id
    RETURNING job.*
)
SELECT `+jobColumns+` FROM updated`, workerID, observedAt, leaseExpiresAt))
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, ErrNoJobAvailable
	}
	if err != nil {
		return Job{}, &storageError{operation: "claim", cause: err}
	}
	return claimed, nil
}

// Heartbeat extends an unexpired active lease only while cancellation and
// deletion remain absent. A cancellation result is returned without extension.
func (store *PostgresStore) Heartbeat(ctx context.Context, jobID, workerID string, observedAt, leaseExpiresAt time.Time) (Job, error) {
	if err := validateHeartbeat(jobID, workerID, observedAt, leaseExpiresAt); err != nil {
		return Job{}, err
	}
	stored, err := scanJob(store.database.QueryRow(ctx, `
UPDATE pdf_job
SET lease_expires_at = CASE
        WHEN cancel_requested_at IS NULL AND deletion_requested_at IS NULL THEN $4
        ELSE lease_expires_at
    END,
    heartbeat_at = CASE
        WHEN cancel_requested_at IS NULL AND deletion_requested_at IS NULL THEN $3
        ELSE heartbeat_at
    END,
    updated_at = CASE
        WHEN cancel_requested_at IS NULL AND deletion_requested_at IS NULL
            THEN GREATEST(updated_at, $3)
        ELSE updated_at
    END,
    version = version + CASE
        WHEN cancel_requested_at IS NULL AND deletion_requested_at IS NULL THEN 1
        ELSE 0
    END
WHERE id = $1
  AND lease_owner = $2
  AND status IN ('analyzing', 'optimizing', 'validating')
  AND lease_expires_at > $3
RETURNING `+jobColumns, jobID, workerID, observedAt, leaseExpiresAt))
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, ErrLeaseLost
	}
	if err != nil {
		return Job{}, &storageError{operation: "heartbeat", cause: err}
	}
	if stored.CancelRequestedAt != nil || stored.DeletionRequestedAt != nil {
		return stored, ErrCancellationRequested
	}
	return stored, nil
}

// Transition applies one monotonic expected-state update only for the current
// owner of an unexpired lease. Terminal transitions release the lease.
func (store *PostgresStore) Transition(ctx context.Context, transition Transition) (Job, error) {
	if err := validateTransition(transition); err != nil {
		return Job{}, err
	}
	terminal := transition.NextStatus.Terminal()
	stored, err := scanJob(store.database.QueryRow(ctx, `
UPDATE pdf_job
SET status = $4,
    progress_percent = $5,
    progress_message = $6,
    error_code = CASE WHEN $4 = 'failed' THEN $7 ELSE error_code END,
    error_message = CASE WHEN $4 = 'failed' THEN $8 ELSE error_message END,
    lease_owner = CASE WHEN $11 THEN NULL ELSE lease_owner END,
    lease_expires_at = CASE WHEN $11 THEN NULL ELSE $10::timestamptz END,
    heartbeat_at = CASE WHEN $11 THEN NULL ELSE $9::timestamptz END,
    completed_at = CASE
        WHEN $11 THEN COALESCE(completed_at, GREATEST($9, created_at))
        ELSE completed_at
    END,
    updated_at = GREATEST(updated_at, $9),
    version = version + 1
WHERE id = $1
  AND lease_owner = $2
  AND status = $3
  AND status IN ('analyzing', 'optimizing', 'validating')
  AND lease_expires_at > $9
  AND progress_percent <= $5
  AND (
      ($4 = 'cancelled' AND (cancel_requested_at IS NOT NULL OR deletion_requested_at IS NOT NULL))
      OR
      ($4 <> 'cancelled' AND cancel_requested_at IS NULL AND deletion_requested_at IS NULL)
  )
RETURNING `+jobColumns,
		transition.JobID, transition.WorkerID, transition.ExpectedStatus, transition.NextStatus,
		transition.ProgressPercent, transition.ProgressMessage,
		nullableString(transition.ErrorCode), nullableString(transition.ErrorMessage),
		transition.ObservedAt, transition.LeaseExpiresAt, terminal,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, ErrLeaseLost
	}
	if err != nil {
		return Job{}, &storageError{operation: "transition", cause: err}
	}
	return stored, nil
}

// RecoverExpired locks a bounded batch with SKIP LOCKED. It cancels requested
// jobs, fails exhausted jobs, and requeues the remainder for a later claim.
func (store *PostgresStore) RecoverExpired(ctx context.Context, request RecoveryRequest) (RecoveryResult, error) {
	if err := validateRecovery(request); err != nil {
		return RecoveryResult{}, err
	}
	var result RecoveryResult
	err := store.database.QueryRow(ctx, `
WITH expired AS (
    SELECT id
    FROM pdf_job
    WHERE status IN ('analyzing', 'optimizing', 'validating')
      AND lease_expires_at <= $1
    ORDER BY lease_expires_at, id
    FOR UPDATE SKIP LOCKED
    LIMIT $3
), recovered AS (
    UPDATE pdf_job AS job
    SET status = CASE
            WHEN cancel_requested_at IS NOT NULL OR deletion_requested_at IS NOT NULL THEN 'cancelled'
            WHEN attempt_count >= max_attempts THEN 'failed'
            ELSE 'queued'
        END,
        progress_percent = CASE
            WHEN cancel_requested_at IS NULL
              AND deletion_requested_at IS NULL
              AND attempt_count < max_attempts THEN 0
            ELSE progress_percent
        END,
        progress_message = CASE
            WHEN cancel_requested_at IS NULL
              AND deletion_requested_at IS NULL
              AND attempt_count < max_attempts THEN ''
            ELSE progress_message
        END,
        available_at = CASE
            WHEN cancel_requested_at IS NULL
              AND deletion_requested_at IS NULL
              AND attempt_count < max_attempts THEN $2
            ELSE available_at
        END,
        lease_owner = NULL,
        lease_expires_at = NULL,
        heartbeat_at = NULL,
        error_code = CASE
            WHEN cancel_requested_at IS NULL
              AND deletion_requested_at IS NULL
              AND attempt_count >= max_attempts THEN 'lease_attempts_exhausted'
            ELSE error_code
        END,
        error_message = CASE
            WHEN cancel_requested_at IS NULL
              AND deletion_requested_at IS NULL
              AND attempt_count >= max_attempts THEN 'Processing stopped after repeated worker interruption.'
            ELSE error_message
        END,
        completed_at = CASE
            WHEN cancel_requested_at IS NOT NULL
              OR deletion_requested_at IS NOT NULL
              OR attempt_count >= max_attempts
                THEN COALESCE(completed_at, GREATEST($1, created_at))
            ELSE completed_at
        END,
        updated_at = GREATEST(updated_at, $1),
        version = version + 1
    FROM expired
    WHERE job.id = expired.id
    RETURNING job.status
)
SELECT
    count(*) FILTER (WHERE status = 'queued'),
    count(*) FILTER (WHERE status = 'cancelled'),
    count(*) FILTER (WHERE status = 'failed')
FROM recovered`, request.ObservedAt, request.RetryAt, request.Limit).Scan(
		&result.Requeued, &result.Cancelled, &result.Failed,
	)
	if err != nil {
		return RecoveryResult{}, &storageError{operation: "recover expired leases", cause: err}
	}
	return result, nil
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
