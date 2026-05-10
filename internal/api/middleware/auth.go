package middleware

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"os"
	"strings"

	"github.com/golang-jwt/jwt/v5"

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

type Auth struct {
	db        *storage.Postgres
	jwtSecret []byte
}

func NewAuth(db *storage.Postgres, jwtSecret string) *Auth {
	return &Auth{db: db, jwtSecret: []byte(jwtSecret)}
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

		// API key auth (prefixed with rk_)
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

		// JWT auth (Supabase Auth).
		//
		// Pin to HS256 — the only signing method we ever issue or
		// accept. Without this, jwt-go's default validator accepts
		// anything advertised in the alg header, which historically
		// has been the source of "none" and "RS256 → HS256" key
		// confusion attacks. iss and aud are optional but enforced
		// when RELIC_JWT_ISSUER / RELIC_JWT_AUDIENCE are set so
		// self-hosters can pin against their identity provider.
		if len(a.jwtSecret) > 0 && !isAllZero(a.jwtSecret) {
			claims := jwt.MapClaims{}
			parseOpts := []jwt.ParserOption{
				jwt.WithValidMethods([]string{"HS256"}),
			}
			if iss := os.Getenv("RELIC_JWT_ISSUER"); iss != "" {
				parseOpts = append(parseOpts, jwt.WithIssuer(iss))
			}
			if aud := os.Getenv("RELIC_JWT_AUDIENCE"); aud != "" {
				parseOpts = append(parseOpts, jwt.WithAudience(aud))
			}
			parsed, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (any, error) {
				return a.jwtSecret, nil
			}, parseOpts...)
			if err == nil && parsed.Valid {
				ctx := r.Context()
				if sub, ok := claims["sub"].(string); ok {
					ctx = context.WithValue(ctx, CtxUserID, sub)
				}
				// org_id may live under app_metadata (Supabase
				// convention) or as a top-level claim (self-host).
				// Try both, app_metadata wins when both are present
				// because it's what the SaaS path issues.
				if md, ok := claims["app_metadata"].(map[string]any); ok {
					if orgID, ok := md["org_id"].(string); ok {
						ctx = context.WithValue(ctx, CtxOrgID, orgID)
					}
				}
				if OrgIDFromContext(ctx) == "" {
					if orgID, ok := claims["org_id"].(string); ok {
						ctx = context.WithValue(ctx, CtxOrgID, orgID)
					}
				}
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
		}

		writeError(w, http.StatusUnauthorized, "invalid credentials")
	})
}

func isAllZero(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return len(b) == 0 || subtle.ConstantTimeCompare(b, make([]byte, len(b))) == 1
}

func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
