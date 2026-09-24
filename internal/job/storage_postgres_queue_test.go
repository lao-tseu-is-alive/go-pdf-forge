package job

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestPostgresStoreClaimUsesSkipLockedAndDurableLease(t *testing.T) {
	t.Parallel()

	queued := materializeJob(t, testJobSeed(t))
	now := queued.CreatedAt.Add(time.Minute)
	claimed := queued
	claimed.Status = StatusAnalyzing
	claimed.AttemptCount = 1
	claimed.LeaseOwner = "worker-a"
	leaseExpiresAt := now.Add(30 * time.Second)
	claimed.LeaseExpiresAt = &leaseExpiresAt
	claimed.HeartbeatAt = &now
	claimed.StartedAt = &now
	claimed.UpdatedAt = now
	claimed.Version++
	var query string
	database := &fakeJobDatabase{queryRow: func(_ context.Context, sql string, _ ...any) pgx.Row {
		query = sql
		return jobRow(claimed)
	}}
	store, _ := NewPostgresStore(database)
	got, err := store.Claim(context.Background(), "worker-a", now, leaseExpiresAt)
	if err != nil || got.Status != StatusAnalyzing || got.LeaseOwner != "worker-a" || got.AttemptCount != 1 {
		t.Fatalf("Claim() = %#v, %v", got, err)
	}
	for _, fragment := range []string{
		"FOR UPDATE SKIP LOCKED", "ORDER BY available_at, created_at, id",
		"attempt_count < max_attempts", "deletion_requested_at IS NULL",
		"SET status = 'analyzing'", "attempt_count = attempt_count + 1",
	} {
		if !strings.Contains(query, fragment) {
			t.Errorf("Claim() SQL lacks %q: %s", fragment, query)
		}
	}

	database.queryRow = func(context.Context, string, ...any) pgx.Row { return errorJobRow(pgx.ErrNoRows) }
	if _, err := store.Claim(context.Background(), "worker-a", now, leaseExpiresAt); !errors.Is(err, ErrNoJobAvailable) {
		t.Fatalf("Claim(empty) error = %v", err)
	}
}

func TestPostgresStoreHeartbeatExtendsOnlyCurrentLease(t *testing.T) {
	t.Parallel()

	active := activeJob(t, "worker-a")
	now := active.CreatedAt.Add(2 * time.Minute)
	newExpiry := now.Add(time.Minute)
	active.LeaseExpiresAt = &newExpiry
	active.HeartbeatAt = &now
	active.UpdatedAt = now
	active.Version++
	var query string
	database := &fakeJobDatabase{queryRow: func(_ context.Context, sql string, _ ...any) pgx.Row {
		query = sql
		return jobRow(active)
	}}
	store, _ := NewPostgresStore(database)
	got, err := store.Heartbeat(context.Background(), active.ID, "worker-a", now, newExpiry)
	if err != nil || got.LeaseExpiresAt == nil || !got.LeaseExpiresAt.Equal(newExpiry) {
		t.Fatalf("Heartbeat() = %#v, %v", got, err)
	}
	for _, fragment := range []string{
		"lease_owner = $2", "lease_expires_at > $3",
		"status IN ('analyzing', 'optimizing', 'validating')",
		"cancel_requested_at IS NULL AND deletion_requested_at IS NULL",
	} {
		if !strings.Contains(query, fragment) {
			t.Errorf("Heartbeat() SQL lacks %q: %s", fragment, query)
		}
	}

	cancellationAt := now.Add(-time.Second)
	active.CancelRequestedAt = &cancellationAt
	database.queryRow = func(context.Context, string, ...any) pgx.Row { return jobRow(active) }
	if returned, err := store.Heartbeat(context.Background(), active.ID, "worker-a", now, newExpiry); !errors.Is(err, ErrCancellationRequested) || returned.CancelRequestedAt == nil {
		t.Fatalf("Heartbeat(cancelled) = %#v, %v", returned, err)
	}

	database.queryRow = func(context.Context, string, ...any) pgx.Row { return errorJobRow(pgx.ErrNoRows) }
	if _, err := store.Heartbeat(context.Background(), active.ID, "worker-b", now, newExpiry); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("Heartbeat(stale worker) error = %v", err)
	}
}

