package anonymous

import (
	"bytes"
	"strings"
	"testing"
)

func TestGenerateAndParse(t *testing.T) {
	t.Parallel()

	pepper := []byte(strings.Repeat("p", 32))
	capability, err := generate(bytes.NewReader(bytes.Repeat([]byte{0x42}, 48)), pepper)
	if err != nil {
		t.Fatalf("generate() error = %v", err)
	}
	if capability.Raw == "" || strings.Contains(capability.Raw, "=") {
		t.Fatalf("Raw = %q, want unpadded URL-safe token", capability.Raw)
	}
	sessionID, digest, err := Parse(capability.Raw, pepper)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if sessionID != capability.SessionID {
		t.Errorf("session ID = %q, want %q", sessionID, capability.SessionID)
	}
	if !Matches(digest, capability.Digest) {
		t.Error("parsed digest does not match generated digest")
	}
}

func TestCapabilityDigestDependsOnPepper(t *testing.T) {
	t.Parallel()

	capability, err := generate(bytes.NewReader(bytes.Repeat([]byte{0x24}, 48)), []byte(strings.Repeat("a", 32)))
	if err != nil {
		t.Fatalf("generate() error = %v", err)
	}
	_, otherDigest, err := Parse(capability.Raw, []byte(strings.Repeat("b", 32)))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if Matches(otherDigest, capability.Digest) {
		t.Error("digest should change when the pepper changes")
	}
}

func TestParseRejectsMalformedCapabilities(t *testing.T) {
	t.Parallel()

	pepper := []byte(strings.Repeat("p", 32))
	for _, raw := range []string{
		"",
		"not-a-uuid.secret",
		"42424242-4242-4242-8242-424242424242.short",
		"42424242-4242-4242-8242-424242424242.secret.extra",
	} {
		if _, _, err := Parse(raw, pepper); err == nil {
			t.Errorf("Parse(%q) error = nil", raw)
		}
	}
}

func TestPepperMustBeHighEntropy(t *testing.T) {
	t.Parallel()

	if _, err := Generate([]byte("too-short")); err == nil {
		t.Fatal("Generate() error = nil")
	}
}
