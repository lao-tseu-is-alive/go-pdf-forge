// Package testpostgres provisions migrated, disposable PostgreSQL schemas for
// integration tests without touching the developer's public schema.
package testpostgres

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/lao-tseu-is-alive/go-pdf-forge/db/migrations"
	"github.com/lao-tseu-is-alive/go-pdf-forge/internal/config"
	"github.com/lao-tseu-is-alive/go-pdf-forge/internal/database"
)

// Environment is a migrated PostgreSQL schema dedicated to one integration
// test. Close must be called even when the test fails.
type Environment struct {
	// Pool is configured with a search path containing only the test schema.
	Pool *pgxpool.Pool

	schema *database.TestSchema
}

// Open creates an isolated schema and applies every embedded migration before
// returning. It drops the schema if migration setup fails.
func Open(ctx context.Context, settings config.Database, component string, logger *slog.Logger) (*Environment, error) {
	schema, err := database.OpenTestSchema(ctx, settings, component, logger)
	if err != nil {
		return nil, err
	}
	migrator, err := database.NewMigrator(migrations.FS, ".", logger)
	if err != nil {
		closeTestSchema(schema)
		return nil, err
	}
	if _, err := migrator.Up(ctx, schema.Pool); err != nil {
		closeTestSchema(schema)
		return nil, err
	}
	return &Environment{Pool: schema.Pool, schema: schema}, nil
}

// Close closes every test connection and drops the disposable schema.
func (environment *Environment) Close(ctx context.Context) error {
	if environment == nil {
		return nil
	}
	return environment.schema.Close(ctx)
}

func closeTestSchema(schema *database.TestSchema) {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = schema.Close(cleanupCtx)
}
