package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/therelicai/therelic-platform/internal/governance"
	"github.com/therelicai/therelic-platform/internal/storage"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

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
		slog.Warn("S3 not configured, denial detection will use unknown tool names", "error", err)
		s3Client = nil
	}

	anthropicKey := os.Getenv("ANTHROPIC_API_KEY")
	pollInterval := 60 * time.Second

	worker := governance.NewWorker(db, s3Client, anthropicKey, pollInterval, logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGTERM)

	go func() {
		slog.Info("starting governance worker", "poll_interval", pollInterval)
		worker.Run(ctx)
	}()

	<-done
	slog.Info("shutting down governance worker")
	cancel()
}
