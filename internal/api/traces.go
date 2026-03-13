package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"
	"github.com/therelicai/therelic-platform/internal/api/middleware"
	"github.com/therelicai/therelic-platform/internal/storage"
)

func (s *Server) handleUploadTrace(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	if orgID == "" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "org_id required"})
		return
	}

	agentName := r.Header.Get("X-Relic-Agent-Name")
	agentVersion := r.Header.Get("X-Relic-Agent-Version")
	policyHash := r.Header.Get("X-Relic-Policy-Hash")
	environment := r.Header.Get("X-Relic-Environment")
	runID := r.Header.Get("X-Relic-Run-ID")

	if runID == "" {
		runID = ulid.Make().String()
	}
	if agentName == "" {
		agentName = "unknown"
	}
	if environment == "" {
		environment = "default"
	}

	actionsTotal, _ := strconv.Atoi(r.Header.Get("X-Relic-Actions-Total"))
	actionsAllowed, _ := strconv.Atoi(r.Header.Get("X-Relic-Actions-Allowed"))
	actionsDenied, _ := strconv.Atoi(r.Header.Get("X-Relic-Actions-Denied"))
	durationMs, _ := strconv.Atoi(r.Header.Get("X-Relic-Duration-Ms"))
	exitCode, _ := strconv.Atoi(r.Header.Get("X-Relic-Exit-Code"))

	storageKey := fmt.Sprintf("%s/%s/%s.trtrace.gz", orgID, agentName, runID)

	body := http.MaxBytesReader(w, r.Body, 100*1024*1024) // 100MB max
	defer body.Close()

	if err := s.s3.Upload(r.Context(), storageKey, body); err != nil {
		s.logger.Error("failed to upload trace to S3", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "storage error"})
		return
	}

	dur := durationMs
	ex := exitCode
	run := &storage.Run{
		ID:             runID,
		OrgID:          orgID,
		AgentName:      agentName,
		AgentVersion:   agentVersion,
		PolicyHash:     policyHash,
		Environment:    environment,
		StartedAt:      time.Now(),
		DurationMs:     &dur,
		ExitCode:       &ex,
		ActionsTotal:   actionsTotal,
		ActionsAllowed: actionsAllowed,
		ActionsDenied:  actionsDenied,
		StorageKey:     storageKey,
		ExpiresAt:      time.Now().AddDate(0, 0, 30),
	}

	if err := s.db.InsertRun(r.Context(), run); err != nil {
		s.logger.Error("failed to index run", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "index error"})
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"run_id":     runID,
		"expires_at": run.ExpiresAt.Format(time.RFC3339),
	})
}

func (s *Server) handleListTraces(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	agentName := r.URL.Query().Get("agent")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if limit <= 0 || limit > 100 {
		limit = 50
	}

	runs, err := s.db.ListRuns(r.Context(), orgID, agentName, limit, offset)
	if err != nil {
		s.logger.Error("failed to list runs", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "database error"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"runs": runs, "count": len(runs)})
}

func (s *Server) handleGetTrace(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	runID := chi.URLParam(r, "runID")

	run, err := s.db.GetRun(r.Context(), orgID, runID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "database error"})
		return
	}
	if run == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "run not found"})
		return
	}

	writeJSON(w, http.StatusOK, run)
}

func (s *Server) handleGetTraceEvents(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	runID := chi.URLParam(r, "runID")

	run, err := s.db.GetRun(r.Context(), orgID, runID)
	if err != nil || run == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "run not found"})
		return
	}

	reader, err := s.s3.Download(r.Context(), run.StorageKey)
	if err != nil {
		s.logger.Error("failed to download trace", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "storage error"})
		return
	}
	defer reader.Close()

	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s.trtrace.gz", runID))
	io.Copy(w, reader)
}

func (s *Server) handleDeleteTrace(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	runID := chi.URLParam(r, "runID")

	storageKey, err := s.db.DeleteRun(r.Context(), orgID, runID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "database error"})
		return
	}
	if storageKey == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "run not found"})
		return
	}

	if err := s.s3.Delete(r.Context(), storageKey); err != nil {
		s.logger.Error("failed to delete trace from S3", "error", err, "key", storageKey)
	}

	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}
