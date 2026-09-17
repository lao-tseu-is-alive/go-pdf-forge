package anonymous

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/netip"
	"time"
)

var (
	// ErrSessionNotFound means the public session ID has no persistent record.
	ErrSessionNotFound = errors.New("anonymous session not found")
	// ErrSessionConflict means a generated session ID or digest already exists.
	ErrSessionConflict = errors.New("anonymous session conflicts with an existing record")
	// ErrInvalidCapability means a token is malformed or does not match its session.
	ErrInvalidCapability = errors.New("invalid anonymous capability")
	// ErrSessionExpired means the capability reached its persistent expiry time.
	ErrSessionExpired = errors.New("anonymous session expired")
	// ErrSessionRevoked means the capability was explicitly revoked.
	ErrSessionRevoked = errors.New("anonymous session revoked")
	// ErrSessionInactive means a concurrent expiry or revocation prevented a touch.
	ErrSessionInactive = errors.New("anonymous session is no longer active")
)

// Session is the persistent anonymous identity record. It deliberately cannot
// contain the raw bearer capability or a raw client IP address.
type Session struct {
	// ID is the public UUID embedded in the bearer capability.
	ID string
	// SecretDigest is the HMAC-SHA-256 of the capability secret.
	SecretDigest [sha256.Size]byte
	// InitialIPDigest is the HMAC-SHA-256 of the creation address.
	InitialIPDigest [sha256.Size]byte
	// LastIPDigest is the HMAC-SHA-256 of the most recently authenticated address.
	LastIPDigest [sha256.Size]byte
	// CreatedAt is the authoritative creation instant.
	CreatedAt time.Time
	// LastSeenAt is the most recent successful capability authentication.
	LastSeenAt time.Time
	// ExpiresAt is the exclusive validity deadline.
	ExpiresAt time.Time
	// RevokedAt is the first explicit revocation instant, when present.
	RevokedAt *time.Time
}

// Expired reports whether the exclusive expiry deadline has been reached.
func (session Session) Expired(at time.Time) bool {
	return !at.Before(session.ExpiresAt)
}

// Revoked reports whether the session has been explicitly revoked.
func (session Session) Revoked() bool {
	return session.RevokedAt != nil
}

// SessionStore is the persistence boundary for anonymous-session lifecycle
// operations. Implementations must never accept or persist a raw capability.
type SessionStore interface {
	// Create persists one new session and returns the authoritative record.
	Create(context.Context, Session) (Session, error)
	// Get returns a session regardless of active, expired, or revoked state.
	Get(context.Context, string) (Session, error)
	// TouchActive records successful use only while the session remains active.
	TouchActive(context.Context, string, [sha256.Size]byte, time.Time) error
	// Revoke idempotently records the first revocation instant.
	Revoke(context.Context, string, time.Time) error
}

// IssuedSession is returned exactly once when a session is created. Token is a
// bearer credential and must never be persisted or logged by the server.
type IssuedSession struct {
	// ID is the public session UUID.
	ID string
	// Token is the one-time raw capability delivered to the client.
	Token string
	// ExpiresAt is the exclusive capability validity deadline.
	ExpiresAt time.Time
}

// Identity is the non-secret result of successful capability authentication.
type Identity struct {
	// SessionID is the authenticated anonymous owner UUID.
	SessionID string
	// CreatedAt is the persistent session creation instant.
	CreatedAt time.Time
	// ExpiresAt is the exclusive capability validity deadline.
	ExpiresAt time.Time
}

// Manager generates capabilities and enforces their persistent lifecycle.
type Manager struct {
	store  SessionStore
	pepper []byte
	ttl    time.Duration
	now    func() time.Time
}

// NewManager validates and copies capability configuration. The pepper is
// retained only in process memory and must contain at least 32 bytes.
func NewManager(store SessionStore, pepper []byte, ttl time.Duration) (*Manager, error) {
	if store == nil {
		return nil, errors.New("anonymous session store is required")
	}
	if err := validatePepper(pepper); err != nil {
		return nil, err
	}
	if ttl <= 0 {
		return nil, errors.New("anonymous session TTL must be positive")
	}
	return &Manager{
		store:  store,
		pepper: append([]byte(nil), pepper...),
		ttl:    ttl,
		now:    time.Now,
	}, nil
}

// Create issues a capability and persists only its secret and IP digests.
func (manager *Manager) Create(ctx context.Context, clientIP netip.Addr) (IssuedSession, error) {
	ipDigest, err := DigestIP(manager.pepper, clientIP)
	if err != nil {
		return IssuedSession{}, err
	}
	capability, err := Generate(manager.pepper)
	if err != nil {
		return IssuedSession{}, err
	}
	now := manager.now().UTC()
	record := Session{
		ID:              capability.SessionID,
		SecretDigest:    capability.Digest,
		InitialIPDigest: ipDigest,
		LastIPDigest:    ipDigest,
		CreatedAt:       now,
		LastSeenAt:      now,
		ExpiresAt:       now.Add(manager.ttl),
	}
	created, err := manager.store.Create(ctx, record)
	if err != nil {
		return IssuedSession{}, err
	}
	if created.ID != record.ID || !Matches(created.SecretDigest, record.SecretDigest) {
		return IssuedSession{}, errors.New("anonymous session store returned an inconsistent record")
	}
	return IssuedSession{ID: created.ID, Token: capability.Raw, ExpiresAt: created.ExpiresAt}, nil
}

// Authenticate verifies a capability in constant time, enforces expiry and
// revocation, then atomically touches the still-active persistent record.
func (manager *Manager) Authenticate(ctx context.Context, raw string, clientIP netip.Addr) (Identity, error) {
	sessionID, digest, err := Parse(raw, manager.pepper)
	if err != nil {
		return Identity{}, ErrInvalidCapability
	}
	record, err := manager.store.Get(ctx, sessionID)
	if err != nil {
		if errors.Is(err, ErrSessionNotFound) {
			return Identity{}, ErrInvalidCapability
		}
		return Identity{}, err
	}
	if !Matches(digest, record.SecretDigest) {
		return Identity{}, ErrInvalidCapability
	}
	if record.Revoked() {
		return Identity{}, ErrSessionRevoked
	}
	now := manager.now().UTC()
	if record.Expired(now) {
		return Identity{}, ErrSessionExpired
	}
	ipDigest, err := DigestIP(manager.pepper, clientIP)
	if err != nil {
		return Identity{}, err
	}
	if err := manager.store.TouchActive(ctx, record.ID, ipDigest, now); err != nil {
		return Identity{}, err
	}
	return Identity{SessionID: record.ID, CreatedAt: record.CreatedAt, ExpiresAt: record.ExpiresAt}, nil
}

// Revoke validates the capability and idempotently records its revocation. An
// already-revoked valid capability succeeds without changing its first timestamp.
func (manager *Manager) Revoke(ctx context.Context, raw string) error {
	sessionID, digest, err := Parse(raw, manager.pepper)
	if err != nil {
		return ErrInvalidCapability
	}
	record, err := manager.store.Get(ctx, sessionID)
	if err != nil {
		if errors.Is(err, ErrSessionNotFound) {
			return ErrInvalidCapability
		}
		return err
	}
	if !Matches(digest, record.SecretDigest) {
		return ErrInvalidCapability
	}
	if record.Revoked() {
		return nil
	}
	now := manager.now().UTC()
	if record.Expired(now) {
		return ErrSessionExpired
	}
	return manager.store.Revoke(ctx, record.ID, now)
}
