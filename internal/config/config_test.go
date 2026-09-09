package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/BrightrockGames/assay/internal/schemas"
)

// configEnvKeys is every env var this package reads. clearConfigEnv unsets them
// all (and restores on cleanup) so a table-driven env test starts from a known
// blank slate — t.Setenv can only set, not unset, and strEnv/LookupEnv treat a
// present-but-empty value differently from an absent one.
var configEnvKeys = []string{
	"ASSAY_MODEL_BUDGET", "ASSAY_MODEL_MID", "ASSAY_MODEL_PREMIUM",
	"ASSAY_SCHEMA_RETRIES", "ASSAY_TRANSIENT_RETRIES",
	"ASSAY_LLM_TIMEOUT_SECONDS", "ASSAY_OPENCODE_BIN",
	"ASSAY_MAX_COST_USD", "ASSAY_MAX_DURATION_SECONDS",
	"ASSAY_EVIDENCE_PACK", "ASSAY_POSTWORTHINESS_GATE",
	"OPENROUTER_API_KEY", "ANTHROPIC_API_KEY", "OPENAI_API_KEY",
	"GOOGLE_API_KEY", "GH_TOKEN", "XDG_DATA_HOME",
}

func clearConfigEnv(t *testing.T) {
	t.Helper()
	for _, k := range configEnvKeys {
		k := k
		if prev, ok := os.LookupEnv(k); ok {
			t.Cleanup(func() { _ = os.Setenv(k, prev) })
		} else {
			t.Cleanup(func() { _ = os.Unsetenv(k) })
		}
		_ = os.Unsetenv(k)
	}
}

func ptrF(f float64) *float64 { return &f }
func ptrI(i int) *int         { return &i }

// ---------------------------------------------------------------------------
// V7 — Budget cap resolution: explicit arg wins > env > defaults (2.0/300).
// ---------------------------------------------------------------------------

// mustAIConfig / mustFromInput unwrap the error-returning constructors for
// tests whose env is known-wellformed.
func mustAIConfig(t *testing.T) AIIntegrationConfig {
	t.Helper()
	c, err := AIConfigFromEnv()
	if err != nil {
		t.Fatalf("AIConfigFromEnv: %v", err)
	}
	return c
}

func mustFromInput(t *testing.T, in schemas.ReviewInput) ReviewConfig {
	t.Helper()
	c, err := ReviewConfig{}.FromInput(in)
	if err != nil {
		t.Fatalf("FromInput: %v", err)
	}
	return c
}

func TestResolveBudgetCapsCascade(t *testing.T) {
	cases := []struct {
		name     string
		argCost  *float64
		argDur   *int
		envCost  string // "" means leave unset
		envDur   string
		wantCost float64
		wantDur  int
	}{
		{"defaults when nil and no env", nil, nil, "", "", 2.0, 3600},
		{"env used when arg nil", nil, nil, "3.3", "450", 3.3, 450},
		{"explicit arg wins over env", ptrF(5.0), ptrI(120), "3.3", "450", 5.0, 120},
		{"explicit arg wins over defaults", ptrF(1.25), ptrI(90), "", "", 1.25, 90},
		{"mixed: explicit cost, env duration", ptrF(9.0), nil, "3.3", "450", 9.0, 450},
		{"mixed: env cost, explicit duration", nil, ptrI(77), "3.3", "450", 3.3, 77},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clearConfigEnv(t)
			if c.envCost != "" {
				t.Setenv("ASSAY_MAX_COST_USD", c.envCost)
			}
			if c.envDur != "" {
				t.Setenv("ASSAY_MAX_DURATION_SECONDS", c.envDur)
			}
			gotCost, gotDur, err := ResolveBudgetCaps(c.argCost, c.argDur)
			if err != nil {
				t.Fatalf("ResolveBudgetCaps: %v", err)
			}
			if gotCost != c.wantCost || gotDur != c.wantDur {
				t.Errorf("ResolveBudgetCaps = (%v, %d), want (%v, %d)", gotCost, gotDur, c.wantCost, c.wantDur)
			}
		})
	}

	// Malformed env raises, exactly like Python's float()/int() inside
	// _resolve_budget_caps (ValueError -> HTTP 400 at the node layer), with
	// Python's message shape. It must NOT silently fall back to the default.
	t.Run("unparseable env is an error with the Python message", func(t *testing.T) {
		clearConfigEnv(t)
		t.Setenv("ASSAY_MAX_COST_USD", "abc")
		_, _, err := ResolveBudgetCaps(nil, nil)
		if err == nil || err.Error() != "could not convert string to float: 'abc'" {
			t.Fatalf("cost err = %v, want Python float() message", err)
		}
		clearConfigEnv(t)
		t.Setenv("ASSAY_MAX_DURATION_SECONDS", "xyz")
		_, _, err = ResolveBudgetCaps(nil, nil)
		if err == nil || err.Error() != "invalid literal for int() with base 10: 'xyz'" {
			t.Fatalf("duration err = %v, want Python int() message", err)
		}
		// And the boot-time path: a malformed ASSAY_SCHEMA_RETRIES fails
		// AIConfigFromEnv (Python crashes at import).
		clearConfigEnv(t)
		t.Setenv("ASSAY_SCHEMA_RETRIES", "many")
		if _, err := AIConfigFromEnv(); err == nil {
			t.Fatal("AIConfigFromEnv with ASSAY_SCHEMA_RETRIES=many: want error, got nil")
		}
	})
}

