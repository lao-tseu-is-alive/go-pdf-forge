package upload

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestPostgresStoreCreateAndGetUseOwnerScopedMetadata(t *testing.T) {
	t.Parallel()

	session := testSession(t)
	var createSQL string
	database := &fakeUploadDatabase{queryRow: func(_ context.Context, sql string, _ ...any) pgx.Row {
		createSQL = sql
		return sessionRow(session)
	}}
	store, err := NewPostgresStore(database)
	if err != nil {
		t.Fatalf("NewPostgresStore() error = %v", err)
	}
	created, err := store.Create(context.Background(), session)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if created.ID != session.ID || !strings.Contains(createSQL, "INSERT INTO upload_session") {
		t.Fatalf("Create() = %#v, SQL = %q", created, createSQL)
	}

	var getSQL string
	database.queryRow = func(_ context.Context, sql string, _ ...any) pgx.Row {
		getSQL = sql
		return sessionRow(session)
	}
	got, err := store.Get(context.Background(), session.ID, session.Owner)
	if err != nil || got.ID != session.ID {
		t.Fatalf("Get() = %#v, %v", got, err)
	}
	if !strings.Contains(getSQL, "owner_user_id IS NOT DISTINCT FROM") || !strings.Contains(getSQL, "anonymous_session_id IS NOT DISTINCT FROM") {
		t.Fatalf("Get() lacks owner predicates: %s", getSQL)
	}
}

func TestPostgresStoreRecordPartAndIdenticalRetry(t *testing.T) {
	t.Parallel()

	session := testSession(t)
	write := testPartWrite(t, session, 0, 4)
	tx := &fakeUploadTx{queryRows: []pgx.Row{sessionRow(session), errorRow(pgx.ErrNoRows)}}
	store, _ := NewPostgresStore(&fakeUploadDatabase{tx: tx})
	receipt, err := store.RecordPart(context.Background(), write)
	if err != nil {
		t.Fatalf("RecordPart() error = %v", err)
	}
	if receipt.AlreadyPresent || receipt.ReceivedChunks != 1 || receipt.ReceivedBytes != 4 || !tx.committed {
		t.Fatalf("RecordPart() = %#v, committed=%v", receipt, tx.committed)
	}
	if len(tx.execSQL) != 2 || !strings.Contains(tx.execSQL[0], "INSERT INTO upload_part") || !strings.Contains(tx.execSQL[1], "UPDATE upload_session") {
		t.Fatalf("mutation SQL = %v", tx.execSQL)
	}

	session.ReceivedChunks = 1
	session.ReceivedBytes = 4
	existing := Part{
		UploadID: session.ID, Index: 0, ByteSize: 4, SHA256: write.SHA256,
		BackendPartID: "stored-backend-id", CreatedAt: write.ObservedAt.Add(-time.Minute),
	}
	tx = &fakeUploadTx{queryRows: []pgx.Row{sessionRow(session), partRow(existing)}}
	store, _ = NewPostgresStore(&fakeUploadDatabase{tx: tx})
	receipt, err = store.RecordPart(context.Background(), write)
	if err != nil {
		t.Fatalf("RecordPart(retry) error = %v", err)
	}
	if !receipt.AlreadyPresent || receipt.Part.BackendPartID != existing.BackendPartID || len(tx.execSQL) != 0 || !tx.committed {
		t.Fatalf("RecordPart(retry) = %#v, SQL=%v", receipt, tx.execSQL)
	}
}

