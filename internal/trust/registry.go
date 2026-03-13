package trust

import (
	"context"
	"log/slog"

	"github.com/therelicai/therelic-platform/internal/storage"
)

type CapabilityListing struct {
	AgentID    string  `json:"agent_id"`
	OrgID      string  `json:"org_id"`
	Endpoint   string  `json:"endpoint"`
	Tools      []Tool  `json:"tools"`
	TrustScore float64 `json:"trust_score"`
	Pricing    Pricing `json:"pricing"`
}

type Tool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type Pricing struct {
	Model            string  `json:"model"`
	PerCallPrice     float64 `json:"per_call_price,omitempty"`
	SubscriptionPrice float64 `json:"subscription_price,omitempty"`
}

type Registry struct {
	db     *storage.Postgres
	logger *slog.Logger
}

func NewRegistry(db *storage.Postgres, logger *slog.Logger) *Registry {
	return &Registry{db: db, logger: logger}
}

func (r *Registry) Search(ctx context.Context, query string) ([]CapabilityListing, error) {
	// TODO: Full-text search on tools JSONB via GIN index
	r.logger.Debug("registry search", "query", query)
	return []CapabilityListing{}, nil
}

func (r *Registry) Publish(ctx context.Context, listing CapabilityListing) error {
	// TODO: INSERT INTO capability_listings
	return nil
}

func (r *Registry) GetTrustScore(ctx context.Context, agentID string) (*Score, error) {
	// TODO: SELECT trust_score FROM capability_listings WHERE agent_id = $1
	return &Score{AgentID: agentID, Score: 0.5, Rated: false}, nil
}
