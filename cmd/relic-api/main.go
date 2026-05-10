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
	"github.com/therelicai/therelic-platform/internal/retention"
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

	db, err := storage.NewPostgres(context.Background(), dbURL)
	if err != nil {
		slog.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer db.Close()

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

	srv := api.NewServer(db, s3Client, jwtSecret, logger)

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
