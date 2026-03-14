package api

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/therelicai/therelic-platform/internal/api/middleware"
	"github.com/therelicai/therelic-platform/internal/storage"
)

type Server struct {
	db     *storage.Postgres
	s3     *storage.S3
	auth   *middleware.Auth
	logger *slog.Logger
}

func NewServer(db *storage.Postgres, s3 *storage.S3, jwtSecret string, logger *slog.Logger) *Server {
	return &Server{
		db:     db,
		s3:     s3,
		auth:   middleware.NewAuth(db, jwtSecret),
		logger: logger,
	}
}

func (s *Server) requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)
		defer func() {
			s.logger.Info("request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", ww.Status(),
				"bytes", ww.BytesWritten(),
				"duration_ms", time.Since(start).Milliseconds(),
			)
		}()
		next.ServeHTTP(ww, r)
	})
}

func (s *Server) Router() http.Handler {
	r := chi.NewRouter()

	corsOpts := cors.Options{
		AllowedOrigins:   []string{"https://app.therelic.dev", "http://localhost:5173", "http://localhost:5174"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type"},
		AllowCredentials: true,
		MaxAge:           300,
	}

	r.Use(cors.Handler(corsOpts))
	r.Use(middleware.NewRateLimiter(10, 20).Middleware)
	r.Use(s.requestLogger)
	r.Use(chimw.RequestID)
	r.Use(chimw.RealIP)
	r.Use(chimw.Recoverer)
	r.Use(chimw.Compress(5))

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})

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
	})

	return r
}
