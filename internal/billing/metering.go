package billing

import (
	"context"
	"log/slog"

	"github.com/therelicai/therelic-platform/internal/storage"
)

type Meter struct {
	db     *storage.Postgres
	logger *slog.Logger
}

func NewMeter(db *storage.Postgres, logger *slog.Logger) *Meter {
	return &Meter{db: db, logger: logger}
}

// RecordTraceUpload increments trace usage for an org.
func (m *Meter) RecordTraceUpload(ctx context.Context, orgID string) error {
	// TODO: Implement usage tracking — either in Postgres or via Stripe metering API
	m.logger.Debug("metering trace upload", "org_id", orgID)
	return nil
}

// CheckLimits returns whether the org has exceeded their plan limits.
func (m *Meter) CheckLimits(ctx context.Context, orgID string) (exceeded bool, reason string, err error) {
	// TODO: Query current period usage against plan limits
	return false, "", nil
}

// EnforceRetention deletes expired traces.
func (m *Meter) EnforceRetention(ctx context.Context) error {
	// TODO: DELETE FROM runs WHERE expires_at < now()
	// For each deleted run, also delete the S3 object
	m.logger.Info("retention enforcement sweep")
	return nil
}
