package job

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const jobColumns = `
    id, upload_id, owner_kind, owner_user_id, anonymous_session_id,
    created_by_external_id, created_by_login, created_by_name, created_by_email,
    original_filename, input_object_key, input_sha256, input_bytes,
    output_object_key, output_sha256, output_bytes, page_count,
    selected_profile, target_bytes, target_met, status,
    progress_percent, progress_message, attempt_count, max_attempts,
    available_at, lease_owner, lease_expires_at, heartbeat_at,
    cancel_requested_at, error_code, error_message,
    created_at, updated_at, started_at, completed_at, expires_at,
    deletion_requested_at, version`

type postgresDatabase interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

type storageError struct {
	operation string
	cause     error
}

func (err *storageError) Error() string {
	return fmt.Sprintf("job storage %s failed", err.operation)
}

func (err *storageError) Unwrap() error {
	return err.cause
}

// PostgresStore persists owner-scoped jobs in PostgreSQL. Job creation copies
// verified input metadata from the committed upload in the insert query.
type PostgresStore struct {
	database postgresDatabase
}

var _ Store = (*PostgresStore)(nil)

// NewPostgresStore binds job persistence to a pgx-compatible database such as
// pgxpool.Pool.
func NewPostgresStore(database postgresDatabase) (*PostgresStore, error) {
	if database == nil {
		return nil, errors.New("job database is required")
	}
	return &PostgresStore{database: database}, nil
}

