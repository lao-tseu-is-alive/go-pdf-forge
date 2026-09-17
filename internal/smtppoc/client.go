// Package smtppoc implements the deliberately small SMTP surface needed by
// cmd/mail-poc. Production notification and retry policy do not belong here.
package smtppoc

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// Mode selects the single SMTP transport negotiated by the diagnostic client.
type Mode string

const (
	// ModePlain uses SMTP without transport encryption for a trusted relay.
	ModePlain Mode = "plain"
	// ModeSTARTTLS upgrades an initially plain SMTP connection and fails closed
	// when the server does not advertise STARTTLS.
	ModeSTARTTLS Mode = "starttls"
	// ModeTLS establishes implicit TLS before speaking SMTP.
	ModeTLS Mode = "tls"
)

// Subject is the fixed subject used by the SMTP connectivity diagnostic.
const Subject = "[PDF Service POC] Test SMTP"

// Config contains one SMTP diagnostic delivery path. It intentionally has no
// fallback provider because silent transport downgrades would hide failures.
type Config struct {
	// Host is the SMTP server hostname used for dialing and TLS verification.
	Host string
	// Port is the SMTP TCP port.
	Port int
	// Mode selects plain SMTP, STARTTLS, or implicit TLS.
	Mode Mode
	// From is the RFC 5322 sender mailbox.
	From string
	// To is the single RFC 5322 recipient mailbox.
	To string
	// Username enables SMTP AUTH when set together with Password.
	Username string
	// Password is the optional SMTP credential and must never be logged.
	Password string
	// ClientHostname is the optional hostname sent with HELO/EHLO.
	ClientHostname string
	// Timeout bounds dialing and the complete SMTP exchange.
	Timeout time.Duration
}

// ParseMode normalizes and validates an SMTP transport name.
func ParseMode(value string) (Mode, error) {
	mode := Mode(strings.ToLower(strings.TrimSpace(value)))
	switch mode {
	case ModePlain, ModeSTARTTLS, ModeTLS:
		return mode, nil
	default:
		return "", fmt.Errorf("unsupported SMTP mode %q (want plain, starttls, or tls)", value)
	}
}

// Validate reports all invalid SMTP settings without including Password.
func (cfg Config) Validate() error {
	var problems []string
	if strings.TrimSpace(cfg.Host) == "" {
		problems = append(problems, "SMTP host is required")
	}
	if cfg.Port < 1 || cfg.Port > 65535 {
		problems = append(problems, "SMTP port must be between 1 and 65535")
	}
	if _, err := ParseMode(string(cfg.Mode)); err != nil {
		problems = append(problems, err.Error())
	}
	if _, err := mailbox(cfg.From); err != nil {
		problems = append(problems, "invalid from address: "+err.Error())
	}
	if _, err := mailbox(cfg.To); err != nil {
		problems = append(problems, "invalid recipient address: "+err.Error())
	}
	if cfg.Timeout <= 0 {
		problems = append(problems, "SMTP timeout must be positive")
	}
	if (cfg.Username == "") != (cfg.Password == "") {
		problems = append(problems, "SMTP username and password must either both be set or both be empty")
	}
	if cfg.ClientHostname != "" && containsNewline(cfg.ClientHostname) {
		problems = append(problems, "SMTP client hostname contains a newline")
	}
	return errors.Join(stringErrors(problems)...)
}

// Address returns the validated host and port in network dial form.
func (cfg Config) Address() string {
	return net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
}

