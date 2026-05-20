package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/oauth2"
)

// OIDCProvider authenticates users against an external OIDC IdP
// (Okta / Entra / Auth0 / Google). The provider discovers JWKS from
// the IdP's well-known endpoint, verifies ID-token signatures, and
// enforces audience + issuer claims.
//
// The HTTP layer (internal/api/oidc_handlers.go) drives the PKCE
// authorization-code flow against this provider; OIDCProvider itself
// is the verifier + token issuer once the user lands on the platform.
// Session JWTs we issue ourselves are HS256-signed with
// RELIC_JWT_SECRET so the existing middleware code path continues to
// work — the OIDC IdP authenticates the user once, after which we
// hand out a session token of our own.
type OIDCProvider struct {
	cfg          OIDCConfig
	verifier     *oidc.IDTokenVerifier
	provider     *oidc.Provider
	oauthCfg     *oauth2.Config
	sessionKey   []byte
	sessionTTL   time.Duration
	defaultOrgID string
	defaultRole  string
}

// OIDCConfig parameterizes the OIDC adapter. DiscoveryURL is the
// well-known issuer URL (NOT the discovery document path); the go-oidc
// library appends /.well-known/openid-configuration. Pass either the
// issuer ("https://accounts.google.com") OR the discovery URL — we
// normalize either form below.
type OIDCConfig struct {
	DiscoveryURL string
	ClientID     string
	ClientSecret string // optional for PKCE-only public clients
	RedirectURL  string
	Scopes       []string
	// DefaultOrgID is the org auto-provisioned users get dropped into.
	// Empty means "refuse unknown users". The hosted instance sets a
	// single shared org so prospects can sign in without a manual
	// invite step.
	DefaultOrgID string
	// DefaultRole is the role granted to auto-provisioned users.
	// "member" if empty.
	DefaultRole string
	// SessionJWTSecret signs the session tokens we issue after a
	// successful OIDC exchange. Same secret the local provider uses
	// (RELIC_JWT_SECRET) so the API middleware verifies them with one
	// code path.
	SessionJWTSecret string
	// SessionTTL defaults to 24h.
	SessionTTL time.Duration
}

// NewOIDCProvider builds and returns an OIDCProvider. Performs the
// discovery + JWKS load synchronously so misconfiguration is loud at
// boot rather than first-login.
func NewOIDCProvider(ctx context.Context, cfg OIDCConfig) (*OIDCProvider, error) {
	if cfg.DiscoveryURL == "" {
		return nil, errors.New("RELIC_OIDC_DISCOVERY_URL is required")
	}
	if cfg.ClientID == "" {
		return nil, errors.New("RELIC_OIDC_CLIENT_ID is required")
	}
	if cfg.RedirectURL == "" {
		return nil, errors.New("RELIC_OIDC_REDIRECT_URL is required")
	}
	if cfg.SessionJWTSecret == "" {
		return nil, errors.New("RELIC_JWT_SECRET is required even in OIDC mode (signs session tokens)")
	}

	issuer := normalizeIssuer(cfg.DiscoveryURL)
	prov, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery: %w", err)
	}
	verifier := prov.Verifier(&oidc.Config{ClientID: cfg.ClientID})

	scopes := cfg.Scopes
	if len(scopes) == 0 {
		scopes = []string{oidc.ScopeOpenID, "email", "profile"}
	}

	oauthCfg := &oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		RedirectURL:  cfg.RedirectURL,
		Endpoint:     prov.Endpoint(),
		Scopes:       scopes,
	}

	ttl := cfg.SessionTTL
	if ttl == 0 {
		ttl = 24 * time.Hour
	}
	role := cfg.DefaultRole
	if role == "" {
		role = "member"
	}

	return &OIDCProvider{
		cfg:          cfg,
		verifier:     verifier,
		provider:     prov,
		oauthCfg:     oauthCfg,
		sessionKey:   []byte(cfg.SessionJWTSecret),
		sessionTTL:   ttl,
		defaultOrgID: cfg.DefaultOrgID,
		defaultRole:  role,
	}, nil
}

func (*OIDCProvider) Mode() Mode { return ModeOIDC }

// Verify validates a Relic session JWT (HS256). Bearer tokens reaching
// the API middleware after a successful OIDC login are our own
// session tokens, not raw ID-tokens. We do NOT verify raw ID-tokens
// per request — they're long-lived (1h with Google), exposed in
// browser network traces during the callback, and not what we want
// driving authorization decisions.
func (p *OIDCProvider) Verify(_ context.Context, token string) (Claims, error) {
	parsed := jwt.MapClaims{}
	t, err := jwt.ParseWithClaims(token, parsed, func(*jwt.Token) (any, error) {
		return p.sessionKey, nil
	}, jwt.WithValidMethods([]string{"HS256"}))
	if err != nil || !t.Valid {
		return Claims{}, fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}
	c := Claims{}
	if sub, ok := parsed["sub"].(string); ok {
		c.UserID = sub
	}
	if email, ok := parsed["email"].(string); ok {
		c.Email = email
	}
	if md, ok := parsed["app_metadata"].(map[string]any); ok {
		if orgID, ok := md["org_id"].(string); ok {
			c.OrgID = orgID
		}
	}
	return c, nil
}

