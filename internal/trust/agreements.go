package trust

import (
	"context"
	"log/slog"

	"github.com/therelicai/therelic-platform/internal/storage"
)

type BilateralAgreement struct {
	ID             string `json:"id"`
	CallerOrgID    string `json:"caller_org_id"`
	ProviderOrgID  string `json:"provider_org_id"`
	CallerAgent    string `json:"caller_agent"`
	ProviderAgent  string `json:"provider_agent"`
	CallerPolicy   any    `json:"caller_policy"`
	ProviderPolicy any    `json:"provider_policy"`
	Status         string `json:"status"`
}

type AgreementService struct {
	db     *storage.Postgres
	logger *slog.Logger
}

func NewAgreementService(db *storage.Postgres, logger *slog.Logger) *AgreementService {
	return &AgreementService{db: db, logger: logger}
}

// GenerateBilateral creates caller-side and provider-side policy templates
// for a requested agent-to-agent interaction.
func (a *AgreementService) GenerateBilateral(ctx context.Context, callerAgent, providerAgent, tool string) (*BilateralAgreement, error) {
	callerPolicy := map[string]any{
		"rules": []map[string]any{{
			"id":       "allow-call-" + tool,
			"protocol": "mcp",
			"method":   "tool_call",
			"target":   tool,
			"action":   "allow",
			"to_agent": providerAgent,
		}},
	}

	providerPolicy := map[string]any{
		"rules": []map[string]any{{
			"id":         "allow-from-" + tool,
			"protocol":   "mcp",
			"method":     "tool_call",
			"target":     tool,
			"action":     "allow",
			"from_agent": callerAgent,
		}},
	}

	return &BilateralAgreement{
		CallerAgent:    callerAgent,
		ProviderAgent:  providerAgent,
		CallerPolicy:   callerPolicy,
		ProviderPolicy: providerPolicy,
		Status:         "pending",
	}, nil
}
