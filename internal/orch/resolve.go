package orch

// resolve.go ports the repo-resolution / PR-branch-checkout code that lives in
// src/pr_af/app.py (_resolve_repo, _checkout_pr_branch, _extract_pr_number) plus
// the orchestrator's _compute_repo_diff. Python's github/client.py::clone_repo
// was deliberately NOT ported into internal/github, so the clone + checkout
// semantics live here and shell out to git directly, tokenizing the remote with
// GH_TOKEN exactly as Python does.
//
// The verbatim error strings (design §B.4) are reproduced for the fetch/checkout
// failures so callers (and tests) see byte-identical messages.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// defaultGitTimeout is the wall-clock ceiling applied to every git subprocess
// (clone, fetch, checkout, diff) when ASSAY_GIT_TIMEOUT_SECONDS is unset. It is
// the largest of the per-call literals this replaced (clone/fetch-all 600s, PR
// fetch 300s, diff 120s, checkout 30s), so no operation gets a shorter budget
// than before. The 30s checkout in particular blew up on large monorepos while
// git was still writing the working tree, killing the review with
// "git checkout ... timed out after 30 seconds".
const defaultGitTimeout = 600 * time.Second

// gitTimeout reads ASSAY_GIT_TIMEOUT_SECONDS (seconds, fractional allowed).
// Non-positive or unparsable values fall back to defaultGitTimeout rather than
// disabling the timeout: an unbounded git call hangs the whole review with no
// diagnostic, which is strictly worse than a timeout error. Mirrors the Python
// node's config.git_timeout_seconds().
func gitTimeout() time.Duration {
	raw := os.Getenv("ASSAY_GIT_TIMEOUT_SECONDS")
	if raw == "" {
		return defaultGitTimeout
	}
	secs, err := strconv.ParseFloat(raw, 64)
	if err != nil || secs <= 0 {
		fmt.Fprintf(os.Stderr,
			"[assay] Ignoring invalid ASSAY_GIT_TIMEOUT_SECONDS=%q (must be a positive "+
				"number of seconds); using %s\n", raw, defaultGitTimeout)
		return defaultGitTimeout
	}
	return time.Duration(secs * float64(time.Second))
}

// skipGitLFS reports whether to skip Git-LFS content download at checkout
// (default: yes). Mirrors the Python node's config.skip_git_lfs.
//
// assay reviews source code, not binary payloads, so pulling LFS objects is
// pure cost: on an asset-heavy repo (Unity/game art, ML weights) it can be
// orders of magnitude more bytes than the source, and the reviewers cannot read
// the result anyway. With this on, LFS-tracked paths check out as their small
// pointer stubs.
//
// This used to be an IMPLICIT side effect of git-lfs not being installed in the
// runtime image. That was fragile — installing git-lfs for any other reason, or
// running the node on a host that has it, silently switched every review to
// downloading gigabytes of assets. Setting GIT_LFS_SKIP_SMUDGE=1 explicitly
// makes the behaviour independent of what happens to be on PATH.
func skipGitLFS() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("ASSAY_SKIP_GIT_LFS"))) {
	case "0", "false", "no":
		return false
	default:
		return true
	}
}

