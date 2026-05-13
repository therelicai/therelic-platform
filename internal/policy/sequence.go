package policy

import (
	"strings"
)

// SequenceRule defines a suspicious tool-call pattern to detect multi-step
// prompt injection attack chains (e.g. web_fetch → read_file → send_email).
type SequenceRule struct {
	ID      string   `yaml:"id"`
	Pattern []string `yaml:"pattern"` // ordered list of tool-name globs; pipe | means alternatives
	Reason  string   `yaml:"reason"`
	Action  string   `yaml:"action"` // "deny" or "audit"
}

// SequenceConfig holds sequence detection settings.
type SequenceConfig struct {
	Rules  []SequenceRule `yaml:"rules"`
	Window int            `yaml:"window"` // how many recent actions to check (default 10)
}

// SequenceDetector tracks recent tool calls and checks for suspicious patterns.
type SequenceDetector struct {
	rules   []SequenceRule
	window  int
	history []string // ring buffer of recent tool names
	pos     int      // next write position in ring buffer
	count   int      // total items added
}

// NewSequenceDetector creates a detector from the given config. If Window is
// not set (zero), it defaults to 10.
func NewSequenceDetector(cfg SequenceConfig) *SequenceDetector {
	w := cfg.Window
	if w < 2 {
		w = 10
	}
	return &SequenceDetector{
		rules:   cfg.Rules,
		window:  w,
		history: make([]string, w),
	}
}

// SequenceMatch describes which rule fired and the actual tool chain that
// triggered it.
type SequenceMatch struct {
	RuleID string
	Reason string
	Action string   // "deny" or "audit"
	Chain  []string // the actual tools that matched
}

// Record adds a tool name to the history and returns a match if the current
// history matches any sequence rule. Returns nil if no match.
func (d *SequenceDetector) Record(toolName string) *SequenceMatch {
	d.history[d.pos] = toolName
	d.pos = (d.pos + 1) % d.window
	d.count++

	h := d.orderedHistory()
	for i := range d.rules {
		if chain := d.matchSubsequence(h, d.rules[i].Pattern); chain != nil {
			return &SequenceMatch{
				RuleID: d.rules[i].ID,
				Reason: d.rules[i].Reason,
				Action: d.rules[i].Action,
				Chain:  chain,
			}
		}
	}
	return nil
}

// orderedHistory returns the ring buffer contents in chronological order,
// limited to the most recent min(count, window) entries.
func (d *SequenceDetector) orderedHistory() []string {
	n := d.window
	if d.count < n {
		n = d.count
	}
	result := make([]string, n)
	start := d.pos - n
	if start < 0 {
		start += d.window
	}
	for i := 0; i < n; i++ {
		result[i] = d.history[(start+i)%d.window]
	}
	return result
}

// matchSubsequence checks whether the pattern appears as an ordered
// subsequence within history. Returns the matching tool names, or nil.
func (d *SequenceDetector) matchSubsequence(history, pattern []string) []string {
	if len(pattern) == 0 || len(pattern) > len(history) {
		return nil
	}
	chain := make([]string, 0, len(pattern))
	pi := 0
	for _, tool := range history {
		if matchPatternElement(tool, pattern[pi]) {
			chain = append(chain, tool)
			pi++
			if pi == len(pattern) {
				return chain
			}
		}
	}
	return nil
}

// matchPatternElement checks a tool name against a pattern element that may
// contain pipe-separated alternatives, each of which is a glob.
// Example: "read_file|list_directory" matches either "read_file" or "list_directory".
func matchPatternElement(toolName, patternElem string) bool {
	for _, alt := range strings.Split(patternElem, "|") {
		if matchGlob(toolName, strings.TrimSpace(alt)) {
			return true
		}
	}
	return false
}
