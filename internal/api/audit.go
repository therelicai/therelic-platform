package api

import (
	"net/http"
)

func (s *Server) handleListAuditEvents(w http.ResponseWriter, r *http.Request) {
	// Audit events listing - returns empty until audit storage is implemented
	writeJSON(w, http.StatusOK, map[string]any{
		"events": []any{},
		"count":  0,
	})
}
