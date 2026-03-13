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

	anthropicKey := os.Getenv("ANTHROPIC_API_KEY")
	pollInterval := 60 * time.Second

	worker := governance.NewWorker(db, anthropicKey, pollInterval, logger)

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
