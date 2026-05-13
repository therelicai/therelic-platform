package policy

import (
	"fmt"
	"os"
	"regexp"

	"gopkg.in/yaml.v3"
)

// Policy is the top-level structure of a .tr/policy.yaml file.
// Field names and semantics follow architecture section 4.1.
type Policy struct {
	Version            string           `yaml:"version"`
	Agent              AgentIdentity    `yaml:"agent"`
	Mode               string           `yaml:"mode"`    // enforce | audit | permissive
	Default            string           `yaml:"default"` // deny | allow
	SignatureRequired  bool             `yaml:"signature_required"`
	Redaction          RedactionConfig  `yaml:"redaction"`
	Rules              []Rule           `yaml:"rules"`
	Constraints        Constraints      `yaml:"constraints"`
	Filesystem         FilesystemConfig `yaml:"filesystem"`
	Network            NetworkConfig    `yaml:"network"`
	Exfiltration       ExfiltrationConfig `yaml:"exfiltration"`
	Sequences          SequenceConfig     `yaml:"sequences"`
}

// FilesystemConfig defines the sandbox configuration for governed agent processes.
type FilesystemConfig struct {
	Enabled      bool              `yaml:"enabled"`
	Mounts       []FilesystemMount `yaml:"mounts"`
	DenyPatterns []string          `yaml:"deny_patterns"`
}

// FilesystemMount binds a host path into the sandbox with a given permission mode.
type FilesystemMount struct {
	Source string `yaml:"source"`
	Target string `yaml:"target"`
	Mode   string `yaml:"mode"` // "ro" or "rw"
}

// NetworkConfig defines DNS-level allow/deny lists for outbound HTTP/HTTPS.
type NetworkConfig struct {
	DNSAllow []string `yaml:"dns_allow"`
	DNSDeny  []string `yaml:"dns_deny"`
}

// ExfiltrationConfig controls the outbound data exfiltration guard.
type ExfiltrationConfig struct {
	Enabled           bool     `yaml:"enabled"`
	SensitivePatterns []string `yaml:"sensitive_patterns"`
	MaxQueryEntropy   float64  `yaml:"max_query_entropy"`
	MinValueLength    int      `yaml:"min_value_length"`
	BlockAction       string   `yaml:"block_action"` // "deny" or "audit"
}

// AgentIdentity identifies the agent this policy governs.
type AgentIdentity struct {
	Name    string `yaml:"name"`
	Version string `yaml:"version"`
}

// Rule is a single authorization rule. Rules are evaluated in document order;
// first match wins (architecture section 7.1).
type Rule struct {
	ID       string            `yaml:"id"`
	Protocol string            `yaml:"protocol"`       // "mcp" | "http" | "https" | "*"
	Method   string            `yaml:"method"`          // "tool_call" | "GET" | "*" | glob
	Target   string            `yaml:"target"`          // glob: "web_search", "api.example.com/**"
	Action   string            `yaml:"action"`          // "allow" | "deny"
	Params   map[string]string `yaml:"params,omitempty"` // param_name → glob pattern
}

// Constraints caps total actions and wall-clock time per run.
type Constraints struct {
	MaxActions         int `yaml:"max_actions"`
	MaxDurationSeconds int `yaml:"max_duration_seconds"`
}

// RedactionConfig lists parameter keys and HTTP headers whose values are
// replaced with "[REDACTED]" before writing to the trace file.
type RedactionConfig struct {
	Keys    []string `yaml:"keys"`
	Headers []string `yaml:"headers"`
}

// Parse deserializes YAML bytes into a Policy. It does not validate — call
// Validate() separately.
func Parse(data []byte) (*Policy, error) {
	var p Policy
	if err := yaml.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("policy: parse yaml: %w", err)
	}
	return &p, nil
}

// Load reads a file from disk and parses it.
func Load(path string) (*Policy, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("policy: read %s: %w", path, err)
	}
	return Parse(data)
}

// ValidationError describes a single policy validation failure.
type ValidationError struct {
	Field   string
	Message string
}

func (e ValidationError) Error() string {
	if e.Field != "" {
		return fmt.Sprintf("%s: %s", e.Field, e.Message)
	}
	return e.Message
}