// ---------------------------------------------------------------------------
// AIIntegrationConfig env cascade + provider_env.
// ---------------------------------------------------------------------------

func TestAIConfigFromEnvDefaults(t *testing.T) {
	clearConfigEnv(t)
	c := mustAIConfig(t)
	for tier, got := range c.Models() {
		if got != DefaultModel {
			t.Errorf("Models()[%q] = %q, want %q", tier, got, DefaultModel)
		}
	}
	if c.SchemaRetries != 2 || c.TransientRetries != 2 {
		t.Errorf("retries = %d/%d, want 2/2", c.SchemaRetries, c.TransientRetries)
	}
	if c.TimeoutSeconds != 1800 {
		t.Errorf("TimeoutSeconds = %d, want 1800", c.TimeoutSeconds)
	}
	if c.OpencodeBin != "opencode" {
		t.Errorf("OpencodeBin = %q, want opencode", c.OpencodeBin)
	}
}

func TestAIConfigFromEnvOverrides(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("ASSAY_MODEL_BUDGET", "openrouter/cheap/model")
	t.Setenv("ASSAY_MODEL_MID", "openrouter/mid/model")
	t.Setenv("ASSAY_MODEL_PREMIUM", "anthropic/premium/model")
	t.Setenv("ASSAY_SCHEMA_RETRIES", "4")
	t.Setenv("ASSAY_TRANSIENT_RETRIES", "5")
	t.Setenv("ASSAY_LLM_TIMEOUT_SECONDS", "900")
	t.Setenv("ASSAY_OPENCODE_BIN", "/usr/bin/opencode")

	c := mustAIConfig(t)
	if c.ModelBudget != "openrouter/cheap/model" || c.ModelMid != "openrouter/mid/model" ||
		c.ModelPremium != "anthropic/premium/model" {
		t.Errorf("models = %q/%q/%q", c.ModelBudget, c.ModelMid, c.ModelPremium)
	}
	if c.SchemaRetries != 4 || c.TransientRetries != 5 {
		t.Errorf("retries = %d/%d, want 4/5", c.SchemaRetries, c.TransientRetries)
	}
	if c.TimeoutSeconds != 900 {
		t.Errorf("TimeoutSeconds = %d, want 900", c.TimeoutSeconds)
	}
	if c.OpencodeBin != "/usr/bin/opencode" {
		t.Errorf("OpencodeBin = %q", c.OpencodeBin)
	}
}

// Blank means unset: a set-but-empty model falls back to the default rather
// than configuring an empty model string.
func TestAIConfigFromEnvBlankModelFallsBack(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("ASSAY_MODEL_BUDGET", "   ")
	if got := mustAIConfig(t).ModelBudget; got != DefaultModel {
		t.Errorf("ModelBudget = %q, want the default for a blank value", got)
	}
}

