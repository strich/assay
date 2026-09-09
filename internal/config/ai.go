// Package config resolves assay's configuration from the environment. Every
// env var is read at CALL time (inside the FromEnv / default constructors),
// never at package init, so a t.Setenv in a test is deterministic and no value
// is frozen at import.
//
// One rule holds everywhere: a blank environment variable means unset. It is
// resolved at read, in one helper (strEnv/blank), because a half-blank config
// surface is how two of the original fourteen bugs hid.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// DefaultModel is the model every tier falls back to until the tier-to-model
// assignments are chosen from evidence. It is the fully-qualified opencode
// model string (provider/model), which is what `opencode run -m` resolves.
const DefaultModel = "openrouter/deepseek/deepseek-v4-flash-0731"

// AIIntegrationConfig is the resolved LLM seam configuration.
//
// Three model IDs, not one: budget, mid and premium. The tier is the only
// indirection — a role resolves to a tier, a tier resolves to a model, in one
// function at the single call seam. The assignments themselves are deliberately
// left to configuration: a routine PR on budget models and a release PR on
// frontier models are the same code path with different numbers.
type AIIntegrationConfig struct {
	ModelBudget  string `json:"model_budget"`
	ModelMid     string `json:"model_mid"`
	ModelPremium string `json:"model_premium"`

	SchemaRetries    int `json:"schema_retries"`
	TransientRetries int `json:"transient_retries"`
	TimeoutSeconds   int `json:"timeout_seconds"`

	OpencodeBin string `json:"opencode_bin"`
}

// AIConfigFromEnv resolves the AI integration config from the environment.
// A malformed numeric env value is an error, reported at boot rather than
// silently defaulted.
func AIConfigFromEnv() (AIIntegrationConfig, error) {
	schemaRetries, err := intEnv("ASSAY_SCHEMA_RETRIES", 2)
	if err != nil {
		return AIIntegrationConfig{}, err
	}
	transientRetries, err := intEnv("ASSAY_TRANSIENT_RETRIES", 2)
	if err != nil {
		return AIIntegrationConfig{}, err
	}
	timeout, err := intEnv("ASSAY_LLM_TIMEOUT_SECONDS", 1800)
	if err != nil {
		return AIIntegrationConfig{}, err
	}
	return AIIntegrationConfig{
		ModelBudget:      blankEnv("ASSAY_MODEL_BUDGET", DefaultModel),
		ModelMid:         blankEnv("ASSAY_MODEL_MID", DefaultModel),
		ModelPremium:     blankEnv("ASSAY_MODEL_PREMIUM", DefaultModel),
		SchemaRetries:    schemaRetries,
		TransientRetries: transientRetries,
		TimeoutSeconds:   timeout,
		OpencodeBin:      blankEnv("ASSAY_OPENCODE_BIN", "opencode"),
	}, nil
}

// Models returns the tier -> model map the runner resolves against.
func (c AIIntegrationConfig) Models() map[string]string {
	return map[string]string{
		"budget":  c.ModelBudget,
		"mid":     c.ModelMid,
		"premium": c.ModelPremium,
	}
}

// ProviderEnv builds the subprocess environment forwarded to opencode: the
// credentials that are set, plus an XDG_DATA_HOME that is created if missing.
func (c AIIntegrationConfig) ProviderEnv() map[string]string {
	env := map[string]string{}
	for _, key := range []string{
		"OPENROUTER_API_KEY",
		"ANTHROPIC_API_KEY",
		"OPENAI_API_KEY",
		"GOOGLE_API_KEY",
		"GH_TOKEN",
	} {
		if v := Credential(key); v != "" {
			env[key] = v
		}
	}
	xdg := strings.TrimSpace(os.Getenv("XDG_DATA_HOME"))
	if xdg == "" {
		xdg = filepath.Join(os.TempDir(), "assay-opencode-data")
	}
	_ = os.MkdirAll(xdg, 0o755)
	env["XDG_DATA_HOME"] = xdg
	return env
}

// providerCredential maps an opencode model's provider prefix to the env var
// that carries its key. The prefix is everything before the first "/".
func providerCredential(model string) (provider, envVar string) {
	provider, _, _ = strings.Cut(strings.TrimSpace(model), "/")
	switch provider {
	case "openrouter":
		return provider, "OPENROUTER_API_KEY"
	case "anthropic":
		return provider, "ANTHROPIC_API_KEY"
	case "openai":
		return provider, "OPENAI_API_KEY"
	case "google":
		return provider, "GOOGLE_API_KEY"
	default:
		return provider, ""
	}
}

// Validate rejects configuration values that would silently disable a safety
// mechanism (a zero timeout, a negative retry count, a non-positive concurrency
// ceiling). It runs before any expensive work.
func (c AIIntegrationConfig) Validate() error {
	if c.SchemaRetries < 0 {
		return fmt.Errorf("ASSAY_SCHEMA_RETRIES must be >= 0, got %d", c.SchemaRetries)
	}
	if c.TransientRetries < 0 {
		return fmt.Errorf("ASSAY_TRANSIENT_RETRIES must be >= 0, got %d", c.TransientRetries)
	}
	if c.TimeoutSeconds <= 0 {
		return fmt.Errorf("ASSAY_LLM_TIMEOUT_SECONDS must be > 0, got %d", c.TimeoutSeconds)
	}
	return nil
}

// CheckModelCredentials verifies that the key for every configured tier's
// provider is present and non-blank, naming the first missing one. It runs
// before any expensive work so a missing credential fails in seconds, not
// after a multi-minute clone.
func (c AIIntegrationConfig) CheckModelCredentials() error {
	seen := map[string]bool{}
	for tier, model := range c.Models() {
		provider, envVar := providerCredential(model)
		if envVar == "" || seen[provider] {
			continue
		}
		seen[provider] = true
		if Credential(envVar) == "" {
			return fmt.Errorf("model tier %q uses provider %q but %s is not set", tier, provider, envVar)
		}
	}
	return nil
}

// Credential returns the environment value for key with surrounding whitespace
// trimmed. Secrets pass through several hands — a GitHub Actions secret, a
// compose environment entry, a .env file — and each stores the value byte for
// byte; a key pasted with a trailing newline is forwarded verbatim, so the
// provider receives a header it cannot match. Trimming is safe: no provider
// issues a key whose value depends on surrounding whitespace. Read credentials
// through this rather than os.Getenv so the trim cannot be missed at a seam.
func Credential(key string) string {
	return strings.TrimSpace(os.Getenv(key))
}

// blankEnv returns the env value for key, or def when the key is unset OR
// blank. Blank means unset, everywhere, at read time.
func blankEnv(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// strEnv returns the env value for key, or def when the key is unset. A key
// that is set (even to "") returns its value, matching os.getenv(key, def).
func strEnv(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

// intEnv parses key as an int, falling back to def when unset or blank. A
// set-but-malformed value is an error.
func intEnv(key string, def int) (int, error) {
	v := strings.TrimSpace(strEnv(key, ""))
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("invalid literal for int() with base 10: '%s'", v)
	}
	return n, nil
}

// floatEnv parses key as a float64, falling back to def when unset or blank. A
// set-but-malformed value is an error.
func floatEnv(key string, def float64) (float64, error) {
	v := strings.TrimSpace(strEnv(key, ""))
	if v == "" {
		return def, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, fmt.Errorf("could not convert string to float: '%s'", v)
	}
	return f, nil
}
