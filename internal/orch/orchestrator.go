// Package orch is the assay review orchestrator — the Go port of
// src/pr_af/orchestrator.py. It coordinates the multi-phase review pipeline:
// intake → anatomy → meta-selectors → review+layer (streaming) → coverage ‖
// consistency → synthesis → merge-gate → output. It owns the HITL revision loop,
// the streaming producer/consumer channel, the order-preserving fan-outs, the
// (inert) wall-clock budget gate, and the byte-exact Markdown output builders.
//
// Concurrency parity: Python's asyncio is cooperative single-threaded, so its
// shared orchestrator state needs no locking. Go runs the fan-outs on real
// goroutines, so every field mutated from a parallel closure (agentInvocations,
// totalCostUSD, costBreakdown, the adversary/cross-ref counters, budgetExhausted)
// is guarded by o.mu.
package orch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/BrightrockGames/assay/internal/budget"
	"github.com/BrightrockGames/assay/internal/config"
	"github.com/BrightrockGames/assay/internal/github"
	"github.com/BrightrockGames/assay/internal/harnessx"
	"github.com/BrightrockGames/assay/internal/ndjson"
	"github.com/BrightrockGames/assay/internal/prompts"
	"github.com/BrightrockGames/assay/internal/reasoners"
	"github.com/BrightrockGames/assay/internal/schemas"
)

// ErrBadInput is the sentinel wrapping every ValueError-class failure Python's
// review() maps to HTTP 400: the "One of pr_url, diff_text, or repo_path is
// required" guard and the _compute_repo_diff failure. The node layer (T4.2)
// maps errors.Is(err, ErrBadInput) to 400 and everything else to 500 with the
// "review execution failed: " prefix.
var ErrBadInput = errors.New("bad input")

// errBudgetExhausted mirrors Python's BudgetExhaustedError (a RuntimeError). It
// is NOT a ValueError, so it maps to 500 — not ErrBadInput.
var errBudgetExhausted = errors.New("budget exhausted")

// errPRDataNotInitialized mirrors Python's RuntimeError("PR data not
// initialized") — a 500-class internal invariant failure.
var errPRDataNotInitialized = errors.New("PR data not initialized")

// Deps carries the injected capabilities: the single LLM seam, the GitHub
// client, the spend accountant and the NDJSON progress reporter. There is no
// control-plane, no callback URL and no local-call tracking — assay is a batch
// job, and the seam every phase routes through is the same one.
type Deps struct {
	LLM      harnessx.Caller
	GH       github.Client
	Budget   *budget.Accountant
	Progress *ndjson.Reporter
}

// phaseOrder is the phases_completed key list.
var phaseOrder = []string{
	"intake", "anatomy", "meta_selectors", "review",
	"adversary", "cross_ref", "coverage", "synthesis", "output",
}

// Meta-selector configuration (schemas/pipeline.py MetaSelectorConfig — not
// ported to Go config, so bound here).
var enabledLenses = []string{"semantic", "mechanical", "systemic"}

const (
	adversaryBatchSize = 5
	maxAdversaryBatch  = 4
)

