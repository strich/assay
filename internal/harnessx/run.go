// Package harnessx is assay's single LLM seam. Every reasoner reaches a model
// through Run[T] or Runner.Run; there is no second path. That matters because
// the previous design had two — an agent harness and an LLM chat call — each
// with its own model-string convention, and the mismatch between them sent an
// OpenRouter key to the wrong provider. One seam removes the bug class.
//
// The seam is opencode, invoked directly as `opencode run --format json`. Assay
// owns the invocation, so it sees the child's stdout and stderr verbatim, the
// per-step cost and token counts opencode reports, and the raw output of every
// failed schema attempt. Structured output uses opencode's written-file
// contract (not native tool calling), validated against the caller's JSON
// schema with a bounded retry.
package harnessx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BrightrockGames/assay/internal/budget"
)

// Caller is the one method every reasoner needs. Runner implements it; tests
// supply fakes.
type Caller interface {
	Run(ctx context.Context, prompt string, schema map[string]any, dest any, opts Options) (*Result, error)
}

// ModelResolver is implemented by callers that can name the model a role or
// tier resolves to. The orchestrator uses it to stamp findings with the model
// that produced and confirmed them, so the tier mix is falsifiable.
type ModelResolver interface {
	ModelFor(opts Options) (tier, model string, err error)
}

// Options are the per-call knobs. Role names the logical role whose tier picks
// the model (see Runner.ModelFor); Tier, when set, overrides the role's tier
// (the depth profile uses this to make the reviewer the cost knob).
type Options struct {
	Role         string
	Tier         string
	SystemPrompt string
	Cwd          string
	Timeout      time.Duration
}

// FailureType classifies how an invocation failed.
type FailureType string

const (
	FailureNone     FailureType = ""
	FailureCrash    FailureType = "crash"
	FailureTimeout  FailureType = "timeout"
	FailureSchema   FailureType = "schema"
	FailureNoOutput FailureType = "no_output"
)

// Attempt is the verbatim record of one opencode invocation. Every schema
// retry keeps its own Attempt so a failed run can be diagnosed from the result
// alone — the opacity that cost a week came from discarding exactly this.
type Attempt struct {
	Prompt       string        `json:"prompt,omitempty"`
	Result       string        `json:"result"`
	OutputFile   string        `json:"output_file,omitempty"`
	Stdout       string        `json:"stdout,omitempty"`
	Stderr       string        `json:"stderr,omitempty"`
	ExitCode     int           `json:"exit_code"`
	ErrorMessage string        `json:"error_message,omitempty"`
	CostUSD      *float64      `json:"cost_usd,omitempty"`
	Tokens       budget.Tokens `json:"tokens"`
	DurationMS   int           `json:"duration_ms"`
}

// Result is the outcome of one logical call, including retries. It carries
// enough to apply a role-specific fallback (IsError, ErrorMessage) and enough
// to diagnose the failure (Stdout, Stderr, Attempts) without re-running.
type Result struct {
	Result       string
	Parsed       any
	IsError      bool
	ErrorMessage string
	FailureType  FailureType
	Model        string
	CostUSD      *float64
	Tokens       budget.Tokens
	NumTurns     int
	DurationMS   int
	Stdout       string
	Stderr       string
	Attempts     []Attempt
}

// CallStat is one invocation's measured outcome, reported to the optional
// OnCall hook so the CLI can stream spend as NDJSON.
type CallStat struct {
	Role         string
	Tier         string
	Model        string
	CostUSD      *float64
	Tokens       budget.Tokens
	DurationMS   int
	NumTurns     int
	IsError      bool
	ErrorMessage string
	Attempt      int
}

