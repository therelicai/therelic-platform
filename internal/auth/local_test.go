package auth

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestHashAndVerifyPassword(t *testing.T) {
	hash, err := HashPassword("correct-horse-battery-staple")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if hash == "" {
		t.Fatal("empty hash")
	}
	if err := VerifyPassword(hash, "correct-horse-battery-staple"); err != nil {
		t.Errorf("verify correct password: %v", err)
	}
	if err := VerifyPassword(hash, "wrong-password"); err == nil {
		t.Errorf("verify wrong password should fail")
	}
}

func TestHashPasswordRejectsShortInput(t *testing.T) {
	if _, err := HashPassword("short"); err == nil {
		t.Errorf("HashPassword should reject < 8 char input")
	}
}

func TestVerifyPasswordRejectsEmptyHash(t *testing.T) {
	if err := VerifyPassword("", "anything"); err == nil {
		t.Errorf("VerifyPassword should reject empty hash")
	}
}

func TestLocalProviderIssueAndVerifyRoundTrip(t *testing.T) {
	p, err := NewLocalProvider(LocalConfig{
		JWTSecret: "test-secret-32-bytes-of-randomness",
		TokenTTL:  1 * time.Hour,
	})
	if err != nil {
		t.Fatalf("provider: %v", err)
	}

	in := Claims{
		UserID: "user-123",
		OrgID:  "org-456",
		Email:  "admin@example.com",
	}
	tok, err := p.IssueToken(context.Background(), in)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if tok == "" {
		t.Fatal("empty token")
	}

	out, err := p.Verify(context.Background(), tok)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if out.UserID != in.UserID || out.OrgID != in.OrgID || out.Email != in.Email {
		t.Errorf("roundtrip mismatch: got %+v want %+v", out, in)
	}
}

func TestLocalProviderRejectsWrongSecret(t *testing.T) {
	a, _ := NewLocalProvider(LocalConfig{JWTSecret: "secret-a", TokenTTL: time.Hour})
	b, _ := NewLocalProvider(LocalConfig{JWTSecret: "secret-b", TokenTTL: time.Hour})
	tok, err := a.IssueToken(context.Background(), Claims{UserID: "u", OrgID: "o"})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if _, err := b.Verify(context.Background(), tok); err == nil {
		t.Errorf("provider with wrong secret should reject token")
	}
}

func TestLocalProviderIssueRequiresUserAndOrg(t *testing.T) {
	p, _ := NewLocalProvider(LocalConfig{JWTSecret: "secret", TokenTTL: time.Hour})
	if _, err := p.IssueToken(context.Background(), Claims{UserID: "", OrgID: "o"}); err == nil {
		t.Errorf("expected error when UserID empty")
	}
	if _, err := p.IssueToken(context.Background(), Claims{UserID: "u", OrgID: ""}); err == nil {
		t.Errorf("expected error when OrgID empty")
	}
}

func TestSupabaseProviderIssueUnsupported(t *testing.T) {
	p, err := NewSupabaseProvider("test-secret")
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	_, err = p.IssueToken(context.Background(), Claims{UserID: "u", OrgID: "o"})
	if err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Errorf("expected ErrIssueUnsupported, got %v", err)
	}
}

func TestParseModeRejectsUnknown(t *testing.T) {
	for _, in := range []string{"", "google", "bearer", "foo"} {
		if _, err := ParseMode(in); err == nil {
			t.Errorf("ParseMode(%q) should fail", in)
		}
	}
	for _, in := range []string{"local", "supabase", "oidc"} {
		if _, err := ParseMode(in); err != nil {
			t.Errorf("ParseMode(%q) should succeed: %v", in, err)
		}
	}
}
