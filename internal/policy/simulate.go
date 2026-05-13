package policy

import (
	"encoding/json"
)

// ActionEvent is the platform-side projection of a runtime trace's
// action line — the minimum surface needed to re-evaluate the intent
// against a candidate policy. Field tags match the runtime's
// trace.ActionEvent (short keys; see therelic/internal/trace/writer.go)
// so a sealed action line decodes cleanly into this struct.
//
// Only the fields the evaluator reads are listed. hmac, response, ctx,
// to_agent, corr, seq, and timestamps are ignored on purpose: the
// simulator is interested in "what would the new policy decide for
// this intent?", not in re-deriving the historical decision.
type ActionEvent struct {
	RunID    string          `json:"run"`
	Protocol string          `json:"proto"`
	Method   string          `json:"method"`
	Target   string          `json:"target"`
	Params   json.RawMessage `json:"params,omitempty"`

	// OldAuth is the recorded verdict ("allow", "deny", "audit_deny",
	// "would_deny"). Simulate compares the new policy's decision
	// against this exact string.
	OldAuth string `json:"auth"`
}

// SampleFlip names a single run+action whose verdict would change
// under the candidate policy. Targets are repeated in the sample so
// the UI can render "this run would have denied web_fetch" without a
// second lookup.
type SampleFlip struct {
	RunID   string `json:"run_id"`
	Target  string `json:"target"`
	Method  string `json:"method"`
	OldAuth string `json:"old_auth"`
	NewAuth string `json:"new_auth"`
}

// Diff is the bucketed result of running a candidate policy across a
// set of historical action events. Counts are total flips; Samples
// holds up to maxSamples (default 5) representative flips per
// direction so the UI has something to click into.
type Diff struct {
	NewlyDenied    int          `json:"newly_denied"`
	NewlyAllowed   int          `json:"newly_allowed"`
	Unchanged      int          `json:"unchanged"`
	TotalEvaluated int          `json:"total_evaluated"`
	Samples        []SampleFlip `json:"samples"`
}

// maxSamplesPerBucket caps the run_id list we collect for each flip
// direction. Five is the sweet spot for the diff drilldown UI — enough
// to let the user form a "looks unintended" judgement without paging.
const maxSamplesPerBucket = 5

// Simulate runs every event in events through the candidate policy
// and reports how the verdicts would have changed. It is pure: no
// network, no goroutines, no policy mutation. It calls the
// package-level Evaluate so the simulator's verdicts and the
// runtime's enforced verdicts can never drift.
//
// State is reset between events. The simulator deliberately does NOT
// apply per-run constraint counters: re-applying max_actions/duration
// at simulation time would conflate "the new policy would deny this"
// with "the run would never have reached this point" — both are
// interesting but mixing them muddies the badge's claim. Slice 13
// answers "would the rule list decide differently?", nothing more.
func Simulate(p *Policy, events []ActionEvent) Diff {
	d := Diff{
		Samples: make([]SampleFlip, 0, maxSamplesPerBucket*2),
	}
	deniedSamples := 0
	allowedSamples := 0

	for _, ev := range events {
		d.TotalEvaluated++

		intent := ActionIntent{
			Protocol: ev.Protocol,
			Method:   ev.Method,
			Target:   ev.Target,
			Params:   ev.Params,
		}
		decision := Evaluate(intent, p, RunState{})

		// Bucket by recorded vs new outcome. "audit_deny" and
		// "would_deny" are non-enforcing denies in the recorded trace
		// — we treat them as denied for diffing because that's the
		// verdict that was logged. If the new policy would also deny
		// (regardless of mode), it's unchanged.
		wasDenied := isDeniedVerdict(ev.OldAuth)
		nowDenied := decision.IsDenied() || decision.Decision == "audit_deny" || decision.Decision == "would_deny"

		switch {
		case wasDenied && nowDenied:
			d.Unchanged++
		case !wasDenied && !nowDenied:
			d.Unchanged++
		case !wasDenied && nowDenied:
			d.NewlyDenied++
			if deniedSamples < maxSamplesPerBucket {
				d.Samples = append(d.Samples, SampleFlip{
					RunID:   ev.RunID,
					Target:  ev.Target,
					Method:  ev.Method,
					OldAuth: ev.OldAuth,
					NewAuth: decision.Decision,
				})
				deniedSamples++
			}
		case wasDenied && !nowDenied:
			d.NewlyAllowed++
			if allowedSamples < maxSamplesPerBucket {
				d.Samples = append(d.Samples, SampleFlip{
					RunID:   ev.RunID,
					Target:  ev.Target,
					Method:  ev.Method,
					OldAuth: ev.OldAuth,
					NewAuth: decision.Decision,
				})
				allowedSamples++
			}
		}
	}
	return d
}

func isDeniedVerdict(auth string) bool {
	switch auth {
	case "deny", "audit_deny", "would_deny":
		return true
	}
	return false
}
