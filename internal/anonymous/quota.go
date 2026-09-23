package anonymous

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"net/netip"
	"time"
)

// QuotaScope identifies the independently enforced anonymous usage owner.
type QuotaScope string

const (
	// QuotaScopeSession applies a limit to one anonymous session across IPs.
	QuotaScopeSession QuotaScope = "session"
	// QuotaScopeIP applies a limit to all sessions sharing one IP digest.
	QuotaScopeIP QuotaScope = "ip"
)

// QuotaMetric identifies the bounded anonymous operation or resource.
type QuotaMetric string

const (
	// QuotaMetricSessions counts newly issued anonymous sessions.
	QuotaMetricSessions QuotaMetric = "sessions"
	// QuotaMetricUploads counts started anonymous uploads.
	QuotaMetricUploads QuotaMetric = "uploads"
	// QuotaMetricJobs counts created anonymous PDF jobs.
	QuotaMetricJobs QuotaMetric = "jobs"
	// QuotaMetricBytes counts committed anonymous source bytes.
	QuotaMetricBytes QuotaMetric = "bytes"
)

// QuotaExceededError reports which fixed-window limit rejected consumption.
// It deliberately omits IP digests, session IDs, and current usage.
type QuotaExceededError struct {
	// Scope is the session or IP limit that was reached.
	Scope QuotaScope
	// Metric is the rejected operation or resource.
	Metric QuotaMetric
	// Limit is the configured maximum for the window.
	Limit int64
	// RetryAt is the exclusive end of the current fixed window.
	RetryAt time.Time
}

// Error returns a non-sensitive stable quota failure description.
func (err *QuotaExceededError) Error() string {
	return fmt.Sprintf("anonymous %s %s quota exceeded", err.Scope, err.Metric)
}

// QuotaLimits contains fixed-window maxima. All fields are required and
// positive; count limits must fit PostgreSQL integer counters.
type QuotaLimits struct {
	// Window is the UTC-aligned fixed-window duration.
	Window time.Duration
	// SessionsPerIP limits session creation attempts from one IP digest.
	SessionsPerIP int64
	// UploadsPerSession limits upload starts for one session.
	UploadsPerSession int64
	// UploadsPerIP limits upload starts across sessions sharing an IP digest.
	UploadsPerIP int64
	// JobsPerSession limits job creations for one session.
	JobsPerSession int64
	// JobsPerIP limits job creations across sessions sharing an IP digest.
	JobsPerIP int64
	// BytesPerSession limits committed source bytes for one session.
	BytesPerSession int64
	// BytesPerIP limits committed source bytes across sessions sharing an IP digest.
	BytesPerIP int64
}

// Validate checks the fixed-window and PostgreSQL counter invariants.
func (limits QuotaLimits) Validate() error {
	var problems []error
	if limits.Window < time.Minute || limits.Window%time.Second != 0 {
		problems = append(problems, errors.New("anonymous quota window must be at least one minute and use whole seconds"))
	}
	counts := []struct {
		name  string
		value int64
	}{
		{name: "sessions per IP", value: limits.SessionsPerIP},
		{name: "uploads per session", value: limits.UploadsPerSession},
		{name: "uploads per IP", value: limits.UploadsPerIP},
		{name: "jobs per session", value: limits.JobsPerSession},
		{name: "jobs per IP", value: limits.JobsPerIP},
	}
	for _, count := range counts {
		if count.value <= 0 || count.value > math.MaxInt32 {
			problems = append(problems, fmt.Errorf("anonymous quota %s must be between 1 and %d", count.name, math.MaxInt32))
		}
	}
	if limits.BytesPerSession <= 0 {
		problems = append(problems, errors.New("anonymous quota bytes per session must be positive"))
	}
	if limits.BytesPerIP <= 0 {
		problems = append(problems, errors.New("anonymous quota bytes per IP must be positive"))
	}
	return errors.Join(problems...)
}

// QuotaWindow is one UTC-aligned fixed interval with an exclusive end.
type QuotaWindow struct {
	// StartedAt is the inclusive counter key.
	StartedAt time.Time
	// EndsAt is the first instant assigned to the following window.
	EndsAt time.Time
}

// Usage is a non-secret counter snapshot for one quota scope and window.
type Usage struct {
	// SessionsCreated counts issued-session attempts and is meaningful for IP scope.
	SessionsCreated int64
	// UploadsStarted counts accepted upload starts.
	UploadsStarted int64
	// JobsCreated counts accepted job creations.
	JobsCreated int64
	// BytesCommitted counts accepted source bytes.
	BytesCommitted int64
}

// UsageDelta is one atomic quota consumption request. Values must be
// non-negative and at least one value must be positive.
type UsageDelta struct {
	// UploadsStarted increments upload starts.
	UploadsStarted int64
	// JobsCreated increments job creations.
	JobsCreated int64
	// BytesCommitted increments committed source bytes.
	BytesCommitted int64
}

// QuotaDecision contains the counters after successful atomic consumption.
type QuotaDecision struct {
	// Window identifies when these counters reset.
	Window QuotaWindow
	// Session is the aggregate across every IP used by the session.
	Session Usage
	// IP is the aggregate across every session using the IP digest.
	IP Usage
}

