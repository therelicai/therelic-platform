package api

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/therelicai/therelic-platform/internal/api/middleware"
	"github.com/therelicai/therelic-platform/internal/livefeed"
	"github.com/therelicai/therelic-platform/internal/metrics"
	"github.com/therelicai/therelic-platform/internal/policyfeed"
	"github.com/therelicai/therelic-platform/internal/simulate"
	"github.com/therelicai/therelic-platform/internal/storage"
)

// allowedOriginsFromEnv returns the CORS allow-list, honoring the
// ALLOWED_ORIGINS env var (comma-separated). Falls back to the production +
// local-dev defaults when the env var is unset or empty.
func allowedOriginsFromEnv() []string {
	defaults := []string{
		"https://app.therelic.dev",
		"http://localhost:5173",
		"http://localhost:5174",
	}
	raw := strings.TrimSpace(os.Getenv("ALLOWED_ORIGINS"))
	if raw == "" {
		return defaults
	}
	var out []string
	for _, o := range strings.Split(raw, ",") {
		if o = strings.TrimSpace(o); o != "" {
			out = append(out, o)
		}
	}
	if len(out) == 0 {
		return defaults
	}
	return out
}

type Server struct {
	db     *storage.Postgres
	s3     *storage.S3
	auth   *middleware.Auth
	logger *slog.Logger

	// traceMasterSecret enables HMAC chain verification of trace
	// uploads. Empty means "skip verification" — the platform still
	// records HasIntegrityChain (presence claim) but never proves it.
	// Populated from RELIC_TRACE_KEY at NewServer time.
	traceMasterSecret []byte

	// requireSealedTraces rejects uploads without an HMAC chain when
	// a master secret is configured. Populated from
	// RELIC_REQUIRE_SEALED_TRACES (truthy values: "1", "true", "yes").
	// Default false so legacy/unsealed clients can still upload during
	// a rollout window.
	requireSealedTraces bool

	// simulate runs candidate-policy diff jobs against historical
	// traces. Nil-safe: handleSimulatePolicy returns 503 when unset,
	// so the API still boots in test contexts that don't wire S3.
	simulate *simulate.Runner

	// live is the slice-14 pub/sub hub powering the dashboard's Live
	// view. Nil-safe: handlePostIntent and handleOrgLive return 503
	// when unset (e.g., test contexts without a Postgres LISTEN
	// connection).
	live *livefeed.Hub

	// policyfeed is the slice-15 pub/sub hub for agent-facing policy
	// update notifications. Nil-safe: handleAgentPolicyUpdates and
	// handleUpsertPolicySet return 503 when unset.
	policyfeed *policyfeed.Hub
}

// WithLiveFeed attaches a livefeed.Hub to the server. Like
// WithSimulator, kept as a setter so existing callers of NewServer
// compile unchanged. Wire from the API entrypoint after the hub's
// Start has succeeded.
func (s *Server) WithLiveFeed(h *livefeed.Hub) *Server {
	s.live = h
	return s
}

// WithPolicyFeed attaches a policyfeed.Hub. Wire from the API
// entrypoint after Start has succeeded.
func (s *Server) WithPolicyFeed(h *policyfeed.Hub) *Server {
	s.policyfeed = h
	return s
}

// WithSimulator attaches a simulate.Runner to the server. Called from
// the API entrypoint after S3 + db are wired so the runner can reach
// both. Kept as a setter (rather than NewServer arg) so existing
// callers of NewServer compile unchanged.
func (s *Server) WithSimulator(r *simulate.Runner) *Server {
	s.simulate = r
	return s
}

// loadTraceMasterSecret decodes RELIC_TRACE_KEY (hex) and returns the
// raw bytes the runtime's IntegrityChain expects. Failure to decode
// is fatal — running without verification when the operator expected
// verification is the kind of silent regression we cannot afford in a
// compliance product. Returning nil with a log line lets the server
// boot in unverified mode when the env var is intentionally unset.
func loadTraceMasterSecret(logger *slog.Logger) []byte {
	raw := strings.TrimSpace(os.Getenv("RELIC_TRACE_KEY"))
	if raw == "" {
		return nil
	}
	key, err := hex.DecodeString(raw)
	if err != nil {
		logger.Error("RELIC_TRACE_KEY is not valid hex — trace verification disabled", "error", err)
		return nil
	}
	if len(key) < 16 {
		logger.Error("RELIC_TRACE_KEY shorter than 16 bytes — trace verification disabled")
		return nil
	}
	logger.Info("trace HMAC verification enabled", "key_bytes", len(key))
	return key
}

