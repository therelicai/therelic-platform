package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/therelicai/therelic-platform/internal/auth"
	"github.com/therelicai/therelic-platform/internal/storage"
)

type contextKey string

const (
	CtxOrgID  contextKey = "org_id"
	CtxUserID contextKey = "user_id"
)

func OrgIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(CtxOrgID).(string)
	return v
}

func UserIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(CtxUserID).(string)
	return v
}

// Auth is the HTTP authentication middleware. Two paths land here:
//
//   1. Bearer rk_*  — API key. Validated against the api_keys table
//                     via storage.ValidateAPIKey. Mode-agnostic;
//                     works the same in local/supabase/oidc.
//
//   2. Bearer <JWT> — Verified by the configured auth.Provider. The
//                     provider knows which secret (or JWKS) backs the
//                     deployment and normalizes claims into a uniform
//                     {UserID, OrgID, Email} shape.
type Auth struct {
	db       *storage.Postgres
	provider auth.Provider
}

func NewAuth(db *storage.Postgres, provider auth.Provider) *Auth {
	return &Auth{db: db, provider: provider}
}

func (a *Auth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if header == "" {
			writeError(w, http.StatusUnauthorized, "missing Authorization header")
			return
		}

		parts := strings.SplitN(header, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			writeError(w, http.StatusUnauthorized, "invalid Authorization format")
			return
		}
		token := parts[1]

		// API key auth (prefixed with rk_).
		if strings.HasPrefix(token, "rk_") {
			orgID, err := a.db.ValidateAPIKey(r.Context(), token)
			if err != nil {
				writeError(w, http.StatusUnauthorized, "invalid API key")
				return
			}
			ctx := context.WithValue(r.Context(), CtxOrgID, orgID)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		// JWT auth: hand off to the provider for verification.
		// Any verify error (ErrInvalidToken, expiry, audience
		// mismatch, transient JWKS failure) collapses to a single
		// 401. Disclosing which factor failed would help brute-force
		// attempts against the signing secret.
		claims, err := a.provider.Verify(r.Context(), token)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid credentials")
			return
		}
		ctx := r.Context()
		if claims.UserID != "" {
			ctx = context.WithValue(ctx, CtxUserID, claims.UserID)
		}
		if claims.OrgID != "" {
			ctx = context.WithValue(ctx, CtxOrgID, claims.OrgID)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
