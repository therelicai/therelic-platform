package auth

import (
	"context"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// mockOIDCServer is a tiny in-process IdP that exposes the four
// endpoints the verifier touches: discovery, JWKS, token, and a
// nominal authorize endpoint. We don't actually do the redirect
// flow in tests — only the bits ExchangeAndVerify reaches.
type mockOIDCServer struct {
	srv      *httptest.Server
	key      *rsa.PrivateKey
	keyID    string
	issuer   string
	clientID string
	// nextIDToken is the ID-token returned by /token. Tests set it
	// before triggering an exchange.
	nextIDToken string
}

func newMockOIDCServer(t *testing.T, clientID string) *mockOIDCServer {
	t.Helper()
	key := mustGenRSA(t)
	m := &mockOIDCServer{key: key, keyID: "test-key-1", clientID: clientID}
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	m.srv = srv
	m.issuer = srv.URL

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 m.issuer,
			"authorization_endpoint": m.issuer + "/authorize",
			"token_endpoint":         m.issuer + "/token",
			"jwks_uri":               m.issuer + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
		})
	})

	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		n := base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes())
		e := base64.RawURLEncoding.EncodeToString([]byte{0x01, 0x00, 0x01})
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]any{{
				"kty": "RSA", "kid": m.keyID, "use": "sig", "alg": "RS256",
				"n": n, "e": e,
			}},
		})
	})

	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		// We don't enforce code_verifier in tests; the real IdP does.
		// We just hand back whatever ID-token the test stashed.
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "test-access-token",
			"token_type":   "Bearer",
			"expires_in":   3600,
			"id_token":     m.nextIDToken,
		})
	})

	mux.HandleFunc("/authorize", func(w http.ResponseWriter, _ *http.Request) {
		// Stub. Real flow would redirect with ?code=. Tests skip past
		// the redirect entirely.
		w.WriteHeader(http.StatusOK)
	})

	return m
}

