package config

import "strings"

// BudgetConfig ports BudgetConfig — global budget caps plus the bounded
// fan-out knobs. Numeric defaults are the config.py "Config numeric tables"
// verbatim. max_duration_seconds is overridden per call to the resolved value
// (3600 by default) via ReviewConfig.FromInput.
//
// Only the two global caps are enforced; they are enforced on MEASURED spend
// and wall-clock, and the actuals are always reported.
type BudgetConfig struct {
	MaxCostUSD         float64 `json:"max_cost_usd"`
	MaxDurationSeconds int     `json:"max_duration_seconds"`

	MaxConcurrentReviewers int `json:"max_concurrent_reviewers"`

	MaxChildSpawnsPerReviewer int `json:"max_child_spawns_per_reviewer"`

	MaxCrossRefDeepDives int `json:"max_cross_ref_deep_dives"`

	MaxCoverageIterations int `json:"max_coverage_iterations"`

	MaxReviewDepth int `json:"max_review_depth"`

	// EvidencePackReviewers pre-reads each dimension's target files and injects
	// them so reviewers reason over a primed pack. Default ON: env
	// ASSAY_EVIDENCE_PACK not in {"0","false","no"}.
	EvidencePackReviewers bool `json:"evidence_pack_reviewers"`
}

// DefaultBudgetConfig builds a BudgetConfig with the defaults, reading
// ASSAY_EVIDENCE_PACK at call time.
func DefaultBudgetConfig() BudgetConfig {
	return BudgetConfig{
		MaxCostUSD:                2.0,
		MaxDurationSeconds:        3600,
		MaxConcurrentReviewers:    8,
		MaxChildSpawnsPerReviewer: 2,
		MaxCrossRefDeepDives:      5,
		MaxCoverageIterations:     2,
		MaxReviewDepth:            2,
		EvidencePackReviewers:     evidencePackDefault(),
	}
}

// evidencePackDefault ports the ASSAY_EVIDENCE_PACK default_factory (default ON,
// disabled only by an explicit "0"/"false"/"no").
func evidencePackDefault() bool {
	switch strings.ToLower(strEnv("ASSAY_EVIDENCE_PACK", "1")) {
	case "0", "false", "no":
		return false
	default:
		return true
	}
}

// ResolveBudgetCaps ports app.py _resolve_budget_caps. An explicit argument
// always wins; when nil, the ASSAY_MAX_COST_USD / ASSAY_MAX_DURATION_SECONDS
// env vars are consulted; finally the defaults 2.0 / 3600 (real reviews
// measure 60-70 minutes — the historical 300s default killed every fresh
// install mid-pipeline). A malformed env value is an error (Python's
// float()/int() raises inside review(), where the ValueError class maps to
// HTTP 400).
func ResolveBudgetCaps(maxCostUSD *float64, maxDurationSeconds *int) (float64, int, error) {
	cost := 2.0
	if maxCostUSD != nil {
		cost = *maxCostUSD
	} else {
		var err error
		if cost, err = floatEnv("ASSAY_MAX_COST_USD", 2.0); err != nil {
			return 0, 0, err
		}
	}

	dur := 3600
	if maxDurationSeconds != nil {
		dur = *maxDurationSeconds
	} else {
		var err error
		if dur, err = intEnv("ASSAY_MAX_DURATION_SECONDS", 3600); err != nil {
			return 0, 0, err
		}
	}
	return cost, dur, nil
}