func TestAIConfigMalformedNumericIsAnError(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("ASSAY_SCHEMA_RETRIES", "many")
	if _, err := AIConfigFromEnv(); err == nil {
		t.Fatal("want an error for a malformed ASSAY_SCHEMA_RETRIES")
	}
}

func TestCheckModelCredentials(t *testing.T) {
	clearConfigEnv(t)
	c := mustAIConfig(t)
	t.Setenv("OPENROUTER_API_KEY", "k")
	if err := c.CheckModelCredentials(); err != nil {
		t.Errorf("with the key set: %v", err)
	}
	_ = os.Unsetenv("OPENROUTER_API_KEY")
	if err := c.CheckModelCredentials(); err == nil {
		t.Error("want an error when the provider key is missing")
	}
}

func TestProviderEnv(t *testing.T) {
	clearConfigEnv(t)
	xdg := t.TempDir()
	t.Setenv("OPENROUTER_API_KEY", "or-key")
	t.Setenv("GH_TOKEN", "gh-tok")
	t.Setenv("XDG_DATA_HOME", xdg)

	env := mustAIConfig(t).ProviderEnv()
	if env["OPENROUTER_API_KEY"] != "or-key" {
		t.Errorf("OPENROUTER_API_KEY = %q", env["OPENROUTER_API_KEY"])
	}
	if env["GH_TOKEN"] != "gh-tok" {
		t.Errorf("GH_TOKEN = %q", env["GH_TOKEN"])
	}
	// Unset credentials must not be forwarded.
	if _, ok := env["ANTHROPIC_API_KEY"]; ok {
		t.Errorf("ANTHROPIC_API_KEY should be absent")
	}
	if env["XDG_DATA_HOME"] != xdg {
		t.Errorf("XDG_DATA_HOME = %q, want %q", env["XDG_DATA_HOME"], xdg)
	}

	// With XDG_DATA_HOME unset, ProviderEnv falls back to a tmp dir and creates
	// it.
	_ = os.Unsetenv("XDG_DATA_HOME")
	env2 := mustAIConfig(t).ProviderEnv()
	wantXDG := filepath.Join(os.TempDir(), "assay-opencode-data")
	if env2["XDG_DATA_HOME"] != wantXDG {
		t.Errorf("fallback XDG_DATA_HOME = %q, want %q", env2["XDG_DATA_HOME"], wantXDG)
	}
	if st, err := os.Stat(wantXDG); err != nil || !st.IsDir() {
		t.Errorf("fallback XDG dir not created: %v", err)
	}
}

// ---------------------------------------------------------------------------
// BudgetConfig / evidence-pack + numeric table.
// ---------------------------------------------------------------------------

func TestDefaultBudgetConfig(t *testing.T) {
	clearConfigEnv(t)
	b := DefaultBudgetConfig()
	if b.MaxCostUSD != 2.0 || b.MaxDurationSeconds != 3600 {
		t.Errorf("caps = %v/%d, want 2.0/3600", b.MaxCostUSD, b.MaxDurationSeconds)
	}
	if b.MaxConcurrentReviewers != 8 ||
		b.MaxChildSpawnsPerReviewer != 2 || b.MaxCrossRefDeepDives != 5 ||
		b.MaxCoverageIterations != 2 || b.MaxReviewDepth != 2 {
		t.Errorf("loop caps mismatch: %+v", b)
	}
	if !b.EvidencePackReviewers {
		t.Errorf("EvidencePackReviewers = false, want true (default ON)")
	}
}

