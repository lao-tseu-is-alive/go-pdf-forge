package smtppoc

import (
	"strings"
	"testing"
	"time"
)

func TestParseMode(t *testing.T) {
	for _, test := range []struct {
		input string
		want  Mode
	}{
		{input: "plain", want: ModePlain},
		{input: " STARTTLS ", want: ModeSTARTTLS},
		{input: "TLS", want: ModeTLS},
	} {
		got, err := ParseMode(test.input)
		if err != nil {
			t.Fatalf("ParseMode(%q): %v", test.input, err)
		}
		if got != test.want {
			t.Errorf("ParseMode(%q) = %q, want %q", test.input, got, test.want)
		}
	}

	if _, err := ParseMode("automatic"); err == nil {
		t.Fatal("ParseMode(automatic) unexpectedly succeeded")
	}
}

func TestConfigValidate(t *testing.T) {
	valid := Config{
		Host:    "smtp.example.com",
		Port:    587,
		Mode:    ModeSTARTTLS,
		From:    "PDF Forge <pdf@example.com>",
		To:      "user@example.com",
		Timeout: 10 * time.Second,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	invalid := valid
	invalid.Host = ""
	invalid.Port = 0
	invalid.Username = "user"
	if err := invalid.Validate(); err == nil {
		t.Fatal("invalid config unexpectedly accepted")
	} else {
		for _, want := range []string{"host is required", "port must be", "both be set"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("validation error %q does not contain %q", err, want)
			}
		}
	}
}

func TestBuildMessage(t *testing.T) {
	sentAt := time.Date(2026, time.September, 15, 16, 0, 0, 0, time.FixedZone("CEST", 2*60*60))
	message, err := BuildMessage(
		"PDF Forge <pdf@example.com>",
		"user@example.com",
		Subject,
		"first line\nsecond line",
		sentAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Subject: [PDF Service POC] Test SMTP\r\n",
		"Content-Type: text/plain; charset=UTF-8\r\n",
		"\r\n\r\nfirst line\r\nsecond line\r\n",
	} {
		if !strings.Contains(message, want) {
			t.Errorf("message does not contain %q:\n%s", want, message)
		}
	}
}

func TestBuildMessageRejectsHeaderInjection(t *testing.T) {
	if _, err := BuildMessage(
		"pdf@example.com",
		"user@example.com",
		"valid\r\nBcc: attacker@example.com",
		"body",
		time.Now(),
	); err == nil {
		t.Fatal("header injection unexpectedly accepted")
	}
}