// IssueToken signs a session JWT for the given claims. Called by the
// /v1/auth/oidc/callback handler after a successful ID-token exchange,
// not per request. Same shape as LocalProvider so the middleware
// extraction code path is unchanged.
func (p *OIDCProvider) IssueToken(_ context.Context, c Claims) (string, error) {
	if c.UserID == "" || c.OrgID == "" {
		return "", errors.New("issue token: UserID and OrgID are both required")
	}
	now := time.Now()
	claims := jwt.MapClaims{
		"sub":   c.UserID,
		"email": c.Email,
		"iat":   now.Unix(),
		"exp":   now.Add(p.sessionTTL).Unix(),
		"app_metadata": map[string]any{
			"org_id": c.OrgID,
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return tok.SignedString(p.sessionKey)
}

// OIDCClaims is the subset of standard OIDC claims we extract from
// the IdP's ID-token. Email may be empty for IdPs that don't expose
// it without the email scope; the handler refuses to auto-provision
// in that case.
type OIDCClaims struct {
	Subject       string `json:"sub"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	Name          string `json:"name"`
}

// ExchangeAndVerify runs the authorization-code → ID-token exchange
// with PKCE, then verifies signature + audience + issuer + nonce.
// Returns the extracted claims so the handler can map them to a Relic
// user.
func (p *OIDCProvider) ExchangeAndVerify(ctx context.Context, code, codeVerifier, nonce string) (*OIDCClaims, error) {
	tok, err := p.oauthCfg.Exchange(ctx, code, oauth2.SetAuthURLParam("code_verifier", codeVerifier))
	if err != nil {
		return nil, fmt.Errorf("token exchange: %w", err)
	}
	rawID, ok := tok.Extra("id_token").(string)
	if !ok || rawID == "" {
		return nil, errors.New("token response missing id_token")
	}
	idTok, err := p.verifier.Verify(ctx, rawID)
	if err != nil {
		return nil, fmt.Errorf("verify id_token: %w", err)
	}
	if nonce != "" && idTok.Nonce != nonce {
		return nil, errors.New("nonce mismatch")
	}
	var c OIDCClaims
	if err := idTok.Claims(&c); err != nil {
		return nil, fmt.Errorf("decode id_token claims: %w", err)
	}
	if c.Subject == "" {
		return nil, errors.New("id_token missing sub claim")
	}
	return &c, nil
}

// AuthCodeURL returns the IdP authorization URL the SPA / callback
// flow should redirect to. state + nonce should be random and stored
// (encrypted, short-lived) so the callback handler can validate them.
// codeChallenge is the SHA256(base64url) of the verifier the caller
// generated; we pass it through as S256.
func (p *OIDCProvider) AuthCodeURL(state, nonce, codeChallenge string) string {
	return p.oauthCfg.AuthCodeURL(state,
		oauth2.SetAuthURLParam("nonce", nonce),
		oauth2.SetAuthURLParam("code_challenge", codeChallenge),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
	)
}

// DefaultOrgID exposes the auto-provisioning target. Handlers consult
// this to decide whether to create a user on first login or refuse.
func (p *OIDCProvider) DefaultOrgID() string { return p.defaultOrgID }

// DefaultRole exposes the role granted to auto-provisioned users.
func (p *OIDCProvider) DefaultRole() string { return p.defaultRole }

// ProviderTag returns the auth_provider string for the users table,
// of the form "oidc:<issuer>". Used by storage methods to refuse
// cross-provider login attempts.
func (p *OIDCProvider) ProviderTag() string {
	return "oidc:" + normalizeIssuer(p.cfg.DiscoveryURL)
}

// normalizeIssuer strips the /.well-known/openid-configuration suffix
// if the operator pasted the discovery URL instead of the bare issuer.
// go-oidc adds the suffix back internally.
func normalizeIssuer(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "/")
	s = strings.TrimSuffix(s, "/.well-known/openid-configuration")
	return s
}

// GenerateState returns a fresh random state token for the OIDC flow.
// 32 bytes hex-encoded; non-guessable, used to bind a redirect cookie
// to the eventual callback.
func GenerateState() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// GenerateNonce returns a fresh random nonce for replay-protecting
// the ID-token. RFC 7636 doesn't mandate it, but every IdP supports
// it and it adds zero cost.
func GenerateNonce() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// GeneratePKCEPair returns (verifier, challenge) where challenge =
// base64url(sha256(verifier)). Verifier is 43-128 chars per RFC 7636;
// we use 64 bytes raw -> 86 char base64url, well inside the range.
func GeneratePKCEPair() (verifier, challenge string, err error) {
	b := make([]byte, 64)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	verifier = base64.RawURLEncoding.EncodeToString(b)
	challenge = pkceChallengeS256(verifier)
	return verifier, challenge, nil
}

func pkceChallengeS256(verifier string) string {
	h := sha256Sum([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h)
}
