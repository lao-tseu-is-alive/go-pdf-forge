package job

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

func TestManagerCreateBuildsQueuedSeed(t *testing.T) {
	t.Parallel()

	store := &memoryStore{}
	manager, err := NewManager(store, 75*1024*1024, 48*time.Hour, 3)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	now := time.Date(2026, 9, 24, 8, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return now }
	manager.random = bytes.NewReader(make([]byte, 16))
	owner, _ := NewAuthenticatedOwner(42)
	creator := Creator{ExternalID: 31415, Login: "cgil", Name: "Carlos Gil", Email: "carlos@example.test"}

	created, err := manager.Create(context.Background(), CreateRequest{
		UploadID: "11111111-1111-4111-8111-111111111111",
		Owner:    owner, Creator: creator,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if created.ID != "00000000-0000-4000-8000-000000000000" || created.Status != StatusQueued {
		t.Fatalf("Create() = %#v", created)
	}
	if created.TargetBytes != 75*1024*1024 || created.MaxAttempts != 3 || created.Version != 1 {
		t.Fatalf("processing policy = %#v", created)
	}
	if !created.CreatedAt.Equal(now) || !created.AvailableAt.Equal(now) || !created.ExpiresAt.Equal(now.Add(48*time.Hour)) {
		t.Fatalf("timestamps = created %v available %v expires %v", created.CreatedAt, created.AvailableAt, created.ExpiresAt)
	}
	if store.created.Owner != owner || store.created.Creator != creator {
		t.Fatalf("persisted owner snapshot = %#v %#v", store.created.Owner, store.created.Creator)
	}
}

func TestManagerRejectsInvalidPoliciesAndIdentity(t *testing.T) {
	t.Parallel()

	store := &memoryStore{}
	for _, test := range []struct {
		name        string
		targetBytes int64
		retention   time.Duration
		attempts    int32
	}{
		{name: "target", retention: time.Hour, attempts: 1},
		{name: "retention", targetBytes: 1, attempts: 1},
		{name: "attempts", targetBytes: 1, retention: time.Hour},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := NewManager(store, test.targetBytes, test.retention, test.attempts); err == nil {
				t.Fatal("NewManager() accepted invalid policy")
			}
		})
	}
	if _, err := NewManager(nil, 1, time.Hour, 1); err == nil {
		t.Fatal("NewManager() accepted nil store")
	}

	manager, _ := NewManager(store, 1, time.Hour, 1)
	owner, _ := NewAuthenticatedOwner(42)
	if _, err := manager.Create(context.Background(), CreateRequest{
		UploadID: "11111111-1111-4111-8111-111111111111", Owner: owner,
	}); err == nil {
		t.Fatal("Create() accepted missing authenticated identity")
	}
	anonymous, _ := NewAnonymousOwner("22222222-2222-4222-8222-222222222222")
	if _, err := manager.Create(context.Background(), CreateRequest{
		UploadID: "11111111-1111-4111-8111-111111111111", Owner: anonymous,
		Creator: Creator{ExternalID: 1, Login: "forbidden", Name: "Forbidden"},
	}); err == nil {
		t.Fatal("Create() accepted anonymous identity snapshot")
	}
}

func TestOwnerValidation(t *testing.T) {
	t.Parallel()

	if _, err := NewAuthenticatedOwner(0); err == nil {
		t.Fatal("NewAuthenticatedOwner() accepted zero")
	}
	if _, err := NewAnonymousOwner("invalid"); err == nil {
		t.Fatal("NewAnonymousOwner() accepted malformed UUID")
	}
	owner, err := NewAnonymousOwner("11111111-1111-4111-8111-111111111111")
	if err != nil || owner.Kind != OwnerAnonymous {
		t.Fatalf("NewAnonymousOwner() = %#v, %v", owner, err)
	}
}

func TestManagerDelegatesOwnerScopedOperations(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 24, 9, 0, 0, 0, time.UTC)
	wantErr := errors.New("store failure")
	store := &memoryStore{operationErr: wantErr}
	manager, _ := NewManager(store, 1, time.Hour, 1)
	manager.now = func() time.Time { return now }
	owner, _ := NewAuthenticatedOwner(42)
	jobID := "11111111-1111-4111-8111-111111111111"

	if _, err := manager.Get(context.Background(), jobID, owner); !errors.Is(err, wantErr) {
		t.Fatalf("Get() error = %v", err)
	}
	if _, err := manager.Cancel(context.Background(), jobID, owner); !errors.Is(err, wantErr) {
		t.Fatalf("Cancel() error = %v", err)
	}
	if !store.observedAt.Equal(now) {
		t.Fatalf("Cancel() observedAt = %v", store.observedAt)
	}
	if err := manager.Delete(context.Background(), jobID, owner); !errors.Is(err, wantErr) {
		t.Fatalf("Delete() error = %v", err)
	}
	if !store.observedAt.Equal(now) {
		t.Fatalf("Delete() observedAt = %v", store.observedAt)
	}
}

type memoryStore struct {
	created      Job
	observedAt   time.Time
	operationErr error
}

func (store *memoryStore) Create(_ context.Context, seed Job) (Job, error) {
	store.created = seed
	return seed, store.operationErr
}

func (store *memoryStore) Get(context.Context, string, Owner) (Job, error) {
	return Job{}, store.operationErr
}

func (store *memoryStore) Cancel(_ context.Context, _ string, _ Owner, observedAt time.Time) (Job, error) {
	store.observedAt = observedAt
	return Job{}, store.operationErr
}

func (store *memoryStore) Delete(_ context.Context, _ string, _ Owner, observedAt time.Time) error {
	store.observedAt = observedAt
	return store.operationErr
}
