package orch

import (
	"context"
	"errors"
	"testing"

	"github.com/BrightrockGames/assay/internal/config"
	"github.com/BrightrockGames/assay/internal/harnessx"
	"github.com/BrightrockGames/assay/internal/schemas"
)

// fakeLLM is a placeholder seam for tests that stub the reasoner functions
// directly. Any unexpected call is a test bug, so it errors loudly.
type fakeLLM struct{ err error }

func (f *fakeLLM) Run(context.Context, string, map[string]any, any, harnessx.Options) (*harnessx.Result, error) {
	if f.err != nil {
		return nil, f.err
	}
	return nil, errors.New("orch test: fakeLLM called unexpectedly (stub o.rfns or the Deps seam)")
}

// strPtr returns a pointer to s.
func strPtr(s string) *string { return &s }

// resolvingLLM names the model for a role/tier so attribution tests can assert
// what the orchestrator stamps on findings.
type resolvingLLM struct{ fakeLLM }

func (r *resolvingLLM) ModelFor(opts harnessx.Options) (string, string, error) {
	tier := opts.Tier
	if tier == "" {
		tier = opts.Role + "-role-tier"
	}
	return tier, tier + "-model", nil
}

// TestModelForRoleStampsDepthProfileTier proves the depth profile — not the
// role table — decides the reviewer's model, and that the tier pin survives the
// WithTier wrapper the orchestrator uses.
func TestModelForRoleStampsDepthProfileTier(t *testing.T) {
	o := New(Deps{LLM: &resolvingLLM{}}, schemas.ReviewInput{}, config.DefaultReviewConfig())
	o.effectiveDepth = "deep"
	if got := o.modelForRole("reviewer", o.reviewerTier()); got != "premium-model" {
		t.Fatalf("reviewer model = %q, want premium-model", got)
	}
	o.effectiveDepth = "quick"
	if got := o.modelForRole("reviewer", o.reviewerTier()); got != "budget-model" {
		t.Fatalf("quick reviewer model = %q, want budget-model", got)
	}
	// A stub without ModelResolver must degrade to "" rather than panic.
	o2 := New(Deps{LLM: &fakeLLM{}}, schemas.ReviewInput{}, config.DefaultReviewConfig())
	if got := o2.modelForRole("reviewer", ""); got != "" {
		t.Fatalf("non-resolver model = %q, want empty", got)
	}
}
