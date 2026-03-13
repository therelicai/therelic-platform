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
	var req struct {
		Name string `json:"name"`
		Slug string `json:"slug"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleGetUser(w http.ResponseWriter, r *http.Request) {
	userID := middleware.UserIDFromContext(r.Context())
	orgID := middleware.OrgIDFromContext(r.Context())

	writeJSON(w, http.StatusOK, map[string]string{
		"user_id": userID,
		"org_id":  orgID,
	})
}