// Validate checks the policy for correctness. Returns a slice of
// ValidationErrors (never nil on success — an empty slice means valid).
// When strict is true, additional security warnings are included.
func Validate(p *Policy, strict bool) []ValidationError {
	var errs []ValidationError

	add := func(field, msg string) {
		errs = append(errs, ValidationError{Field: field, Message: msg})
	}

	// version
	if p.Version == "" {
		add("version", "missing required field")
	} else if p.Version != "1" {
		add("version", fmt.Sprintf("unsupported value %q (must be \"1\")", p.Version))
	}

	// agent.name
	if p.Agent.Name == "" {
		add("agent.name", "missing required field")
	}

	// mode
	switch p.Mode {
	case "enforce", "audit", "permissive":
	case "":
		add("mode", "missing required field (must be enforce|audit|permissive)")
	default:
		add("mode", fmt.Sprintf("invalid value %q (must be enforce|audit|permissive)", p.Mode))
	}

	// default
	switch p.Default {
	case "deny", "allow":
	case "":
		add("default", "missing required field (must be deny|allow)")
	default:
		add("default", fmt.Sprintf("invalid value %q (must be deny|allow)", p.Default))
	}

	// rules
	seenIDs := make(map[string]int) // id -> first occurrence index
	for i, r := range p.Rules {
		prefix := fmt.Sprintf("rules[%d]", i)

		if r.ID == "" {
			add(prefix+".id", "missing required field")
		} else {
			if prev, dup := seenIDs[r.ID]; dup {
				add(prefix+".id", fmt.Sprintf("duplicate id %q (first seen at rules[%d])", r.ID, prev))
			} else {
				seenIDs[r.ID] = i
			}
		}

		if r.Protocol == "" {
			add(prefix+".protocol", "missing required field")
		}
		if r.Method == "" {
			add(prefix+".method", "missing required field")
		}
		if r.Target == "" {
			add(prefix+".target", "missing required field")
		}
		switch r.Action {
		case "allow", "deny":
		case "":
			add(prefix+".action", "missing required field (must be allow|deny)")
		default:
			add(prefix+".action", fmt.Sprintf("invalid value %q (must be allow|deny)", r.Action))
		}
	}

	// Filesystem validation
	if p.Filesystem.Enabled {
		if len(p.Filesystem.Mounts) == 0 {
			add("filesystem.mounts", "filesystem is enabled but no mounts are configured")
		}
		for i, m := range p.Filesystem.Mounts {
			prefix := fmt.Sprintf("filesystem.mounts[%d]", i)
			if m.Source == "" {
				add(prefix+".source", "missing required field")
			}
			if m.Target == "" {
				add(prefix+".target", "missing required field")
			}
			if m.Mode != "ro" && m.Mode != "rw" {
				add(prefix+".mode", fmt.Sprintf("invalid value %q (must be \"ro\" or \"rw\")", m.Mode))
			}
		}
	}

	// Exfiltration config validation
	if p.Exfiltration.Enabled {
		for i, pat := range p.Exfiltration.SensitivePatterns {
			if _, err := regexp.Compile(pat); err != nil {
				add(fmt.Sprintf("exfiltration.sensitive_patterns[%d]", i),
					fmt.Sprintf("invalid regex %q: %v", pat, err))
			}
		}
		switch p.Exfiltration.BlockAction {
		case "", "deny", "audit":
		default:
			add("exfiltration.block_action",
				fmt.Sprintf("invalid value %q (must be \"deny\" or \"audit\")", p.Exfiltration.BlockAction))
		}
	}

	// Sequence rules validation
	if len(p.Sequences.Rules) > 0 {
		if p.Sequences.Window != 0 && p.Sequences.Window < 2 {
			add("sequences.window", fmt.Sprintf("invalid value %d (must be >= 2)", p.Sequences.Window))
		}
		seqIDs := make(map[string]int)
		for i, sr := range p.Sequences.Rules {
			prefix := fmt.Sprintf("sequences.rules[%d]", i)
			if sr.ID == "" {
				add(prefix+".id", "missing required field")
			} else if prev, dup := seqIDs[sr.ID]; dup {
				add(prefix+".id", fmt.Sprintf("duplicate id %q (first seen at sequences.rules[%d])", sr.ID, prev))
			} else {
				seqIDs[sr.ID] = i
			}
			if len(sr.Pattern) < 2 {
				add(prefix+".pattern", "must contain at least 2 elements")
			}
			switch sr.Action {
			case "deny", "audit":
			case "":
				add(prefix+".action", "missing required field (must be deny|audit)")
			default:
				add(prefix+".action", fmt.Sprintf("invalid value %q (must be deny|audit)", sr.Action))
			}
		}
	}

	// Strict-mode warnings
	if strict {
		if p.Mode == "enforce" && p.Default == "allow" {
			add("default",
				"[strict] default:allow with mode:enforce is insecure — any unmatched action is permitted")
		}
		if p.Mode == "enforce" && !p.SignatureRequired {
			add("signature_required",
				"[strict] enforce mode without signature_required allows unsigned policy files")
		}
		for i, m := range p.Filesystem.Mounts {
			if m.Mode == "rw" {
				add(fmt.Sprintf("filesystem.mounts[%d].mode", i),
					"[strict] read-write mount — consider read-only for defense in depth")
			}
		}
	}

	return errs
}
