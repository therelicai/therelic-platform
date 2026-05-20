package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/therelicai/therelic-platform/internal/auth"
	otelexp "github.com/therelicai/therelic-platform/internal/integrations/otel"
)

// OIDC HTTP handlers. Three routes mount when RELIC_AUTH_MODE=oidc:
//
//   GET  /v1/auth/oidc/login     — kick off PKCE flow; redirect to IdP
//   GET  /v1/auth/oidc/callback  — exchange code, set session cookie, redirect to SPA
//   POST /v1/auth/oidc/logout    — clear cookie + revoke server-side session
//
// The pre-auth-flow state (PKCE verifier, nonce, state token) lives
// in short-lived signed cookies. We sign with RELIC_JWT_SECRET (same
// key that signs session tokens) — the cookie is opaque to the
// browser and only the server reads it.
//
// We never store the IdP's access_token. Once we have an authenticated
// identity we issue our own session JWT and forget the IdP tokens.
// This keeps the per-request verification path cheap and avoids
// rotating IdP tokens across browser tabs.

const (
	oidcFlowCookie    = "relic_oidc_flow"
	oidcSessionCookie = "relic_session"
	oidcFlowTTL       = 10 * time.Minute
)

// oidcProviderFromServer returns the *OIDCProvider if the server is
// configured for OIDC mode, or nil otherwise. The handlers all check
// this and 503 if not configured — keeps the routes safe even when
// mistakenly mounted.
func (s *Server) oidcProvider() *auth.OIDCProvider {
	if p, ok := s.authProvider.(*auth.OIDCProvider); ok {
		return p
	}
	return nil
}

// handleOIDCLogin starts the PKCE authorization-code flow. Generates
// state + nonce + verifier, stashes them in a signed cookie, and
// redirects the user to the IdP's authorization endpoint.
func (s *Server) handleOIDCLogin(w http.ResponseWriter, r *http.Request) {
	p := s.oidcProvider()
	if p == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "OIDC not configured")
		return
	}
	state, err := auth.GenerateState()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "rand")
		return
	}
	nonce, err := auth.GenerateNonce()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "rand")
		return
	}
	verifier, challenge, err := auth.GeneratePKCEPair()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "rand")
		return
	}

	// "return_to" lets the SPA bounce the user back to the page they
	// originally requested. We accept only same-origin relative paths
	// to avoid open-redirect bugs.
	returnTo := r.URL.Query().Get("return_to")
	if !isSafeReturnTo(returnTo) {
		returnTo = "/"
	}

	flow := oidcFlowState{
		State:    state,
		Nonce:    nonce,
		Verifier: verifier,
		ReturnTo: returnTo,
	}
	cookieVal, err := encodeFlowCookie(s.sessionSecret(), flow)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "cookie")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     oidcFlowCookie,
		Value:    cookieVal,
		Path:     "/",
		HttpOnly: true,
		Secure:   !s.cookieInsecureForDev(),
		SameSite: http.SameSiteLaxMode, // Lax so the IdP redirect can send the cookie back.
		MaxAge:   int(oidcFlowTTL.Seconds()),
	})

	http.Redirect(w, r, p.AuthCodeURL(state, nonce, challenge), http.StatusFound)
}

