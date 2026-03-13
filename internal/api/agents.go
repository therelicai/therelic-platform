package api

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/therelicai/therelic-platform/internal/api/middleware"
	"github.com/therelicai/therelic-platform/internal/storage"
)

func (s *Server) handleRegisterAgent(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	body, err := io.ReadAll(io.LimitReader(r.Body, 1*1024*1024))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}

	var req struct {
		Name             string          `json:"name"`
		Version          string          `json:"version"`
		IdentityManifest json.RawMessage `json:"identity_manifest"`
		CapabilitiesHash string          `json:"capabilities_hash"`
		PolicyHash       string          `json:"policy_hash"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}

	if req.Name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
		return
	}

	agent := &storage.Agent{
		OrgID:            orgID,
		Name:             req.Name,
		Version:          req.Version,
		IdentityManifest: req.IdentityManifest,
		CapabilitiesHash: req.CapabilitiesHash,
		PolicyHash:       req.PolicyHash,
	}

	if err := s.db.UpsertAgent(r.Context(), agent); err != nil {
		s.logger.Error("failed to register agent", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "database error"})
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{
		"status": "registered",
		"name":   req.Name,
	})
}

func (s *Server) handleListAgents(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	agents, err := s.db.ListAgents(r.Context(), orgID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "database error"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"agents": agents, "count": len(agents)})
}

func (s *Server) handleGetAgent(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	name := chi.URLParam(r, "name")

	agent, err := s.db.GetAgent(r.Context(), orgID, name)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "database error"})
		return
	}
	if agent == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "agent not found"})
		return
	}

	writeJSON(w, http.StatusOK, agent)
}

func (s *Server) handleGetAgentPolicy(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	name := chi.URLParam(r, "name")

	agent, err := s.db.GetAgent(r.Context(), orgID, name)
	if err != nil || agent == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "agent not found"})
		return
	}

	// Policy distribution: return stored policy for this agent
	// For now, return 501 until policy storage is implemented
	writeJSON(w, http.StatusNotImplemented, map[string]string{
		"error": "policy distribution not yet implemented",
	})
}

func (s *Server) handleUpdateAgentPolicy(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusNotImplemented, map[string]string{
		"error": "policy update not yet implemented",
	})
}
