package trust

import (
	"math"
	"testing"
)

// Trust score formula at scoring.go:38-63 was previously untested.
// These tests pin the behavior of every weighted term so a future
// reweighting is visible in diff.

func TestComputeScore_BelowRatingThreshold(t *testing.T) {
	s := (&Scorer{}).ComputeScore(ScoreInput{TotalInteractions: 9})
	if s.Rated {
		t.Errorf("Rated=true with <10 interactions; want false")
	}
	if s.Score != 0 {
		t.Errorf("Score=%v with <10 interactions; want 0", s.Score)
	}
}

func TestComputeScore_AtRatingThreshold(t *testing.T) {
	// Exactly 10 interactions is the minimum for rating. Verify it
	// flips Rated to true rather than silently staying unrated.
	s := (&Scorer{}).ComputeScore(ScoreInput{
		TotalInteractions: 10,
		PolicyViolations:  0,
		UptimePercent:     100,
		AvgLatencyMs:      0,
		AccountAgeDays:    0,
	})
	if !s.Rated {
		t.Errorf("Rated=false at 10 interactions; want true (formula boundary)")
	}
}

func TestComputeScore_PerfectAgentIsCapped(t *testing.T) {
	// 1000 interactions, no violations, 100% uptime, 0 latency, >1y
	// old, verified. Sum should saturate at 1.0 — verify the cap holds.
	s := (&Scorer{}).ComputeScore(ScoreInput{
		TotalInteractions: 1000,
		PolicyViolations:  0,
		UptimePercent:     100,
		AvgLatencyMs:      0,
		AccountAgeDays:    365,
		IsVerified:        true,
	})
	if s.Score > 1.0 {
		t.Errorf("Score=%v exceeded 1.0 cap", s.Score)
	}
	if math.Abs(s.Score-1.0) > 1e-9 {
		t.Errorf("Score=%v; perfect agent should round to 1.0", s.Score)
	}
}

func TestComputeScore_ViolationsZeroOutTermAt10Percent(t *testing.T) {
	// scoring.go:50 — `(1.0 - math.Min(violationRate*10, 1.0))`
	// At 10% violation rate the term is exactly 0. This is a
	// non-obvious shape worth pinning; a change to `*5` or `*20`
	// would massively shift scores without flagging the diff.
	s := (&Scorer{}).ComputeScore(ScoreInput{
		TotalInteractions: 100,
		PolicyViolations:  10, // 10% rate
		UptimePercent:     0,
		AvgLatencyMs:      0, // gives full 0.05 latency contribution
		AccountAgeDays:    0,
	})
	// successScore=0.03, violationScore=0 (term zeroed at 10%),
	// uptimeScore=0, latencyScore=0.05, ageScore=0. Sum=0.08.
	if math.Abs(s.Score-0.08) > 1e-9 {
		t.Errorf("Score=%v at 10%% violation rate; want 0.08 (violation term zeroed, latency=full)", s.Score)
	}
}

func TestComputeScore_VerifiedBonusOnlyAdds01(t *testing.T) {
	base := (&Scorer{}).ComputeScore(ScoreInput{
		TotalInteractions: 10,
		PolicyViolations:  0,
		UptimePercent:     100,
		AvgLatencyMs:      5000,
		AccountAgeDays:    0,
	})
	verified := (&Scorer{}).ComputeScore(ScoreInput{
		TotalInteractions: 10,
		PolicyViolations:  0,
		UptimePercent:     100,
		AvgLatencyMs:      5000,
		AccountAgeDays:    0,
		IsVerified:        true,
	})
	delta := verified.Score - base.Score
	if math.Abs(delta-0.1) > 0.011 { // rounding to 2dp may shift by 0.005
		t.Errorf("verified bonus delta=%v; expected ~0.10 (got base=%v verified=%v)",
			delta, base.Score, verified.Score)
	}
}

func TestComputeScore_LatencyAbove5sZeroesTerm(t *testing.T) {
	// `math.Max(0, 1.0 - avgLatencyMs/5000)` — above 5s the term is
	// clamped at 0 (rather than going negative).
	s := (&Scorer{}).ComputeScore(ScoreInput{
		TotalInteractions: 100,
		PolicyViolations:  0,
		UptimePercent:     0,
		AvgLatencyMs:      60_000, // 60s — way above the clamp
		AccountAgeDays:    0,
	})
	// Only successScore contributes: 100/1000 * 0.3 = 0.03.
	// Violation: PolicyViolations=0, rate=0 -> (1-0)*0.3 = 0.3.
	// Sum = 0.33. Latency must NOT push the total negative.
	if s.Score < 0 {
		t.Errorf("Score=%v went negative under huge latency; clamp failed", s.Score)
	}
	if math.Abs(s.Score-0.33) > 1e-9 {
		t.Errorf("Score=%v; want 0.33 (latency term clamped at 0)", s.Score)
	}
}

func TestComputeScore_AgeIsCappedAtOneYear(t *testing.T) {
	// AccountAgeDays beyond 365 should not keep adding score — the
	// term saturates at 0.1.
	short := (&Scorer{}).ComputeScore(ScoreInput{
		TotalInteractions: 10,
		AccountAgeDays:    365,
	})
	long := (&Scorer{}).ComputeScore(ScoreInput{
		TotalInteractions: 10,
		AccountAgeDays:    36500, // 100 years
	})
	if short.Score != long.Score {
		t.Errorf("age term not capped: 365d=%v vs 36500d=%v", short.Score, long.Score)
	}
}

func TestComputeScore_ResultIsRoundedTo2dp(t *testing.T) {
	// scoring.go:61 — `total = math.Round(total*100) / 100`. Any
	// score we emit must have at most 2 decimal places, regardless
	// of inputs. This is a contract for UI display + storage.
	s := (&Scorer{}).ComputeScore(ScoreInput{
		TotalInteractions: 137,
		PolicyViolations:  3,
		UptimePercent:     99.7,
		AvgLatencyMs:      234.5,
		AccountAgeDays:    91,
		IsVerified:        false,
	})
	scaled := s.Score * 100
	if math.Abs(scaled-math.Round(scaled)) > 1e-9 {
		t.Errorf("Score=%v not rounded to 2dp", s.Score)
	}
}