// handleOIDCCallback handles the IdP's redirect back. Validates state,
// exchanges code for tokens (PKCE), verifies the ID-token signature
// + audience + issuer + nonce, then provisions or matches a Relic
// user and issues a session cookie.
func (s *Server) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	p := s.oidcProvider()
	if p == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "OIDC not configured")
		return
	}
	q := r.URL.Query()
	if errParam := q.Get("error"); errParam != "" {
		writeJSONError(w, http.StatusBadRequest, "idp returned error: "+errParam)
		return
	}
	code := q.Get("code")
	state := q.Get("state")
	if code == "" || state == "" {
		writeJSONError(w, http.StatusBadRequest, "missing code or state")
		return
	}
	flowCookie, err := r.Cookie(oidcFlowCookie)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "missing flow cookie")
		return
	}
	flow, err := decodeFlowCookie(s.sessionSecret(), flowCookie.Value)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid flow cookie")
		return
	}
	// Defense in depth: even though the cookie is signed, we still
	// compare state to bind the redirect to the request that opened
	// the flow.
	if flow.State != state {
		writeJSONError(w, http.StatusBadRequest, "state mismatch")
		return
	}

	claims, err := p.ExchangeAndVerify(r.Context(), code, flow.Verifier, flow.Nonce)
	if err != nil {
		s.logger.Warn("oidc exchange failed", "error", err)
		writeJSONError(w, http.StatusUnauthorized, "oidc exchange failed")
		return
	}
	if claims.Email == "" {
		writeJSONError(w, http.StatusUnauthorized, "id_token missing email; configure scope email in IdP")
		return
	}

	providerTag := p.ProviderTag()
	user, err := s.db.LookupUserForLogin(r.Context(), strings.ToLower(claims.Email), providerTag)
	if err != nil {
		s.logger.Error("oidc lookup user", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if user == nil {
		// Auto-provision into the configured default org when set.
		// Refuse otherwise so a customer's IdP doesn't accidentally
		// pump unknown users into the wrong tenant.
		if p.DefaultOrgID() == "" {
			writeJSONError(w, http.StatusForbidden, "no Relic account for this user and no default org configured")
			return
		}
		u, err := s.db.CreateUserWithPassword(r.Context(),
			p.DefaultOrgID(),
			strings.ToLower(claims.Email),
			p.DefaultRole(),
			"", // OIDC users have no password hash
			providerTag,
		)
		if err != nil {
			s.logger.Error("oidc create user", "error", err)
			writeJSONError(w, http.StatusInternalServerError, "internal error")
			return
		}
		user = u
	}

	sessionToken, err := p.IssueToken(r.Context(), auth.Claims{
		UserID: user.ID,
		OrgID:  user.OrgID,
		Email:  user.Email,
	})
	if err != nil {
		otelexp.EmitAuthLogin(r.Context(), user.OrgID, user.ID, "oidc", false)
		writeJSONError(w, http.StatusInternalServerError, "issue token")
		return
	}
	otelexp.EmitAuthLogin(r.Context(), user.OrgID, user.ID, "oidc", true)

	// Clear the flow cookie now that it's consumed.
	http.SetCookie(w, &http.Cookie{
		Name: oidcFlowCookie, Path: "/", MaxAge: -1,
	})
	// Hand the session token to the SPA. We support two delivery
	// modes:
	//   - cookie (default): httpOnly so JS can't read it. Good
	//     defense-in-depth, requires CSRF protection on writes
	//     (WS-2D).
	//   - redirect with #access_token fragment: legacy/compat for
	//     SPAs that prefer to hold the token in memory. Off by
	//     default; enable with RELIC_OIDC_TOKEN_DELIVERY=fragment.
	http.SetCookie(w, &http.Cookie{
		Name:     oidcSessionCookie,
		Value:    sessionToken,
		Path:     "/",
		HttpOnly: true,
		Secure:   !s.cookieInsecureForDev(),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   24 * 60 * 60,
	})

	// Build the SPA return URL.
	target := flow.ReturnTo
	if target == "" {
		target = "/"
	}
	// Tack the access_token onto the fragment when the operator
	// opts in to fragment-delivery. The SPA picks it up and stores
	// it locally.
	if s.oidcTokenDeliveryFragment() {
		if strings.Contains(target, "#") {
			target += "&access_token=" + sessionToken
		} else {
			target += "#access_token=" + sessionToken
		}
	}
	http.Redirect(w, r, target, http.StatusFound)
}

// handleOIDCLogout clears the session cookie. The session-revocation
// table (WS-1E, migration 016) will be checked on subsequent requests
// for hard revoke; for now this is best-effort client-side clearing.
func (s *Server) handleOIDCLogout(w http.ResponseWriter, _ *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: oidcSessionCookie, Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: !s.cookieInsecureForDev(),
		SameSite: http.SameSiteLaxMode,
	})
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// --- helpers ---

// oidcFlowState is the pre-callback state we round-trip through the
// signed flow cookie. Kept minimal — anything bigger and we'd hit the
// 4KB cookie limit on multi-tab logins.
type oidcFlowState struct {
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	ReturnTo string `json:"r,omitempty"`
}

// encodeFlowCookie HMACs the JSON payload with the session secret.
// Format: base64(json) + "." + base64(hmac). Single dot separator
// keeps it cheap to parse and avoids JSON-in-cookie escape issues.
func encodeFlowCookie(secret []byte, st oidcFlowState) (string, error) {
	if len(secret) == 0 {
		return "", fmt.Errorf("session secret unset")
	}
	body, err := json.Marshal(st)
	if err != nil {
		return "", err
	}
	enc := b64URLEncode(body)
	mac := hmacSHA256(secret, []byte(enc))
	return enc + "." + b64URLEncode(mac), nil
}

func decodeFlowCookie(secret []byte, raw string) (oidcFlowState, error) {
	parts := strings.SplitN(raw, ".", 2)
	if len(parts) != 2 {
		return oidcFlowState{}, fmt.Errorf("malformed flow cookie")
	}
	body, err := b64URLDecode(parts[0])
	if err != nil {
		return oidcFlowState{}, err
	}
	mac, err := b64URLDecode(parts[1])
	if err != nil {
		return oidcFlowState{}, err
	}
	want := hmacSHA256(secret, []byte(parts[0]))
	if !constantTimeEqual(mac, want) {
		return oidcFlowState{}, fmt.Errorf("bad cookie hmac")
	}
	var st oidcFlowState
	if err := json.Unmarshal(body, &st); err != nil {
		return oidcFlowState{}, err
	}
	return st, nil
}

// isSafeReturnTo allows only same-origin relative paths. Anything
// starting with // or http(s):// is rejected to prevent open-redirect.
func isSafeReturnTo(p string) bool {
	if p == "" {
		return false
	}
	if !strings.HasPrefix(p, "/") {
		return false
	}
	if strings.HasPrefix(p, "//") {
		return false
	}
	return true
}
