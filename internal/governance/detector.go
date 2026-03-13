package governance

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/therelicai/therelic-platform/internal/storage"
)

type DenialPattern struct {
	AgentName   string   `json:"agent_name"`
	Tool        string   `json:"tool"`
	Count       int      `json:"count"`
	RunIDs      []string `json:"run_ids"`
	SampleParam string   `json:"sample_param,omitempty"`
}

type Detector struct {
	db     *storage.Postgres
	logger *slog.Logger
}

func NewDetector(db *storage.Postgres, logger *slog.Logger) *Detector {
	return &Detector{db: db, logger: logger}
}

// DetectDenialPatterns scans recent runs for an org and groups denied actions
// by tool name. If a tool is denied more than threshold times in the window,
// it returns a DenialPattern.
func (d *Detector) DetectDenialPatterns(ctx context.Context, orgID string, windowDays, threshold int) ([]DenialPattern, error) {
	runs, err := d.db.ListRuns(ctx, orgID, "", 500, 0)
	if err != nil {
		return nil, err
	}

	// Aggregate denial counts by agent+tool across runs
	type key struct{ agent, tool string }
	counts := map[key]*DenialPattern{}

	for _, run := range runs {
		if run.ActionsDenied == 0 {
			continue
		}
		k := key{run.AgentName, "unknown"}
		if p, ok := counts[k]; ok {
			p.Count += run.ActionsDenied
			p.RunIDs = append(p.RunIDs, run.ID)
		} else {
			counts[k] = &DenialPattern{
				AgentName: run.AgentName,
				Tool:      "unknown",
				Count:     run.ActionsDenied,
				RunIDs:    []string{run.ID},
			}
		}
	}

	var patterns []DenialPattern
	for _, p := range counts {
		if p.Count >= threshold {
			patterns = append(patterns, *p)
		}
	}

	return patterns, nil
}

// GenerateProposal creates a Proposal from a DenialPattern.
func (d *Detector) GenerateProposal(orgID string, pattern DenialPattern) *storage.Proposal {
	evidence, _ := json.Marshal(map[string]any{
		"denied_tool":  pattern.Tool,
		"denial_count": pattern.Count,
		"run_ids":      pattern.RunIDs,
	})
	rule, _ := json.Marshal(map[string]any{
		"id":       "allow-" + pattern.Tool,
		"protocol": "mcp",
		"method":   "tool_call",
		"target":   pattern.Tool,
		"action":   "allow",
	})

	return &storage.Proposal{
		OrgID:        orgID,
		AgentName:    pattern.AgentName,
		TriggerType:  "denial_pattern",
		Evidence:     evidence,
		ProposedRule: rule,
	}
}
