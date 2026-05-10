package api

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/therelicai/therelic-platform/internal/storage"
)

func (s *Server) handleRegisterAgent(w http.ResponseWriter, r *http.Request) {
	orgID, ok := s.requireOrg(w, r)
	if !ok {
		return
	}

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

	s.auditLog(r.Context(), auditAgentRegister, "agent", req.Name, map[string]any{
		"version":           req.Version,
		"capabilities_hash": req.CapabilitiesHash,
		"policy_hash":       req.PolicyHash,
	})

	writeJSON(w, http.StatusCreated, map[string]string{
		"status": "registered",
		"name":   req.Name,
	})
}

func (s *Server) handleListAgents(w http.ResponseWriter, r *http.Request) {
	orgID, ok := s.requireOrg(w, r)
	if !ok {
		return
	}

	agents, err := s.db.ListAgents(r.Context(), orgID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "database error"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"agents": agents, "count": len(agents)})
}

func (s *Server) handleGetAgent(w http.ResponseWriter, r *http.Request) {
	orgID, ok := s.requireOrg(w, r)
	if !ok {
		return
	}
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
	orgID, ok := s.requireOrg(w, r)
	if !ok {
		return
	}
	name := chi.URLParam(r, "name")

	agent, err := s.db.GetAgent(r.Context(), orgID, name)
	if err != nil || agent == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "agent not found"})
		return
	}

	policyYAML, err := s.db.GetAgentPolicy(r.Context(), orgID, name)
	if err != nil {
		s.logger.Error("failed to get agent policy", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "database error"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"policy": policyYAML,
	})
}

func (s *Server) handleUpdateAgentPolicy(w http.ResponseWriter, r *http.Request) {
	orgID, ok := s.requireOrg(w, r)
	if !ok {
		return
	}
	name := chi.URLParam(r, "name")

	agent, err := s.db.GetAgent(r.Context(), orgID, name)
	if err != nil || agent == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "agent not found"})
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 256*1024))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}

	var req struct {
		Policy string `json:"policy"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}

	if err := s.db.UpdateAgentPolicy(r.Context(), orgID, name, req.Policy); err != nil {
		s.logger.Error("failed to update agent policy", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "database error"})
		return
	}

	// Recording the size only, not the policy itself — policies often
	// contain customer-sensitive target patterns we don't want in audit
	// logs that may be exported.
	s.auditLog(r.Context(), auditPolicyUpdate, "agent", name, map[string]any{
		"policy_bytes": len(req.Policy),
	})

	writeJSON(w, http.StatusOK, map[string]string{
		"status": "updated",
	})
}

func (s *Server) handleGetAgentBaseline(w http.ResponseWriter, r *http.Request) {
	orgID, ok := s.requireOrg(w, r)
	if !ok {
		return
	}
	name := chi.URLParam(r, "name")

	agent, err := s.db.GetAgent(r.Context(), orgID, name)
	if err != nil || agent == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "agent not found"})
		return
	}

	baseline, err := s.db.GetAgentBaseline(r.Context(), orgID, agent.ID)
	if err != nil {
		s.logger.Error("failed to get agent baseline", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "database error"})
		return
	}
	if baseline == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no baseline found for this agent"})
		return
	}

	writeJSON(w, http.StatusOK, baseline)
}
