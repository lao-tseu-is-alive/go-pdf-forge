package config

import (
	"fmt"
	"math"
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
	if cfg.UploadSessionTTL != 24*time.Hour {
		t.Errorf("UploadSessionTTL = %v", cfg.UploadSessionTTL)
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
	wantQuotas := AnonymousQuotas{
		Window:            24 * time.Hour,
		SessionsPerIP:     20,
		UploadsPerSession: 10,
		UploadsPerIP:      50,
		JobsPerSession:    10,
		JobsPerIP:         50,
		BytesPerSession:   1024 * 1024 * 1024,
		BytesPerIP:        5 * 1024 * 1024 * 1024,
	}
	if cfg.Anonymous.Quotas != wantQuotas {
		t.Errorf("Anonymous.Quotas = %#v, want %#v", cfg.Anonymous.Quotas, wantQuotas)
	}
}

func TestLoadAnonymousQuotaOverrides(t *testing.T) {
	t.Parallel()

	values := validEnvironment()
	values["ANONYMOUS_QUOTA_WINDOW"] = "6h"
	values["ANONYMOUS_QUOTA_SESSIONS_PER_IP"] = "3"
	values["ANONYMOUS_QUOTA_UPLOADS_PER_SESSION"] = "4"
	values["ANONYMOUS_QUOTA_UPLOADS_PER_IP"] = "5"
	values["ANONYMOUS_QUOTA_JOBS_PER_SESSION"] = "6"
	values["ANONYMOUS_QUOTA_JOBS_PER_IP"] = "7"
	values["ANONYMOUS_QUOTA_BYTES_PER_SESSION"] = "800"
	values["ANONYMOUS_QUOTA_BYTES_PER_IP"] = "900"

	cfg, err := Load(mapLookup(values))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := AnonymousQuotas{
		Window:            6 * time.Hour,
		SessionsPerIP:     3,
		UploadsPerSession: 4,
		UploadsPerIP:      5,
		JobsPerSession:    6,
		JobsPerIP:         7,
		BytesPerSession:   800,
		BytesPerIP:        900,
	}
	if cfg.Anonymous.Quotas != want {
		t.Fatalf("Anonymous.Quotas = %#v, want %#v", cfg.Anonymous.Quotas, want)
	}
}

func TestLoadUploadSessionTTLOverride(t *testing.T) {
	t.Parallel()

	values := validEnvironment()
	values["UPLOAD_SESSION_TTL"] = "6h"
	cfg, err := Load(mapLookup(values))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.UploadSessionTTL != 6*time.Hour {
		t.Fatalf("UploadSessionTTL = %v", cfg.UploadSessionTTL)
	}
}

func TestLoadRejectsInvalidUploadSettings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		values map[string]string
		want   string
	}{
		{name: "zero TTL", values: map[string]string{"UPLOAD_SESSION_TTL": "0s"}, want: "UPLOAD_SESSION_TTL"},
		{name: "chunk integer overflow", values: map[string]string{
			"MAX_UPLOAD_BYTES":   "4294967296",
			"UPLOAD_CHUNK_BYTES": "2147483648",
		}, want: "UPLOAD_CHUNK_BYTES"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			values := validEnvironment()
			for key, value := range test.values {
				values[key] = value
			}
			_, err := Load(mapLookup(values))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Load() error = %v, want reference to %s", err, test.want)
			}
		})
	}
}

func TestLoadRejectsInvalidAnonymousQuotas(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "short window", key: "ANONYMOUS_QUOTA_WINDOW", value: "59s"},
		{name: "fractional window", key: "ANONYMOUS_QUOTA_WINDOW", value: "1m500ms"},
		{name: "zero count", key: "ANONYMOUS_QUOTA_UPLOADS_PER_IP", value: "0"},
		{name: "count overflow", key: "ANONYMOUS_QUOTA_JOBS_PER_SESSION", value: fmt.Sprint(int64(math.MaxInt32) + 1)},
		{name: "negative bytes", key: "ANONYMOUS_QUOTA_BYTES_PER_SESSION", value: "-1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			values := validEnvironment()
			values[test.key] = test.value
			_, err := Load(mapLookup(values))
			if err == nil || !strings.Contains(err.Error(), test.key) {
				t.Fatalf("Load() error = %v, want reference to %s", err, test.key)
			}
		})
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

func validEnvironment() map[string]string {
	return map[string]string{
		"ANONYMOUS_TOKEN_PEPPER": strings.Repeat("p", 32),
		"DB_PASSWORD":            "database-secret",
		"S3_ACCESS_KEY_ID":       "access-key",
		"S3_SECRET_ACCESS_KEY":   "object-secret",
	}
}