// Runner invokes opencode. Construct it once per process with NewRunner.
type Runner struct {
	// Bin is the opencode executable (ASSAY_OPENCODE_BIN, default "opencode").
	Bin string

	// Models maps tier -> fully-qualified opencode model string, e.g.
	// "openrouter/deepseek/deepseek-v4-flash-0731". The tier is the only
	// indirection; opencode resolves the provider prefix.
	Models map[string]string

	// RoleTiers maps a role name (reviewer, planner, adversary, ...) to a tier.
	RoleTiers map[string]string

	// Timeout is the default per-invocation subprocess timeout. Every
	// subprocess has an explicit, configurable timeout; this one defaults
	// generous because a reviewer reading a monorepo is not a hang.
	Timeout time.Duration

	// SchemaRetries is the number of follow-up attempts after the first
	// schema-invalid output (0 means one attempt total).
	SchemaRetries int

	// TransientRetries is the number of retries for transient provider errors
	// (rate limits, 5xx, timeouts) within a single attempt.
	TransientRetries int

	// Env is merged over the process environment for the child.
	Env map[string]string

	// Accountant receives measured cost/tokens for every invocation. May be nil.
	Accountant *budget.Accountant

	// OnCall, when set, is invoked after every opencode process exits. May be nil.
	OnCall func(CallStat)

	// Logger receives diagnostic lines. Defaults to a discarding logger.
	Logger *log.Logger

	// execCommand is the subprocess seam. Tests replace it; production uses
	// execWithTimeout.
	execCommand func(ctx context.Context, bin string, args []string, env []string, dir string, stdin []byte, timeout time.Duration) (stdout, stderr string, exitCode int, err error)

	// xdgDataHome isolates opencode state when set.
	xdgDataHome string
}

// NewRunner builds a Runner with production defaults.
func NewRunner(bin string, models, roleTiers map[string]string, env map[string]string) *Runner {
	if bin == "" {
		bin = "opencode"
	}
	if models == nil {
		models = map[string]string{}
	}
	if roleTiers == nil {
		roleTiers = map[string]string{}
	}
	return &Runner{
		Bin:              bin,
		Models:           models,
		RoleTiers:        roleTiers,
		Timeout:          30 * time.Minute,
		SchemaRetries:    2,
		TransientRetries: 2,
		Env:              env,
		Logger:           log.New(os.Stderr, "[assay:llm] ", log.LstdFlags),
		execCommand:      execWithTimeout,
	}
}

// ModelFor resolves the model string for a call. Tier wins over role; a role
// with no mapping falls back to mid. "standard" is accepted as an alias for
// "mid" so depth profiles written before the tier names settled keep working.
func (r *Runner) ModelFor(opts Options) (tier, model string, err error) {
	tier = strings.ToLower(strings.TrimSpace(opts.Tier))
	if tier == "" {
		tier = strings.ToLower(strings.TrimSpace(r.RoleTiers[opts.Role]))
	}
	if tier == "" {
		tier = "mid"
	}
	if tier == "standard" {
		tier = "mid"
	}
	model = strings.TrimSpace(r.Models[tier])
	if model == "" {
		return tier, "", fmt.Errorf("harnessx: no model configured for tier %q (set ASSAY_MODEL_%s)", tier, strings.ToUpper(tier))
	}
	return tier, model, nil
}

// Run implements Caller. It resolves the model, builds the prompt (including
// the structured-output instructions when schema is non-nil), invokes opencode
// with bounded schema retries, validates the written output, and returns a
// Result that preserves every attempt's raw output.
func (r *Runner) Run(ctx context.Context, prompt string, schema map[string]any, dest any, opts Options) (*Result, error) {
	tier, model, err := r.ModelFor(opts)
	if err != nil {
		return nil, err
	}

	outputDir, cleanup, err := r.outputDir(opts.Cwd)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	effectivePrompt := r.buildPrompt(prompt, schema, outputDir, opts)

	res := &Result{Model: model}
	started := time.Now()

	var lastRaw rawAttempt
	for attempt := 0; attempt <= r.SchemaRetries; attempt++ {
		if attempt > 0 {
			// Rebuild the prompt for the retry: a fresh task prompt when the
			// child crashed before writing anything, otherwise a targeted
			// follow-up naming what was wrong with the output file.
			effectivePrompt = r.buildRetryPrompt(prompt, schema, outputDir, lastRaw, opts)
		}

		raw, execErr := r.invoke(ctx, tier, model, effectivePrompt, opts)
		raw.DurationMS = int(time.Since(started).Milliseconds())
		raw.Prompt = effectivePrompt
		raw.Attempt.Prompt = effectivePrompt
		raw.Attempt.OutputFile = readOutputFile(outputDir)
		res.Stdout = raw.Stdout
		res.Stderr = raw.Stderr
		res.NumTurns += raw.NumTurns
		res.DurationMS = raw.DurationMS
		res.CostUSD = addCost(res.CostUSD, raw.CostUSD)
		res.Tokens = res.Tokens.Add(raw.Tokens)
		res.Attempts = append(res.Attempts, raw.Attempt)

		if execErr != nil {
			// Subprocess-level failure: binary missing, timeout, non-zero exit
			// with no parseable output. This is a transport failure, not a
			// schema problem — return it as an error so no caller can mistake
			// it for a clean (or degraded) result. The child's stdout/stderr
			// stay on the Result.
			res.IsError = true
			res.FailureType = raw.FailureType
			res.ErrorMessage = execErr.Error()
			if raw.Result != "" {
				res.Result = raw.Result
			}
			return res, execErr
		}

		if raw.IsError {
			// An in-band provider error (model not found, auth, crash) is not a
			// schema problem; retrying the same prompt would just burn budget.
			res.IsError = true
			res.FailureType = raw.FailureType
			res.ErrorMessage = raw.ErrorMessage
			if raw.Result != "" {
				res.Result = raw.Result
			}
			return res, fmt.Errorf("%s", raw.ErrorMessage)
		}

		if schema == nil {
			res.Result = raw.Result
			return res, nil
		}

		_, parseErr := parseAndValidate(outputDir, schema, dest, raw.Result)
		if parseErr == nil {
			res.Result = raw.Result
			res.Parsed = dest
			return res, nil
		}
		r.logf("schema attempt %d/%d failed: %v", attempt+1, r.SchemaRetries+1, parseErr)
		lastRaw = raw
		lastRaw.ErrorMessage = parseErr.Error()
	}

	res.IsError = true
	res.FailureType = FailureSchema
	res.ErrorMessage = fmt.Sprintf("schema validation failed after %d attempt(s); last error: %s",
		r.SchemaRetries+1, lastRaw.ErrorMessage)
	return res, nil
}

