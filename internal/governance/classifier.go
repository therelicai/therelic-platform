package governance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
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

	prompt := fmt.Sprintf(
		`Classify this denied agent action:
Tool: %s
Parameters: %s
Denial count (recent): %d

Is this likely a policy gap (user wants it allowed but hasn't configured it) or a correct denial (user intentionally blocks this)?

Respond with JSON only, no other text: {"intent": "...", "category": "...", "is_gap": bool, "confidence": 0.0-1.0, "reasoning": "..."}`,
		tool, params, denyCount,
	)

	client := anthropic.NewClient(option.WithAPIKey(c.anthropicKey))

	msg, err := client.Messages.New(ctx, anthropic.MessageNewParams{
		MaxTokens: 1024,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)),
		},
		Model: anthropic.ModelClaude3_7SonnetLatest,
	})
	if err != nil {
		return nil, fmt.Errorf("anthropic API call: %w", err)
	}

	var responseText strings.Builder
	for _, block := range msg.Content {
		if block.Type == "text" && block.Text != "" {
			responseText.WriteString(block.Text)
		}
	}

	text := strings.TrimSpace(responseText.String())
	if text == "" {
		return &IntentClassification{
			Intent:     tool,
			Category:   "pending_classification",
			IsGap:      denyCount > 10,
			Confidence: 0.5,
			Reasoning:  "empty LLM response — fallback to threshold",
		}, nil
	}

	// Extract JSON from response (may be wrapped in markdown code blocks)
	jsonStr := text
	if idx := strings.Index(text, "{"); idx >= 0 {
		jsonStr = text[idx:]
	}
	if idx := strings.LastIndex(jsonStr, "}"); idx >= 0 {
		jsonStr = jsonStr[:idx+1]
	}

	var out IntentClassification
	if err := json.Unmarshal([]byte(jsonStr), &out); err != nil {
		c.logger.Warn("failed to parse classification JSON", "response", text, "error", err)
		return &IntentClassification{
			Intent:     tool,
			Category:   "pending_classification",
			IsGap:      denyCount > 10,
			Confidence: 0.5,
			Reasoning:  "LLM response parse failed — fallback to threshold",
		}, nil
	}

	return &out, nil
}
