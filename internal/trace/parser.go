// Package trace parses .trtrace files (gzipped NDJSON) and produces an
// authoritative summary. The platform uses this to recompute run metadata
// from the trace bytes rather than trusting headers — any API-key holder
// could otherwise upload an empty file claiming "1000 actions, 0 denied".
//
// The parser is defensive: it accepts traces produced by any version of
// the runtime, ignores unknown event types, and tolerates truncated
// streams (returning whatever it could parse plus an error).
package trace

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

// MaxEventSize caps a single NDJSON line before the parser bails. 4 MiB is
// enough for any realistic redacted tool-call params; beyond that we
// assume the file is hostile.
const MaxEventSize = 4 * 1024 * 1024

// MaxEvents caps how many events we'll parse per file. Prevents an
// attacker from forcing the API process to spend unbounded CPU on a
// pathological trace.
const MaxEvents = 1_000_000

// Summary is the authoritative view of what happened during a run,
// derived from the trace events themselves.
type Summary struct {
	// RunID from the run-start event. Required.
	RunID string

	// AgentName/Version/PolicyHash/Environment from run-start. Optional
	// — empty strings are valid.
	AgentName    string
	AgentVersion string
	PolicyHash   string
	Environment  string

	// StartedAt is the run-start event timestamp parsed as RFC3339Nano.
	// Falls back to time.Now() if missing or unparseable.
	StartedAt time.Time

	// EndedAt is set when a run-end event is present.
	EndedAt *time.Time

	// ExitCode and DurationMs come from the run-end event if present.
	// Both may be nil if the run was truncated.
	ExitCode   *int
	DurationMs *int

	// Action counts are recomputed from action events, not read from
	// run-end. This is the whole point of server-side parsing: refuse
	// to trust client-supplied counts.
	ActionsTotal   int
	ActionsAllowed int
	ActionsDenied  int

	// EventCount is total events seen (action + run + policy_reload + ...).
	EventCount int

	// HasIntegrityChain is true iff every action event carried an "hmac"
	// field. Used to gate the "tamper-evident" badge on the dashboard.
	// Presence-only: this says the trace *claims* tamper-evidence,
	// not that the HMACs verify against the server's master secret.
	HasIntegrityChain bool

	// ChainVerified is true iff the platform recomputed every event's
	// HMAC against the per-run key derived from the configured master
	// secret. False when no master secret is configured OR when the
	// trace simply doesn't carry a chain. True is the strong claim
	// surfaced to compliance dashboards.
	ChainVerified bool

	// Truncated is set if the underlying stream ended mid-event or hit
	// MaxEvents. The run is still indexable but flagged for the user.
	Truncated bool
}

// ErrEmptyTrace is returned when the file decodes successfully but
// contains zero events. We refuse these uploads — there's nothing to
// audit.
var ErrEmptyTrace = errors.New("trace: contains zero events")

// ErrMissingRunStart is returned when no `t:"run", status:"start"`
// event is found. Without it we can't establish the run's identity.
var ErrMissingRunStart = errors.New("trace: missing run-start event")

// ErrTooLarge is returned when a single line exceeds MaxEventSize.
var ErrTooLarge = errors.New("trace: event exceeds maximum line size")

// ErrChainBroken is returned when the master secret is configured and
// the trace's HMAC chain doesn't verify. We surface this distinctly so
// the API layer can return a 422 (semantic failure on a well-formed
// upload) rather than a 400.
var ErrChainBroken = errors.New("trace: HMAC chain failed verification")

// ErrChainExpected is returned when the master secret is configured
// but the trace carries no chain at all. Configurable separately from
// ErrChainBroken because some deployments may accept unsealed traces
// from legacy clients during rollout.
var ErrChainExpected = errors.New("trace: chain expected but missing")

// Parse reads a gzipped NDJSON trace from r and returns the derived
// Summary. The reader is consumed entirely. The caller is responsible
// for size-limiting the upstream (MaxBytesReader, etc).
//
// Parse does not cryptographically verify the HMAC chain — use
// ParseAndVerify when the platform has the trace master secret.
func Parse(r io.Reader) (*Summary, error) {
	return parse(r, nil, false)
}

// ParseAndVerify is like Parse but additionally recomputes the HMAC
// chain using a per-run key derived from masterSecret + runID. When
// requireChain is true, traces without a chain are rejected with
// ErrChainExpected; otherwise unsealed traces are accepted and
// ChainVerified is left false.
//
// A masterSecret of nil or empty is equivalent to Parse.
func ParseAndVerify(r io.Reader, masterSecret []byte, requireChain bool) (*Summary, error) {
	return parse(r, masterSecret, requireChain)
}

