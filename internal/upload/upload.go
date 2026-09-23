// Package upload models resumable PDF upload sessions without depending on a
// particular object-store implementation.
package upload

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"path"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	maxFilenameBytes    = 255
	maxContentTypeBytes = 255
	maxObjectKeyBytes   = 1024
	maxBackendIDBytes   = 2048
)

var (
	// ErrNotFound hides whether an upload does not exist or belongs to another owner.
	ErrNotFound = errors.New("upload not found")
	// ErrConflict reports an upload identifier or immutable value collision.
	ErrConflict = errors.New("upload conflict")
	// ErrExpired reports an upload whose exclusive expiry instant has passed.
	ErrExpired = errors.New("upload expired")
	// ErrInvalidState reports an operation forbidden by the durable upload state.
	ErrInvalidState = errors.New("upload state does not allow operation")
	// ErrPartConflict reports a retry that changes an accepted part.
	ErrPartConflict = errors.New("upload part conflicts with accepted part")
	// ErrPartOutOfOrder reports a new part that is not the next contiguous index.
	ErrPartOutOfOrder = errors.New("upload part index is not contiguous")
	// ErrPartSize reports a part whose byte count does not match its position.
	ErrPartSize = errors.New("upload part size is invalid")
	// ErrIncomplete reports an upload whose complete ordered part set is absent.
	ErrIncomplete = errors.New("upload is incomplete")
)

// OwnerKind identifies the mutually exclusive upload ownership namespace.
type OwnerKind string

const (
	// OwnerAuthenticated associates an upload with one internal numeric user ID.
	OwnerAuthenticated OwnerKind = "authenticated"
	// OwnerAnonymous associates an upload with one anonymous-session UUID.
	OwnerAnonymous OwnerKind = "anonymous"
)

// Owner is the storage-level authorization key for an upload. Exactly one of
// UserID and AnonymousSessionID is populated according to Kind.
type Owner struct {
	// Kind selects the authenticated or anonymous ownership namespace.
	Kind OwnerKind
	// UserID is the positive internal user ID for authenticated ownership.
	UserID int64
	// AnonymousSessionID is the active capability session UUID for anonymous ownership.
	AnonymousSessionID string
}

// NewAuthenticatedOwner constructs a validated authenticated upload owner.
func NewAuthenticatedOwner(userID int64) (Owner, error) {
	owner := Owner{Kind: OwnerAuthenticated, UserID: userID}
	return owner, owner.Validate()
}

// NewAnonymousOwner constructs a validated anonymous upload owner.
func NewAnonymousOwner(sessionID string) (Owner, error) {
	owner := Owner{Kind: OwnerAnonymous, AnonymousSessionID: sessionID}
	return owner, owner.Validate()
}

// Validate enforces the exclusive owner representation used in SQL predicates.
func (owner Owner) Validate() error {
	switch owner.Kind {
	case OwnerAuthenticated:
		if owner.UserID <= 0 || owner.AnonymousSessionID != "" {
			return errors.New("authenticated upload owner requires only a positive user ID")
		}
	case OwnerAnonymous:
		if owner.UserID != 0 || !validUUID(owner.AnonymousSessionID) {
			return errors.New("anonymous upload owner requires only a session UUID")
		}
	default:
		return errors.New("upload owner kind is invalid")
	}
	return nil
}

// Status is the durable lifecycle state of an upload session.
type Status string

const (
	// StatusUploading accepts the next contiguous part and idempotent retries.
	StatusUploading Status = "uploading"
	// StatusCommitting prevents new parts while object verification runs.
	StatusCommitting Status = "committing"
	// StatusCommitted records a successfully verified complete object.
	StatusCommitted Status = "committed"
	// StatusAborted records an explicit idempotent abandonment.
	StatusAborted Status = "aborted"
	// StatusExpired records retention cleanup of an incomplete upload.
	StatusExpired Status = "expired"
)

// SHA256 is a decoded SHA-256 digest. Its fixed size prevents malformed hashes
// from crossing the domain-to-storage boundary.
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

