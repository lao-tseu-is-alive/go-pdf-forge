package job

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestPostgresStoreCreateCopiesCommittedUploadAndIsRetrySafe(t *testing.T) {
	t.Parallel()

	seed := testJobSeed(t)
	stored := materializeJob(t, seed)
	var query string
	database := &fakeJobDatabase{queryRow: func(_ context.Context, sql string, _ ...any) pgx.Row {
		query = sql
		return jobRow(stored)
	}}
	store, err := NewPostgresStore(database)
	if err != nil {
		t.Fatalf("NewPostgresStore() error = %v", err)
	}
	created, err := store.Create(context.Background(), seed)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if created.ID != seed.ID || created.InputObjectKey == "" || created.InputBytes != 9 {
		t.Fatalf("Create() = %#v", created)
	}
	for _, fragment := range []string{
		"FROM upload_session", "status = 'committed'", "received_bytes = declared_size",
		"owner_user_id IS NOT DISTINCT FROM", "ON CONFLICT (upload_id) DO UPDATE",
	} {
		if !strings.Contains(query, fragment) {
			t.Errorf("Create() SQL lacks %q: %s", fragment, query)
		}
	}

	database.queryRow = func(context.Context, string, ...any) pgx.Row { return jobRow(stored) }
	retried, err := store.Create(context.Background(), seed)
	if err != nil || retried.ID != created.ID {
		t.Fatalf("Create(retry) = %#v, %v", retried, err)
	}
}

func TestPostgresStoreCreateRejectsUnavailableUploadAndDeletedRetry(t *testing.T) {
	t.Parallel()

	seed := testJobSeed(t)
	store, _ := NewPostgresStore(&fakeJobDatabase{queryRow: func(context.Context, string, ...any) pgx.Row {
		return errorJobRow(pgx.ErrNoRows)
	}})
	if _, err := store.Create(context.Background(), seed); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Create(unavailable upload) error = %v", err)
	}

	stored := materializeJob(t, seed)
	deletedAt := stored.CreatedAt.Add(time.Minute)
	stored.DeletionRequestedAt = &deletedAt
	store, _ = NewPostgresStore(&fakeJobDatabase{queryRow: func(context.Context, string, ...any) pgx.Row {
		return jobRow(stored)
	}})
	if _, err := store.Create(context.Background(), seed); !errors.Is(err, ErrConflict) {
		t.Fatalf("Create(deleted retry) error = %v", err)
	}

	conflict := &pgconn.PgError{Code: "23505", ConstraintName: "pdf_job_pkey"}
	store, _ = NewPostgresStore(&fakeJobDatabase{queryRow: func(context.Context, string, ...any) pgx.Row {
		return errorJobRow(conflict)
	}})
	if _, err := store.Create(context.Background(), seed); !errors.Is(err, ErrConflict) {
		t.Fatalf("Create(identifier collision) error = %v", err)
	}
}

func TestPostgresStoreGetIsOwnerScopedAndHidesDeletion(t *testing.T) {
	t.Parallel()

	stored := materializeJob(t, testJobSeed(t))
	var query string
	database := &fakeJobDatabase{queryRow: func(_ context.Context, sql string, _ ...any) pgx.Row {
		query = sql
		return jobRow(stored)
	}}
	store, _ := NewPostgresStore(database)
	got, err := store.Get(context.Background(), stored.ID, stored.Owner)
	if err != nil || got.ID != stored.ID {
		t.Fatalf("Get() = %#v, %v", got, err)
	}
	for _, fragment := range []string{
		"owner_user_id IS NOT DISTINCT FROM", "anonymous_session_id IS NOT DISTINCT FROM",
		"deletion_requested_at IS NULL",
	} {
		if !strings.Contains(query, fragment) {
			t.Errorf("Get() SQL lacks %q: %s", fragment, query)
		}
	}

	database.queryRow = func(context.Context, string, ...any) pgx.Row { return errorJobRow(pgx.ErrNoRows) }
	if _, err := store.Get(context.Background(), stored.ID, stored.Owner); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(hidden) error = %v", err)
	}
}