// Send establishes exactly the configured transport. It never downgrades from
// TLS to clear text and never retries through a different SMTP provider.
func Send(ctx context.Context, cfg Config, body string) error {
	if err := cfg.Validate(); err != nil {
		return err
	}

	from, _ := mailbox(cfg.From)
	to, _ := mailbox(cfg.To)
	message, err := BuildMessage(cfg.From, cfg.To, Subject, body, time.Now())
	if err != nil {
		return err
	}

	deadline := time.Now().Add(cfg.Timeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}

	dialer := &net.Dialer{Timeout: cfg.Timeout}
	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: cfg.Host,
	}

	var conn net.Conn
	switch cfg.Mode {
	case ModeTLS:
		conn, err = (&tls.Dialer{NetDialer: dialer, Config: tlsConfig}).DialContext(ctx, "tcp", cfg.Address())
	default:
		conn, err = dialer.DialContext(ctx, "tcp", cfg.Address())
	}
	if err != nil {
		return fmt.Errorf("connect to SMTP server: %w", err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(deadline); err != nil {
		return fmt.Errorf("set SMTP deadline: %w", err)
	}

	client, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		return fmt.Errorf("create SMTP client: %w", err)
	}
	defer client.Close()

	if cfg.ClientHostname != "" {
		if err := client.Hello(cfg.ClientHostname); err != nil {
			return fmt.Errorf("send SMTP HELO: %w", err)
		}
	}
	if cfg.Mode == ModeSTARTTLS {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return errors.New("SMTP server does not advertise STARTTLS")
		}
		if err := client.StartTLS(tlsConfig); err != nil {
			return fmt.Errorf("start SMTP TLS: %w", err)
		}
	}
	if cfg.Username != "" {
		if ok, _ := client.Extension("AUTH"); !ok {
			return errors.New("SMTP authentication configured but server does not advertise AUTH")
		}
		auth := smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("authenticate to SMTP server: %w", err)
		}
	}
	if err := client.Mail(from); err != nil {
		return fmt.Errorf("set SMTP sender: %w", err)
	}
	if err := client.Rcpt(to); err != nil {
		return fmt.Errorf("set SMTP recipient: %w", err)
	}

	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("open SMTP DATA: %w", err)
	}
	if _, err := io.WriteString(writer, message); err != nil {
		_ = writer.Close()
		return fmt.Errorf("write SMTP message: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("finish SMTP message: %w", err)
	}
	if err := client.Quit(); err != nil {
		return fmt.Errorf("quit SMTP session: %w", err)
	}
	return nil
}

// BuildMessage constructs a minimal UTF-8 text message with CRLF line endings
// and rejects header injection through addresses or the subject.
func BuildMessage(from, to, subject, body string, sentAt time.Time) (string, error) {
	if containsNewline(subject) {
		return "", errors.New("SMTP subject contains a newline")
	}
	fromAddress, err := mail.ParseAddress(from)
	if err != nil {
		return "", fmt.Errorf("parse from address: %w", err)
	}
	toAddress, err := mail.ParseAddress(to)
	if err != nil {
		return "", fmt.Errorf("parse recipient address: %w", err)
	}

	// SMTP DATA requires CRLF. Normalize caller text before composing headers.
	normalizedBody := strings.ReplaceAll(body, "\r\n", "\n")
	normalizedBody = strings.ReplaceAll(normalizedBody, "\r", "\n")
	normalizedBody = strings.ReplaceAll(normalizedBody, "\n", "\r\n")

	var message strings.Builder
	fmt.Fprintf(&message, "Date: %s\r\n", sentAt.Format(time.RFC1123Z))
	fmt.Fprintf(&message, "From: %s\r\n", fromAddress.String())
	fmt.Fprintf(&message, "To: %s\r\n", toAddress.String())
	fmt.Fprintf(&message, "Subject: %s\r\n", subject)
	message.WriteString("MIME-Version: 1.0\r\n")
	message.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	message.WriteString("Content-Transfer-Encoding: 8bit\r\n")
	message.WriteString("\r\n")
	message.WriteString(normalizedBody)
	if !strings.HasSuffix(normalizedBody, "\r\n") {
		message.WriteString("\r\n")
	}
	return message.String(), nil
}

func mailbox(value string) (string, error) {
	if containsNewline(value) {
		return "", errors.New("address contains a newline")
	}
	address, err := mail.ParseAddress(value)
	if err != nil {
		return "", err
	}
	return address.Address, nil
}

func containsNewline(value string) bool {
	return strings.ContainsAny(value, "\r\n")
}

func stringErrors(values []string) []error {
	errs := make([]error, 0, len(values))
	for _, value := range values {
		errs = append(errs, errors.New(value))
	}
	return errs
}