// WithTier returns a Caller that pins the tier when the call does not choose
// one explicitly. It is how the orchestrator makes the depth profile — not the
// role table — decide the reviewer's tier.
func WithTier(c Caller, tier string) Caller {
	if c == nil || tier == "" {
		return c
	}
	return tierCaller{Caller: c, tier: tier}
}

type tierCaller struct {
	Caller
	tier string
}

func (t tierCaller) Run(ctx context.Context, prompt string, schema map[string]any, dest any, opts Options) (*Result, error) {
	if opts.Tier == "" {
		opts.Tier = t.tier
	}
	return t.Caller.Run(ctx, prompt, schema, dest, opts)
}

// ModelFor forwards to the wrapped caller with the pinned tier applied.
func (t tierCaller) ModelFor(opts Options) (string, string, error) {
	if r, ok := t.Caller.(ModelResolver); ok {
		if opts.Tier == "" {
			opts.Tier = t.tier
		}
		return r.ModelFor(opts)
	}
	return "", "", fmt.Errorf("harnessx: underlying caller cannot resolve models")
}

// Run is the generic entry point reasoners use. It resolves the JSON schema for
// T (the committed pydantic-generated fixture when registered, invopop
// reflection otherwise) and delegates to the caller.
func Run[T any](ctx context.Context, caller Caller, prompt string, opts Options) (*T, *Result, error) {
	if caller == nil {
		return nil, nil, errors.New("harnessx: caller is nil")
	}
	schema := schemaFor[T]()

	var dest T
	result, err := caller.Run(ctx, prompt, schema, &dest, opts)
	if err != nil {
		return nil, result, err
	}
	if result == nil || result.Parsed == nil {
		seeded := seedDefaults[T]()
		return &seeded, result, nil
	}
	return &dest, result, nil
}

// seedDefaults returns a T seeded with its pydantic-parity defaults.
// Unmarshaling an empty JSON object invokes T's UnmarshalJSON (which seeds
// non-zero defaults) when present, and leaves the Go zero value otherwise.
func seedDefaults[T any]() T {
	var v T
	_ = json.Unmarshal([]byte("{}"), &v)
	return v
}

// buildPrompt assembles the effective prompt: an inlined system prompt (opencode
// has no --system-prompt flag) plus the structured-output instructions.
func (r *Runner) buildPrompt(prompt string, schema map[string]any, outputDir string, opts Options) string {
	p := prompt
	if opts.SystemPrompt != "" {
		p = fmt.Sprintf("SYSTEM INSTRUCTIONS:\n%s\n\n---\n\nUSER REQUEST:\n%s",
			strings.TrimSpace(opts.SystemPrompt), prompt)
	}
	if schema != nil {
		p += buildOutputSuffix(schema, outputDir)
	}
	return p
}

