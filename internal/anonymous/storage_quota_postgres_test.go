package anonymous

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestPostgresQuotaStoreConsumesBothScopesAtomically(t *testing.T) {
	t.Parallel()

	request := validQuotaRequest()
	tx := &fakeQuotaTx{rows: []pgx.Row{
		quotaRow(1),
		quotaRow(int64(2), int64(3), int64(4), int64(500)),
		quotaRow(int64(1), int64(2), int64(300)),
	}}
	store, err := NewPostgresQuotaStore(&fakeQuotaBeginner{tx: tx})
	if err != nil {
		t.Fatalf("NewPostgresQuotaStore() error = %v", err)
	}
	decision, err := store.Consume(context.Background(), request)
	if err != nil {
		t.Fatalf("Consume() error = %v", err)
	}
	if !tx.committed {
		t.Fatal("Consume() did not commit")
	}
	if decision.Session != (Usage{UploadsStarted: 2, JobsCreated: 2, BytesCommitted: 300}) {
		t.Errorf("session usage = %#v", decision.Session)
	}
	if decision.IP != (Usage{SessionsCreated: 2, UploadsStarted: 4, JobsCreated: 4, BytesCommitted: 500}) {
		t.Errorf("IP usage = %#v", decision.IP)
	}
	wantOrder := []string{"query:anonymous_session", "exec:anonymous_ip_usage", "query:anonymous_ip_usage", "query:anonymous_usage", "exec:anonymous_usage", "exec:anonymous_ip_usage"}
	if strings.Join(tx.events, "|") != strings.Join(wantOrder, "|") {
		t.Fatalf("transaction order = %v, want %v", tx.events, wantOrder)
	}
}

func TestPostgresQuotaStoreRollsBackExceededScope(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		ipUsage      Usage
		sessionUsage Usage
		wantScope    QuotaScope
	}{
		{
			name:         "session",
			ipUsage:      Usage{},
			sessionUsage: Usage{UploadsStarted: testQuotaLimits().UploadsPerSession},
			wantScope:    QuotaScopeSession,
		},
		{
			name:         "ip",
			ipUsage:      Usage{UploadsStarted: testQuotaLimits().UploadsPerIP},
			sessionUsage: Usage{},
			wantScope:    QuotaScopeIP,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			tx := &fakeQuotaTx{rows: []pgx.Row{
				quotaRow(1),
				usageRow(test.ipUsage, true),
				usageRow(test.sessionUsage, false),
			}}
			store, _ := NewPostgresQuotaStore(&fakeQuotaBeginner{tx: tx})
			_, err := store.Consume(context.Background(), validQuotaRequest())
			var exceeded *QuotaExceededError
			if !errors.As(err, &exceeded) {
				t.Fatalf("Consume() error = %v, want QuotaExceededError", err)
			}
			if exceeded.Scope != test.wantScope || exceeded.Metric != QuotaMetricUploads || exceeded.Limit <= 0 {
				t.Fatalf("quota error = %#v", exceeded)
			}
			if tx.committed || !tx.rolledBack {
				t.Fatalf("commit=%v rollback=%v", tx.committed, tx.rolledBack)
			}
			if len(tx.events) != 4 {
				t.Fatalf("counter mutation ran after rejection: events = %v", tx.events)
			}
		})
	}
}

func TestPostgresQuotaStoreConsumesSessionCreation(t *testing.T) {
	t.Parallel()

	request := validQuotaRequest()
	tx := &fakeQuotaTx{rows: []pgx.Row{quotaRow(int64(3), int64(2), int64(1), int64(64))}}
	store, _ := NewPostgresQuotaStore(&fakeQuotaBeginner{tx: tx})
	usage, err := store.ConsumeSessionCreation(
		context.Background(), request.IPDigest, request.Window, request.Limits.SessionsPerIP,
	)
	if err != nil {
		t.Fatalf("ConsumeSessionCreation() error = %v", err)
	}
	if usage.SessionsCreated != 4 || !tx.committed {
		t.Fatalf("usage = %#v, committed = %v", usage, tx.committed)
	}

	tx = &fakeQuotaTx{rows: []pgx.Row{quotaRow(request.Limits.SessionsPerIP, int64(0), int64(0), int64(0))}}
	store, _ = NewPostgresQuotaStore(&fakeQuotaBeginner{tx: tx})
	_, err = store.ConsumeSessionCreation(
		context.Background(), request.IPDigest, request.Window, request.Limits.SessionsPerIP,
	)
	var exceeded *QuotaExceededError
	if !errors.As(err, &exceeded) || exceeded.Scope != QuotaScopeIP || exceeded.Metric != QuotaMetricSessions {
		t.Fatalf("ConsumeSessionCreation() error = %#v", err)
	}
	if tx.committed || !tx.rolledBack || len(tx.events) != 2 {
		t.Fatalf("rejected creation transaction = events %v commit=%v rollback=%v", tx.events, tx.committed, tx.rolledBack)
	}
}

func TestPostgresQuotaStoreRejectsInactiveSession(t *testing.T) {
	t.Parallel()

	tx := &fakeQuotaTx{rows: []pgx.Row{quotaErrorRow(pgx.ErrNoRows)}}
	store, _ := NewPostgresQuotaStore(&fakeQuotaBeginner{tx: tx})
	if _, err := store.Consume(context.Background(), validQuotaRequest()); !errors.Is(err, ErrSessionInactive) {
		t.Fatalf("Consume() error = %v, want ErrSessionInactive", err)
	}
	if tx.committed || !tx.rolledBack || len(tx.events) != 1 {
		t.Fatalf("inactive session transaction = events %v commit=%v rollback=%v", tx.events, tx.committed, tx.rolledBack)
	}
}

