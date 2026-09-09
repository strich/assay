package config

import "strings"

// CommentConfig ports config.py CommentConfig — comment formatting and posting
// preferences. PostWorthinessGate is env-driven (default OFF); every other
// default is a plain constant.
type CommentConfig struct {
	MinSeverity                 string `json:"min_severity"` // Minimum severity to include
	MaxComments                 int    `json:"max_comments"` // Cap inline comments
	IncludeSuggestions          bool   `json:"include_suggestions"`
	IncludeDimensionAttribution bool   `json:"include_dimension_attribution"`
	IncludeConfidence           bool   `json:"include_confidence"`
	SuggestionMode              string `json:"suggestion_mode"` // comment | code

	// PostWorthinessGate: an experienced-reviewer precision pass. DEFAULT OFF;
	// enabled by ASSAY_POSTWORTHINESS_GATE in {"1","true","yes"}.
	PostWorthinessGate bool `json:"post_worthiness_gate"`

	SeverityEmojis map[string]string `json:"severity_emojis"`
}

// DefaultCommentConfig builds the comment defaults, reading
// ASSAY_POSTWORTHINESS_GATE at call time.
func DefaultCommentConfig() CommentConfig {
	return CommentConfig{
		MinSeverity:                 "nitpick",
		MaxComments:                 25,
		IncludeSuggestions:          true,
		IncludeDimensionAttribution: true,
		IncludeConfidence:           true,
		SuggestionMode:              "comment",
		PostWorthinessGate:          postWorthinessDefault(),
		SeverityEmojis: map[string]string{
			"critical":   "🔴",
			"important":  "🟠",
			"suggestion": "🔵",
			"nitpick":    "⚪",
		},
	}
}

// postWorthinessDefault ports the ASSAY_POSTWORTHINESS_GATE default_factory
// (default OFF, enabled only by "1"/"true"/"yes").
func postWorthinessDefault() bool {
	switch strings.ToLower(strEnv("ASSAY_POSTWORTHINESS_GATE", "")) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}
