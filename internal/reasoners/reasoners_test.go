package reasoners

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/BrightrockGames/assay/internal/harnessx"
	"github.com/BrightrockGames/assay/internal/prompts"
	"github.com/BrightrockGames/assay/internal/schemas"
)

// --- seams -------------------------------------------------------------------

// fakeLLM is the single-seam test double. A call with a SystemPrompt is a
// non-agentic gate (intake/coverage) and decodes aiPayload; a call without one
// is a harness-style reasoner and decodes payload. parseFail forces the
// seeded-default path, err/aiErr force transport failures.
type fakeLLM struct {
	payload   string
	aiPayload string
	parseFail bool
	err       error
	aiErr     error

	harnessCalls int
	gateCalls    int
	gotPrompt    string
	gotSystem    string
	gotSchema    map[string]any
	gotOpts      harnessx.Options
}

func (f *fakeLLM) Run(_ context.Context, prompt string, schema map[string]any, dest any, opts harnessx.Options) (*harnessx.Result, error) {
	f.gotPrompt = prompt
	f.gotOpts = opts
	f.gotSchema = schema
	if opts.SystemPrompt != "" {
		f.gateCalls++
		f.gotSystem = opts.SystemPrompt
		if f.aiErr != nil {
			return nil, f.aiErr
		}
		if f.parseFail {
			return &harnessx.Result{IsError: true, ErrorMessage: "schema validation failed"}, nil
		}
		if err := json.Unmarshal([]byte(f.aiPayload), dest); err != nil {
			return nil, err
		}
		return &harnessx.Result{Parsed: dest, Result: f.aiPayload}, nil
	}
	f.harnessCalls++
	if f.err != nil {
		return nil, f.err
	}
	if f.parseFail {
		return &harnessx.Result{IsError: true, ErrorMessage: "schema validation failed"}, nil
	}
	if err := json.Unmarshal([]byte(f.payload), dest); err != nil {
		return nil, err
	}
	return &harnessx.Result{Parsed: dest, Result: f.payload}, nil
}