// Create inserts a queued job only from a committed owner-matching upload.
// Repeating creation for the same upload returns the original immutable job.
func (store *PostgresStore) Create(ctx context.Context, seed Job) (Job, error) {
	if err := validateJobSeed(seed); err != nil {
		return Job{}, err
	}
	userID, anonymousID := ownerDatabaseValues(seed.Owner)
	externalID, login, name, email := creatorDatabaseValues(seed.Owner, seed.Creator)
	created, err := scanJob(store.database.QueryRow(ctx, `
WITH source AS (
    SELECT original_filename, object_key, committed_sha256, declared_size
    FROM upload_session
    WHERE id = $2
      AND owner_kind = $3
      AND owner_user_id IS NOT DISTINCT FROM $4
      AND anonymous_session_id IS NOT DISTINCT FROM $5
      AND status = 'committed'
      AND committed_sha256 IS NOT NULL
      AND committed_at IS NOT NULL
      AND received_bytes = declared_size
), inserted AS (
    INSERT INTO pdf_job (
        id, upload_id, owner_kind, owner_user_id, anonymous_session_id,
        created_by_external_id, created_by_login, created_by_name, created_by_email,
        original_filename, input_object_key, input_sha256, input_bytes,
        target_bytes, status, progress_message, max_attempts,
        available_at, created_at, updated_at, expires_at, version
    )
    SELECT
        $1, $2, $3, $4, $5,
        $6, $7, $8, $9,
        source.original_filename, source.object_key, source.committed_sha256, source.declared_size,
        $10, 'queued', '', $11,
        $12, $13, $14, $15, 1
    FROM source
    ON CONFLICT (upload_id) DO UPDATE
    SET upload_id = EXCLUDED.upload_id
    WHERE pdf_job.owner_kind = EXCLUDED.owner_kind
      AND pdf_job.owner_user_id IS NOT DISTINCT FROM EXCLUDED.owner_user_id
      AND pdf_job.anonymous_session_id IS NOT DISTINCT FROM EXCLUDED.anonymous_session_id
    RETURNING `+jobColumns+`
)
SELECT `+jobColumns+` FROM inserted
LIMIT 1`,
		seed.ID, seed.UploadID, seed.Owner.Kind, userID, anonymousID,
		externalID, login, name, email,
		seed.TargetBytes, seed.MaxAttempts, seed.AvailableAt,
		seed.CreatedAt, seed.UpdatedAt, seed.ExpiresAt,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && postgresError.Code == "23505" {
		return Job{}, ErrConflict
	}
	if err != nil {
		return Job{}, &storageError{operation: "create", cause: err}
	}
	if created.DeletionRequestedAt != nil {
		return Job{}, ErrConflict
	}
	return created, nil
}

// Get returns a non-deleted job only when all owner columns match in SQL.
func (store *PostgresStore) Get(ctx context.Context, jobID string, owner Owner) (Job, error) {
	if !validUUID(jobID) || owner.Validate() != nil {
		return Job{}, ErrNotFound
	}
	userID, anonymousID := ownerDatabaseValues(owner)
	stored, err := scanJob(store.database.QueryRow(ctx, `
SELECT `+jobColumns+`
FROM pdf_job
WHERE id = $1
  AND owner_kind = $2
  AND owner_user_id IS NOT DISTINCT FROM $3
  AND anonymous_session_id IS NOT DISTINCT FROM $4
  AND deletion_requested_at IS NULL`,
		jobID, owner.Kind, userID, anonymousID,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	if err != nil {
		return Job{}, &storageError{operation: "get", cause: err}
	}
	return stored, nil
}

// Cancel atomically records cooperative cancellation for running work and
// moves unclaimed queued work directly to cancelled. Repeated calls are safe.
func (store *PostgresStore) Cancel(ctx context.Context, jobID string, owner Owner, observedAt time.Time) (Job, error) {
	if !validUUID(jobID) || owner.Validate() != nil {
		return Job{}, ErrNotFound
	}
	if observedAt.IsZero() {
		return Job{}, errors.New("job cancellation time is required")
	}
	userID, anonymousID := ownerDatabaseValues(owner)
	stored, err := scanJob(store.database.QueryRow(ctx, `
UPDATE pdf_job
SET status = CASE WHEN status = 'queued' THEN 'cancelled' ELSE status END,
    cancel_requested_at = CASE
        WHEN status IN ('queued', 'analyzing', 'optimizing', 'validating')
            THEN COALESCE(cancel_requested_at, GREATEST($5, created_at))
        ELSE cancel_requested_at
    END,
    completed_at = CASE
        WHEN status = 'queued' THEN COALESCE(completed_at, GREATEST($5, created_at))
        ELSE completed_at
    END,
    updated_at = CASE
        WHEN status = 'queued'
          OR (status IN ('analyzing', 'optimizing', 'validating') AND cancel_requested_at IS NULL)
            THEN GREATEST(updated_at, $5)
        ELSE updated_at
    END,
    version = version + CASE
        WHEN status = 'queued'
          OR (status IN ('analyzing', 'optimizing', 'validating') AND cancel_requested_at IS NULL)
            THEN 1
        ELSE 0
    END
WHERE id = $1
  AND owner_kind = $2
  AND owner_user_id IS NOT DISTINCT FROM $3
  AND anonymous_session_id IS NOT DISTINCT FROM $4
  AND deletion_requested_at IS NULL
RETURNING `+jobColumns,
		jobID, owner.Kind, userID, anonymousID, observedAt,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	if err != nil {
		return Job{}, &storageError{operation: "cancel", cause: err}
	}
	return stored, nil
}

// Delete records a durable tombstone instead of removing object keys before
// the purge owns blob deletion. Missing and repeated owner-scoped deletes succeed.
func (store *PostgresStore) Delete(ctx context.Context, jobID string, owner Owner, observedAt time.Time) error {
	if !validUUID(jobID) {
		return nil
	}
	if err := owner.Validate(); err != nil {
		return ErrNotFound
	}
	if observedAt.IsZero() {
		return errors.New("job deletion time is required")
	}
	userID, anonymousID := ownerDatabaseValues(owner)
	var storedID string
	err := store.database.QueryRow(ctx, `
UPDATE pdf_job
SET status = CASE WHEN status = 'queued' THEN 'cancelled' ELSE status END,
    cancel_requested_at = CASE
        WHEN status IN ('queued', 'analyzing', 'optimizing', 'validating')
            THEN COALESCE(cancel_requested_at, GREATEST($5, created_at))
        ELSE cancel_requested_at
    END,
    completed_at = CASE
        WHEN status = 'queued' THEN COALESCE(completed_at, GREATEST($5, created_at))
        ELSE completed_at
    END,
    deletion_requested_at = COALESCE(deletion_requested_at, GREATEST($5, created_at)),
    expires_at = LEAST(expires_at, GREATEST($5, created_at + INTERVAL '1 microsecond')),
    updated_at = CASE
        WHEN deletion_requested_at IS NULL OR status = 'queued'
          OR (status IN ('analyzing', 'optimizing', 'validating') AND cancel_requested_at IS NULL)
            THEN GREATEST(updated_at, $5)
        ELSE updated_at
    END,
    version = version + CASE
        WHEN deletion_requested_at IS NULL OR status = 'queued'
          OR (status IN ('analyzing', 'optimizing', 'validating') AND cancel_requested_at IS NULL)
            THEN 1
        ELSE 0
    END
WHERE id = $1
  AND owner_kind = $2
  AND owner_user_id IS NOT DISTINCT FROM $3
  AND anonymous_session_id IS NOT DISTINCT FROM $4
RETURNING id`, jobID, owner.Kind, userID, anonymousID, observedAt).Scan(&storedID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return &storageError{operation: "delete", cause: err}
	}
	return nil
}

func scanJob(row pgx.Row) (Job, error) {
	var stored Job
	var ownerKind string
	var ownerUserID *int64
	var anonymousSessionID *string
	var externalID *int64
	var login, name, email *string
	var inputDigest string
	var outputDigest *string
	var outputObjectKey, selectedProfile *string
	var leaseOwner, errorCode, errorMessage *string
	var status string
	if err := row.Scan(
		&stored.ID, &stored.UploadID, &ownerKind, &ownerUserID, &anonymousSessionID,
		&externalID, &login, &name, &email,
		&stored.OriginalFilename, &stored.InputObjectKey, &inputDigest, &stored.InputBytes,
		&outputObjectKey, &outputDigest, &stored.OutputBytes, &stored.PageCount,
		&selectedProfile, &stored.TargetBytes, &stored.TargetMet, &status,
		&stored.ProgressPercent, &stored.ProgressMessage, &stored.AttemptCount, &stored.MaxAttempts,
		&stored.AvailableAt, &leaseOwner, &stored.LeaseExpiresAt, &stored.HeartbeatAt,
		&stored.CancelRequestedAt, &errorCode, &errorMessage,
		&stored.CreatedAt, &stored.UpdatedAt, &stored.StartedAt, &stored.CompletedAt, &stored.ExpiresAt,
		&stored.DeletionRequestedAt, &stored.Version,
	); err != nil {
		return Job{}, err
	}
	stored.Owner.Kind = OwnerKind(ownerKind)
	if ownerUserID != nil {
		stored.Owner.UserID = *ownerUserID
	}
	if anonymousSessionID != nil {
		stored.Owner.AnonymousSessionID = *anonymousSessionID
	}
	if externalID != nil {
		stored.Creator.ExternalID = *externalID
	}
	if login != nil {
		stored.Creator.Login = *login
	}
	if name != nil {
		stored.Creator.Name = *name
	}
	if email != nil {
		stored.Creator.Email = *email
	}
	stored.Status = Status(status)
	inputSHA256, err := ParseSHA256(inputDigest)
	if err != nil {
		return Job{}, err
	}
	stored.InputSHA256 = inputSHA256
	if outputDigest != nil {
		digest, err := ParseSHA256(*outputDigest)
		if err != nil {
			return Job{}, err
		}
		stored.OutputSHA256 = &digest
	}
	if outputObjectKey != nil {
		stored.OutputObjectKey = *outputObjectKey
	}
	if selectedProfile != nil {
		stored.SelectedProfile = *selectedProfile
	}
	if leaseOwner != nil {
		stored.LeaseOwner = *leaseOwner
	}
	if errorCode != nil {
		stored.ErrorCode = *errorCode
	}
	if errorMessage != nil {
		stored.ErrorMessage = *errorMessage
	}
	if err := validateStoredJob(stored); err != nil {
		return Job{}, err
	}
	return stored, nil
}

func ownerDatabaseValues(owner Owner) (any, any) {
	if owner.Kind == OwnerAuthenticated {
		return owner.UserID, nil
	}
	return nil, owner.AnonymousSessionID
}

func creatorDatabaseValues(owner Owner, creator Creator) (any, any, any, any) {
	if owner.Kind == OwnerAnonymous {
		return nil, nil, nil, nil
	}
	var email any
	if creator.Email != "" {
		email = creator.Email
	}
	return creator.ExternalID, creator.Login, creator.Name, email
}
