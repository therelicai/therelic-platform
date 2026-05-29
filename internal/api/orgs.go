package api

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/therelicai/therelic-platform/internal/api/middleware"
)

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}[a-z0-9]$`)

func (s *Server) handleCreateOrg(w http.ResponseWriter, r *http.Request) {
	// Reject callers that already have an org context. This endpoint
	// exists for the fresh-signup window — between token issuance and
	// org assignment — where the caller has a valid token but no
	// org_id yet. In every supported auth mode (local, supabase, oidc)
	// users normally arrive with an org already bound; an authed
	// caller hitting this path means they're either (a) attempting to
	// spin up sibling orgs they shouldn't have, or (b) trying to
	// abuse the endpoint to fill the orgs table. Both paths are
	// denied here. Sibling-org access goes through the bilateral
	// agreements flow, not org self-creation.
	if callerOrg := middleware.OrgIDFromContext(r.Context()); callerOrg != "" {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": "caller already has an org; use bilateral agreements for cross-org access",
		})
		return
	}

	var req struct {
		Name string `json:"name"`
		Slug string `json:"slug"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if req.Name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
		return
	}
	if req.Slug == "" {
		req.Slug = strings.ToLower(strings.ReplaceAll(req.Name, " ", "-"))
	}
	if !slugRe.MatchString(req.Slug) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid slug format"})
		return
	}

	org, err := s.db.CreateOrg(r.Context(), req.Name, req.Slug)
	if err != nil {
		s.logger.Error("failed to create org", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not create organization"})
		return
	}

	// Audit against the new org so the row lands in the org's own log
	// — the request context org_id is probably empty for fresh signups.
	userID := middleware.UserIDFromContext(r.Context())
	if userID == "" {
		userID = "api-key"
	}
	meta, _ := json.Marshal(map[string]any{"slug": req.Slug, "name": req.Name})
	if err := s.db.InsertAuditEvent(r.Context(), org.ID, userID, string(auditOrgCreate), "organization", org.ID, meta); err != nil {
		s.logger.Warn("audit org.create failed", "error", err, "org_id", org.ID)
	}

	writeJSON(w, http.StatusCreated, org)
}

func (s *Server) handleGetOrg(w http.ResponseWriter, r *http.Request) {
	orgID := chi.URLParam(r, "orgID")

	authOrgID := middleware.OrgIDFromContext(r.Context())
	if authOrgID != orgID {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "access denied"})
		return
	}

	org, err := s.db.GetOrg(r.Context(), orgID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "database error"})
		return
	}
	if org == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "organization not found"})
		return
	}

	writeJSON(w, http.StatusOK, org)
}

func (s *Server) handleCreateAPIKey(w http.ResponseWriter, r *http.Request) {
	orgID := chi.URLParam(r, "orgID")
	authOrgID := middleware.OrgIDFromContext(r.Context())
	if authOrgID != orgID {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "access denied"})
		return
	}

	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
		return
	}

	key, plaintext, err := s.db.CreateAPIKey(r.Context(), orgID, req.Name)
	if err != nil {
		s.logger.Error("failed to create API key", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not create key"})
		return
	}

	s.auditLog(r.Context(), auditAPIKeyCreate, "api_key", key.ID, map[string]any{
		"name":   key.Name,
		"prefix": key.KeyPrefix,
	})

	writeJSON(w, http.StatusCreated, map[string]any{
		"key":        key,
		"secret_key": plaintext,
		"warning":    "Store this key securely. It will not be shown again.",
	})
}

func (s *Server) handleRevokeAPIKey(w http.ResponseWriter, r *http.Request) {
	orgID := chi.URLParam(r, "orgID")
	keyID := chi.URLParam(r, "keyID")
	authOrgID := middleware.OrgIDFromContext(r.Context())
	if authOrgID != orgID {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "access denied"})
		return
	}

	if err := s.db.RevokeAPIKey(r.Context(), orgID, keyID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not revoke key"})
		return
	}

	s.auditLog(r.Context(), auditAPIKeyRevoke, "api_key", keyID, nil)

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleOnboard(w http.ResponseWriter, r *http.Request) {
	// Explicit org creation for onboarding flow
	s.handleCreateOrg(w, r)
}

func (s *Server) handleGetUser(w http.ResponseWriter, r *http.Request) {
	userID := middleware.UserIDFromContext(r.Context())
	orgID := middleware.OrgIDFromContext(r.Context())

	writeJSON(w, http.StatusOK, map[string]string{
		"user_id": userID,
		"org_id":  orgID,
	})
}
