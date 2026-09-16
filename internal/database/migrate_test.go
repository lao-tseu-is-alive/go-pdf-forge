package database

import (
	"crypto/sha256"
	"testing"
	"testing/fstest"

	"github.com/lao-tseu-is-alive/go-pdf-forge/db/migrations"
)

func TestLoadMigrationsOrdersAndSplitsFiles(t *testing.T) {
	t.Parallel()

	second := []byte("-- migrate:up\nCREATE TABLE second (id bigint);\n-- migrate:down\nDROP TABLE second;\n")
	source := fstest.MapFS{
		"20260916110000_second.sql": {Data: second},
		"20260915170000_first.sql":  {Data: []byte("-- migrate:up\nSELECT 1;\n-- migrate:down\nSELECT 2;\n")},
		"embed.go":                  {Data: []byte("package ignored")},
	}
	migrations, err := LoadMigrations(source, ".")
	if err != nil {
		t.Fatalf("LoadMigrations() error = %v", err)
	}
	if len(migrations) != 2 {
		t.Fatalf("len(migrations) = %d, want 2", len(migrations))
	}
	if migrations[0].Version != "20260915170000" || migrations[0].Name != "first" {
		t.Errorf("first migration = %#v", migrations[0])
	}
	if migrations[1].UpSQL != "CREATE TABLE second (id bigint);" || migrations[1].DownSQL != "DROP TABLE second;" {
		t.Errorf("second migration was not split correctly: %#v", migrations[1])
	}
	if migrations[1].Checksum != sha256.Sum256(second) {
		t.Error("migration checksum does not cover the original file")
	}
}

func TestLoadMigrationsRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		source fstest.MapFS
	}{
		{name: "empty", source: fstest.MapFS{}},
		{name: "invalid filename", source: fstest.MapFS{"initial.sql": {Data: validMigration()}}},
		{name: "missing down", source: fstest.MapFS{"20260916110000_test.sql": {Data: []byte("-- migrate:up\nSELECT 1;")}}},
		{name: "duplicate version", source: fstest.MapFS{
			"20260916110000_first.sql":  {Data: validMigration()},
			"20260916110000_second.sql": {Data: validMigration()},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := LoadMigrations(tt.source, "."); err == nil {
				t.Fatal("LoadMigrations() error = nil")
			}
		})
	}
}

func TestMigrationsReturnsCopy(t *testing.T) {
	t.Parallel()

	migrator, err := NewMigrator(fstest.MapFS{
		"20260916110000_test.sql": {Data: validMigration()},
	}, ".", nil)
	if err != nil {
		t.Fatalf("NewMigrator() error = %v", err)
	}
	first := migrator.Migrations()
	first[0].Name = "changed"
	if migrator.Migrations()[0].Name != "test" {
		t.Fatal("Migrations() exposed mutable internal state")
	}
}

func TestEmbeddedMigrationsLoad(t *testing.T) {
	t.Parallel()

	migrator, err := NewMigrator(migrations.FS, ".", nil)
	if err != nil {
		t.Fatalf("NewMigrator() error = %v", err)
	}
	loaded := migrator.Migrations()
	if len(loaded) != 1 || loaded[0].Version != "20260915170000" || loaded[0].Name != "initial" {
		t.Fatalf("embedded migrations = %#v", loaded)
	}
}

func validMigration() []byte {
	return []byte("-- migrate:up\nSELECT 1;\n-- migrate:down\nSELECT 2;\n")
}