func TestEvidencePackToggle(t *testing.T) {
	for _, tc := range []struct {
		val  string
		want bool
	}{
		{"0", false}, {"false", false}, {"no", false}, {"FALSE", false},
		{"1", true}, {"true", true}, {"yes", true}, {"anything", true},
	} {
		t.Run(tc.val, func(t *testing.T) {
			clearConfigEnv(t)
			t.Setenv("ASSAY_EVIDENCE_PACK", tc.val)
			if got := DefaultBudgetConfig().EvidencePackReviewers; got != tc.want {
				t.Errorf("ASSAY_EVIDENCE_PACK=%q -> %v, want %v", tc.val, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// CommentConfig / post-worthiness gate.
// ---------------------------------------------------------------------------

func TestDefaultCommentConfig(t *testing.T) {
	clearConfigEnv(t)
	c := DefaultCommentConfig()
	if c.MinSeverity != "nitpick" || c.MaxComments != 25 {
		t.Errorf("MinSeverity/MaxComments = %q/%d", c.MinSeverity, c.MaxComments)
	}
	if !c.IncludeSuggestions || !c.IncludeDimensionAttribution || !c.IncludeConfidence {
		t.Errorf("include flags = %+v, want all true", c)
	}
	if c.SuggestionMode != "comment" {
		t.Errorf("suggestion = %+v", c)
	}
	if c.PostWorthinessGate {
		t.Errorf("PostWorthinessGate = true, want false (default OFF)")
	}
	if c.SeverityEmojis["critical"] != "🔴" || c.SeverityEmojis["nitpick"] != "⚪" {
		t.Errorf("SeverityEmojis = %v", c.SeverityEmojis)
	}
}

func TestPostWorthinessToggle(t *testing.T) {
	for _, tc := range []struct {
		val  string
		want bool
	}{
		{"1", true}, {"true", true}, {"yes", true}, {"YES", true},
		{"0", false}, {"false", false}, {"", false}, {"maybe", false},
	} {
		t.Run("v_"+tc.val, func(t *testing.T) {
			clearConfigEnv(t)
			t.Setenv("ASSAY_POSTWORTHINESS_GATE", tc.val)
			if got := DefaultCommentConfig().PostWorthinessGate; got != tc.want {
				t.Errorf("ASSAY_POSTWORTHINESS_GATE=%q -> %v, want %v", tc.val, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Depth profiles + thresholds (static).
// ---------------------------------------------------------------------------

func TestDepthProfiles(t *testing.T) {
	want := map[string]DepthProfile{
		"quick":    {MaxDimensions: 3, ModelTier: "budget"},
		"standard": {MaxDimensions: 6, ModelTier: "mid"},
		"deep":     {MaxDimensions: 12, ModelTier: "premium"},
	}
	if !reflect.DeepEqual(DepthProfiles, want) {
		t.Errorf("DepthProfiles = %v, want %v", DepthProfiles, want)
	}
	wantThresholds := []AutoDepthThreshold{{100, "quick"}, {500, "standard"}}
	if !reflect.DeepEqual(AutoDepthThresholds, wantThresholds) {
		t.Errorf("AutoDepthThresholds = %v, want %v", AutoDepthThresholds, wantThresholds)
	}
}

// ---------------------------------------------------------------------------
// ReviewConfig.FromInput merge semantics.
// ---------------------------------------------------------------------------

func TestDefaultReviewConfigIgnorePaths(t *testing.T) {
	clearConfigEnv(t)
	c := DefaultReviewConfig()
	if len(c.IgnorePaths) != 11 {
		t.Errorf("IgnorePaths len = %d, want 11", len(c.IgnorePaths))
	}
	if c.Hints == nil {
		t.Errorf("Hints = nil, want non-nil empty")
	}
}

func TestFromInputBudgetCapsResolvedToPerCallDefault(t *testing.T) {
	// Key parity check: with no explicit caps and no env, FromInput must set the
	// per-call duration to 3600 via the ResolveBudgetCaps cascade — the same
	// resolved value Python's review() always writes over the BudgetConfig
	// default.
	clearConfigEnv(t)
	c := mustFromInput(t, schemas.ReviewInput{MaxReviewDepth: 2})
	if c.Budget.MaxCostUSD != 2.0 {
		t.Errorf("MaxCostUSD = %v, want 2.0", c.Budget.MaxCostUSD)
	}
	if c.Budget.MaxDurationSeconds != 3600 {
		t.Errorf("MaxDurationSeconds = %d, want 3600 (per-call resolved default)", c.Budget.MaxDurationSeconds)
	}
}

func TestFromInputMergeSemantics(t *testing.T) {
	clearConfigEnv(t)
	in := schemas.ReviewInput{
		MaxCostUSD:             ptrF(7.5),
		MaxDurationSeconds:     ptrI(150),
		MaxConcurrentReviewers: ptrI(4),
		MaxCoverageIterations:  ptrI(5),
		MaxReviewDepth:         10, // clamps to 3
		Models:                 map[string]string{"reviewer": "anthropic/claude-x", "bogus_field": "ignored"},
		IgnorePaths:            []string{"custom/**", "*.md"}, // *.md dups a default
		Hints:                  []string{"be strict", "check nil derefs"},
		SuggestionMode:         "code",
	}
	c := mustFromInput(t, in)

	if c.Budget.MaxCostUSD != 7.5 || c.Budget.MaxDurationSeconds != 150 {
		t.Errorf("explicit caps = %v/%d, want 7.5/150", c.Budget.MaxCostUSD, c.Budget.MaxDurationSeconds)
	}
	if c.Budget.MaxConcurrentReviewers != 4 {
		t.Errorf("MaxConcurrentReviewers = %d, want 4", c.Budget.MaxConcurrentReviewers)
	}
	if c.Budget.MaxCoverageIterations != 5 {
		t.Errorf("MaxCoverageIterations = %d, want 5", c.Budget.MaxCoverageIterations)
	}
	if c.Budget.MaxReviewDepth != 3 {
		t.Errorf("MaxReviewDepth = %d, want 3 (clamped from 10)", c.Budget.MaxReviewDepth)
	}
	if c.Model.Reviewer != "anthropic/claude-x" {
		t.Errorf("Model.Reviewer = %q, want anthropic/claude-x", c.Model.Reviewer)
	}
	// Unknown model key ignored; other fields keep defaults.
	if c.Model.Planner != "premium" {
		t.Errorf("Model.Planner = %q, want premium (unchanged)", c.Model.Planner)
	}
	// ignore_paths union deduped: *.md appears once, custom/** present.
	if countOf(c.IgnorePaths, "*.md") != 1 {
		t.Errorf("*.md count = %d, want 1 (deduped)", countOf(c.IgnorePaths, "*.md"))
	}
	if countOf(c.IgnorePaths, "custom/**") != 1 {
		t.Errorf("custom/** missing from union: %v", c.IgnorePaths)
	}
	if len(c.IgnorePaths) != 12 { // 11 defaults + custom/** (*.md deduped)
		t.Errorf("IgnorePaths len = %d, want 12", len(c.IgnorePaths))
	}
	if !reflect.DeepEqual(c.Hints, []string{"be strict", "check nil derefs"}) {
		t.Errorf("Hints = %v", c.Hints)
	}
	if c.Comments.SuggestionMode != "code" {
		t.Errorf("SuggestionMode = %q, want code", c.Comments.SuggestionMode)
	}
}

func TestFromInputReviewDepthClampVariants(t *testing.T) {
	clearConfigEnv(t)
	for _, tc := range []struct{ in, want int }{
		{1, 1}, {2, 2}, {3, 3}, {4, 3}, {99, 3},
	} {
		c := mustFromInput(t, schemas.ReviewInput{MaxReviewDepth: tc.in})
		if c.Budget.MaxReviewDepth != tc.want {
			t.Errorf("FromInput MaxReviewDepth(%d) = %d, want %d", tc.in, c.Budget.MaxReviewDepth, tc.want)
		}
	}
}

func TestFromInputEmptyOverridesKeepDefaults(t *testing.T) {
	clearConfigEnv(t)
	// Empty hints / suggestion_mode leave the defaults intact.
	c := mustFromInput(t, schemas.ReviewInput{MaxReviewDepth: 2, Hints: nil, SuggestionMode: ""})
	if len(c.Hints) != 0 {
		t.Errorf("Hints = %v, want empty (unchanged)", c.Hints)
	}
	if c.Comments.SuggestionMode != "comment" {
		t.Errorf("SuggestionMode = %q, want comment (unchanged)", c.Comments.SuggestionMode)
	}
	if len(c.IgnorePaths) != 11 {
		t.Errorf("IgnorePaths = %d, want 11 (no union)", len(c.IgnorePaths))
	}
}

func countOf(xs []string, v string) int {
	n := 0
	for _, x := range xs {
		if x == v {
			n++
		}
	}
	return n
}
