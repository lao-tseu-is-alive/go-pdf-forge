package anonymous

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestManagerCreatePersistsOnlyDerivedValues(t *testing.T) {
	t.Parallel()

	pepper := []byte(strings.Repeat("p", 32))
	store := &memorySessionStore{}
	manager, err := NewManager(store, pepper, 48*time.Hour)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	now := time.Date(2026, 9, 17, 10, 0, 0, 0, time.FixedZone("CEST", 2*60*60))
	manager.now = func() time.Time { return now }

	issued, err := manager.Create(context.Background(), netip.MustParseAddr("192.0.2.10"))
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if issued.Token == "" || issued.ID == "" {
		t.Fatalf("Create() = %#v", issued)
	}
	if store.created.ID != issued.ID || store.created.CreatedAt != now.UTC() {
		t.Errorf("persisted session = %#v", store.created)
	}
	if !store.created.ExpiresAt.Equal(now.UTC().Add(48*time.Hour)) || !issued.ExpiresAt.Equal(store.created.ExpiresAt) {
		t.Errorf("expiry persisted=%v issued=%v", store.created.ExpiresAt, issued.ExpiresAt)
	}
	sessionID, digest, err := Parse(issued.Token, pepper)
	if err != nil {
		t.Fatalf("Parse(issued token) error = %v", err)
	}
	if sessionID != store.created.ID || !Matches(digest, store.created.SecretDigest) {
		t.Fatal("persisted digest does not authenticate the issued capability")
	}
	wantIP, err := DigestIP(pepper, netip.MustParseAddr("192.0.2.10"))
	if err != nil {
		t.Fatalf("DigestIP() error = %v", err)
	}
	if !Matches(wantIP, store.created.InitialIPDigest) || !Matches(wantIP, store.created.LastIPDigest) {
		t.Fatal("Create() did not persist canonical IP digests")
	}
}

func TestManagerCreateDoesNotIssueTokenForInconsistentStoreResult(t *testing.T) {
	t.Parallel()

	pepper := []byte(strings.Repeat("p", 32))
	store := &memorySessionStore{replaceCreatedID: "42424242-4242-4242-8242-424242424242"}
	manager, err := NewManager(store, pepper, time.Hour)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	if issued, err := manager.Create(context.Background(), netip.MustParseAddr("192.0.2.10")); err == nil || issued.Token != "" {
		t.Fatalf("Create() = %#v, %v; want no issued token", issued, err)
	}
}

func TestManagerAuthenticateTouchesActiveSession(t *testing.T) {
	t.Parallel()

	manager, store, raw, record, now := activeManager(t)
	clientIP := netip.MustParseAddr("2001:db8::42")
	identity, err := manager.Authenticate(context.Background(), raw, clientIP)
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if identity.SessionID != record.ID || !identity.ExpiresAt.Equal(record.ExpiresAt) {
		t.Errorf("Authenticate() = %#v", identity)
	}
	if store.touchCount != 1 || store.touchedID != record.ID || !store.touchedAt.Equal(now) {
		t.Errorf("touch = count %d id %q at %v", store.touchCount, store.touchedID, store.touchedAt)
	}
	wantIP, _ := DigestIP(manager.pepper, clientIP)
	if !Matches(wantIP, store.touchedIP) {
		t.Error("Authenticate() touched the wrong IP digest")
	}
}

func TestManagerAuthenticateRejectsInactiveOrInvalidCapabilities(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		prepare func(*memorySessionStore, *Session, string) string
		want    error
	}{
		{name: "malformed", prepare: func(_ *memorySessionStore, _ *Session, _ string) string { return "invalid" }, want: ErrInvalidCapability},
		{name: "missing", prepare: func(store *memorySessionStore, _ *Session, raw string) string {
			store.getError = ErrSessionNotFound
			return raw
		}, want: ErrInvalidCapability},
		{name: "wrong secret", prepare: capabilityWithWrongSecret, want: ErrInvalidCapability},
		{name: "expired", prepare: func(_ *memorySessionStore, record *Session, raw string) string {
			record.ExpiresAt = time.Date(2026, 9, 17, 7, 0, 0, 0, time.UTC)
			return raw
		}, want: ErrSessionExpired},
		{name: "revoked", prepare: func(_ *memorySessionStore, record *Session, raw string) string {
			revoked := time.Date(2026, 9, 17, 7, 0, 0, 0, time.UTC)
			record.RevokedAt = &revoked
			return raw
		}, want: ErrSessionRevoked},
		{name: "concurrent revoke", prepare: func(store *memorySessionStore, _ *Session, raw string) string {
			store.touchError = ErrSessionInactive
			return raw
		}, want: ErrSessionInactive},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			manager, store, raw, record, _ := activeManager(t)
			raw = test.prepare(store, &record, raw)
			store.record = record
			_, err := manager.Authenticate(context.Background(), raw, netip.MustParseAddr("192.0.2.20"))
			if !errors.Is(err, test.want) {
				t.Fatalf("Authenticate() error = %v, want %v", err, test.want)
			}
			if test.want != ErrSessionInactive && store.touchCount != 0 {
				t.Errorf("touch count = %d, want 0", store.touchCount)
			}
		})
	}
}

