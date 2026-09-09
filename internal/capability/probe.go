// Package capability probes what grounding a review actually has, and reports
// it. Capabilities are probed rather than assumed so a run states what it stood
// on — otherwise you cannot tell a thin review from a thorough one after the
// fact, and a finding class cannot be honestly gated on the evidence that
// grounds it.
package capability

import (
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
)

// Status is one capability's availability and why.
type Status struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason"`
}

// Report is the full probe result, logged at startup.
type Report struct {
	Text        Status `json:"text"`
	Structure   Status `json:"structure"`
	Symbols     Status `json:"symbols"`
	Diagnostics Status `json:"diagnostics"`
	HotPath     Status `json:"hot_path"`
	History     Status `json:"history"`
}

// projectMarkers are files whose presence gives a file-to-module map without
// compiling anything. C# is the first target: committed .csproj/.sln files.
var projectMarkers = []string{
	".sln", ".csproj", "go.mod", "package.json", "pom.xml",
	"build.gradle", "Cargo.toml", ".xcodeproj", "CMakeLists.txt",
}

// ignoredDirs are never worth walking for a project-file map.
var ignoredDirs = map[string]bool{
	".git": true, "node_modules": true, "Library": true, "Temp": true,
	"obj": true, "bin": true, "vendor": true, ".assay-out": true,
}

// Probe inspects repoPath for the grounding each capability needs. repoPath may
// be empty (a raw-diff review), in which case only Text is available.
func Probe(repoPath string) Report {
	r := Report{
		Text:        Status{Available: true, Reason: "diff, file reads and symbol grep are always available"},
		Symbols:     Status{Available: false, Reason: "no language server configured (Symbols spike not run)"},
		Diagnostics: Status{Available: false, Reason: "no compile step configured (Diagnostics spike not run)"},
		HotPath:     Status{Available: false, Reason: "no framework hint pack loaded"},
		History:     Status{Available: false, Reason: "History capability not built"},
	}
	if repoPath == "" {
		r.Structure = Status{Available: false, Reason: "no working tree (raw diff)"}
		return r
	}
	found := findProjectFiles(repoPath)
	if len(found) > 0 {
		r.Structure = Status{Available: true, Reason: "project files: " + strings.Join(found, ", ")}
	} else {
		r.Structure = Status{Available: false, Reason: "no project files found"}
	}
	return r
}

// findProjectFiles returns the distinct project markers found within a bounded
// walk, sorted. The bound keeps a monorepo's dependency tree from turning the
// startup probe into a second clone.
func findProjectFiles(root string) []string {
	const maxDepth = 6
	found := map[string]bool{}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		depth := strings.Count(rel, string(filepath.Separator))
		if d.IsDir() {
			if ignoredDirs[d.Name()] {
				return filepath.SkipDir
			}
			// Some project markers are directories (.xcodeproj).
			for _, m := range projectMarkers {
				if strings.HasSuffix(d.Name(), m) {
					found[m] = true
				}
			}
			if depth >= maxDepth {
				return filepath.SkipDir
			}
			return nil
		}
		base := d.Name()
		for _, m := range projectMarkers {
			if strings.HasSuffix(base, m) {
				found[m] = true
			}
		}
		if len(found) == len(projectMarkers) {
			return fs.SkipAll
		}
		return nil
	})
	out := make([]string, 0, len(found))
	for m := range found {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}
