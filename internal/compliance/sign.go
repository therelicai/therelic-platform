package compliance

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
)

// Signer abstracts the pack-signing strategy. v0 = HMAC; v1 adds GPG
// detached signatures. The interface is small enough that we can swap
// implementations later without touching the pack assembler.
type Signer interface {
	Kind() string                        // "hmac" | "gpg"
	Sign(payload []byte) ([]byte, error) // detached signature bytes
	Verify(payload, sig []byte) error
	KeyID() string
}

// NewHMACSigner builds a Signer over HMAC-SHA256 with the given key.
// keyHex must be at least 16 bytes after hex-decode; an empty key is
// rejected so misconfiguration is loud.
func NewHMACSigner(keyHex string) (Signer, error) {
	if keyHex == "" {
		return nil, errors.New("hmac signer: empty key")
	}
	key, err := hex.DecodeString(keyHex)
	if err != nil {
		return nil, fmt.Errorf("hmac signer: invalid hex key: %w", err)
	}
	if len(key) < 16 {
		return nil, errors.New("hmac signer: key must be at least 16 bytes (32 hex chars)")
	}
	return &hmacSigner{key: key}, nil
}

type hmacSigner struct {
	key []byte
}

func (*hmacSigner) Kind() string { return "hmac" }

func (s *hmacSigner) Sign(payload []byte) ([]byte, error) {
	mac := hmac.New(sha256.New, s.key)
	mac.Write(payload)
	return mac.Sum(nil), nil
}

func (s *hmacSigner) Verify(payload, sig []byte) error {
	want, _ := s.Sign(payload)
	if !hmac.Equal(want, sig) {
		return errors.New("signature mismatch")
	}
	return nil
}

func (s *hmacSigner) KeyID() string {
	// Stable per-key id: first 8 hex chars of HMAC(key, "key-id-derivation").
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte("key-id-derivation"))
	sum := mac.Sum(nil)
	return hex.EncodeToString(sum[:4])
}
