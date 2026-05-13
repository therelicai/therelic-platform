package policy

import (
	"encoding/json"
	"testing"
)

// fixturePolicy returns a small enforce-mode policy with default-deny
// and a single allow rule for tool_call web_search. Used as the base
// "candidate" the simulator evaluates against.
func fixturePolicy(t *testing.T) *Policy {
	t.Helper()
	yamlBytes := []byte(`
version: "1"
agent:
  name: simulator-test
mode: enforce
default: deny
rules:
  - id: allow-search
    protocol: mcp
    method: tool_call
    target: web_search
    action: allow
`)
	p, err := Parse(yamlBytes)
	if err != nil {
		t.Fatalf("fixturePolicy parse: %v", err)
	}
	if errs := Validate(p, false); len(errs) > 0 {
		t.Fatalf("fixturePolicy validate: %v", errs)
	}
	return p
}

// TestSimulate_BucketsFlips walks one event of each flip direction
// through the candidate policy and confirms the counters and samples
// match. This is the load-bearing test for the diff badge claim.
func TestSimulate_BucketsFlips(t *testing.T) {
	p := fixturePolicy(t)

	events := []ActionEvent{
		// Was allowed in production (suppose old policy let everything),
		// new policy denies because exec_shell isn't on the allow list.
		{RunID: "run-A", Protocol: "mcp", Method: "tool_call", Target: "exec_shell", OldAuth: "allow"},
		// Was denied in production, new policy still denies — unchanged.
		{RunID: "run-B", Protocol: "mcp", Method: "tool_call", Target: "read_file", OldAuth: "deny"},
		// Was denied in production, new policy allows because web_search
		// is on the allow list — newly_allowed flip.
		{RunID: "run-C", Protocol: "mcp", Method: "tool_call", Target: "web_search", OldAuth: "deny"},
		// Was allowed in production, new policy also allows web_search — unchanged.
		{RunID: "run-D", Protocol: "mcp", Method: "tool_call", Target: "web_search", OldAuth: "allow"},
	}

	d := Simulate(p, events)

	if d.TotalEvaluated != 4 {
		t.Errorf("TotalEvaluated: got %d, want 4", d.TotalEvaluated)
	}
	if d.NewlyDenied != 1 {
		t.Errorf("NewlyDenied: got %d, want 1", d.NewlyDenied)
	}
	if d.NewlyAllowed != 1 {
		t.Errorf("NewlyAllowed: got %d, want 1", d.NewlyAllowed)
	}
	if d.Unchanged != 2 {
		t.Errorf("Unchanged: got %d, want 2", d.Unchanged)
	}
	if len(d.Samples) != 2 {
		t.Fatalf("Samples: got %d, want 2", len(d.Samples))
	}

	var sawDenied, sawAllowed bool
	for _, s := range d.Samples {
		switch s.RunID {
		case "run-A":
			sawDenied = true
			if s.OldAuth != "allow" || s.NewAuth != "deny" {
				t.Errorf("run-A sample: old=%q new=%q, want allow→deny", s.OldAuth, s.NewAuth)
			}
		case "run-C":
			sawAllowed = true
			if s.OldAuth != "deny" || s.NewAuth != "allow" {
				t.Errorf("run-C sample: old=%q new=%q, want deny→allow", s.OldAuth, s.NewAuth)
			}
		}
	}
	if !sawDenied || !sawAllowed {
		t.Errorf("missing expected samples: sawDenied=%v sawAllowed=%v", sawDenied, sawAllowed)
	}
}

// TestSimulate_CapsSamplesPerBucket confirms we cap each direction at
// maxSamplesPerBucket so a pathological diff doesn't dump megabytes of
// flips into the API response.
func TestSimulate_CapsSamplesPerBucket(t *testing.T) {
	p := fixturePolicy(t)

	events := make([]ActionEvent, 0, 20)
	for i := 0; i < 20; i++ {
		// Twenty newly-denied flips: old policy allowed, new policy
		// denies because exec_shell isn't on the allow list.
		events = append(events, ActionEvent{
			RunID: "run", Protocol: "mcp", Method: "tool_call",
			Target: "exec_shell", OldAuth: "allow",
		})
	}

	d := Simulate(p, events)

	if d.NewlyDenied != 20 {
		t.Errorf("NewlyDenied: got %d, want 20", d.NewlyDenied)
	}
	if len(d.Samples) != maxSamplesPerBucket {
		t.Errorf("Samples: got %d, want %d", len(d.Samples), maxSamplesPerBucket)
	}
}

// TestSimulate_ParamGlobMatching exercises the matchParams path —
// proves the simulator is using the same matcher the runtime uses, so
// "would have denied web_fetch with a flagged param" diffs work. The
// pattern is on a non-path-shaped value (no '/') so it sidesteps
// doublestar's segment-aware behavior — that's tested by the engine's
// own suite upstream, not our concern here.
func TestSimulate_ParamGlobMatching(t *testing.T) {
	yamlBytes := []byte(`
version: "1"
agent:
  name: param-test
mode: enforce
default: allow
rules:
  - id: deny-internal-fetch
    protocol: mcp
    method: tool_call
    target: web_fetch
    action: deny
    params:
      env: "prod*"
`)
	p, err := Parse(yamlBytes)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	events := []ActionEvent{
		{
			RunID: "run-X", Protocol: "mcp", Method: "tool_call",
			Target: "web_fetch", OldAuth: "allow",
			Params: json.RawMessage(`{"env":"production"}`),
		},
		{
			RunID: "run-Y", Protocol: "mcp", Method: "tool_call",
			Target: "web_fetch", OldAuth: "allow",
			Params: json.RawMessage(`{"env":"staging"}`),
		},
	}

	d := Simulate(p, events)

	if d.NewlyDenied != 1 {
		t.Errorf("NewlyDenied: got %d, want 1", d.NewlyDenied)
	}
	if d.Unchanged != 1 {
		t.Errorf("Unchanged: got %d, want 1", d.Unchanged)
	}
	if len(d.Samples) != 1 || d.Samples[0].RunID != "run-X" {
		t.Errorf("Sample: got %+v, want run-X only", d.Samples)
	}
}

// TestSimulate_TreatsAuditDenyAsDenied confirms recorded audit_deny /
// would_deny verdicts are treated as denies for diffing purposes. If
// a user moves from permissive to enforce, the diff should show "no
// flips" — the rule list isn't changing, only the enforcement mode.
func TestSimulate_TreatsAuditDenyAsDenied(t *testing.T) {
	p := fixturePolicy(t) // enforce + default deny + allow web_search

	events := []ActionEvent{
		// Same rule that produced this audit_deny still produces a deny.
		{RunID: "run-1", Protocol: "mcp", Method: "tool_call", Target: "exec_shell", OldAuth: "audit_deny"},
		{RunID: "run-2", Protocol: "mcp", Method: "tool_call", Target: "exec_shell", OldAuth: "would_deny"},
	}

	d := Simulate(p, events)
	if d.NewlyDenied != 0 {
		t.Errorf("audit_deny/would_deny should not be counted as newly_denied; got %d", d.NewlyDenied)
	}
	if d.Unchanged != 2 {
		t.Errorf("Unchanged: got %d, want 2", d.Unchanged)
	}
}
