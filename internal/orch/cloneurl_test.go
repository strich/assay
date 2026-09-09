package orch

// Clone-URL credential form for GitHub tokens.
//
// Regression test for the failure seen with a GitHub Actions
// secrets.GITHUB_TOKEN (a GitHub App INSTALLATION token, ghs_…):
//
//	remote: Invalid username or token. Password authentication is not
//	supported for Git operations.
//	fatal: Authentication failed for 'https://github.com/<org>/<repo>.git/'
//
// Installation/App tokens are only accepted as
// https://x-access-token:<token>@github.com/… . The bare-token userinfo form
// this used to build works for classic PATs (ghp_…) but not for those, which is
// why it went unnoticed. "x-access-token" covers both, so it is the only form
// used — if a future change "simplifies" the username away, these fail.

import (
	"strings"
	"testing"
)

func TestTokenizedCloneURL(t *testing.T) {
	for _, tc := range []struct {
		name  string
		url   string
		token string
		want  string
	}{
		{
			name:  "installation token",
			url:   "https://github.com/acme/widgets.git",
			token: "ghs_installationtoken",
			want:  "https://x-access-token:ghs_installationtoken@github.com/acme/widgets.git",
		},
		{
			// One code path for both token kinds — no branch on token prefix.
			name:  "classic PAT uses the same form",
			url:   "https://github.com/acme/widgets.git",
			token: "ghp_classicpat",
			want:  "https://x-access-token:ghp_classicpat@github.com/acme/widgets.git",
		},
		{
			name:  "empty token leaves url untouched",
			url:   "https://github.com/acme/widgets.git",
			token: "",
			want:  "https://github.com/acme/widgets.git",
		},
		{
			name:  "non-github host untouched",
			url:   "https://gitlab.com/acme/widgets.git",
			token: "ghs_tok",
			want:  "https://gitlab.com/acme/widgets.git",
		},
		{
			name:  "ssh remote untouched",
			url:   "git@github.com:acme/widgets.git",
			token: "ghs_tok",
			want:  "git@github.com:acme/widgets.git",
		},
		{
			name:  "plain http untouched",
			url:   "http://github.com/acme/widgets.git",
			token: "ghs_tok",
			want:  "http://github.com/acme/widgets.git",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tokenizedCloneURL(tc.url, tc.token); got != tc.want {
				t.Errorf("tokenizedCloneURL(%q, %q) = %q, want %q", tc.url, tc.token, got, tc.want)
			}
		})
	}
}

func TestTokenizedCloneURLNeverEmitsBareTokenUserinfo(t *testing.T) {
	// The exact shape GitHub rejects for installation tokens must never appear.
	got := tokenizedCloneURL("https://github.com/acme/widgets.git", "ghs_tok")
	if strings.Contains(got, "https://ghs_tok@github.com/") {
		t.Errorf("tokenizedCloneURL emitted bare-token userinfo: %q", got)
	}
	if !strings.HasPrefix(got, "https://x-access-token:") {
		t.Errorf("tokenizedCloneURL = %q, want an x-access-token: prefix", got)
	}
}

func TestTokenizedCloneURLRewritesPrefixOnce(t *testing.T) {
	// A repo path that happens to contain the prefix text is not double-rewritten.
	got := tokenizedCloneURL("https://github.com/acme/https://github.com/.git", "ghs_tok")
	if n := strings.Count(got, "x-access-token"); n != 1 {
		t.Errorf("tokenizedCloneURL = %q, want exactly 1 x-access-token, got %d", got, n)
	}
}