func keySet(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func wantKeys(t *testing.T, m map[string]any, want ...string) {
	t.Helper()
	sort.Strings(want)
	if got := keySet(m); !reflect.DeepEqual(got, want) {
		t.Fatalf("key set mismatch:\n got  %v\n want %v", got, want)
	}
}

// fixturePR mirrors the Python fixture used to capture helper goldens
// (helpers_test.go documents the capture run).
func fixturePR() schemas.GitHubPRData {
	paths := []string{
		"src/auth/login.py", "db/migrations/0001_init.sql", "web/App.tsx",
		"cmd/main.go", "config/settings.yml", "tests/test_login.py",
		"Dockerfile", "README.md", "script.unknownext",
	}
	files := make([]schemas.ChangedFile, len(paths))
	for i, p := range paths {
		files[i] = schemas.ChangedFile{Path: p, Status: "modified", Additions: 3, Deletions: 1}
	}
	return schemas.GitHubPRData{
		Owner: "o", Repo: "r", Number: 7,
		Title:       "Add OAuth login",
		Description: "  Implements OAuth2 login flow.  ",
		Labels:      []string{"auth"},
		Author:      "alice",
		BaseSHA:     "b", HeadSHA: "h",
		CommitMessages: []string{"init", "Co-Authored-By: Claude <x>", "fix tests"},
		ChangedFiles:   files,
	}
}

var intakeKeys = []string{
	"pr_type", "complexity", "languages", "areas_touched",
	"risk_signals", "ai_generated", "review_depth", "pr_summary",
}

// --- intake_phase -------------------------------------------------------------

// Contract: a confident gate yields the full IntakeResult key set with the
// deterministic extractor values; depth "auto" resolves through _auto_depth.
func TestIntakePhaseConfidentGate(t *testing.T) {
	h := &fakeLLM{aiPayload: `{"pr_type":"feature","complexity":"trivial","confident":true}`}
	out, err := IntakePhase(context.Background(), Deps{LLM: h}, IntakeInput{
		PRData: fixturePR(), Depth: "auto",
	})
	if err != nil {
		t.Fatal(err)
	}
	wantKeys(t, out, intakeKeys...)
	if h.harnessCalls != 0 {
		t.Fatalf("confident gate must not invoke the harness fallback, got %d calls", h.harnessCalls)
	}
	if h.gotSystem != prompts.IntakeGateSystem {
		t.Fatalf("system prompt = %q", h.gotSystem)
	}
	if h.gotSchema == nil || h.gotSchema["additionalProperties"] != false {
		t.Fatalf("intake schema = %v, want the registered strict schema", h.gotSchema)
	}
	if out["pr_type"] != "feature" || out["complexity"] != "trivial" {
		t.Fatalf("gate fields not propagated: %v", out)
	}
	if out["review_depth"] != "quick" { // auto + trivial -> quick (Python golden)
		t.Fatalf("review_depth = %v, want quick", out["review_depth"])
	}
	if out["ai_generated"] != 0.2 { // Python golden for the fixture
		t.Fatalf("ai_generated = %v, want 0.2", out["ai_generated"])
	}
	if out["pr_summary"] != "Implements OAuth2 login flow." {
		t.Fatalf("pr_summary = %v", out["pr_summary"])
	}
	wantLangs := []any{"go", "markdown", "python", "sql", "typescript", "yaml"}
	if !reflect.DeepEqual(out["languages"], wantLangs) {
		t.Fatalf("languages = %v, want %v", out["languages"], wantLangs)
	}
	wantAreas := []any{"auth", "database", "frontend", "tests", "config", "infra"}
	if !reflect.DeepEqual(out["areas_touched"], wantAreas) {
		t.Fatalf("areas_touched = %v, want %v", out["areas_touched"], wantAreas)
	}
	wantRisk := []any{
		"touches authentication or security-sensitive paths",
		"modifies data model or schema-affecting code",
		"includes configuration changes",
		"test behavior updated",
	}
	if !reflect.DeepEqual(out["risk_signals"], wantRisk) {
		t.Fatalf("risk_signals = %v", out["risk_signals"])
	}
}

// Contract: an unconfident gate escalates to the harness; a parsed fallback
// result dumps the full key set.
func TestIntakePhaseFallbackParsed(t *testing.T) {
	h := &fakeLLM{
		aiPayload: `{"pr_type":"","complexity":"","confident":false}`,
		payload: `{
		"pr_type":"refactor","complexity":"standard","languages":["go"],
		"areas_touched":["api"],"risk_signals":[],"ai_generated":0.1,
		"review_depth":"standard","pr_summary":"s"}`,
	}
	out, err := IntakePhase(context.Background(), Deps{LLM: h}, IntakeInput{
		PRData: fixturePR(), Depth: "standard",
	})
	if err != nil {
		t.Fatal(err)
	}
	if h.harnessCalls != 1 {
		t.Fatalf("harness calls = %d, want 1", h.harnessCalls)
	}
	if !strings.HasPrefix(h.gotPrompt, "Classify this pull request for a multi-agent review pipeline.") {
		t.Fatalf("fallback prompt mismatch: %q", h.gotPrompt[:60])
	}
	wantKeys(t, out, intakeKeys...)
	if out["pr_type"] != "refactor" {
		t.Fatalf("pr_type = %v", out["pr_type"])
	}
}

// Contract: a gate failure escalates to the harness instead of sinking the
// review — the gate is treated as unconfident.
func TestIntakePhaseGateFailureFallsBackToHarness(t *testing.T) {
	payload := `{
		"pr_type":"refactor","complexity":"standard","languages":["go"],
		"areas_touched":["api"],"risk_signals":[],"ai_generated":0.1,
		"review_depth":"standard","pr_summary":"s"}`

	h := &fakeLLM{payload: payload, aiErr: errors.New("response_format is not supported")}
	out, err := IntakePhase(context.Background(), Deps{LLM: h}, IntakeInput{
		PRData: fixturePR(), Depth: "standard",
	})
	if err != nil {
		t.Fatal(err)
	}
	if h.harnessCalls != 1 {
		t.Fatalf("harness calls = %d, want 1", h.harnessCalls)
	}
	wantKeys(t, out, intakeKeys...)
	if out["pr_type"] != "refactor" {
		t.Fatalf("pr_type = %v", out["pr_type"])
	}
}

// Contract: a fallback that produces nothing is an ERROR carrying the
// provider's message — NOT an empty dict recorded as success.
//
// This test previously asserted the opposite (`want {}`). A real run showed
// why that was wrong: aforge 401'd three times, IntakePhase returned {}, and
// mapToStruct turned that into a zero-valued IntakeResult, so the review
// carried on with no pr_type/complexity/summary and the 401 was never
// reported. Python's equivalent died one layer up on pydantic validation,
// blaming missing fields instead of the provider.
func TestIntakePhaseFallbackParseFailIsAnError(t *testing.T) {
	h := &fakeLLM{aiPayload: `{"pr_type":"","complexity":"","confident":false}`, parseFail: true}
	out, err := IntakePhase(context.Background(), Deps{LLM: h}, IntakeInput{
		PRData: fixturePR(), Depth: "standard",
	})
	if err == nil {
		t.Fatalf("want an error, got out=%v", out)
	}
	if out != nil {
		t.Errorf("want nil output alongside the error, got %v", out)
	}
	var unavailable *IntakeUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("error = %T (%v), want *IntakeUnavailableError", err, err)
	}
	// The provider's own message must be quoted, plus the actionable hint.
	for _, want := range []string{
		"produced no usable output",
		"schema validation failed",
		"ASSAY_MODEL",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}

// Contract: when the AI gate ALSO failed, its error is reported too — it is
// usually the same root cause and the first place it was visible.
func TestIntakeFailureMessageReportsBothCalls(t *testing.T) {
	msg := intakeFailureMessage("API error (401): User not found.",
		errors.New("AuthenticationError: 401"))
	for _, want := range []string{
		"harness error: API error (401): User not found.",
		"ai gate error: AuthenticationError: 401",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q missing %q", msg, want)
		}
	}

	// Nothing reported at all: point somewhere rather than nowhere.
	bare := intakeFailureMessage("  ", nil)
	if !strings.Contains(bare, "check the node logs") {
		t.Errorf("message %q should say where to look", bare)
	}
	if strings.Contains(bare, "harness error") {
		t.Errorf("message %q invented a harness error", bare)
	}
}

// --- anatomy_phase -------------------------------------------------------------

var anatomyKeys = []string{
	"files", "clusters", "blast_radius", "dependency_graph", "stats",
	"pr_narrative", "risk_surfaces", "unrelated_changes", "intent_gaps", "context_notes",
}

