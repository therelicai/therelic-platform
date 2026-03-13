package trust

import (
	"context"
	"log/slog"
	"math"
	"time"

	"github.com/therelicai/therelic-platform/internal/storage"
)

type ScoreInput struct {
	TotalInteractions int
	PolicyViolations  int
	UptimePercent     float64
	AvgLatencyMs      float64
	AccountAgeDays    int
	IsVerified        bool
}

type Score struct {
	AgentID   string    `json:"agent_id"`
	Score     float64   `json:"score"`
	Rated     bool      `json:"rated"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Scorer struct {
	db     *storage.Postgres
	logger *slog.Logger
}

func NewScorer(db *storage.Postgres, logger *slog.Logger) *Scorer {
	return &Scorer{db: db, logger: logger}
}

// ComputeScore calculates a trust score from 0.0 to 1.0 based on weighted signals.
func (s *Scorer) ComputeScore(input ScoreInput) Score {
	if input.TotalInteractions < 10 {
		return Score{Score: 0, Rated: false, UpdatedAt: time.Now()}
	}

	violationRate := 0.0
	if input.TotalInteractions > 0 {
		violationRate = float64(input.PolicyViolations) / float64(input.TotalInteractions)
	}

	successScore := math.Min(float64(input.TotalInteractions)/1000.0, 1.0) * 0.3
	violationScore := (1.0 - math.Min(violationRate*10, 1.0)) * 0.3
	uptimeScore := (input.UptimePercent / 100.0) * 0.15
	latencyScore := math.Max(0, 1.0-input.AvgLatencyMs/5000.0) * 0.05
	ageScore := math.Min(float64(input.AccountAgeDays)/365.0, 1.0) * 0.1

	total := successScore + violationScore + uptimeScore + latencyScore + ageScore

	if input.IsVerified {
		total += 0.1
	}

	total = math.Min(total, 1.0)
	total = math.Round(total*100) / 100

	return Score{Score: total, Rated: true, UpdatedAt: time.Now()}
}

// RunBatchScoring recomputes trust scores for all listed agents.
func (s *Scorer) RunBatchScoring(ctx context.Context) error {
	s.logger.Info("running batch trust score computation")
	// TODO: Query all capability_listings, compute scores from trace data,
	// update trust_score column
	return nil
}
