package governance

import (
	"context"
	"fmt"
	"log/slog"
)

type IntentClassification struct {
	Intent     string  `json:"intent"`
	Category   string  `json:"category"`
	IsGap      bool    `json:"is_gap"`
	Confidence float64 `json:"confidence"`
	Reasoning  string  `json:"reasoning"`
}

type Classifier struct {
	anthropicKey string
	logger       *slog.Logger
}

func NewClassifier(anthropicKey string, logger *slog.Logger) *Classifier {
	return &Classifier{anthropicKey: anthropicKey, logger: logger}
}

// ClassifyDenial uses an LLM to determine whether a denied action represents
// a policy gap or a correct denial. Returns an intent classification.
func (c *Classifier) ClassifyDenial(ctx context.Context, tool string, params string, denyCount int) (*IntentClassification, error) {
	if c.anthropicKey == "" {
		return &IntentClassification{
			Intent:     "unknown",
			Category:   "unclassified",
			IsGap:      false,
			Confidence: 0,
			Reasoning:  "no LLM API key configured",
		}, nil
	}

	// Build classification prompt
	prompt := fmt.Sprintf(
		`Classify this denied agent action:
Tool: %s
Parameters: %s
Denial count (recent): %d

Is this likely a policy gap (user wants it allowed but hasn't configured it) or a correct denial (user intentionally blocks this)?

Respond with JSON: {"intent": "...", "category": "...", "is_gap": bool, "confidence": 0.0-1.0, "reasoning": "..."}`,
		tool, params, denyCount,
	)

	_ = prompt
	// TODO: Call Anthropic Claude API with the prompt
	// For now, return a conservative classification
	return &IntentClassification{
		Intent:     tool,
		Category:   "pending_classification",
		IsGap:      denyCount > 10,
		Confidence: 0.5,
		Reasoning:  "automated threshold-based classification — LLM classification pending",
	}, nil
}
