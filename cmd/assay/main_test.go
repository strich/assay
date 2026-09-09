package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BrightrockGames/assay/internal/config"
)

func TestParseFlagsAndValidate(t *testing.T) {
	opts, err := parseFlags([]string{"--pr", "https://github.com/o/r/pull/1", "--dry-run", "--ignore-path", "*.md", "--ignore-path", "vendor/**"}, os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	if err := opts.validate(); err != nil {
		t.Fatal(err)
	}
	if opts.pr == "" || !opts.dryRun || len(opts.ignorePaths) != 2 {
		t.Fatalf("opts = %+v", opts)
	}
	if opts.depth != "auto" || opts.output != "ndjson" || opts.maxReviewDepth != 2 {
		t.Fatalf("defaults = %+v", opts)
	}
}

func TestValidateRequiresExactlyOneMode(t *testing.T) {
	if err := (options{}).validate(); err == nil {
		t.Error("no mode must be invalid")
	}
	both := options{pr: "x", repo: "y"}
	if err := both.validate(); err == nil {
		t.Error("two modes must be invalid")
	}
	if err := (options{diff: "-", depth: "nonsense"}).validate(); err == nil {
		t.Error("invalid depth must be invalid")
	}
	if err := (options{diff: "-", output: "xml"}).validate(); err == nil {
		t.Error("invalid output must be invalid")
	}
}

func TestReadDiffFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "changes.diff")
	if err := os.WriteFile(path, []byte("diff --git a b"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readDiff(path)
	if err != nil || got != "diff --git a b" {
		t.Fatalf("got %q err %v", got, err)
	}
}

func TestFlagChangedDistinguishesUnsetFromZero(t *testing.T) {
	if flagChanged([]string{"--pr", "x"}, "max-cost-usd") {
		t.Error("unset flag reported changed")
	}
	if !flagChanged([]string{"--max-cost-usd=0"}, "max-cost-usd") {
		t.Error("--flag=value form not detected")
	}
	if !flagChanged([]string{"--max-cost-usd", "0"}, "max-cost-usd") {
		t.Error("--flag value form not detected")
	}
}

func TestRoleTiersCoversEveryReasonerRole(t *testing.T) {
	roles := roleTiers(config.DefaultModelConfig())
	for _, role := range []string{
		"intake_gate", "intake_fallback", "anatomy_semantic", "planner",
		"reviewer", "cross_ref", "adversary", "coverage_gate", "dedup_gate",
	} {
		if strings.TrimSpace(roles[role]) == "" {
			t.Errorf("role %q has no tier", role)
		}
	}
}
