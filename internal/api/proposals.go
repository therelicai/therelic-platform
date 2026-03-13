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
	orgID := middleware.OrgIDFromContext(r.Context())
	userID := middleware.UserIDFromContext(r.Context())
	proposalID := chi.URLParam(r, "proposalID")

	if err := s.db.UpdateProposalStatus(r.Context(), orgID, proposalID, "approved", userID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "database error"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "approved"})
}

func (s *Server) handleRejectProposal(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	userID := middleware.UserIDFromContext(r.Context())
	proposalID := chi.URLParam(r, "proposalID")

	if err := s.db.UpdateProposalStatus(r.Context(), orgID, proposalID, "rejected", userID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "database error"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "rejected"})
}

func (s *Server) handleDismissProposal(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	proposalID := chi.URLParam(r, "proposalID")

	if err := s.db.UpdateProposalStatus(r.Context(), orgID, proposalID, "dismissed", ""); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "database error"})
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
