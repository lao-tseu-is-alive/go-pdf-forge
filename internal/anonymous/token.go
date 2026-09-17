// Package anonymous creates and verifies high-entropy anonymous session
// capabilities. Only the digest is persisted; Raw is returned to the client
// once and must be treated like a credential.
package anonymous

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"strings"
)

const (
	secretBytes    = 32
	pepperBytes    = 32
	ipDigestDomain = "go-pdf-forge/anonymous-ip/v1\x00"
)

// Capability is the one-time credential issued for an anonymous session. Raw
// belongs only to the client; persistence layers store Digest instead.
type Capability struct {
	// SessionID is the public UUID embedded in Raw and used for indexed lookup.
	SessionID string
	// Raw is the bearer credential returned once to the client and never logged.
	Raw string
	// Digest is the HMAC-SHA-256 value safe to persist instead of Raw.
	Digest [sha256.Size]byte
}

// Generate creates a session UUID and a 256-bit secret using crypto/rand, then
// derives the digest that the server persists.
func Generate(pepper []byte) (Capability, error) {
	return generate(rand.Reader, pepper)
}

// Parse validates a raw capability, returns its embedded session UUID, and
// derives the digest used for constant-time comparison with persisted state.
func Parse(raw string, pepper []byte) (sessionID string, digest [sha256.Size]byte, err error) {
	if err := validatePepper(pepper); err != nil {
		return "", digest, err
	}
	sessionID, encodedSecret, ok := strings.Cut(raw, ".")
	if !ok || !validUUID(sessionID) || encodedSecret == "" || strings.Contains(encodedSecret, ".") {
		return "", digest, errors.New("invalid anonymous capability")
	}
	secret, err := base64.RawURLEncoding.DecodeString(encodedSecret)
	if err != nil || len(secret) != secretBytes {
		return "", digest, errors.New("invalid anonymous capability")
	}
	return sessionID, tokenDigest(pepper, secret), nil
}

// Matches compares capability digests in constant time.
func Matches(actual, expected [sha256.Size]byte) bool {
	return hmac.Equal(actual[:], expected[:])
}

// DigestIP returns a deployment-specific HMAC of a canonical client address.
// IPv4-mapped addresses are normalized so one client cannot acquire distinct
// quota identities by switching textual representations.
func DigestIP(pepper []byte, address netip.Addr) ([sha256.Size]byte, error) {
	var digest [sha256.Size]byte
	if err := validatePepper(pepper); err != nil {
		return digest, err
	}
	if !address.IsValid() || address.IsUnspecified() {
		return digest, errors.New("client IP address is required")
	}
	address = address.Unmap()
	mac := hmac.New(sha256.New, pepper)
	_, _ = mac.Write([]byte(ipDigestDomain))
	_, _ = mac.Write(address.AsSlice())
	copy(digest[:], mac.Sum(nil))
	return digest, nil
}

func generate(random io.Reader, pepper []byte) (Capability, error) {
	if err := validatePepper(pepper); err != nil {
		return Capability{}, err
	}

	idBytes := make([]byte, 16)
	secret := make([]byte, secretBytes)
	if _, err := io.ReadFull(random, idBytes); err != nil {
		return Capability{}, fmt.Errorf("generate anonymous session ID: %w", err)
	}
	if _, err := io.ReadFull(random, secret); err != nil {
		return Capability{}, fmt.Errorf("generate anonymous session secret: %w", err)
	}
	idBytes[6] = (idBytes[6] & 0x0f) | 0x40
	idBytes[8] = (idBytes[8] & 0x3f) | 0x80
	sessionID := formatUUID(idBytes)
	raw := sessionID + "." + base64.RawURLEncoding.EncodeToString(secret)
	return Capability{SessionID: sessionID, Raw: raw, Digest: tokenDigest(pepper, secret)}, nil
}

func tokenDigest(pepper, secret []byte) [sha256.Size]byte {
	mac := hmac.New(sha256.New, pepper)
	_, _ = mac.Write(secret)
	var digest [sha256.Size]byte
	copy(digest[:], mac.Sum(nil))
	return digest
}

func validatePepper(pepper []byte) error {
	if len(pepper) < pepperBytes {
		return fmt.Errorf("anonymous token pepper must contain at least %d bytes", pepperBytes)
	}
	return nil
}

func formatUUID(value []byte) string {
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		value[0:4], value[4:6], value[6:8], value[8:10], value[10:16])
}

func validUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	for index, character := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			continue
		}
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return true
}
