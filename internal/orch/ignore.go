package orch

import (
	"regexp"
	"strings"

	"github.com/strich/assay/internal/schemas"
)

// ignore.go applies the configured path globs to the changed-file set before
// any review work happens. A monorepo needs this: without it, generated files,
// lockfiles and vendored trees consume reviewer budget that the source needed.
//
// Patterns support the subset the defaults use: `*` within a path segment, `**`
// across segments, and `?` for one character. Matching is case-sensitive, like
// git's pathspec.

// matchesIgnore reports whether path matches any pattern.
func matchesIgnore(path string, patterns []string) bool {
	for _, p := range patterns {
		if matchGlob(p, path) {
			return true
		}
	}
	return false
}

// filterIgnoredFiles returns files minus those matching an ignore pattern.
func filterIgnoredFiles(files []schemas.ChangedFile, patterns []string) []schemas.ChangedFile {
	if len(patterns) == 0 {
		return files
	}
	compiled := make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		compiled = append(compiled, compileGlob(p))
	}
	out := make([]schemas.ChangedFile, 0, len(files))
	for _, f := range files {
		ignored := false
		for _, re := range compiled {
			if re.MatchString(f.Path) {
				ignored = true
				break
			}
		}
		if !ignored {
			out = append(out, f)
		}
	}
	return out
}

// matchGlob is the non-caching form used by tests.
func matchGlob(pattern, path string) bool {
	return compileGlob(pattern).MatchString(path)
}

// compileGlob translates a glob to an anchored regexp. `**/` also matches zero
// directories, so `**/*.generated.*` matches a root-level `Foo.generated.cs`.
func compileGlob(pattern string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch c := pattern[i]; c {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				if i+2 < len(pattern) && pattern[i+2] == '/' {
					b.WriteString("(?:.*/)?")
					i += 2
				} else {
					b.WriteString(".*")
					i++
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String())
}

// applyIgnores drops changed files matching the configured ignore globs.
func (o *Orchestrator) applyIgnores() {
	if o.prData == nil || len(o.config.IgnorePaths) == 0 {
		return
	}
	before := len(o.prData.ChangedFiles)
	o.prData.ChangedFiles = filterIgnoredFiles(o.prData.ChangedFiles, o.config.IgnorePaths)
	if dropped := before - len(o.prData.ChangedFiles); dropped > 0 {
		o.progress("ignored_files", map[string]any{"dropped": dropped, "remaining": len(o.prData.ChangedFiles)})
	}
}
