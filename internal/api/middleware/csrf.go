package middleware

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
)

// CSRF implements the double-submit cookie pattern.
//
// On every response we ensure a relic_csrf cookie is present
// (SameSite=Strict, Secure, NOT HttpOnly — the SPA needs to read it
// to mirror in the X-CSRF-Token header). Mutating requests (POST,
// PUT, PATCH, DELETE) must include X-CSRF-Token matching the cookie.
//
// Exemptions:
//   - GET / HEAD / OPTIONS — non-mutating.
//   - Bearer rk_* auth — API key clients aren't browsers and don't
//     send cookies; CSRF doesn't apply.
//   - Routes the operator marks as exempt via the WithCSRFExempt
//     helper (the /v1/auth/* routes that BOOTSTRAP a session need
//     to be reachable before a token exists).
type CSRF struct {
	cookieName string
	headerName string
	cookiePath string
	secure     bool
	// exemptRoutes is a set of exact paths skipped entirely. The
	// global rate limiter and auth middleware still apply.
	exemptRoutes map[string]struct{}
}

func NewCSRF(secure bool, exemptRoutes ...string) *CSRF {
	em := make(map[string]struct{}, len(exemptRoutes))
	for _, p := range exemptRoutes {
		em[p] = struct{}{}
	}
	return &CSRF{
		cookieName:   "relic_csrf",
		headerName:   "X-CSRF-Token",
		cookiePath:   "/",
		secure:       secure,
		exemptRoutes: em,
	}
}

// Middleware enforces CSRF on mutating requests. Mounts as
// router-wide middleware in server.go.
func (c *CSRF) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Always make sure a token cookie exists so the SPA can
		// observe + mirror it. mintIfMissing is idempotent.
		c.mintIfMissing(w, r)

		// Non-mutating requests skip the verify step.
		if isSafeMethod(r.Method) {
			next.ServeHTTP(w, r)
			return
		}
		// Path-based exemptions (e.g. /v1/auth/oidc/callback, which
		// runs before the user has a CSRF cookie scoped to the API).
		if _, ok := c.exemptRoutes[r.URL.Path]; ok {
			next.ServeHTTP(w, r)
			return
		}
		// Bearer rk_* (API key) clients aren't browsers.
		if strings.HasPrefix(strings.TrimSpace(r.Header.Get("Authorization")), "Bearer rk_") {
			next.ServeHTTP(w, r)
			return
		}
		// If the caller has no session cookie at all, CSRF doesn't
		// apply — there's no session for a cross-site forgery to
		// hijack. Auth middleware downstream will reject with 401
		// instead. This keeps stateless Bearer-token clients (the
		// CLI, agents) working without CSRF setup.
		if _, err := r.Cookie("relic_session"); err != nil {
			next.ServeHTTP(w, r)
			return
		}

		cookie, err := r.Cookie(c.cookieName)
		if err != nil || cookie.Value == "" {
			writeCSRFError(w, "csrf cookie missing")
			return
		}
		header := r.Header.Get(c.headerName)
		if header == "" {
			writeCSRFError(w, "csrf header missing")
			return
		}
		if subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(header)) != 1 {
			writeCSRFError(w, "csrf token mismatch")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (c *CSRF) mintIfMissing(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(c.cookieName); err == nil && cookie.Value != "" {
		return
	}
	tok, err := newToken()
	if err != nil {
		return // best-effort
	}
	http.SetCookie(w, &http.Cookie{
		Name:     c.cookieName,
		Value:    tok,
		Path:     c.cookiePath,
		HttpOnly: false, // SPA must read this to mirror in header
		Secure:   c.secure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   8 * 60 * 60, // 8h matches typical work session
	})
}

func isSafeMethod(m string) bool {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	return false
}

func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func writeCSRFError(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte(`{"error":"` + msg + `"}`))
}
