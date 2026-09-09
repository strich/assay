package ndjson

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestEmitWritesOneJSONLinePerEvent(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf)
	r.Emit("phase_start", map[string]any{"phase": "intake"})
	r.Logf("info", "hello")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want 2: %q", len(lines), buf.String())
	}
	var ev map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &ev); err != nil {
		t.Fatal(err)
	}
	if ev["event"] != "phase_start" || ev["phase"] != "intake" || ev["ts"] == "" {
		t.Fatalf("event = %v", ev)
	}
}

func TestNilReporterIsNoOp(t *testing.T) {
	var r *Reporter
	r.Emit("x", nil)
	r.Logf("info", "x")
}
