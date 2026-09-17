package anonymous

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestPostgresSessionStoreCreateUsesOnlyDerivedValues(t *testing.T) {
	t.Parallel()

	record := validSessionRecord(t)
	var query string
	var arguments []any
	database := &fakeSessionDatabase{queryRow: func(_ context.Context, sql string, args ...any) pgx.Row {
		query = sql
		arguments = args
		return fakeSessionRow{record: record}
	}}
	store, err := NewPostgresSessionStore(database)
	if err != nil {
		t.Fatalf("NewPostgresSessionStore() error = %v", err)
	}
	created, err := store.Create(context.Background(), record)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if created.ID != record.ID || !strings.Contains(query, "INSERT INTO anonymous_session") {
		t.Errorf("Create() = %#v, query = %q", created, query)
	}
	if len(arguments) != 7 {
		t.Fatalf("Create() arguments = %d, want 7", len(arguments))
	}
	for index, argument := range arguments {
		if text, ok := argument.(string); ok && strings.Contains(text, ".") {
			t.Errorf("argument %d resembles a raw capability: %q", index, text)
		}
	}
}

func TestPostgresSessionStoreMapsLookupAndConflictErrors(t *testing.T) {
	t.Parallel()

	conflictDatabase := &fakeSessionDatabase{queryRow: func(context.Context, string, ...any) pgx.Row {
		return fakeSessionRow{err: &pgconn.PgError{Code: "23505"}}
	}}
	store, _ := NewPostgresSessionStore(conflictDatabase)
	if _, err := store.Create(context.Background(), validSessionRecord(t)); !errors.Is(err, ErrSessionConflict) {
		t.Fatalf("Create() error = %v, want conflict", err)
	}

	missingDatabase := &fakeSessionDatabase{queryRow: func(context.Context, string, ...any) pgx.Row {
		return fakeSessionRow{err: pgx.ErrNoRows}
	}}
	store, _ = NewPostgresSessionStore(missingDatabase)
	if _, err := store.Get(context.Background(), validSessionRecord(t).ID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("Get() error = %v, want not found", err)
	}
}

func TestPostgresSessionStoreRejectsMalformedPersistentDigest(t *testing.T) {
	t.Parallel()

	record := validSessionRecord(t)
	database := &fakeSessionDatabase{queryRow: func(context.Context, string, ...any) pgx.Row {
		return fakeSessionRow{record: record, secretDigest: []byte("short")}
	}}
	store, _ := NewPostgresSessionStore(database)
	if _, err := store.Get(context.Background(), record.ID); err == nil || !strings.Contains(err.Error(), "lookup failed") {
		t.Fatalf("Get() error = %v", err)
	}
}

func TestPostgresSessionStoreTouchAndRevokeAreConditionalAndIdempotent(t *testing.T) {
	t.Parallel()

	record := validSessionRecord(t)
	now := record.CreatedAt.Add(time.Minute)
	var statements []string
	database := &fakeSessionDatabase{exec: func(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
		statements = append(statements, sql)
		return pgconn.NewCommandTag("UPDATE 1"), nil
	}}
	store, _ := NewPostgresSessionStore(database)
	if err := store.TouchActive(context.Background(), record.ID, record.LastIPDigest, now); err != nil {
		t.Fatalf("TouchActive() error = %v", err)
	}
	if err := store.Revoke(context.Background(), record.ID, now); err != nil {
		t.Fatalf("Revoke() error = %v", err)
	}
	if !strings.Contains(statements[0], "revoked_at IS NULL") || !strings.Contains(statements[0], "expires_at >") {
		t.Errorf("touch query lacks active predicates: %s", statements[0])
	}
	if !strings.Contains(statements[0], "GREATEST(last_seen_at") {
		t.Errorf("touch query can move last_seen_at backwards: %s", statements[0])
	}
	if !strings.Contains(statements[1], "COALESCE(revoked_at") {
		t.Errorf("revoke query is not idempotent: %s", statements[1])
	}

	store.database = &fakeSessionDatabase{exec: func(context.Context, string, ...any) (pgconn.CommandTag, error) {
		return pgconn.NewCommandTag("UPDATE 0"), nil
	}}
	if err := store.TouchActive(context.Background(), record.ID, record.LastIPDigest, now); !errors.Is(err, ErrSessionInactive) {
		t.Fatalf("TouchActive() error = %v, want inactive", err)
	}
	if err := store.Revoke(context.Background(), record.ID, now); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("Revoke() error = %v, want not found", err)
	}
}

func TestNewPostgresStoreAndCreateValidateInput(t *testing.T) {
	t.Parallel()

	if _, err := NewPostgresSessionStore(nil); err == nil {
		t.Fatal("NewPostgresSessionStore() accepted nil")
	}
	store, _ := NewPostgresSessionStore(&fakeSessionDatabase{})
	record := validSessionRecord(t)
	record.ExpiresAt = record.CreatedAt
	if _, err := store.Create(context.Background(), record); err == nil {
		t.Fatal("Create() accepted non-positive lifetime")
	}
}

func validSessionRecord(t *testing.T) Session {
	t.Helper()
	capability, err := Generate([]byte(strings.Repeat("p", 32)))
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	createdAt := time.Date(2026, 9, 17, 8, 0, 0, 0, time.UTC)
	return Session{
		ID:              capability.SessionID,
		SecretDigest:    capability.Digest,
		InitialIPDigest: capability.Digest,
		LastIPDigest:    capability.Digest,
		CreatedAt:       createdAt,
		LastSeenAt:      createdAt,
		ExpiresAt:       createdAt.Add(48 * time.Hour),
	}
}

type fakeSessionDatabase struct {
	exec     func(context.Context, string, ...any) (pgconn.CommandTag, error)
	queryRow func(context.Context, string, ...any) pgx.Row
}

func (database *fakeSessionDatabase) Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error) {
	if database.exec == nil {
		return pgconn.CommandTag{}, errors.New("unexpected Exec")
	}
	return database.exec(ctx, sql, arguments...)
}

func (database *fakeSessionDatabase) QueryRow(ctx context.Context, sql string, arguments ...any) pgx.Row {
	if database.queryRow == nil {
		return fakeSessionRow{err: errors.New("unexpected QueryRow")}
	}
	return database.queryRow(ctx, sql, arguments...)
}

type fakeSessionRow struct {
	record       Session
	secretDigest []byte
	err          error
}

func (row fakeSessionRow) Scan(destinations ...any) error {
	if row.err != nil {
		return row.err
	}
	if len(destinations) != 8 {
		return errors.New("unexpected scan destination count")
	}
	secretDigest := row.secretDigest
	if secretDigest == nil {
		secretDigest = row.record.SecretDigest[:]
	}
	*(destinations[0].(*string)) = row.record.ID
	*(destinations[1].(*[]byte)) = append([]byte(nil), secretDigest...)
	*(destinations[2].(*[]byte)) = append([]byte(nil), row.record.InitialIPDigest[:]...)
	*(destinations[3].(*[]byte)) = append([]byte(nil), row.record.LastIPDigest[:]...)
	*(destinations[4].(*time.Time)) = row.record.CreatedAt
	*(destinations[5].(*time.Time)) = row.record.LastSeenAt
	*(destinations[6].(*time.Time)) = row.record.ExpiresAt
	*(destinations[7].(**time.Time)) = row.record.RevokedAt
	return nil
}
