// Package config parses and validates process configuration without loading
// dotenv files. Secret values are intentionally kept out of error messages.
package config

import (
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultMaxUploadBytes int64 = 256 * 1024 * 1024
	defaultChunkBytes     int64 = 8 * 1024 * 1024
	defaultTargetBytes    int64 = 75 * 1024 * 1024
)

const (
	defaultAnonymousQuotaWindow                  = 24 * time.Hour
	defaultAnonymousQuotaSessionsPerIP     int64 = 20
	defaultAnonymousQuotaUploadsPerSession int64 = 10
	defaultAnonymousQuotaUploadsPerIP      int64 = 50
	defaultAnonymousQuotaJobsPerSession    int64 = 10
	defaultAnonymousQuotaJobsPerIP         int64 = 50
	defaultAnonymousQuotaBytesPerSession   int64 = 1024 * 1024 * 1024
	defaultAnonymousQuotaBytesPerIP        int64 = 5 * 1024 * 1024 * 1024
)

// AuthMode controls whether authenticated and anonymous identities are accepted.
type AuthMode string

const (
	// AuthRequired accepts only requests carrying a valid employee JWT.
	AuthRequired AuthMode = "required"
	// AuthOptional accepts a valid employee JWT or an anonymous capability.
	AuthOptional AuthMode = "optional"
	// AuthAnonymous disables login and requires anonymous capabilities.
	AuthAnonymous AuthMode = "anonymous"
)

// LookupEnv abstracts environment access so configuration parsing is testable
// without mutating the process environment.
type LookupEnv func(string) (string, bool)

// Config contains the complete validated runtime configuration shared by API
// and worker processes. Secret fields must never be logged as a whole value.
type Config struct {
	// Auth selects the accepted identity modes.
	Auth AuthMode
	// MaxUploadBytes is the maximum declared size of one source PDF.
	MaxUploadBytes int64
	// UploadChunkBytes is the chunk size browsers should use for uploads.
	UploadChunkBytes int64
	// PDFTargetBytes is the indicative best-effort output-size target.
	PDFTargetBytes int64
	// JobRetention determines when completed objects and metadata expire.
	JobRetention time.Duration
	// WorkerConcurrency bounds simultaneous PDF-processing jobs per worker.
	WorkerConcurrency int
	// Anonymous contains capability-security and lifetime settings.
	Anonymous Anonymous
	// Database contains PostgreSQL connection-pool settings.
	Database Database
	// ObjectStore contains S3-compatible storage settings.
	ObjectStore ObjectStore
}

// Anonymous contains configuration for anonymous session capabilities.
type Anonymous struct {
	// TokenPepper is the deployment secret used to HMAC capability secrets.
	TokenPepper []byte
	// SessionTTL is the validity period assigned to a new anonymous session.
	SessionTTL time.Duration
	// Quotas contains PostgreSQL-backed anonymous usage limits.
	Quotas AnonymousQuotas
}

// AnonymousQuotas defines one fixed-window limit policy for public traffic.
type AnonymousQuotas struct {
	// Window is the UTC-aligned duration shared by all anonymous counters.
	Window time.Duration
	// SessionsPerIP limits session creation attempts from one IP digest.
	SessionsPerIP int64
	// UploadsPerSession limits started uploads for one anonymous session.
	UploadsPerSession int64
	// UploadsPerIP limits started uploads across sessions sharing an IP digest.
	UploadsPerIP int64
	// JobsPerSession limits created PDF jobs for one anonymous session.
	JobsPerSession int64
	// JobsPerIP limits created PDF jobs across sessions sharing an IP digest.
	JobsPerIP int64
	// BytesPerSession limits committed source bytes for one anonymous session.
	BytesPerSession int64
	// BytesPerIP limits committed source bytes across sessions sharing an IP digest.
	BytesPerIP int64
}

