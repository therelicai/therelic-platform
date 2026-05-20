package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/therelicai/therelic-platform/internal/auth"
	otelexp "github.com/therelicai/therelic-platform/internal/integrations/otel"
)

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type loginResponse struct {
	Token string  `json:"token"`
	User  userDTO `json:"user"`
}

type userDTO struct {
	ID    string `json:"id"`
	OrgID string `json:"org_id"`
	Email string `json:"email"`
	Role  string `json:"role"`
}

// handleAuthLogin backs POST /v1/auth/login. Only mounted when the
// configured auth.Provider is in local mode (Supabase / OIDC issue
// tokens out-of-band via their IdP). Returns an HS256 JWT plus the
// user record on success.
//
// Failure mode is uniform: every wrong-email, wrong-password, and
// disabled-account case returns the same generic 401 with the same
// generic message. Leaking which factor failed is a fingerprinting
// surface that helps brute-force the other.
func (s *Server) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	if r.Body == nil {
		writeJSONError(w, http.StatusBadRequest, "missing request body")
		return
	}
	defer r.Body.Close()

	var req loginRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	if req.Email == "" || req.Password == "" {
		writeJSONError(w, http.StatusBadRequest, "email and password are required")
		return
	}

	// LookupUserForLogin filters to the same auth_provider that owns
	// this deployment, so a Supabase-created user can't be logged in
	// via local password (they don't have password_hash anyway, but
	// the explicit refusal is the contract).
	u, err := s.db.LookupUserForLogin(r.Context(), req.Email, string(auth.ModeLocal))
	if err != nil {
		s.logger.Error("login lookup failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if u == nil {
		otelexp.EmitAuthLogin(r.Context(), "", "", "local", false)
		writeJSONError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if err := auth.VerifyPassword(u.PasswordHash, req.Password); err != nil {
		otelexp.EmitAuthLogin(r.Context(), u.OrgID, u.ID, "local", false)
		writeJSONError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	token, err := s.authProvider.IssueToken(r.Context(), auth.Claims{
		UserID: u.ID,
		OrgID:  u.OrgID,
		Email:  u.Email,
	})
	if err != nil {
		if errors.Is(err, auth.ErrIssueUnsupported) {
			// Server misconfigured: local-only endpoint reached
			// without a local provider. Shouldn't happen because
			// the route only mounts in local mode, but guard anyway.
			writeJSONError(w, http.StatusServiceUnavailable, "token issuance not configured")
			return
		}
		s.logger.Error("token issuance failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}

	otelexp.EmitAuthLogin(r.Context(), u.OrgID, u.ID, "local", true)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(loginResponse{
		Token: token,
		User: userDTO{
			ID:    u.ID,
			OrgID: u.OrgID,
			Email: u.Email,
			Role:  u.Role,
		},
	})
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
