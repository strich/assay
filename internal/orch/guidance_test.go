package orch

// Repo-root AGENTS.md as review guidance. Reuses the convention OpenAI Codex
// review uses, so a repo that already has an AGENTS.md for other agentic tools
// needs no assay-specific file. Mirrors tests/test_repo_guidance.py.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadRepoGuidance(t *testing.T) {
	repo := t.TempDir()
	body := "# Conventions\n\nFlag missing null-checks on MonoBehaviour Awake()."
	if err := os.WriteFile(filepath.Join(repo, repoGuidanceFilename), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got := readRepoGuidance(repo)
	if !strings.Contains(got, "MonoBehaviour Awake()") {
		t.Errorf("readRepoGuidance = %q, want the AGENTS.md body", got)
	}
}

func TestReadRepoGuidanceMissing(t *testing.T) {
	// No AGENTS.md, no repo path, and a directory in its place must all be
	// empty rather than an error: missing conventions make the review less
	// specific, not wrong.
	if got := readRepoGuidance(t.TempDir()); got != "" {
		t.Errorf("no AGENTS.md = %q, want empty", got)
	}
	if got := readRepoGuidance(""); got != "" {
		t.Errorf("empty repo path = %q, want empty", got)
	}
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, repoGuidanceFilename), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := readRepoGuidance(repo); got != "" {
		t.Errorf("AGENTS.md as a directory = %q, want empty", got)
	}
}

func TestReadRepoGuidanceTruncates(t *testing.T) {
	// A huge AGENTS.md must not crowd out the diff and evidence pack.
	repo := t.TempDir()
	big := strings.Repeat("x", maxRepoGuidanceChars+5000)
	if err := os.WriteFile(filepath.Join(repo, repoGuidanceFilename), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	got := readRepoGuidance(repo)
	if !strings.Contains(got, "truncated by PR-AF") {
		t.Errorf("oversized AGENTS.md not marked truncated: %q", got[max(0, len(got)-80):])
	}
	if n := len([]rune(got)); n > maxRepoGuidanceChars+200 {
		t.Errorf("truncated length = %d runes, want <= %d", n, maxRepoGuidanceChars+200)
	}
}

func TestRepoGuidanceIsCachedPerReview(t *testing.T) {
	// Read once: it feeds every meta selector and every reviewer.
	repo := t.TempDir()
	path := filepath.Join(repo, repoGuidanceFilename)
	if err := os.WriteFile(path, []byte("first"), 0o644); err != nil {
		t.Fatal(err)
	}
	o := &Orchestrator{}
	o.input.RepoPath = &repo
	if got := o.repoGuidance(); got != "first" {
		t.Fatalf("repoGuidance() = %q, want \"first\"", got)
	}
	if err := os.WriteFile(path, []byte("second"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := o.repoGuidance(); got != "first" {
		t.Errorf("repoGuidance() re-read the file: %q", got)
	}
}

func TestRepoGuidanceEmptyWhenNoFile(t *testing.T) {
	repo := t.TempDir()
	o := &Orchestrator{}
	o.input.RepoPath = &repo
	if got := o.repoGuidance(); got != "" {
		t.Errorf("repoGuidance() = %q, want empty", got)
	}
}
