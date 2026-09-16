// Package database owns PostgreSQL connection lifecycle, health checks, and
// migration execution. It never exposes or logs a connection string.
package database

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/url"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/lao-tseu-is-alive/go-pdf-forge/internal/config"
)

const applicationName = "go-pdf-forge"

type operationError struct {
	operation string
	cause     error
}

func (err *operationError) Error() string {
	return fmt.Sprintf("postgres %s failed", err.operation)
}

func (err *operationError) Unwrap() error {
	return err.cause
}

// Open creates a bounded pool and proves that a connection can be acquired
// before returning. The returned errors have deliberately non-sensitive text;
// callers can still use errors.Is/errors.As through the wrapped cause.
func Open(ctx context.Context, settings config.Database, component string, logger *slog.Logger) (*pgxpool.Pool, error) {
	poolConfig, err := buildPoolConfig(settings, component)
	if err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}

	connectCtx, cancel := context.WithTimeout(ctx, settings.ConnectTimeout)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(connectCtx, poolConfig)
	if err != nil {
		return nil, &operationError{operation: "pool creation", cause: err}
	}
	if err := pool.Ping(connectCtx); err != nil {
		pool.Close()
		return nil, &operationError{operation: "connection check", cause: err}
	}

	logger.Info("postgres connection pool ready", "database", safeSettings{settings})
	return pool, nil
}

type pinger interface {
	Ping(context.Context) error
}

// Ping bounds readiness checks independently from the request context.
func Ping(ctx context.Context, target pinger, timeout time.Duration) error {
	if target == nil {
		return errors.New("postgres ping target is required")
	}
	if timeout <= 0 {
		return errors.New("postgres ping timeout must be positive")
	}
	pingCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := target.Ping(pingCtx); err != nil {
		return &operationError{operation: "health check", cause: err}
	}
	return nil
}

func buildPoolConfig(settings config.Database, component string) (*pgxpool.Config, error) {
	if err := settings.Validate(); err != nil {
		return nil, err
	}
	if settings.MaxConnections > math.MaxInt32 || settings.MinConnections > math.MaxInt32 {
		return nil, errors.New("database connection counts exceed the pgx limit")
	}

	connectionURL := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(settings.User, settings.Password),
		Host:   net.JoinHostPort(settings.Host, strconv.Itoa(settings.Port)),
		Path:   "/" + settings.Name,
	}
	query := connectionURL.Query()
	query.Set("sslmode", settings.SSLMode)
	connectionURL.RawQuery = query.Encode()

	poolConfig, err := pgxpool.ParseConfig(connectionURL.String())
	if err != nil {
		// Parse errors can echo their input. Keep the public error independent
		// from the in-memory URL because it contains the password.
		return nil, errors.New("build PostgreSQL pool configuration")
	}
	poolConfig.ConnConfig.ConnectTimeout = settings.ConnectTimeout
	poolConfig.ConnConfig.RuntimeParams["application_name"] = postgresApplicationName(component)
	poolConfig.MaxConns = int32(settings.MaxConnections)
	poolConfig.MinConns = int32(settings.MinConnections)
	poolConfig.MaxConnLifetime = settings.MaxConnectionAge
	poolConfig.MaxConnIdleTime = settings.MaxConnectionIdle
	poolConfig.HealthCheckPeriod = settings.HealthCheckPeriod
	poolConfig.PingTimeout = settings.HealthTimeout
	return poolConfig, nil
}

func postgresApplicationName(component string) string {
	if component == "" {
		return applicationName
	}
	return applicationName + "/" + component
}

type safeSettings struct {
	config.Database
}

func (settings safeSettings) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("host", settings.Host),
		slog.Int("port", settings.Port),
		slog.String("name", settings.Name),
		slog.String("user", settings.User),
		slog.String("ssl_mode", settings.SSLMode),
		slog.Int("max_connections", settings.MaxConnections),
		slog.Int("min_connections", settings.MinConnections),
	)
}
