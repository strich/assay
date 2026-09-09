package orch

// guidance.go reads the checked-out repository's root AGENTS.md, the
// repo-specific review conventions file. Ports the Python node's
// config.read_repo_guidance.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// repoGuidanceFilename is the same convention OpenAI Codex review uses, so a
// repo that already has an AGENTS.md for other agentic tools needs no
// assay-specific file.
const repoGuidanceFilename = "AGENTS.md"

// maxRepoGuidanceChars caps the guidance text injected into prompts. A
// repo-root AGENTS.md is normally a page or two; the cap stops a large one from
// crowding out the diff, evidence pack and PR context the review depends on.
const maxRepoGuidanceChars = 20000

// readRepoGuidance reads the repo root's AGENTS.md, or "" when there is none.
//
// Only the repository ROOT is read -- assay does not walk up from each changed
// file, because its reviewers are scoped to dimensions (which span files)
// rather than to a single file.
//
// Unreadable or oversized files degrade to a truncated value or "" rather than
// failing the review: missing conventions make the review less specific, not
// wrong. The cap counts RUNES, matching Python's len() over str.
//
// The truncation marker keeps its original "PR-AF" spelling: it is prompt-
// adjacent text and the prompt corpus ports byte for byte.
func readRepoGuidance(repoPath string) string {
	if repoPath == "" {
		return ""
	}
	raw, err := os.ReadFile(filepath.Join(repoPath, repoGuidanceFilename))
	if err != nil {
		return ""
	}
	text := strings.TrimSpace(string(raw))
	runes := []rune(text)
	if len(runes) > maxRepoGuidanceChars {
		text = strings.TrimRight(string(runes[:maxRepoGuidanceChars]), " \t\n\r") +
			fmt.Sprintf("\n\n[truncated by PR-AF at %d characters]", maxRepoGuidanceChars)
	}
	return text
}

// repoGuidance returns the workspace's root AGENTS.md, read once per review.
// sync.Once because the meta-selector and reviewer fan-outs call it from
// multiple goroutines.
func (o *Orchestrator) repoGuidance() string {
	o.repoGuidanceOnce.Do(func() {
		o.repoGuidanceCache = readRepoGuidance(strp(o.input.RepoPath))
		if o.repoGuidanceCache != "" {
			fmt.Fprintf(os.Stderr, "[assay] applying repo review guidance from AGENTS.md (%d chars)\n",
				len(o.repoGuidanceCache))
		}
	})
	return o.repoGuidanceCache
}
