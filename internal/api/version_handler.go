package api

import (
	"encoding/json"
	"net/http"

	"github.com/therelicai/therelic-platform/internal/version"
)

// handleVersion backs GET /v1/version. Public, unauthenticated. The
// app and the OSS CLI call it at startup to detect version skew
// against the platform they're talking to. Schema version is the
// highest applied core migration filename (e.g. "012_policy_sets_labels.sql"),
// useful to gate features that depend on a specific migration.
//
// Auth mode is included so an operator can confirm at a glance which
// adapter the platform is configured with without reading server logs.
func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	schema, _ := version.SchemaVersion(r.Context(), s.db.Pool())
	authMode := ""
	if s.authProvider != nil {
		authMode = string(s.authProvider.Mode())
	}
	body := map[string]string{
		"build":          version.Build,
		"commit":         version.Commit,
		"schema_version": schema,
		"auth_mode":      authMode,
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(body)
}