// Database contains PostgreSQL connection, timeout, and pool settings.
type Database struct {
	// Driver identifies the supported database implementation; it must be postgres.
	Driver string
	// Host is the PostgreSQL server hostname or IP address.
	Host string
	// Port is the PostgreSQL TCP port.
	Port int
	// Name is the PostgreSQL database name.
	Name string
	// User is the PostgreSQL login role.
	User string
	// Password is the PostgreSQL credential and must never be logged.
	Password string
	// SSLMode is the pgx TLS verification mode.
	SSLMode string
	// ConnectTimeout bounds initial pool creation and connectivity checks.
	ConnectTimeout time.Duration
	// HealthTimeout bounds an individual readiness ping.
	HealthTimeout time.Duration
	// MigrationTimeout bounds one complete migration transaction.
	MigrationTimeout time.Duration
	// MaxConnections is the upper bound of connections in one process pool.
	MaxConnections int
	// MinConnections is the number of idle connections the pool tries to retain.
	MinConnections int
	// MaxConnectionAge retires connections after this lifetime.
	MaxConnectionAge time.Duration
	// MaxConnectionIdle retires connections idle longer than this duration.
	MaxConnectionIdle time.Duration
	// HealthCheckPeriod controls how often pgx checks idle connections.
	HealthCheckPeriod time.Duration
}

// ObjectStore contains credentials and addressing for the S3-compatible blob
// store. SecretAccessKey must never be logged.
type ObjectStore struct {
	// Endpoint is the absolute HTTP(S) endpoint of the S3-compatible service.
	Endpoint string
	// Region is the S3 signing region.
	Region string
	// Bucket contains temporary PDF inputs and results.
	Bucket string
	// AccessKeyID identifies the S3 credential.
	AccessKeyID string
	// SecretAccessKey authenticates the S3 credential and must never be logged.
	SecretAccessKey string
	// PathStyle forces bucket names into URL paths for Garage compatibility.
	PathStyle bool
}

// Load parses and validates the complete process configuration. It does not
// read dotenv files; callers decide how environment variables are populated.
func Load(lookup LookupEnv) (Config, error) {
	if lookup == nil {
		return Config{}, errors.New("environment lookup is required")
	}

	auth, err := parseAuthMode(value(lookup, "AUTH_MODE", string(AuthOptional)))
	if err != nil {
		return Config{}, err
	}
	maxUpload, err := int64Value(lookup, "MAX_UPLOAD_BYTES", defaultMaxUploadBytes)
	if err != nil {
		return Config{}, err
	}
	chunkSize, err := int64Value(lookup, "UPLOAD_CHUNK_BYTES", defaultChunkBytes)
	if err != nil {
		return Config{}, err
	}
	targetSize, err := int64Value(lookup, "PDF_TARGET_BYTES", defaultTargetBytes)
	if err != nil {
		return Config{}, err
	}
	retention, err := durationValue(lookup, "JOB_RETENTION", 48*time.Hour)
	if err != nil {
		return Config{}, err
	}
	workerConcurrency, err := intValue(lookup, "WORKER_CONCURRENCY", 1)
	if err != nil {
		return Config{}, err
	}
	anonymousSessionTTL, err := durationValue(lookup, "ANONYMOUS_SESSION_TTL", 48*time.Hour)
	if err != nil {
		return Config{}, err
	}
	anonymousQuotas, err := anonymousQuotasFromEnvironment(lookup)
	if err != nil {
		return Config{}, err
	}
	database, err := databaseFromEnvironment(lookup)
	if err != nil {
		return Config{}, err
	}
	pathStyle, err := boolValue(lookup, "S3_PATH_STYLE", true)
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		Auth:              auth,
		MaxUploadBytes:    maxUpload,
		UploadChunkBytes:  chunkSize,
		PDFTargetBytes:    targetSize,
		JobRetention:      retention,
		WorkerConcurrency: workerConcurrency,
		Anonymous: Anonymous{
			TokenPepper: []byte(value(lookup, "ANONYMOUS_TOKEN_PEPPER", "")),
			SessionTTL:  anonymousSessionTTL,
			Quotas:      anonymousQuotas,
		},
		Database: database,
		ObjectStore: ObjectStore{
			Endpoint:        value(lookup, "S3_ENDPOINT", "http://127.0.0.1:3900"),
			Region:          value(lookup, "S3_REGION", "garage"),
			Bucket:          value(lookup, "S3_BUCKET", "go-pdf-forge"),
			AccessKeyID:     value(lookup, "S3_ACCESS_KEY_ID", ""),
			SecretAccessKey: value(lookup, "S3_SECRET_ACCESS_KEY", ""),
			PathStyle:       pathStyle,
		},
	}
	return cfg, cfg.Validate()
}

