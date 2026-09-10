package orch

import (
	"testing"

	"github.com/strich/assay/internal/config"
	"github.com/strich/assay/internal/schemas"
)

func TestMatchGlob(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		{"*.md", "README.md", true},
		{"*.md", "docs/README.md", false},
		{".github/**", ".github/workflows/ci.yml", true},
		{"vendor/**", "vendor/lib/x.go", true},
		{"**/*.generated.*", "src/deep/Foo.generated.cs", true},
		{"**/*.generated.*", "Foo.generated.cs", true},
		{"**/*.min.js", "web/app.min.js", true},
		{"**/package-lock.json", "package-lock.json", true},
		{"src/*.go", "src/main.go", true},
		{"src/*.go", "src/sub/main.go", false},
		{"?.go", "a.go", true},
		{"?.go", "ab.go", false},
	}
	for _, tc := range cases {
		if got := matchGlob(tc.pattern, tc.path); got != tc.want {
			t.Errorf("matchGlob(%q, %q) = %v, want %v", tc.pattern, tc.path, got, tc.want)
		}
	}
}

func TestApplyIgnoresDropsMatchingChangedFiles(t *testing.T) {
	o := New(Deps{LLM: &fakeLLM{}}, schemas.ReviewInput{}, config.DefaultReviewConfig())
	o.config.IgnorePaths = []string{"**/*.generated.*", "vendor/**"}
	o.prData = &schemas.GitHubPRData{ChangedFiles: []schemas.ChangedFile{
		{Path: "src/main.go"},
		{Path: "src/Thing.generated.cs"},
		{Path: "vendor/lib/x.go"},
	}}
	o.applyIgnores()
	if len(o.prData.ChangedFiles) != 1 || o.prData.ChangedFiles[0].Path != "src/main.go" {
		t.Fatalf("changed files = %+v, want only src/main.go", o.prData.ChangedFiles)
	}
}