func TestPostgresStoreTransitionUsesExpectedStateAndReleasesTerminalLease(t *testing.T) {
	t.Parallel()

	active := activeJob(t, "worker-a")
	now := active.CreatedAt.Add(2 * time.Minute)
	nextExpiry := now.Add(time.Minute)
	transitioned := active
	transitioned.Status = StatusOptimizing
	transitioned.ProgressPercent = 20
	transitioned.ProgressMessage = "Optimizing"
	transitioned.LeaseExpiresAt = &nextExpiry
	transitioned.HeartbeatAt = &now
	transitioned.UpdatedAt = now
	transitioned.Version++
	var query string
	database := &fakeJobDatabase{queryRow: func(_ context.Context, sql string, _ ...any) pgx.Row {
		query = sql
		return jobRow(transitioned)
	}}
	store, _ := NewPostgresStore(database)
	request := Transition{
		JobID: active.ID, WorkerID: "worker-a",
		ExpectedStatus: StatusAnalyzing, NextStatus: StatusOptimizing,
		ProgressPercent: 20, ProgressMessage: "Optimizing",
		ObservedAt: now, LeaseExpiresAt: nextExpiry,
	}
	got, err := store.Transition(context.Background(), request)
	if err != nil || got.Status != StatusOptimizing || got.ProgressPercent != 20 {
		t.Fatalf("Transition() = %#v, %v", got, err)
	}
	for _, fragment := range []string{
		"status = $3", "lease_owner = $2", "lease_expires_at > $9",
		"progress_percent <= $5", "cancel_requested_at IS NULL",
		"lease_owner = CASE WHEN $11 THEN NULL",
	} {
		if !strings.Contains(query, fragment) {
			t.Errorf("Transition() SQL lacks %q: %s", fragment, query)
		}
	}

	completed := transitioned
	completed.Status = StatusCompleted
	completed.ProgressPercent = 100
	completed.LeaseOwner = ""
	completed.LeaseExpiresAt = nil
	completed.HeartbeatAt = nil
	completed.CompletedAt = &now
	completed.Version++
	database.queryRow = func(context.Context, string, ...any) pgx.Row { return jobRow(completed) }
	request.ExpectedStatus = StatusAnalyzing
	request.NextStatus = StatusCompleted
	request.ProgressPercent = 100
	request.ProgressMessage = "Completed"
	if got, err := store.Transition(context.Background(), request); err != nil || got.LeaseOwner != "" || got.CompletedAt == nil {
		t.Fatalf("Transition(completed) = %#v, %v", got, err)
	}

	database.queryRow = func(context.Context, string, ...any) pgx.Row { return errorJobRow(pgx.ErrNoRows) }
	if _, err := store.Transition(context.Background(), request); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("Transition(stale) error = %v", err)
	}
}

func TestPostgresStoreRecoverExpiredIsBoundedAndClassifiesRows(t *testing.T) {
	t.Parallel()

	var query string
	database := &fakeJobDatabase{queryRow: func(_ context.Context, sql string, _ ...any) pgx.Row {
		query = sql
		return recoveryRow(2, 1, 1)
	}}
	store, _ := NewPostgresStore(database)
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	result, err := store.RecoverExpired(context.Background(), RecoveryRequest{
		ObservedAt: now, RetryAt: now.Add(5 * time.Second), Limit: 25,
	})
	if err != nil || result != (RecoveryResult{Requeued: 2, Cancelled: 1, Failed: 1}) {
		t.Fatalf("RecoverExpired() = %#v, %v", result, err)
	}
	for _, fragment := range []string{
		"lease_expires_at <= $1", "FOR UPDATE SKIP LOCKED", "LIMIT $3",
		"attempt_count >= max_attempts THEN 'failed'", "ELSE 'queued'",
		"lease_owner = NULL", "lease_attempts_exhausted",
	} {
		if !strings.Contains(query, fragment) {
			t.Errorf("RecoverExpired() SQL lacks %q: %s", fragment, query)
		}
	}
}

func activeJob(t *testing.T, workerID string) Job {
	t.Helper()
	stored := materializeJob(t, testJobSeed(t))
	startedAt := stored.CreatedAt.Add(time.Minute)
	leaseExpiresAt := startedAt.Add(time.Minute)
	stored.Status = StatusAnalyzing
	stored.AttemptCount = 1
	stored.LeaseOwner = workerID
	stored.LeaseExpiresAt = &leaseExpiresAt
	stored.HeartbeatAt = &startedAt
	stored.StartedAt = &startedAt
	stored.UpdatedAt = startedAt
	stored.Version++
	return stored
}

func recoveryRow(requeued, cancelled, failed int32) pgx.Row {
	return scanJobRow(func(destinations ...any) error {
		if len(destinations) != 3 {
			return errors.New("unexpected recovery destination count")
		}
		*(destinations[0].(*int32)) = requeued
		*(destinations[1].(*int32)) = cancelled
		*(destinations[2].(*int32)) = failed
		return nil
	})
}
