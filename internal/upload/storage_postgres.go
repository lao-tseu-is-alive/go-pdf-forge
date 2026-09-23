package upload

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const uploadSessionColumns = `
    id, owner_kind, owner_user_id, anonymous_session_id,
    original_filename, content_type, declared_size, chunk_size,
    expected_chunks, received_chunks, received_bytes, status,
    object_key, multipart_upload_id, committed_sha256,
    created_at, updated_at, committed_at, expires_at`

type postgresDatabase interface {
	Begin(context.Context) (pgx.Tx, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type storageError struct {
	operation string
	cause     error
}

func (err *storageError) Error() string {
	return fmt.Sprintf("upload storage %s failed", err.operation)
}

func (err *storageError) Unwrap() error {
	return err.cause
}

// PostgresStore persists upload metadata and serializes mutations with a row
// lock on upload_session. It never receives or stores chunk content.
type PostgresStore struct {
	database postgresDatabase
}

var _ Store = (*PostgresStore)(nil)

// NewPostgresStore binds upload persistence to a pgx-compatible database such
// as pgxpool.Pool.
func NewPostgresStore(database postgresDatabase) (*PostgresStore, error) {
	if database == nil {
		return nil, errors.New("upload database is required")
	}
	return &PostgresStore{database: database}, nil
}

// Create inserts immutable upload metadata and returns PostgreSQL's timestamp representation.
func (store *PostgresStore) Create(ctx context.Context, session Session) (Session, error) {
	if err := validateSessionForCreate(session); err != nil {
		return Session{}, err
	}
	userID, anonymousID := ownerDatabaseValues(session.Owner)
	created, err := scanSession(store.database.QueryRow(ctx, `
INSERT INTO upload_session (
    id, owner_kind, owner_user_id, anonymous_session_id,
    original_filename, content_type, declared_size, chunk_size,
    expected_chunks, status, object_key, multipart_upload_id,
    created_at, updated_at, expires_at
) VALUES (
    $1, $2, $3, $4,
    $5, $6, $7, $8,
    $9, $10, $11, NULLIF($12, ''),
    $13, $14, $15
)
RETURNING `+uploadSessionColumns,
		session.ID, session.Owner.Kind, userID, anonymousID,
		session.OriginalFilename, session.ContentType, session.DeclaredSize, int32(session.ChunkSize),
		session.ExpectedChunks, session.Status, session.ObjectKey, session.MultipartUploadID,
		session.CreatedAt, session.UpdatedAt, session.ExpiresAt,
	))
	if err == nil {
		return created, nil
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && postgresError.Code == "23505" {
		return Session{}, ErrConflict
	}
	return Session{}, &storageError{operation: "create", cause: err}
}

// Get returns upload metadata only when the supplied owner matches in SQL.
func (store *PostgresStore) Get(ctx context.Context, uploadID string, owner Owner) (Session, error) {
	if !validUUID(uploadID) || owner.Validate() != nil {
		return Session{}, ErrNotFound
	}
	userID, anonymousID := ownerDatabaseValues(owner)
	session, err := scanSession(store.database.QueryRow(ctx, `
SELECT `+uploadSessionColumns+`
FROM upload_session
WHERE id = $1
  AND owner_kind = $2
  AND owner_user_id IS NOT DISTINCT FROM $3
  AND anonymous_session_id IS NOT DISTINCT FROM $4`,
		uploadID, owner.Kind, userID, anonymousID,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, &storageError{operation: "get", cause: err}
	}
	return session, nil
}

// RecordPart locks the upload, accepts only the next exact-size part, and
// increments progress in the same transaction as the part insert.
func (store *PostgresStore) RecordPart(ctx context.Context, write PartWrite) (PartReceipt, error) {
	if err := validatePartWrite(write); err != nil {
		return PartReceipt{}, err
	}
	tx, err := store.database.Begin(ctx)
	if err != nil {
		return PartReceipt{}, &storageError{operation: "begin part record", cause: err}
	}
	defer rollback(ctx, tx)

	session, err := lockOwnedSession(ctx, tx, write.UploadID, write.Owner)
	if err != nil {
		return PartReceipt{}, err
	}
	if !write.ObservedAt.Before(session.ExpiresAt) {
		return PartReceipt{}, ErrExpired
	}
	if session.Status != StatusUploading {
		return PartReceipt{}, ErrInvalidState
	}
	if write.ObservedAt.Before(session.CreatedAt) {
		return PartReceipt{}, errors.New("upload part observation precedes session creation")
	}

	existing, err := readPart(ctx, tx, write.UploadID, write.Index)
	if err == nil {
		if existing.ByteSize != write.ByteSize || existing.SHA256 != write.SHA256 {
			return PartReceipt{}, ErrPartConflict
		}
		if err := tx.Commit(ctx); err != nil {
			return PartReceipt{}, &storageError{operation: "commit part retry", cause: err}
		}
		return PartReceipt{
			Part: existing, AlreadyPresent: true,
			ReceivedChunks: session.ReceivedChunks, ReceivedBytes: session.ReceivedBytes,
		}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return PartReceipt{}, &storageError{operation: "read existing part", cause: err}
	}
	if write.Index != session.ReceivedChunks || write.Index >= session.ExpectedChunks {
		return PartReceipt{}, ErrPartOutOfOrder
	}
	expectedSize, err := expectedPartSize(session, write.Index)
	if err != nil {
		return PartReceipt{}, &storageError{operation: "validate stored session", cause: err}
	}
	if write.ByteSize != expectedSize {
		return PartReceipt{}, fmt.Errorf("%w: part %d must contain exactly %d bytes", ErrPartSize, write.Index, expectedSize)
	}
	updatedAt := maxTime(write.ObservedAt, session.UpdatedAt)

	if _, err := tx.Exec(ctx, `
INSERT INTO upload_part (
    upload_id, part_index, byte_size, sha256, backend_part_id, created_at
) VALUES ($1, $2, $3, $4, $5, $6)`,
		write.UploadID, write.Index, int32(write.ByteSize), write.SHA256.String(),
		write.BackendPartID, write.ObservedAt,
	); err != nil {
		return PartReceipt{}, &storageError{operation: "insert part", cause: err}
	}
	if _, err := tx.Exec(ctx, `
UPDATE upload_session
SET received_chunks = received_chunks + 1,
    received_bytes = received_bytes + $2,
    updated_at = $3
WHERE id = $1`, write.UploadID, write.ByteSize, updatedAt); err != nil {
		return PartReceipt{}, &storageError{operation: "update progress", cause: err}
	}
	part := Part{
		UploadID: write.UploadID, Index: write.Index, ByteSize: write.ByteSize,
		SHA256: write.SHA256, BackendPartID: write.BackendPartID, CreatedAt: write.ObservedAt,
	}
	if err := tx.Commit(ctx); err != nil {
		return PartReceipt{}, &storageError{operation: "commit part record", cause: err}
	}
	return PartReceipt{
		Part: part, ReceivedChunks: session.ReceivedChunks + 1,
		ReceivedBytes: session.ReceivedBytes + write.ByteSize,
	}, nil
}

// BeginCommit checks every part rather than trusting denormalized counters,
// then freezes the part set before external object finalization starts.
func (store *PostgresStore) BeginCommit(ctx context.Context, uploadID string, owner Owner, observedAt time.Time) (CommitPlan, error) {
	if err := validateOwnedOperation(uploadID, owner, observedAt); err != nil {
		return CommitPlan{}, err
	}
	tx, err := store.database.Begin(ctx)
	if err != nil {
		return CommitPlan{}, &storageError{operation: "begin commit preparation", cause: err}
	}
	defer rollback(ctx, tx)
	session, err := lockOwnedSession(ctx, tx, uploadID, owner)
	if err != nil {
		return CommitPlan{}, err
	}
	if !observedAt.Before(session.ExpiresAt) {
		return CommitPlan{}, ErrExpired
	}
	if session.Status != StatusUploading && session.Status != StatusCommitting {
		return CommitPlan{}, ErrInvalidState
	}
	if observedAt.Before(session.CreatedAt) {
		return CommitPlan{}, errors.New("upload commit observation precedes session creation")
	}
	parts, err := readAllParts(ctx, tx, uploadID)
	if err != nil {
		return CommitPlan{}, err
	}
	if err := validateCompleteParts(session, parts); err != nil {
		return CommitPlan{}, err
	}
	if session.Status == StatusUploading {
		updatedAt := maxTime(observedAt, session.UpdatedAt)
		if _, err := tx.Exec(ctx, `
UPDATE upload_session
SET status = 'committing', updated_at = $2
WHERE id = $1`, uploadID, updatedAt); err != nil {
			return CommitPlan{}, &storageError{operation: "freeze parts", cause: err}
		}
		session.Status = StatusCommitting
		session.UpdatedAt = updatedAt
	}
	if err := tx.Commit(ctx); err != nil {
		return CommitPlan{}, &storageError{operation: "commit preparation", cause: err}
	}
	return CommitPlan{Session: session, Parts: parts}, nil
}

// Complete records complete-object integrity after backend streaming verification.
func (store *PostgresStore) Complete(ctx context.Context, uploadID string, owner Owner, digest SHA256, observedAt time.Time) (Session, error) {
	if err := validateOwnedOperation(uploadID, owner, observedAt); err != nil {
		return Session{}, err
	}
	tx, err := store.database.Begin(ctx)
	if err != nil {
		return Session{}, &storageError{operation: "begin completion", cause: err}
	}
	defer rollback(ctx, tx)
	session, err := lockOwnedSession(ctx, tx, uploadID, owner)
	if err != nil {
		return Session{}, err
	}
	if session.Status == StatusCommitted {
		if session.CommittedSHA256 == nil || *session.CommittedSHA256 != digest {
			return Session{}, ErrConflict
		}
		if err := tx.Commit(ctx); err != nil {
			return Session{}, &storageError{operation: "commit completion retry", cause: err}
		}
		return session, nil
	}
	if !observedAt.Before(session.ExpiresAt) {
		return Session{}, ErrExpired
	}
	if session.Status != StatusCommitting {
		return Session{}, ErrInvalidState
	}
	if observedAt.Before(session.CreatedAt) {
		return Session{}, errors.New("upload completion observation precedes session creation")
	}
	completedAt := maxTime(observedAt, session.UpdatedAt)
	if _, err := tx.Exec(ctx, `
UPDATE upload_session
SET status = 'committed', committed_sha256 = $2,
    committed_at = $3, updated_at = $3
WHERE id = $1`, uploadID, digest.String(), completedAt); err != nil {
		return Session{}, &storageError{operation: "complete", cause: err}
	}
	digestCopy := digest
	committedAt := completedAt
	session.Status = StatusCommitted
	session.CommittedSHA256 = &digestCopy
	session.CommittedAt = &committedAt
	session.UpdatedAt = completedAt
	if err := tx.Commit(ctx); err != nil {
		return Session{}, &storageError{operation: "commit completion", cause: err}
	}
	return session, nil
}

// Abort closes an uploading or committing session and succeeds for repeated
// calls on already aborted or expired sessions.
func (store *PostgresStore) Abort(ctx context.Context, uploadID string, owner Owner, observedAt time.Time) (Session, error) {
	if err := validateOwnedOperation(uploadID, owner, observedAt); err != nil {
		return Session{}, err
	}
	tx, err := store.database.Begin(ctx)
	if err != nil {
		return Session{}, &storageError{operation: "begin abort", cause: err}
	}
	defer rollback(ctx, tx)
	session, err := lockOwnedSession(ctx, tx, uploadID, owner)
	if err != nil {
		return Session{}, err
	}
	switch session.Status {
	case StatusAborted, StatusExpired:
		if err := tx.Commit(ctx); err != nil {
			return Session{}, &storageError{operation: "commit abort retry", cause: err}
		}
		return session, nil
	case StatusUploading, StatusCommitting:
		if observedAt.Before(session.CreatedAt) {
			return Session{}, errors.New("upload abort observation precedes session creation")
		}
		updatedAt := maxTime(observedAt, session.UpdatedAt)
		if _, err := tx.Exec(ctx, `
UPDATE upload_session
SET status = 'aborted', updated_at = $2
WHERE id = $1`, uploadID, updatedAt); err != nil {
			return Session{}, &storageError{operation: "abort", cause: err}
		}
		session.Status = StatusAborted
		session.UpdatedAt = updatedAt
	default:
		return Session{}, ErrInvalidState
	}
	if err := tx.Commit(ctx); err != nil {
		return Session{}, &storageError{operation: "commit abort", cause: err}
	}
	return session, nil
}

func lockOwnedSession(ctx context.Context, tx pgx.Tx, uploadID string, owner Owner) (Session, error) {
	userID, anonymousID := ownerDatabaseValues(owner)
	session, err := scanSession(tx.QueryRow(ctx, `
SELECT `+uploadSessionColumns+`
FROM upload_session
WHERE id = $1
  AND owner_kind = $2
  AND owner_user_id IS NOT DISTINCT FROM $3
  AND anonymous_session_id IS NOT DISTINCT FROM $4
FOR UPDATE`, uploadID, owner.Kind, userID, anonymousID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, &storageError{operation: "lock owned session", cause: err}
	}
	return session, nil
}

func readPart(ctx context.Context, tx pgx.Tx, uploadID string, index int32) (Part, error) {
	var part Part
	var digest string
	err := tx.QueryRow(ctx, `
SELECT upload_id, part_index, byte_size, sha256, backend_part_id, created_at
FROM upload_part
WHERE upload_id = $1 AND part_index = $2`, uploadID, index).Scan(
		&part.UploadID, &part.Index, &part.ByteSize, &digest, &part.BackendPartID, &part.CreatedAt,
	)
	if err != nil {
		return Part{}, err
	}
	part.SHA256, err = ParseSHA256(digest)
	if err != nil {
		return Part{}, err
	}
	return part, nil
}

func readAllParts(ctx context.Context, tx pgx.Tx, uploadID string) ([]Part, error) {
	rows, err := tx.Query(ctx, `
SELECT upload_id, part_index, byte_size, sha256, backend_part_id, created_at
FROM upload_part
WHERE upload_id = $1
ORDER BY part_index`, uploadID)
	if err != nil {
		return nil, &storageError{operation: "list parts", cause: err}
	}
	defer rows.Close()
	var parts []Part
	for rows.Next() {
		var part Part
		var digest string
		if err := rows.Scan(&part.UploadID, &part.Index, &part.ByteSize, &digest, &part.BackendPartID, &part.CreatedAt); err != nil {
			return nil, &storageError{operation: "scan part", cause: err}
		}
		part.SHA256, err = ParseSHA256(digest)
		if err != nil {
			return nil, &storageError{operation: "decode part digest", cause: err}
		}
		parts = append(parts, part)
	}
	if err := rows.Err(); err != nil {
		return nil, &storageError{operation: "iterate parts", cause: err}
	}
	return parts, nil
}

func scanSession(row pgx.Row) (Session, error) {
	var session Session
	var ownerKind string
	var userID *int64
	var anonymousID *string
	var chunkSize int32
	var status string
	var multipartID *string
	var committedDigest *string
	if err := row.Scan(
		&session.ID, &ownerKind, &userID, &anonymousID,
		&session.OriginalFilename, &session.ContentType, &session.DeclaredSize, &chunkSize,
		&session.ExpectedChunks, &session.ReceivedChunks, &session.ReceivedBytes, &status,
		&session.ObjectKey, &multipartID, &committedDigest,
		&session.CreatedAt, &session.UpdatedAt, &session.CommittedAt, &session.ExpiresAt,
	); err != nil {
		return Session{}, err
	}
	session.Owner.Kind = OwnerKind(ownerKind)
	if userID != nil {
		session.Owner.UserID = *userID
	}
	if anonymousID != nil {
		session.Owner.AnonymousSessionID = *anonymousID
	}
	if err := session.Owner.Validate(); err != nil {
		return Session{}, err
	}
	session.ChunkSize = int64(chunkSize)
	session.Status = Status(status)
	if multipartID != nil {
		session.MultipartUploadID = *multipartID
	}
	if committedDigest != nil {
		digest, err := ParseSHA256(*committedDigest)
		if err != nil {
			return Session{}, err
		}
		session.CommittedSHA256 = &digest
	}
	if err := validateStoredSession(session); err != nil {
		return Session{}, err
	}
	return session, nil
}

func validateSessionForCreate(session Session) error {
	if !validUUID(session.ID) {
		return errors.New("upload ID must be a UUID")
	}
	if err := session.Owner.Validate(); err != nil {
		return err
	}
	filename, err := SanitizeFilename(session.OriginalFilename)
	if err != nil || filename != session.OriginalFilename {
		return errors.New("upload filename must already be sanitized")
	}
	contentType, err := normalizeContentType(session.ContentType)
	if err != nil || contentType != session.ContentType {
		return errors.New("upload content type must already be normalized")
	}
	if session.DeclaredSize <= 0 || session.ChunkSize <= 0 || session.ChunkSize > math.MaxInt32 {
		return errors.New("upload sizes are invalid")
	}
	expectedChunks := (session.DeclaredSize-1)/session.ChunkSize + 1
	if expectedChunks > math.MaxInt32 || session.ExpectedChunks != int32(expectedChunks) {
		return errors.New("upload expected chunk count is inconsistent")
	}
	if session.ReceivedChunks != 0 || session.ReceivedBytes != 0 || session.Status != StatusUploading {
		return errors.New("new upload must have empty uploading progress")
	}
	if strings.TrimSpace(session.ObjectKey) == "" || len(session.ObjectKey) > maxObjectKeyBytes {
		return errors.New("upload object key is required")
	}
	if len(session.MultipartUploadID) > maxBackendIDBytes {
		return errors.New("upload multipart ID is too long")
	}
	if session.CommittedSHA256 != nil || session.CommittedAt != nil {
		return errors.New("new upload cannot be committed")
	}
	if session.CreatedAt.IsZero() || session.UpdatedAt.IsZero() || session.ExpiresAt.IsZero() ||
		session.UpdatedAt.Before(session.CreatedAt) || !session.ExpiresAt.After(session.CreatedAt) {
		return errors.New("upload timestamps are invalid")
	}
	return nil
}

func validatePartWrite(write PartWrite) error {
	if !validUUID(write.UploadID) {
		return errors.New("upload ID must be a UUID")
	}
	if err := write.Owner.Validate(); err != nil {
		return err
	}
	if write.Index < 0 {
		return errors.New("upload part index must not be negative")
	}
	if write.ByteSize <= 0 || write.ByteSize > math.MaxInt32 {
		return errors.New("upload part size must be positive and fit int32")
	}
	if strings.TrimSpace(write.BackendPartID) == "" || len(write.BackendPartID) > maxBackendIDBytes {
		return errors.New("upload backend part ID is required")
	}
	if write.ObservedAt.IsZero() {
		return errors.New("upload part observation time is required")
	}
	return nil
}

func validateOwnedOperation(uploadID string, owner Owner, observedAt time.Time) error {
	if !validUUID(uploadID) {
		return ErrNotFound
	}
	if err := owner.Validate(); err != nil {
		return err
	}
	if observedAt.IsZero() {
		return errors.New("upload operation time is required")
	}
	return nil
}

func validateCompleteParts(session Session, parts []Part) error {
	if session.ReceivedChunks != session.ExpectedChunks || session.ReceivedBytes != session.DeclaredSize || len(parts) != int(session.ExpectedChunks) {
		return ErrIncomplete
	}
	var total int64
	for index, part := range parts {
		if part.UploadID != session.ID || part.Index != int32(index) || strings.TrimSpace(part.BackendPartID) == "" {
			return ErrIncomplete
		}
		expectedSize, err := expectedPartSize(session, part.Index)
		if err != nil || part.ByteSize != expectedSize {
			return ErrIncomplete
		}
		if total > session.DeclaredSize-part.ByteSize {
			return ErrIncomplete
		}
		total += part.ByteSize
	}
	if total != session.DeclaredSize {
		return ErrIncomplete
	}
	return nil
}

func validateStoredSession(session Session) error {
	if !validUUID(session.ID) {
		return errors.New("stored upload ID is invalid")
	}
	if err := session.Owner.Validate(); err != nil {
		return err
	}
	filename, err := SanitizeFilename(session.OriginalFilename)
	if err != nil || filename != session.OriginalFilename {
		return errors.New("stored upload filename is invalid")
	}
	contentType, err := normalizeContentType(session.ContentType)
	if err != nil || contentType != session.ContentType {
		return errors.New("stored upload content type is invalid")
	}
	if session.DeclaredSize <= 0 || session.ChunkSize <= 0 || session.ChunkSize > math.MaxInt32 {
		return errors.New("stored upload sizes are invalid")
	}
	expectedChunks := (session.DeclaredSize-1)/session.ChunkSize + 1
	if expectedChunks > math.MaxInt32 || session.ExpectedChunks != int32(expectedChunks) ||
		session.ReceivedChunks < 0 || session.ReceivedChunks > session.ExpectedChunks ||
		session.ReceivedBytes < 0 || session.ReceivedBytes > session.DeclaredSize {
		return errors.New("stored upload progress is invalid")
	}
	switch session.Status {
	case StatusUploading, StatusCommitting, StatusCommitted, StatusAborted, StatusExpired:
	default:
		return errors.New("stored upload status is invalid")
	}
	if strings.TrimSpace(session.ObjectKey) == "" || len(session.ObjectKey) > maxObjectKeyBytes || len(session.MultipartUploadID) > maxBackendIDBytes {
		return errors.New("stored upload backend metadata is invalid")
	}
	if session.CreatedAt.IsZero() || session.UpdatedAt.IsZero() || session.ExpiresAt.IsZero() ||
		session.UpdatedAt.Before(session.CreatedAt) || !session.ExpiresAt.After(session.CreatedAt) {
		return errors.New("stored upload timestamps are invalid")
	}
	if session.Status == StatusCommitted {
		if session.CommittedSHA256 == nil || session.CommittedAt == nil {
			return errors.New("stored committed upload lacks integrity metadata")
		}
	} else if session.CommittedSHA256 != nil || session.CommittedAt != nil {
		return errors.New("stored incomplete upload has committed metadata")
	}
	return nil
}

func expectedPartSize(session Session, index int32) (int64, error) {
	if index < 0 || index >= session.ExpectedChunks || session.ChunkSize <= 0 || session.DeclaredSize <= 0 {
		return 0, errors.New("invalid upload part geometry")
	}
	if index < session.ExpectedChunks-1 {
		return session.ChunkSize, nil
	}
	consumed := int64(session.ExpectedChunks-1) * session.ChunkSize
	if consumed < 0 || consumed >= session.DeclaredSize {
		return 0, errors.New("invalid upload final-part geometry")
	}
	return session.DeclaredSize - consumed, nil
}

func ownerDatabaseValues(owner Owner) (any, any) {
	if owner.Kind == OwnerAuthenticated {
		return owner.UserID, nil
	}
	return nil, owner.AnonymousSessionID
}

func maxTime(left, right time.Time) time.Time {
	if left.Before(right) {
		return right
	}
	return left
}

func rollback(ctx context.Context, tx pgx.Tx) {
	rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = tx.Rollback(rollbackCtx)
}
