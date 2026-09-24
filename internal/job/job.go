// Package job contains the PDF job lifecycle independent from transport and
// persistence implementations.
package job

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	maxIdentityTextBytes = 255
	maxEmailBytes        = 320
	maxFilenameBytes     = 255
	maxObjectKeyBytes    = 1024
	maxProgressBytes     = 512
	maxErrorCodeBytes    = 128
	maxErrorMessageBytes = 1024
)

var (
	// ErrNotFound hides whether a job does not exist, was deleted, or belongs to another owner.
	ErrNotFound = errors.New("job not found")
	// ErrConflict reports a collision with immutable job or upload identity.
	ErrConflict = errors.New("job conflict")
)

// OwnerKind identifies the mutually exclusive job ownership namespace.
type OwnerKind string

const (
	// OwnerAuthenticated associates a job with one internal numeric user ID.
	OwnerAuthenticated OwnerKind = "authenticated"
	// OwnerAnonymous associates a job with one anonymous-session UUID.
	OwnerAnonymous OwnerKind = "anonymous"
)

// Owner is the storage-level authorization key for a job. Exactly one of
// UserID and AnonymousSessionID is populated according to Kind.
type Owner struct {
	// Kind selects the authenticated or anonymous ownership namespace.
	Kind OwnerKind
	// UserID is the positive internal user ID for authenticated ownership.
	UserID int64
	// AnonymousSessionID is the capability session UUID for anonymous ownership.
	AnonymousSessionID string
}

// NewAuthenticatedOwner constructs a validated authenticated job owner.
func NewAuthenticatedOwner(userID int64) (Owner, error) {
	owner := Owner{Kind: OwnerAuthenticated, UserID: userID}
	return owner, owner.Validate()
}

// NewAnonymousOwner constructs a validated anonymous job owner.
func NewAnonymousOwner(sessionID string) (Owner, error) {
	owner := Owner{Kind: OwnerAnonymous, AnonymousSessionID: sessionID}
	return owner, owner.Validate()
}

// Validate enforces the exclusive owner representation used in SQL predicates.
func (owner Owner) Validate() error {
	switch owner.Kind {
	case OwnerAuthenticated:
		if owner.UserID <= 0 || owner.AnonymousSessionID != "" {
			return errors.New("authenticated job owner requires only a positive user ID")
		}
	case OwnerAnonymous:
		if owner.UserID != 0 || !validUUID(owner.AnonymousSessionID) {
			return errors.New("anonymous job owner requires only a session UUID")
		}
	default:
		return errors.New("job owner kind is invalid")
	}
	return nil
}

// Creator is the immutable identity snapshot stored for an authenticated job.
// Anonymous jobs always use the zero value.
type Creator struct {
	// ExternalID is the positive numeric Goeland employee ID.
	ExternalID int64
	// Login is the authenticated login at job creation time.
	Login string
	// Name is the display name at job creation time.
	Name string
	// Email is the optional authenticated email at job creation time.
	Email string
}

// SHA256 is a decoded SHA-256 digest. Its fixed size prevents malformed hashes
// from crossing the storage boundary.
type SHA256 [sha256.Size]byte

// ParseSHA256 decodes exactly 64 lowercase hexadecimal characters.
func ParseSHA256(value string) (SHA256, error) {
	var digest SHA256
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return digest, errors.New("SHA-256 must contain 64 lowercase hexadecimal characters")
	}
	decoded, err := hex.DecodeString(value)
	if err != nil {
		return digest, errors.New("SHA-256 must contain 64 lowercase hexadecimal characters")
	}
	copy(digest[:], decoded)
	return digest, nil
}

// String returns the canonical lowercase hexadecimal digest.
func (digest SHA256) String() string {
	return hex.EncodeToString(digest[:])
}

