package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"os"
	"strings"
)

// b64URLEncode is base64 URL-encoding without padding. Used for
// signed-cookie payloads.
func b64URLEncode(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}

func b64URLDecode(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(s)
}

// hmacSHA256 returns HMAC-SHA256(key, msg).
func hmacSHA256(key, msg []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(msg)
	return m.Sum(nil)
}

func constantTimeEqual(a, b []byte) bool {
	return subtle.ConstantTimeCompare(a, b) == 1
}

// sessionSecret returns the secret used to sign session JWTs and the
// OIDC flow cookie. Both share the same key so misconfiguration is
// loud at boot rather than per-request.
func (s *Server) sessionSecret() []byte {
	raw := strings.TrimSpace(os.Getenv("RELIC_JWT_SECRET"))
	if raw == "" {
		return nil
	}
	return []byte(raw)
}

// cookieInsecureForDev returns true when the operator opts into the
// insecure-cookies-for-localhost-dev escape hatch. Production deploys
// must never set this; it allows the Secure flag to be dropped so
// http://localhost works.
func (s *Server) cookieInsecureForDev() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("RELIC_COOKIES_INSECURE_DEV")))
	return v == "1" || v == "true" || v == "yes"
}

// oidcTokenDeliveryFragment returns true when the operator wants
// session tokens delivered via URL fragment to the SPA instead of
// httpOnly cookie. Default false (cookie).
func (s *Server) oidcTokenDeliveryFragment() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("RELIC_OIDC_TOKEN_DELIVERY")))
	return v == "fragment"
}