func envTruthy(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func NewServer(db *storage.Postgres, s3 *storage.S3, jwtSecret string, logger *slog.Logger) *Server {
	return &Server{
		db:                  db,
		s3:                  s3,
		auth:                middleware.NewAuth(db, jwtSecret),
		logger:              logger,
		traceMasterSecret:   loadTraceMasterSecret(logger),
		requireSealedTraces: envTruthy("RELIC_REQUIRE_SEALED_TRACES"),
	}
}

// requestLogger emits a single structured log line per request and
// records the Prometheus counters/histograms. We capture the chi
// RoutePattern (e.g. "/v1/traces/{runID}") rather than the raw URL
// path so the histogram label set stays bounded — recording one
// metric per run ID would blow up cardinality on a busy deploy.
//
// The request_id pulled from chimw.RequestID lands in the log line
// AND is echoed in the X-Request-ID response header for client-side
// log correlation. Operators looking at a stack trace can grep the
// API logs for the same id.
func (s *Server) requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)
		reqID := chimw.GetReqID(r.Context())
		if reqID != "" {
			ww.Header().Set("X-Request-ID", reqID)
		}
		defer func() {
			route := chi.RouteContext(r.Context()).RoutePattern()
			if route == "" {
				// Unmatched routes (404) fall back to a constant so
				// scanners hitting /admin.php don't generate one
				// metric per probed path.
				route = "unknown"
			}
			duration := time.Since(start)
			metrics.ObserveRequest(r.Method, route, ww.Status(), duration)
			s.logger.Info("request",
				"request_id", reqID,
				"method", r.Method,
				"path", r.URL.Path,
				"route", route,
				"status", ww.Status(),
				"bytes", ww.BytesWritten(),
				"duration_ms", duration.Milliseconds(),
			)
		}()
		next.ServeHTTP(ww, r)
	})
}