// Validate reports every invalid complete-runtime setting without including
// secret values in its errors.
func (cfg Config) Validate() error {
	var problems []error
	if _, err := parseAuthMode(string(cfg.Auth)); err != nil {
		problems = append(problems, err)
	}
	if cfg.MaxUploadBytes <= 0 {
		problems = append(problems, errors.New("MAX_UPLOAD_BYTES must be positive"))
	}
	if cfg.UploadChunkBytes <= 0 {
		problems = append(problems, errors.New("UPLOAD_CHUNK_BYTES must be positive"))
	}
	if cfg.MaxUploadBytes > 0 && cfg.UploadChunkBytes > cfg.MaxUploadBytes {
		problems = append(problems, errors.New("UPLOAD_CHUNK_BYTES must not exceed MAX_UPLOAD_BYTES"))
	}
	if cfg.PDFTargetBytes <= 0 {
		problems = append(problems, errors.New("PDF_TARGET_BYTES must be positive"))
	}
	if cfg.JobRetention <= 0 {
		problems = append(problems, errors.New("JOB_RETENTION must be positive"))
	}
	if cfg.WorkerConcurrency <= 0 {
		problems = append(problems, errors.New("WORKER_CONCURRENCY must be positive"))
	}
	if cfg.Auth != AuthRequired && len(cfg.Anonymous.TokenPepper) < 32 {
		problems = append(problems, errors.New("ANONYMOUS_TOKEN_PEPPER must contain at least 32 bytes"))
	}
	if cfg.Anonymous.SessionTTL <= 0 {
		problems = append(problems, errors.New("ANONYMOUS_SESSION_TTL must be positive"))
	}
	problems = append(problems, cfg.Anonymous.Quotas.validate()...)
	problems = append(problems, cfg.Database.validate()...)
	endpoint, err := url.Parse(cfg.ObjectStore.Endpoint)
	if err != nil || endpoint.Scheme == "" || endpoint.Host == "" {
		problems = append(problems, errors.New("S3_ENDPOINT must be an absolute URL"))
	} else if endpoint.Scheme != "http" && endpoint.Scheme != "https" {
		problems = append(problems, errors.New("S3_ENDPOINT scheme must be http or https"))
	}
	if strings.TrimSpace(cfg.ObjectStore.Region) == "" {
		problems = append(problems, errors.New("S3_REGION is required"))
	}
	if strings.TrimSpace(cfg.ObjectStore.Bucket) == "" {
		problems = append(problems, errors.New("S3_BUCKET is required"))
	}
	if cfg.ObjectStore.AccessKeyID == "" {
		problems = append(problems, errors.New("S3_ACCESS_KEY_ID is required"))
	}
	if cfg.ObjectStore.SecretAccessKey == "" {
		problems = append(problems, errors.New("S3_SECRET_ACCESS_KEY is required"))
	}
	return errors.Join(problems...)
}

func anonymousQuotasFromEnvironment(lookup LookupEnv) (AnonymousQuotas, error) {
	window, err := durationValue(lookup, "ANONYMOUS_QUOTA_WINDOW", defaultAnonymousQuotaWindow)
	if err != nil {
		return AnonymousQuotas{}, err
	}
	values := []struct {
		name     string
		fallback int64
		target   *int64
	}{
		{name: "ANONYMOUS_QUOTA_SESSIONS_PER_IP", fallback: defaultAnonymousQuotaSessionsPerIP},
		{name: "ANONYMOUS_QUOTA_UPLOADS_PER_SESSION", fallback: defaultAnonymousQuotaUploadsPerSession},
		{name: "ANONYMOUS_QUOTA_UPLOADS_PER_IP", fallback: defaultAnonymousQuotaUploadsPerIP},
		{name: "ANONYMOUS_QUOTA_JOBS_PER_SESSION", fallback: defaultAnonymousQuotaJobsPerSession},
		{name: "ANONYMOUS_QUOTA_JOBS_PER_IP", fallback: defaultAnonymousQuotaJobsPerIP},
		{name: "ANONYMOUS_QUOTA_BYTES_PER_SESSION", fallback: defaultAnonymousQuotaBytesPerSession},
		{name: "ANONYMOUS_QUOTA_BYTES_PER_IP", fallback: defaultAnonymousQuotaBytesPerIP},
	}
	quotas := AnonymousQuotas{Window: window}
	values[0].target = &quotas.SessionsPerIP
	values[1].target = &quotas.UploadsPerSession
	values[2].target = &quotas.UploadsPerIP
	values[3].target = &quotas.JobsPerSession
	values[4].target = &quotas.JobsPerIP
	values[5].target = &quotas.BytesPerSession
	values[6].target = &quotas.BytesPerIP
	for _, item := range values {
		parsed, err := int64Value(lookup, item.name, item.fallback)
		if err != nil {
			return AnonymousQuotas{}, err
		}
		*item.target = parsed
	}
	return quotas, nil
}

