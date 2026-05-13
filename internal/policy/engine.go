package policy

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/bmatcuk/doublestar/v4"
)

// matchParams returns true when every key in ruleParams has a corresponding
// value in intentParams whose string representation matches the glob pattern.
// An empty ruleParams map matches any intent.
func matchParams(intentParams json.RawMessage, ruleParams map[string]string) bool {
	if len(ruleParams) == 0 {
		return true
	}
	if len(intentParams) == 0 {
		return false
	}
	var parsed map[string]any
	if err := json.Unmarshal(intentParams, &parsed); err != nil {
		return false
	}
	for key, pattern := range ruleParams {
		val, ok := parsed[key]
		if !ok {
			return false
		}
		strVal := fmt.Sprintf("%v", val)
		if !matchGlob(strVal, pattern) {
			return false
		}
	}
	return true
}

// ActionIntent is the normalized representation of any intercepted request.
// Every protocol adapter (MCP proxy, HTTP logger) converts its raw request
// into this common form before calling the policy engine.
type ActionIntent struct {
	Protocol string          // "mcp", "http", "https"
	Method   string          // "tool_call", "resource_read", "GET", "CONNECT", …
	Target   string          // Tool name, resource URI, URL path, or host:port
	Params   json.RawMessage // Input parameters; may be redacted before tracing
}

// RunState captures the execution counters needed for constraint evaluation.
// The proxy maintains this state and passes it on every Evaluate call.
type RunState struct {
	ActionCount    int // Total actions completed so far in this run
	ElapsedSeconds int // Wall-clock seconds elapsed since the run started
}

// AuthDecision is the result of a single policy evaluation.
type AuthDecision struct {
	// Decision is one of: "allow", "deny", "audit_deny", "would_deny".
	//   "allow"      – the action is permitted.
	//   "deny"       – the action is blocked; an error is returned to the agent.
	//   "audit_deny" – the action proceeds but is flagged in the trace (audit mode).
	//   "would_deny" – the action proceeds but is flagged in the trace (permissive mode).
	Decision string

	// RuleID is the matched rule's id field, "default", or a constraint label
	// ("constraint:max_actions", "constraint:max_duration").
	RuleID string

	// Reason is a human-readable explanation used in MCP/HTTP error responses.
	Reason string
}

// IsAllowed reports whether the action should proceed (even if flagged).
func (d AuthDecision) IsAllowed() bool {
	return d.Decision != "deny"
}

// IsDenied reports whether the action should be blocked.
func (d AuthDecision) IsDenied() bool {
	return d.Decision == "deny"
}

// Evaluate is the core policy evaluation function described in architecture
// section 7.1. It is a pure function: no state, no side effects.
//
// Evaluation order:
//  1. Constraint checks (hard limits — not overridden by mode)
//  2. Rule list (document order, first match wins)
//  3. Policy default (deny or allow)
//
// The mode (enforce / audit / permissive) is applied to rule and default
// outcomes but NOT to constraint violations, which are always hard denials.
func Evaluate(intent ActionIntent, p *Policy, state RunState) AuthDecision {
	// Constraint: maximum actions per run.
	if p.Constraints.MaxActions > 0 && state.ActionCount >= p.Constraints.MaxActions {
		return AuthDecision{
			Decision: "deny",
			RuleID:   "constraint:max_actions",
			Reason: fmt.Sprintf(
				"action limit reached (%d/%d actions)",
				state.ActionCount, p.Constraints.MaxActions,
			),
		}
	}

	// Constraint: maximum wall-clock duration.
	if p.Constraints.MaxDurationSeconds > 0 && state.ElapsedSeconds >= p.Constraints.MaxDurationSeconds {
		return AuthDecision{
			Decision: "deny",
			RuleID:   "constraint:max_duration",
			Reason: fmt.Sprintf(
				"duration limit reached (%ds/%ds elapsed)",
				state.ElapsedSeconds, p.Constraints.MaxDurationSeconds,
			),
		}
	}

	// Rules — document order, first match wins.
	for _, rule := range p.Rules {
		if matchGlob(intent.Protocol, rule.Protocol) &&
			matchGlob(intent.Method, rule.Method) &&
			matchGlob(intent.Target, rule.Target) &&
			matchParams(intent.Params, rule.Params) {
			reason := fmt.Sprintf("matched rule %q", rule.ID)
			return applyMode(rule.Action, rule.ID, reason, p.Mode)
		}
	}

	// Default — no rule matched.
	reason := fmt.Sprintf("no matching rule; policy default: %s", p.Default)
	return applyMode(p.Default, "default", reason, p.Mode)
}

// applyMode transforms a raw "allow"/"deny" outcome into a final AuthDecision
// by applying the policy mode. Constraints skip this path and are always hard.
func applyMode(action, ruleID, reason, mode string) AuthDecision {
	isDeny := action == "deny"

	switch mode {
	case "audit":
		if isDeny {
			return AuthDecision{
				Decision: "audit_deny",
				RuleID:   ruleID,
				Reason:   reason + " (audit mode: action allowed)",
			}
		}
	case "permissive":
		if isDeny {
			return AuthDecision{
				Decision: "would_deny",
				RuleID:   ruleID,
				Reason:   reason + " (permissive mode: action allowed)",
			}
		}
	}

	if isDeny {
		return AuthDecision{Decision: "deny", RuleID: ruleID, Reason: reason}
	}
	return AuthDecision{Decision: "allow", RuleID: ruleID, Reason: reason}
}

// matchGlob returns true when value matches pattern using doublestar semantics:
//
//	*      matches any sequence of non-separator characters
//	**     matches any sequence of characters including separators
//	{a,b}  matches any of the alternatives
//	?      matches any single non-separator character
//
// The separator character is '/'. For tool names (which contain no '/'),
// '*' and '**' are equivalent.
func matchGlob(value, pattern string) bool {
	if pattern == "*" || pattern == "**" {
		return true
	}
	matched, err := doublestar.Match(pattern, value)
	if err != nil {
		return false
	}
	return matched
}

// ---------------------------------------------------------------------------
// Engine — convenience wrapper that holds a *Policy for repeated calls.
// ---------------------------------------------------------------------------

// Engine evaluates authorization decisions against a Policy that can be
// swapped at runtime (e.g. via --watch hot-reload). All access to the
// underlying policy is protected by an RWMutex.
type Engine struct {
	mu     sync.RWMutex
	policy *Policy
}

// NewEngine creates an Engine bound to the given policy.
func NewEngine(p *Policy) *Engine {
	return &Engine{policy: p}
}

// SwapPolicy atomically replaces the current policy.
func (e *Engine) SwapPolicy(p *Policy) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.policy = p
}

// Evaluate delegates to the package-level Evaluate function, reading the
// current policy under a read lock.
func (e *Engine) Evaluate(intent ActionIntent, state RunState) AuthDecision {
	e.mu.RLock()
	p := e.policy
	e.mu.RUnlock()
	return Evaluate(intent, p, state)
}
