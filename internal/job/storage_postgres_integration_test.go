package job

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
	"github.com/lao-tseu-is-alive/go-pdf-forge/internal/database"
	"github.com/lao-tseu-is-alive/go-pdf-forge/internal/upload"
)

func TestPostgresStoreJobLifecycle(t *testing.T) {
	if os.Getenv("GPF_POSTGRES_TESTS") != "1" {
		t.Skip("set GPF_POSTGRES_TESTS=1 to run PostgreSQL integration tests")
	}

	databaseConfig, err := config.LoadDatabase(os.LookupEnv)
	if err != nil {
		t.Fatalf("LoadDatabase() error = %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	pool, err := database.Open(context.Background(), databaseConfig, "job-integration-test", logger)
	if err != nil {
		t.Fatalf("database.Open() error = %v", err)
	}
	t.Cleanup(pool.Close)

	uploadStore, err := upload.NewPostgresStore(pool)
	if err != nil {
		t.Fatalf("upload.NewPostgresStore() error = %v", err)
	}
	uploadManager, err := upload.NewManager(uploadStore, 64, 4, time.Hour)
	if err != nil {
		t.Fatalf("upload.NewManager() error = %v", err)
	}
	uploadOwner, _ := upload.NewAuthenticatedOwner(424242)
	createdUpload, err := uploadManager.Start(context.Background(), uploadOwner, "job-integration.pdf", "application/pdf", 4)
	if err != nil {
		t.Fatalf("upload Start() error = %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := pool.Exec(cleanupCtx, `DELETE FROM pdf_job WHERE upload_id = $1`, createdUpload.ID); err != nil {
			t.Errorf("delete integration job: %v", err)
		}
		if _, err := pool.Exec(cleanupCtx, `DELETE FROM upload_session WHERE id = $1`, createdUpload.ID); err != nil {
			t.Errorf("delete integration upload: %v", err)
		}
	})

	partDigest := upload.SHA256(sha256.Sum256([]byte("part")))
	if _, err := uploadManager.RecordPart(context.Background(), uploadOwner, createdUpload.ID, 0, 4, partDigest, "backend-part"); err != nil {
		t.Fatalf("upload RecordPart() error = %v", err)
	}
	if _, err := uploadManager.BeginCommit(context.Background(), uploadOwner, createdUpload.ID); err != nil {
		t.Fatalf("upload BeginCommit() error = %v", err)
	}
	completeDigest := upload.SHA256(sha256.Sum256([]byte("complete-object")))
	if _, err := uploadManager.Complete(context.Background(), uploadOwner, createdUpload.ID, completeDigest); err != nil {
		t.Fatalf("upload Complete() error = %v", err)
	}

	store, err := NewPostgresStore(pool)
	if err != nil {
		t.Fatalf("NewPostgresStore() error = %v", err)
	}
	manager, err := NewManager(store, 75*1024*1024, 48*time.Hour, 3)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	owner, _ := NewAuthenticatedOwner(424242)
	request := CreateRequest{
		UploadID: createdUpload.ID,
		Owner:    owner,
		Creator: Creator{
			ExternalID: 31415,
			Login:      "job-integration",
			Name:       "Job Integration",
			Email:      "job-integration@example.test",
		},
	}
	created, err := manager.Create(context.Background(), request)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if created.Status != StatusQueued || created.InputBytes != 4 || created.InputSHA256.String() != completeDigest.String() {
		t.Fatalf("Create() = %#v", created)
	}
	retried, err := manager.Create(context.Background(), request)
	if err != nil || retried.ID != created.ID {
		t.Fatalf("Create(retry) = %#v, %v", retried, err)
	}

	otherOwner, _ := NewAuthenticatedOwner(424243)
	if _, err := manager.Get(context.Background(), created.ID, otherOwner); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(other owner) error = %v", err)
	}
	if err := manager.Delete(context.Background(), created.ID, otherOwner); err != nil {
		t.Fatalf("Delete(other owner) error = %v", err)
	}
	if _, err := manager.Get(context.Background(), created.ID, owner); err != nil {
		t.Fatalf("Get(after other-owner delete) error = %v", err)
	}

	cancelled, err := manager.Cancel(context.Background(), created.ID, owner)
	if err != nil || cancelled.Status != StatusCancelled || cancelled.CancelRequestedAt == nil || cancelled.CompletedAt == nil {
		t.Fatalf("Cancel() = %#v, %v", cancelled, err)
	}
	retriedCancellation, err := manager.Cancel(context.Background(), created.ID, owner)
	if err != nil || retriedCancellation.Version != cancelled.Version {
		t.Fatalf("Cancel(retry) = %#v, %v", retriedCancellation, err)
	}
	if err := manager.Delete(context.Background(), created.ID, owner); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := manager.Get(context.Background(), created.ID, owner); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(deleted) error = %v", err)
	}
	if err := manager.Delete(context.Background(), created.ID, owner); err != nil {
		t.Fatalf("Delete(retry) error = %v", err)
	}
}
