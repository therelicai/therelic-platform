package governance

import (
	"context"
	"log/slog"

	"github.com/therelicai/therelic-platform/internal/storage"
)

type Proposer struct {
	db         *storage.Postgres
	detector   *Detector
	classifier *Classifier
	logger     *slog.Logger
}

func NewProposer(db *storage.Postgres, s3 *storage.S3, anthropicKey string, logger *slog.Logger) *Proposer {
	return &Proposer{
		db:         db,
		detector:   NewDetector(db, s3, logger),
		classifier: NewClassifier(anthropicKey, logger),
		logger:     logger,
	}
}

// ProcessOrg scans recent runs for an org, detects denial patterns,
// classifies them, and generates proposals for policy gaps.
func (p *Proposer) ProcessOrg(ctx context.Context, orgID string) error {
	patterns, err := p.detector.DetectDenialPatterns(ctx, orgID, 7, 5)
	if err != nil {
		return err
	}

	for _, pattern := range patterns {
		classification, err := p.classifier.ClassifyDenial(ctx, pattern.Tool, pattern.SampleParam, pattern.Count)
		if err != nil {
			p.logger.Error("classification failed", "tool", pattern.Tool, "error", err)
			continue
		}

		if !classification.IsGap {
			p.logger.Debug("denial classified as correct", "tool", pattern.Tool)
			continue
		}

		proposal := p.detector.GenerateProposal(orgID, pattern)
		if err := p.db.InsertProposal(ctx, proposal); err != nil {
			p.logger.Error("failed to insert proposal", "error", err)
			continue
		}

		p.logger.Info("proposal generated",
			"org_id", orgID,
			"agent", pattern.AgentName,
			"tool", pattern.Tool,
			"proposal_id", proposal.ID,
		)
	}

	return nil
}