// Job is the authoritative durable processing state. Object keys and creator
// details are internal metadata and must not be exposed without authorization.
type Job struct {
	// ID is the public job UUID.
	ID string
	// UploadID identifies the single committed upload consumed by this job.
	UploadID string
	// Owner is the authorization key required by every user-facing operation.
	Owner Owner
	// Creator is the authenticated identity snapshot, or zero for anonymous jobs.
	Creator Creator
	// OriginalFilename is sanitized display metadata copied from the upload.
	OriginalFilename string
	// InputObjectKey is the opaque verified source-object key.
	InputObjectKey string
	// InputSHA256 is the verified complete source-object digest.
	InputSHA256 SHA256
	// InputBytes is the verified complete source size in bytes.
	InputBytes int64
	// OutputObjectKey is the opaque selected-result key when available.
	OutputObjectKey string
	// OutputSHA256 is the selected-result digest when available.
	OutputSHA256 *SHA256
	// OutputBytes is the selected-result size in bytes when available.
	OutputBytes *int64
	// PageCount is the validated source and result page count when known.
	PageCount *int32
	// SelectedProfile names the original, ebook, or screen result when selected.
	SelectedProfile string
	// TargetBytes is the indicative maximum desired result size in bytes.
	TargetBytes int64
	// TargetMet reports whether the selected result meets TargetBytes when known.
	TargetMet *bool
	// Status is the durable lifecycle state.
	Status Status
	// ProgressPercent is indicative progress between zero and one hundred.
	ProgressPercent int16
	// ProgressMessage is safe user-facing progress without raw tool output.
	ProgressMessage string
	// AttemptCount counts successful worker claims.
	AttemptCount int32
	// MaxAttempts bounds worker claims before stable failure.
	MaxAttempts int32
	// AvailableAt is the earliest instant at which a worker may claim the job.
	AvailableAt time.Time
	// LeaseOwner identifies the current worker while a lease is active.
	LeaseOwner string
	// LeaseExpiresAt is the exclusive current worker lease deadline.
	LeaseExpiresAt *time.Time
	// HeartbeatAt is the latest accepted worker heartbeat.
	HeartbeatAt *time.Time
	// CancelRequestedAt records cooperative cancellation intent.
	CancelRequestedAt *time.Time
	// ErrorCode is a stable machine-readable terminal error when present.
	ErrorCode string
	// ErrorMessage is a safe user-facing terminal error when present.
	ErrorMessage string
	// CreatedAt is the durable creation instant.
	CreatedAt time.Time
	// UpdatedAt is the latest durable mutation instant.
	UpdatedAt time.Time
	// StartedAt is the first successful worker claim instant.
	StartedAt *time.Time
	// CompletedAt is the terminal processing instant when present.
	CompletedAt *time.Time
	// ExpiresAt is the retention deadline, shortened to deletion time on request.
	ExpiresAt time.Time
	// DeletionRequestedAt tombstones the job until its blobs and metadata are purged.
	DeletionRequestedAt *time.Time
	// Version increases on each effective durable mutation.
	Version int64
}

// CreateRequest identifies the committed upload and immutable owner snapshot
// used to create exactly one durable job.
type CreateRequest struct {
	// UploadID identifies an owner-matching committed upload.
	UploadID string
	// Owner must match the upload in the creation query.
	Owner Owner
	// Creator is required for authenticated owners and forbidden for anonymous owners.
	Creator Creator
}

// Store is the durable owner-scoped job repository boundary.
type Store interface {
	// Create queues exactly one job from a committed owner-matching upload.
	Create(context.Context, Job) (Job, error)
	// Get returns a non-deleted job only to its owner.
	Get(context.Context, string, Owner) (Job, error)
	// Cancel records cancellation idempotently and immediately cancels queued work.
	Cancel(context.Context, string, Owner, time.Time) (Job, error)
	// Delete tombstones a job idempotently and requests cancellation when active.
	Delete(context.Context, string, Owner, time.Time) error
}

// Manager constructs jobs with repository-wide retention, target, retry and
// identifier policies before delegating mutations to Store.
type Manager struct {
	store       Store
	targetBytes int64
	retention   time.Duration
	maxAttempts int32
	random      io.Reader
	now         func() time.Time
}

// NewManager constructs a job manager with positive processing policies.
func NewManager(store Store, targetBytes int64, retention time.Duration, maxAttempts int32) (*Manager, error) {
	if store == nil {
		return nil, errors.New("job store is required")
	}
	if targetBytes <= 0 {
		return nil, errors.New("job target size must be positive")
	}
	if retention <= 0 {
		return nil, errors.New("job retention must be positive")
	}
	if maxAttempts <= 0 {
		return nil, errors.New("job maximum attempts must be positive")
	}
	return &Manager{
		store: store, targetBytes: targetBytes, retention: retention,
		maxAttempts: maxAttempts, random: rand.Reader, now: time.Now,
	}, nil
}

// Create validates ownership and queues a job from authoritative committed-upload metadata.
func (manager *Manager) Create(ctx context.Context, request CreateRequest) (Job, error) {
	if !validUUID(request.UploadID) {
		return Job{}, errors.New("job upload ID must be a UUID")
	}
	if err := validateCreator(request.Owner, request.Creator); err != nil {
		return Job{}, err
	}
	id, err := generateUUID(manager.random)
	if err != nil {
		return Job{}, fmt.Errorf("generate job ID: %w", err)
	}
	now := manager.now().UTC()
	return manager.store.Create(ctx, Job{
		ID: id, UploadID: request.UploadID, Owner: request.Owner,
		Creator: request.Creator, TargetBytes: manager.targetBytes,
		Status: StatusQueued, MaxAttempts: manager.maxAttempts,
		AvailableAt: now, CreatedAt: now, UpdatedAt: now,
		ExpiresAt: now.Add(manager.retention), Version: 1,
	})
}

// Get returns the current non-deleted owner-authorized job snapshot.
func (manager *Manager) Get(ctx context.Context, jobID string, owner Owner) (Job, error) {
	return manager.store.Get(ctx, jobID, owner)
}

// Cancel records owner-authorized cancellation at the manager clock instant.
func (manager *Manager) Cancel(ctx context.Context, jobID string, owner Owner) (Job, error) {
	return manager.store.Cancel(ctx, jobID, owner, manager.now().UTC())
}

