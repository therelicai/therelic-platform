package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/therelicai/therelic-platform/internal/simulate"
)

// MaxSimulateBodyBytes caps the request body for POST /v1/policy/simulate.
// A YAML policy is usually under 10 KB; 64 KB is plenty of headroom
// and a small enough cap that runaway clients can't flood the API
// with multi-megabyte fake policies.
const MaxSimulateBodyBytes = 64 * 1024

// agentSelector is the wire shape for the slice 13 selector. The
// `agent_name` field is the only valid form today; slice 15 extends
// this with label-match keys (e.g. `match: { env: "prod" }`). When
// that lands we'll add the new field as an additional union arm and
// the existing { agent_name } shape remains valid.
type agentSelector struct {
	AgentName string `json:"agent_name"`
}

// simulateRequest is the deserialized POST /v1/policy/simulate body.
type simulateRequest struct {
	PolicyYAML    string        `json:"policy_yaml"`
	AgentSelector agentSelector `json:"agent_selector"`
	WindowDays    int           `json:"window_days"`
}

// handleSimulatePolicy queues an async simulation. We do parse +
// validate synchronously so YAML problems come back as a 400 from
// this call rather than as a status:error on the follow-up GET — the
// editor experience leans on that.
func (s *Server) handleSimulatePolicy(w http.ResponseWriter, r *http.Request) {
	orgID, ok := s.requireOrg(w, r)
	if !ok {
		return
	}
	if s.simulate == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "simulator not configured"})
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxSimulateBodyBytes))
	if err != nil {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "request exceeds maximum size"})
		return
	}

	var req simulateRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}

	if req.AgentSelector.AgentName == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "agent_selector.agent_name is required"})
		return
	}

	jobID, err := s.simulate.SubmitJob(r.Context(), simulate.Request{
		OrgID:      orgID,
		AgentName:  req.AgentSelector.AgentName,
		PolicyYAML: req.PolicyYAML,
		WindowDays: req.WindowDays,
	})
	if err != nil {
		if errors.Is(err, simulate.ErrInvalidRequest) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		s.logger.Error("simulate submit failed", "error", err, "org_id", orgID)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "simulate submit failed"})
		return
	}

	s.auditLog(r.Context(), auditPolicySimulate, "agent", req.AgentSelector.AgentName, map[string]any{
		"job_id":      jobID,
		"window_days": req.WindowDays,
	})

	writeJSON(w, http.StatusAccepted, map[string]string{"job_id": jobID})
}

// handleGetSimulateJob returns the current status (and result, if
// done) for a previously-submitted simulation. Org scoping is enforced
// by the runner — a job submitted by org A cannot be polled by org B.
func (s *Server) handleGetSimulateJob(w http.ResponseWriter, r *http.Request) {
	orgID, ok := s.requireOrg(w, r)
	if !ok {
		return
	}
	if s.simulate == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "simulator not configured"})
		return
	}
	jobID := chi.URLParam(r, "jobID")

	job, err := s.simulate.GetJob(orgID, jobID)
	if err != nil {
		if errors.Is(err, simulate.ErrJobNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "job not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	out := map[string]any{
		"job_id":       job.ID,
		"status":       job.Status,
		"submitted_at": job.SubmittedAt,
	}
	if !job.FinishedAt.IsZero() {
		out["finished_at"] = job.FinishedAt
	}
	if job.Error != "" {
		out["error"] = job.Error
	}
	if job.Result != nil {
		out["result"] = job.Result
	}
	writeJSON(w, http.StatusOK, out)
}