// Session is the authoritative metadata and progress of one resumable upload.
type Session struct {
	// ID is the public upload UUID.
	ID string
	// Owner is the authorization key copied into every storage operation.
	Owner Owner
	// OriginalFilename is a sanitized display name, never an object-store path.
	OriginalFilename string
	// ContentType is browser-declared metadata and is not a trust signal.
	ContentType string
	// DeclaredSize is the exact complete source size in bytes.
	DeclaredSize int64
	// ChunkSize is the required size of each non-final part in bytes.
	ChunkSize int64
	// ExpectedChunks is the exact number of zero-based contiguous parts.
	ExpectedChunks int32
	// ReceivedChunks counts distinct durable part records.
	ReceivedChunks int32
	// ReceivedBytes sums distinct durable part sizes.
	ReceivedBytes int64
	// Status is the durable upload lifecycle state.
	Status Status
	// ObjectKey is an opaque backend key unrelated to OriginalFilename.
	ObjectKey string
	// MultipartUploadID is an optional backend-specific multipart handle.
	MultipartUploadID string
	// CommittedSHA256 is present only after complete-object verification.
	CommittedSHA256 *SHA256
	// CreatedAt is the authoritative creation instant.
	CreatedAt time.Time
	// UpdatedAt is the most recent metadata transition instant.
	UpdatedAt time.Time
	// CommittedAt is present after successful complete-object verification.
	CommittedAt *time.Time
	// ExpiresAt is the exclusive expiry instant for incomplete state.
	ExpiresAt time.Time
}

// Part is one durable backend part descriptor; it never contains chunk bytes.
type Part struct {
	// UploadID identifies the parent session.
	UploadID string
	// Index is the zero-based contiguous position.
	Index int32
	// ByteSize is the exact accepted byte count.
	ByteSize int64
	// SHA256 authenticates the accepted chunk content.
	SHA256 SHA256
	// BackendPartID is the opaque identifier required to finalize the backend object.
	BackendPartID string
	// CreatedAt is the first acceptance instant and does not change on retries.
	CreatedAt time.Time
}

// PartWrite is an owner-scoped attempt to record one backend part.
type PartWrite struct {
	// UploadID identifies the target upload.
	UploadID string
	// Owner must match the upload in the storage query.
	Owner Owner
	// Index is the proposed zero-based position.
	Index int32
	// ByteSize is the backend-confirmed part size.
	ByteSize int64
	// SHA256 is the server-calculated part digest.
	SHA256 SHA256
	// BackendPartID is the backend-confirmed opaque part identifier.
	BackendPartID string
	// ObservedAt is used for exclusive expiry and update timestamps.
	ObservedAt time.Time
}

// PartReceipt reports progress after a new part or identical retry.
type PartReceipt struct {
	// Part is the authoritative stored descriptor.
	Part Part
	// AlreadyPresent is true only for an identical accepted retry.
	AlreadyPresent bool
	// ReceivedChunks counts distinct accepted parts after the operation.
	ReceivedChunks int32
	// ReceivedBytes sums distinct accepted bytes after the operation.
	ReceivedBytes int64
}

// CommitPlan is an immutable ordered snapshot used to finalize and verify the
// backend object without holding a PostgreSQL transaction open.
type CommitPlan struct {
	// Session is locked against new parts by StatusCommitting.
	Session Session
	// Parts contains exactly ExpectedChunks descriptors ordered by index.
	Parts []Part
}

// Store is the durable boundary for upload sessions and part metadata.
type Store interface {
	// Create inserts one complete upload session.
	Create(context.Context, Session) (Session, error)
	// Get returns an upload only to its owner.
	Get(context.Context, string, Owner) (Session, error)
	// RecordPart atomically enforces idempotence, order, size and progress.
	RecordPart(context.Context, PartWrite) (PartReceipt, error)
	// BeginCommit verifies completeness and freezes the ordered part set.
	BeginCommit(context.Context, string, Owner, time.Time) (CommitPlan, error)
	// Complete records a verified complete-object digest idempotently.
	Complete(context.Context, string, Owner, SHA256, time.Time) (Session, error)
	// Abort idempotently prevents further writes to an incomplete upload.
	Abort(context.Context, string, Owner, time.Time) (Session, error)
}

// Manager validates upload metadata, generates opaque identifiers, and
// delegates all correctness-sensitive mutations to Store.
type Manager struct {
	store      Store
	maxBytes   int64
	chunkBytes int64
	sessionTTL time.Duration
	random     io.Reader
	now        func() time.Time
}