func TestPostgresStoreCancelIsAtomicAndIdempotent(t *testing.T) {
	t.Parallel()

	stored := materializeJob(t, testJobSeed(t))
	observedAt := stored.CreatedAt.Add(time.Minute)
	cancelled := stored
	cancelled.Status = StatusCancelled
	cancelled.CancelRequestedAt = &observedAt
	cancelled.CompletedAt = &observedAt
	cancelled.UpdatedAt = observedAt
	cancelled.Version++
	var query string
	database := &fakeJobDatabase{queryRow: func(_ context.Context, sql string, _ ...any) pgx.Row {
		query = sql
		return jobRow(cancelled)
	}}
	store, _ := NewPostgresStore(database)
	got, err := store.Cancel(context.Background(), stored.ID, stored.Owner, observedAt)
	if err != nil || got.Status != StatusCancelled || got.Version != 2 {
		t.Fatalf("Cancel() = %#v, %v", got, err)
	}
	for _, fragment := range []string{
		"WHEN status = 'queued' THEN 'cancelled'", "COALESCE(cancel_requested_at, GREATEST($5, created_at))",
		"version = version + CASE", "deletion_requested_at IS NULL",
	} {
		if !strings.Contains(query, fragment) {
			t.Errorf("Cancel() SQL lacks %q: %s", fragment, query)
		}
	}

	database.queryRow = func(context.Context, string, ...any) pgx.Row { return jobRow(cancelled) }
	retried, err := store.Cancel(context.Background(), stored.ID, stored.Owner, observedAt.Add(time.Minute))
	if err != nil || retried.Version != cancelled.Version {
		t.Fatalf("Cancel(retry) = %#v, %v", retried, err)
	}
}

func TestPostgresStoreDeleteTombstonesIdempotently(t *testing.T) {
	t.Parallel()

	stored := materializeJob(t, testJobSeed(t))
	var query string
	database := &fakeJobDatabase{queryRow: func(_ context.Context, sql string, _ ...any) pgx.Row {
		query = sql
		return stringRow(stored.ID)
	}}
	store, _ := NewPostgresStore(database)
	if err := store.Delete(context.Background(), stored.ID, stored.Owner, stored.CreatedAt.Add(time.Minute)); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	for _, fragment := range []string{
		"deletion_requested_at = COALESCE", "expires_at = LEAST", "status = CASE",
		"cancel_requested_at = CASE", "owner_user_id IS NOT DISTINCT FROM",
	} {
		if !strings.Contains(query, fragment) {
			t.Errorf("Delete() SQL lacks %q: %s", fragment, query)
		}
	}
	if err := store.Delete(context.Background(), stored.ID, stored.Owner, stored.CreatedAt.Add(2*time.Minute)); err != nil {
		t.Fatalf("Delete(retry) error = %v", err)
	}

	database.queryRow = func(context.Context, string, ...any) pgx.Row { return errorJobRow(pgx.ErrNoRows) }
	if err := store.Delete(context.Background(), "22222222-2222-4222-8222-222222222222", stored.Owner, observedTime()); err != nil {
		t.Fatalf("Delete(missing or other owner) error = %v", err)
	}
	if err := store.Delete(context.Background(), "invalid", stored.Owner, observedTime()); err != nil {
		t.Fatalf("Delete(invalid missing ID) error = %v", err)
	}
}