func parse(r io.Reader, masterSecret []byte, requireChain bool) (*Summary, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("trace: gzip: %w", err)
	}
	defer gz.Close()

	s := &Summary{
		HasIntegrityChain: true, // turns false as soon as we see one missing hmac
	}

	scanner := bufio.NewScanner(gz)
	scanner.Buffer(make([]byte, 0, 64*1024), MaxEventSize)

	// Capture sealed line bytes lazily — only when a master secret was
	// supplied, since otherwise verification will never run and the
	// buffer would just burn memory.
	var sealedLines [][]byte
	if len(masterSecret) > 0 {
		sealedLines = make([][]byte, 0, 256)
	}

	sawAction := false
	for scanner.Scan() {
		if s.EventCount >= MaxEvents {
			s.Truncated = true
			break
		}
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		s.EventCount++
		if sealedLines != nil {
			// scanner.Bytes() is owned by the scanner and overwritten
			// on the next Scan(); we must copy if we want to keep it.
			cp := make([]byte, len(line))
			copy(cp, line)
			sealedLines = append(sealedLines, cp)
		}

		var envelope map[string]json.RawMessage
		if err := json.Unmarshal(line, &envelope); err != nil {
			// One bad line shouldn't blow up the whole upload — flag
			// truncation and continue. Production runtimes shouldn't
			// emit malformed JSON; this is defense against tooling drift.
			s.Truncated = true
			continue
		}

		var eventType string
		if raw, ok := envelope["t"]; ok {
			_ = json.Unmarshal(raw, &eventType)
		}

		switch eventType {
		case "run":
			applyRunEvent(s, envelope)
		case "action":
			sawAction = true
			applyActionEvent(s, envelope)
		}
	}

	if err := scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return nil, ErrTooLarge
		}
		s.Truncated = true
	}

	// If we never saw an action, the chain claim is meaningless.
	if !sawAction {
		s.HasIntegrityChain = false
	}

	if s.EventCount == 0 {
		return nil, ErrEmptyTrace
	}
	if s.RunID == "" {
		return nil, ErrMissingRunStart
	}
	if s.StartedAt.IsZero() {
		s.StartedAt = time.Now().UTC()
	}

	if len(masterSecret) > 0 {
		switch {
		case !s.HasIntegrityChain:
			if requireChain {
				return s, ErrChainExpected
			}
		case s.Truncated:
			// A truncated trace can't be cleanly verified — the chain
			// itself may be intact up to the truncation point but
			// promising "verified" on a partial stream is misleading.
			// We surface HasIntegrityChain so dashboards can show
			// "claimed sealed, partial".
		default:
			perRunKey := generateChainKey(s.RunID, masterSecret)
			if err := verifyChain(sealedLines, perRunKey); err != nil {
				return s, fmt.Errorf("%w: %v", ErrChainBroken, err)
			}
			s.ChainVerified = true
		}
	}

	return s, nil
}

func applyRunEvent(s *Summary, env map[string]json.RawMessage) {
	var status string
	if raw, ok := env["status"]; ok {
		_ = json.Unmarshal(raw, &status)
	}

	switch status {
	case "start":
		// Run-start defines identity. Later starts (shouldn't happen)
		// don't overwrite — first one wins.
		if s.RunID == "" {
			s.RunID = stringField(env, "run")
			s.AgentName = stringField(env, "agent")
			s.AgentVersion = stringField(env, "agent_v")
			s.PolicyHash = stringField(env, "policy")
			s.Environment = stringField(env, "env")
			if ts := stringField(env, "ts"); ts != "" {
				if parsed, err := time.Parse(time.RFC3339Nano, ts); err == nil {
					s.StartedAt = parsed.UTC()
				}
			}
		}
	case "end":
		if raw, ok := env["exit"]; ok {
			var v int
			if err := json.Unmarshal(raw, &v); err == nil {
				s.ExitCode = &v
			}
		}
		if raw, ok := env["ms"]; ok {
			var v int
			if err := json.Unmarshal(raw, &v); err == nil {
				s.DurationMs = &v
			}
		}
		if ts := stringField(env, "ts"); ts != "" {
			if parsed, err := time.Parse(time.RFC3339Nano, ts); err == nil {
				utc := parsed.UTC()
				s.EndedAt = &utc
			}
		}
	}
}

func applyActionEvent(s *Summary, env map[string]json.RawMessage) {
	s.ActionsTotal++

	if _, ok := env["hmac"]; !ok {
		s.HasIntegrityChain = false
	}

	auth := stringField(env, "auth")
	// Match the runtime's actionStats.record semantics. Any policy mode
	// that refused the call counts as denied.
	switch auth {
	case "deny", "audit_deny", "would_deny":
		s.ActionsDenied++
	default:
		s.ActionsAllowed++
	}
}

func stringField(env map[string]json.RawMessage, key string) string {
	raw, ok := env[key]
	if !ok {
		return ""
	}
	var v string
	if err := json.Unmarshal(raw, &v); err != nil {
		return ""
	}
	return v
}
