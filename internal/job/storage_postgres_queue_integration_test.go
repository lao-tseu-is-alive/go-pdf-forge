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

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/lao-tseu-is-alive/go-pdf-forge/internal/config"
	"github.com/lao-tseu-is-alive/go-pdf-forge/internal/database"
	"github.com/lao-tseu-is-alive/go-pdf-forge/internal/upload"
)

func TestPostgresQueueClaimsLeasesTransitionsAndRecovers(t *testing.T) {
	if os.Getenv("GPF_POSTGRES_TESTS") != "1" {
		t.Skip("set GPF_POSTGRES_TESTS=1 to run PostgreSQL integration tests")
	}

	databaseConfig, err := config.LoadDatabase(os.LookupEnv)
	if err != nil {
		t.Fatalf("LoadDatabase() error = %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	pool, err := database.Open(context.Background(), databaseConfig, "job-queue-integration-test", logger)
	if err != nil {
		t.Fatalf("database.Open() error = %v", err)
	}
	t.Cleanup(pool.Close)

	store, err := NewPostgresStore(pool)
	if err != nil {
		t.Fatalf("NewPostgresStore() error = %v", err)
	}
	manager, err := NewManager(store, 75*1024*1024, 48*time.Hour, 3)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	owner, _ := NewAuthenticatedOwner(515151)
	first := createQueueIntegrationJob(t, pool, manager, owner, "queue-first.pdf")
	second := createQueueIntegrationJob(t, pool, manager, owner, "queue-second.pdf")

	queueA, _ := NewQueue(store, "integration-worker-a", time.Minute, 0, 10)
	queueB, _ := NewQueue(store, "integration-worker-b", time.Minute, 0, 10)
	claimedA, err := queueA.Claim(context.Background())
	if err != nil {
		t.Fatalf("queueA Claim() error = %v", err)
	}
	claimedB, err := queueB.Claim(context.Background())
	if err != nil {
		t.Fatalf("queueB Claim() error = %v", err)
	}
	if claimedA.ID == claimedB.ID || !containsJobID([]Job{first, second}, claimedA.ID) || !containsJobID([]Job{first, second}, claimedB.ID) {
		t.Fatalf("claimed distinct jobs = %s and %s; created %s and %s", claimedA.ID, claimedB.ID, first.ID, second.ID)
	}
	if _, err := queueA.Claim(context.Background()); !errors.Is(err, ErrNoJobAvailable) {
		t.Fatalf("Claim(empty) error = %v", err)
	}

	if _, err := queueB.Heartbeat(context.Background(), claimedA.ID); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("Heartbeat(other worker) error = %v", err)
	}
	heartbeatA, err := queueA.Heartbeat(context.Background(), claimedA.ID)
	if err != nil || heartbeatA.Version <= claimedA.Version {
		t.Fatalf("Heartbeat() = %#v, %v", heartbeatA, err)
	}
	optimizing, err := queueA.Transition(context.Background(), Transition{
		JobID: claimedA.ID, ExpectedStatus: StatusAnalyzing, NextStatus: StatusOptimizing,
		ProgressPercent: 20, ProgressMessage: "Optimizing",
	})
	if err != nil || optimizing.Status != StatusOptimizing {
		t.Fatalf("Transition(optimizing) = %#v, %v (cause %v)", optimizing, err, errors.Unwrap(err))
	}
	if _, err := manager.Cancel(context.Background(), claimedA.ID, owner); err != nil {
		t.Fatalf("Cancel(active) error = %v", err)
	}
	if _, err := queueA.Heartbeat(context.Background(), claimedA.ID); !errors.Is(err, ErrCancellationRequested) {
		t.Fatalf("Heartbeat(cancel requested) error = %v", err)
	}
	cancelled, err := queueA.Transition(context.Background(), Transition{
		JobID: claimedA.ID, ExpectedStatus: StatusOptimizing, NextStatus: StatusCancelled,
		ProgressPercent: optimizing.ProgressPercent, ProgressMessage: "Cancelled",
	})
	if err != nil || cancelled.Status != StatusCancelled || cancelled.LeaseOwner != "" {
		t.Fatalf("Transition(cancelled) = %#v, %v", cancelled, err)
	}

	forceExpiredLease(t, pool, claimedB.ID)
	recovered, err := queueA.RecoverExpired(context.Background())
	if err != nil || recovered.Requeued != 1 {
		t.Fatalf("RecoverExpired(first) = %#v, %v", recovered, err)
	}
	retried, err := queueB.Claim(context.Background())
	if err != nil || retried.ID != claimedB.ID || retried.AttemptCount != 2 {
		t.Fatalf("Claim(retry 2) = %#v, %v", retried, err)
	}
	forceExpiredLease(t, pool, claimedB.ID)
	recovered, err = queueA.RecoverExpired(context.Background())
	if err != nil || recovered.Requeued != 1 {
		t.Fatalf("RecoverExpired(second) = %#v, %v", recovered, err)
	}
	retried, err = queueB.Claim(context.Background())
	if err != nil || retried.AttemptCount != 3 {
		t.Fatalf("Claim(retry 3) = %#v, %v", retried, err)
	}
	forceExpiredLease(t, pool, claimedB.ID)
	recovered, err = queueA.RecoverExpired(context.Background())
	if err != nil || recovered.Failed != 1 {
		t.Fatalf("RecoverExpired(exhausted) = %#v, %v", recovered, err)
	}
	failed, err := manager.Get(context.Background(), claimedB.ID, owner)
	if err != nil || failed.Status != StatusFailed || failed.ErrorCode != "lease_attempts_exhausted" || failed.LeaseOwner != "" {
		t.Fatalf("Get(exhausted) = %#v, %v", failed, err)
	}
}

func createQueueIntegrationJob(t *testing.T, pool *pgxpool.Pool, manager *Manager, owner Owner, filename string) Job {
	t.Helper()
	uploadStore, err := upload.NewPostgresStore(pool)
	if err != nil {
		t.Fatalf("upload.NewPostgresStore() error = %v", err)
	}
	uploadManager, err := upload.NewManager(uploadStore, 64, 4, time.Hour)
	if err != nil {
		t.Fatalf("upload.NewManager() error = %v", err)
	}
	uploadOwner, _ := upload.NewAuthenticatedOwner(owner.UserID)
	createdUpload, err := uploadManager.Start(context.Background(), uploadOwner, filename, "application/pdf", 4)
	if err != nil {
		t.Fatalf("upload Start() error = %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := pool.Exec(cleanupCtx, `DELETE FROM pdf_job WHERE upload_id = $1`, createdUpload.ID); err != nil {
			t.Errorf("delete queue integration job: %v", err)
		}
		if _, err := pool.Exec(cleanupCtx, `DELETE FROM upload_session WHERE id = $1`, createdUpload.ID); err != nil {
			t.Errorf("delete queue integration upload: %v", err)
		}
	})
	partDigest := upload.SHA256(sha256.Sum256([]byte("part")))
	if _, err := uploadManager.RecordPart(context.Background(), uploadOwner, createdUpload.ID, 0, 4, partDigest, "backend-part"); err != nil {
		t.Fatalf("upload RecordPart() error = %v", err)
	}
	if _, err := uploadManager.BeginCommit(context.Background(), uploadOwner, createdUpload.ID); err != nil {
		t.Fatalf("upload BeginCommit() error = %v", err)
	}
	completeDigest := upload.SHA256(sha256.Sum256([]byte("complete-" + filename)))
	if _, err := uploadManager.Complete(context.Background(), uploadOwner, createdUpload.ID, completeDigest); err != nil {
		t.Fatalf("upload Complete() error = %v", err)
	}
	created, err := manager.Create(context.Background(), CreateRequest{
		UploadID: createdUpload.ID,
		Owner:    owner,
		Creator: Creator{
			ExternalID: 515151,
			Login:      "queue-integration",
			Name:       "Queue Integration",
			Email:      "queue-integration@example.test",
		},
	})
	if err != nil {
		t.Fatalf("job Create() error = %v", err)
	}
	return created
}

func forceExpiredLease(t *testing.T, pool *pgxpool.Pool, jobID string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
UPDATE pdf_job
SET lease_expires_at = now() - interval '1 second'
WHERE id = $1`, jobID); err != nil {
		t.Fatalf("force expired lease: %v", err)
	}
}

func containsJobID(jobs []Job, jobID string) bool {
	for _, candidate := range jobs {
		if candidate.ID == jobID {
			return true
		}
	}
	return false
}
