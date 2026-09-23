package anonymous

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
)

type quotaBeginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

type quotaStorageError struct {
	operation string
	cause     error
}

func (err *quotaStorageError) Error() string {
	return fmt.Sprintf("anonymous quota %s failed", err.operation)
}

func (err *quotaStorageError) Unwrap() error {
	return err.cause
}

// PostgresQuotaStore enforces session and IP counters inside PostgreSQL
// transactions. Every session-scoped operation locks the active session row
// before the shared IP row, providing one consistent lock order.
type PostgresQuotaStore struct {
	database quotaBeginner
}

var _ QuotaStore = (*PostgresQuotaStore)(nil)

// NewPostgresQuotaStore binds anonymous quota persistence to a pgx-compatible
// transaction beginner such as pgxpool.Pool.
func NewPostgresQuotaStore(database quotaBeginner) (*PostgresQuotaStore, error) {
	if database == nil {
		return nil, errors.New("anonymous quota database is required")
	}
	return &PostgresQuotaStore{database: database}, nil
}

// ConsumeSessionCreation serializes and increments one IP-scoped counter.
func (store *PostgresQuotaStore) ConsumeSessionCreation(
	ctx context.Context,
	ipDigest [sha256.Size]byte,
	window QuotaWindow,
	limit int64,
) (Usage, error) {
	if err := validateCreationQuota(window, limit); err != nil {
		return Usage{}, err
	}
	tx, err := store.database.Begin(ctx)
	if err != nil {
		return Usage{}, &quotaStorageError{operation: "begin session-creation consumption", cause: err}
	}
	defer rollbackQuotaTransaction(ctx, tx)

	if err := ensureIPUsage(ctx, tx, ipDigest, window.StartedAt); err != nil {
		return Usage{}, err
	}
	usage, err := readIPUsageForUpdate(ctx, tx, ipDigest, window.StartedAt)
	if err != nil {
		return Usage{}, err
	}
	if exceeds(usage.SessionsCreated, 1, limit) {
		return Usage{}, quotaExceeded(QuotaScopeIP, QuotaMetricSessions, limit, window.EndsAt)
	}
	if _, err := tx.Exec(ctx, `
UPDATE anonymous_ip_usage
SET sessions_created = sessions_created + 1
WHERE ip_hash = $1 AND window_started_at = $2`, ipDigest[:], window.StartedAt); err != nil {
		return Usage{}, &quotaStorageError{operation: "increment IP session counter", cause: err}
	}
	usage.SessionsCreated++
	if err := tx.Commit(ctx); err != nil {
		return Usage{}, &quotaStorageError{operation: "commit session-creation consumption", cause: err}
	}
	return usage, nil
}

// Consume locks an active session and its IP window, verifies both scopes, and
// increments both counter sets atomically. Any exceeded limit rolls back all
// increments before returning.
func (store *PostgresQuotaStore) Consume(ctx context.Context, request QuotaRequest) (QuotaDecision, error) {
	if err := validateQuotaRequest(request); err != nil {
		return QuotaDecision{}, err
	}
	tx, err := store.database.Begin(ctx)
	if err != nil {
		return QuotaDecision{}, &quotaStorageError{operation: "begin consumption", cause: err}
	}
	defer rollbackQuotaTransaction(ctx, tx)

	if err := lockActiveSession(ctx, tx, request.SessionID, request.ObservedAt); err != nil {
		return QuotaDecision{}, err
	}
	if err := ensureIPUsage(ctx, tx, request.IPDigest, request.Window.StartedAt); err != nil {
		return QuotaDecision{}, err
	}
	ipUsage, err := readIPUsageForUpdate(ctx, tx, request.IPDigest, request.Window.StartedAt)
	if err != nil {
		return QuotaDecision{}, err
	}
	sessionUsage, err := readSessionUsage(ctx, tx, request.SessionID, request.Window.StartedAt)
	if err != nil {
		return QuotaDecision{}, err
	}
	if err := checkScopedLimits(QuotaScopeSession, sessionUsage, request.Delta, request.Limits, request.Window.EndsAt); err != nil {
		return QuotaDecision{}, err
	}
	if err := checkScopedLimits(QuotaScopeIP, ipUsage, request.Delta, request.Limits, request.Window.EndsAt); err != nil {
		return QuotaDecision{}, err
	}

	if _, err := tx.Exec(ctx, `
INSERT INTO anonymous_usage (
    session_id, ip_hash, window_started_at,
    uploads_started, jobs_created, bytes_committed
) VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (session_id, ip_hash, window_started_at) DO UPDATE SET
    uploads_started = anonymous_usage.uploads_started + EXCLUDED.uploads_started,
    jobs_created = anonymous_usage.jobs_created + EXCLUDED.jobs_created,
    bytes_committed = anonymous_usage.bytes_committed + EXCLUDED.bytes_committed`,
		request.SessionID,
		request.IPDigest[:],
		request.Window.StartedAt,
		int32(request.Delta.UploadsStarted),
		int32(request.Delta.JobsCreated),
		request.Delta.BytesCommitted,
	); err != nil {
		return QuotaDecision{}, &quotaStorageError{operation: "increment session counters", cause: err}
	}
	if _, err := tx.Exec(ctx, `
UPDATE anonymous_ip_usage
SET uploads_started = uploads_started + $3,
    jobs_created = jobs_created + $4,
    bytes_committed = bytes_committed + $5
WHERE ip_hash = $1 AND window_started_at = $2`,
		request.IPDigest[:],
		request.Window.StartedAt,
		request.Delta.UploadsStarted,
		request.Delta.JobsCreated,
		request.Delta.BytesCommitted,
	); err != nil {
		return QuotaDecision{}, &quotaStorageError{operation: "increment IP counters", cause: err}
	}

	applyDelta(&sessionUsage, request.Delta)
	applyDelta(&ipUsage, request.Delta)
	if err := tx.Commit(ctx); err != nil {
		return QuotaDecision{}, &quotaStorageError{operation: "commit consumption", cause: err}
	}
	return QuotaDecision{Window: request.Window, Session: sessionUsage, IP: ipUsage}, nil
}

