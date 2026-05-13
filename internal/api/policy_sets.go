package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/therelicai/therelic-platform/internal/policy"
	"github.com/therelicai/therelic-platform/internal/policyfeed"
	"github.com/therelicai/therelic-platform/internal/storage"
)

// MaxPolicySetBodyBytes caps the policy_set upload size. Same budget
// as the simulator's 64 KiB — a realistic policy fits well inside it.
const MaxPolicySetBodyBytes = 64 * 1024

// policySetRequest is the wire shape for POST/PUT /v1/policy_sets.
type policySetRequest struct {
	Name       string          `json:"name"`
	Selector   json.RawMessage `json:"selector"`
	PolicyYAML string          `json:"policy_yaml"`
}

// computePolicyHash returns the first 8 bytes of SHA-256(policy_yaml)
// as hex. Matches the runtime's policy.PolicyHash so a YAML that came
// from the platform hashes identically end-to-end.
func computePolicyHash(yamlBytes []byte) string {
	h := sha256.Sum256(yamlBytes)
	return hex.EncodeToString(h[:8])
}

// handleUpsertPolicySet creates or replaces a policy_set, resolves
// its selector to the matching agent set, persists the row, and fans
// out a notification on the agent-facing SSE channel. The runtime's
// SSE subscribers receive the notification, pull the YAML, hot-reload,
// and POST /v1/agents/:name/policy_applied — closing the loop the
// dashboard renders.
func (s *Server) handleUpsertPolicySet(w http.ResponseWriter, r *http.Request) {
	orgID, ok := s.requireOrg(w, r)
	if !ok {
		return
	}
	if s.policyfeed == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "policy feed not configured"})
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxPolicySetBodyBytes))
	if err != nil {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "request exceeds maximum size"})
		return
	}
	var req policySetRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if req.Name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
		return
	}
	if len(req.Selector) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "selector is required"})
		return
	}
	if req.PolicyYAML == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "policy_yaml is required"})
		return
	}

	// Validate the YAML synchronously so a typo comes back as 400 from
	// this call, not as a stale "policy update available" frame the
	// runtime later rejects.
	parsed, perr := policy.Parse([]byte(req.PolicyYAML))
	if perr != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("invalid policy YAML: %v", perr)})
		return
	}
	if errs := policy.Validate(parsed, false); len(errs) > 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": errs[0].Error()})
		return
	}

	// Resolve the selector to an agent set BEFORE writing the row so
	// a malformed selector surfaces as a 400 rather than as silent
	// fanout to zero agents.
	matched, rerr := s.db.ResolveSelector(r.Context(), orgID, req.Selector)
	if rerr != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": rerr.Error()})
		return
	}

	hash := computePolicyHash([]byte(req.PolicyYAML))
	set := &storage.PolicySet{
		OrgID:      orgID,
		Name:       req.Name,
		Selector:   req.Selector,
		PolicyYAML: req.PolicyYAML,
		PolicyHash: hash,
	}
	if err := s.db.UpsertPolicySet(r.Context(), set); err != nil {
		s.logger.Error("upsert policy_set failed", "error", err, "org_id", orgID, "name", req.Name)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "persist failed"})
		return
	}

	// Fan out notifications. Failures here are logged, not fatal — the
	// row is already persisted; missing notifications surface as
	// "stale" in the applied-state counter and the operator can
	// re-save to retry. This preserves the dashboard's "n/m on hash"
	// affordance as the recovery path.
	now := time.Now().UTC()
	publishErrs := 0
	for _, a := range matched {
		note := policyfeed.Notification{
			OrgID:         orgID,
			AgentName:     a.Name,
			PolicyHash:    hash,
			Version:       set.Version,
			PolicySetName: set.Name,
			PublishedAt:   now,
		}
		if err := s.policyfeed.Publish(r.Context(), note); err != nil {
			s.logger.Warn("policyfeed publish failed", "error", err, "agent", a.Name)
			publishErrs++
		}
	}

	// Slice-15 audit lineage: every set-write is recorded so an
	// operator can see who shipped what to which agent set.
	s.auditLog(r.Context(), auditPolicySetWrite, "policy_set", set.ID, map[string]any{
		"name":             set.Name,
		"policy_hash":      hash,
		"version":          set.Version,
		"matched_agents":   len(matched),
		"publish_failures": publishErrs,
	})

	out := map[string]any{
		"id":              set.ID,
		"name":            set.Name,
		"policy_hash":     set.PolicyHash,
		"version":         set.Version,
		"matched_agents":  matched,
		"created_at":      set.CreatedAt,
		"updated_at":      set.UpdatedAt,
	}
	writeJSON(w, http.StatusOK, out)
}