// Orchestrator holds one review's state and seams. New wires the default
// (production) seams; tests override individual fields to stub phases.
type Orchestrator struct {
	deps   Deps
	input  schemas.ReviewInput
	config config.ReviewConfig

	reviewID  string
	startedAt time.Time

	mu                        sync.Mutex // guards the counters below (mutated from fan-out goroutines)
	agentInvocations          int
	budgetExhausted           bool
	durationCapTripped        bool // the wall-clock cap (not the cost cap) exhausted the budget
	reviewDimensionsAttempted int
	reviewDimensionsParseable int
	degradedDimensions        int

	// Single-threaded-written state (set before/after fan-outs, read after joins).
	prData                   *schemas.GitHubPRData
	intakeResult             *schemas.IntakeResult
	anatomyResult            *schemas.AnatomyResult
	metaSelectorResults      []schemas.MetaDimensionResult
	coverageIterations       int
	crossRefCount            int
	adversaryConfirmedCount  int
	adversaryChallengedCount int
	effectiveDepth           string

	patchesCache    []prompts.StrPair
	patchesCacheSet bool

	// Repo-root AGENTS.md, read lazily by repoGuidance() and cached: it feeds
	// every meta selector and every reviewer, and the workspace tree does not
	// change mid-review.
	repoGuidanceCache string
	repoGuidanceOnce  sync.Once

	// clock is time.Since(startedAt) — indirected so budget tests can drive it.
	clock func() time.Duration

	// layerBatchHook, when set, is called with each batch the streaming layer
	// consumer receives — a test seam to prove the layer consumes as reviewers
	// complete (streaming), not after all of them finish (batching). nil in prod.
	layerBatchHook func([]schemas.ReviewFinding)

	// Control-flow seams (default to bound methods; tests override).
	runIntakeFn       func(ctx context.Context) (schemas.IntakeResult, error)
	runAnatomyFn      func(ctx context.Context, intake schemas.IntakeResult) (schemas.AnatomyResult, error)
	resolveDepthFn    func(intake schemas.IntakeResult) string
	runReviewPhasesFn func(ctx context.Context, intake schemas.IntakeResult, anatomy schemas.AnatomyResult, depth, feedback string) (schemas.ReviewPlan, []schemas.ScoredFinding, error)
	generateOutputFn  func(ctx context.Context, scored []schemas.ScoredFinding, intake schemas.IntakeResult, anatomy schemas.AnatomyResult, plan schemas.ReviewPlan, post bool) (schemas.ReviewResult, error)
	cleanupFn         func()

	// Reasoner-call seams (default to reasoners.*; streaming/order tests override).
	rfns reasonerSeams
}

type dimensionParseStats struct {
	mu        sync.Mutex
	attempted int
	parseable int
	failed    int
}

type dimensionParseSnapshot struct {
	Attempted int
	Parseable int
	Failed    int
}

func (s *dimensionParseStats) recordAttempt() {
	s.mu.Lock()
	s.attempted++
	s.mu.Unlock()
}

func (s *dimensionParseStats) recordResult(schemaParseFailed bool) {
	s.mu.Lock()
	if schemaParseFailed {
		s.failed++
	} else {
		s.parseable++
	}
	s.mu.Unlock()
}

func (s *dimensionParseStats) snapshot() dimensionParseSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return dimensionParseSnapshot{Attempted: s.attempted, Parseable: s.parseable, Failed: s.failed}
}

func (o *Orchestrator) resetDimensionStats() {
	o.mu.Lock()
	o.reviewDimensionsAttempted = 0
	o.reviewDimensionsParseable = 0
	o.degradedDimensions = 0
	o.mu.Unlock()
}

func (o *Orchestrator) recordDimensionAttempt() {
	o.mu.Lock()
	o.reviewDimensionsAttempted++
	o.mu.Unlock()
}

func (o *Orchestrator) recordDimensionResult(schemaParseFailed bool) {
	o.mu.Lock()
	if schemaParseFailed {
		o.degradedDimensions++
	} else {
		o.reviewDimensionsParseable++
	}
	o.mu.Unlock()
}

func (o *Orchestrator) dimensionStats() dimensionParseSnapshot {
	o.mu.Lock()
	defer o.mu.Unlock()
	return dimensionParseSnapshot{Attempted: o.reviewDimensionsAttempted, Parseable: o.reviewDimensionsParseable, Failed: o.degradedDimensions}
}

