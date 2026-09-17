// Command pdf-migrate lists, checks, and explicitly applies embedded migrations.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/lao-tseu-is-alive/go-pdf-forge/db/migrations"
	"github.com/lao-tseu-is-alive/go-pdf-forge/internal/config"
	"github.com/lao-tseu-is-alive/go-pdf-forge/internal/database"
	"github.com/lao-tseu-is-alive/go-pdf-forge/internal/version"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "pdf-migrate: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 1 && (args[0] == "--version" || args[0] == "-version") {
		fmt.Println(version.String())
		return nil
	}

	flags := flag.NewFlagSet("pdf-migrate", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	apply := flags.Bool("up", false, "apply all pending migrations")
	list := flags.Bool("list", false, "list embedded migrations without connecting")
	check := flags.Bool("check", false, "validate configuration and PostgreSQL connectivity without changing schema")
	showVersion := flags.Bool("version", false, "display build version and exit")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %v", flags.Args())
	}
	if *showVersion {
		fmt.Println(version.String())
		return nil
	}
	operations := 0
	for _, selected := range []bool{*apply, *list, *check} {
		if selected {
			operations++
		}
	}
	if operations != 1 {
		return fmt.Errorf("choose exactly one operation: --list, --check, or --up")
	}

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	migrator, err := database.NewMigrator(migrations.FS, ".", logger)
	if err != nil {
		return err
	}
	if *list {
		for _, migration := range migrator.Migrations() {
			fmt.Printf("%s\t%s\t%s\n", migration.Version, migration.Name, migration.ChecksumHex())
		}
		return nil
	}

	databaseConfig, err := config.LoadDatabase(os.LookupEnv)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pool, err := database.Open(ctx, databaseConfig, "pdf-migrate", logger)
	if err != nil {
		return err
	}
	defer pool.Close()
	if *check {
		fmt.Println("database connection OK")
		return nil
	}

	migrationCtx, cancel := context.WithTimeout(ctx, databaseConfig.MigrationTimeout)
	defer cancel()
	result, err := migrator.Up(migrationCtx, pool)
	if err != nil {
		return err
	}
	if len(result.Applied) == 0 {
		fmt.Println("database schema is already current")
		return nil
	}
	fmt.Printf("applied %d migration(s); current version %s\n", len(result.Applied), result.Applied[len(result.Applied)-1].Version)
	return nil
}