// handleGetPolicySet returns the set + the currently-resolved agent
// set with each agent's applied state inline. The dashboard's editor
// renders "47/52 agents on hash abc123, 5 stale" off this payload.
func (s *Server) handleGetPolicySet(w http.ResponseWriter, r *http.Request) {
	orgID, ok := s.requireOrg(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	set, err := s.db.GetPolicySetByID(r.Context(), orgID, id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lookup failed"})
		return
	}
	if set == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "policy_set not found"})
		return
	}

	matched, err := s.db.ResolveSelector(r.Context(), orgID, set.Selector)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "selector resolution failed"})
		return
	}

	type appliedView struct {
		Name              string  `json:"name"`
		AppliedPolicyHash *string `json:"applied_policy_hash,omitempty"`
		AppliedAt         *string `json:"applied_at,omitempty"`
		Stale             bool    `json:"stale"`
	}
	views := make([]appliedView, 0, len(matched))
	onHash, stale := 0, 0
	for _, a := range matched {
		v := appliedView{Name: a.Name, AppliedPolicyHash: a.AppliedPolicyHash}
		if a.AppliedAt != nil {
			ts := a.AppliedAt.UTC().Format(time.RFC3339Nano)
			v.AppliedAt = &ts
		}
		if a.AppliedPolicyHash != nil && *a.AppliedPolicyHash == set.PolicyHash {
			onHash++
		} else {
			v.Stale = true
			stale++
		}
		views = append(views, v)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"id":             set.ID,
		"name":           set.Name,
		"selector":       set.Selector,
		"policy_yaml":    set.PolicyYAML,
		"policy_hash":    set.PolicyHash,
		"version":        set.Version,
		"created_at":     set.CreatedAt,
		"updated_at":     set.UpdatedAt,
		"matched_agents": views,
		"applied_state": map[string]int{
			"on_hash": onHash,
			"stale":   stale,
			"total":   len(matched),
		},
	})
}