func TestManagerRevokeIsIdempotent(t *testing.T) {
	t.Parallel()

	manager, store, raw, record, now := activeManager(t)
	if err := manager.Revoke(context.Background(), raw); err != nil {
		t.Fatalf("Revoke() error = %v", err)
	}
	if store.revokeCount != 1 || store.revokedID != record.ID || !store.revokedAt.Equal(now) {
		t.Errorf("revoke = count %d id %q at %v", store.revokeCount, store.revokedID, store.revokedAt)
	}

	record.RevokedAt = &now
	store.record = record
	if err := manager.Revoke(context.Background(), raw); err != nil {
		t.Fatalf("second Revoke() error = %v", err)
	}
	if store.revokeCount != 1 {
		t.Errorf("revoke count = %d, want idempotent 1", store.revokeCount)
	}
}

func TestNewManagerValidatesDependencies(t *testing.T) {
	t.Parallel()

	store := &memorySessionStore{}
	if _, err := NewManager(nil, []byte(strings.Repeat("p", 32)), time.Hour); err == nil {
		t.Fatal("NewManager() accepted a nil store")
	}
	if _, err := NewManager(store, []byte("short"), time.Hour); err == nil {
		t.Fatal("NewManager() accepted a short pepper")
	}
	if _, err := NewManager(store, []byte(strings.Repeat("p", 32)), 0); err == nil {
		t.Fatal("NewManager() accepted a zero TTL")
	}
}

func activeManager(t *testing.T) (*Manager, *memorySessionStore, string, Session, time.Time) {
	t.Helper()
	pepper := []byte(strings.Repeat("p", 32))
	capability, err := Generate(pepper)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	now := time.Date(2026, 9, 17, 8, 0, 0, 0, time.UTC)
	ipDigest, _ := DigestIP(pepper, netip.MustParseAddr("192.0.2.10"))
	record := Session{
		ID:              capability.SessionID,
		SecretDigest:    capability.Digest,
		InitialIPDigest: ipDigest,
		LastIPDigest:    ipDigest,
		CreatedAt:       now.Add(-time.Hour),
		LastSeenAt:      now.Add(-time.Minute),
		ExpiresAt:       now.Add(time.Hour),
	}
	store := &memorySessionStore{record: record}
	manager, err := NewManager(store, pepper, 48*time.Hour)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	manager.now = func() time.Time { return now }
	return manager, store, capability.Raw, record, now
}

func capabilityWithWrongSecret(_ *memorySessionStore, record *Session, _ string) string {
	other, _ := Generate([]byte(strings.Repeat("p", 32)))
	_, encodedSecret, _ := strings.Cut(other.Raw, ".")
	return record.ID + "." + encodedSecret
}

type memorySessionStore struct {
	record           Session
	created          Session
	getError         error
	touchError       error
	touchCount       int
	touchedID        string
	touchedIP        [sha256.Size]byte
	touchedAt        time.Time
	revokeCount      int
	revokedID        string
	revokedAt        time.Time
	replaceCreatedID string
}

func (store *memorySessionStore) Create(_ context.Context, record Session) (Session, error) {
	store.created = record
	store.record = record
	if store.replaceCreatedID != "" {
		record.ID = store.replaceCreatedID
	}
	return record, nil
}

func (store *memorySessionStore) Get(_ context.Context, id string) (Session, error) {
	if store.getError != nil {
		return Session{}, store.getError
	}
	if id != store.record.ID {
		return Session{}, ErrSessionNotFound
	}
	return store.record, nil
}

func (store *memorySessionStore) TouchActive(_ context.Context, id string, ip [sha256.Size]byte, at time.Time) error {
	store.touchCount++
	store.touchedID = id
	store.touchedIP = ip
	store.touchedAt = at
	return store.touchError
}

func (store *memorySessionStore) Revoke(_ context.Context, id string, at time.Time) error {
	store.revokeCount++
	store.revokedID = id
	store.revokedAt = at
	return nil
}
