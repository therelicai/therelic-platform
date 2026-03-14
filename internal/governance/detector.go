package governance

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"

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
	s3     *storage.S3
	logger *slog.Logger
}

func NewDetector(db *storage.Postgres, s3 *storage.S3, logger *slog.Logger) *Detector {
	return &Detector{db: db, s3: s3, logger: logger}
}

// traceActionEvent represents a single action event from the NDJSON trace format.
// Supports both "t"/"target" (trtrace format) and "type"/"tool" (alternate).
type traceActionEvent struct {
	Type   string          `json:"t"`
	TypeAlt string         `json:"type"`
	Target string          `json:"target"`
	Tool   string          `json:"tool"`
	Auth   string          `json:"auth"`
	Params json.RawMessage `json:"params"`
}

func (e traceActionEvent) toolName() string {
	if t := e.Target; t != "" {
		return t
	}
	return e.Tool
}

func (e traceActionEvent) isAction() bool {
	return e.Type == "action" || e.TypeAlt == "action"
}

func (e traceActionEvent) isDenied() bool {
	return e.Auth == "deny"
}

// DetectDenialPatterns scans recent runs for an org and groups denied actions
// by tool name. For runs with denials, downloads and parses trace events from S3
// to identify actual tool names. If a tool is denied more than threshold times
// in the window, it returns a DenialPattern.
func (d *Detector) DetectDenialPatterns(ctx context.Context, orgID string, windowDays, threshold int) ([]DenialPattern, error) {
	runs, err := d.db.ListRuns(ctx, orgID, "", 500, 0)
	if err != nil {
		return nil, err
	}

	type key struct{ agent, tool string }
	counts := map[key]*DenialPattern{}
	runIDSeen := map[key]map[string]bool{} // track which runIDs are in each pattern

	for _, run := range runs {
		if run.ActionsDenied == 0 {
			continue
		}

		toolCounts := make(map[string]int)
		var sampleParam string

		if d.s3 != nil && run.StorageKey != "" {
			deniedTools, sample, err := d.parseTraceDenials(ctx, run.StorageKey)
			if err != nil {
				d.logger.Warn("failed to parse trace for denied tools", "run_id", run.ID, "error", err)
				// Fall back to "unknown" if we can't parse
				toolCounts["unknown"] = run.ActionsDenied
			} else {
				for tool, n := range deniedTools {
					toolCounts[tool] += n
				}
				sampleParam = sample
			}
		} else {
			toolCounts["unknown"] = run.ActionsDenied
		}

		for tool, n := range toolCounts {
			k := key{run.AgentName, tool}
			if p, ok := counts[k]; ok {
				p.Count += n
				if !runIDSeen[k][run.ID] {
					p.RunIDs = append(p.RunIDs, run.ID)
					if runIDSeen[k] == nil {
						runIDSeen[k] = make(map[string]bool)
					}
					runIDSeen[k][run.ID] = true
				}
				if sampleParam != "" && p.SampleParam == "" {
					p.SampleParam = sampleParam
				}
			} else {
				runIDSeen[k] = map[string]bool{run.ID: true}
				counts[k] = &DenialPattern{
					AgentName:   run.AgentName,
					Tool:        tool,
					Count:       n,
					RunIDs:      []string{run.ID},
					SampleParam: sampleParam,
				}
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

// parseTraceDenials downloads the gzipped NDJSON trace from S3 and returns
// a map of tool name -> denial count, plus a sample params string.
func (d *Detector) parseTraceDenials(ctx context.Context, storageKey string) (map[string]int, string, error) {
	reader, err := d.s3.Download(ctx, storageKey)
	if err != nil {
		return nil, "", err
	}
	defer reader.Close()

	gz, err := gzip.NewReader(reader)
	if err != nil {
		return nil, "", err
	}
	defer gz.Close()

	toolCounts := make(map[string]int)
	var sampleParam string

	scanner := bufio.NewScanner(gz)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var ev traceActionEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}

		if !ev.isAction() || !ev.isDenied() {
			continue
		}

		tool := ev.toolName()
		if tool == "" {
			tool = "unknown"
		}
		toolCounts[tool]++

		if sampleParam == "" && len(ev.Params) > 0 {
			sampleParam = string(ev.Params)
			if len(sampleParam) > 500 {
				sampleParam = sampleParam[:500] + "..."
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, "", err
	}
	if err := gz.Close(); err != nil && err != io.EOF {
		return nil, "", err
	}

	return toolCounts, sampleParam, nil
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
