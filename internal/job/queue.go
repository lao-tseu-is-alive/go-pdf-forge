package job

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	// ErrNoJobAvailable reports that no queued job is currently claimable.
	ErrNoJobAvailable = errors.New("no job available")
	// ErrLeaseLost reports that a worker no longer owns an unexpired job lease.
	ErrLeaseLost = errors.New("job lease lost")
	// ErrCancellationRequested reports that a valid lease must stop extending work.
	ErrCancellationRequested = errors.New("job cancellation requested")
)

// QueueStore is the durable worker-facing queue and lease boundary.
type QueueStore interface {
	// Claim atomically locks and leases the oldest available queued job.
	Claim(context.Context, string, time.Time, time.Time) (Job, error)
	// Heartbeat conditionally extends one unexpired worker-owned lease.
	Heartbeat(context.Context, string, string, time.Time, time.Time) (Job, error)
	// Transition applies one expected-state mutation under an unexpired lease.
	Transition(context.Context, Transition) (Job, error)
	// RecoverExpired atomically requeues or terminates a bounded batch of expired leases.
	RecoverExpired(context.Context, RecoveryRequest) (RecoveryResult, error)
}

// Transition is one worker-owned expected-state mutation.
type Transition struct {
	// JobID identifies the leased job.
	JobID string
	// WorkerID must match the current lease owner.
	WorkerID string
	// ExpectedStatus prevents stale workers from overwriting a newer phase.
	ExpectedStatus Status
	// NextStatus is an allowed direct lifecycle successor.
	NextStatus Status
	// ProgressPercent is monotonic indicative progress from zero through one hundred.
	ProgressPercent int16
	// ProgressMessage is safe user-facing text and never raw tool output.
	ProgressMessage string
	// ErrorCode is required only when NextStatus is failed.
	ErrorCode string
	// ErrorMessage is safe user-facing failure text required with ErrorCode.
	ErrorMessage string
	// ObservedAt is the worker clock instant used for lease validation and timestamps.
	ObservedAt time.Time
	// LeaseExpiresAt is the exclusive extended deadline for a non-terminal successor.
	LeaseExpiresAt time.Time
}

// RecoveryRequest bounds one expired-lease recovery transaction.
type RecoveryRequest struct {
	// ObservedAt is the exclusive instant at which leases are considered expired.
	ObservedAt time.Time
	// RetryAt schedules retryable recovered jobs without sleeping in a transaction.
	RetryAt time.Time
	// Limit bounds rows locked by one recovery pass.
	Limit int32
}

// RecoveryResult summarizes one expired-lease recovery transaction.
type RecoveryResult struct {
	// Requeued counts retryable jobs returned to queued.
	Requeued int32
	// Cancelled counts jobs terminated because cancellation or deletion was requested.
	Cancelled int32
	// Failed counts jobs that exhausted MaxAttempts.
	Failed int32
}

// Queue applies worker identity, lease duration and retry policy to QueueStore.
// Each live worker process must use a unique WorkerID.
type Queue struct {
	store         QueueStore
	workerID      string
	leaseDuration time.Duration
	retryDelay    time.Duration
	recoveryBatch int32
	now           func() time.Time
}

// NewQueue constructs a worker queue with bounded positive lease and recovery policies.
func NewQueue(store QueueStore, workerID string, leaseDuration, retryDelay time.Duration, recoveryBatch int32) (*Queue, error) {
	if store == nil {
		return nil, errors.New("job queue store is required")
	}
	if !validWorkerID(workerID) {
		return nil, errors.New("job worker ID is invalid")
	}
	if leaseDuration <= 0 {
		return nil, errors.New("job lease duration must be positive")
	}
	if retryDelay < 0 {
		return nil, errors.New("job retry delay must not be negative")
	}
	if recoveryBatch <= 0 || recoveryBatch > 1000 {
		return nil, errors.New("job recovery batch must be between 1 and 1000")
	}
	return &Queue{
		store: store, workerID: workerID, leaseDuration: leaseDuration,
		retryDelay: retryDelay, recoveryBatch: recoveryBatch, now: time.Now,
	}, nil
}

