package database

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/lao-tseu-is-alive/go-pdf-forge/internal/config"
)

func TestBuildPoolConfig(t *testing.T) {
	t.Parallel()

	settings := validSettings()
	settings.Password = "p@ssword:/?#with-special-characters"
	poolConfig, err := buildPoolConfig(settings, "test")
	if err != nil {
		t.Fatalf("buildPoolConfig() error = %v", err)
	}
	if poolConfig.ConnConfig.Host != settings.Host || poolConfig.ConnConfig.Port != uint16(settings.Port) {
		t.Errorf("connection address = %s:%d", poolConfig.ConnConfig.Host, poolConfig.ConnConfig.Port)
	}
	if poolConfig.ConnConfig.Database != settings.Name || poolConfig.ConnConfig.User != settings.User {
		t.Errorf("connection identity = %s/%s", poolConfig.ConnConfig.User, poolConfig.ConnConfig.Database)
	}
	if poolConfig.ConnConfig.Password != settings.Password {
		t.Error("connection password was not preserved through URL escaping")
	}
	if poolConfig.ConnConfig.RuntimeParams["application_name"] != "go-pdf-forge/test" {
		t.Errorf("application_name = %q", poolConfig.ConnConfig.RuntimeParams["application_name"])
	}
	if poolConfig.MaxConns != int32(settings.MaxConnections) || poolConfig.MinConns != int32(settings.MinConnections) {
		t.Errorf("pool bounds = %d..%d", poolConfig.MinConns, poolConfig.MaxConns)
	}
	if poolConfig.PingTimeout != settings.HealthTimeout {
		t.Errorf("pool ping timeout = %v, want %v", poolConfig.PingTimeout, settings.HealthTimeout)
	}
}

func TestSafeSettingsNeverLogsPassword(t *testing.T) {
	t.Parallel()

	settings := validSettings()
	settings.Password = "do-not-log-this-secret"
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	logger.Info("configured", "database", safeSettings{settings})
	if strings.Contains(output.String(), settings.Password) || strings.Contains(output.String(), "Password") {
		t.Fatalf("safe settings leaked a password: %s", output.String())
	}
	for _, expected := range []string{settings.Host, settings.Name, settings.User} {
		if !strings.Contains(output.String(), expected) {
			t.Errorf("safe settings log does not contain %q", expected)
		}
	}
}

func TestOperationErrorTextDoesNotLeakCause(t *testing.T) {
	t.Parallel()

	cause := errors.New("dial postgres://user:very-secret@example.test/database")
	err := &operationError{operation: "connection check", cause: cause}
	if strings.Contains(err.Error(), "very-secret") {
		t.Fatalf("operation error leaked its cause: %v", err)
	}
	if !errors.Is(err, cause) {
		t.Fatal("operation error does not unwrap its cause")
	}
}

func TestPingUsesBoundedContext(t *testing.T) {
	t.Parallel()

	target := pingerFunc(func(ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		if !ok {
			return errors.New("missing deadline")
		}
		if remaining := time.Until(deadline); remaining <= 0 || remaining > time.Second {
			return errors.New("unexpected deadline")
		}
		return nil
	})
	if err := Ping(context.Background(), target, time.Second); err != nil {
		t.Fatalf("Ping() error = %v", err)
	}
}

type pingerFunc func(context.Context) error

func (fn pingerFunc) Ping(ctx context.Context) error {
	return fn(ctx)
}

func validSettings() config.Database {
	return config.Database{
		Driver:            "postgres",
		Host:              "127.0.0.1",
		Port:              5432,
		Name:              "go_pdf_forge",
		User:              "go_pdf_forge",
		Password:          "secret",
		SSLMode:           "disable",
		ConnectTimeout:    5 * time.Second,
		HealthTimeout:     2 * time.Second,
		MigrationTimeout:  2 * time.Minute,
		MaxConnections:    10,
		MinConnections:    0,
		MaxConnectionAge:  30 * time.Minute,
		MaxConnectionIdle: 5 * time.Minute,
		HealthCheckPeriod: 30 * time.Second,
	}
}
