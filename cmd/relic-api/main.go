package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/therelicai/therelic-platform/internal/api"
	"github.com/therelicai/therelic-platform/internal/livefeed"
	"github.com/therelicai/therelic-platform/internal/metrics"
	"github.com/therelicai/therelic-platform/internal/policyfeed"
	"github.com/therelicai/therelic-platform/internal/retention"
	"github.com/therelicai/therelic-platform/internal/simulate"
	"github.com/therelicai/therelic-platform/internal/storage"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

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

	jwtSecret := os.Getenv("SUPABASE_JWT_SECRET")

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

	srv := api.NewServer(db, s3Client, jwtSecret, logger).
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