func TestPostgresStoreRejectsPartConflictOrderSizeAndExpiry(t *testing.T) {
	t.Parallel()

	base := testSession(t)
	valid := testPartWrite(t, base, 0, 4)
	conflicting := valid
	conflicting.SHA256[0]++
	tests := []struct {
		name    string
		session Session
		write   PartWrite
		rows    []pgx.Row
		want    error
	}{
		{
			name: "conflict", session: base, write: conflicting,
			rows: []pgx.Row{sessionRow(base), partRow(Part{UploadID: base.ID, Index: 0, ByteSize: 4, SHA256: valid.SHA256})},
			want: ErrPartConflict,
		},
		{
			name: "gap", session: base, write: testPartWrite(t, base, 1, 4),
			rows: []pgx.Row{sessionRow(base), errorRow(pgx.ErrNoRows)}, want: ErrPartOutOfOrder,
		},
		{
			name: "wrong size", session: base, write: testPartWrite(t, base, 0, 3),
			rows: []pgx.Row{sessionRow(base), errorRow(pgx.ErrNoRows)}, want: nil,
		},
		{
			name: "expired", session: expiredSession(base), write: valid,
			rows: []pgx.Row{sessionRow(expiredSession(base))}, want: ErrExpired,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			tx := &fakeUploadTx{queryRows: test.rows}
			store, _ := NewPostgresStore(&fakeUploadDatabase{tx: tx})
			_, err := store.RecordPart(context.Background(), test.write)
			if test.want != nil {
				if !errors.Is(err, test.want) {
					t.Fatalf("RecordPart() error = %v, want %v", err, test.want)
				}
			} else if err == nil || !strings.Contains(err.Error(), "exactly 4 bytes") {
				t.Fatalf("RecordPart() error = %v, want exact-size error", err)
			}
			if tx.committed || !tx.rolledBack || len(tx.execSQL) != 0 {
				t.Fatalf("rejected transaction commit=%v rollback=%v SQL=%v", tx.committed, tx.rolledBack, tx.execSQL)
			}
		})
	}
}

func TestPostgresStoreBeginCommitVerifiesOrderedParts(t *testing.T) {
	t.Parallel()

	session := testSession(t)
	session.ReceivedChunks = 3
	session.ReceivedBytes = 9
	parts := []Part{
		testPart(t, session, 0, 4),
		testPart(t, session, 1, 4),
		testPart(t, session, 2, 1),
	}
	tx := &fakeUploadTx{queryRows: []pgx.Row{sessionRow(session)}, rows: []pgx.Rows{&fakePartRows{parts: parts}}}
	store, _ := NewPostgresStore(&fakeUploadDatabase{tx: tx})
	plan, err := store.BeginCommit(context.Background(), session.ID, session.Owner, session.UpdatedAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("BeginCommit() error = %v", err)
	}
	if plan.Session.Status != StatusCommitting || len(plan.Parts) != 3 || len(tx.execSQL) != 1 || !tx.committed {
		t.Fatalf("BeginCommit() = %#v, SQL=%v commit=%v", plan, tx.execSQL, tx.committed)
	}

	tx = &fakeUploadTx{queryRows: []pgx.Row{sessionRow(session)}, rows: []pgx.Rows{&fakePartRows{parts: parts[:2]}}}
	store, _ = NewPostgresStore(&fakeUploadDatabase{tx: tx})
	if _, err := store.BeginCommit(context.Background(), session.ID, session.Owner, session.UpdatedAt.Add(time.Minute)); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("BeginCommit(incomplete) error = %v", err)
	}
	if tx.committed || !tx.rolledBack || len(tx.execSQL) != 0 {
		t.Fatalf("incomplete transaction commit=%v rollback=%v SQL=%v", tx.committed, tx.rolledBack, tx.execSQL)
	}
}

