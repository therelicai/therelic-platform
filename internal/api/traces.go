package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/therelicai/therelic-platform/internal/storage"
	tracepkg "github.com/therelicai/therelic-platform/internal/trace"
)

// MaxTraceBytes caps the gzipped upload size at 100 MiB. Beyond that we
// assume abuse (or that the user should split their run).
const MaxTraceBytes = 100 * 1024 * 1024

// validIdentifier accepts the conservative subset of characters we allow
// in agent names, environments, and run IDs. Anything outside this set
// either trips path-component validation in S3 or makes URLs hostile;
// rejecting at the API boundary is simpler than retro-cleaning.
//
// 64 chars is enough for ULIDs, short hashes, and human-readable names.
var validIdentifier = regexp.MustCompile(`^[A-Za-z0-9_.\-:]{1,64}$`)

func sanitizeIdentifier(s, fallback string) string {
	if validIdentifier.MatchString(s) {
		return s
	}
	return fallback
}

// handleUploadTrace accepts a gzipped NDJSON trace, parses it
// server-side, and indexes the recomputed summary. Any X-Relic-*
// headers are advisory — the parser's view always wins. This is the
// core integrity guarantee: a client cannot lie about what happened
// during a run.
func (s *Server) handleUploadTrace(w http.ResponseWriter, r *http.Request) {
	orgID, ok := s.requireOrg(w, r)
	if !ok {
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxTraceBytes))
	if err != nil {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "trace exceeds maximum size"})
		return
	}

	// Verification is opt-in. When RELIC_TRACE_KEY is unset the parser
	// runs in presence-only mode (HasIntegrityChain is still set when
	// every action event carried an hmac field), matching the
	// pre-Slice-6 behavior. When the key is set, the parser
	// cryptographically verifies the chain against a per-run key
	// derived from the master secret + run id.
	summary, err := tracepkg.ParseAndVerify(
		bytes.NewReader(body),
		s.traceMasterSecret,
		s.requireSealedTraces,
	)
	if err != nil {
		s.logger.Warn("trace parse failed", "org_id", orgID, "error", err)
		switch {
		case errors.Is(err, tracepkg.ErrEmptyTrace):
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "trace contains no events"})
		case errors.Is(err, tracepkg.ErrMissingRunStart):
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "trace missing run-start event"})
		case errors.Is(err, tracepkg.ErrTooLarge):
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "trace event line exceeds maximum"})
		case errors.Is(err, tracepkg.ErrChainBroken):
			// 422 Unprocessable Entity: the upload was syntactically
			// fine but didn't pass the server's integrity policy. The
			// audit trail records this attempt — surface as much as we
			// know. summary is non-nil on this path (parser only
			// returns ErrChainBroken after deriving RunID), but guard
			// anyway so a future refactor can't silently panic.
			runRef := ""
			if summary != nil {
				runRef = summary.RunID
			}
			s.auditLog(r.Context(), auditTraceUpload, "run", runRef, map[string]any{
				"rejected": "chain_broken",
			})
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "trace HMAC chain failed verification"})
		case errors.Is(err, tracepkg.ErrChainExpected):
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "this platform requires sealed traces (RELIC_TRACE_KEY)"})
		default:
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "trace is not valid gzipped NDJSON"})
		}
		return
	}

	// Use trace-derived identity. Headers are only consulted when the
	// trace itself omits the field — never to override it.
	agentName := sanitizeIdentifier(summary.AgentName, "")
	if agentName == "" {
		agentName = sanitizeIdentifier(r.Header.Get("X-Relic-Agent-Name"), "unknown")
	}
	environment := sanitizeIdentifier(summary.Environment, "")
	if environment == "" {
		environment = sanitizeIdentifier(r.Header.Get("X-Relic-Environment"), "default")
	}
	runID := sanitizeIdentifier(summary.RunID, "")
	if runID == "" {
		// Should be impossible — Parse() returns ErrMissingRunStart in
		// this case — but belt-and-suspenders.
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "trace missing run id"})
		return
	}

	storageKey := fmt.Sprintf("%s/%s/%s.trtrace.gz", orgID, agentName, runID)

	// 30-day default retention. Once Slice 7 (retention worker) lands
	// this drives actual deletion; for now it's an advisory expiry.
	expiresAt := time.Now().Add(30 * 24 * time.Hour)

	run := &storage.Run{
		ID:             runID,
		OrgID:          orgID,
		AgentName:      agentName,
		AgentVersion:   summary.AgentVersion,
		PolicyHash:     summary.PolicyHash,
		Environment:    environment,
		StartedAt:      summary.StartedAt,
		DurationMs:     summary.DurationMs,
		ExitCode:       summary.ExitCode,
		ActionsTotal:   summary.ActionsTotal,
		ActionsAllowed: summary.ActionsAllowed,
		ActionsDenied:  summary.ActionsDenied,
		StorageKey:     storageKey,
		ExpiresAt:      expiresAt,
		IntegrityChain: summary.HasIntegrityChain,
		ChainVerified:  summary.ChainVerified,
		Truncated:      summary.Truncated,
	}

	// Insert first so a duplicate run_id fails before we touch S3 — no
	// orphaned objects on idempotent re-uploads.
	if err := s.db.InsertRun(r.Context(), run); err != nil {
		if errors.Is(err, storage.ErrRunAlreadyExists) {
			// Idempotent: return the existing row's identity instead
			// of re-uploading.
			existing, _ := s.db.GetRun(r.Context(), orgID, runID)
			if existing != nil {
				writeJSON(w, http.StatusOK, map[string]any{
					"run_id":     existing.ID,
					"expires_at": existing.ExpiresAt.Format(time.RFC3339),
					"duplicate":  true,
				})
				return
			}
		}
		s.logger.Error("failed to index run", "error", err, "org_id", orgID, "run_id", runID)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "index error"})
		return
	}

	if err := s.s3.UploadBytes(r.Context(), storageKey, body); err != nil {
		// Roll back the DB row so we don't leave a phantom index entry
		// pointing at a missing object.
		if _, delErr := s.db.DeleteRun(r.Context(), orgID, runID); delErr != nil {
			s.logger.Error("failed to roll back run insert", "error", delErr, "run_id", runID)
		}
		s.logger.Error("failed to upload trace to S3", "error", err, "run_id", runID)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "storage error"})
		return
	}

	s.auditLog(r.Context(), auditTraceUpload, "run", runID, map[string]any{
		"agent_name":      agentName,
		"actions_total":   summary.ActionsTotal,
		"actions_denied":  summary.ActionsDenied,
		"integrity_chain": summary.HasIntegrityChain,
		"chain_verified":  summary.ChainVerified,
	})

	writeJSON(w, http.StatusCreated, map[string]any{
		"run_id":          runID,
		"expires_at":      expiresAt.Format(time.RFC3339),
		"actions_total":   summary.ActionsTotal,
		"actions_allowed": summary.ActionsAllowed,
		"actions_denied":  summary.ActionsDenied,
		"integrity_chain": summary.HasIntegrityChain,
		"chain_verified":  summary.ChainVerified,
		"truncated":       summary.Truncated,
	})
}

func (s *Server) handleListTraces(w http.ResponseWriter, r *http.Request) {
	orgID, ok := s.requireOrg(w, r)
	if !ok {
		return
	}
	agentName := r.URL.Query().Get("agent")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
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
	orgID, ok := s.requireOrg(w, r)
	if !ok {
		return
	}
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
	orgID, ok := s.requireOrg(w, r)
	if !ok {
		return
	}
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
	if _, err := io.Copy(w, reader); err != nil {
		// Connection probably closed mid-stream — nothing we can do, but
		// surfacing the error so it's visible in logs.
		s.logger.Warn("trace download copy failed", "error", err, "run_id", runID)
	}
}

func (s *Server) handleDeleteTrace(w http.ResponseWriter, r *http.Request) {
	orgID, ok := s.requireOrg(w, r)
	if !ok {
		return
	}
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

	s.auditLog(r.Context(), auditTraceDelete, "run", runID, nil)

	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// Caller already got headers + status; there's nothing useful to
		// return to them at this point. Log it for ops.
		_ = err
	}
}