// handleResolveSelector is a read-only helper the dashboard uses on
// selector change to preview the matched agent set + per-agent applied
// state without creating a policy_set. Body shape mirrors the create
// path; we ignore policy_yaml here (the editor hasn't decided yet).
//
// This is "free" in the sense that it doesn't write or notify — it's
// the read side of UpsertPolicySet's resolve+resolve-state logic
// factored out so the UI can render before the operator clicks Save.
func (s *Server) handleResolveSelector(w http.ResponseWriter, r *http.Request) {
	orgID, ok := s.requireOrg(w, r)
	if !ok {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8*1024))
	if err != nil {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "request too large"})
		return
	}
	var req struct {
		Selector json.RawMessage `json:"selector"`
		// Optional: when the caller is editing an existing set, pass
		// the policy_hash so we can report applied-state vs that
		// candidate. Empty → no applied-state in the response.
		PolicyHash string `json:"policy_hash,omitempty"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if len(req.Selector) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "selector is required"})
		return
	}
	matched, err := s.db.ResolveSelector(r.Context(), orgID, req.Selector)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	onHash, stale := 0, 0
	type viewEntry struct {
		Name              string  `json:"name"`
		AppliedPolicyHash *string `json:"applied_policy_hash,omitempty"`
		Stale             bool    `json:"stale,omitempty"`
	}
	views := make([]viewEntry, 0, len(matched))
	for _, a := range matched {
		entry := viewEntry{Name: a.Name, AppliedPolicyHash: a.AppliedPolicyHash}
		if req.PolicyHash != "" {
			if a.AppliedPolicyHash != nil && *a.AppliedPolicyHash == req.PolicyHash {
				onHash++
			} else {
				entry.Stale = true
				stale++
			}
		}
		views = append(views, entry)
	}

	resp := map[string]any{
		"matched_agents": views,
		"total":          len(matched),
	}
	if req.PolicyHash != "" {
		resp["applied_state"] = map[string]int{
			"on_hash": onHash,
			"stale":   stale,
			"total":   len(matched),
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleSetAgentLabels overwrites an agent's label set. Body:
// { "labels": { "env": "prod", "tier": "primary" } }.
func (s *Server) handleSetAgentLabels(w http.ResponseWriter, r *http.Request) {
	orgID, ok := s.requireOrg(w, r)
	if !ok {
		return
	}
	agentName := chi.URLParam(r, "name")
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8*1024))
	if err != nil {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "request too large"})
		return
	}
	var req struct {
		Labels map[string]string `json:"labels"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if err := s.db.SetAgentLabels(r.Context(), orgID, agentName, req.Labels); err != nil {
		// "agent not found" is a 404; everything else is a 500.
		if errors.Is(err, errAgentNotFound) || err.Error() == fmt.Sprintf("set agent labels: agent %q not found", agentName) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "agent not found"})
			return
		}
		s.logger.Error("set agent labels failed", "error", err, "agent", agentName)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "persist failed"})
		return
	}
	s.auditLog(r.Context(), auditAgentLabels, "agent", agentName, map[string]any{
		"labels": req.Labels,
	})
	writeJSON(w, http.StatusOK, map[string]any{"labels": req.Labels})
}

// handlePolicyApplied advances the agent's applied-state. The
// runtime calls this after a successful hot-reload so the dashboard's
// "47/52 on hash abc123" counter advances.
func (s *Server) handlePolicyApplied(w http.ResponseWriter, r *http.Request) {
	orgID, ok := s.requireOrg(w, r)
	if !ok {
		return
	}
	agentName := chi.URLParam(r, "name")
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1024))
	if err != nil {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "request too large"})
		return
	}
	var req struct {
		Hash string `json:"hash"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if req.Hash == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "hash is required"})
		return
	}
	if err := s.db.MarkPolicyApplied(r.Context(), orgID, agentName, req.Hash); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleAgentPolicyUpdates is the agent-facing SSE channel — distinct
// from /v1/orgs/:id/live in audience, auth, and event shape. The path
// `name` identifies the agent; the auth context provides the org. The
// channel never sees cross-agent or cross-tenant notifications.
//
// Frame format: one event per notification, `event: policy_update`,
// `data: {<Notification JSON>}`. The runtime parses the data, pulls
// policy via the existing /v1/agents/:name/policy endpoint, swaps in
// place, and POSTs /v1/agents/:name/policy_applied.
func (s *Server) handleAgentPolicyUpdates(w http.ResponseWriter, r *http.Request) {
	orgID, ok := s.requireOrg(w, r)
	if !ok {
		return
	}
	agentName := chi.URLParam(r, "name")
	if s.policyfeed == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "policy feed not configured"})
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming not supported"})
		return
	}

	sub := s.policyfeed.Subscribe(orgID, agentName)
	defer sub.Close()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "retry: 2000\n\n")
	flusher.Flush()

	keepalive := time.NewTicker(25 * time.Second)
	defer keepalive.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-keepalive.C:
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case note, ok := <-sub.C:
			if !ok {
				return
			}
			payload, err := json.Marshal(note)
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "event: policy_update\ndata: %s\n\n", payload); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// errAgentNotFound is the sentinel SetAgentLabels uses to flag a
// missing agent; the storage layer wraps it with %q-quoted name so a
// caller can distinguish "not found" from other errors. Exported only
// so this file's switch doesn't need to know the exact storage-layer
// wording.
var errAgentNotFound = errors.New("agent not found")
