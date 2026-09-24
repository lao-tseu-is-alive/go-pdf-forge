package database

import (
	"context"
	"io"
	"log/slog"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/lao-tseu-is-alive/go-pdf-forge/db/migrations"
	"github.com/lao-tseu-is-alive/go-pdf-forge/internal/config"
)

func TestPostgresMigrationsConcurrentUpDownAndUp(t *testing.T) {
	if os.Getenv("GPF_POSTGRES_TESTS") != "1" {
		t.Skip("set GPF_POSTGRES_TESTS=1 to run PostgreSQL integration tests")
	}

	settings, err := config.LoadDatabase(os.LookupEnv)
	if err != nil {
		t.Fatalf("LoadDatabase() error = %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	schema, err := OpenTestSchema(context.Background(), settings, "migration-integration-test", logger)
	if err != nil {
		t.Fatalf("OpenTestSchema() error = %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := schema.Close(cleanupCtx); err != nil {
			t.Errorf("TestSchema.Close() error = %v", err)
		}
	})
	migrator, err := NewMigrator(migrations.FS, ".", logger)
	if err != nil {
		t.Fatalf("NewMigrator() error = %v", err)
	}

	const runners = 2
	start := make(chan struct{})
	results := make(chan MigrationResult, runners)
	errorsFound := make(chan error, runners)
	var wait sync.WaitGroup
	for range runners {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			result, err := migrator.Up(context.Background(), schema.Pool)
			if err != nil {
				errorsFound <- err
				return
			}
			results <- result
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	close(errorsFound)
	for err := range errorsFound {
		t.Errorf("concurrent Up() error = %v", err)
	}
	var applied int
	for result := range results {
		applied += len(result.Applied)
	}
	if applied != len(migrator.Migrations()) {
		t.Fatalf("concurrent Up() applied %d migrations, want %d", applied, len(migrator.Migrations()))
	}
	assertCurrentSchemaTables(t, schema, []string{
		"anonymous_ip_usage",
		"anonymous_session",
		"anonymous_usage",
		"outbox_event",
		"pdf_job",
		"schema_migration",
		"upload_part",
		"upload_session",
	})

	rollback, err := migrator.DownAll(context.Background(), schema.Pool)
	if err != nil {
		t.Fatalf("DownAll() error = %v", err)
	}
	if len(rollback.Reverted) != len(migrator.Migrations()) {
		t.Fatalf("DownAll() reverted %d migrations, want %d", len(rollback.Reverted), len(migrator.Migrations()))
	}
	for index, migration := range rollback.Reverted {
		want := migrator.Migrations()[len(migrator.Migrations())-1-index].Version
		if migration.Version != want {
			t.Fatalf("DownAll() version %d = %s, want %s", index, migration.Version, want)
		}
	}
	assertCurrentSchemaTables(t, schema, nil)

	reapplied, err := migrator.Up(context.Background(), schema.Pool)
	if err != nil {
		t.Fatalf("Up(after DownAll) error = %v", err)
	}
	if len(reapplied.Applied) != len(migrator.Migrations()) {
		t.Fatalf("Up(after DownAll) applied %d migrations, want %d", len(reapplied.Applied), len(migrator.Migrations()))
	}
}

func assertCurrentSchemaTables(t *testing.T, schema *TestSchema, want []string) {
	t.Helper()
	rows, err := schema.Pool.Query(context.Background(), `
SELECT tablename
FROM pg_catalog.pg_tables
WHERE schemaname = current_schema()
ORDER BY tablename`)
	if err != nil {
		t.Fatalf("query current schema tables: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			t.Fatalf("scan current schema table: %v", err)
		}
		got = append(got, table)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate current schema tables: %v", err)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("current schema tables = %v, want %v", got, want)
	}
}
