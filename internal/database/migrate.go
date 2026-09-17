package database

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	upMarker   = "-- migrate:up"
	downMarker = "-- migrate:down"

	// Transaction-scoped and stable for this application. It serializes all
	// schema changes without leaving a session lock in a pooled connection.
	migrationAdvisoryLock int64 = 0x4750464d494752 // "GPFMIGR"
)

var migrationFilename = regexp.MustCompile(`^([0-9]{14})_([a-z0-9][a-z0-9_]*)\.sql$`)

// Migration is one immutable, ordered SQL change loaded from an embedded file.
type Migration struct {
	// Version is the fourteen-digit UTC timestamp prefix from the filename.
	Version string
	// Name is the stable descriptive suffix from the filename.
	Name string
	// UpSQL advances the schema and may contain multiple trusted statements.
	UpSQL string
	// DownSQL documents how to reverse the change for controlled test use.
	DownSQL string
	// Checksum fingerprints the complete source file, including both directions.
	Checksum [sha256.Size]byte
}

// ChecksumHex returns the migration checksum in lowercase hexadecimal form.
func (migration Migration) ChecksumHex() string {
	return hex.EncodeToString(migration.Checksum[:])
}

// MigrationResult reports migrations committed by one Up invocation.
type MigrationResult struct {
	// Applied is ordered from the oldest to the newest committed migration.
	Applied []Migration
}

type beginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

// Migrator validates immutable migration history and applies pending changes.
// It is safe to run concurrently across processes because Up takes a
// transaction-scoped PostgreSQL advisory lock.
type Migrator struct {
	migrations []Migration
	logger     *slog.Logger
}

// NewMigrator loads and validates every SQL migration in directory.
func NewMigrator(source fs.FS, directory string, logger *slog.Logger) (*Migrator, error) {
	migrations, err := LoadMigrations(source, directory)
	if err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Migrator{migrations: migrations, logger: logger}, nil
}

// Migrations returns a defensive copy of the ordered migration set.
func (migrator *Migrator) Migrations() []Migration {
	result := make([]Migration, len(migrator.migrations))
	copy(result, migrator.migrations)
	return result
}

// Up serializes migration runners, verifies the immutable history, and applies
// every pending migration atomically. A single failure rolls back the complete
// batch, including the migration ledger updates.
func (migrator *Migrator) Up(ctx context.Context, db beginner) (result MigrationResult, err error) {
	if db == nil {
		return result, errors.New("migration database is required")
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return result, &operationError{operation: "begin migration transaction", cause: err}
	}
	defer func() {
		if err != nil {
			rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_ = tx.Rollback(rollbackCtx)
		}
	}()

	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, migrationAdvisoryLock); err != nil {
		return result, &operationError{operation: "acquire migration lock", cause: err}
	}
	if _, err = tx.Exec(ctx, `
CREATE TABLE IF NOT EXISTS schema_migration (
    version text PRIMARY KEY,
    name text NOT NULL,
    checksum bytea NOT NULL CHECK (octet_length(checksum) = 32),
    applied_at timestamptz NOT NULL DEFAULT now()
)`); err != nil {
		return result, &operationError{operation: "create migration ledger", cause: err}
	}

	applied, err := readAppliedMigrations(ctx, tx)
	if err != nil {
		return result, err
	}
	known := make(map[string]Migration, len(migrator.migrations))
	for _, migration := range migrator.migrations {
		known[migration.Version] = migration
	}
	for version, record := range applied {
		migration, ok := known[version]
		if !ok {
			return result, fmt.Errorf("database contains migration %s unknown to this binary", version)
		}
		if record.name != migration.Name || !bytes.Equal(record.checksum, migration.Checksum[:]) {
			return result, fmt.Errorf("migration %s differs from the applied immutable history", version)
		}
	}

	for _, migration := range migrator.migrations {
		if _, ok := applied[migration.Version]; ok {
			continue
		}
		if _, err = tx.Exec(ctx, migration.UpSQL, pgx.QueryExecModeSimpleProtocol); err != nil {
			return result, &operationError{operation: "apply migration " + migration.Version, cause: err}
		}
		if _, err = tx.Exec(ctx,
			`INSERT INTO schema_migration (version, name, checksum) VALUES ($1, $2, $3)`,
			migration.Version, migration.Name, migration.Checksum[:]); err != nil {
			return result, &operationError{operation: "record migration " + migration.Version, cause: err}
		}
		result.Applied = append(result.Applied, migration)
	}

	if err = tx.Commit(ctx); err != nil {
		return MigrationResult{}, &operationError{operation: "commit migrations", cause: err}
	}
	for _, migration := range result.Applied {
		migrator.logger.Info("postgres migration applied", "version", migration.Version, "name", migration.Name)
	}
	return result, nil
}

