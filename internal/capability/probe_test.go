package capability

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProbeReportsStructureFromProjectFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Game.csproj"), []byte("<Project/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := Probe(dir)
	if !r.Text.Available {
		t.Error("Text must always be available")
	}
	if !r.Structure.Available || r.Structure.Reason == "" {
		t.Errorf("Structure = %+v, want available with a reason", r.Structure)
	}
	if r.Symbols.Available || r.Diagnostics.Available || r.History.Available {
		t.Errorf("unbuilt capabilities must report unavailable: %+v", r)
	}
}

func TestProbeEmptyPathOnlyText(t *testing.T) {
	r := Probe("")
	if !r.Text.Available || r.Structure.Available {
		t.Fatalf("raw-diff probe = %+v", r)
	}
}

func TestProbeNoProjectFiles(t *testing.T) {
	r := Probe(t.TempDir())
	if r.Structure.Available {
		t.Fatalf("Structure should be unavailable: %+v", r.Structure)
	}
}
