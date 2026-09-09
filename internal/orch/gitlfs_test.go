package orch

// ASSAY_SKIP_GIT_LFS and its effect on the git subprocess environment.
//
// Skipping Git-LFS content used to be an IMPLICIT consequence of git-lfs not
// being installed in the runtime image: nothing in the code asked for it, so
// the behaviour would have flipped silently the moment git-lfs appeared on PATH
// (the Docker images now install it, precisely so the opt-in below works).

import (
	"slices"
	"testing"
)

func TestGitEnvSkipsLFSByDefault(t *testing.T) {
	t.Setenv("ASSAY_SKIP_GIT_LFS", "")
	if !skipGitLFS() {
		t.Fatal("skipGitLFS() = false, want true by default")
	}
	env := gitEnv()
	for _, want := range []string{"GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=echo", "GIT_LFS_SKIP_SMUDGE=1"} {
		if !slices.Contains(env, want) {
			t.Errorf("gitEnv() missing %q", want)
		}
	}
}

func TestGitEnvTruthyValues(t *testing.T) {
	for _, raw := range []string{"1", "true", "yes", "TRUE", " True "} {
		t.Setenv("ASSAY_SKIP_GIT_LFS", raw)
		if !skipGitLFS() {
			t.Errorf("skipGitLFS() with %q = false, want true", raw)
		}
	}
}

func TestGitEnvOptInDropsInheritedSmudgeVar(t *testing.T) {
	// An inherited GIT_LFS_SKIP_SMUDGE must not defeat an explicit opt-in to
	// real LFS content.
	t.Setenv("GIT_LFS_SKIP_SMUDGE", "1")
	for _, raw := range []string{"0", "false", "no", "FALSE", " no "} {
		t.Setenv("ASSAY_SKIP_GIT_LFS", raw)
		if skipGitLFS() {
			t.Errorf("skipGitLFS() with %q = true, want false", raw)
		}
		for _, kv := range gitEnv() {
			if len(kv) >= 20 && kv[:20] == "GIT_LFS_SKIP_SMUDGE=" {
				t.Errorf("gitEnv() with ASSAY_SKIP_GIT_LFS=%q still carries %q", raw, kv)
			}
		}
	}
}