type appliedMigration struct {
	name     string
	checksum []byte
}

func readAppliedMigrations(ctx context.Context, tx pgx.Tx) (map[string]appliedMigration, error) {
	rows, err := tx.Query(ctx, `SELECT version, name, checksum FROM schema_migration ORDER BY version`)
	if err != nil {
		return nil, &operationError{operation: "read migration ledger", cause: err}
	}
	defer rows.Close()

	applied := make(map[string]appliedMigration)
	for rows.Next() {
		var version string
		var record appliedMigration
		if err := rows.Scan(&version, &record.name, &record.checksum); err != nil {
			return nil, &operationError{operation: "scan migration ledger", cause: err}
		}
		applied[version] = record
	}
	if err := rows.Err(); err != nil {
		return nil, &operationError{operation: "iterate migration ledger", cause: err}
	}
	return applied, nil
}

// LoadMigrations parses, validates, fingerprints, and orders SQL migration
// files. It rejects malformed names, duplicate versions, and missing sections.
func LoadMigrations(source fs.FS, directory string) ([]Migration, error) {
	if source == nil {
		return nil, errors.New("migration source is required")
	}
	if directory == "" {
		directory = "."
	}
	entries, err := fs.ReadDir(source, directory)
	if err != nil {
		return nil, fmt.Errorf("read migration directory: %w", err)
	}

	var migrations []Migration
	versions := make(map[string]string)
	for _, entry := range entries {
		if entry.IsDir() || path.Ext(entry.Name()) != ".sql" {
			continue
		}
		matches := migrationFilename.FindStringSubmatch(entry.Name())
		if matches == nil {
			return nil, fmt.Errorf("invalid migration filename %q", entry.Name())
		}
		if previous, exists := versions[matches[1]]; exists {
			return nil, fmt.Errorf("migration version %s is duplicated by %q and %q", matches[1], previous, entry.Name())
		}
		content, err := fs.ReadFile(source, path.Join(directory, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", entry.Name(), err)
		}
		upSQL, downSQL, err := splitMigration(content)
		if err != nil {
			return nil, fmt.Errorf("parse migration %q: %w", entry.Name(), err)
		}
		versions[matches[1]] = entry.Name()
		migrations = append(migrations, Migration{
			Version:  matches[1],
			Name:     matches[2],
			UpSQL:    upSQL,
			DownSQL:  downSQL,
			Checksum: sha256.Sum256(content),
		})
	}
	if len(migrations) == 0 {
		return nil, errors.New("no SQL migrations found")
	}
	sort.Slice(migrations, func(i, j int) bool {
		return migrations[i].Version < migrations[j].Version
	})
	return migrations, nil
}

func splitMigration(content []byte) (string, string, error) {
	text := string(content)
	upIndex := strings.Index(text, upMarker)
	downIndex := strings.Index(text, downMarker)
	if upIndex < 0 {
		return "", "", errors.New("missing -- migrate:up marker")
	}
	if downIndex < 0 {
		return "", "", errors.New("missing -- migrate:down marker")
	}
	if strings.Count(text, upMarker) != 1 || strings.Count(text, downMarker) != 1 {
		return "", "", errors.New("migration markers must appear exactly once")
	}
	if downIndex <= upIndex {
		return "", "", errors.New("down marker must follow up marker")
	}
	upSQL := strings.TrimSpace(text[upIndex+len(upMarker) : downIndex])
	downSQL := strings.TrimSpace(text[downIndex+len(downMarker):])
	if upSQL == "" {
		return "", "", errors.New("up migration is empty")
	}
	if downSQL == "" {
		return "", "", errors.New("down migration is empty")
	}
	return upSQL, downSQL, nil
}
