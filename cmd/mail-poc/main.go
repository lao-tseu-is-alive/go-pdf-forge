// Command mail-poc validates one explicitly configured SMTP delivery path.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/lao-tseu-is-alive/go-pdf-forge/internal/smtppoc"
	"github.com/lao-tseu-is-alive/go-pdf-forge/internal/version"
)

const defaultBody = "This message confirms that go-pdf-forge can reach and use the configured SMTP service."

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Fatalf("mail-poc failed: %v", err)
	}
}

func run(args []string) error {
	if len(args) == 1 && (args[0] == "--version" || args[0] == "-version") {
		fmt.Println(version.String())
		return nil
	}

	flags := flag.NewFlagSet("mail-poc", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)

	defaultPort, err := intFromEnv("SMTP_PORT", 25)
	if err != nil {
		return err
	}
	defaultTimeout, err := durationFromEnv("SMTP_TIMEOUT", 10*time.Second)
	if err != nil {
		return err
	}

	host := flags.String("smtp-host", os.Getenv("SMTP_HOST"), "SMTP server host")
	port := flags.Int("smtp-port", defaultPort, "SMTP server port")
	modeValue := flags.String("mode", valueOrDefault(os.Getenv("SMTP_MODE"), "plain"), "transport: plain, starttls, or tls")
	from := flags.String("from", os.Getenv("SMTP_FROM"), "message sender")
	to := flags.String("to", "", "test recipient")
	username := flags.String("username", os.Getenv("SMTP_USERNAME"), "optional SMTP username")
	clientHostname := flags.String("client-hostname", os.Getenv("SMTP_CLIENT_HOSTNAME"), "optional SMTP HELO hostname")
	timeout := flags.Duration("timeout", defaultTimeout, "connection and command timeout")
	dryRun := flags.Bool("dry-run", false, "validate and display the non-secret configuration without sending")
	showVersion := flags.Bool("version", false, "display build version and exit")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %s", strings.Join(flags.Args(), " "))
	}
	if *showVersion {
		fmt.Println(version.String())
		return nil
	}

	mode, err := smtppoc.ParseMode(*modeValue)
	if err != nil {
		return err
	}
	cfg := smtppoc.Config{
		Host:           *host,
		Port:           *port,
		Mode:           mode,
		From:           *from,
		To:             *to,
		Username:       *username,
		Password:       os.Getenv("SMTP_PASSWORD"),
		ClientHostname: *clientHostname,
		Timeout:        *timeout,
	}
	if err := cfg.Validate(); err != nil {
		return err
	}

	fmt.Printf("SMTP server: %s\n", cfg.Address())
	fmt.Printf("SMTP mode: %s\n", cfg.Mode)
	fmt.Printf("SMTP authentication: %t\n", cfg.Username != "")
	fmt.Printf("From: %s\n", cfg.From)
	fmt.Printf("Recipient: %s\n", cfg.To)
	if *dryRun {
		fmt.Println("Result: configuration valid; message not sent (dry-run)")
		return nil
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	if err := smtppoc.Send(ctx, cfg, defaultBody); err != nil {
		return err
	}
	fmt.Println("Result: message accepted by SMTP server")
	return nil
}

func intFromEnv(name string, fallback int) (int, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", name, err)
	}
	return parsed, nil
}

func durationFromEnv(name string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", name, err)
	}
	return parsed, nil
}

func valueOrDefault(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
