package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/therelicai/therelic-platform/internal/api"
	"github.com/therelicai/therelic-platform/internal/auth"
	"github.com/therelicai/therelic-platform/internal/livefeed"
	"github.com/therelicai/therelic-platform/internal/metrics"
	"github.com/therelicai/therelic-platform/internal/policyfeed"
	"github.com/therelicai/therelic-platform/internal/retention"
	"github.com/therelicai/therelic-platform/internal/simulate"
	"github.com/therelicai/therelic-platform/internal/storage"
	"github.com/therelicai/therelic-platform/internal/telemetry"
	"github.com/therelicai/therelic-platform/internal/version"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	// Subcommand dispatch. Default (no args) runs the HTTP server.
	// Subcommands let operators run migrations + backups without
	// spinning up the API process.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "migrate":
			migrateCommand(os.Args[2:], logger)
			return
		case "version":
			versionCommand()
			return
		case "backup", "restore":
			backupCommand(os.Args[1], os.Args[2:], logger)
			return
		case "-h", "--help", "help":
			printUsage()
			return
		}
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		slog.Error("DATABASE_URL is required")
		os.Exit(1)
	}

	poolCfg := storage.PoolConfig{
		MaxConns:        int32(intEnv("RELIC_PG_MAX_CONNS", 20)),
		MinConns:        int32(intEnv("RELIC_PG_MIN_CONNS", 2)),
		MaxConnLifetime: durationEnv("RELIC_PG_MAX_LIFETIME", 30*time.Minute),
		MaxConnIdleTime: durationEnv("RELIC_PG_MAX_IDLE_TIME", 5*time.Minute),
	}
	db, err := storage.NewPostgresWith(context.Background(), dbURL, poolCfg)
	if err != nil {
		slog.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer db.Close()

	// Wire pool stats into Prometheus. Without this the
	// relic_api_db_pool_* gauges always read zero.
	metrics.SetDBPoolProvider(func() metrics.DBPoolStats {
		s := db.Stats()
		return metrics.DBPoolStats{
			Acquired: s.Acquired,
			Idle:     s.Idle,
			Max:      s.Max,
		}
	})

	s3Client, err := storage.NewS3(
		os.Getenv("S3_ENDPOINT"),
		os.Getenv("S3_BUCKET"),
		os.Getenv("S3_ACCESS_KEY"),
		os.Getenv("S3_SECRET_KEY"),
		os.Getenv("S3_REGION"),
	)
	if err != nil {
		slog.Error("failed to initialize S3 storage", "error", err)
		os.Exit(1)
	}

	// Pick the auth provider based on RELIC_AUTH_MODE. Refuse to boot
	// on misconfiguration: a silent fallback to a wrong provider would
	// either lock everyone out (verifier doesn't match issuer) or
	// accept anything (empty secret).
	authProvider, err := buildAuthProvider()
	if err != nil {
		slog.Error("auth provider configuration", "error", err)
		os.Exit(1)
	}
	slog.Info("auth provider configured", "mode", authProvider.Mode())

	// First-boot bootstrap (local mode only): if RELIC_ADMIN_EMAIL +
	// RELIC_ADMIN_PASSWORD are set and zero users exist, create the
	// admin's org and user. Lets `docker compose up` succeed end-to-end
	// without an extra step. Skip silently if either env is unset; in
	// that case the operator runs `relic-api create-user` (WS-7) or
	// adopts an externally-managed identity provider.
	if authProvider.Mode() == auth.ModeLocal {
		if err := bootstrapLocalAdmin(context.Background(), db, logger); err != nil {
			slog.Error("admin bootstrap failed", "error", err)
			os.Exit(1)
		}
	}

	simulator := simulate.NewRunner(db, s3Client, logger)

	// Live-feed hub binds a dedicated LISTEN connection from the pool.
	// We Start it before the HTTP server starts accepting so a
	// listener failure surfaces at boot, not at first live-view
	// connection. Failure here is fatal — silently degrading the live
	// view to "nothing ever appears" would be the worst possible UX.
	live := livefeed.New(db.Pool(), logger)
	if err := live.Start(context.Background()); err != nil {
		slog.Error("failed to start live feed", "error", err)
		os.Exit(1)
	}
	defer live.Close()

	// Slice 15: agent-facing policy update hub. Same pattern as the
	// dashboard live feed but on a separate Postgres channel so the
	// two surfaces (dashboard vs agent) can't drift into each other.
	policyHub := policyfeed.New(db.Pool(), logger)
	if err := policyHub.Start(context.Background()); err != nil {
		slog.Error("failed to start policy feed", "error", err)
		os.Exit(1)
	}
	defer policyHub.Close()

	srv := api.NewServer(db, s3Client, authProvider, logger).
		WithSimulator(simulator).
		WithLiveFeed(live).
		WithPolicyFeed(policyHub)

	httpServer := &http.Server{
		Addr:         ":" + port,
		Handler:      srv.Router(),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()

	// Retention worker tied to rootCtx — cancelling the context on
	// SIGINT/SIGTERM stops the worker cleanly alongside the HTTP
	// server. Disabled when RELIC_RETENTION_DISABLED is set so a
	// short-lived migration runner or CI job doesn't accidentally
	// reap trace data.
	// Anonymous opt-in telemetry. Off by default; one ping at boot
	// plus one per 24h when RELIC_TELEMETRY=true. No tenant data, no
	// PII, bucketed counts only. See internal/telemetry for the
	// exact payload.
	telReporter, telOn := telemetry.New(db.Pool(), logger, version.Build, version.Commit, string(authProvider.Mode()))
	if telOn {
		slog.Info("anonymous telemetry enabled (RELIC_TELEMETRY=true). See internal/telemetry for what gets reported.")
		go telReporter.Run(rootCtx)
	}

	if !envTruthy("RELIC_RETENTION_DISABLED") {
		retentionCfg := retention.Config{
			Interval:  durationEnv("RELIC_RETENTION_INTERVAL", retention.DefaultInterval),
			BatchSize: intEnv("RELIC_RETENTION_BATCH", retention.DefaultBatchSize),
		}
		worker := retention.New(db, s3Client, logger, retentionCfg)
		metrics.SetRetentionProvider(func() metrics.RetentionStats {
			s := worker.Stats()
			return metrics.RetentionStats{
				SweepsCompleted: s.SweepsCompleted,
				RowsDeleted:     s.RowsDeleted,
				RowsDBFailures:  s.RowsDBFailures,
				RowsS3Failures:  s.RowsS3Failures,
				LastRunAt:       s.LastRunAt,
			}
		})
		go worker.Run(rootCtx)
	} else {
		slog.Info("retention worker disabled via RELIC_RETENTION_DISABLED")
	}

	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGTERM)

	go func() {
		slog.Info("starting relic-api", "port", port)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	<-done
	slog.Info("shutting down")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	rootCancel()
	if err := httpServer.Shutdown(ctx); err != nil {
		slog.Error("shutdown error", "error", err)
	}
}

// buildAuthProvider resolves RELIC_AUTH_MODE to a configured provider.
// Returns an error rather than silently defaulting; misconfiguration
// must be loud.
func buildAuthProvider() (auth.Provider, error) {
	mode, err := auth.ParseMode(strings.TrimSpace(os.Getenv("RELIC_AUTH_MODE")))
	if err != nil {
		return nil, err
	}
	switch mode {
	case auth.ModeLocal:
		return auth.NewLocalProvider(auth.LocalConfig{
			JWTSecret: os.Getenv("RELIC_JWT_SECRET"),
			TokenTTL:  durationEnv("RELIC_JWT_TTL", 0),
			Issuer:    os.Getenv("RELIC_JWT_ISSUER"),
			Audience:  os.Getenv("RELIC_JWT_AUDIENCE"),
		})
	case auth.ModeSupabase:
		return auth.NewSupabaseProvider(os.Getenv("SUPABASE_JWT_SECRET"))
	case auth.ModeOIDC:
		return nil, fmt.Errorf("RELIC_AUTH_MODE=oidc is stubbed; lands in ROADMAP Phase 1 (SSO/SAML/SCIM)")
	default:
		return nil, fmt.Errorf("unhandled auth mode %q", mode)
	}
}

// bootstrapLocalAdmin runs on every boot in local-auth mode. When the
// users table is empty and RELIC_ADMIN_EMAIL + RELIC_ADMIN_PASSWORD
// are set, it creates the admin's org + user so the operator can log
// in immediately. Silent no-op when env vars are unset (operator can
// run `relic-api create-user` instead) or when a user already exists.
//
// This is the difference between "docker compose up just works" and
// "docker compose up + read 4 docs to provision a user." We want the
// former for the $0-cost self-host story.
func bootstrapLocalAdmin(ctx context.Context, db *storage.Postgres, logger *slog.Logger) error {
	email := strings.ToLower(strings.TrimSpace(os.Getenv("RELIC_ADMIN_EMAIL")))
	password := os.Getenv("RELIC_ADMIN_PASSWORD")
	if email == "" || password == "" {
		logger.Info("admin bootstrap skipped: RELIC_ADMIN_EMAIL / RELIC_ADMIN_PASSWORD not set")
		return nil
	}

	// Only check for local-auth users. Pre-existing Supabase or OIDC
	// users (or the dev seed) don't block local-auth bootstrap, so
	// the same install can be reconfigured from supabase mode to
	// local mode without manual surgery.
	count, err := db.CountUsersByProvider(ctx, string(auth.ModeLocal))
	if err != nil {
		return fmt.Errorf("count local users: %w", err)
	}
	if count > 0 {
		logger.Info("admin bootstrap skipped: local-auth user already exists", "count", count)
		return nil
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		return fmt.Errorf("hash admin password: %w", err)
	}

	orgName := strings.SplitN(email, "@", 2)[0] + "'s Organization"
	orgSlug := "primary"
	org, err := db.CreateOrg(ctx, orgName, orgSlug)
	if err != nil {
		return fmt.Errorf("create admin org: %w", err)
	}
	user, err := db.CreateUserWithPassword(ctx, org.ID, email, "admin", hash, string(auth.ModeLocal))
	if err != nil {
		return fmt.Errorf("create admin user: %w", err)
	}
	logger.Info("bootstrapped local admin", "org_id", org.ID, "user_id", user.ID, "email", user.Email)
	return nil
}

func envTruthy(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// durationEnv reads a duration like "1h", "30m" from the environment.
// Falls back to def on parse failure; the operator gets a one-line
// stderr warning but the worker still starts.
func durationEnv(name string, def time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return def
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		slog.Warn("invalid duration env, using default", "name", name, "value", raw, "default", def)
		return def
	}
	return d
}

// intEnv reads a positive integer from the environment.
func intEnv(name string, def int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		slog.Warn("invalid int env, using default", "name", name, "value", raw, "default", def)
		return def
	}
	return n
}