// Delete tombstones owner-authorized retained data at the manager clock instant.
func (manager *Manager) Delete(ctx context.Context, jobID string, owner Owner) error {
	return manager.store.Delete(ctx, jobID, owner, manager.now().UTC())
}

func validateCreator(owner Owner, creator Creator) error {
	if err := owner.Validate(); err != nil {
		return err
	}
	if owner.Kind == OwnerAnonymous {
		if creator != (Creator{}) {
			return errors.New("anonymous job cannot contain an identity snapshot")
		}
		return nil
	}
	if creator.ExternalID <= 0 {
		return errors.New("authenticated job requires a positive external ID")
	}
	if !validBoundedText(creator.Login, maxIdentityTextBytes) {
		return errors.New("authenticated job login is invalid")
	}
	if !validBoundedText(creator.Name, maxIdentityTextBytes) {
		return errors.New("authenticated job name is invalid")
	}
	if creator.Email != "" && !validBoundedText(creator.Email, maxEmailBytes) {
		return errors.New("authenticated job email is invalid")
	}
	return nil
}

func validateJobSeed(job Job) error {
	if !validUUID(job.ID) || !validUUID(job.UploadID) {
		return errors.New("job and upload IDs must be UUIDs")
	}
	if err := validateCreator(job.Owner, job.Creator); err != nil {
		return err
	}
	if job.OriginalFilename != "" || job.InputObjectKey != "" || job.InputSHA256 != (SHA256{}) || job.InputBytes != 0 {
		return errors.New("new job input metadata must come from the committed upload")
	}
	if job.Status != StatusQueued || job.ProgressPercent != 0 || job.ProgressMessage != "" || job.AttemptCount != 0 {
		return errors.New("new job must have empty queued progress")
	}
	if job.TargetBytes <= 0 || job.MaxAttempts <= 0 || job.Version != 1 {
		return errors.New("new job processing policy is invalid")
	}
	if job.CreatedAt.IsZero() || job.UpdatedAt.IsZero() || job.AvailableAt.IsZero() || job.ExpiresAt.IsZero() ||
		job.UpdatedAt.Before(job.CreatedAt) || job.AvailableAt.Before(job.CreatedAt) || !job.ExpiresAt.After(job.CreatedAt) {
		return errors.New("new job timestamps are invalid")
	}
	return nil
}

func validateStoredJob(job Job) error {
	if !validUUID(job.ID) || !validUUID(job.UploadID) {
		return errors.New("stored job identity is invalid")
	}
	if err := validateCreator(job.Owner, job.Creator); err != nil {
		return err
	}
	if !validBoundedText(job.OriginalFilename, maxFilenameBytes) ||
		!validBoundedText(job.InputObjectKey, maxObjectKeyBytes) || job.InputBytes <= 0 {
		return errors.New("stored job input metadata is invalid")
	}
	if !job.Status.Valid() || job.ProgressPercent < 0 || job.ProgressPercent > 100 ||
		len(job.ProgressMessage) > maxProgressBytes || job.AttemptCount < 0 ||
		job.MaxAttempts <= 0 || job.AttemptCount > job.MaxAttempts || job.TargetBytes <= 0 {
		return errors.New("stored job processing state is invalid")
	}
	if job.OutputObjectKey != "" && !validBoundedText(job.OutputObjectKey, maxObjectKeyBytes) {
		return errors.New("stored job output object key is invalid")
	}
	if job.OutputBytes != nil && *job.OutputBytes <= 0 {
		return errors.New("stored job output size is invalid")
	}
	if job.PageCount != nil && *job.PageCount <= 0 {
		return errors.New("stored job page count is invalid")
	}
	switch job.SelectedProfile {
	case "", "original", "ebook", "screen":
	default:
		return errors.New("stored job selected profile is invalid")
	}
	if len(job.LeaseOwner) > maxIdentityTextBytes || len(job.ErrorCode) > maxErrorCodeBytes ||
		len(job.ErrorMessage) > maxErrorMessageBytes {
		return errors.New("stored job operational metadata is too long")
	}
	if job.CreatedAt.IsZero() || job.UpdatedAt.IsZero() || job.AvailableAt.IsZero() || job.ExpiresAt.IsZero() ||
		job.UpdatedAt.Before(job.CreatedAt) || job.AvailableAt.Before(job.CreatedAt) || job.Version <= 0 {
		return errors.New("stored job timestamps or version are invalid")
	}
	for _, optional := range []*time.Time{
		job.LeaseExpiresAt, job.HeartbeatAt, job.CancelRequestedAt, job.StartedAt,
		job.CompletedAt, job.DeletionRequestedAt,
	} {
		if optional != nil && optional.Before(job.CreatedAt) {
			return errors.New("stored job event precedes creation")
		}
	}
	return nil
}

func validBoundedText(value string, maximum int) bool {
	return strings.TrimSpace(value) != "" && len(value) <= maximum
}

func generateUUID(random io.Reader) (string, error) {
	value := make([]byte, 16)
	if _, err := io.ReadFull(random, value); err != nil {
		return "", err
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}

func validUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	for index, character := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			continue
		}
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return true
}