func TestPostgresStoreCompleteAndAbortAreIdempotent(t *testing.T) {
	t.Parallel()

	session := testSession(t)
	session.Status = StatusCommitting
	digest, _ := ParseSHA256(strings.Repeat("cd", 32))
	tx := &fakeUploadTx{queryRows: []pgx.Row{sessionRow(session)}}
	store, _ := NewPostgresStore(&fakeUploadDatabase{tx: tx})
	completed, err := store.Complete(context.Background(), session.ID, session.Owner, digest, session.UpdatedAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if completed.Status != StatusCommitted || completed.CommittedSHA256 == nil || *completed.CommittedSHA256 != digest || len(tx.execSQL) != 1 {
		t.Fatalf("Complete() = %#v, SQL=%v", completed, tx.execSQL)
	}

	session = completed
	tx = &fakeUploadTx{queryRows: []pgx.Row{sessionRow(session)}}
	store, _ = NewPostgresStore(&fakeUploadDatabase{tx: tx})
	if _, err := store.Complete(context.Background(), session.ID, session.Owner, digest, session.UpdatedAt.Add(time.Minute)); err != nil {
		t.Fatalf("Complete(retry) error = %v", err)
	}
	if len(tx.execSQL) != 0 || !tx.committed {
		t.Fatalf("Complete(retry) SQL=%v commit=%v", tx.execSQL, tx.committed)
	}

	session = testSession(t)
	tx = &fakeUploadTx{queryRows: []pgx.Row{sessionRow(session)}}
	store, _ = NewPostgresStore(&fakeUploadDatabase{tx: tx})
	aborted, err := store.Abort(context.Background(), session.ID, session.Owner, session.UpdatedAt.Add(time.Minute))
	if err != nil || aborted.Status != StatusAborted || len(tx.execSQL) != 1 {
		t.Fatalf("Abort() = %#v, %v, SQL=%v", aborted, err, tx.execSQL)
	}
	tx = &fakeUploadTx{queryRows: []pgx.Row{sessionRow(aborted)}}
	store, _ = NewPostgresStore(&fakeUploadDatabase{tx: tx})
	if _, err := store.Abort(context.Background(), aborted.ID, aborted.Owner, aborted.UpdatedAt.Add(time.Minute)); err != nil {
		t.Fatalf("Abort(retry) error = %v", err)
	}
	if len(tx.execSQL) != 0 || !tx.committed {
		t.Fatalf("Abort(retry) SQL=%v commit=%v", tx.execSQL, tx.committed)
	}
}

func TestPostgresStoreValidationAndSafeErrors(t *testing.T) {
	t.Parallel()

	if _, err := NewPostgresStore(nil); err == nil {
		t.Fatal("NewPostgresStore() accepted nil")
	}
	database := &fakeUploadDatabase{}
	store, _ := NewPostgresStore(database)
	if _, err := store.Get(context.Background(), "invalid", Owner{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get() error = %v", err)
	}
	if database.beginCalls != 0 {
		t.Fatal("invalid operation reached the database")
	}

	cause := errors.New("sensitive database details")
	store, _ = NewPostgresStore(&fakeUploadDatabase{beginErr: cause})
	write := testPartWrite(t, testSession(t), 0, 4)
	_, err := store.RecordPart(context.Background(), write)
	if !errors.Is(err, cause) || strings.Contains(err.Error(), cause.Error()) {
		t.Fatalf("storage error = %v", err)
	}
}

func testSession(t *testing.T) Session {
	t.Helper()
	owner, err := NewAuthenticatedOwner(42)
	if err != nil {
		t.Fatalf("NewAuthenticatedOwner() error = %v", err)
	}
	now := time.Date(2026, 9, 23, 8, 0, 0, 0, time.UTC)
	return Session{
		ID: "11111111-1111-4111-8111-111111111111", Owner: owner,
		OriginalFilename: "report.pdf", ContentType: "application/pdf",
		DeclaredSize: 9, ChunkSize: 4, ExpectedChunks: 3,
		Status: StatusUploading, ObjectKey: "uploads/11111111-1111-4111-8111-111111111111/source",
		CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(24 * time.Hour),
	}
}

func expiredSession(session Session) Session {
	session.ExpiresAt = session.CreatedAt.Add(30 * time.Second)
	return session
}

func testPartWrite(t *testing.T, session Session, index int32, size int64) PartWrite {
	t.Helper()
	digest, err := ParseSHA256(strings.Repeat("ab", 32))
	if err != nil {
		t.Fatalf("ParseSHA256() error = %v", err)
	}
	return PartWrite{
		UploadID: session.ID, Owner: session.Owner, Index: index, ByteSize: size,
		SHA256: digest, BackendPartID: "backend-part", ObservedAt: session.UpdatedAt.Add(time.Minute),
	}
}

func testPart(t *testing.T, session Session, index int32, size int64) Part {
	t.Helper()
	write := testPartWrite(t, session, index, size)
	return Part{
		UploadID: write.UploadID, Index: index, ByteSize: size, SHA256: write.SHA256,
		BackendPartID: write.BackendPartID, CreatedAt: write.ObservedAt,
	}
}

type fakeUploadDatabase struct {
	tx         pgx.Tx
	beginErr   error
	beginCalls int
	queryRow   func(context.Context, string, ...any) pgx.Row
}

func (database *fakeUploadDatabase) Begin(context.Context) (pgx.Tx, error) {
	database.beginCalls++
	return database.tx, database.beginErr
}

func (database *fakeUploadDatabase) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if database.queryRow == nil {
		return errorRow(errors.New("unexpected database QueryRow"))
	}
	return database.queryRow(ctx, sql, args...)
}

type fakeUploadTx struct {
	pgx.Tx
	queryRows  []pgx.Row
	rows       []pgx.Rows
	execSQL    []string
	committed  bool
	rolledBack bool
}

func (tx *fakeUploadTx) QueryRow(_ context.Context, _ string, _ ...any) pgx.Row {
	if len(tx.queryRows) == 0 {
		return errorRow(errors.New("unexpected transaction QueryRow"))
	}
	row := tx.queryRows[0]
	tx.queryRows = tx.queryRows[1:]
	return row
}

func (tx *fakeUploadTx) Query(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
	if len(tx.rows) == 0 {
		return nil, errors.New("unexpected transaction Query")
	}
	rows := tx.rows[0]
	tx.rows = tx.rows[1:]
	return rows, nil
}

func (tx *fakeUploadTx) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	tx.execSQL = append(tx.execSQL, sql)
	return pgconn.NewCommandTag("UPDATE 1"), nil
}

