package job

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestQueueAppliesWorkerLeaseAndRecoveryPolicies(t *testing.T) {
	t.Parallel()

	store := &memoryQueueStore{}
	queue, err := NewQueue(store, "worker-a", 30*time.Second, 5*time.Second, 25)
	if err != nil {
		t.Fatalf("NewQueue() error = %v", err)
	}
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	queue.now = func() time.Time { return now }

	if _, err := queue.Claim(context.Background()); err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if store.workerID != "worker-a" || !store.observedAt.Equal(now) || !store.leaseExpiresAt.Equal(now.Add(30*time.Second)) {
		t.Fatalf("claim policy = worker %q observed %v expires %v", store.workerID, store.observedAt, store.leaseExpiresAt)
	}

	jobID := "11111111-1111-4111-8111-111111111111"
	if _, err := queue.Heartbeat(context.Background(), jobID); err != nil {
		t.Fatalf("Heartbeat() error = %v", err)
	}
	if store.jobID != jobID || !store.leaseExpiresAt.Equal(now.Add(30*time.Second)) {
		t.Fatalf("heartbeat policy = job %q expires %v", store.jobID, store.leaseExpiresAt)
	}

	transition := Transition{
		JobID: jobID, ExpectedStatus: StatusAnalyzing, NextStatus: StatusOptimizing,
		ProgressPercent: 20, ProgressMessage: "Optimizing",
	}
	if _, err := queue.Transition(context.Background(), transition); err != nil {
		t.Fatalf("Transition() error = %v", err)
	}
	if store.transition.WorkerID != "worker-a" || !store.transition.ObservedAt.Equal(now) ||
		!store.transition.LeaseExpiresAt.Equal(now.Add(30*time.Second)) {
		t.Fatalf("transition policy = %#v", store.transition)
	}

	if _, err := queue.RecoverExpired(context.Background()); err != nil {
		t.Fatalf("RecoverExpired() error = %v", err)
	}
	if !store.recovery.ObservedAt.Equal(now) || !store.recovery.RetryAt.Equal(now.Add(5*time.Second)) || store.recovery.Limit != 25 {
		t.Fatalf("recovery policy = %#v", store.recovery)
	}
}

func TestNewQueueRejectsInvalidPolicies(t *testing.T) {
	t.Parallel()

	store := &memoryQueueStore{}
	tests := []struct {
		name          string
		store         QueueStore
		workerID      string
		leaseDuration time.Duration
		retryDelay    time.Duration
		batch         int32
	}{
		{name: "store", workerID: "worker", leaseDuration: time.Second, batch: 1},
		{name: "worker", store: store, leaseDuration: time.Second, batch: 1},
		{name: "trimmed worker", store: store, workerID: " worker ", leaseDuration: time.Second, batch: 1},
		{name: "lease", store: store, workerID: "worker", batch: 1},
		{name: "retry", store: store, workerID: "worker", leaseDuration: time.Second, retryDelay: -time.Second, batch: 1},
		{name: "batch zero", store: store, workerID: "worker", leaseDuration: time.Second},
		{name: "batch high", store: store, workerID: "worker", leaseDuration: time.Second, batch: 1001},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := NewQueue(test.store, test.workerID, test.leaseDuration, test.retryDelay, test.batch); err == nil {
				t.Fatal("NewQueue() accepted invalid policy")
			}
		})
	}
}

func TestQueueValidatesTransitionsBeforeStorage(t *testing.T) {
	t.Parallel()

	store := &memoryQueueStore{}
	queue, _ := NewQueue(store, "worker-a", 30*time.Second, 0, 1)
	jobID := "11111111-1111-4111-8111-111111111111"
	tests := []struct {
		name       string
		transition Transition
	}{
		{name: "invalid edge", transition: Transition{JobID: jobID, ExpectedStatus: StatusAnalyzing, NextStatus: StatusValidating}},
		{name: "queued source", transition: Transition{JobID: jobID, ExpectedStatus: StatusQueued, NextStatus: StatusAnalyzing}},
		{name: "worker cannot recover", transition: Transition{JobID: jobID, ExpectedStatus: StatusAnalyzing, NextStatus: StatusQueued}},
		{name: "bad progress", transition: Transition{JobID: jobID, ExpectedStatus: StatusAnalyzing, NextStatus: StatusOptimizing, ProgressPercent: 101}},
		{name: "incomplete completion", transition: Transition{JobID: jobID, ExpectedStatus: StatusAnalyzing, NextStatus: StatusCompleted, ProgressPercent: 99}},
		{name: "failed without detail", transition: Transition{JobID: jobID, ExpectedStatus: StatusAnalyzing, NextStatus: StatusFailed}},
		{name: "error on success", transition: Transition{JobID: jobID, ExpectedStatus: StatusAnalyzing, NextStatus: StatusCompleted, ProgressPercent: 100, ErrorCode: "unexpected"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := queue.Transition(context.Background(), test.transition); err == nil {
				t.Fatal("Transition() accepted invalid request")
			}
		})
	}
	if store.transitionCalls != 0 {
		t.Fatalf("invalid transitions reached storage %d times", store.transitionCalls)
	}
}

func TestQueuePropagatesLeaseSignals(t *testing.T) {
	t.Parallel()

	store := &memoryQueueStore{operationErr: ErrCancellationRequested}
	queue, _ := NewQueue(store, "worker-a", time.Minute, 0, 1)
	jobID := "11111111-1111-4111-8111-111111111111"
	if _, err := queue.Heartbeat(context.Background(), jobID); !errors.Is(err, ErrCancellationRequested) {
		t.Fatalf("Heartbeat() error = %v", err)
	}
}

type memoryQueueStore struct {
	workerID        string
	jobID           string
	observedAt      time.Time
	leaseExpiresAt  time.Time
	transition      Transition
	transitionCalls int
	recovery        RecoveryRequest
	operationErr    error
}

func (store *memoryQueueStore) Claim(_ context.Context, workerID string, observedAt, leaseExpiresAt time.Time) (Job, error) {
	store.workerID = workerID
	store.observedAt = observedAt
	store.leaseExpiresAt = leaseExpiresAt
	return Job{}, store.operationErr
}

func (store *memoryQueueStore) Heartbeat(_ context.Context, jobID, workerID string, observedAt, leaseExpiresAt time.Time) (Job, error) {
	store.jobID = jobID
	store.workerID = workerID
	store.observedAt = observedAt
	store.leaseExpiresAt = leaseExpiresAt
	return Job{}, store.operationErr
}

func (store *memoryQueueStore) Transition(_ context.Context, transition Transition) (Job, error) {
	store.transitionCalls++
	store.transition = transition
	return Job{}, store.operationErr
}

func (store *memoryQueueStore) RecoverExpired(_ context.Context, request RecoveryRequest) (RecoveryResult, error) {
	store.recovery = request
	return RecoveryResult{}, store.operationErr
}
