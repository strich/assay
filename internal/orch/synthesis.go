package orch

// synthesis.go ports Phase 7 (_synthesize): deterministic dedup → scoring →
// truncate to max_comments.

import (
	"github.com/strich/assay/internal/schemas"
	"github.com/strich/assay/internal/scoring"
)

func (o *Orchestrator) synthesize(
	findings []schemas.ReviewFinding,
	adversaryResults []schemas.AdversaryResult,
) []schemas.ScoredFinding {
	o.progress("phase_start", map[string]any{"phase": "synthesis", "findings": len(findings)})
	deduped := scoring.DeduplicateExact(findings)
	aiGen := 0.0
	if o.intakeResult != nil {
		aiGen = o.intakeResult.AIGenerated
	}
	blastSize := 0
	if o.anatomyResult != nil {
		blastSize = len(o.anatomyResult.BlastRadius)
	}
	scored := scoring.ScoreFindings(deduped, adversaryResults, o.config.Scoring, aiGen, blastSize)
	if len(scored) > o.config.Comments.MaxComments {
		scored = scored[:o.config.Comments.MaxComments]
	}
	// Hidden traps are synthesized by the adversary itself.
	adversaryModel := o.modelForRole("adversary", "")
	for i := range scored {
		if scored[i].DimensionID == "adversary" && scored[i].ProducedByModel == "" {
			scored[i].ProducedByModel = adversaryModel
		}
	}
	o.progress("phase_end", map[string]any{"phase": "synthesis", "deduped": len(deduped), "scored": len(scored)})
	return scored
}