// reasonerSeams bundles the reasoner entry points the pipeline invokes so tests
// can inject latency/instrumentation without a live harness.
type reasonerSeams struct {
	intake         func(ctx context.Context, deps reasoners.Deps, in reasoners.IntakeInput) (map[string]any, error)
	anatomy        func(ctx context.Context, deps reasoners.Deps, in reasoners.AnatomyInput) (map[string]any, error)
	metaSemantic   func(ctx context.Context, deps reasoners.Deps, in reasoners.MetaInput) (map[string]any, error)
	metaMechanical func(ctx context.Context, deps reasoners.Deps, in reasoners.MetaInput) (map[string]any, error)
	metaSystemic   func(ctx context.Context, deps reasoners.Deps, in reasoners.MetaInput) (map[string]any, error)
	reviewDim      func(ctx context.Context, deps reasoners.Deps, in reasoners.ReviewDimensionInput) (map[string]any, error)
	postWorthiness func(ctx context.Context, deps reasoners.Deps, in reasoners.PostWorthinessInput) (map[string]any, error)
	evidenceVerify func(ctx context.Context, deps reasoners.Deps, in reasoners.EvidenceVerifierInput) (map[string]any, error)
	adversary      func(ctx context.Context, deps reasoners.Deps, in reasoners.AdversaryInput) (map[string]any, error)
	compoundFinder func(ctx context.Context, deps reasoners.Deps, in reasoners.CompoundFinderInput) (map[string]any, error)
	compoundDedup  func(ctx context.Context, deps reasoners.Deps, in reasoners.CompoundDedupInput) (map[string]any, error)
	coverageGate   func(ctx context.Context, deps reasoners.Deps, in reasoners.CoverageGateInput) (map[string]any, error)
	extractOblig   func(ctx context.Context, deps reasoners.Deps, in reasoners.ExtractObligationsInput) (map[string]any, error)
	verifyOblig    func(ctx context.Context, deps reasoners.Deps, in reasoners.VerifyObligationInput) (map[string]any, error)
}

func defaultReasonerSeams() reasonerSeams {
	return reasonerSeams{
		intake:         reasoners.IntakePhase,
		anatomy:        reasoners.AnatomyPhase,
		metaSemantic:   reasoners.MetaSemantic,
		metaMechanical: reasoners.MetaMechanical,
		metaSystemic:   reasoners.MetaSystemic,
		reviewDim:      reasoners.ReviewDimension,
		postWorthiness: reasoners.PostWorthinessGate,
		evidenceVerify: reasoners.EvidenceVerifier,
		adversary:      reasoners.AdversaryPhase,
		compoundFinder: reasoners.CompoundFinderPhase,
		compoundDedup:  reasoners.CompoundDedupPhase,
		coverageGate:   reasoners.CoverageGate,
		extractOblig:   reasoners.ExtractObligations,
		verifyOblig:    reasoners.VerifyObligation,
	}
}

// New constructs an Orchestrator with production seams. startedAt is set now so
// the wall-clock budget gate measures from construction.
func New(d Deps, in schemas.ReviewInput, cfg config.ReviewConfig) *Orchestrator {
	o := &Orchestrator{
		deps:           d,
		input:          in,
		config:         cfg,
		reviewID:       "rev_" + hex12(),
		startedAt:      time.Now(),
		effectiveDepth: "standard",
		rfns:           defaultReasonerSeams(),
	}
	o.clock = func() time.Duration { return time.Since(o.startedAt) }

	o.runIntakeFn = o.runIntake
	o.runAnatomyFn = o.runAnatomy
	o.resolveDepthFn = o.resolveDepth
	o.runReviewPhasesFn = o.runReviewPhases
	o.generateOutputFn = o.generateOutput
	o.cleanupFn = o.cleanupContextDir
	return o
}

// reasonerDeps builds the reasoner capability bundle from the single LLM seam.
func (o *Orchestrator) reasonerDeps() reasoners.Deps {
	return reasoners.Deps{LLM: o.deps.LLM}
}

// reviewerTier is the depth profile's tier for the reviewer role, or "" when
// the profile leaves it at the role table's value.
func (o *Orchestrator) reviewerTier() string {
	if profile, ok := config.DepthProfiles[o.effectiveDepth]; ok {
		return profile.ModelTier
	}
	return ""
}

