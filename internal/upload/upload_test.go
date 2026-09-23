package upload

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestManagerStartBuildsBoundedOpaqueSession(t *testing.T) {
	t.Parallel()

	store := &memoryStore{}
	manager, err := NewManager(store, 256, 4, 24*time.Hour)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	manager.random = bytes.NewReader(make([]byte, 16))
	now := time.Date(2026, 9, 23, 10, 0, 0, 0, time.FixedZone("CEST", 2*60*60))
	manager.now = func() time.Time { return now }
	owner, _ := NewAuthenticatedOwner(42)

	created, err := manager.Start(context.Background(), owner, `C:\fakepath\ report.pdf `, "Application/PDF", 9)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if created.ID != "00000000-0000-4000-8000-000000000000" {
		t.Errorf("ID = %q", created.ID)
	}
	if created.OriginalFilename != "report.pdf" || created.ContentType != "application/pdf" {
		t.Errorf("metadata = %q, %q", created.OriginalFilename, created.ContentType)
	}
	if created.ExpectedChunks != 3 || created.ChunkSize != 4 || created.DeclaredSize != 9 {
		t.Errorf("geometry = size %d chunk %d count %d", created.DeclaredSize, created.ChunkSize, created.ExpectedChunks)
	}
	if created.ObjectKey != "uploads/00000000-0000-4000-8000-000000000000/source" || strings.Contains(created.ObjectKey, "report.pdf") {
		t.Errorf("object key = %q", created.ObjectKey)
	}
	if created.CreatedAt != now.UTC() || created.ExpiresAt != now.UTC().Add(24*time.Hour) {
		t.Errorf("timestamps = created %v expires %v", created.CreatedAt, created.ExpiresAt)
	}
	if store.created != created {
		t.Fatal("Start() did not persist the generated session")
	}
}

func TestManagerRejectsInvalidConfigurationAndStart(t *testing.T) {
	t.Parallel()

	store := &memoryStore{}
	if _, err := NewManager(nil, 10, 5, time.Hour); err == nil {
		t.Fatal("NewManager() accepted nil store")
	}
	if _, err := NewManager(store, 0, 5, time.Hour); err == nil {
		t.Fatal("NewManager() accepted zero maximum")
	}
	if _, err := NewManager(store, 10, 11, time.Hour); err == nil {
		t.Fatal("NewManager() accepted chunk above maximum")
	}
	if _, err := NewManager(store, 10, 5, 0); err == nil {
		t.Fatal("NewManager() accepted zero TTL")
	}
	manager, _ := NewManager(store, 10, 5, time.Hour)
	owner, _ := NewAuthenticatedOwner(42)
	for _, test := range []struct {
		name        string
		filename    string
		contentType string
		size        int64
	}{
		{name: "empty size", filename: "file.pdf", contentType: "application/pdf", size: 0},
		{name: "oversize", filename: "file.pdf", contentType: "application/pdf", size: 11},
		{name: "invalid filename", filename: "\x00.pdf", contentType: "application/pdf", size: 1},
		{name: "invalid content type", filename: "file.pdf", contentType: "not a type", size: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := manager.Start(context.Background(), owner, test.filename, test.contentType, test.size); err == nil {
				t.Fatal("Start() error = nil")
			}
		})
	}
}

func TestOwnerValidation(t *testing.T) {
	t.Parallel()

	if _, err := NewAuthenticatedOwner(0); err == nil {
		t.Fatal("NewAuthenticatedOwner() accepted zero")
	}
	if _, err := NewAnonymousOwner("invalid"); err == nil {
		t.Fatal("NewAnonymousOwner() accepted malformed UUID")
	}
	owner, err := NewAnonymousOwner("11111111-1111-4111-8111-111111111111")
	if err != nil || owner.Kind != OwnerAnonymous {
		t.Fatalf("NewAnonymousOwner() = %#v, %v", owner, err)
	}
}

func TestSHA256CanonicalParsing(t *testing.T) {
	t.Parallel()

	value := strings.Repeat("ab", 32)
	digest, err := ParseSHA256(value)
	if err != nil {
		t.Fatalf("ParseSHA256() error = %v", err)
	}
	if digest.String() != value {
		t.Fatalf("String() = %q", digest.String())
	}
	for _, invalid := range []string{"", strings.Repeat("a", 63), strings.Repeat("AB", 32), strings.Repeat("z", 64)} {
		if _, err := ParseSHA256(invalid); err == nil {
			t.Errorf("ParseSHA256(%q) error = nil", invalid)
		}
	}
}

func TestSanitizeFilename(t *testing.T) {
	t.Parallel()

	for input, want := range map[string]string{
		"report.pdf":                 "report.pdf",
		" ../../private/report.pdf ": "report.pdf",
		`C:\fakepath\report.pdf`:     "report.pdf",
	} {
		got, err := SanitizeFilename(input)
		if err != nil || got != want {
			t.Errorf("SanitizeFilename(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	for _, invalid := range []string{"", ".", "..", "\x00.pdf", strings.Repeat("a", maxFilenameBytes+1)} {
		if _, err := SanitizeFilename(invalid); err == nil {
			t.Errorf("SanitizeFilename(%q) error = nil", invalid)
		}
	}
}

type memoryStore struct {
	created Session
}

func (store *memoryStore) Create(_ context.Context, session Session) (Session, error) {
	store.created = session
	return session, nil
}

func (*memoryStore) Get(context.Context, string, Owner) (Session, error) {
	return Session{}, errors.New("unexpected Get")
}

func (*memoryStore) RecordPart(context.Context, PartWrite) (PartReceipt, error) {
	return PartReceipt{}, errors.New("unexpected RecordPart")
}

func (*memoryStore) BeginCommit(context.Context, string, Owner, time.Time) (CommitPlan, error) {
	return CommitPlan{}, errors.New("unexpected BeginCommit")
}

func (*memoryStore) Complete(context.Context, string, Owner, SHA256, time.Time) (Session, error) {
	return Session{}, errors.New("unexpected Complete")
}

func (*memoryStore) Abort(context.Context, string, Owner, time.Time) (Session, error) {
	return Session{}, errors.New("unexpected Abort")
}

var _ Store = (*memoryStore)(nil)
