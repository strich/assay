package orch

// ASSAY_GIT_TIMEOUT_SECONDS resolution. The git subprocess timeouts used to be
// per-call literals, and checkout's was 30s — short enough that a large
// monorepo died with "git checkout ... timed out after 30 seconds" while git
// was still writing the working tree. One env var now covers clone / fetch /
// checkout / diff, mirroring the Python node's config.git_timeout_seconds().

import (
	"testing"
	"time"
)

func TestGitTimeoutDefault(t *testing.T) {
	// The default must stay the LARGEST of the literals it replaced, so no git
	// call gets a shorter budget than the pre-env-var code gave it.
	if defaultGitTimeout != 600*time.Second {
		t.Fatalf("defaultGitTimeout = %s, want 600s", defaultGitTimeout)
	}
	t.Setenv("ASSAY_GIT_TIMEOUT_SECONDS", "")
	if got := gitTimeout(); got != defaultGitTimeout {
		t.Errorf("gitTimeout() with unset var = %s, want %s", got, defaultGitTimeout)
	}
}

func TestGitTimeoutExplicit(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want time.Duration
	}{
		{"1800", 1800 * time.Second},
		{"45.5", 45500 * time.Millisecond},
	} {
		t.Setenv("ASSAY_GIT_TIMEOUT_SECONDS", tc.raw)
		if got := gitTimeout(); got != tc.want {
			t.Errorf("gitTimeout() with %q = %s, want %s", tc.raw, got, tc.want)
		}
	}
}

func TestGitTimeoutInvalidFallsBackToDefault(t *testing.T) {
	// Never disable the timeout: an unbounded git call hangs the whole review
	// with no diagnostic, which is strictly worse than a timeout error.
	for _, raw := range []string{"0", "-1", "abc", "5m"} {
		t.Setenv("ASSAY_GIT_TIMEOUT_SECONDS", raw)
		if got := gitTimeout(); got != defaultGitTimeout {
			t.Errorf("gitTimeout() with %q = %s, want %s", raw, got, defaultGitTimeout)
		}
	}
}
