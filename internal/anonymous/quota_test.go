package anonymous

import (
	"context"
	"crypto/sha256"
	"math"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestQuotaManagerDerivesWindowDigestAndOperationDeltas(t *testing.T) {
	t.Parallel()

	pepper := []byte(strings.Repeat("p", 32))
	store := &recordingQuotaStore{}
	manager, err := NewQuotaManager(store, pepper, testQuotaLimits())
	if err != nil {
		t.Fatalf("NewQuotaManager() error = %v", err)
	}
	now := time.Date(2026, 9, 23, 14, 30, 0, 0, time.FixedZone("CEST", 2*60*60))
	manager.now = func() time.Time { return now }
	clientIP := netip.MustParseAddr("::ffff:192.0.2.42")
	const sessionID = "11111111-1111-4111-8111-111111111111"

	if _, err := manager.ConsumeUploadStart(context.Background(), sessionID, clientIP); err != nil {
		t.Fatalf("ConsumeUploadStart() error = %v", err)
	}
	assertQuotaRequest(t, store.request, sessionID, now.UTC(), UsageDelta{UploadsStarted: 1})
	wantDigest, err := DigestIP(pepper, netip.MustParseAddr("192.0.2.42"))
	if err != nil {
		t.Fatalf("DigestIP() error = %v", err)
	}
	if store.request.IPDigest != wantDigest {
		t.Fatal("ConsumeUploadStart() did not canonicalize and digest the client IP")
	}

	if _, err := manager.ConsumeJobCreation(context.Background(), sessionID, clientIP); err != nil {
		t.Fatalf("ConsumeJobCreation() error = %v", err)
	}
	assertQuotaRequest(t, store.request, sessionID, now.UTC(), UsageDelta{JobsCreated: 1})

	if _, err := manager.ConsumeCommittedBytes(context.Background(), sessionID, clientIP, 4096); err != nil {
		t.Fatalf("ConsumeCommittedBytes() error = %v", err)
	}
	assertQuotaRequest(t, store.request, sessionID, now.UTC(), UsageDelta{BytesCommitted: 4096})
}

func TestQuotaManagerConsumesSessionCreationPerIP(t *testing.T) {
	t.Parallel()

	pepper := []byte(strings.Repeat("p", 32))
	store := &recordingQuotaStore{}
	manager, err := NewQuotaManager(store, pepper, testQuotaLimits())
	if err != nil {
		t.Fatalf("NewQuotaManager() error = %v", err)
	}
	now := time.Date(2026, 9, 23, 23, 59, 0, 0, time.UTC)
	manager.now = func() time.Time { return now }
	ip := netip.MustParseAddr("2001:db8::42")

	if _, err := manager.ConsumeSessionCreation(context.Background(), ip); err != nil {
		t.Fatalf("ConsumeSessionCreation() error = %v", err)
	}
	wantDigest, _ := DigestIP(pepper, ip)
	if store.creationDigest != wantDigest || store.creationLimit != testQuotaLimits().SessionsPerIP {
		t.Fatalf("session creation digest/limit = %x/%d", store.creationDigest, store.creationLimit)
	}
	if store.creationWindow.StartedAt != time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC) ||
		store.creationWindow.EndsAt != time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC) {
		t.Fatalf("session creation window = %#v", store.creationWindow)
	}
}

func TestQuotaManagerAndLimitsRejectInvalidInput(t *testing.T) {
	t.Parallel()

	pepper := []byte(strings.Repeat("p", 32))
	store := &recordingQuotaStore{}
	if _, err := NewQuotaManager(nil, pepper, testQuotaLimits()); err == nil {
		t.Fatal("NewQuotaManager() accepted nil store")
	}
	if _, err := NewQuotaManager(store, []byte("short"), testQuotaLimits()); err == nil {
		t.Fatal("NewQuotaManager() accepted short pepper")
	}
	limits := testQuotaLimits()
	limits.JobsPerIP = int64(math.MaxInt32) + 1
	if _, err := NewQuotaManager(store, pepper, limits); err == nil {
		t.Fatal("NewQuotaManager() accepted an overflowing count limit")
	}
	manager, err := NewQuotaManager(store, pepper, testQuotaLimits())
	if err != nil {
		t.Fatalf("NewQuotaManager() error = %v", err)
	}
	if _, err := manager.ConsumeCommittedBytes(context.Background(), "11111111-1111-4111-8111-111111111111", netip.MustParseAddr("192.0.2.1"), 0); err == nil {
		t.Fatal("ConsumeCommittedBytes() accepted zero bytes")
	}
	if store.consumeCalls != 0 {
		t.Fatal("invalid committed bytes reached persistence")
	}
}

func TestQuotaExceededErrorDoesNotExposeIdentifiers(t *testing.T) {
	t.Parallel()

	retryAt := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	err := &QuotaExceededError{Scope: QuotaScopeIP, Metric: QuotaMetricUploads, Limit: 50, RetryAt: retryAt}
	if got := err.Error(); got != "anonymous ip uploads quota exceeded" {
		t.Fatalf("Error() = %q", got)
	}
	if strings.Contains(err.Error(), "192.0.2.1") || strings.Contains(err.Error(), "11111111") {
		t.Fatal("quota error exposed an identifier")
	}
}

func assertQuotaRequest(t *testing.T, got QuotaRequest, sessionID string, observedAt time.Time, delta UsageDelta) {
	t.Helper()
	if got.SessionID != sessionID || got.ObservedAt != observedAt || got.Delta != delta {
		t.Fatalf("quota request = %#v", got)
	}
	if got.Window.StartedAt != time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC) ||
		got.Window.EndsAt != time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC) {
		t.Fatalf("quota window = %#v", got.Window)
	}
}

func testQuotaLimits() QuotaLimits {
	return QuotaLimits{
		Window:            24 * time.Hour,
		SessionsPerIP:     20,
		UploadsPerSession: 10,
		UploadsPerIP:      50,
		JobsPerSession:    10,
		JobsPerIP:         50,
		BytesPerSession:   1024,
		BytesPerIP:        4096,
	}
}

type recordingQuotaStore struct {
	request        QuotaRequest
	consumeCalls   int
	creationDigest [sha256.Size]byte
	creationWindow QuotaWindow
	creationLimit  int64
	err            error
}

func (store *recordingQuotaStore) ConsumeSessionCreation(
	_ context.Context,
	digest [sha256.Size]byte,
	window QuotaWindow,
	limit int64,
) (Usage, error) {
	store.creationDigest = digest
	store.creationWindow = window
	store.creationLimit = limit
	return Usage{}, store.err
}

func (store *recordingQuotaStore) Consume(_ context.Context, request QuotaRequest) (QuotaDecision, error) {
	store.consumeCalls++
	store.request = request
	return QuotaDecision{}, store.err
}

var _ QuotaStore = (*recordingQuotaStore)(nil)
var _ error = (*QuotaExceededError)(nil)