// gitEnv reproduces app.py's git_env: the process environment plus
// GIT_TERMINAL_PROMPT=0 and GIT_ASKPASS=echo so a missing credential fails fast
// instead of blocking on an interactive prompt, plus an explicit
// GIT_LFS_SKIP_SMUDGE (see skipGitLFS).
func gitEnv() []string {
	env := os.Environ()
	if skipGitLFS() {
		return append(env, "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=echo", "GIT_LFS_SKIP_SMUDGE=1")
	}
	// An inherited GIT_LFS_SKIP_SMUDGE would otherwise defeat an explicit
	// opt-in to real LFS content. exec uses the LAST assignment of a duplicated
	// key on Linux, but not portably, so drop the inherited entries instead.
	out := make([]string, 0, len(env)+2)
	for _, kv := range env {
		if strings.HasPrefix(kv, "GIT_LFS_SKIP_SMUDGE=") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=echo")
}

// runGit executes a git command with a hard timeout and returns stdout, stderr,
// and the error. dir is passed via -C by the caller (kept out of here so the
// clone command — which has no -C — works too).
func runGit(parent context.Context, timeout time.Duration, args ...string) (stdout, stderr string, err error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = gitEnv()
	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err = cmd.Run()
	return outBuf.String(), errBuf.String(), err
}

// ExtractPRNumber ports app.py::_extract_pr_number: the integer following
// "/pull/" in a github.com URL, or (0, false) when absent/unparsable.
func ExtractPRNumber(prURL string) (int, bool) {
	if !strings.Contains(prURL, "github.com") || !strings.Contains(prURL, "/pull/") {
		return 0, false
	}
	tail := prURL[strings.LastIndex(prURL, "/pull/")+len("/pull/"):]
	// split on "/" and strip, matching .split("/")[0].strip("/").
	seg := tail
	if i := strings.Index(seg, "/"); i >= 0 {
		seg = seg[:i]
	}
	seg = strings.Trim(seg, "/")
	n, convErr := strconv.Atoi(seg)
	if convErr != nil {
		return 0, false
	}
	return n, true
}

// checkoutPRBranch ports app.py::_checkout_pr_branch. It fetches the PR head into
// FETCH_HEAD (which always succeeds, even when the workspace is reused and
// pr-review is the current branch) and then checkout -B (re)points pr-review at
// it — the fix for the silent "reused workspace reviews the first PR forever"
// bug. The two failure strings are the §B.4 verbatim contracts.
func checkoutPRBranch(ctx context.Context, targetDir string, prNumber int) error {
	_, stderr, err := runGit(ctx, gitTimeout(),
		"-C", targetDir, "fetch", "--depth", "1", "origin", fmt.Sprintf("pull/%d/head", prNumber))
	if err != nil {
		return fmt.Errorf("git fetch of PR #%d head failed: %s", prNumber, strings.TrimSpace(stderr))
	}
	_, stderr, err = runGit(ctx, gitTimeout(),
		"-C", targetDir, "checkout", "-B", "pr-review", "FETCH_HEAD")
	if err != nil {
		return fmt.Errorf("git checkout of PR #%d (pr-review) failed: %s", prNumber, strings.TrimSpace(stderr))
	}
	return nil
}

// tokenizedCloneURL embeds token in a github.com HTTPS clone URL as basic-auth
// credentials. Mirrors the Python node's _tokenized_clone_url.
//
// The username MUST be "x-access-token". GitHub App INSTALLATION tokens
// (ghs_…, which is what secrets.GITHUB_TOKEN hands a GitHub Actions workflow)
// are only accepted in that form; passing the token alone as the userinfo —
// https://<token>@github.com/…, which is what this used to do — gets rejected
// with:
//
//	remote: Invalid username or token. Password authentication is not
//	supported for Git operations.
//	fatal: Authentication failed for 'https://github.com/<org>/<repo>.git/'
//
// Classic PATs (ghp_…) are accepted either way, which is why this went
// unnoticed. "x-access-token" works for both, so it is the only form used. Do
// not "simplify" the username away.
//
// Non-github.com or non-HTTPS URLs, and an empty token, are returned unchanged.
func tokenizedCloneURL(url, token string) string {
	const prefix = "https://github.com/"
	if token == "" || !strings.HasPrefix(url, prefix) {
		return url
	}
	return strings.Replace(url, prefix,
		fmt.Sprintf("https://x-access-token:%s@github.com/", token), 1)
}

// ResolveRepo ports app.py::_resolve_repo. It resolves repoPath / prURL to a
// local directory: an existing dir is returned as-is (resolved absolute), an
// http(s)/git@ URL is (shallow) cloned into $ASSAY_WORKDIR (with GH_TOKEN
// injected into a github.com HTTPS remote) and the PR branch checked out when a
// PR number is known, and anything else falls back to $ASSAY_REPO_PATH / cwd.
//
// The workspace is keyed by repo AND PR number when one is known
// (<repoName>-pr<N>): the shared per-repo dir re-pointed its pr-review branch
// on every review, so two concurrent reviews of different PRs of the same repo
// silently reviewed the wrong checkout. Plain <repoName> is kept when no PR
// number is known (repo_path / diff_text flows), preserving the old layout.
//
// Empty strings stand in for Python's None. Errors mirror Python's ValueError
// ("git clone failed: …", plus the checkout strings via checkoutPRBranch).
func ResolveRepo(ctx context.Context, repoPath, prURL string) (string, error) {
	workdir := strings.TrimSpace(os.Getenv("ASSAY_WORKDIR"))
	if workdir == "" {
		// A runner is not a container: /workspaces does not exist and creating
		// it needs root. Default to a writable temp dir.
		workdir = filepath.Join(os.TempDir(), "assay-work")
	}
	target := repoPath
	prNumber := 0
	hasPR := false

	if target == "" && strings.Contains(prURL, "github.com") && strings.Contains(prURL, "/pull/") {
		// parts = pr_url.split("github.com/")[-1].split("/pull/")[0].strip("/")
		afterHost := prURL[strings.LastIndex(prURL, "github.com/")+len("github.com/"):]
		parts := afterHost
		if i := strings.Index(parts, "/pull/"); i >= 0 {
			parts = parts[:i]
		}
		parts = strings.Trim(parts, "/")
		if strings.Count(parts, "/") == 1 {
			target = fmt.Sprintf("https://github.com/%s.git", parts)
		}
		prNumber, hasPR = ExtractPRNumber(prURL)
	}

	// Existing directory → return resolved absolute path.
	if target != "" && isDir(target) {
		abs, err := filepath.Abs(target)
		if err != nil {
			return target, nil
		}
		return abs, nil
	}

	// Remote URL → clone (or refresh) into the workspace.
	if target != "" && (strings.HasPrefix(target, "https://") ||
		strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "git@")) {
		repoName := strings.TrimSuffix(lastSegment(strings.TrimRight(target, "/")), ".git")
		workspaceName := repoName
		if hasPR && prNumber != 0 {
			// Per-PR workspace: isolates concurrent reviews of different PRs
			// of the same repo (see the doc comment above).
			workspaceName = fmt.Sprintf("%s-pr%d", repoName, prNumber)
		}
		targetDir := filepath.Join(workdir, workspaceName)
		if err := os.MkdirAll(workdir, 0o755); err != nil {
			return "", fmt.Errorf("git clone failed: %s", strings.TrimSpace(err.Error()))
		}

		cloneURL := tokenizedCloneURL(target, strings.TrimSpace(os.Getenv("GH_TOKEN")))

		if isDir(targetDir) && isDir(filepath.Join(targetDir, ".git")) {
			// Reused workspace: refresh all refs (errors swallowed, as Python does).
			_, _, _ = runGit(ctx, gitTimeout(), "-C", targetDir, "fetch", "--all")
		} else {
			cloneCmd := []string{"clone", "--depth", "1", "--no-tags", cloneURL, targetDir}
			if hasPR && prNumber != 0 {
				// Skip default-branch checkout; the PR ref is fetched next.
				cloneCmd = []string{"clone", "--depth", "1", "--no-tags", "--no-checkout", cloneURL, targetDir}
			}
			_, stderr, err := runGit(ctx, gitTimeout(), cloneCmd...)
			if err != nil {
				return "", fmt.Errorf("git clone failed: %s", strings.TrimSpace(stderr))
			}
		}

		if hasPR && prNumber != 0 {
			if err := checkoutPRBranch(ctx, targetDir, prNumber); err != nil {
				return "", err
			}
		}
		return targetDir, nil
	}

	// Fallback: ASSAY_REPO_PATH or cwd.
	fallback := os.Getenv("ASSAY_REPO_PATH")
	if fallback == "" {
		if cwd, err := os.Getwd(); err == nil {
			fallback = cwd
		}
	}
	abs, err := filepath.Abs(fallback)
	if err != nil {
		return fallback, nil
	}
	return abs, nil
}

// computeRepoDiff ports orchestrator._compute_repo_diff: a `git diff` over a
// revision range derived from base/head refs. A non-zero exit is a ValueError in
// Python → wrapped in ErrBadInput here (the review()-caught 400 class).
func computeRepoDiff(ctx context.Context, repoPath, baseRef, headRef string) (string, error) {
	if headRef != "" && baseRef == "" {
		baseRef = "HEAD"
	}
	var revision string
	switch {
	case baseRef != "" && headRef != "":
		revision = fmt.Sprintf("%s...%s", baseRef, headRef)
	case baseRef != "":
		revision = fmt.Sprintf("%s...HEAD", baseRef)
	default:
		revision = "HEAD~1...HEAD"
	}
	stdout, stderr, err := runGit(ctx, gitTimeout(), "-C", repoPath, "diff", "--no-color", revision)
	if err != nil {
		msg := strings.TrimSpace(stderr)
		if msg == "" {
			msg = "Failed to compute git diff"
		}
		return "", badInput(msg)
	}
	return stdout, nil
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func lastSegment(path string) string {
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[i+1:]
	}
	return path
}