func (tx *fakeUploadTx) Commit(context.Context) error {
	tx.committed = true
	return nil
}

func (tx *fakeUploadTx) Rollback(context.Context) error {
	tx.rolledBack = true
	return nil
}

type scanRow func(...any) error

func (row scanRow) Scan(destinations ...any) error {
	return row(destinations...)
}

func errorRow(err error) pgx.Row {
	return scanRow(func(...any) error { return err })
}

func sessionRow(session Session) pgx.Row {
	return scanRow(func(destinations ...any) error {
		if len(destinations) != 19 {
			return errors.New("unexpected session destination count")
		}
		*(destinations[0].(*string)) = session.ID
		*(destinations[1].(*string)) = string(session.Owner.Kind)
		if session.Owner.Kind == OwnerAuthenticated {
			value := session.Owner.UserID
			*(destinations[2].(**int64)) = &value
		} else {
			value := session.Owner.AnonymousSessionID
			*(destinations[3].(**string)) = &value
		}
		*(destinations[4].(*string)) = session.OriginalFilename
		*(destinations[5].(*string)) = session.ContentType
		*(destinations[6].(*int64)) = session.DeclaredSize
		*(destinations[7].(*int32)) = int32(session.ChunkSize)
		*(destinations[8].(*int32)) = session.ExpectedChunks
		*(destinations[9].(*int32)) = session.ReceivedChunks
		*(destinations[10].(*int64)) = session.ReceivedBytes
		*(destinations[11].(*string)) = string(session.Status)
		*(destinations[12].(*string)) = session.ObjectKey
		if session.MultipartUploadID != "" {
			value := session.MultipartUploadID
			*(destinations[13].(**string)) = &value
		}
		if session.CommittedSHA256 != nil {
			value := session.CommittedSHA256.String()
			*(destinations[14].(**string)) = &value
		}
		*(destinations[15].(*time.Time)) = session.CreatedAt
		*(destinations[16].(*time.Time)) = session.UpdatedAt
		*(destinations[17].(**time.Time)) = session.CommittedAt
		*(destinations[18].(*time.Time)) = session.ExpiresAt
		return nil
	})
}

func partRow(part Part) pgx.Row {
	return scanRow(func(destinations ...any) error {
		return scanPart(destinations, part)
	})
}

func scanPart(destinations []any, part Part) error {
	if len(destinations) != 6 {
		return errors.New("unexpected part destination count")
	}
	*(destinations[0].(*string)) = part.UploadID
	*(destinations[1].(*int32)) = part.Index
	*(destinations[2].(*int64)) = part.ByteSize
	*(destinations[3].(*string)) = part.SHA256.String()
	*(destinations[4].(*string)) = part.BackendPartID
	*(destinations[5].(*time.Time)) = part.CreatedAt
	return nil
}

type fakePartRows struct {
	pgx.Rows
	parts   []Part
	current int
	closed  bool
}

func (rows *fakePartRows) Close() {
	rows.closed = true
}

func (*fakePartRows) Err() error {
	return nil
}

func (rows *fakePartRows) Next() bool {
	return rows.current < len(rows.parts)
}

func (rows *fakePartRows) Scan(destinations ...any) error {
	if rows.current >= len(rows.parts) {
		return errors.New("scan without current part")
	}
	part := rows.parts[rows.current]
	rows.current++
	return scanPart(destinations, part)
}
