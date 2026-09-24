package anonymous

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	"net/netip"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lao-tseu-is-alive/go-pdf-forge/internal/config"
	"github.com/lao-tseu-is-alive/go-pdf-forge/internal/testpostgres"
)

func TestPostgresQuotaStoreConcurrentLimit(t *testing.T) {
	if os.Getenv("GPF_POSTGRES_TESTS") != "1" {
		t.Skip("set GPF_POSTGRES_TESTS=1 to run PostgreSQL integration tests")
	}

	databaseConfig, err := config.LoadDatabase(os.LookupEnv)
	if err != nil {
		t.Fatalf("LoadDatabase() error = %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	environment, err := testpostgres.Open(context.Background(), databaseConfig, "quota-integration-test", logger)
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

	pepper := make([]byte, 32)
	if _, err := rand.Read(pepper); err != nil {
		t.Fatalf("rand.Read() error = %v", err)
	}
	capability, err := Generate(pepper)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	clientIP := netip.MustParseAddr("192.0.2.203")
	ipDigest, err := DigestIP(pepper, clientIP)
	if err != nil {
		t.Fatalf("DigestIP() error = %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if _, err := pool.Exec(context.Background(), `
INSERT INTO anonymous_session (
    id, secret_hash, initial_ip_hash, last_ip_hash,
    created_at, last_seen_at, expires_at
) VALUES ($1, $2, $3, $3, $4, $4, $5)`,
		capability.SessionID, capability.Digest[:], ipDigest[:], now, now.Add(time.Hour)); err != nil {
		t.Fatalf("insert test session: %v", err)
	}
	limits := QuotaLimits{
		Window:            time.Hour,
		SessionsPerIP:     3,
		UploadsPerSession: 5,
		UploadsPerIP:      100,
		JobsPerSession:    100,
		JobsPerIP:         100,
		BytesPerSession:   1024,
		BytesPerIP:        4096,
	}
	store, err := NewPostgresQuotaStore(pool)
	if err != nil {
		t.Fatalf("NewPostgresQuotaStore() error = %v", err)
	}
	manager, err := NewQuotaManager(store, pepper, limits)
	if err != nil {
		t.Fatalf("NewQuotaManager() error = %v", err)
	}
	manager.now = func() time.Time { return now }

	for attempt := int64(0); attempt < limits.SessionsPerIP; attempt++ {
		if _, err := manager.ConsumeSessionCreation(context.Background(), clientIP); err != nil {
			t.Fatalf("ConsumeSessionCreation() attempt %d: %v", attempt, err)
		}
	}
	if _, err := manager.ConsumeSessionCreation(context.Background(), clientIP); !quotaExceededBy(err, QuotaScopeIP, QuotaMetricSessions) {
		t.Fatalf("ConsumeSessionCreation() after limit = %v", err)
	}

	const attempts = 20
	var accepted atomic.Int64
	var rejected atomic.Int64
	errorsFound := make(chan error, attempts)
	var wait sync.WaitGroup
	for range attempts {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := manager.ConsumeUploadStart(context.Background(), capability.SessionID, clientIP)
			switch {
			case err == nil:
				accepted.Add(1)
			case quotaExceededBy(err, QuotaScopeSession, QuotaMetricUploads):
				rejected.Add(1)
			default:
				errorsFound <- err
			}
		}()
	}
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Errorf("concurrent quota consumption: %v", err)
	}
	if accepted.Load() != limits.UploadsPerSession || rejected.Load() != attempts-limits.UploadsPerSession {
		t.Fatalf("concurrent results accepted=%d rejected=%d", accepted.Load(), rejected.Load())
	}

	var sessionUploads int64
	if err := pool.QueryRow(context.Background(), `
SELECT COALESCE(SUM(uploads_started), 0)
FROM anonymous_usage
WHERE session_id = $1`, capability.SessionID).Scan(&sessionUploads); err != nil {
		t.Fatalf("read session upload counter: %v", err)
	}
	var ipSessions int64
	var ipUploads int64
	if err := pool.QueryRow(context.Background(), `
SELECT COALESCE(SUM(sessions_created), 0), COALESCE(SUM(uploads_started), 0)
FROM anonymous_ip_usage
WHERE ip_hash = $1`, ipDigest[:]).Scan(&ipSessions, &ipUploads); err != nil {
		t.Fatalf("read IP counters: %v", err)
	}
	if sessionUploads != limits.UploadsPerSession || ipUploads != limits.UploadsPerSession || ipSessions != limits.SessionsPerIP {
		t.Fatalf("persisted counters session_uploads=%d ip_uploads=%d ip_sessions=%d", sessionUploads, ipUploads, ipSessions)
	}
}

func quotaExceededBy(err error, scope QuotaScope, metric QuotaMetric) bool {
	var exceeded *QuotaExceededError
	return errors.As(err, &exceeded) && exceeded.Scope == scope && exceeded.Metric == metric
}
