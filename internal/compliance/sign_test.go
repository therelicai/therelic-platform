package compliance

import (
	"strings"
	"testing"
)

func TestNewHMACSigner_RejectsBadKeys(t *testing.T) {
	cases := []string{"", "ZZZZ", "0102"} // empty, non-hex, too short
	for _, k := range cases {
		if _, err := NewHMACSigner(k); err == nil {
			t.Errorf("expected error for key %q", k)
		}
	}
}

func TestHMACSigner_RoundTrip(t *testing.T) {
	s, err := NewHMACSigner(strings.Repeat("ab", 16)) // 32-byte key
	if err != nil {
		t.Fatalf("NewHMACSigner: %v", err)
	}
	payload := []byte(`{"manifest":"v1"}`)
	sig, err := s.Sign(payload)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if err := s.Verify(payload, sig); err != nil {
		t.Errorf("Verify (matching) failed: %v", err)
	}
	if err := s.Verify([]byte(`{"manifest":"tampered"}`), sig); err == nil {
		t.Error("Verify(tampered) succeeded; expected mismatch")
	}
}

func TestHMACSigner_KeyIDIsDeterministic(t *testing.T) {
	k := strings.Repeat("ab", 16)
	s1, _ := NewHMACSigner(k)
	s2, _ := NewHMACSigner(k)
	if s1.KeyID() != s2.KeyID() {
		t.Errorf("KeyID not deterministic: %s vs %s", s1.KeyID(), s2.KeyID())
	}
	s3, _ := NewHMACSigner(strings.Repeat("cd", 16))
	if s1.KeyID() == s3.KeyID() {
		t.Error("different keys produce same key id")
	}
}
