package anonymous

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const sessionColumns = `id, secret_hash, initial_ip_hash, last_ip_hash,
    created_at, last_seen_at, expires_at, revoked_at`

type sessionDatabase interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type sessionStorageError struct {
	operation string
	cause     error
}

func (err *sessionStorageError) Error() string {
	return fmt.Sprintf("anonymous session %s failed", err.operation)
}

func (err *sessionStorageError) Unwrap() error {
	return err.cause
}

// PostgresSessionStore persists anonymous sessions without receiving raw capabilities
// or raw client addresses.
type PostgresSessionStore struct {
	database sessionDatabase
}

var _ SessionStore = (*PostgresSessionStore)(nil)

// NewPostgresSessionStore binds anonymous-session persistence to a pgx-compatible
// database such as pgxpool.Pool.
func NewPostgresSessionStore(database sessionDatabase) (*PostgresSessionStore, error) {
	if database == nil {
		return nil, errors.New("anonymous session database is required")
	}
	return &PostgresSessionStore{database: database}, nil
}

// Create inserts one complete session record and returns PostgreSQL's
// authoritative timestamp representation.
func (store *PostgresSessionStore) Create(ctx context.Context, session Session) (Session, error) {
	if err := validateSessionForCreate(session); err != nil {
		return Session{}, err
	}
	row := store.database.QueryRow(ctx, `
INSERT INTO anonymous_session (
    id, secret_hash, initial_ip_hash, last_ip_hash,
    created_at, last_seen_at, expires_at
) VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING `+sessionColumns,
		session.ID,
		session.SecretDigest[:],
		session.InitialIPDigest[:],
		session.LastIPDigest[:],
		session.CreatedAt,
		session.LastSeenAt,
		session.ExpiresAt,
	)
	created, err := scanSession(row)
	if err == nil {
		return created, nil
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && postgresError.Code == "23505" {
		return Session{}, ErrSessionConflict
	}
	return Session{}, &sessionStorageError{operation: "create", cause: err}
}

// Get loads one session by its public UUID without filtering inactive states.
func (store *PostgresSessionStore) Get(ctx context.Context, id string) (Session, error) {
	if !validUUID(id) {
		return Session{}, ErrSessionNotFound
	}
	record, err := scanSession(store.database.QueryRow(ctx,
		`SELECT `+sessionColumns+` FROM anonymous_session WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrSessionNotFound
	}
	if err != nil {
		return Session{}, &sessionStorageError{operation: "lookup", cause: err}
	}
	return record, nil
}

// TouchActive updates successful-use metadata only if expiry or revocation did
// not race with the caller's preceding verification.
func (store *PostgresSessionStore) TouchActive(ctx context.Context, id string, ipDigest [sha256.Size]byte, seenAt time.Time) error {
	if !validUUID(id) {
		return ErrSessionNotFound
	}
	if seenAt.IsZero() {
		return errors.New("anonymous session last-seen time is required")
	}
	result, err := store.database.Exec(ctx, `
UPDATE anonymous_session
SET last_ip_hash = CASE WHEN last_seen_at <= $3 THEN $2 ELSE last_ip_hash END,
    last_seen_at = GREATEST(last_seen_at, $3)
WHERE id = $1 AND revoked_at IS NULL AND expires_at > $3`, id, ipDigest[:], seenAt)
	if err != nil {
		return &sessionStorageError{operation: "touch", cause: err}
	}
	if result.RowsAffected() == 0 {
		return ErrSessionInactive
	}
	return nil
}

// Revoke records the first revocation instant and succeeds for repeated calls.
func (store *PostgresSessionStore) Revoke(ctx context.Context, id string, revokedAt time.Time) error {
	if !validUUID(id) {
		return ErrSessionNotFound
	}
	if revokedAt.IsZero() {
		return errors.New("anonymous session revocation time is required")
	}
	result, err := store.database.Exec(ctx, `
UPDATE anonymous_session
SET revoked_at = COALESCE(revoked_at, $2)
WHERE id = $1`, id, revokedAt)
	if err != nil {
		return &sessionStorageError{operation: "revoke", cause: err}
	}
	if result.RowsAffected() == 0 {
		return ErrSessionNotFound
	}
	return nil
}

func scanSession(row pgx.Row) (Session, error) {
	var record Session
	var secretDigest []byte
	var initialIPDigest []byte
	var lastIPDigest []byte
	if err := row.Scan(
		&record.ID,
		&secretDigest,
		&initialIPDigest,
		&lastIPDigest,
		&record.CreatedAt,
		&record.LastSeenAt,
		&record.ExpiresAt,
		&record.RevokedAt,
	); err != nil {
		return Session{}, err
	}
	if err := copyDigest(&record.SecretDigest, secretDigest, "secret"); err != nil {
		return Session{}, err
	}
	if err := copyDigest(&record.InitialIPDigest, initialIPDigest, "initial IP"); err != nil {
		return Session{}, err
	}
	if err := copyDigest(&record.LastIPDigest, lastIPDigest, "last IP"); err != nil {
		return Session{}, err
	}
	return record, nil
}

func copyDigest(destination *[sha256.Size]byte, source []byte, name string) error {
	if len(source) != sha256.Size {
		return fmt.Errorf("anonymous session %s digest has invalid length", name)
	}
	copy(destination[:], source)
	return nil
}

func validateSessionForCreate(session Session) error {
	if !validUUID(session.ID) {
		return errors.New("anonymous session ID must be a UUID")
	}
	if session.CreatedAt.IsZero() || session.LastSeenAt.IsZero() || session.ExpiresAt.IsZero() {
		return errors.New("anonymous session timestamps are required")
	}
	if !session.ExpiresAt.After(session.CreatedAt) {
		return errors.New("anonymous session expiry must follow creation")
	}
	if session.RevokedAt != nil {
		return errors.New("new anonymous session cannot be revoked")
	}
	return nil
}