// signIDToken returns an RS256-signed ID-token with the given claims.
func (m *mockOIDCServer) signIDToken(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	if _, ok := claims["iss"]; !ok {
		claims["iss"] = m.issuer
	}
	if _, ok := claims["aud"]; !ok {
		claims["aud"] = m.clientID
	}
	if _, ok := claims["iat"]; !ok {
		claims["iat"] = time.Now().Unix()
	}
	if _, ok := claims["exp"]; !ok {
		claims["exp"] = time.Now().Add(1 * time.Hour).Unix()
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = m.keyID
	s, err := tok.SignedString(m.key)
	if err != nil {
		t.Fatalf("sign id_token: %v", err)
	}
	return s
}

func mustGenRSA(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	// Use a fixed-size key. 2048 is overkill for tests but matches
	// what real IdPs ship and exercises the same path.
	key, err := rsaGen()
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	return key
}

func TestNewOIDCProvider_DiscoverySucceeds(t *testing.T) {
	m := newMockOIDCServer(t, "test-client")
	p, err := NewOIDCProvider(context.Background(), OIDCConfig{
		DiscoveryURL:     m.issuer,
		ClientID:         "test-client",
		RedirectURL:      "https://app.example/cb",
		SessionJWTSecret: "test-secret-please-change",
	})
	if err != nil {
		t.Fatalf("NewOIDCProvider: %v", err)
	}
	if p.Mode() != ModeOIDC {
		t.Errorf("Mode() = %v, want %v", p.Mode(), ModeOIDC)
	}
}

func TestNewOIDCProvider_RejectsMissingConfig(t *testing.T) {
	cases := []struct {
		name string
		cfg  OIDCConfig
	}{
		{"no discovery url", OIDCConfig{ClientID: "x", RedirectURL: "y", SessionJWTSecret: "z"}},
		{"no client id", OIDCConfig{DiscoveryURL: "https://x", RedirectURL: "y", SessionJWTSecret: "z"}},
		{"no redirect", OIDCConfig{DiscoveryURL: "https://x", ClientID: "x", SessionJWTSecret: "z"}},
		{"no session secret", OIDCConfig{DiscoveryURL: "https://x", ClientID: "x", RedirectURL: "y"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewOIDCProvider(context.Background(), tc.cfg); err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}

func TestOIDCProvider_IssueAndVerifySessionRoundTrip(t *testing.T) {
	m := newMockOIDCServer(t, "test-client")
	p, err := NewOIDCProvider(context.Background(), OIDCConfig{
		DiscoveryURL:     m.issuer,
		ClientID:         "test-client",
		RedirectURL:      "https://app.example/cb",
		SessionJWTSecret: "test-secret-please-change",
	})
	if err != nil {
		t.Fatalf("NewOIDCProvider: %v", err)
	}
	tok, err := p.IssueToken(context.Background(), Claims{
		UserID: "user-1", OrgID: "org-1", Email: "u@example.com",
	})
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	got, err := p.Verify(context.Background(), tok)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got.UserID != "user-1" || got.OrgID != "org-1" || got.Email != "u@example.com" {
		t.Fatalf("claims roundtrip = %+v", got)
	}
}

func TestOIDCProvider_VerifyRejectsBadSignature(t *testing.T) {
	m := newMockOIDCServer(t, "test-client")
	p, _ := NewOIDCProvider(context.Background(), OIDCConfig{
		DiscoveryURL:     m.issuer,
		ClientID:         "test-client",
		RedirectURL:      "https://app.example/cb",
		SessionJWTSecret: "secret-a",
	})
	// Sign with a different secret.
	other := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": "x", "exp": time.Now().Add(1 * time.Hour).Unix(),
	})
	bad, _ := other.SignedString([]byte("secret-b"))
	if _, err := p.Verify(context.Background(), bad); err == nil {
		t.Fatal("expected verify failure on bad signature")
	}
}

func TestOIDCProvider_AuthCodeURLIncludesPKCE(t *testing.T) {
	m := newMockOIDCServer(t, "test-client")
	p, _ := NewOIDCProvider(context.Background(), OIDCConfig{
		DiscoveryURL:     m.issuer,
		ClientID:         "test-client",
		RedirectURL:      "https://app.example/cb",
		SessionJWTSecret: "secret",
	})
	verifier, challenge, err := GeneratePKCEPair()
	if err != nil {
		t.Fatalf("GeneratePKCEPair: %v", err)
	}
	if len(verifier) < 43 || len(verifier) > 128 {
		t.Errorf("verifier length out of RFC range: %d", len(verifier))
	}
	// Confirm the challenge actually matches SHA256 of the verifier.
	want := sha256.Sum256([]byte(verifier))
	wantStr := base64.RawURLEncoding.EncodeToString(want[:])
	if challenge != wantStr {
		t.Errorf("challenge mismatch: got=%s want=%s", challenge, wantStr)
	}
	state, _ := GenerateState()
	nonce, _ := GenerateNonce()
	u := p.AuthCodeURL(state, nonce, challenge)
	if !strings.Contains(u, "code_challenge="+challenge) {
		t.Errorf("AuthCodeURL missing PKCE challenge: %s", u)
	}
	if !strings.Contains(u, "code_challenge_method=S256") {
		t.Errorf("AuthCodeURL missing S256 method: %s", u)
	}
	if !strings.Contains(u, "nonce="+nonce) {
		t.Errorf("AuthCodeURL missing nonce: %s", u)
	}
}

func TestOIDCProvider_ExchangeAndVerify(t *testing.T) {
	m := newMockOIDCServer(t, "test-client")
	p, _ := NewOIDCProvider(context.Background(), OIDCConfig{
		DiscoveryURL:     m.issuer,
		ClientID:         "test-client",
		RedirectURL:      "https://app.example/cb",
		SessionJWTSecret: "secret",
	})
	nonce, _ := GenerateNonce()
	m.nextIDToken = m.signIDToken(t, jwt.MapClaims{
		"sub":            "user-sub-1",
		"email":          "alice@example.com",
		"email_verified": true,
		"nonce":          nonce,
		"name":           "Alice",
	})
	c, err := p.ExchangeAndVerify(context.Background(), "fake-code", "fake-verifier", nonce)
	if err != nil {
		t.Fatalf("ExchangeAndVerify: %v", err)
	}
	if c.Subject != "user-sub-1" || c.Email != "alice@example.com" || !c.EmailVerified {
		t.Fatalf("claims = %+v", c)
	}
}

func TestOIDCProvider_ExchangeRejectsBadNonce(t *testing.T) {
	m := newMockOIDCServer(t, "test-client")
	p, _ := NewOIDCProvider(context.Background(), OIDCConfig{
		DiscoveryURL:     m.issuer,
		ClientID:         "test-client",
		RedirectURL:      "https://app.example/cb",
		SessionJWTSecret: "secret",
	})
	m.nextIDToken = m.signIDToken(t, jwt.MapClaims{
		"sub":   "u",
		"email": "u@example.com",
		"nonce": "the-nonce-we-stored",
	})
	if _, err := p.ExchangeAndVerify(context.Background(), "code", "verifier", "a-different-nonce"); err == nil {
		t.Fatal("expected nonce mismatch error")
	}
}

func TestOIDCProvider_ExchangeRejectsWrongAudience(t *testing.T) {
	m := newMockOIDCServer(t, "expected-client")
	p, _ := NewOIDCProvider(context.Background(), OIDCConfig{
		DiscoveryURL:     m.issuer,
		ClientID:         "expected-client",
		RedirectURL:      "https://app.example/cb",
		SessionJWTSecret: "secret",
	})
	// Sign with a different aud.
	m.nextIDToken = m.signIDToken(t, jwt.MapClaims{
		"sub": "u", "aud": "other-client",
	})
	if _, err := p.ExchangeAndVerify(context.Background(), "code", "verifier", ""); err == nil {
		t.Fatal("expected audience-mismatch error")
	}
}

func TestNormalizeIssuer_TrimsWellKnown(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://accounts.google.com", "https://accounts.google.com"},
		{"https://accounts.google.com/", "https://accounts.google.com"},
		{"https://accounts.google.com/.well-known/openid-configuration", "https://accounts.google.com"},
	}
	for _, tc := range cases {
		if got := normalizeIssuer(tc.in); got != tc.want {
			t.Errorf("normalizeIssuer(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}

func TestProviderTagShape(t *testing.T) {
	m := newMockOIDCServer(t, "x")
	p, _ := NewOIDCProvider(context.Background(), OIDCConfig{
		DiscoveryURL:     m.issuer,
		ClientID:         "x",
		RedirectURL:      "https://app.example/cb",
		SessionJWTSecret: "secret",
	})
	tag := p.ProviderTag()
	if !strings.HasPrefix(tag, "oidc:") {
		t.Errorf("ProviderTag()=%q, want oidc: prefix", tag)
	}
}
