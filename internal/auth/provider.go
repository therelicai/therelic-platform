// Package auth defines the AuthProvider interface and ships adapters
// for every supported authentication mode: local (HS256 with a
// platform-issued secret), supabase (HS256 with the Supabase project
// secret), and oidc (stub today; RS256 + JWKS in WS-2's Phase 1
// ROADMAP work).
//
// The adapter is selected at boot from RELIC_AUTH_MODE. The HTTP
// middleware in internal/api/middleware/auth.go calls Verify() on
// every authenticated request without caring which adapter is in
// play; switching auth modes is a config flag, not a code change.
package auth

import (
	"context"
	"errors"
	"fmt"
)

// Mode is the auth-provider selector. Keep these strings stable; they
// appear in environment variables, the migrations runner, and the
// users.auth_provider column.
type Mode string

const (
	ModeLocal    Mode = "local"
	ModeSupabase Mode = "supabase"
	ModeOIDC     Mode = "oidc"
)

// ParseMode normalizes an env-var value to a Mode. Returns an error
// rather than silently defaulting so misconfiguration is loud at boot.
func ParseMode(s string) (Mode, error) {
	switch Mode(s) {
	case ModeLocal, ModeSupabase, ModeOIDC:
		return Mode(s), nil
	case "":
		return "", errors.New("RELIC_AUTH_MODE is required (one of: local, supabase, oidc)")
	default:
		return "", fmt.Errorf("unknown RELIC_AUTH_MODE %q (expected: local, supabase, oidc)", s)
	}
}

// Claims is the auth-provider-independent claim shape every adapter
// produces. The HTTP middleware only ever sees this; provider-specific
// claim layouts (Supabase's app_metadata, OIDC's sub/aud) get
// normalized inside the adapter.
type Claims struct {
	UserID string
	OrgID  string
	Email  string
}

// Provider is the boot-time-selected authentication adapter.
//
// Verify is called per request to validate a bearer token and pull
// claims out of it. The middleware does not call Provider directly
// for API-key auth — that path goes through Postgres.ValidateAPIKey
// regardless of mode.
//
// IssueToken is optional: only LocalProvider implements it (to back
// POST /v1/auth/login). Supabase and OIDC issue tokens out-of-band
// via their own IdP; for those, IssueToken returns ErrIssueUnsupported.
type Provider interface {
	Mode() Mode
	Verify(ctx context.Context, token string) (Claims, error)
	IssueToken(ctx context.Context, claims Claims) (string, error)
}

// ErrInvalidToken is returned by Verify when the bearer token is
// malformed, signed with the wrong key, expired, or otherwise
// unacceptable. The middleware translates this to HTTP 401.
var ErrInvalidToken = errors.New("invalid token")

// ErrIssueUnsupported is returned by IssueToken on providers that
// don't issue tokens (supabase, oidc). Local mode supports issuance
// because it backs the POST /v1/auth/login flow.
var ErrIssueUnsupported = errors.New("token issuance not supported in this auth mode")
