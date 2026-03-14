package governance

import (
	"context"
	"log/slog"
	"time"

	"github.com/therelicai/therelic-platform/internal/storage"
)

type Worker struct {
	db           *storage.Postgres
	s3           *storage.S3
	proposer     *Proposer
	anthropicKey string
	interval     time.Duration
	logger       *slog.Logger
}

func NewWorker(db *storage.Postgres, s3 *storage.S3, anthropicKey string, interval time.Duration, logger *slog.Logger) *Worker {
	return &Worker{
		db:           db,
		s3:           s3,
		proposer:     NewProposer(db, s3, anthropicKey, logger),
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

	orgIDs, err := w.db.ListOrgs(ctx)
	if err != nil {
		return err
	}

	for _, orgID := range orgIDs {
		if err := w.proposer.ProcessOrg(ctx, orgID); err != nil {
			w.logger.Error("process org failed", "org_id", orgID, "error", err)
			continue
		}
		w.logger.Info("processed org", "org_id", orgID)
	}

	w.logger.Info("governance poll complete", "orgs_processed", len(orgIDs))
	return nil
}
