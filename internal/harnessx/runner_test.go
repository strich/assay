package harnessx

import (
	"context"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/BrightrockGames/assay/internal/budget"
)

// fakeExec scripts the subprocess seam. It parses the prompt (argv or stdin)
// for the output-file path and calls onPrompt to decide what to write/return.
type fakeExec struct {
	calls    int
	onPrompt func(prompt string, call int) (stdout, stderr string, exitCode int, err error)
}

func (f *fakeExec) run(_ context.Context, _ string, args []string, _ []string, _ string, stdin []byte, _ time.Duration) (string, string, int, error) {
	f.calls++
	prompt := string(stdin)
	if prompt == "" && len(args) > 0 {
		prompt = args[len(args)-1]
	}
	return f.onPrompt(prompt, f.calls)
}

var outPathRe = regexp.MustCompile(`([^\s"']*\.assay_output\.json)`)

func outputPathFromPrompt(prompt string) string {
	m := outPathRe.FindStringSubmatch(prompt)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// eventStream renders the opencode JSONL events a provider emits for a step
// that cost `cost` and used `in`/`out` tokens.
func eventStream(text string, cost float64, in, out int) string {
	stepFinish, _ := json.Marshal(map[string]any{
		"type": "step_finish",
		"part": map[string]any{
			"cost":   cost,
			"tokens": map[string]any{"input": in, "output": out, "cache": map[string]any{"read": 3, "write": 1}},
		},
	})
	result, _ := json.Marshal(map[string]any{"type": "result", "result": text})
	return string(stepFinish) + "\n" + string(result) + "\n"
}

func newTestRunner(f *fakeExec) (*Runner, *budget.Accountant) {
	acct := budget.New()
	r := NewRunner("opencode", map[string]string{"budget": "openrouter/cheap", "mid": "openrouter/mid", "premium": "anthropic/premium"},
		map[string]string{"reviewer": "premium", "coverage_gate": "budget"}, nil)
	r.execCommand = f.run
	r.Accountant = acct
	r.SchemaRetries = 1
	r.Logger.SetOutput(os.Stderr)
	return r, acct
}

func TestModelForResolvesRoleTierAndOverride(t *testing.T) {
	r, _ := newTestRunner(&fakeExec{})

	tier, model, err := r.ModelFor(Options{Role: "reviewer"})
	if err != nil || tier != "premium" || model != "anthropic/premium" {
		t.Fatalf("reviewer: %q %q %v", tier, model, err)
	}
	tier, model, err = r.ModelFor(Options{Role: "coverage_gate"})
	if err != nil || tier != "budget" || model != "openrouter/cheap" {
		t.Fatalf("coverage_gate: %q %q %v", tier, model, err)
	}
	// Explicit tier wins over the role, and "standard" aliases "mid".
	tier, model, err = r.ModelFor(Options{Role: "reviewer", Tier: "standard"})
	if err != nil || tier != "mid" || model != "openrouter/mid" {
		t.Fatalf("override: %q %q %v", tier, model, err)
	}
	// Unknown role falls back to mid.
	if _, model, _ = r.ModelFor(Options{Role: "nobody"}); model != "openrouter/mid" {
		t.Fatalf("unknown role model = %q", model)
	}
	// A missing tier mapping is an error naming the env var.
	r.Models = map[string]string{}
	if _, _, err := r.ModelFor(Options{Role: "reviewer"}); err == nil || !strings.Contains(err.Error(), "ASSAY_MODEL_PREMIUM") {
		t.Fatalf("missing model error = %v", err)
	}
}

func TestRunSuccessParsesOutputAndRecordsSpend(t *testing.T) {
	f := &fakeExec{onPrompt: func(prompt string, _ int) (string, string, int, error) {
		out := outputPathFromPrompt(prompt)
		if out == "" {
			return "", "no output path in prompt", 1, nil
		}
		_ = os.WriteFile(out, []byte(`{"pr_type":"feature","confident":true}`), 0o644)
		return eventStream("done", 0.12, 100, 40), "", 0, nil
	}}
	r, acct := newTestRunner(f)

	type gate struct {
		PrType    string `json:"pr_type"`
		Confident bool   `json:"confident"`
	}
	var dest gate
	schema := map[string]any{"type": "object", "properties": map[string]any{
		"pr_type":   map[string]any{"type": "string"},
		"confident": map[string]any{"type": "boolean"},
	}, "required": []any{"pr_type", "confident"}}

	res, err := r.Run(context.Background(), "classify", schema, &dest, Options{Cwd: t.TempDir(), Role: "coverage_gate"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.IsError || dest.PrType != "feature" || !dest.Confident {
		t.Fatalf("result = %+v dest = %+v", res, dest)
	}
	if res.CostUSD == nil || *res.CostUSD != 0.12 {
		t.Fatalf("cost = %v, want 0.12", res.CostUSD)
	}
	if res.Tokens.Input != 100 || res.Tokens.Output != 40 || res.Tokens.CacheRead != 3 {
		t.Fatalf("tokens = %+v", res.Tokens)
	}
	if snap := acct.Snapshot(); snap.CostUSD != 0.12 || snap.Calls != 1 || !snap.KnownUSD {
		t.Fatalf("accountant = %+v", snap)
	}
	if res.Stdout == "" {
		t.Fatal("raw stdout must be preserved")
	}
}

func TestRunSchemaRetryPreservesEveryAttempt(t *testing.T) {
	f := &fakeExec{onPrompt: func(prompt string, call int) (string, string, int, error) {
		out := outputPathFromPrompt(prompt)
		if call == 1 {
			_ = os.WriteFile(out, []byte(`{"pr_type":123}`), 0o644) // wrong type
			return eventStream("bad", 0.01, 10, 1), "", 0, nil
		}
		_ = os.WriteFile(out, []byte(`{"pr_type":"fix"}`), 0o644)
		return eventStream("good", 0.02, 20, 2), "", 0, nil
	}}
	r, acct := newTestRunner(f)

	type gate struct {
		PrType string `json:"pr_type"`
	}
	schema := map[string]any{"type": "object", "properties": map[string]any{
		"pr_type": map[string]any{"type": "string"},
	}, "required": []any{"pr_type"}}

	var dest gate
	res, err := r.Run(context.Background(), "classify", schema, &dest, Options{Cwd: t.TempDir()})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.IsError || dest.PrType != "fix" {
		t.Fatalf("result = %+v dest = %+v", res, dest)
	}
	if len(res.Attempts) != 2 {
		t.Fatalf("attempts = %d, want 2", len(res.Attempts))
	}
	if !strings.Contains(res.Attempts[0].Stdout, "bad") || !strings.Contains(res.Attempts[1].Stdout, "good") {
		t.Fatalf("raw attempt output not preserved: %+v", res.Attempts)
	}
	if snap := acct.Snapshot(); snap.CostUSD != 0.03 || snap.Calls != 2 {
		t.Fatalf("accountant = %+v (failed attempts must still be billed)", snap)
	}
	if !strings.Contains(res.Attempts[1].Prompt, "PREVIOUS ATTEMPT FAILED") {
		t.Fatalf("retry prompt missing the failure marker: %q", res.Attempts[1].Prompt)
	}
}

func TestRunSchemaExhaustionIsAnErrorResult(t *testing.T) {
	f := &fakeExec{onPrompt: func(prompt string, _ int) (string, string, int, error) {
		_ = os.WriteFile(outputPathFromPrompt(prompt), []byte(`not json`), 0o644)
		return eventStream("nope", 0.01, 1, 1), "", 0, nil
	}}
	r, _ := newTestRunner(f)

	var dest map[string]any
	res, err := r.Run(context.Background(), "x", map[string]any{"type": "object"}, &dest, Options{Cwd: t.TempDir()})
	if err != nil {
		t.Fatalf("Run returned transport error: %v", err)
	}
	if !res.IsError || res.FailureType != FailureSchema {
		t.Fatalf("res = %+v, want a schema failure", res)
	}
	if !strings.Contains(res.ErrorMessage, "schema validation failed after") {
		t.Fatalf("message = %q", res.ErrorMessage)
	}
}

func TestRunSurfacesChildStderrVerbatim(t *testing.T) {
	f := &fakeExec{onPrompt: func(string, int) (string, string, int, error) {
		return "", "Error: model not found: nope/nope", 1, nil
	}}
	r, _ := newTestRunner(f)

	var dest map[string]any
	_, err := r.Run(context.Background(), "x", map[string]any{"type": "object"}, &dest, Options{Cwd: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "model not found: nope/nope") {
		t.Fatalf("err = %v, want the child's stderr verbatim", err)
	}
}

func TestRunTimeoutIsReported(t *testing.T) {
	f := &fakeExec{onPrompt: func(string, int) (string, string, int, error) {
		return "", "", -1, &TimeoutError{Command: "opencode", Timeout: time.Second}
	}}
	r, _ := newTestRunner(f)

	var dest map[string]any
	res, err := r.Run(context.Background(), "x", map[string]any{"type": "object"}, &dest, Options{Cwd: t.TempDir()})
	if err == nil {
		t.Fatal("want a timeout error")
	}
	if res == nil || res.FailureType != FailureTimeout {
		t.Fatalf("res = %+v, want FailureTimeout", res)
	}
}

func TestWithTierPinsOnlyWhenUnset(t *testing.T) {
	base := &captureCaller{}
	pinned := WithTier(base, "budget")
	_, _ = pinned.Run(context.Background(), "p", nil, nil, Options{Role: "reviewer"})
	_, _ = pinned.Run(context.Background(), "p", nil, nil, Options{Role: "reviewer", Tier: "premium"})
	if base.tiers[0] != "budget" || base.tiers[1] != "premium" {
		t.Fatalf("tiers = %v, want [budget premium]", base.tiers)
	}
}

type captureCaller struct {
	tiers []string
	fail  bool
}

func (c *captureCaller) Run(_ context.Context, _ string, _ map[string]any, _ any, opts Options) (*Result, error) {
	c.tiers = append(c.tiers, opts.Tier)
	if c.fail {
		return &Result{IsError: true, ErrorMessage: "schema validation failed"}, nil
	}
	return &Result{}, nil
}

func TestRunGenericSeedsDefaultsOnParseFailure(t *testing.T) {
	// A Caller that reports a schema failure: Run[T] must return the
	// default-seeded T plus the Result, not an error.
	c := &captureCaller{fail: true}
	out, res, err := Run[seeded](context.Background(), c, "p", Options{})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if out == nil || !out.Complete || out.Scope != "medium" {
		t.Fatalf("seeded defaults not applied: %+v", out)
	}
	if res == nil || !res.IsError {
		t.Fatal("expected the failing Result alongside the seeded value")
	}
}

type seeded struct {
	Complete bool   `json:"complete"`
	Scope    string `json:"estimated_scope"`
}

func (s *seeded) UnmarshalJSON(b []byte) error {
	*s = seeded{Complete: true, Scope: "medium"}
	type alias seeded
	return json.Unmarshal(b, (*alias)(s))
}

// captureCaller doubles as a failing caller for the generic Run test.
func TestEventParsing(t *testing.T) {
	stream := eventStream("hello", 0.5, 7, 9)
	events := parseOpenCodeEvents(stream)
	if got := finalText(events); got != "hello" {
		t.Errorf("finalText = %q", got)
	}
	if got := costFromEvents(events); got == nil || *got != 0.5 {
		t.Errorf("cost = %v", got)
	}
	toks := tokensFromEvents(events)
	if toks.Input != 7 || toks.Output != 9 || toks.CacheRead != 3 || toks.CacheWrite != 1 {
		t.Errorf("tokens = %+v", toks)
	}
	if turnsFromEvents(events) != 0 {
		t.Errorf("turns = %d, want 0 (no step_start)", turnsFromEvents(events))
	}
	errStream := `{"type":"error","message":"model not found"}` + "\n" + stream
	if got := eventError(parseOpenCodeEvents(errStream)); got != "model not found" {
		t.Errorf("eventError = %q", got)
	}
	// Non-JSON log lines are skipped, not fatal.
	mixed := "some log line\n" + stream
	if len(parseOpenCodeEvents(mixed)) != 2 {
		t.Errorf("mixed stream parsed wrong")
	}
}

func TestBuildOutputSuffixNamesOutputPath(t *testing.T) {
	dir := t.TempDir()
	suffix := buildOutputSuffix(map[string]any{"type": "object"}, dir)
	if !strings.Contains(suffix, OutputPath(dir)) {
		t.Fatalf("suffix does not name the output path: %q", suffix)
	}
	if !strings.Contains(suffix, "CRITICAL OUTPUT REQUIREMENTS") {
		t.Fatalf("suffix missing the requirements header")
	}
}

// Contract: an in-band error event is a failure even when stdout is non-empty
// (the previous classification required an empty result and let these pass).
func TestRunInBandErrorEventIsAFailure(t *testing.T) {
	f := &fakeExec{onPrompt: func(string, int) (string, string, int, error) {
		return `{"type":"error","message":"model not found: nope/nope"}` + "\n", "", 0, nil
	}}
	r, _ := newTestRunner(f)

	var dest map[string]any
	res, err := r.Run(context.Background(), "x", map[string]any{"type": "object"}, &dest, Options{Cwd: t.TempDir()})
	if err == nil {
		t.Fatal("want an error for an in-band error event")
	}
	if res == nil || !res.IsError || !strings.Contains(res.ErrorMessage, "model not found") {
		t.Fatalf("res = %+v", res)
	}
}

// Contract: every failed schema attempt preserves the malformed output file's
// bytes, not just the event stream.
func TestRunPreservesMalformedOutputFile(t *testing.T) {
	f := &fakeExec{onPrompt: func(prompt string, call int) (string, string, int, error) {
		out := outputPathFromPrompt(prompt)
		_ = os.WriteFile(out, []byte(`{"bad":`), 0o644)
		return eventStream("done", 0, 0, 0), "", 0, nil
	}}
	r, _ := newTestRunner(f)

	var dest map[string]any
	res, _ := r.Run(context.Background(), "x", map[string]any{"type": "object"}, &dest, Options{Cwd: t.TempDir()})
	if len(res.Attempts) == 0 || res.Attempts[0].OutputFile != `{"bad":` {
		t.Fatalf("attempts = %+v, want the malformed file preserved", res.Attempts)
	}
}

// Contract: a transient provider failure is retried up to TransientRetries
// within the same schema attempt, and every subprocess is accounted.
func TestRunRetriesTransientFailures(t *testing.T) {
	f := &fakeExec{onPrompt: func(prompt string, call int) (string, string, int, error) {
		if call == 1 {
			return "", "503 service unavailable", 1, nil
		}
		_ = os.WriteFile(outputPathFromPrompt(prompt), []byte(`{"ok":true}`), 0o644)
		return eventStream("done", 0.01, 1, 1), "", 0, nil
	}}
	r, acct := newTestRunner(f)
	r.TransientRetries = 1

	type out struct {
		OK bool `json:"ok"`
	}
	schema := map[string]any{"type": "object", "properties": map[string]any{"ok": map[string]any{"type": "boolean"}}, "required": []any{"ok"}}
	var dest out
	res, err := r.Run(context.Background(), "x", schema, &dest, Options{Cwd: t.TempDir()})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !dest.OK || f.calls != 2 {
		t.Fatalf("calls = %d dest = %+v, want a retry then success", f.calls, dest)
	}
	if res.IsError {
		t.Fatalf("res = %+v", res)
	}
	if snap := acct.Snapshot(); snap.Calls != 2 {
		t.Fatalf("accountant calls = %d, want 2 (both subprocesses billed)", snap.Calls)
	}
}

// --- schema generation ------------------------------------------------------

type schemaChild struct {
	Name string `json:"name"`
}

type schemaParent struct {
	Outcome  string        `json:"outcome" jsonschema:"enum=completed,enum=failed"`
	Children []schemaChild `json:"children"`
}

func TestSchemaForNestedStructEmitsDefsItemsEnum(t *testing.T) {
	m := schemaFor[schemaParent]()
	props, ok := m["properties"].(map[string]any)
	if !ok {
		t.Fatalf("expected top-level properties, got %v", m)
	}
	outcome, _ := props["outcome"].(map[string]any)
	enum, _ := outcome["enum"].([]any)
	if len(enum) == 0 {
		t.Fatalf("expected enum on outcome, got %v", outcome)
	}
	children, _ := props["children"].(map[string]any)
	if children["type"] != "array" || children["items"] == nil {
		t.Fatalf("expected array items on children, got %v", children)
	}
	if defs, _ := m["$defs"].(map[string]any); len(defs) == 0 {
		t.Fatalf("expected $defs for the nested type, got %v", m["$defs"])
	}
}

func TestSchemaForIsCached(t *testing.T) {
	a := schemaFor[schemaParent]()
	b := schemaFor[schemaParent]()
	a["__probe__"] = 1
	if _, ok := b["__probe__"]; !ok {
		t.Fatal("schemaFor must return the cached map instance")
	}
	delete(a, "__probe__")
}
