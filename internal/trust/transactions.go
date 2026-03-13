package trust

import (
	"context"
	"log/slog"
	"time"

	"github.com/therelicai/therelic-platform/internal/storage"
)

type Transaction struct {
	ID            string    `json:"id"`
	Corr          string    `json:"corr"`
	CallerOrgID   string    `json:"caller_org_id"`
	ProviderOrgID string    `json:"provider_org_id"`
	CallerAgent   string    `json:"caller_agent"`
	ProviderAgent string    `json:"provider_agent"`
	Tool          string    `json:"tool"`
	DurationMs    int       `json:"duration_ms"`
	Price         float64   `json:"price"`
	Status        string    `json:"status"`
	CreatedAt     time.Time `json:"created_at"`
}

type TransactionSummary struct {
	TotalTransactions int     `json:"total_transactions"`
	TotalRevenue      float64 `json:"total_revenue"`
	PlatformFee       float64 `json:"platform_fee"`
	Period            string  `json:"period"`
}

type TransactionService struct {
	db     *storage.Postgres
	logger *slog.Logger
}

func NewTransactionService(db *storage.Postgres, logger *slog.Logger) *TransactionService {
	return &TransactionService{db: db, logger: logger}
}

func (t *TransactionService) Record(ctx context.Context, txn Transaction) error {
	// TODO: INSERT INTO transactions
	t.logger.Info("recording transaction", "corr", txn.Corr, "tool", txn.Tool, "price", txn.Price)
	return nil
}

func (t *TransactionService) Summarize(ctx context.Context, orgID string) (*TransactionSummary, error) {
	// TODO: Aggregate transactions for current month
	return &TransactionSummary{
		TotalTransactions: 0,
		TotalRevenue:      0,
		PlatformFee:       0,
		Period:            "current_month",
	}, nil
}

// RunSettlement processes daily transaction aggregation and prepares payouts.
func (t *TransactionService) RunSettlement(ctx context.Context) error {
	t.logger.Info("running daily settlement")
	// TODO: Aggregate completed transactions, compute platform fee,
	// create Stripe Connect transfers for provider orgs
	return nil
}