// buildRetryPrompt produces the follow-up prompt for a failed schema attempt.
// The original task is prepended: without session resume, a repair-only prompt
// leaves a fresh opencode process with no idea what it was asked to produce.
func (r *Runner) buildRetryPrompt(original string, schema map[string]any, outputDir string, last rawAttempt, opts Options) string {
	if last.FailureType == FailureCrash {
		return r.buildPrompt(original, schema, outputDir, opts)
	}
	diagnosis := diagnoseOutputFailure(OutputPath(outputDir), schema)
	if last.ErrorMessage != "" {
		diagnosis = last.ErrorMessage
	}
	return r.buildPrompt(original, schema, outputDir, opts) + "\n\n---\n\n" + buildFollowupPrompt(diagnosis, outputDir, schema)
}

// readOutputFile returns the structured-output file's bytes, or "" when it does
// not exist. Called before a retry can overwrite it so the malformed content of
// every attempt survives in Result.Attempts.
func readOutputFile(dir string) string {
	b, err := os.ReadFile(OutputPath(dir))
	if err != nil {
		return ""
	}
	return string(b)
}

// invoke runs opencode and parses its event stream, retrying transient provider
// failures (rate limits, 5xx, timeouts) up to TransientRetries times. Every
// subprocess execution is accounted and reported, including retried ones.
func (r *Runner) invoke(ctx context.Context, tier, model, prompt string, opts Options) (rawAttempt, error) {
	args := []string{"run", "--format", "json"}
	if opts.Cwd != "" {
		args = append(args, "--dir", opts.Cwd)
	}
	if model != "" {
		args = append(args, "-m", model)
	}
	// The prompt always goes over stdin. It keeps a monorepo-sized prompt off
	// the argv limit on every platform and off the cmd.exe shim on Windows.
	stdin := []byte(prompt)

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = r.Timeout
	}

	for try := 0; ; try++ {
		started := time.Now()
		env := r.childEnv()
		stdout, stderr, exitCode, execErr := r.execCommand(ctx, r.Bin, args, env, opts.Cwd, stdin, timeout)

		raw := rawAttempt{Prompt: prompt, Stdout: stdout, Stderr: stderr, ExitCode: exitCode}
		raw.DurationMS = int(time.Since(started).Milliseconds())
		events := parseOpenCodeEvents(stdout)
		text := finalText(events)
		raw.Result = text
		if text == "" && len(events) == 0 {
			// No parseable events: surface raw stdout (older opencode versions).
			raw.Result = strings.TrimSpace(stdout)
		}
		raw.CostUSD = costFromEvents(events)
		raw.Tokens = tokensFromEvents(events)
		raw.NumTurns = turnsFromEvents(events)
		eventErr := eventError(events)

		switch {
		case execErr != nil:
			var timeoutErr *TimeoutError
			if errors.As(execErr, &timeoutErr) {
				raw.FailureType = FailureTimeout
			} else {
				raw.FailureType = FailureCrash
			}
			raw.ErrorMessage = execErr.Error()
			if isExecNotFound(execErr) {
				raw.ErrorMessage = fmt.Sprintf("opencode binary not found at %q: install opencode or set ASSAY_OPENCODE_BIN", r.Bin)
			}
			raw.IsError = true
		case exitCode != 0 && text == "":
			// A non-zero exit with no final message is a failure even when
			// stdout carried log lines: classify on the answer text, not on
			// whether any bytes arrived.
			raw.FailureType = FailureCrash
			raw.IsError = true
			raw.ErrorMessage = stderrError(stripANSI(strings.TrimSpace(stderr)), exitCode)
		case eventErr != "":
			raw.FailureType = FailureCrash
			raw.IsError = true
			raw.ErrorMessage = eventErr
		case text == "" && strings.TrimSpace(stderr) != "" && matchesStderrError(stripANSI(stderr)):
			// opencode sometimes exits 0 on hard failures (model not found,
			// auth); surface stderr instead of reporting an empty result.
			raw.FailureType = FailureCrash
			raw.IsError = true
			raw.ErrorMessage = extractStderrError(stripANSI(strings.TrimSpace(stderr)))
		}
		if raw.NumTurns == 0 && raw.Result != "" {
			raw.NumTurns = 1
		}
		raw.Attempt = raw.toAttempt()

		if r.Accountant != nil {
			r.Accountant.Record(raw.CostUSD, raw.Tokens)
		}
		r.reportCall(tier, model, try, raw, opts.Role)

		if raw.IsError && isTransient(raw.ErrorMessage) && try < r.TransientRetries {
			r.logf("transient failure (retry %d/%d): %s", try+1, r.TransientRetries, raw.ErrorMessage)
			sleepBackoff(ctx, try)
			continue
		}
		if raw.IsError {
			// Transport-level provider failure: returned as a non-nil error so
			// the caller cannot mistake it for a schema problem.
			return raw, fmt.Errorf("%s", raw.ErrorMessage)
		}
		return raw, nil
	}
}

