package upload

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/lao-tseu-is-alive/go-pdf-forge/internal/config"
	"github.com/lao-tseu-is-alive/go-pdf-forge/internal/testpostgres"
)

func TestPostgresStoreUploadLifecycle(t *testing.T) {
	if os.Getenv("GPF_POSTGRES_TESTS") != "1" {
		t.Skip("set GPF_POSTGRES_TESTS=1 to run PostgreSQL integration tests")
	}

	databaseConfig, err := config.LoadDatabase(os.LookupEnv)
	if err != nil {
		t.Fatalf("LoadDatabase() error = %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	environment, err := testpostgres.Open(context.Background(), databaseConfig, "upload-integration-test", logger)
	if err != nil {
		t.Fatalf("testpostgres.Open() error = %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := environment.Close(cleanupCtx); err != nil {
			t.Errorf("testpostgres.Close() error = %v", err)
		}
	})
	pool := environment.Pool
	store, err := NewPostgresStore(pool)
	if err != nil {
		t.Fatalf("NewPostgresStore() error = %v", err)
	}
	manager, err := NewManager(store, 64, 4, time.Hour)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	owner, _ := NewAuthenticatedOwner(424242)
	created, err := manager.Start(context.Background(), owner, "integration.pdf", "application/pdf", 9)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if _, err := store.Get(context.Background(), created.ID, Owner{Kind: OwnerAuthenticated, UserID: 424243}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(other owner) error = %v", err)
	}
	digests := []SHA256{
		SHA256(sha256.Sum256([]byte("part-zero"))),
		SHA256(sha256.Sum256([]byte("part-one"))),
		SHA256(sha256.Sum256([]byte("part-two"))),
	}
	first, err := manager.RecordPart(context.Background(), owner, created.ID, 0, 4, digests[0], "backend-0")
	if err != nil {
		t.Fatalf("RecordPart(0) error = %v", err)
	}
	retry, err := manager.RecordPart(context.Background(), owner, created.ID, 0, 4, digests[0], "replacement-backend-0")
	if err != nil || !retry.AlreadyPresent || retry.Part.BackendPartID != first.Part.BackendPartID {
		t.Fatalf("RecordPart(0 retry) = %#v, %v", retry, err)
	}
	if _, err := manager.RecordPart(context.Background(), owner, created.ID, 2, 1, digests[2], "backend-2"); !errors.Is(err, ErrPartOutOfOrder) {
		t.Fatalf("RecordPart(gap) error = %v", err)
	}
	if _, err := manager.RecordPart(context.Background(), owner, created.ID, 1, 4, digests[1], "backend-1"); err != nil {
		t.Fatalf("RecordPart(1) error = %v", err)
	}
	if _, err := manager.RecordPart(context.Background(), owner, created.ID, 2, 1, digests[2], "backend-2"); err != nil {
		t.Fatalf("RecordPart(2) error = %v", err)
	}

	plan, err := manager.BeginCommit(context.Background(), owner, created.ID)
	if err != nil {
		t.Fatalf("BeginCommit() error = %v", err)
	}
	if plan.Session.Status != StatusCommitting || len(plan.Parts) != 3 {
		t.Fatalf("BeginCommit() = %#v", plan)
	}
	completeDigest := SHA256(sha256.Sum256([]byte("complete-object")))
	completed, err := manager.Complete(context.Background(), owner, created.ID, completeDigest)
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if completed.Status != StatusCommitted || completed.CommittedSHA256 == nil || *completed.CommittedSHA256 != completeDigest {
		t.Fatalf("Complete() = %#v", completed)
	}
	if _, err := manager.Complete(context.Background(), owner, created.ID, completeDigest); err != nil {
		t.Fatalf("Complete(retry) error = %v", err)
	}
	if _, err := manager.Abort(context.Background(), owner, created.ID); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("Abort(committed) error = %v", err)
	}
}
