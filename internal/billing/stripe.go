package billing

import (
	"context"
	"fmt"
	"log/slog"
)

type Plan struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	PriceMonthly   int    `json:"price_monthly_cents"`
	RetentionDays  int    `json:"retention_days"`
	MaxTracesMo    int    `json:"max_traces_per_month"`
	MaxAgents      int    `json:"max_agents"`
}

var Plans = []Plan{
	{ID: "free", Name: "Free", PriceMonthly: 0, RetentionDays: 7, MaxTracesMo: 100, MaxAgents: 3},
	{ID: "team", Name: "Team", PriceMonthly: 5000, RetentionDays: 30, MaxTracesMo: 10000, MaxAgents: 25},
	{ID: "enterprise", Name: "Enterprise", PriceMonthly: 25000, RetentionDays: 365, MaxTracesMo: -1, MaxAgents: -1},
}

type Service struct {
	stripeKey string
	logger    *slog.Logger
}

func NewService(stripeKey string, logger *slog.Logger) *Service {
	return &Service{stripeKey: stripeKey, logger: logger}
}

func (s *Service) CreateCheckoutSession(_ context.Context, orgID, planID, successURL, cancelURL string) (string, error) {
	if s.stripeKey == "" {
		return "", fmt.Errorf("billing not configured: STRIPE_SECRET_KEY not set")
	}

	// TODO: Use stripe-go to create a Checkout Session
	// stripe.Key = s.stripeKey
	// params := &stripe.CheckoutSessionParams{...}
	// session, err := session.New(params)

	return fmt.Sprintf("https://checkout.stripe.com/placeholder?org=%s&plan=%s", orgID, planID), nil
}

func (s *Service) GetUsage(_ context.Context, orgID string) (*Usage, error) {
	return &Usage{
		OrgID:        orgID,
		TracesUsed:   0,
		TracesLimit:  100,
		AgentsUsed:   0,
		AgentsLimit:  3,
		PeriodStart:  "2026-03-01",
		PeriodEnd:    "2026-03-31",
	}, nil
}

type Usage struct {
	OrgID       string `json:"org_id"`
	TracesUsed  int    `json:"traces_used"`
	TracesLimit int    `json:"traces_limit"`
	AgentsUsed  int    `json:"agents_used"`
	AgentsLimit int    `json:"agents_limit"`
	PeriodStart string `json:"period_start"`
	PeriodEnd   string `json:"period_end"`
}
