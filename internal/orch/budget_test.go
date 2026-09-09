package orch

// Budget: the ceiling is enforced on MEASURED spend (the accountant) and on
// wall-clock; the actuals are reported either way. The no-source guard surfaces
// ErrBadInput with the verbatim message the CLI maps to exit 2.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/BrightrockGames/assay/internal/budget"
	"github.com/BrightrockGames/assay/internal/config"
	"github.com/BrightrockGames/assay/internal/schemas"
)

func TestWallClockBudgetTrips(t *testing.T) {
	// Production builds config via FromInput, which resolves the effective
	// duration cap to 3600s.
	cfg, cfgErr := config.ReviewConfig{}.FromInput(schemas.ReviewInput{})
	if cfgErr != nil {
		t.Fatalf("FromInput: %v", cfgErr)
	}
	o := New(Deps{LLM: &fakeLLM{}}, schemas.ReviewInput{}, cfg)
	o.clock = func() time.Duration { return 10 * time.Second }
	if o.budgetOrTimeoutExhausted("review") {
		t.Error("should not be exhausted at 10s")
	}
	if o.isBudgetExhausted() {
		t.Error("budgetExhausted flag set prematurely")
	}
	o.clock = func() time.Duration { return 4000 * time.Second }
	if !o.budgetOrTimeoutExhausted("intake") {
		t.Error("should be exhausted past the 3600s wall-clock cap")
	}
	if !o.isBudgetExhausted() {
		t.Error("budgetExhausted flag not set after timeout")
	}
	want := "Review time budget exceeded (max_duration_seconds=3600) before intake"
	if got := o.budgetExhaustedMessage("intake"); got != want {
		t.Errorf("duration-cap message = %q, want %q", got, want)
	}
}

func TestMeasuredCostCapTrips(t *testing.T) {
	cfg, cfgErr := config.ReviewConfig{}.FromInput(schemas.ReviewInput{})
	if cfgErr != nil {
		t.Fatalf("FromInput: %v", cfgErr)
	}
	acct := budget.New()
	o := New(Deps{LLM: &fakeLLM{}, Budget: acct}, schemas.ReviewInput{}, cfg)
	o.clock = func() time.Duration { return 10 * time.Second }
	if o.budgetOrTimeoutExhausted("review") {
		t.Fatal("should not be exhausted at zero measured spend")
	}
	spent := cfg.Budget.MaxCostUSD
	acct.Record(&spent, budget.Tokens{Input: 10, Output: 2})
	if !o.budgetOrTimeoutExhausted("review") {
		t.Fatal("should be exhausted at the cost cap")
	}
	want := "Budget exhausted before review"
	if got := o.budgetExhaustedMessage("review"); got != want {
		t.Errorf("cost-cap message = %q, want %q", got, want)
	}
	if got := o.totalCost(); got != spent {
		t.Errorf("totalCost = %v, want the measured %v", got, spent)
	}
}

func TestRunNoSourceIsBadInput(t *testing.T) {
	o := New(Deps{LLM: &fakeLLM{}}, schemas.ReviewInput{Depth: "auto"}, config.DefaultReviewConfig())
	_, err := o.Run(context.Background())
	if err == nil {
		t.Fatal("expected an error for a review with no source")
	}
	if !errors.Is(err, ErrBadInput) {
		t.Errorf("error should wrap ErrBadInput, got %v", err)
	}
	if err.Error() != "One of pr_url, diff_text, or repo_path is required" {
		t.Errorf("error message = %q, want the verbatim ValueError string", err.Error())
	}
}
