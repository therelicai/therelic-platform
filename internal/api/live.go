package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/therelicai/therelic-platform/internal/livefeed"
)

// MaxIntentBodyBytes caps a single POST /v1/intents body. The runtime
// emits one sealed NDJSON event per request; redacted intents from
// the slice 14 demo path are typically under 1 KiB. 32 KiB leaves
// headroom for HTTP-protocol events whose request lines might carry
// larger payloads (still well below the NOTIFY 8 KiB ceiling, which
// is the binding constraint downstream).
const MaxIntentBodyBytes = 32 * 1024

// handlePostIntent ingests a single sealed event line from the
// runtime's streamer. The runtime emits these for intent + action
// events; we route them into the livefeed hub for fanout to dashboard
// SSE subscribers, and bump agents.last_seen on every successful
// ingest so the slice 14a Online pill stays current for streaming
// runtimes too.
func (s *Server) handlePostIntent(w http.ResponseWriter, r *http.Request) {
	orgID, ok := s.requireOrg(w, r)
	if !ok {
		return
	}
	if s.live == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "live feed not configured"})
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxIntentBodyBytes))
	if err != nil {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "event exceeds maximum size"})
		return
	}
	// The body is a single NDJSON event line. We drop a trailing
	// newline if the runtime emits one (TraceWriter.flush appends \n
	// per line) so we don't reject otherwise-valid uploads.
	body = bytes.TrimRight(body, "\n")
	if len(body) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "empty event"})
		return
	}

	// Sniff the envelope without doing a full decode — most fields
	// are runtime-internal and we only need t / run / agent / target
	// / auth to build the livefeed.Event.
	var env struct {
		T      string `json:"t"`
		Run    string `json:"run"`
		Agent  string `json:"agent"`
		Target string `json:"target"`
		Auth   string `json:"auth"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if env.T != "intent" && env.T != "action" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("unsupported event type %q (expected intent or action)", env.T)})
		return
	}

	// The agent name on the live channel is the (org, run) -> agent
	// mapping. RunEvent at run-start carries it; subsequent events
	// don't. We resolve via the runs table; absent rows (intent
	// arrives before the trace upload — common during a live run)
	// fall back to an empty Agent on the event, and the dashboard
	// shows "(unknown)" until the run-end batch upload pins it.
	agent := env.Agent
	if agent == "" && env.Run != "" {
		if run, err := s.db.GetRun(r.Context(), orgID, env.Run); err == nil && run != nil {
			agent = run.AgentName
		}
	}

	ev := livefeed.Event{
		OrgID:   orgID,
		Type:    env.T,
		Agent:   agent,
		Run:     env.Run,
		Verdict: env.Auth,
		Payload: json.RawMessage(body),
	}
	if err := s.live.Publish(r.Context(), ev); err != nil {
		s.logger.Warn("livefeed publish failed", "error", err, "org_id", orgID, "type", env.T)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "publish failed"})
		return
	}

	// Slice 14a layer: streaming intents also advance last_seen, so
	// the "Online" pill is accurate without waiting for end-of-run
	// batch upload.
	if agent != "" {
		if err := s.db.UpdateAgentLastSeen(r.Context(), orgID, agent); err != nil {
			s.logger.Warn("update agent last_seen failed", "error", err, "org_id", orgID, "agent_name", agent)
		}
	}

	w.WriteHeader(http.StatusAccepted)
}

// handleOrgLive is the dashboard-facing SSE channel. Authenticated by
// user JWT (the auth middleware validates this for any /v1 route);
// the path orgID must match the JWT's resolved org so a leaked token
// can't peek at another tenant by varying the URL.
//
// Slice 14 query parameters (all optional): agent_name, tool, verdict.
// Slice 15 will extend the selector grammar to label-match expressions
// without breaking these query params.
//
// Distinct from the agent-facing SSE channel planned for slice 15 at
// /v1/agents/:id/policy_updates: different audience (humans vs.
// agents), different auth (JWT vs. API key), different event shape.
func (s *Server) handleOrgLive(w http.ResponseWriter, r *http.Request) {
	orgID, ok := s.requireOrg(w, r)
	if !ok {
		return
	}
	pathOrg := chi.URLParam(r, "orgID")
	if pathOrg != orgID {
		// The auth context is the source of truth; a mismatched path
		// org is either a misconfigured client or a probing attacker.
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "org mismatch"})
		return
	}
	if s.live == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "live feed not configured"})
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming not supported"})
		return
	}

	filter := livefeed.Filter{
		AgentName: strings.TrimSpace(r.URL.Query().Get("agent_name")),
		Tool:      strings.TrimSpace(r.URL.Query().Get("tool")),
		Verdict:   strings.TrimSpace(r.URL.Query().Get("verdict")),
	}
	sub := s.live.Subscribe(orgID, filter)
	defer sub.Close()

	// SSE wire prep. We disable proxy buffering with X-Accel-Buffering
	// so Cloudflare / Fly's edge doesn't hold events back; without it
	// the first event lands only when the body crosses the proxy's
	// buffer, which can be many KiB.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	// Opening retry hint so a flaky network triggers reconnect at a
	// sensible cadence.
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
			// Comment line — SSE spec ignores it but the bytes keep
			// idle TCP connections from being reaped by proxies.
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case ev, ok := <-sub.C:
			if !ok {
				// Hub was closed.
				return
			}
			if err := writeSSEEvent(w, ev); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// writeSSEEvent serializes a livefeed.Event as a single SSE data
// frame. The event ID is the underlying payload's run+seq when we can
// extract it, else a timestamp — used by EventSource clients to
// resume after reconnects in future slices. We don't enforce ordering
// here: clients should sort on (run, seq) before display.
func writeSSEEvent(w io.Writer, ev livefeed.Event) error {
	body, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	if _, err := fmt.Fprintf(w, "event: %s\n", ev.Type); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", body); err != nil {
		return err
	}
	return nil
}

// errLiveNotConfigured is returned when a handler reaches the live
// path without a hub wired in (test contexts, primarily). Exported
// only so the simulate handler's nil-check style stays consistent.
var errLiveNotConfigured = errors.New("live feed not configured")