func validateCreationQuota(window QuotaWindow, limit int64) error {
	if window.StartedAt.IsZero() || window.EndsAt.IsZero() || !window.EndsAt.After(window.StartedAt) {
		return errors.New("anonymous quota window is invalid")
	}
	if window.StartedAt.Location() != time.UTC || window.EndsAt.Location() != time.UTC {
		return errors.New("anonymous quota window must use UTC")
	}
	if limit <= 0 || limit > math.MaxInt32 {
		return fmt.Errorf("anonymous session creation quota must be between 1 and %d", math.MaxInt32)
	}
	return nil
}

func validateQuotaRequest(request QuotaRequest) error {
	if !validUUID(request.SessionID) {
		return errors.New("anonymous quota session ID must be a UUID")
	}
	if err := request.Limits.Validate(); err != nil {
		return err
	}
	if err := request.Window.validate(request.Limits.Window, request.ObservedAt); err != nil {
		return err
	}
	return request.Delta.validate()
}

func ensureIPUsage(ctx context.Context, tx pgx.Tx, ipDigest [sha256.Size]byte, windowStart time.Time) error {
	if _, err := tx.Exec(ctx, `
INSERT INTO anonymous_ip_usage (ip_hash, window_started_at)
VALUES ($1, $2)
ON CONFLICT (ip_hash, window_started_at) DO NOTHING`, ipDigest[:], windowStart); err != nil {
		return &quotaStorageError{operation: "initialize IP counters", cause: err}
	}
	return nil
}

func readIPUsageForUpdate(ctx context.Context, tx pgx.Tx, ipDigest [sha256.Size]byte, windowStart time.Time) (Usage, error) {
	var usage Usage
	err := tx.QueryRow(ctx, `
SELECT sessions_created, uploads_started, jobs_created, bytes_committed
FROM anonymous_ip_usage
WHERE ip_hash = $1 AND window_started_at = $2
FOR UPDATE`, ipDigest[:], windowStart).Scan(
		&usage.SessionsCreated,
		&usage.UploadsStarted,
		&usage.JobsCreated,
		&usage.BytesCommitted,
	)
	if err != nil {
		return Usage{}, &quotaStorageError{operation: "lock IP counters", cause: err}
	}
	return usage, nil
}

func lockActiveSession(ctx context.Context, tx pgx.Tx, sessionID string, at time.Time) error {
	var marker int
	err := tx.QueryRow(ctx, `
SELECT 1
FROM anonymous_session
WHERE id = $1 AND revoked_at IS NULL AND expires_at > $2
FOR UPDATE`, sessionID, at).Scan(&marker)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrSessionInactive
	}
	if err != nil {
		return &quotaStorageError{operation: "lock active session", cause: err}
	}
	return nil
}

func readSessionUsage(ctx context.Context, tx pgx.Tx, sessionID string, windowStart time.Time) (Usage, error) {
	var usage Usage
	err := tx.QueryRow(ctx, `
SELECT COALESCE(SUM(uploads_started), 0),
       COALESCE(SUM(jobs_created), 0),
       COALESCE(SUM(bytes_committed), 0)
FROM anonymous_usage
WHERE session_id = $1 AND window_started_at = $2`, sessionID, windowStart).Scan(
		&usage.UploadsStarted,
		&usage.JobsCreated,
		&usage.BytesCommitted,
	)
	if err != nil {
		return Usage{}, &quotaStorageError{operation: "read session counters", cause: err}
	}
	return usage, nil
}

func checkScopedLimits(scope QuotaScope, usage Usage, delta UsageDelta, limits QuotaLimits, retryAt time.Time) error {
	uploadLimit := limits.UploadsPerSession
	jobLimit := limits.JobsPerSession
	byteLimit := limits.BytesPerSession
	if scope == QuotaScopeIP {
		uploadLimit = limits.UploadsPerIP
		jobLimit = limits.JobsPerIP
		byteLimit = limits.BytesPerIP
	}
	if exceeds(usage.UploadsStarted, delta.UploadsStarted, uploadLimit) {
		return quotaExceeded(scope, QuotaMetricUploads, uploadLimit, retryAt)
	}
	if exceeds(usage.JobsCreated, delta.JobsCreated, jobLimit) {
		return quotaExceeded(scope, QuotaMetricJobs, jobLimit, retryAt)
	}
	if exceeds(usage.BytesCommitted, delta.BytesCommitted, byteLimit) {
		return quotaExceeded(scope, QuotaMetricBytes, byteLimit, retryAt)
	}
	return nil
}

func exceeds(current, delta, limit int64) bool {
	if current < 0 || delta < 0 || limit < 0 {
		return true
	}
	return current > limit || delta > limit-current
}

func quotaExceeded(scope QuotaScope, metric QuotaMetric, limit int64, retryAt time.Time) error {
	return &QuotaExceededError{Scope: scope, Metric: metric, Limit: limit, RetryAt: retryAt}
}

func applyDelta(usage *Usage, delta UsageDelta) {
	usage.UploadsStarted += delta.UploadsStarted
	usage.JobsCreated += delta.JobsCreated
	usage.BytesCommitted += delta.BytesCommitted
}

func rollbackQuotaTransaction(ctx context.Context, tx pgx.Tx) {
	rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = tx.Rollback(rollbackCtx)
}
