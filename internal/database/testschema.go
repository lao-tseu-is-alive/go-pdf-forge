package database

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/lao-tseu-is-alive/go-pdf-forge/internal/config"
)

const testSchemaRandomBytes = 12

// TestSchema owns a generated PostgreSQL schema and a pool whose search path
// is restricted to that schema. Close must be called to drop all test data.
type TestSchema struct {
	// Pool is the isolated connection pool used by the integration test.
	Pool *pgxpool.Pool

	admin  *pgxpool.Pool
	name   string
	mu     sync.Mutex
	closed bool
}

// OpenTestSchema creates a random schema in the configured database and opens
// an isolated pool for integration tests. It never changes the public schema.
func OpenTestSchema(ctx context.Context, settings config.Database, component string, logger *slog.Logger) (*TestSchema, error) {
	adminConfig, err := buildPoolConfig(settings, component+"/schema-admin")
	if err != nil {
		return nil, err
	}
	admin, err := pgxpool.NewWithConfig(ctx, adminConfig)
	if err != nil {
		return nil, &operationError{operation: "test schema admin pool creation", cause: err}
	}
	if err := admin.Ping(ctx); err != nil {
		admin.Close()
		return nil, &operationError{operation: "test schema admin connection check", cause: err}
	}

	name, err := randomTestSchemaName()
	if err != nil {
		admin.Close()
		return nil, err
	}
	identifier := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, `CREATE SCHEMA `+identifier); err != nil {
		admin.Close()
		return nil, &operationError{operation: "create isolated test schema", cause: err}
	}

	poolConfig, err := buildPoolConfig(settings, component)
	if err != nil {
		dropTestSchema(context.WithoutCancel(ctx), admin, identifier)
		admin.Close()
		return nil, err
	}
	poolConfig.ConnConfig.RuntimeParams["search_path"] = name
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		dropTestSchema(context.WithoutCancel(ctx), admin, identifier)
		admin.Close()
		return nil, &operationError{operation: "isolated test pool creation", cause: err}
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		dropTestSchema(context.WithoutCancel(ctx), admin, identifier)
		admin.Close()
		return nil, &operationError{operation: "isolated test connection check", cause: err}
	}
	if logger != nil {
		logger.Debug("isolated postgres test schema ready", "schema", name)
	}
	return &TestSchema{Pool: pool, admin: admin, name: name}, nil
}

// Close closes the isolated pool and drops its schema with CASCADE. Repeated
// calls are safe; a failed drop is returned so leaked test data is visible.
func (schema *TestSchema) Close(ctx context.Context) error {
	if schema == nil {
		return nil
	}
	schema.mu.Lock()
	defer schema.mu.Unlock()
	if schema.closed {
		return nil
	}
	schema.Pool.Close()
	identifier := pgx.Identifier{schema.name}.Sanitize()
	if err := dropTestSchema(ctx, schema.admin, identifier); err != nil {
		return err
	}
	schema.admin.Close()
	schema.closed = true
	return nil
}

func randomTestSchemaName() (string, error) {
	random := make([]byte, testSchemaRandomBytes)
	if _, err := rand.Read(random); err != nil {
		return "", &operationError{operation: "generate isolated test schema name", cause: err}
	}
	return "gpf_test_" + hex.EncodeToString(random), nil
}

func dropTestSchema(ctx context.Context, admin *pgxpool.Pool, identifier string) error {
	if admin == nil {
		return errors.New("postgres test schema admin pool is required")
	}
	if _, err := admin.Exec(ctx, `DROP SCHEMA IF EXISTS `+identifier+` CASCADE`); err != nil {
		return &operationError{operation: "drop isolated test schema", cause: err}
	}
	return nil
}