func (quotas AnonymousQuotas) validate() []error {
	var problems []error
	if quotas.Window < time.Minute || quotas.Window%time.Second != 0 {
		problems = append(problems, errors.New("ANONYMOUS_QUOTA_WINDOW must be at least one minute and use whole seconds"))
	}
	countLimits := []struct {
		name  string
		value int64
	}{
		{name: "ANONYMOUS_QUOTA_SESSIONS_PER_IP", value: quotas.SessionsPerIP},
		{name: "ANONYMOUS_QUOTA_UPLOADS_PER_SESSION", value: quotas.UploadsPerSession},
		{name: "ANONYMOUS_QUOTA_UPLOADS_PER_IP", value: quotas.UploadsPerIP},
		{name: "ANONYMOUS_QUOTA_JOBS_PER_SESSION", value: quotas.JobsPerSession},
		{name: "ANONYMOUS_QUOTA_JOBS_PER_IP", value: quotas.JobsPerIP},
	}
	for _, limit := range countLimits {
		if limit.value <= 0 || limit.value > math.MaxInt32 {
			problems = append(problems, fmt.Errorf("%s must be between 1 and %d", limit.name, math.MaxInt32))
		}
	}
	byteLimits := []struct {
		name  string
		value int64
	}{
		{name: "ANONYMOUS_QUOTA_BYTES_PER_SESSION", value: quotas.BytesPerSession},
		{name: "ANONYMOUS_QUOTA_BYTES_PER_IP", value: quotas.BytesPerIP},
	}
	for _, limit := range byteLimits {
		if limit.value <= 0 {
			problems = append(problems, fmt.Errorf("%s must be positive", limit.name))
		}
	}
	return problems
}

// LoadDatabase parses only PostgreSQL settings. Migration and diagnostic
// binaries use it so they do not require unrelated object-store credentials.
func LoadDatabase(lookup LookupEnv) (Database, error) {
	if lookup == nil {
		return Database{}, errors.New("environment lookup is required")
	}
	database, err := databaseFromEnvironment(lookup)
	if err != nil {
		return Database{}, err
	}
	return database, errors.Join(database.validate()...)
}

func databaseFromEnvironment(lookup LookupEnv) (Database, error) {
	port, err := intValue(lookup, "DB_PORT", 5432)
	if err != nil {
		return Database{}, err
	}
	connectTimeout, err := durationValue(lookup, "DB_CONNECT_TIMEOUT", 5*time.Second)
	if err != nil {
		return Database{}, err
	}
	healthTimeout, err := durationValue(lookup, "DB_HEALTH_TIMEOUT", 2*time.Second)
	if err != nil {
		return Database{}, err
	}
	migrationTimeout, err := durationValue(lookup, "DB_MIGRATION_TIMEOUT", 2*time.Minute)
	if err != nil {
		return Database{}, err
	}
	maxConnections, err := intValue(lookup, "DB_MAX_CONNECTIONS", 10)
	if err != nil {
		return Database{}, err
	}
	minConnections, err := intValue(lookup, "DB_MIN_CONNECTIONS", 0)
	if err != nil {
		return Database{}, err
	}
	maxConnectionAge, err := durationValue(lookup, "DB_MAX_CONNECTION_AGE", 30*time.Minute)
	if err != nil {
		return Database{}, err
	}
	maxConnectionIdle, err := durationValue(lookup, "DB_MAX_CONNECTION_IDLE", 5*time.Minute)
	if err != nil {
		return Database{}, err
	}
	healthCheckPeriod, err := durationValue(lookup, "DB_HEALTH_CHECK_PERIOD", 30*time.Second)
	if err != nil {
		return Database{}, err
	}

	database := Database{
		Driver:            value(lookup, "DB_DRIVER", "postgres"),
		Host:              value(lookup, "DB_HOST", "127.0.0.1"),
		Port:              port,
		Name:              value(lookup, "DB_NAME", "go_pdf_forge"),
		User:              value(lookup, "DB_USER", "go_pdf_forge"),
		Password:          value(lookup, "DB_PASSWORD", ""),
		SSLMode:           value(lookup, "DB_SSL_MODE", "disable"),
		ConnectTimeout:    connectTimeout,
		HealthTimeout:     healthTimeout,
		MigrationTimeout:  migrationTimeout,
		MaxConnections:    maxConnections,
		MinConnections:    minConnections,
		MaxConnectionAge:  maxConnectionAge,
		MaxConnectionIdle: maxConnectionIdle,
		HealthCheckPeriod: healthCheckPeriod,
	}
	return database, nil
}

