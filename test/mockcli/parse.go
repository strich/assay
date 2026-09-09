package main

import (
	"encoding/json"
	"regexp"
	"strings"
)

// parse.go extracts the facts each mock role needs from the prompt. assay
// invokes `opencode run` with the prompt as a positional argument on POSIX and
// over stdin on Windows:
//
//	opencode run --format json [--dir D] [-m M] "<user-prompt + OUTPUT-REQUIREMENTS suffix>"
//
// The suffix names the .assay_output.json path the mock must write.

var (
	// reOutputPath matches the OutputPath(dir) the runner's suffix names. Backtick
	// and quote are excluded so a fenced/quoted mention does not swallow trailing
	// punctuation. The prefix is `*` (not `+`): calls made with an empty Cwd get
	// the RELATIVE path ".assay_output.json", which has no prefix characters.
	reOutputPath = regexp.MustCompile("([^\\s\"'`]*\\.assay_output\\.json)")

	// reTargetFiles pulls the review_dimension "**Target files** (read and analyze
	// these): a, b" line — the files the emitted findings are anchored to.
	reTargetFiles = regexp.MustCompile(`\*\*Target files\*\*[^:]*:\s*(.+)`)

	// reFindingsArray isolates the JSON findings array the adversary / evidence
	// prompts append after "Findings with ground-truth evidence:" /
	// "Findings to verify:". Non-greedy across newlines from the first '[' to the
	// last ']' on the tail of the prompt.
	reFindingCount = regexp.MustCompile(`FINDINGS\s*\((\d+)\)`)
)

// firstGroup returns the first capture group of the first match, trimmed.
func firstGroup(re *regexp.Regexp, s string) string {
	m := re.FindStringSubmatch(s)
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(m[1])
}

// promptFromArgs returns the positional prompt for the `opencode run`
// invocation, or "" when the prompt was handed over stdin (Windows). Flags that
// take a value (--format, --dir, -m, --variant) are skipped so their values are
// not mistaken for the prompt.
func promptFromArgs(argv []string) string {
	var positional []string
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		switch a {
		case "run":
			continue
		case "--format", "--dir", "-m", "--variant":
			i++ // skip the flag's value
			continue
		}
		if strings.HasPrefix(a, "-") {
			continue
		}
		positional = append(positional, a)
	}
	if len(positional) == 0 {
		return ""
	}
	return positional[len(positional)-1]
}

// outputPathFrom extracts the .assay_output.json path the runner's suffix
// told the agent to write.
func outputPathFrom(prompt string) string {
	return firstGroup(reOutputPath, prompt)
}

// promptHead returns the first n runes of the prompt's first non-empty line —
// enough to identify the reasoner in the invocation log without bloating it.
func promptHead(prompt string, n int) string {
	for _, line := range strings.Split(prompt, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		r := []rune(line)
		if len(r) > n {
			return string(r[:n])
		}
		return line
	}
	return ""
}

// reRetryError pulls the "Error: <diagnosis>" line the runner embeds in its
// schema-retry followup prompt (BuildFollowupPrompt) — it carries the reason the
// PREVIOUS attempt failed validation, which is gold for diagnosing runs.
var reRetryError = regexp.MustCompile(`(?m)^Error:\s*(.+)$`)

func retryErrorFrom(prompt string) string {
	return firstGroup(reRetryError, prompt)
}

// targetFilesFrom parses the review_dimension "**Target files** ...: a, b" line
// into a []string. Returns nil when absent (a gap-dimension prompt has none).
func targetFilesFrom(prompt string) []string {
	line := firstGroup(reTargetFiles, prompt)
	if line == "" {
		return nil
	}
	// Stop at the next markdown bold field on the same logical line, if any.
	if i := strings.Index(line, "**"); i >= 0 {
		line = line[:i]
	}
	var out []string
	for _, part := range strings.Split(line, ",") {
		p := strings.TrimSpace(part)
		p = strings.Trim(p, "`")
		p = strings.TrimSpace(p)
		if p != "" && p != "(none)" {
			out = append(out, p)
		}
	}
	return out
}

