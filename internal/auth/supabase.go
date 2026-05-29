package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/golang-jwt/jwt/v5"
)

// SupabaseProvider verifies HS256 JWTs signed with the project's
// SUPABASE_JWT_SECRET. Token issuance happens out-of-band via
// Supabase Auth; IssueToken returns ErrIssueUnsupported.
//
// Org binding: a Supabase signup runs the handle_new_user trigger
// from migrations.supabase/001_supabase_user_sync.sql, which sets
// app_metadata.org_id on the user. We pull that out of the claims
// here.
type SupabaseProvider struct {
	jwtSecret []byte
	issuer    string // optional, RELIC_JWT_ISSUER
	audience  string // optional, RELIC_JWT_AUDIENCE
}

// NewSupabaseProvider constructs a SupabaseProvider. Returns an error
// when the secret is empty because there's no legitimate path where
// supabase mode is correct with no secret.
//
// Warns when RELIC_JWT_ISSUER or RELIC_JWT_AUDIENCE are unset: without
// them, Verify accepts any HS256 token signed with the shared secret
// regardless of who issued it or what audience it targets, which
// weakens defense-in-depth if the secret ever leaks (token replay
// from a sibling Supabase project, mis-routed tokens, etc.).
func NewSupabaseProvider(secret string) (*SupabaseProvider, error) {
	if secret == "" {
		return nil, errors.New("SUPABASE_JWT_SECRET is required for RELIC_AUTH_MODE=supabase")
	}
	issuer := os.Getenv("RELIC_JWT_ISSUER")
	audience := os.Getenv("RELIC_JWT_AUDIENCE")
	if issuer == "" || audience == "" {
		slog.Warn("supabase auth: issuer/audience binding not configured — tokens accepted without iss/aud check",
			"issuer_set", issuer != "",
			"audience_set", audience != "",
			"recommendation", "set RELIC_JWT_ISSUER and RELIC_JWT_AUDIENCE to pin token origin")
	}
	return &SupabaseProvider{
		jwtSecret: []byte(secret),
		issuer:    issuer,
		audience:  audience,
	}, nil
}

func (p *SupabaseProvider) Mode() Mode { return ModeSupabase }

func (p *SupabaseProvider) Verify(_ context.Context, token string) (Claims, error) {
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
	// Supabase places org_id under app_metadata, set by the
	// handle_new_user trigger. Fall back to top-level org_id for
	// hand-issued tokens (CI, ops scripts).
	if md, ok := parsed["app_metadata"].(map[string]any); ok {
		if orgID, ok := md["org_id"].(string); ok {
			c.OrgID = orgID
		}
	}
	if c.OrgID == "" {
		if orgID, ok := parsed["org_id"].(string); ok {
			c.OrgID = orgID
		}
	}
	return c, nil
}

func (*SupabaseProvider) IssueToken(_ context.Context, _ Claims) (string, error) {
	return "", ErrIssueUnsupported
}