// Validate reports every invalid PostgreSQL setting without exposing the
// password or a connection string.
func (cfg Database) Validate() error {
	return errors.Join(cfg.validate()...)
}

func (cfg Database) validate() []error {
	var problems []error
	if cfg.Driver != "postgres" {
		problems = append(problems, errors.New("DB_DRIVER must be postgres"))
	}
	if strings.TrimSpace(cfg.Host) == "" {
		problems = append(problems, errors.New("DB_HOST is required"))
	}
	if cfg.Port < 1 || cfg.Port > 65535 {
		problems = append(problems, errors.New("DB_PORT must be between 1 and 65535"))
	}
	if strings.TrimSpace(cfg.Name) == "" {
		problems = append(problems, errors.New("DB_NAME is required"))
	}
	if strings.TrimSpace(cfg.User) == "" {
		problems = append(problems, errors.New("DB_USER is required"))
	}
	if cfg.Password == "" {
		problems = append(problems, errors.New("DB_PASSWORD is required"))
	}
	if cfg.SSLMode == "" {
		problems = append(problems, errors.New("DB_SSL_MODE is required"))
	} else if !validSSLMode(cfg.SSLMode) {
		problems = append(problems, errors.New("DB_SSL_MODE must be disable, allow, prefer, require, verify-ca, or verify-full"))
	}
	if cfg.ConnectTimeout <= 0 {
		problems = append(problems, errors.New("DB_CONNECT_TIMEOUT must be positive"))
	}
	if cfg.HealthTimeout <= 0 {
		problems = append(problems, errors.New("DB_HEALTH_TIMEOUT must be positive"))
	}
	if cfg.MigrationTimeout <= 0 {
		problems = append(problems, errors.New("DB_MIGRATION_TIMEOUT must be positive"))
	}
	if cfg.MaxConnections <= 0 {
		problems = append(problems, errors.New("DB_MAX_CONNECTIONS must be positive"))
	}
	if cfg.MinConnections < 0 {
		problems = append(problems, errors.New("DB_MIN_CONNECTIONS must not be negative"))
	}
	if cfg.MinConnections > cfg.MaxConnections {
		problems = append(problems, errors.New("DB_MIN_CONNECTIONS must not exceed DB_MAX_CONNECTIONS"))
	}
	if cfg.MaxConnectionAge <= 0 {
		problems = append(problems, errors.New("DB_MAX_CONNECTION_AGE must be positive"))
	}
	if cfg.MaxConnectionIdle <= 0 {
		problems = append(problems, errors.New("DB_MAX_CONNECTION_IDLE must be positive"))
	}
	if cfg.HealthCheckPeriod <= 0 {
		problems = append(problems, errors.New("DB_HEALTH_CHECK_PERIOD must be positive"))
	}
	return problems
}

func validSSLMode(mode string) bool {
	switch mode {
	case "disable", "allow", "prefer", "require", "verify-ca", "verify-full":
		return true
	default:
		return false
	}
}

func parseAuthMode(raw string) (AuthMode, error) {
	mode := AuthMode(strings.ToLower(strings.TrimSpace(raw)))
	switch mode {
	case AuthRequired, AuthOptional, AuthAnonymous:
		return mode, nil
	default:
		return "", fmt.Errorf("AUTH_MODE must be required, optional, or anonymous, got %q", raw)
	}
}

func value(lookup LookupEnv, name, fallback string) string {
	if raw, ok := lookup(name); ok {
		return strings.TrimSpace(raw)
	}
	return fallback
}

func intValue(lookup LookupEnv, name string, fallback int) (int, error) {
	raw := value(lookup, name, "")
	if raw == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", name, err)
	}
	return parsed, nil
}

func int64Value(lookup LookupEnv, name string, fallback int64) (int64, error) {
	raw := value(lookup, name, "")
	if raw == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", name, err)
	}
	return parsed, nil
}

func durationValue(lookup LookupEnv, name string, fallback time.Duration) (time.Duration, error) {
	raw := value(lookup, name, "")
	if raw == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", name, err)
	}
	return parsed, nil
}

func boolValue(lookup LookupEnv, name string, fallback bool) (bool, error) {
	raw := value(lookup, name, "")
	if raw == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("parse %s: %w", name, err)
	}
	return parsed, nil
}