// transientPatterns are substrings that indicate a retryable provider error.
var transientPatterns = []string{
	"rate limit", "rate_limit", "overloaded", "timeout", "timed out",
	"connection reset", "connection refused", "temporarily unavailable",
	"service unavailable", "503", "502", "504", "internal server error", "500",
}

func isTransient(msg string) bool {
	lower := strings.ToLower(msg)
	for _, p := range transientPatterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

// sleepBackoff waits an exponentially increasing delay between transient
// retries, honouring context cancellation.
func sleepBackoff(ctx context.Context, attempt int) {
	delay := time.Duration(500*(1<<attempt)) * time.Millisecond
	if delay > 5*time.Second {
		delay = 5 * time.Second
	}
	t := time.NewTimer(delay)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// logf writes to the configured logger when one is present.
func (r *Runner) logf(format string, args ...any) {
	if r.Logger != nil {
		r.Logger.Printf(format, args...)
	}
}

// outputDir creates the per-call directory the agent writes structured output
// into. It lives under the working directory so the agent's Write tool can
// reach it, falling back to the system temp dir when Cwd is empty or read-only.
func (r *Runner) outputDir(cwd string) (string, func(), error) {
	base := cwd
	if base == "" {
		base = os.TempDir()
	} else {
		base = filepath.Clean(base)
	}
	dir, err := os.MkdirTemp(base, ".assay-out-")
	if err != nil {
		dir, err = os.MkdirTemp("", "assay-out-")
		if err != nil {
			return "", nil, fmt.Errorf("harnessx: create output dir: %w", err)
		}
	}
	return dir, func() { _ = os.RemoveAll(dir) }, nil
}

// childEnv merges the process environment with the runner's overrides and an
// isolated opencode data home when one was configured.
func (r *Runner) childEnv() []string {
	env := os.Environ()
	seen := map[string]int{}
	for i, kv := range env {
		if k, _, ok := strings.Cut(kv, "="); ok {
			seen[k] = i
		}
	}
	set := func(k, v string) {
		if i, ok := seen[k]; ok {
			env[i] = k + "=" + v
			return
		}
		env = append(env, k+"="+v)
	}
	if r.xdgDataHome != "" {
		set("XDG_DATA_HOME", r.xdgDataHome)
	}
	for k, v := range r.Env {
		if v == "" {
			continue
		}
		set(k, v)
	}
	return env
}

// SetXDGDataHome isolates opencode state under dir for the lifetime of the
// runner. Empty disables isolation.
func (r *Runner) SetXDGDataHome(dir string) {
	r.xdgDataHome = dir
}

// reportCall forwards a per-invocation stat to the OnCall hook.
func (r *Runner) reportCall(tier, model string, attempt int, raw rawAttempt, role string) {
	if r.OnCall == nil {
		return
	}
	r.OnCall(CallStat{
		Role:         role,
		Tier:         tier,
		Model:        model,
		CostUSD:      raw.CostUSD,
		Tokens:       raw.Tokens,
		DurationMS:   raw.DurationMS,
		NumTurns:     raw.NumTurns,
		IsError:      raw.IsError,
		ErrorMessage: raw.ErrorMessage,
		Attempt:      attempt,
	})
}

// addCost sums two optional costs, returning nil only when both are nil.
func addCost(a, b *float64) *float64 {
	if b == nil {
		return a
	}
	if a == nil {
		zero := 0.0
		a = &zero
	}
	sum := *a + *b
	return &sum
}

// stderrError renders a non-zero exit with the child's stderr verbatim.
func stderrError(stderr string, exitCode int) string {
	if stderr != "" {
		return fmt.Sprintf("opencode exited with code %d: %s", exitCode, stderr)
	}
	return fmt.Sprintf("opencode exited with code %d and produced no output", exitCode)
}