// reviewerDeps pins the reviewer tier for this run's depth profile. The
// reviewer is the expensive line item, so the depth profile — not the role
// table — decides its tier.
func (o *Orchestrator) reviewerDeps() reasoners.Deps {
	tier := o.reviewerTier()
	if tier == "" {
		return o.reasonerDeps()
	}
	return reasoners.Deps{LLM: harnessx.WithTier(o.deps.LLM, tier)}
}

// modelForRole resolves the model string a role will run on, for per-finding
// attribution. Empty when the seam cannot name it (e.g. a test stub).
func (o *Orchestrator) modelForRole(role, tierOverride string) string {
	r, ok := o.deps.LLM.(harnessx.ModelResolver)
	if !ok {
		return ""
	}
	_, model, err := r.ModelFor(harnessx.Options{Role: role, Tier: tierOverride})
	if err != nil {
		return ""
	}
	return model
}

// progress emits one NDJSON event when a reporter is wired.
func (o *Orchestrator) progress(event string, fields map[string]any) {
	o.deps.Progress.Emit(event, fields)
}

// hex12 renders 6 random bytes as 12 lowercase hex.
func hex12() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Run executes the seven-stage pipeline: intake → anatomy → meta-selectors →
// review+layer → coverage ‖ consistency → synthesis → output. It posts the
// review when there is a PR URL and the run is not a dry run; the output phase
// owns that decision.
//
// The wall-clock ceiling is a real deadline: pipeline work runs under a context
// that expires with it, so a hung phase cannot outlive the budget. Output and
// posting use the root context, because a partial result is still worth posting.
func (o *Orchestrator) Run(ctx context.Context) (schemas.ReviewResult, error) {
	var zero schemas.ReviewResult
	rootCtx := ctx
	if d := o.config.Budget.MaxDurationSeconds; d > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(d)*time.Second)
		defer cancel()
	}

	o.progress("run_start", map[string]any{"review_id": o.reviewID, "pr_url": strp(o.input.PrURL)})

	intake, err := o.runIntakeFn(ctx)
	if err != nil {
		return zero, err
	}
	o.intakeResult = &intake
	reviewDepth := o.resolveDepthFn(intake)
	o.progress("depth_resolved", map[string]any{"depth": reviewDepth, "pr_type": intake.PrType, "complexity": intake.Complexity})

	anatomy, err := o.runAnatomyFn(ctx, intake)
	if err != nil {
		if o.isBudgetExhausted() || errors.Is(err, errBudgetExhausted) {
			return o.finishPartial(rootCtx, intake, schemas.AnatomyResult{}, "anatomy")
		}
		return zero, err
	}
	o.anatomyResult = &anatomy

	o.resetDimensionStats()
	plan, scored, err := o.runReviewPhasesFn(ctx, intake, anatomy, reviewDepth, "")
	if err != nil {
		if o.isBudgetExhausted() || errors.Is(err, errBudgetExhausted) {
			return o.finishPartial(rootCtx, intake, anatomy, "review")
		}
		return zero, err
	}
	return o.finish(rootCtx, scored, intake, anatomy, plan, true)
}

// finishPartial emits what the run has when the budget ceiling stops it: the
// phases completed so far, the measured spend, and an explicit incomplete
// marker. A cap chosen without measured spend is a guess; a cap hit without a
// reported result is a wasted run.
func (o *Orchestrator) finishPartial(ctx context.Context, intake schemas.IntakeResult, anatomy schemas.AnatomyResult, stoppedAt string) (schemas.ReviewResult, error) {
	o.progress("budget_abort", map[string]any{"phase": stoppedAt, "message": o.budgetExhaustedMessage(stoppedAt)})
	return o.finish(ctx, nil, intake, anatomy, schemas.ReviewPlan{}, true)
}

// finish generates output (optionally posting) and cleans up the context dir.
// A posting failure is returned alongside the locally generated result so the
// caller can still report the review.
func (o *Orchestrator) finish(
	ctx context.Context,
	scored []schemas.ScoredFinding,
	intake schemas.IntakeResult,
	anatomy schemas.AnatomyResult,
	plan schemas.ReviewPlan,
	post bool,
) (schemas.ReviewResult, error) {
	result, err := o.generateOutputFn(ctx, scored, intake, anatomy, plan, post)
	o.cleanupFn()
	return result, err
}