func TestPostgresStoreValidationAndSafeErrors(t *testing.T) {
	t.Parallel()

	if _, err := NewPostgresStore(nil); err == nil {
		t.Fatal("NewPostgresStore() accepted nil")
	}
	database := &fakeJobDatabase{}
	store, _ := NewPostgresStore(database)
	if _, err := store.Get(context.Background(), "invalid", Owner{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(invalid) error = %v", err)
	}
	if database.queryCalls != 0 {
		t.Fatal("invalid get reached database")
	}

	cause := errors.New("sensitive database details")
	store, _ = NewPostgresStore(&fakeJobDatabase{queryRow: func(context.Context, string, ...any) pgx.Row {
		return errorJobRow(cause)
	}})
	seed := testJobSeed(t)
	_, err := store.Create(context.Background(), seed)
	if !errors.Is(err, cause) || strings.Contains(err.Error(), cause.Error()) {
		t.Fatalf("storage error = %v", err)
	}
}

func testJobSeed(t *testing.T) Job {
	t.Helper()
	owner, err := NewAuthenticatedOwner(42)
	if err != nil {
		t.Fatalf("NewAuthenticatedOwner() error = %v", err)
	}
	now := time.Date(2026, 9, 24, 8, 0, 0, 0, time.UTC)
	return Job{
		ID:          "11111111-1111-4111-8111-111111111111",
		UploadID:    "22222222-2222-4222-8222-222222222222",
		Owner:       owner,
		Creator:     Creator{ExternalID: 31415, Login: "cgil", Name: "Carlos Gil", Email: "carlos@example.test"},
		TargetBytes: 75 * 1024 * 1024, Status: StatusQueued, MaxAttempts: 3,
		AvailableAt: now, CreatedAt: now, UpdatedAt: now,
		ExpiresAt: now.Add(48 * time.Hour), Version: 1,
	}
}

func materializeJob(t *testing.T, seed Job) Job {
	t.Helper()
	digest, err := ParseSHA256(strings.Repeat("ab", 32))
	if err != nil {
		t.Fatalf("ParseSHA256() error = %v", err)
	}
	seed.OriginalFilename = "report.pdf"
	seed.InputObjectKey = "uploads/22222222-2222-4222-8222-222222222222/source"
	seed.InputSHA256 = digest
	seed.InputBytes = 9
	return seed
}

func observedTime() time.Time {
	return time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
}

type fakeJobDatabase struct {
	queryRow   func(context.Context, string, ...any) pgx.Row
	queryCalls int
}

func (database *fakeJobDatabase) QueryRow(ctx context.Context, sql string, arguments ...any) pgx.Row {
	database.queryCalls++
	if database.queryRow == nil {
		return errorJobRow(errors.New("unexpected database QueryRow"))
	}
	return database.queryRow(ctx, sql, arguments...)
}

type scanJobRow func(...any) error

func (row scanJobRow) Scan(destinations ...any) error {
	return row(destinations...)
}

func errorJobRow(err error) pgx.Row {
	return scanJobRow(func(...any) error { return err })
}

func stringRow(value string) pgx.Row {
	return scanJobRow(func(destinations ...any) error {
		if len(destinations) != 1 {
			return errors.New("unexpected string destination count")
		}
		*(destinations[0].(*string)) = value
		return nil
	})
}

func jobRow(stored Job) pgx.Row {
	return scanJobRow(func(destinations ...any) error {
		if len(destinations) != 39 {
			return errors.New("unexpected job destination count")
		}
		*(destinations[0].(*string)) = stored.ID
		*(destinations[1].(*string)) = stored.UploadID
		*(destinations[2].(*string)) = string(stored.Owner.Kind)
		if stored.Owner.Kind == OwnerAuthenticated {
			value := stored.Owner.UserID
			*(destinations[3].(**int64)) = &value
		} else {
			value := stored.Owner.AnonymousSessionID
			*(destinations[4].(**string)) = &value
		}
		if stored.Creator.ExternalID != 0 {
			value := stored.Creator.ExternalID
			*(destinations[5].(**int64)) = &value
		}
		setOptionalString(destinations[6], stored.Creator.Login)
		setOptionalString(destinations[7], stored.Creator.Name)
		setOptionalString(destinations[8], stored.Creator.Email)
		*(destinations[9].(*string)) = stored.OriginalFilename
		*(destinations[10].(*string)) = stored.InputObjectKey
		*(destinations[11].(*string)) = stored.InputSHA256.String()
		*(destinations[12].(*int64)) = stored.InputBytes
		setOptionalString(destinations[13], stored.OutputObjectKey)
		if stored.OutputSHA256 != nil {
			value := stored.OutputSHA256.String()
			*(destinations[14].(**string)) = &value
		}
		*(destinations[15].(**int64)) = stored.OutputBytes
		*(destinations[16].(**int32)) = stored.PageCount
		setOptionalString(destinations[17], stored.SelectedProfile)
		*(destinations[18].(*int64)) = stored.TargetBytes
		*(destinations[19].(**bool)) = stored.TargetMet
		*(destinations[20].(*string)) = string(stored.Status)
		*(destinations[21].(*int16)) = stored.ProgressPercent
		*(destinations[22].(*string)) = stored.ProgressMessage
		*(destinations[23].(*int32)) = stored.AttemptCount
		*(destinations[24].(*int32)) = stored.MaxAttempts
		*(destinations[25].(*time.Time)) = stored.AvailableAt
		setOptionalString(destinations[26], stored.LeaseOwner)
		*(destinations[27].(**time.Time)) = stored.LeaseExpiresAt
		*(destinations[28].(**time.Time)) = stored.HeartbeatAt
		*(destinations[29].(**time.Time)) = stored.CancelRequestedAt
		setOptionalString(destinations[30], stored.ErrorCode)
		setOptionalString(destinations[31], stored.ErrorMessage)
		*(destinations[32].(*time.Time)) = stored.CreatedAt
		*(destinations[33].(*time.Time)) = stored.UpdatedAt
		*(destinations[34].(**time.Time)) = stored.StartedAt
		*(destinations[35].(**time.Time)) = stored.CompletedAt
		*(destinations[36].(*time.Time)) = stored.ExpiresAt
		*(destinations[37].(**time.Time)) = stored.DeletionRequestedAt
		*(destinations[38].(*int64)) = stored.Version
		return nil
	})
}

func setOptionalString(destination any, value string) {
	if value == "" {
		return
	}
	copy := value
	*(destination.(**string)) = &copy
}
