package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/therelicai/therelic-platform/internal/api/middleware"
)

func (s *Server) handleListProposals(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	status := r.URL.Query().Get("status")

	proposals, err := s.db.ListProposals(r.Context(), orgID, status)
	if err != nil {
		s.logger.Error("failed to list proposals", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "database error"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"proposals": proposals, "count": len(proposals)})
}

func (s *Server) handleGetProposal(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	proposalID := chi.URLParam(r, "proposalID")

	proposals, err := s.db.ListProposals(r.Context(), orgID, "")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "database error"})
		return
	}

	for _, p := range proposals {
		if p.ID == proposalID {
			writeJSON(w, http.StatusOK, p)
			return
		}
	}

	writeJSON(w, http.StatusNotFound, map[string]string{"error": "proposal not found"})
}

func (s *Server) handleApproveProposal(w http.ResponseWriter, r *http.Request) {
	s.decideProposal(w, r, "approved", auditProposalAccept)
}

func (s *Server) handleRejectProposal(w http.ResponseWriter, r *http.Request) {
	s.decideProposal(w, r, "rejected", auditProposalReject)
}

func (s *Server) handleDismissProposal(w http.ResponseWriter, r *http.Request) {
	s.decideProposal(w, r, "dismissed", auditProposalDismiss)
}

// decideProposal centralizes the three identical "set status + audit"
// handlers. Returns 404 for unknown proposal ids (so callers can tell
// the difference between a successful no-op and a typo) and writes an
// audit row regardless of the verdict.
func (s *Server) decideProposal(w http.ResponseWriter, r *http.Request, status string, action auditAction) {
	orgID := middleware.OrgIDFromContext(r.Context())
	if orgID == "" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "org_id required"})
		return
	}
	userID := middleware.UserIDFromContext(r.Context())
	proposalID := chi.URLParam(r, "proposalID")

	updated, err := s.db.UpdateProposalStatus(r.Context(), orgID, proposalID, status, userID)
	if err != nil {
		s.logger.Error("update proposal status", "error", err, "proposal_id", proposalID)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "database error"})
		return
	}
	if !updated {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "proposal not found"})
		return
	}

	s.auditLog(r.Context(), action, "proposal", proposalID, map[string]any{
		"status": status,
	})

	if status == "dismissed" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": status})
}