// findingCountFrom parses "FINDINGS (N):" (post_worthiness / compound prompts) so
// the gate can keep every index. Returns 0 when absent.
func findingCountFrom(prompt string) int {
	s := firstGroup(reFindingCount, prompt)
	if s == "" {
		return 0
	}
	n := 0
	for _, r := range s {
		n = n*10 + int(r-'0')
	}
	return n
}

// promptFinding is the subset of a rendered finding the mock echoes back. The
// adversary / evidence prompts serialize findings as a JSON array; the mock needs
// the title (adversary results key off it) and the anchor.
type promptFinding struct {
	Title         string `json:"title"`
	Severity      string `json:"severity"`
	FilePath      string `json:"file_path"`
	DimensionName string `json:"dimension_name"`
	LineStart     int    `json:"line_start"`
}

// findingsFrom extracts the JSON findings array embedded in an adversary /
// evidence-verifier prompt. It scans for the LAST balanced top-level [ ... ] block
// (the findings payload is appended after the instructions) and decodes it,
// tolerating extra per-finding keys (ground_truth, evidence, etc.). Returns nil
// when no decodable array is present.
func findingsFrom(prompt string) []promptFinding {
	for _, block := range jsonArrayBlocks(prompt) {
		var fs []promptFinding
		if err := json.Unmarshal([]byte(block), &fs); err == nil && len(fs) > 0 {
			return fs
		}
	}
	return nil
}

// contextObjectFrom decodes the trailing JSON context object embedded in the meta
// prompts (it carries clusters, diff_patches, file_paths). Returns nil when no
// decodable object is present.
func contextObjectFrom(prompt string) map[string]any {
	blocks := jsonObjectBlocks(prompt)
	// The meta context blob is the LAST top-level object; prefer later blocks.
	for i := len(blocks) - 1; i >= 0; i-- {
		var m map[string]any
		if err := json.Unmarshal([]byte(blocks[i]), &m); err == nil && len(m) > 0 {
			return m
		}
	}
	return nil
}

// changedFilesFrom best-effort extracts changed file paths from a prompt: first
// from the meta context object's diff_patches / clusters, then from any "### path"
// diff-section headers the reviewer/meta prompts render.
func changedFilesFrom(prompt string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	if ctx := contextObjectFrom(prompt); ctx != nil {
		if dp, ok := ctx["diff_patches"].(map[string]any); ok {
			for k := range dp {
				add(k)
			}
		}
		if cs, ok := ctx["clusters"].([]any); ok {
			for _, c := range cs {
				if cm, ok := c.(map[string]any); ok {
					if fs, ok := cm["files"].([]any); ok {
						for _, f := range fs {
							if s, ok := f.(string); ok {
								add(s)
							}
						}
					}
				}
			}
		}
	}
	for _, m := range regexp.MustCompile(`(?m)^###\s+(.+)$`).FindAllStringSubmatch(prompt, -1) {
		add(strings.TrimSpace(m[1]))
	}
	return out
}

// jsonArrayBlocks returns every balanced top-level [ ... ] substring, in order.
func jsonArrayBlocks(text string) []string { return balancedBlocks(text, '[', ']') }

// jsonObjectBlocks returns every balanced top-level { ... } substring, in order.
func jsonObjectBlocks(text string) []string { return balancedBlocks(text, '{', '}') }

// balancedBlocks scans for balanced top-level open/close pairs (string-aware so
// braces inside quoted values do not throw off the depth count).
func balancedBlocks(text string, open, close rune) []string {
	var blocks []string
	depth := 0
	start := -1
	inStr := false
	esc := false
	for i, r := range text {
		if inStr {
			switch {
			case esc:
				esc = false
			case r == '\\':
				esc = true
			case r == '"':
				inStr = false
			}
			continue
		}
		switch r {
		case '"':
			inStr = true
		case open:
			if depth == 0 {
				start = i
			}
			depth++
		case close:
			if depth > 0 {
				depth--
				if depth == 0 && start >= 0 {
					blocks = append(blocks, text[start:i+1])
					start = -1
				}
			}
		}
	}
	return blocks
}