// handleReadyz reports whether the API can actually do its job: DB
// reachable and S3 bucket reachable. /health stays a cheap liveness
// probe (process is up); /readyz is the gate the load balancer or
// orchestrator should use to decide if this replica receives
// traffic.
//
// We use a short context timeout so a slow DB doesn't keep the
// readyz handler hanging forever — the orchestrator's own timeout
// would fire instead, but the explicit deadline makes the failure
// mode crisper in logs.
func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	type checkResult struct {
		Status string `json:"status"`
		Error  string `json:"error,omitempty"`
	}
	body := map[string]any{
		"status": "ok",
		"checks": map[string]checkResult{},
	}
	checks := body["checks"].(map[string]checkResult)
	allOK := true

	if err := s.db.Ping(ctx); err != nil {
		checks["db"] = checkResult{Status: "fail", Error: err.Error()}
		allOK = false
	} else {
		checks["db"] = checkResult{Status: "ok"}
	}
	if err := s.s3.Ping(ctx); err != nil {
		checks["s3"] = checkResult{Status: "fail", Error: err.Error()}
		allOK = false
	} else {
		checks["s3"] = checkResult{Status: "ok"}
	}

	status := http.StatusOK
	if !allOK {
		body["status"] = "degraded"
		status = http.StatusServiceUnavailable
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (s *Server) Router() http.Handler {
	r := chi.NewRouter()

	corsOpts := cors.Options{
		AllowedOrigins:   allowedOriginsFromEnv(),
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type"},
		AllowCredentials: true,
		MaxAge:           300,
	}

	// RequestID must run BEFORE requestLogger so the logger can read
	// the id from the context. Recoverer must run after so panic
	// recovery sees an annotated context.
	r.Use(chimw.RequestID)
	r.Use(cors.Handler(corsOpts))
	r.Use(middleware.NewRateLimiter(10, 20).Middleware)
	r.Use(s.requestLogger)
	r.Use(chimw.RealIP)
	r.Use(chimw.Recoverer)
	r.Use(chimw.Compress(5))

	// Cheap liveness probe — the process is up and the router can
	// serve. Distinct from /readyz, which checks downstream deps.
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})
	r.Get("/readyz", s.handleReadyz)
	// /metrics is intentionally outside /v1 so it bypasses the auth
	// middleware. Operators are expected to either firewall the port
	// or front the API with an ingress that strips/adds basic-auth.
	r.Handle("/metrics", metrics.Handler())

	r.Route("/v1", func(r chi.Router) {
		r.Use(s.auth.Middleware)

		// Onboarding (explicit org creation)
		r.Post("/onboard", s.handleOnboard)

		// Traces
		r.Post("/traces", s.handleUploadTrace)
		r.Get("/traces", s.handleListTraces)
		r.Get("/traces/{runID}", s.handleGetTrace)
		r.Get("/traces/{runID}/events", s.handleGetTraceEvents)
		r.Delete("/traces/{runID}", s.handleDeleteTrace)

		// Agents
		r.Post("/agents", s.handleRegisterAgent)
		r.Get("/agents", s.handleListAgents)
		r.Get("/agents/{name}", s.handleGetAgent)
		r.Get("/agents/{name}/baseline", s.handleGetAgentBaseline)
		r.Get("/agents/{name}/policy", s.handleGetAgentPolicy)
		r.Put("/agents/{name}/policy", s.handleUpdateAgentPolicy)

		// Organizations
		r.Post("/orgs", s.handleCreateOrg)
		r.Get("/orgs/{orgID}", s.handleGetOrg)
		r.Post("/orgs/{orgID}/api-keys", s.handleCreateAPIKey)
		r.Delete("/orgs/{orgID}/api-keys/{keyID}", s.handleRevokeAPIKey)

		// User
		r.Get("/user", s.handleGetUser)

		// Audit
		r.Get("/audit-events", s.handleListAuditEvents)

		// Proposals
		r.Get("/proposals", s.handleListProposals)
		r.Get("/proposals/{proposalID}", s.handleGetProposal)
		r.Post("/proposals/{proposalID}/approve", s.handleApproveProposal)
		r.Post("/proposals/{proposalID}/reject", s.handleRejectProposal)
		r.Delete("/proposals/{proposalID}", s.handleDismissProposal)

		// Registry (Trust Network)
		r.Get("/registry", s.handleSearchRegistry)
		r.Post("/registry", s.handlePublishListing)
		r.Put("/registry/{agentID}", s.handleUpdateListing)
		r.Delete("/registry/{agentID}", s.handleDeleteListing)
		r.Get("/registry/{agentID}/trust", s.handleGetTrustScore)

		// Transactions
		r.Get("/transactions", s.handleListTransactions)
		r.Get("/transactions/summary", s.handleTransactionSummary)
		r.Get("/transactions/{txnID}", s.handleGetTransaction)

		// Policy simulator (Slice 13)
		r.Post("/policy/simulate", s.handleSimulatePolicy)
		r.Get("/policy/simulate/{jobID}", s.handleGetSimulateJob)

		// Live feed (Slice 14)
		r.Post("/intents", s.handlePostIntent)
		r.Get("/orgs/{orgID}/live", s.handleOrgLive)

		// Universal policy (Slice 15)
		r.Post("/policy_sets", s.handleUpsertPolicySet)
		r.Put("/policy_sets/{id}", s.handleUpsertPolicySet) // upsert-by-name; id is informational
		r.Get("/policy_sets/{id}", s.handleGetPolicySet)
		r.Post("/policy_sets/resolve", s.handleResolveSelector)
		r.Post("/agents/{name}/labels", s.handleSetAgentLabels)
		r.Post("/agents/{name}/policy_applied", s.handlePolicyApplied)
		r.Get("/agents/{name}/policy_updates", s.handleAgentPolicyUpdates)
	})

	return r
}