// Claim leases the oldest currently available job for this worker.
func (queue *Queue) Claim(ctx context.Context) (Job, error) {
	now := queue.now().UTC()
	return queue.store.Claim(ctx, queue.workerID, now, now.Add(queue.leaseDuration))
}

// Heartbeat extends a valid lease or returns ErrCancellationRequested without
// extending it when an owner has requested cancellation or deletion.
func (queue *Queue) Heartbeat(ctx context.Context, jobID string) (Job, error) {
	now := queue.now().UTC()
	return queue.store.Heartbeat(ctx, jobID, queue.workerID, now, now.Add(queue.leaseDuration))
}

// Transition validates a direct lifecycle edge and applies it under this worker's lease.
func (queue *Queue) Transition(ctx context.Context, transition Transition) (Job, error) {
	transition.WorkerID = queue.workerID
	transition.ObservedAt = queue.now().UTC()
	transition.LeaseExpiresAt = transition.ObservedAt.Add(queue.leaseDuration)
	if err := validateTransition(transition); err != nil {
		return Job{}, err
	}
	return queue.store.Transition(ctx, transition)
}

// RecoverExpired reclaims a bounded batch without assuming ownership by this worker.
func (queue *Queue) RecoverExpired(ctx context.Context) (RecoveryResult, error) {
	now := queue.now().UTC()
	return queue.store.RecoverExpired(ctx, RecoveryRequest{
		ObservedAt: now, RetryAt: now.Add(queue.retryDelay), Limit: queue.recoveryBatch,
	})
}

func validateClaim(workerID string, observedAt, leaseExpiresAt time.Time) error {
	if !validWorkerID(workerID) {
		return errors.New("job worker ID is invalid")
	}
	if observedAt.IsZero() || !leaseExpiresAt.After(observedAt) {
		return errors.New("job claim lease timestamps are invalid")
	}
	return nil
}

func validateHeartbeat(jobID, workerID string, observedAt, leaseExpiresAt time.Time) error {
	if !validUUID(jobID) {
		return ErrLeaseLost
	}
	return validateClaim(workerID, observedAt, leaseExpiresAt)
}

func validateTransition(transition Transition) error {
	if !validUUID(transition.JobID) {
		return ErrLeaseLost
	}
	if !validWorkerID(transition.WorkerID) {
		return errors.New("job worker ID is invalid")
	}
	if err := ValidateTransition(transition.ExpectedStatus, transition.NextStatus); err != nil {
		return err
	}
	if transition.ExpectedStatus == StatusQueued || transition.ExpectedStatus.Terminal() ||
		transition.NextStatus == StatusQueued || transition.NextStatus == StatusExpired {
		return errors.New("job worker transition must start from an active lease and cannot expire data")
	}
	if transition.ProgressPercent < 0 || transition.ProgressPercent > 100 {
		return errors.New("job transition progress must be between zero and one hundred")
	}
	if len(transition.ProgressMessage) > maxProgressBytes {
		return fmt.Errorf("job progress message exceeds %d bytes", maxProgressBytes)
	}
	if transition.NextStatus == StatusCompleted && transition.ProgressPercent != 100 {
		return errors.New("completed job transition requires 100 percent progress")
	}
	if transition.NextStatus == StatusFailed {
		if !validBoundedText(transition.ErrorCode, maxErrorCodeBytes) ||
			!validBoundedText(transition.ErrorMessage, maxErrorMessageBytes) {
			return errors.New("failed job transition requires bounded error details")
		}
	} else if transition.ErrorCode != "" || transition.ErrorMessage != "" {
		return errors.New("non-failed job transition cannot contain error details")
	}
	if transition.ObservedAt.IsZero() || !transition.LeaseExpiresAt.After(transition.ObservedAt) {
		return errors.New("job transition lease timestamps are invalid")
	}
	return nil
}

func validateRecovery(request RecoveryRequest) error {
	if request.ObservedAt.IsZero() || request.RetryAt.Before(request.ObservedAt) {
		return errors.New("job recovery timestamps are invalid")
	}
	if request.Limit <= 0 || request.Limit > 1000 {
		return errors.New("job recovery limit must be between 1 and 1000")
	}
	return nil
}

func validWorkerID(workerID string) bool {
	return strings.TrimSpace(workerID) == workerID && validBoundedText(workerID, maxIdentityTextBytes)
}
