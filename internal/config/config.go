// Package config parses and validates process configuration without loading
// dotenv files. Secret values are intentionally kept out of error messages.
package config

import (
	"errors"
	"fmt"
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

type AuthMode string

const (
	AuthRequired  AuthMode = "required"
	AuthOptional  AuthMode = "optional"
	AuthAnonymous AuthMode = "anonymous"
)

type LookupEnv func(string) (string, bool)

type Config struct {
	Auth              AuthMode
	MaxUploadBytes    int64
	UploadChunkBytes  int64
	PDFTargetBytes    int64
	JobRetention      time.Duration
	WorkerConcurrency int
	Anonymous         Anonymous
	Database          Database
	ObjectStore       ObjectStore
}

type Anonymous struct {
	TokenPepper []byte
	SessionTTL  time.Duration
}

type Database struct {
	Driver   string
	Host     string
	Port     int
	Name     string
	User     string
	Password string
	SSLMode  string
}

type ObjectStore struct {
	Endpoint        string
	Region          string
	Bucket          string
	AccessKeyID     string
	SecretAccessKey string
	PathStyle       bool
}

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
	databasePort, err := intValue(lookup, "DB_PORT", 5432)
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
		},
		Database: Database{
			Driver:   value(lookup, "DB_DRIVER", "postgres"),
			Host:     value(lookup, "DB_HOST", "127.0.0.1"),
			Port:     databasePort,
			Name:     value(lookup, "DB_NAME", "go_pdf_forge"),
			User:     value(lookup, "DB_USER", "go_pdf_forge"),
			Password: value(lookup, "DB_PASSWORD", ""),
			SSLMode:  value(lookup, "DB_SSL_MODE", "disable"),
		},
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
	if cfg.Database.Driver != "postgres" {
		problems = append(problems, errors.New("DB_DRIVER must be postgres"))
	}
	if strings.TrimSpace(cfg.Database.Host) == "" {
		problems = append(problems, errors.New("DB_HOST is required"))
	}
	if cfg.Database.Port < 1 || cfg.Database.Port > 65535 {
		problems = append(problems, errors.New("DB_PORT must be between 1 and 65535"))
	}
	if strings.TrimSpace(cfg.Database.Name) == "" {
		problems = append(problems, errors.New("DB_NAME is required"))
	}
	if strings.TrimSpace(cfg.Database.User) == "" {
		problems = append(problems, errors.New("DB_USER is required"))
	}
	if cfg.Database.Password == "" {
		problems = append(problems, errors.New("DB_PASSWORD is required"))
	}
	if cfg.Database.SSLMode == "" {
		problems = append(problems, errors.New("DB_SSL_MODE is required"))
	}
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
