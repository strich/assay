// Command assay reviews a pull request: it clones the repo, runs the seven-stage
// review pipeline, verifies findings adversarially, and posts what survives.
//
// One process, one language, one command:
//
//	assay --pr https://github.com/owner/repo/pull/123
//	assay --repo . --base-ref main --head-ref HEAD --dry-run
//	assay --diff changes.diff --dry-run
//
// Progress is newline-delimited JSON on stdout; CI tails the process. The final
// event carries the full ReviewResult. Exit codes: 0 success, 2 usage or bad
// input, 3 configuration or credential failure, 4 review failed.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/BrightrockGames/assay/internal/budget"
	"github.com/BrightrockGames/assay/internal/capability"
	"github.com/BrightrockGames/assay/internal/config"
	"github.com/BrightrockGames/assay/internal/github"
	"github.com/BrightrockGames/assay/internal/harnessx"
	"github.com/BrightrockGames/assay/internal/ndjson"
	"github.com/BrightrockGames/assay/internal/orch"
	"github.com/BrightrockGames/assay/internal/schemas"
)

const version = "0.1.0"

// Exit codes. The taxonomy is small and stable so a workflow can branch on it.
const (
	exitOK           = 0
	exitUsage        = 2
	exitConfig       = 3
	exitReviewFailed = 4
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

// options are the CLI's flag surface.
type options struct {
	pr                 string
	diff               string
	repo               string
	baseRef            string
	headRef            string
	depth              string
	maxCostUSD         float64
	maxDurationSeconds int
	maxConcurrent      int
	maxCoverage        int
	maxReviewDepth     int
	ignorePaths        stringList
	hints              stringList
	suggestionMode     string
	dryRun             bool
	output             string
	showVersion        bool
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	opts, err := parseFlags(args, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if opts.showVersion {
		fmt.Fprintf(stdout, "assay %s\n", version)
		return exitOK
	}
	if err := opts.validate(); err != nil {
		fmt.Fprintf(stderr, "assay: %v\n", err)
		return exitUsage
	}

	// Configuration and credentials are checked BEFORE the clone: a wrong or
	// missing credential must fail in seconds with a message naming the cause,
	// never after a four-and-a-half minute checkout.
	aiConf, err := config.AIConfigFromEnv()
	if err != nil {
		fmt.Fprintf(stderr, "assay: invalid configuration: %v\n", err)
		return exitConfig
	}
	if err := checkBinary(aiConf.OpencodeBin); err != nil {
		fmt.Fprintf(stderr, "assay: opencode binary %q not found: install opencode or set ASSAY_OPENCODE_BIN\n", aiConf.OpencodeBin)
		return exitConfig
	}
	if err := aiConf.Validate(); err != nil {
		fmt.Fprintf(stderr, "assay: %v\n", err)
		return exitConfig
	}
	if err := aiConf.CheckModelCredentials(); err != nil {
		fmt.Fprintf(stderr, "assay: %v\n", err)
		return exitConfig
	}
	// Verify each distinct provider once, not just the first map iteration.
	checked := map[string]bool{}
	for _, model := range aiConf.Models() {
		provider, _, _ := strings.Cut(model, "/")
		if checked[provider] {
			continue
		}
		checked[provider] = true
		if err := config.VerifyModelCredential(ctx, model); err != nil {
			fmt.Fprintf(stderr, "assay: %v\n", err)
			return exitConfig
		}
	}
	posting := opts.pr != "" && !opts.dryRun
	if posting {
		if err := github.VerifyToken(ctx, config.Credential("GH_TOKEN")); err != nil {
			fmt.Fprintf(stderr, "assay: %v\n", err)
			return exitConfig
		}
	}

	// Progress goes to stdout as NDJSON. In --output json mode it is discarded
	// so stdout carries exactly one JSON document.
	progressOut := stdout
	if opts.output == "json" {
		progressOut = io.Discard
	}
	reporter := ndjson.New(progressOut)

	accountant := budget.New()

	input := schemas.ReviewInput{
		Depth:          opts.depth,
		DryRun:         opts.dryRun,
		SuggestionMode: opts.suggestionMode,
		IgnorePaths:    opts.ignorePaths,
		Hints:          opts.hints,
		MaxReviewDepth: opts.maxReviewDepth,
	}
	if opts.pr != "" {
		input.PrURL = &opts.pr
	}
	if opts.repo != "" {
		input.RepoPath = &opts.repo
	}
	if opts.baseRef != "" {
		input.BaseRef = &opts.baseRef
	}
	if opts.headRef != "" {
		input.HeadRef = &opts.headRef
	}
	if opts.diff != "" {
		text, err := readDiff(opts.diff)
		if err != nil {
			fmt.Fprintf(stderr, "assay: %v\n", err)
			return exitUsage
		}
		input.DiffText = &text
	}
	if flagChanged(args, "max-cost-usd") {
		input.MaxCostUSD = &opts.maxCostUSD
	}
	if flagChanged(args, "max-duration-seconds") {
		input.MaxDurationSeconds = &opts.maxDurationSeconds
	}
	if flagChanged(args, "max-concurrent-reviewers") {
		input.MaxConcurrentReviewers = &opts.maxConcurrent
	}
	if flagChanged(args, "max-coverage-iterations") {
		input.MaxCoverageIterations = &opts.maxCoverage
	}

	// Resolve the working tree (clone + PR checkout) before the pipeline needs
	// it for evidence extraction. Raw-diff reviews have no working tree.
	if opts.pr != "" || opts.repo != "" {
		resolved, err := orch.ResolveRepo(ctx, strp(input.RepoPath), strp(input.PrURL))
		if err != nil {
			fmt.Fprintf(stderr, "assay: %v\n", err)
			return exitReviewFailed
		}
		input.RepoPath = &resolved
		probe := capability.Probe(resolved)
		reporter.Emit("capabilities", map[string]any{
			"text": probe.Text, "structure": probe.Structure, "symbols": probe.Symbols,
			"diagnostics": probe.Diagnostics, "hot_path": probe.HotPath, "history": probe.History,
		})
	}

	cfg, err := config.ReviewConfig{}.FromInput(input)
	if err != nil {
		fmt.Fprintf(stderr, "assay: invalid configuration: %v\n", err)
		return exitConfig
	}
	if err := validateResolvedConfig(cfg); err != nil {
		fmt.Fprintf(stderr, "assay: %v\n", err)
		return exitConfig
	}

	// The role table comes from the resolved config, so a per-call model
	// override would reach the seam rather than being silently ignored.
	runner := harnessx.NewRunner(aiConf.OpencodeBin, aiConf.Models(), roleTiers(cfg.Model), aiConf.ProviderEnv())
	runner.SchemaRetries = aiConf.SchemaRetries
	runner.TransientRetries = aiConf.TransientRetries
	runner.Timeout = time.Duration(aiConf.TimeoutSeconds) * time.Second
	runner.Accountant = accountant
	runner.OnCall = func(stat harnessx.CallStat) {
		cost := any(nil)
		if stat.CostUSD != nil {
			cost = *stat.CostUSD
		}
		reporter.Emit("llm_call", map[string]any{
			"role": stat.Role, "tier": stat.Tier, "model": stat.Model,
			"cost_usd": cost, "input_tokens": stat.Tokens.Input, "output_tokens": stat.Tokens.Output,
			"duration_ms": stat.DurationMS, "turns": stat.NumTurns,
			"is_error": stat.IsError, "attempt": stat.Attempt,
		})
	}

	deps := orch.Deps{
		LLM:      runner,
		GH:       github.NewClient(""),
		Budget:   accountant,
		Progress: reporter,
	}

	result, err := orch.New(deps, input, cfg).Run(ctx)
	if err != nil {
		// A review that failed late (e.g. the GitHub post failed, or the budget
		// stopped it) still carries a result worth reporting. Emit it first.
		if result.ReviewID != "" {
			if emitErr := emitResult(stdout, reporter, opts, result); emitErr != nil {
				fmt.Fprintf(stderr, "assay: encode result: %v\n", emitErr)
			}
		}
		reporter.Emit("error", map[string]any{"message": err.Error(), "bad_input": errors.Is(err, orch.ErrBadInput)})
		fmt.Fprintf(stderr, "assay: review failed: %v\n", err)
		if errors.Is(err, orch.ErrBadInput) {
			return exitUsage
		}
		return exitReviewFailed
	}

	if err := emitResult(stdout, reporter, opts, result); err != nil {
		fmt.Fprintf(stderr, "assay: encode result: %v\n", err)
		return exitReviewFailed
	}

	spend := accountant.Snapshot()
	fmt.Fprintf(stderr, "assay: %d findings · %d blocking · $%.4f · %s\n",
		result.Summary.TotalFindings, result.Summary.BlockingCount, spend.CostUSD,
		time.Duration(result.Summary.DurationSeconds*float64(time.Second)).Round(time.Second))
	return exitOK
}

// emitResult writes the final ReviewResult: a single JSON document under
// --output json, or a result event on the NDJSON stream otherwise.
func emitResult(stdout io.Writer, reporter *ndjson.Reporter, opts options, result schemas.ReviewResult) error {
	if opts.output == "json" {
		enc := json.NewEncoder(stdout)
		enc.SetEscapeHTML(false)
		return enc.Encode(result)
	}
	reporter.Emit("result", map[string]any{"result": result})
	return nil
}

// parseFlags binds the CLI surface.
func parseFlags(args []string, stderr io.Writer) (options, error) {
	var opts options
	fs := flag.NewFlagSet("assay", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: assay --pr <url> | --repo <path> | --diff <file> [flags]\n\n")
		fs.PrintDefaults()
	}
	fs.StringVar(&opts.pr, "pr", "", "GitHub pull request URL to review")
	fs.StringVar(&opts.diff, "diff", "", "path to a unified diff to review (\"-\" for stdin)")
	fs.StringVar(&opts.repo, "repo", "", "path to a local repository to review")
	fs.StringVar(&opts.baseRef, "base-ref", "", "base ref for a local repository diff")
	fs.StringVar(&opts.headRef, "head-ref", "", "head ref for a local repository diff")
	fs.StringVar(&opts.depth, "depth", "auto", "review depth: auto|quick|standard|deep")
	fs.Float64Var(&opts.maxCostUSD, "max-cost-usd", 0, "cost ceiling in USD (default ASSAY_MAX_COST_USD or 2.0)")
	fs.IntVar(&opts.maxDurationSeconds, "max-duration-seconds", 0, "wall-clock ceiling in seconds (default ASSAY_MAX_DURATION_SECONDS or 3600)")
	fs.IntVar(&opts.maxConcurrent, "max-concurrent-reviewers", 0, "max parallel reviewers (default 8)")
	fs.IntVar(&opts.maxCoverage, "max-coverage-iterations", 0, "max coverage loop iterations (default 2)")
	fs.IntVar(&opts.maxReviewDepth, "max-review-depth", 2, "max sub-review depth (1=flat, 2=one level, 3=max)")
	fs.Var(&opts.ignorePaths, "ignore-path", "glob to exclude from review (repeatable)")
	fs.Var(&opts.hints, "hint", "extra review hint (repeatable)")
	fs.StringVar(&opts.suggestionMode, "suggestion-mode", "comment", "suggestion format: comment|code")
	fs.BoolVar(&opts.dryRun, "dry-run", false, "review without posting to GitHub")
	fs.StringVar(&opts.output, "output", "ndjson", "stdout format: ndjson|json")
	fs.BoolVar(&opts.showVersion, "version", false, "print version and exit")

	if err := fs.Parse(args); err != nil {
		return opts, err
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "assay: unexpected argument %q\n", fs.Arg(0))
		fs.Usage()
		return opts, errors.New("unexpected argument")
	}
	return opts, nil
}

func (o options) validate() error {
	modes := 0
	for _, set := range []bool{o.pr != "", o.diff != "", o.repo != ""} {
		if set {
			modes++
		}
	}
	if modes != 1 {
		return errors.New("exactly one of --pr, --repo or --diff is required")
	}
	switch o.depth {
	case "auto", "quick", "standard", "deep":
	default:
		return fmt.Errorf("invalid --depth %q: want auto|quick|standard|deep", o.depth)
	}
	switch o.output {
	case "ndjson", "json":
	default:
		return fmt.Errorf("invalid --output %q: want ndjson|json", o.output)
	}
	return nil
}

// validateResolvedConfig rejects resolved values that would disable a safety
// mechanism. The flags and env are validated together because FromInput merges
// them: `--max-concurrent-reviewers 0` is as dangerous as the env equivalent.
func validateResolvedConfig(cfg config.ReviewConfig) error {
	b := cfg.Budget
	if b.MaxCostUSD <= 0 || math.IsNaN(b.MaxCostUSD) || math.IsInf(b.MaxCostUSD, 0) {
		return fmt.Errorf("cost ceiling must be a positive, finite number, got %v", b.MaxCostUSD)
	}
	if b.MaxDurationSeconds <= 0 {
		return fmt.Errorf("duration ceiling must be > 0, got %d", b.MaxDurationSeconds)
	}
	if b.MaxConcurrentReviewers <= 0 {
		return fmt.Errorf("max concurrent reviewers must be > 0, got %d", b.MaxConcurrentReviewers)
	}
	if b.MaxCoverageIterations <= 0 {
		return fmt.Errorf("max coverage iterations must be > 0, got %d", b.MaxCoverageIterations)
	}
	if b.MaxReviewDepth < 1 {
		return fmt.Errorf("max review depth must be >= 1, got %d", b.MaxReviewDepth)
	}
	return nil
}

// checkBinary verifies the opencode executable is resolvable before any
// expensive work. An explicit path is stat'd directly (exec.LookPath insists on
// a PATHEXT extension on Windows); a bare name goes through PATH.
func checkBinary(bin string) error {
	if strings.ContainsAny(bin, `/\`) {
		info, err := os.Stat(bin)
		if err != nil {
			return err
		}
		if info.IsDir() {
			return fmt.Errorf("%s is a directory", bin)
		}
		return nil
	}
	_, err := exec.LookPath(bin)
	return err
}

// readDiff reads a unified diff from a file or stdin.
func readDiff(path string) (string, error) {
	if path == "-" {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", fmt.Errorf("read stdin: %w", err)
		}
		return string(b), nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read diff: %w", err)
	}
	return string(b), nil
}

// roleTiers maps the nine roles to the tier their work tolerates.
func roleTiers(m config.ModelConfig) map[string]string {
	return map[string]string{
		"intake_gate":      m.IntakeGate,
		"intake_fallback":  m.IntakeFallback,
		"anatomy_semantic": m.AnatomySemantic,
		"planner":          m.Planner,
		"reviewer":         m.Reviewer,
		"cross_ref":        m.CrossRef,
		"adversary":        m.Adversary,
		"coverage_gate":    m.CoverageGate,
		"dedup_gate":       m.DedupGate,
	}
}

// flagChanged reports whether name was passed on the command line, so an
// unset flag can fall through to the env/default cascade instead of pinning 0.
func flagChanged(args []string, name string) bool {
	for _, a := range args {
		if a == "--"+name || strings.HasPrefix(a, "--"+name+"=") {
			return true
		}
	}
	return false
}

// stringList is a repeatable string flag.
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// strp dereferences a *string (nil -> "").
func strp(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
