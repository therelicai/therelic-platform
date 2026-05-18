package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

// LocalProvider authenticates users against the platform's own users
// table. Passwords are stored as bcrypt hashes in users.password_hash
// (added in migration 013_local_auth.sql). Tokens are HS256-signed
// with RELIC_JWT_SECRET, with claims shaped so the same middleware
// (and the same RLS get_org_id() function) work across local /
// supabase / OIDC modes.
//
// LocalProvider does not handle the HTTP POST /v1/auth/login flow
// directly; that lives in internal/api/auth_handlers.go and calls
// LookupAndVerifyPassword + IssueToken on this provider.
type LocalProvider struct {
	jwtSecret []byte
	tokenTTL  time.Duration
	issuer    string
	audience  string
}

// LocalConfig configures a LocalProvider. JWTSecret must be non-empty;
// the API refuses to boot otherwise.
type LocalConfig struct {
	JWTSecret string
	// TokenTTL defaults to 24h when zero. Refresh-token support comes
	// later (Phase 1 of ROADMAP); for now sessions are short-ish JWTs
	// with no refresh.
	TokenTTL time.Duration
	// Issuer + Audience are optional. When set, IssueToken stamps
	// them and Verify enforces them. Self-hosters typically leave
	// blank; OIDC bridges and CI use them.
	Issuer   string
	Audience string
}

func NewLocalProvider(cfg LocalConfig) (*LocalProvider, error) {
	if cfg.JWTSecret == "" {
		return nil, errors.New("RELIC_JWT_SECRET is required for RELIC_AUTH_MODE=local")
	}
	ttl := cfg.TokenTTL
	if ttl == 0 {
		ttl = 24 * time.Hour
	}
	return &LocalProvider{
		jwtSecret: []byte(cfg.JWTSecret),
		tokenTTL:  ttl,
		issuer:    cfg.Issuer,
		audience:  cfg.Audience,
	}, nil
}

func (*LocalProvider) Mode() Mode { return ModeLocal }

func (p *LocalProvider) Verify(_ context.Context, token string) (Claims, error) {
	parsed := jwt.MapClaims{}
	parseOpts := []jwt.ParserOption{jwt.WithValidMethods([]string{"HS256"})}
	if p.issuer != "" {
		parseOpts = append(parseOpts, jwt.WithIssuer(p.issuer))
	}
	if p.audience != "" {
		parseOpts = append(parseOpts, jwt.WithAudience(p.audience))
	}
	t, err := jwt.ParseWithClaims(token, parsed, func(*jwt.Token) (any, error) {
		return p.jwtSecret, nil
	}, parseOpts...)
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
	// Local-issued tokens use the same app_metadata.org_id shape as
	// Supabase so the RLS get_org_id() function works without a mode
	// branch.
	if md, ok := parsed["app_metadata"].(map[string]any); ok {
		if orgID, ok := md["org_id"].(string); ok {
			c.OrgID = orgID
		}
	}
	return c, nil
}

func (p *LocalProvider) IssueToken(_ context.Context, c Claims) (string, error) {
	if c.UserID == "" || c.OrgID == "" {
		return "", errors.New("issue token: UserID and OrgID are both required")
	}
	now := time.Now()
	claims := jwt.MapClaims{
		"sub":   c.UserID,
		"email": c.Email,
		"iat":   now.Unix(),
		"exp":   now.Add(p.tokenTTL).Unix(),
		"app_metadata": map[string]any{
			"org_id": c.OrgID,
		},
	}
	if p.issuer != "" {
		claims["iss"] = p.issuer
	}
	if p.audience != "" {
		claims["aud"] = p.audience
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return tok.SignedString(p.jwtSecret)
}

// HashPassword runs bcrypt with the default cost (10) over the
// password. Storing the result in users.password_hash is the contract
// the POST /v1/auth/login handler expects.
func HashPassword(plaintext string) (string, error) {
	if len(plaintext) < 8 {
		return "", errors.New("password must be at least 8 characters")
	}
	b, err := bcrypt.GenerateFromPassword([]byte(plaintext), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("bcrypt: %w", err)
	}
	return string(b), nil
}

// VerifyPassword compares a plaintext password against a stored
// bcrypt hash. Returns nil on match, an error otherwise (the caller
// must translate to 401 without leaking which factor failed).
func VerifyPassword(hash, plaintext string) error {
	if hash == "" {
		return errors.New("no password set for this account")
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plaintext))
}
