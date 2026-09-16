package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := Load(mapLookup(map[string]string{
		"ANONYMOUS_TOKEN_PEPPER": strings.Repeat("p", 32),
		"DB_PASSWORD":            "database-secret",
		"S3_ACCESS_KEY_ID":       "access-key",
		"S3_SECRET_ACCESS_KEY":   "object-secret",
	}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Auth != AuthOptional {
		t.Errorf("Auth = %q, want %q", cfg.Auth, AuthOptional)
	}
	if cfg.MaxUploadBytes != 256*1024*1024 {
		t.Errorf("MaxUploadBytes = %d", cfg.MaxUploadBytes)
	}
	if cfg.UploadChunkBytes != 8*1024*1024 {
		t.Errorf("UploadChunkBytes = %d", cfg.UploadChunkBytes)
	}
	if cfg.PDFTargetBytes != 75*1024*1024 {
		t.Errorf("PDFTargetBytes = %d", cfg.PDFTargetBytes)
	}
	if cfg.JobRetention != 48*time.Hour {
		t.Errorf("JobRetention = %v", cfg.JobRetention)
	}
	if cfg.WorkerConcurrency != 1 {
		t.Errorf("WorkerConcurrency = %d", cfg.WorkerConcurrency)
	}
	if cfg.Database.ConnectTimeout != 5*time.Second || cfg.Database.HealthTimeout != 2*time.Second {
		t.Errorf("database timeouts = connect %v, health %v", cfg.Database.ConnectTimeout, cfg.Database.HealthTimeout)
	}
	if cfg.Database.MigrationTimeout != 2*time.Minute {
		t.Errorf("database migration timeout = %v", cfg.Database.MigrationTimeout)
	}
	if cfg.Database.MaxConnections != 10 || cfg.Database.MinConnections != 0 {
		t.Errorf("database pool bounds = %d..%d", cfg.Database.MinConnections, cfg.Database.MaxConnections)
	}
}

func TestLoadDatabaseDoesNotRequireUnrelatedSecrets(t *testing.T) {
	t.Parallel()

	database, err := LoadDatabase(mapLookup(map[string]string{"DB_PASSWORD": "database-secret"}))
	if err != nil {
		t.Fatalf("LoadDatabase() error = %v", err)
	}
	if database.Name != "go_pdf_forge" || database.User != "go_pdf_forge" {
		t.Errorf("database identity = %s/%s", database.User, database.Name)
	}
}

func TestLoadDatabaseValidatesPoolBounds(t *testing.T) {
	t.Parallel()

	_, err := LoadDatabase(mapLookup(map[string]string{
		"DB_PASSWORD":        "database-secret",
		"DB_MAX_CONNECTIONS": "2",
		"DB_MIN_CONNECTIONS": "3",
	}))
	if err == nil || !strings.Contains(err.Error(), "DB_MIN_CONNECTIONS") {
		t.Fatalf("LoadDatabase() error = %v", err)
	}
}

func TestLoadRejectsInvalidValuesWithoutLeakingSecrets(t *testing.T) {
	t.Parallel()

	const databaseSecret = "super-secret-database-value"
	_, err := Load(mapLookup(map[string]string{
		"ANONYMOUS_TOKEN_PEPPER": strings.Repeat("p", 32),
		"AUTH_MODE":              "sometimes",
		"MAX_UPLOAD_BYTES":       "10",
		"UPLOAD_CHUNK_BYTES":     "20",
		"DB_PASSWORD":            databaseSecret,
		"S3_ACCESS_KEY_ID":       "access-key",
		"S3_SECRET_ACCESS_KEY":   "object-secret",
	}))
	if err == nil {
		t.Fatal("Load() error = nil")
	}
	if strings.Contains(err.Error(), databaseSecret) {
		t.Fatal("validation error leaked the database password")
	}
}

func TestLoadRequiresSecrets(t *testing.T) {
	t.Parallel()

	_, err := Load(mapLookup(nil))
	if err == nil {
		t.Fatal("Load() error = nil")
	}
	for _, expected := range []string{"ANONYMOUS_TOKEN_PEPPER", "DB_PASSWORD", "S3_ACCESS_KEY_ID", "S3_SECRET_ACCESS_KEY"} {
		if !strings.Contains(err.Error(), expected) {
			t.Errorf("Load() error %q does not mention %s", err, expected)
		}
	}
}

func mapLookup(values map[string]string) LookupEnv {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}
