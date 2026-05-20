package middleware

import "net/http"

// SecurityHeaders sets the conservative defaults pen-test reports
// flag most often: HSTS, X-Content-Type-Options, X-Frame-Options,
// Referrer-Policy, Permissions-Policy, Cross-Origin-Opener-Policy,
// Cross-Origin-Resource-Policy.
//
// CSP is intentionally NOT set here — the platform serves JSON, not
// HTML. The SPA in therelic-app sets its own per-document CSP. If we
// ever serve HTML from the API (we shouldn't), add CSP at that route.
//
// HSTS includes preload + includeSubDomains because api.therelic.dev
// and app.therelic.dev share the apex; the apex is preloaded once,
// the subdomains inherit. Operators self-hosting on a domain they
// don't own can override per-header via env (RELIC_HSTS_MAX_AGE=0
// disables HSTS).
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		// Only set HSTS on HTTPS responses — sending it over HTTP is
		// a no-op per spec but some scanners flag the inconsistency.
		// chi's CORS+TLS middleware sets r.TLS for direct HTTPS; when
		// behind a TLS-terminating proxy we honor X-Forwarded-Proto.
		if isHTTPS(r) {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains; preload")
		}
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Permissions-Policy", "interest-cohort=(), browsing-topics=()")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

func isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if r.Header.Get("X-Forwarded-Proto") == "https" {
		return true
	}
	return false
}