func TestPostgresQuotaStoreValidatesBeforeStartingTransaction(t *testing.T) {
	t.Parallel()

	if _, err := NewPostgresQuotaStore(nil); err == nil {
		t.Fatal("NewPostgresQuotaStore() accepted nil")
	}
	database := &fakeQuotaBeginner{tx: &fakeQuotaTx{}}
	store, _ := NewPostgresQuotaStore(database)
	request := validQuotaRequest()
	request.SessionID = "invalid"
	if _, err := store.Consume(context.Background(), request); err == nil {
		t.Fatal("Consume() accepted an invalid session ID")
	}
	if database.beginCalls != 0 {
		t.Fatal("invalid request started a transaction")
	}

	local := time.FixedZone("CEST", 2*60*60)
	request = validQuotaRequest()
	request.Window.StartedAt = request.Window.StartedAt.In(local)
	request.Window.EndsAt = request.Window.EndsAt.In(local)
	if _, err := store.ConsumeSessionCreation(context.Background(), request.IPDigest, request.Window, 1); err == nil {
		t.Fatal("ConsumeSessionCreation() accepted a non-UTC window")
	}
	if database.beginCalls != 0 {
		t.Fatal("invalid creation request started a transaction")
	}
}

func TestPostgresQuotaStoreErrorsAreSafeAndUnwrappable(t *testing.T) {
	t.Parallel()

	cause := errors.New("database details that must not be exposed")
	store, _ := NewPostgresQuotaStore(&fakeQuotaBeginner{err: cause})
	_, err := store.Consume(context.Background(), validQuotaRequest())
	if !errors.Is(err, cause) {
		t.Fatalf("Consume() error does not wrap cause: %v", err)
	}
	if strings.Contains(err.Error(), cause.Error()) {
		t.Fatalf("Consume() exposed database details: %v", err)
	}
}

func validQuotaRequest() QuotaRequest {
	observedAt := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	var digest [32]byte
	digest[0] = 42
	return QuotaRequest{
		SessionID:  "11111111-1111-4111-8111-111111111111",
		IPDigest:   digest,
		Window:     QuotaWindow{StartedAt: observedAt.Truncate(24 * time.Hour), EndsAt: observedAt.Truncate(24 * time.Hour).Add(24 * time.Hour)},
		ObservedAt: observedAt,
		Delta:      UsageDelta{UploadsStarted: 1},
		Limits:     testQuotaLimits(),
	}
}

func usageRow(usage Usage, includeSessions bool) pgx.Row {
	if includeSessions {
		return quotaRow(usage.SessionsCreated, usage.UploadsStarted, usage.JobsCreated, usage.BytesCommitted)
	}
	return quotaRow(usage.UploadsStarted, usage.JobsCreated, usage.BytesCommitted)
}

type fakeQuotaBeginner struct {
	tx         pgx.Tx
	err        error
	beginCalls int
}

func (database *fakeQuotaBeginner) Begin(context.Context) (pgx.Tx, error) {
	database.beginCalls++
	return database.tx, database.err
}

type fakeQuotaTx struct {
	pgx.Tx
	rows       []pgx.Row
	events     []string
	committed  bool
	rolledBack bool
}

func (tx *fakeQuotaTx) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	tx.events = append(tx.events, "exec:"+quotaTable(sql))
	return pgconn.NewCommandTag("UPDATE 1"), nil
}

func (tx *fakeQuotaTx) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	tx.events = append(tx.events, "query:"+quotaTable(sql))
	if len(tx.rows) == 0 {
		return quotaErrorRow(errors.New("unexpected QueryRow"))
	}
	row := tx.rows[0]
	tx.rows = tx.rows[1:]
	return row
}

func (tx *fakeQuotaTx) Commit(context.Context) error {
	tx.committed = true
	return nil
}

func (tx *fakeQuotaTx) Rollback(context.Context) error {
	tx.rolledBack = true
	return nil
}

func quotaTable(sql string) string {
	for _, table := range []string{"anonymous_session", "anonymous_ip_usage", "anonymous_usage"} {
		if strings.Contains(sql, table) {
			return table
		}
	}
	return "unknown"
}

type quotaRowValues struct {
	values []int64
	err    error
}

func quotaRow(values ...any) pgx.Row {
	converted := make([]int64, len(values))
	for index, value := range values {
		switch typed := value.(type) {
		case int:
			converted[index] = int64(typed)
		case int64:
			converted[index] = typed
		default:
			panic("quotaRow received an unsupported value")
		}
	}
	return quotaRowValues{values: converted}
}

func quotaErrorRow(err error) pgx.Row {
	return quotaRowValues{err: err}
}

func (row quotaRowValues) Scan(destinations ...any) error {
	if row.err != nil {
		return row.err
	}
	if len(destinations) != len(row.values) {
		return errors.New("unexpected quota scan destination count")
	}
	for index, destination := range destinations {
		switch typed := destination.(type) {
		case *int:
			*typed = int(row.values[index])
		case *int64:
			*typed = row.values[index]
		default:
			return errors.New("unsupported quota scan destination")
		}
	}
	return nil
}