// ---- budget / cost ----

// budgetOrTimeoutExhausted reports whether the run has hit either ceiling.
// Cost is MEASURED spend from opencode (the accountant), not an estimate; the
// wall clock is measured from construction. Which cap tripped is recorded so
// budgetExhaustedMessage can word the failure honestly.
func (o *Orchestrator) budgetOrTimeoutExhausted(phase string) bool {
	elapsed := o.clock().Seconds()
	o.mu.Lock()
	defer o.mu.Unlock()
	if elapsed > float64(o.config.Budget.MaxDurationSeconds) {
		o.budgetExhausted = true
		o.durationCapTripped = true
		return true
	}
	if o.deps.Budget.CostUSD() >= o.config.Budget.MaxCostUSD {
		o.budgetExhausted = true
		return true
	}
	return false
}

// budgetExhaustedMessage words the exhaustion by cause.
func (o *Orchestrator) budgetExhaustedMessage(phase string) string {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.durationCapTripped {
		return fmt.Sprintf("Review time budget exceeded (max_duration_seconds=%d) before %s",
			o.config.Budget.MaxDurationSeconds, phase)
	}
	return "Budget exhausted before " + phase
}

func (o *Orchestrator) incInvocations(n int) {
	o.mu.Lock()
	o.agentInvocations += n
	o.mu.Unlock()
}

func (o *Orchestrator) invocations() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.agentInvocations
}

func (o *Orchestrator) isBudgetExhausted() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.budgetExhausted
}

// totalCost is the measured spend reported by the accountant.
func (o *Orchestrator) totalCost() float64 {
	return o.deps.Budget.CostUSD()
}

// resolveDepth ports _resolve_depth.
func (o *Orchestrator) resolveDepth(intake schemas.IntakeResult) string {
	if o.input.Depth != "auto" {
		return o.input.Depth
	}
	if _, ok := config.DepthProfiles[intake.ReviewDepth]; ok {
		return intake.ReviewDepth
	}
	if o.prData != nil && o.prData.Diff != "" {
		lineCount := len(strings.Split(o.prData.Diff, "\n"))
		// Python splitlines() does not count a trailing newline as an extra line;
		// mirror it.
		lineCount = countSplitlines(o.prData.Diff)
		// Under the smallest threshold → that threshold's depth.
		if len(config.AutoDepthThresholds) > 0 {
			minTh := config.AutoDepthThresholds[0]
			for _, th := range config.AutoDepthThresholds {
				if th.Lines < minTh.Lines {
					minTh = th
				}
			}
			if lineCount < minTh.Lines {
				return minTh.Depth
			}
			// Ascending scan: first threshold the count is under.
			for _, th := range config.AutoDepthThresholds {
				if lineCount < th.Lines {
					return th.Depth
				}
			}
		}
		return "deep"
	}
	return "standard"
}

// escalateDepth ports _escalate_depth.
func (o *Orchestrator) escalateDepth(currentDepth string) string {
	if currentDepth == "deep" {
		return "deep"
	}
	signals := 0
	if o.anatomyResult != nil {
		if len(o.anatomyResult.BlastRadius) > 10 {
			signals++
		}
		if len(o.anatomyResult.IntentGaps) > 0 {
			signals++
		}
		if len(o.anatomyResult.RiskSurfaces) > 3 {
			signals++
		}
		if o.anatomyResult.Stats.TotalAdditions > 500 {
			signals++
		}
	}
	if len(o.metaSelectorResults) > 0 {
		low := 0
		for _, m := range o.metaSelectorResults {
			if m.Confidence < 0.5 {
				low++
			}
		}
		if low >= 2 {
			signals++
		}
	}
	if signals >= 2 && currentDepth == "quick" {
		return "standard"
	}
	if signals >= 3 && currentDepth == "standard" {
		return "deep"
	}
	return currentDepth
}