// NewManager constructs a manager with bounded sizes and incomplete-session lifetime.
func NewManager(store Store, maxBytes, chunkBytes int64, sessionTTL time.Duration) (*Manager, error) {
	if store == nil {
		return nil, errors.New("upload store is required")
	}
	if maxBytes <= 0 {
		return nil, errors.New("maximum upload size must be positive")
	}
	if chunkBytes <= 0 || chunkBytes > maxBytes || chunkBytes > math.MaxInt32 {
		return nil, errors.New("upload chunk size must be positive, fit int32, and not exceed maximum size")
	}
	if sessionTTL <= 0 {
		return nil, errors.New("upload session TTL must be positive")
	}
	return &Manager{
		store: store, maxBytes: maxBytes, chunkBytes: chunkBytes,
		sessionTTL: sessionTTL, random: rand.Reader, now: time.Now,
	}, nil
}

// Start validates immutable metadata and persists a new resumable session.
func (manager *Manager) Start(ctx context.Context, owner Owner, filename, contentType string, declaredSize int64) (Session, error) {
	if err := owner.Validate(); err != nil {
		return Session{}, err
	}
	if declaredSize <= 0 || declaredSize > manager.maxBytes {
		return Session{}, fmt.Errorf("declared upload size must be between 1 and %d bytes", manager.maxBytes)
	}
	filename, err := SanitizeFilename(filename)
	if err != nil {
		return Session{}, err
	}
	contentType, err = normalizeContentType(contentType)
	if err != nil {
		return Session{}, err
	}
	expectedChunks := (declaredSize-1)/manager.chunkBytes + 1
	if expectedChunks > math.MaxInt32 {
		return Session{}, errors.New("upload requires too many chunks")
	}
	id, err := generateUUID(manager.random)
	if err != nil {
		return Session{}, fmt.Errorf("generate upload ID: %w", err)
	}
	now := manager.now().UTC()
	return manager.store.Create(ctx, Session{
		ID: id, Owner: owner, OriginalFilename: filename, ContentType: contentType,
		DeclaredSize: declaredSize, ChunkSize: manager.chunkBytes,
		ExpectedChunks: int32(expectedChunks), Status: StatusUploading,
		ObjectKey: "uploads/" + id + "/source", CreatedAt: now, UpdatedAt: now,
		ExpiresAt: now.Add(manager.sessionTTL),
	})
}

// RecordPart persists one backend-confirmed chunk descriptor.
func (manager *Manager) RecordPart(ctx context.Context, owner Owner, uploadID string, index int32, byteSize int64, digest SHA256, backendPartID string) (PartReceipt, error) {
	return manager.store.RecordPart(ctx, PartWrite{
		UploadID: uploadID, Owner: owner, Index: index, ByteSize: byteSize,
		SHA256: digest, BackendPartID: backendPartID, ObservedAt: manager.now().UTC(),
	})
}

// BeginCommit verifies and freezes a complete ordered part set.
func (manager *Manager) BeginCommit(ctx context.Context, owner Owner, uploadID string) (CommitPlan, error) {
	return manager.store.BeginCommit(ctx, uploadID, owner, manager.now().UTC())
}

// Complete records the digest produced by streaming the finalized backend object.
func (manager *Manager) Complete(ctx context.Context, owner Owner, uploadID string, digest SHA256) (Session, error) {
	return manager.store.Complete(ctx, uploadID, owner, digest, manager.now().UTC())
}

// Abort idempotently closes an incomplete upload.
func (manager *Manager) Abort(ctx context.Context, owner Owner, uploadID string) (Session, error) {
	return manager.store.Abort(ctx, uploadID, owner, manager.now().UTC())
}

// SanitizeFilename returns a bounded display basename independent of host path semantics.
func SanitizeFilename(value string) (string, error) {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	value = strings.TrimSpace(path.Base(value))
	if value == "" || value == "." || value == ".." || value == "/" || !utf8.ValidString(value) {
		return "", errors.New("upload filename is invalid")
	}
	if len(value) > maxFilenameBytes {
		return "", fmt.Errorf("upload filename exceeds %d bytes", maxFilenameBytes)
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return "", errors.New("upload filename contains control characters")
		}
	}
	return value, nil
}

func normalizeContentType(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "application/octet-stream", nil
	}
	mediaType, parameters, err := mime.ParseMediaType(value)
	if err != nil {
		return "", errors.New("upload content type is invalid")
	}
	value = mime.FormatMediaType(mediaType, parameters)
	if len(value) > maxContentTypeBytes {
		return "", fmt.Errorf("upload content type exceeds %d bytes", maxContentTypeBytes)
	}
	return value, nil
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