// QuotaRequest carries already-derived persistence values for one consumption.
// It never contains a raw client IP or bearer capability.
type QuotaRequest struct {
	// SessionID is the active anonymous session UUID.
	SessionID string
	// IPDigest is the domain-separated HMAC of the canonical client IP.
	IPDigest [sha256.Size]byte
	// Window is the fixed interval selected by QuotaManager.
	Window QuotaWindow
	// ObservedAt is the request instant used for active-session validation.
	ObservedAt time.Time
	// Delta is consumed from both session and IP scopes.
	Delta UsageDelta
	// Limits are the configuration snapshot applied to this decision.
	Limits QuotaLimits
}

// QuotaStore is the persistence boundary for atomic anonymous counters.
type QuotaStore interface {
	// ConsumeSessionCreation increments the IP-scoped session counter.
	ConsumeSessionCreation(context.Context, [sha256.Size]byte, QuotaWindow, int64) (Usage, error)
	// Consume increments session and IP counters in one atomic operation.
	Consume(context.Context, QuotaRequest) (QuotaDecision, error)
}

// QuotaManager derives IP digests and fixed windows before persistence.
type QuotaManager struct {
	store  QuotaStore
	pepper []byte
	limits QuotaLimits
	now    func() time.Time
}

// NewQuotaManager validates and copies the complete anonymous quota policy.
func NewQuotaManager(store QuotaStore, pepper []byte, limits QuotaLimits) (*QuotaManager, error) {
	if store == nil {
		return nil, errors.New("anonymous quota store is required")
	}
	if err := validatePepper(pepper); err != nil {
		return nil, err
	}
	if err := limits.Validate(); err != nil {
		return nil, err
	}
	return &QuotaManager{
		store:  store,
		pepper: append([]byte(nil), pepper...),
		limits: limits,
		now:    time.Now,
	}, nil
}

// ConsumeSessionCreation atomically spends one IP-scoped session creation.
func (manager *QuotaManager) ConsumeSessionCreation(ctx context.Context, clientIP netip.Addr) (Usage, error) {
	digest, err := DigestIP(manager.pepper, clientIP)
	if err != nil {
		return Usage{}, err
	}
	return manager.store.ConsumeSessionCreation(
		ctx, digest, manager.window(manager.now().UTC()), manager.limits.SessionsPerIP,
	)
}

// ConsumeUploadStart atomically spends one upload from session and IP scopes.
func (manager *QuotaManager) ConsumeUploadStart(ctx context.Context, sessionID string, clientIP netip.Addr) (QuotaDecision, error) {
	return manager.consume(ctx, sessionID, clientIP, UsageDelta{UploadsStarted: 1})
}

// ConsumeJobCreation atomically spends one job from session and IP scopes.
func (manager *QuotaManager) ConsumeJobCreation(ctx context.Context, sessionID string, clientIP netip.Addr) (QuotaDecision, error) {
	return manager.consume(ctx, sessionID, clientIP, UsageDelta{JobsCreated: 1})
}

// ConsumeCommittedBytes atomically spends positive source bytes from session
// and IP scopes after an upload has passed complete-object verification.
func (manager *QuotaManager) ConsumeCommittedBytes(ctx context.Context, sessionID string, clientIP netip.Addr, byteCount int64) (QuotaDecision, error) {
	if byteCount <= 0 {
		return QuotaDecision{}, errors.New("committed anonymous bytes must be positive")
	}
	return manager.consume(ctx, sessionID, clientIP, UsageDelta{BytesCommitted: byteCount})
}

func (manager *QuotaManager) consume(ctx context.Context, sessionID string, clientIP netip.Addr, delta UsageDelta) (QuotaDecision, error) {
	digest, err := DigestIP(manager.pepper, clientIP)
	if err != nil {
		return QuotaDecision{}, err
	}
	observedAt := manager.now().UTC()
	return manager.store.Consume(ctx, QuotaRequest{
		SessionID:  sessionID,
		IPDigest:   digest,
		Window:     manager.window(observedAt),
		ObservedAt: observedAt,
		Delta:      delta,
		Limits:     manager.limits,
	})
}

func (manager *QuotaManager) window(at time.Time) QuotaWindow {
	start := at.Truncate(manager.limits.Window)
	return QuotaWindow{StartedAt: start, EndsAt: start.Add(manager.limits.Window)}
}

func (delta UsageDelta) validate() error {
	if delta.UploadsStarted < 0 || delta.JobsCreated < 0 || delta.BytesCommitted < 0 {
		return errors.New("anonymous quota deltas must not be negative")
	}
	if delta.UploadsStarted == 0 && delta.JobsCreated == 0 && delta.BytesCommitted == 0 {
		return errors.New("anonymous quota delta must consume at least one counter")
	}
	if delta.UploadsStarted > math.MaxInt32 || delta.JobsCreated > math.MaxInt32 {
		return errors.New("anonymous quota count delta exceeds PostgreSQL integer range")
	}
	return nil
}

func (window QuotaWindow) validate(expectedDuration time.Duration, observedAt time.Time) error {
	if window.StartedAt.IsZero() || window.EndsAt.IsZero() || !window.EndsAt.Equal(window.StartedAt.Add(expectedDuration)) {
		return errors.New("anonymous quota window does not match configured duration")
	}
	if window.StartedAt.Location() != time.UTC || window.EndsAt.Location() != time.UTC {
		return errors.New("anonymous quota window must use UTC")
	}
	if observedAt.Before(window.StartedAt) || !observedAt.Before(window.EndsAt) {
		return errors.New("anonymous quota observation is outside its window")
	}
	return nil
}
