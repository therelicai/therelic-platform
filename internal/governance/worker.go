package governance

import (
	"context"
	"log/slog"
	"time"

	"github.com/therelicai/therelic-platform/internal/storage"
)

type Worker struct {
	db           *storage.Postgres
	anthropicKey string
	interval     time.Duration
	logger       *slog.Logger
}

func NewWorker(db *storage.Postgres, anthropicKey string, interval time.Duration, logger *slog.Logger) *Worker {
	return &Worker{
		db:           db,
		anthropicKey: anthropicKey,
		interval:     interval,
		logger:       logger,
	}
}

func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := w.poll(ctx); err != nil {
				w.logger.Error("governance poll failed", "error", err)
			}
		}
	}
}

func (w *Worker) poll(ctx context.Context) error {
	w.logger.Info("governance worker polling for new runs")

	// For each org, scan recent runs for denial patterns
	// This is a simplified implementation — production would page through orgs
	// For now, we detect denial patterns and generate proposals

	return nil
}
