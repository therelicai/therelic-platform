package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

func (s *Server) handleSearchRegistry(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"listings": []any{},
		"count":    0,
		"message":  "capability registry — coming with trust network launch",
	})
}

func (s *Server) handlePublishListing(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusNotImplemented, map[string]string{
		"error": "capability registry not yet available",
	})
}

func (s *Server) handleUpdateListing(w http.ResponseWriter, r *http.Request) {
	_ = chi.URLParam(r, "agentID")
	writeJSON(w, http.StatusNotImplemented, map[string]string{
		"error": "capability registry not yet available",
	})
}

func (s *Server) handleDeleteListing(w http.ResponseWriter, r *http.Request) {
	_ = chi.URLParam(r, "agentID")
	writeJSON(w, http.StatusNotImplemented, map[string]string{
		"error": "capability registry not yet available",
	})
}

func (s *Server) handleGetTrustScore(w http.ResponseWriter, r *http.Request) {
	_ = chi.URLParam(r, "agentID")
	writeJSON(w, http.StatusNotImplemented, map[string]string{
		"error": "trust scoring not yet available",
	})
}