// Contract: anatomy merges deterministic diff decomposition with the parsed
// semantic fields; empty diff falls back to metadata-derived FileChanges.
func TestAnatomyPhaseHappyPath(t *testing.T) {
	h := &fakeLLM{payload: `{
		"pr_narrative":"replaces X with Y","risk_surfaces":["callers of X"],
		"unrelated_changes":[],"intent_gaps":["undocumented flag"],"context_notes":"n"}`}
	out, err := AnatomyPhase(context.Background(), Deps{LLM: h}, AnatomyInput{
		PRData: fixturePR(), Intake: schemas.IntakeResult{PrType: "feature", Complexity: "standard", PrSummary: "s"},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantKeys(t, out, anatomyKeys...)
	if out["pr_narrative"] != "replaces X with Y" {
		t.Fatalf("pr_narrative = %v", out["pr_narrative"])
	}
	files := out["files"].([]any)
	if len(files) != 9 { // metadata fallback: one FileChange per changed file
		t.Fatalf("files len = %d, want 9", len(files))
	}
	if dg := out["dependency_graph"].(map[string]any); len(dg) != 0 {
		t.Fatalf("dependency_graph = %v, want {}", dg)
	}
	if !strings.HasPrefix(h.gotPrompt, "You are a senior engineer performing structural analysis") {
		t.Fatalf("prompt mismatch: %q", h.gotPrompt[:50])
	}
}

// Contract: semantic parse failure degrades to empty narrative fields with the
// full key set intact (never an error).
func TestAnatomyPhaseParseFailSeedsEmptySemantics(t *testing.T) {
	h := &fakeLLM{parseFail: true}
	out, err := AnatomyPhase(context.Background(), Deps{LLM: h}, AnatomyInput{
		PRData: fixturePR(), Intake: schemas.IntakeResult{},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantKeys(t, out, anatomyKeys...)
	if out["pr_narrative"] != "" || out["context_notes"] != "" {
		t.Fatalf("want empty semantics, got %v / %v", out["pr_narrative"], out["context_notes"])
	}
	if rs := out["risk_surfaces"].([]any); len(rs) != 0 {
		t.Fatalf("risk_surfaces = %v, want []", rs)
	}
}

// --- planning_phase -------------------------------------------------------------

// Contract: a parsed plan dumps the four ReviewPlan keys; an absent
// total_budget lands on the pydantic default_factory seed (0.5/60/3/2).
func TestPlanningPhaseHappyPath(t *testing.T) {
	h := &fakeLLM{payload: `{
		"dimensions":[{"id":"d1","name":"N","review_prompt":"p","target_files":["a.go"]}],
		"cross_ref_hints":["h"],"ai_adjusted":true}`}
	out, err := PlanningPhase(context.Background(), Deps{LLM: h}, PlanningInput{Depth: "standard"})
	if err != nil {
		t.Fatal(err)
	}
	wantKeys(t, out, "dimensions", "cross_ref_hints", "ai_adjusted", "total_budget")
	dims := out["dimensions"].([]any)
	dim := dims[0].(map[string]any)
	if dim["priority"] != float64(1) { // seeded ReviewDimension default
		t.Fatalf("priority = %v, want 1", dim["priority"])
	}
	tb := out["total_budget"].(map[string]any)
	if tb["max_cost_usd"] != 0.5 || tb["max_duration_seconds"] != float64(60) {
		t.Fatalf("total_budget not seeded: %v", tb)
	}
}

// Contract: parse failure returns Python's literal two-key fallback.
func TestPlanningPhaseParseFailFallback(t *testing.T) {
	h := &fakeLLM{parseFail: true}
	out, err := PlanningPhase(context.Background(), Deps{LLM: h}, PlanningInput{Depth: "deep"})
	if err != nil {
		t.Fatal(err)
	}
	wantKeys(t, out, "dimensions", "cross_ref_hints")
	if len(out["dimensions"].([]any)) != 0 || len(out["cross_ref_hints"].([]any)) != 0 {
		t.Fatalf("want empty lists, got %v", out)
	}
}

// --- meta selectors -------------------------------------------------------------

var metaKeys = []string{"lens", "dimensions", "confidence", "rationale"}

// Contract: each selector forces its own lens (even over a model-supplied
// value) and emits the MetaDimensionResult key set.
func TestMetaSelectorsForceLens(t *testing.T) {
	cases := []struct {
		lens string
		fn   func(context.Context, Deps, MetaInput) (map[string]any, error)
	}{
		{"semantic", MetaSemantic},
		{"mechanical", MetaMechanical},
		{"systemic", MetaSystemic},
	}
	for _, tc := range cases {
		t.Run(tc.lens, func(t *testing.T) {
			h := &fakeLLM{payload: `{
				"lens":"WRONG",
				"dimensions":[{"id":"x","name":"X","review_prompt":"p","target_files":["a"],"priority":5}],
				"confidence":0.9,"rationale":"r"}`}
			out, err := tc.fn(context.Background(), Deps{LLM: h}, MetaInput{Depth: "standard"})
			if err != nil {
				t.Fatal(err)
			}
			wantKeys(t, out, metaKeys...)
			if out["lens"] != tc.lens {
				t.Fatalf("lens = %v, want %s", out["lens"], tc.lens)
			}
			if out["confidence"] != 0.9 {
				t.Fatalf("confidence = %v", out["confidence"])
			}
			if !strings.Contains(h.gotPrompt, strings.ToUpper(tc.lens)) {
				t.Fatalf("prompt does not carry the %s lens", tc.lens)
			}
		})
	}
}

// Contract: parse failure yields lens + empty dimensions + the seeded 0.7
// confidence (Python's MetaDimensionResult(lens=..., dimensions=[])).
func TestMetaSelectorParseFail(t *testing.T) {
	h := &fakeLLM{parseFail: true}
	out, err := MetaMechanical(context.Background(), Deps{LLM: h}, MetaInput{Depth: "quick"})
	if err != nil {
		t.Fatal(err)
	}
	wantKeys(t, out, metaKeys...)
	if out["lens"] != "mechanical" {
		t.Fatalf("lens = %v", out["lens"])
	}
	if len(out["dimensions"].([]any)) != 0 {
		t.Fatalf("dimensions = %v, want []", out["dimensions"])
	}
	if out["confidence"] != 0.7 {
		t.Fatalf("confidence = %v, want seeded 0.7", out["confidence"])
	}
	if out["rationale"] != "" {
		t.Fatalf("rationale = %v", out["rationale"])
	}
}

// --- review_dimension -------------------------------------------------------------

var reviewDimKeys = []string{"findings", "sub_reviews", "current_depth", "schema_parse_failed"}

var findingDumpKeys = []string{
	"dimension_id", "dimension_name", "file_path", "line_start", "line_end",
	"hunk_context", "severity", "title", "body", "suggestion", "evidence",
	"confidence", "tags",
}

// Contract: findings dump with the full ReviewFinding key set; sub_reviews are
// sliced to 2 THEN filtered on review_prompt+target_files; current_depth echoes
// the input.
func TestReviewDimensionHappyPath(t *testing.T) {
	h := &fakeLLM{payload: `{
		"findings":[{"title":"T","severity":"high","file_path":"a.go","line_start":3}],
		"sub_reviews":[
			{"reason":"r1","review_prompt":"p1","target_files":["a.go"]},
			{"reason":"r2","review_prompt":"","target_files":["b.go"]},
			{"reason":"r3","review_prompt":"p3","target_files":["c.go"]}
		]}`}
	out, err := ReviewDimension(context.Background(), Deps{LLM: h}, ReviewDimensionInput{
		ReviewPrompt: "Investigate X", TargetFiles: []string{"a.go"},
		CurrentDepth: 1, MaxDepth: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantKeys(t, out, reviewDimKeys...)
	if out["current_depth"] != 1 {
		t.Fatalf("current_depth = %v", out["current_depth"])
	}
	if out["schema_parse_failed"] != false {
		t.Fatalf("schema_parse_failed = %v, want false", out["schema_parse_failed"])
	}
	findings := out["findings"].([]any)
	if len(findings) != 1 {
		t.Fatalf("findings = %v", findings)
	}
	f := findings[0].(map[string]any)
	wantKeys(t, f, findingDumpKeys...)
	if f["severity"] != "important" { // "high" coerced by the canonical map
		t.Fatalf("severity = %v, want important", f["severity"])
	}
	if f["confidence"] != 0.5 { // seeded pydantic default
		t.Fatalf("confidence = %v, want 0.5", f["confidence"])
	}
	// [:2] slice happens BEFORE the filter: sr2 (empty prompt) is inside the
	// slice and dropped, sr3 is outside it — only sr1 survives.
	subs := out["sub_reviews"].([]any)
	if len(subs) != 1 {
		t.Fatalf("sub_reviews = %v, want exactly 1", subs)
	}
	sub := subs[0].(map[string]any)
	wantKeys(t, sub, "reason", "review_prompt", "target_files", "context_files", "priority")
	if sub["priority"] != 1 { // seeded default
		t.Fatalf("priority = %v", sub["priority"])
	}
}

func TestReviewDimensionThreadsAuthorDescriptionToPrompt(t *testing.T) {
	h := &fakeLLM{payload: `{"findings":[],"sub_reviews":[]}`}
	_, err := ReviewDimension(context.Background(), Deps{LLM: h}, ReviewDimensionInput{
		ReviewPrompt:  "Investigate X",
		TargetFiles:   []string{"a.go"},
		MaxDepth:      2,
		PrDescription: "FAIL_SOFT_RATIONALE",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.gotPrompt, "## Author's Stated Intent (PR Description)") ||
		!strings.Contains(h.gotPrompt, "FAIL_SOFT_RATIONALE") {
		t.Fatal("reviewer prompt did not receive the author description")
	}
}

// Contract: at max depth no sub-reviews are forwarded even if the model
// returned some.
func TestReviewDimensionAtMaxDepthDropsSubReviews(t *testing.T) {
	h := &fakeLLM{payload: `{
		"findings":[],
		"sub_reviews":[{"reason":"r","review_prompt":"p","target_files":["a"]}]}`}
	out, err := ReviewDimension(context.Background(), Deps{LLM: h}, ReviewDimensionInput{
		ReviewPrompt: "x", TargetFiles: []string{"a"}, CurrentDepth: 2, MaxDepth: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out["sub_reviews"].([]any)) != 0 {
		t.Fatalf("sub_reviews = %v, want []", out["sub_reviews"])
	}
	if !strings.Contains(h.gotPrompt, "You are at maximum review depth.") {
		t.Fatal("prompt should carry the no-spawn instruction")
	}
}

// Contract: schema parse failure reports zero findings (with the key set
// intact), never an error.
func TestReviewDimensionParseFail(t *testing.T) {
	h := &fakeLLM{parseFail: true}
	out, err := ReviewDimension(context.Background(), Deps{LLM: h}, ReviewDimensionInput{
		ReviewPrompt: "x", TargetFiles: []string{"a"}, CurrentDepth: 0, MaxDepth: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantKeys(t, out, reviewDimKeys...)
	if len(out["findings"].([]any)) != 0 || len(out["sub_reviews"].([]any)) != 0 {
		t.Fatalf("want empty findings/sub_reviews, got %v", out)
	}
	if out["current_depth"] != 0 {
		t.Fatalf("current_depth = %v", out["current_depth"])
	}
	if out["schema_parse_failed"] != true {
		t.Fatalf("schema_parse_failed = %v, want true", out["schema_parse_failed"])
	}
}

// --- compound finder / dedup -------------------------------------------------------------

// Contract: fewer than two findings short-circuits without a harness call.
func TestCompoundFinderShortCircuit(t *testing.T) {
	h := &fakeLLM{}
	out, err := CompoundFinderPhase(context.Background(), Deps{LLM: h}, CompoundFinderInput{
		ClusterFindings: []schemas.ReviewFinding{{Title: "only"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantKeys(t, out, "findings")
	if len(out["findings"].([]any)) != 0 || h.harnessCalls != 0 {
		t.Fatalf("want no harness call and [], got calls=%d out=%v", h.harnessCalls, out)
	}
}

var compoundFindingKeys = []string{
	"title", "severity", "file_path", "line_start", "line_end", "body",
	"evidence", "suggestion", "confidence", "tags", "contributing_findings",
}

// Contract: compound findings dump the _CompoundFinding key set with seeded
// defaults for absent fields.
func TestCompoundFinderHappyPath(t *testing.T) {
	h := &fakeLLM{payload: `{"findings":[{"title":"combo","contributing_findings":["a","b"]}]}`}
	out, err := CompoundFinderPhase(context.Background(), Deps{LLM: h}, CompoundFinderInput{
		ClusterFindings: []schemas.ReviewFinding{{Title: "a"}, {Title: "b"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	findings := out["findings"].([]any)
	f := findings[0].(map[string]any)
	wantKeys(t, f, compoundFindingKeys...)
	if f["severity"] != "suggestion" || f["confidence"] != 0.5 {
		t.Fatalf("seeded defaults missing: %v", f)
	}
}

// Contract: compound parse failure degrades to an empty findings list.
func TestCompoundFinderParseFail(t *testing.T) {
	h := &fakeLLM{parseFail: true}
	out, err := CompoundFinderPhase(context.Background(), Deps{LLM: h}, CompoundFinderInput{
		ClusterFindings: []schemas.ReviewFinding{{Title: "a"}, {Title: "b"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out["findings"].([]any)) != 0 {
		t.Fatalf("want [], got %v", out["findings"])
	}
}

// Contract: <=1 compound findings short-circuit with the fixed reasoning
// string; valid indices filter; empty/invalid indices keep everything.
func TestCompoundDedupPhase(t *testing.T) {
	// Short circuit.
	out, err := CompoundDedupPhase(context.Background(), Deps{}, CompoundDedupInput{
		CompoundFindings: []schemas.ReviewFinding{{Title: "x"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantKeys(t, out, "keep_indices", "reasoning")
	if out["reasoning"] != "single finding, no dedup needed" {
		t.Fatalf("reasoning = %v", out["reasoning"])
	}
	if !reflect.DeepEqual(out["keep_indices"], []int{0}) {
		t.Fatalf("keep_indices = %v", out["keep_indices"])
	}

	// Valid subset (out-of-range dropped).
	h := &fakeLLM{payload: `{"keep_indices":[1,7,-1],"reasoning":"r"}`}
	out, err = CompoundDedupPhase(context.Background(), Deps{LLM: h}, CompoundDedupInput{
		CompoundFindings: []schemas.ReviewFinding{{Title: "a"}, {Title: "b"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(out["keep_indices"], []int{1}) || out["reasoning"] != "r" {
		t.Fatalf("got %v", out)
	}

	// Parse failure -> keep all, reasoning "".
	h = &fakeLLM{parseFail: true}
	out, err = CompoundDedupPhase(context.Background(), Deps{LLM: h}, CompoundDedupInput{
		CompoundFindings: []schemas.ReviewFinding{{Title: "a"}, {Title: "b"}, {Title: "c"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(out["keep_indices"], []int{0, 1, 2}) || out["reasoning"] != "" {
		t.Fatalf("got %v", out)
	}
}

// --- post_worthiness_gate -------------------------------------------------------------

// Contract: <=1 finding returns keep_indices ONLY (no reasoning key — exact
// Python branch); >1 filters valid indices; empty/parse-fail keeps everything.
func TestPostWorthinessGate(t *testing.T) {
	// Zero findings: keep_indices [] and no reasoning key.
	out, err := PostWorthinessGate(context.Background(), Deps{}, PostWorthinessInput{})
	if err != nil {
		t.Fatal(err)
	}
	wantKeys(t, out, "keep_indices")
	if len(out["keep_indices"].([]int)) != 0 {
		t.Fatalf("keep_indices = %v", out["keep_indices"])
	}

	// One finding: [0], still no reasoning key, no harness call.
	h := &fakeLLM{}
	out, err = PostWorthinessGate(context.Background(), Deps{LLM: h}, PostWorthinessInput{
		Findings: []schemas.ReviewFinding{{Title: "t"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantKeys(t, out, "keep_indices")
	if !reflect.DeepEqual(out["keep_indices"], []int{0}) || h.harnessCalls != 0 {
		t.Fatalf("got %v calls=%d", out, h.harnessCalls)
	}

	// Judged subset.
	h = &fakeLLM{payload: `{"keep_indices":[0,2,9],"reasoning":"kept real bugs"}`}
	out, err = PostWorthinessGate(context.Background(), Deps{LLM: h}, PostWorthinessInput{
		Findings: []schemas.ReviewFinding{{Title: "a"}, {Title: "b"}, {Title: "c"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantKeys(t, out, "keep_indices", "reasoning")
	if !reflect.DeepEqual(out["keep_indices"], []int{0, 2}) || out["reasoning"] != "kept real bugs" {
		t.Fatalf("got %v", out)
	}

	// Parse failure never silences everything.
	h = &fakeLLM{parseFail: true}
	out, err = PostWorthinessGate(context.Background(), Deps{LLM: h}, PostWorthinessInput{
		Findings: []schemas.ReviewFinding{{Title: "a"}, {Title: "b"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(out["keep_indices"], []int{0, 1}) {
		t.Fatalf("got %v", out)
	}
}

// --- evidence_verifier -------------------------------------------------------------

var verifiedFindingKeys = []string{
	"title", "verified", "actual_behavior", "revised_severity",
	"revised_confidence", "verification_notes",
}

// Contract: verified findings dump the _VerifiedFinding key set; absent fields
// land on seeded defaults (verified=true, revised_confidence=0.5).
func TestEvidenceVerifierHappyPath(t *testing.T) {
	h := &fakeLLM{payload: `{"verified_findings":[{"title":"T","verified":false,"revised_severity":"nitpick"}]}`}
	out, err := EvidenceVerifier(context.Background(), Deps{LLM: h}, EvidenceVerifierInput{
		Findings: []schemas.ReviewFinding{{Title: "T", Severity: "important"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantKeys(t, out, "verified_findings")
	vf := out["verified_findings"].([]any)[0].(map[string]any)
	wantKeys(t, vf, verifiedFindingKeys...)
	if vf["verified"] != false || vf["revised_confidence"] != 0.5 {
		t.Fatalf("got %v", vf)
	}
}

// Contract: parse failure degrades to an empty verified_findings list.
func TestEvidenceVerifierParseFail(t *testing.T) {
	h := &fakeLLM{parseFail: true}
	out, err := EvidenceVerifier(context.Background(), Deps{LLM: h}, EvidenceVerifierInput{
		Findings: []schemas.ReviewFinding{{Title: "T"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out["verified_findings"].([]any)) != 0 {
		t.Fatalf("got %v", out)
	}
}

// --- adversary_phase -------------------------------------------------------------

var adversaryResultKeys = []string{
	"finding_title", "verdict", "reason", "severity_adjustment", "hidden_trap",
}

// Contract: results dump the AdversaryResult key set (seeded
// severity_adjustment="none", hidden_trap null); skepticism escalates in the
// prompt above 0.5 AI confidence.
func TestAdversaryPhaseHappyPath(t *testing.T) {
	h := &fakeLLM{payload: `{"results":[{"finding_title":"T","verdict":"confirmed","reason":"r"}]}`}
	out, err := AdversaryPhase(context.Background(), Deps{LLM: h}, AdversaryInput{
		Findings:              []schemas.ReviewFinding{{Title: "T"}},
		AIGeneratedConfidence: 0.8,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantKeys(t, out, "results")
	r := out["results"].([]any)[0].(map[string]any)
	wantKeys(t, r, adversaryResultKeys...)
	if r["severity_adjustment"] != "none" {
		t.Fatalf("severity_adjustment = %v, want seeded none", r["severity_adjustment"])
	}
	if r["hidden_trap"] != nil {
		t.Fatalf("hidden_trap = %v, want null", r["hidden_trap"])
	}
	if !strings.Contains(h.gotPrompt, "Skepticism mode: high") {
		t.Fatal("skepticism should escalate above 0.5")
	}
	if !strings.Contains(h.gotPrompt, "AI-generated confidence: 0.8") {
		t.Fatal("prompt should carry the AI confidence")
	}
}

// Contract: parse failure degrades to an empty results list.
func TestAdversaryPhaseParseFail(t *testing.T) {
	h := &fakeLLM{parseFail: true}
	out, err := AdversaryPhase(context.Background(), Deps{LLM: h}, AdversaryInput{
		Findings: []schemas.ReviewFinding{{Title: "T"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out["results"].([]any)) != 0 {
		t.Fatalf("got %v", out)
	}
}

// --- deepen_findings -------------------------------------------------------------

var deepenFindingKeys = []string{
	"dimension_id", "dimension_name", "file_path", "line_start", "line_end",
	"severity", "title", "body", "suggestion", "evidence", "confidence", "tags",
}

// Contract: no non-empty patches short-circuits without a harness call; parsed
// findings carry the _DeepenFinding seeds for absent fields.
func TestDeepenFindings(t *testing.T) {
	h := &fakeLLM{}
	out, err := DeepenFindings(context.Background(), Deps{LLM: h}, DeepenInput{
		DiffPatches: OrderedPatches{{Key: "a.go", Val: ""}}, // filtered to empty
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out["findings"].([]any)) != 0 || h.harnessCalls != 0 {
		t.Fatalf("want short-circuit, got calls=%d out=%v", h.harnessCalls, out)
	}

	h = &fakeLLM{payload: `{"findings":[{"title":"wrong arg","file_path":"a.go","line_start":3}]}`}
	out, err = DeepenFindings(context.Background(), Deps{LLM: h}, DeepenInput{
		DiffPatches: OrderedPatches{{Key: "a.go", Val: "+x"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantKeys(t, out, "findings")
	f := out["findings"].([]any)[0].(map[string]any)
	wantKeys(t, f, deepenFindingKeys...)
	if f["dimension_id"] != "literal-verify" ||
		f["dimension_name"] != "Literal-Correctness Verifier" ||
		f["severity"] != "important" || f["confidence"] != 0.7 {
		t.Fatalf("seeded defaults missing: %v", f)
	}

	h = &fakeLLM{parseFail: true}
	out, err = DeepenFindings(context.Background(), Deps{LLM: h}, DeepenInput{
		DiffPatches: OrderedPatches{{Key: "a.go", Val: "+x"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out["findings"].([]any)) != 0 {
		t.Fatalf("want [], got %v", out["findings"])
	}
}

// --- extract_obligations / verify_obligation -------------------------------------------------------------

// Contract: obligations dump {id, where, relies_on, property}; empty patches
// short-circuit; parse failure degrades to an empty list.
func TestExtractObligations(t *testing.T) {
	h := &fakeLLM{}
	out, err := ExtractObligations(context.Background(), Deps{LLM: h}, ExtractObligationsInput{})
	if err != nil {
		t.Fatal(err)
	}
	if len(out["obligations"].([]any)) != 0 || h.harnessCalls != 0 {
		t.Fatalf("want short-circuit, got %v", out)
	}

	h = &fakeLLM{payload: `{"obligations":[{"id":"o1","where":"w","relies_on":"r","property":"p"}]}`}
	out, err = ExtractObligations(context.Background(), Deps{LLM: h}, ExtractObligationsInput{
		DiffPatches: OrderedPatches{{Key: "a.go", Val: "+x"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantKeys(t, out, "obligations")
	o := out["obligations"].([]any)[0].(map[string]any)
	wantKeys(t, o, "id", "where", "relies_on", "property")

	h = &fakeLLM{parseFail: true}
	out, err = ExtractObligations(context.Background(), Deps{LLM: h}, ExtractObligationsInput{
		DiffPatches: OrderedPatches{{Key: "a.go", Val: "+x"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out["obligations"].([]any)) != 0 {
		t.Fatalf("want [], got %v", out)
	}
}

var verdictKeys = []string{
	"holds", "title", "severity", "file_path", "line_start", "line_end",
	"body", "evidence", "suggestion", "confidence",
}

// Contract: a parsed verdict dumps all ten keys; parse failure returns the
// seeded holds=true verdict (severity="important", confidence=0.7).
func TestVerifyObligation(t *testing.T) {
	h := &fakeLLM{payload: `{
		"holds":false,"title":"key mismatch","severity":"critical","file_path":"a.go",
		"line_start":10,"line_end":12,"body":"b","evidence":"e","suggestion":"s","confidence":0.9}`}
	out, err := VerifyObligation(context.Background(), Deps{LLM: h}, VerifyObligationInput{
		Obligation: map[string]any{"id": "o1", "where": "w", "relies_on": "r", "property": "p"},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantKeys(t, out, verdictKeys...)
	if out["holds"] != false || out["severity"] != "critical" {
		t.Fatalf("got %v", out)
	}
	if !strings.Contains(h.gotPrompt, "- WHERE (the changed code): w\n") {
		t.Fatalf("obligation fields not in prompt: %q", h.gotPrompt)
	}

	h = &fakeLLM{parseFail: true}
	out, err = VerifyObligation(context.Background(), Deps{LLM: h}, VerifyObligationInput{
		Obligation: map[string]any{"where": "w", "relies_on": "r", "property": "p"},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantKeys(t, out, verdictKeys...)
	if out["holds"] != true || out["severity"] != "important" || out["confidence"] != 0.7 {
		t.Fatalf("seeded verdict wrong: %v", out)
	}
	if out["suggestion"] != nil {
		t.Fatalf("suggestion = %v, want null", out["suggestion"])
	}
}

// --- coverage_gate -------------------------------------------------------------

// Contract: the gate dumps {fully_covered, gap_descriptions, confident};
// absent keys land on the pydantic seeds (confident=true, gaps=[]).
func TestCoverageGate(t *testing.T) {
	h := &fakeLLM{aiPayload: `{"fully_covered":false,"gap_descriptions":["cluster_1 unreviewed"]}`}
	out, err := CoverageGate(context.Background(), Deps{LLM: h}, CoverageGateInput{
		Anatomy: schemas.AnatomyResult{
			Clusters: []schemas.ChangeCluster{{ID: "cluster_0", Name: "root", Files: []string{"a.go"}}},
		},
		ReviewedClusters:       []string{"cluster_0"},
		DimensionNamesReviewed: []string{"Dim A"},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantKeys(t, out, "fully_covered", "gap_descriptions", "confident")
	if out["fully_covered"] != false || out["confident"] != true {
		t.Fatalf("got %v", out)
	}
	if h.gotSystem != prompts.CoverageGateSystem {
		t.Fatalf("system = %q", h.gotSystem)
	}
	if h.gotSchema == nil || h.gotSchema["additionalProperties"] != false {
		t.Fatalf("coverage schema = %v, want the registered strict schema", h.gotSchema)
	}
	if !strings.Contains(h.gotPrompt, "Dimensions already reviewed: Dim A.") {
		t.Fatalf("prompt = %q", h.gotPrompt)
	}

	// Empty response object: everything seeded.
	h = &fakeLLM{aiPayload: `{}`}
	out, err = CoverageGate(context.Background(), Deps{LLM: h}, CoverageGateInput{})
	if err != nil {
		t.Fatal(err)
	}
	if out["confident"] != true || out["fully_covered"] != false {
		t.Fatalf("seeds wrong: %v", out)
	}
	if len(out["gap_descriptions"].([]any)) != 0 {
		t.Fatalf("gap_descriptions = %v", out["gap_descriptions"])
	}
}

// Contract: a gate failure retries the SAME coverage prompt through the
// harness, and a harness result that fails to parse yields the literal empty
// dict.
func TestCoverageGateFailureFallsBackToHarness(t *testing.T) {
	in := CoverageGateInput{
		Anatomy: schemas.AnatomyResult{
			Clusters: []schemas.ChangeCluster{{ID: "cluster_0", Name: "root", Files: []string{"a.go"}}},
		},
		ReviewedClusters:       []string{"cluster_0"},
		DimensionNamesReviewed: []string{"Dim A"},
	}

	h := &fakeLLM{
		aiErr:   errors.New("response_format is not supported"),
		payload: `{"fully_covered":true,"gap_descriptions":[],"confident":true}`,
	}
	out, err := CoverageGate(context.Background(), Deps{LLM: h}, in)
	if err != nil {
		t.Fatal(err)
	}
	if h.harnessCalls != 1 {
		t.Fatalf("harness calls = %d, want 1", h.harnessCalls)
	}
	if !strings.Contains(h.gotPrompt, "Dimensions already reviewed: Dim A.") {
		t.Fatalf("harness prompt = %q, want the coverage prompt", h.gotPrompt)
	}
	wantKeys(t, out, "fully_covered", "gap_descriptions", "confident")
	if out["fully_covered"] != true {
		t.Fatalf("got %v", out)
	}

	h = &fakeLLM{aiErr: errors.New("boom"), parseFail: true}
	out, err = CoverageGate(context.Background(), Deps{LLM: h}, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Fatalf("want {}, got %v", out)
	}
}

// --- error propagation -------------------------------------------------------------

type erroringLLM struct{}

func (erroringLLM) Run(context.Context, string, map[string]any, any, harnessx.Options) (*harnessx.Result, error) {
	return nil, context.DeadlineExceeded
}

// Contract: seam transport errors propagate out of every harness-backed
// reasoner (Python lets the exception escape).
func TestHarnessErrorPropagates(t *testing.T) {
	deps := Deps{LLM: erroringLLM{}}
	if _, err := AnatomyPhase(context.Background(), deps, AnatomyInput{PRData: fixturePR()}); err == nil {
		t.Fatal("anatomy: want error")
	}
	if _, err := ReviewDimension(context.Background(), deps, ReviewDimensionInput{TargetFiles: []string{"a"}}); err == nil {
		t.Fatal("review_dimension: want error")
	}
	if _, err := VerifyObligation(context.Background(), deps, VerifyObligationInput{Obligation: map[string]any{}}); err == nil {
		t.Fatal("verify_obligation: want error")
	}
}

// Contract: aiStructured decodes a successful gate response, and reports a
// failed one as an error (the caller applies its deterministic fallback).
func TestAIStructured(t *testing.T) {
	type gate struct {
		PrType    string `json:"pr_type"`
		Confident bool   `json:"confident"`
	}

	t.Run("success decodes the payload", func(t *testing.T) {
		f := &fakeLLM{aiPayload: `{"pr_type":"feature","confident":true}`}
		var g gate
		if err := aiStructured(context.Background(), f, "p", "s", strictAISchemas[strictAISchemaIntakeGate], &g, "intake_gate"); err != nil {
			t.Fatalf("aiStructured: %v", err)
		}
		if g.PrType != "feature" || !g.Confident {
			t.Fatalf("gate=%+v, want parsed fields", g)
		}
		if f.gotSystem != "s" || f.gotOpts.Role != "intake_gate" {
			t.Fatalf("system/role not forwarded: %q / %q", f.gotSystem, f.gotOpts.Role)
		}
	})

	t.Run("schema-invalid result is an error", func(t *testing.T) {
		f := &fakeLLM{parseFail: true}
		var g gate
		err := aiStructured(context.Background(), f, "p", "s", strictAISchemas[strictAISchemaIntakeGate], &g, "intake_gate")
		if err == nil || !strings.HasPrefix(err.Error(), "Could not parse structured response: ") {
			t.Fatalf("err = %v, want Could-not-parse prefix", err)
		}
	})

	t.Run("transport error is not retried", func(t *testing.T) {
		f := &fakeLLM{aiErr: errors.New("boom")}
		var g gate
		if err := aiStructured(context.Background(), f, "p", "s", strictAISchemas[strictAISchemaIntakeGate], &g, "intake_gate"); err == nil || err.Error() != "boom" {
			t.Fatalf("err = %v, want boom", err)
		}
		if f.gateCalls != 1 {
			t.Fatalf("calls = %d, want 1", f.gateCalls)
		}
	})
}
