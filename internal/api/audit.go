package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/therelicai/therelic-platform/internal/api/middleware"
)

// requireOrg extracts the request's org_id and writes a 403 if it's
// empty. Returns the org_id and a bool indicating whether the caller
// should proceed. Centralizing this prevents the empty-orgID -> blank
// SQL filter -> cross-tenant data leak class of bug.
func (s *Server) requireOrg(w http.ResponseWriter, r *http.Request) (string, bool) {
	orgID := middleware.OrgIDFromContext(r.Context())
	if orgID == "" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "org_id required"})
		return "", false
	}
	return orgID, true
}

// auditAction is a typed constant for the `action` column of audit_events.
// Centralizing the strings makes them easier to search and discourages
// typo-driven drift across handlers.
type auditAction string

const (
	auditTraceUpload    auditAction = "trace.upload"
	auditTraceDelete    auditAction = "trace.delete"
	auditAgentRegister  auditAction = "agent.register"
	auditPolicyUpdate   auditAction = "agent.policy_update"
	auditProposalAccept auditAction = "proposal.approve"
	auditProposalReject auditAction = "proposal.reject"
	auditProposalDismiss auditAction = "proposal.dismiss"
	auditAPIKeyCreate   auditAction = "apikey.create"
	auditAPIKeyRevoke   auditAction = "apikey.revoke"
	auditOrgCreate      auditAction = "org.create"
	auditPolicySimulate auditAction = "policy.simulate"
)

// auditLog records an audit row and never fails the caller. Mutations
// shouldn't fail just because the audit table is unhappy — but we do
// log so ops can see persistent failures.
//
// userID falls back to "api-key" when a request authenticates via API key
// (no JWT subject); some auth paths populate it in the request context.
func (s *Server) auditLog(
	ctx context.Context,
	action auditAction,
	resource string,
	resourceID string,
	meta map[string]any,
) {
	orgID := middleware.OrgIDFromContext(ctx)
	if orgID == "" {
		// No org means no tenant — nothing useful to record.
		return
	}
	userID := middleware.UserIDFromContext(ctx)
	if userID == "" {
		userID = "api-key"
	}

	var metaBytes []byte
	if meta != nil {
		b, err := json.Marshal(meta)
		if err != nil {
			s.logger.Warn("audit metadata marshal failed", "action", string(action), "error", err)
			return
		}
		metaBytes = b
	}

	if err := s.db.InsertAuditEvent(ctx, orgID, userID, string(action), resource, resourceID, metaBytes); err != nil {
		s.logger.Warn("audit write failed",
			"action", string(action),
			"resource", resource,
			"resource_id", resourceID,
			"error", err,
		)
	}
}

func (s *Server) handleListAuditEvents(w http.ResponseWriter, r *http.Request) {
	orgID, ok := s.requireOrg(w, r)
	if !ok {
		return
	}

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	events, err := s.db.ListAuditEvents(r.Context(), orgID, limit, offset)
	if err != nil {
		s.logger.Error("list audit events", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "database error"})
		return
	}

	// Audit events store metadata as bytea. Reshape into a generic
	// map so the JSON we emit doesn't double-encode it as a base64
	// string. Bad metadata falls back to an empty object so the
	// dashboard never crashes on a malformed row.
	type wireEvent struct {
		ID         string         `json:"id"`
		OrgID      string         `json:"org_id"`
		UserID     string         `json:"user_id"`
		Action     string         `json:"action"`
		Resource   string         `json:"resource"`
		ResourceID string         `json:"resource_id"`
		Metadata   map[string]any `json:"metadata"`
		CreatedAt  string         `json:"created_at"`
	}
	wire := make([]wireEvent, 0, len(events))
	for _, e := range events {
		var meta map[string]any
		if len(e.Metadata) > 0 {
			if err := json.Unmarshal(e.Metadata, &meta); err != nil {
				meta = map[string]any{}
			}
		} else {
			meta = map[string]any{}
		}
		wire = append(wire, wireEvent{
			ID:         e.ID,
			OrgID:      e.OrgID,
			UserID:     e.UserID,
			Action:     e.Action,
			Resource:   e.Resource,
			ResourceID: e.ResourceID,
			Metadata:   meta,
			CreatedAt:  e.CreatedAt.Format("2006-01-02T15:04:05.000Z"),
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{"events": wire, "count": len(wire)})
}
