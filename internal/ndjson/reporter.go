// Package ndjson emits assay's progress as newline-delimited JSON on stdout.
// CI tails the process; there is no event bus, no reconnect logic, and no
// stream to lose. Every event is one line of JSON with an "event" name and an
// RFC3339Nano timestamp.
package ndjson

import (
	"encoding/json"
	"io"
	"sync"
	"time"
)

// Reporter serializes events to a writer. A nil *Reporter is a valid no-op, so
// callers never need a nil check.
type Reporter struct {
	mu      sync.Mutex
	w       io.Writer
	enc     *json.Encoder
	enabled bool
}

// New returns a reporter writing to w. A nil writer yields a disabled reporter.
func New(w io.Writer) *Reporter {
	if w == nil {
		return &Reporter{}
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return &Reporter{w: w, enc: enc, enabled: true}
}

// Emit writes one event line. fields may be nil. Field names are preserved;
// "event" and "ts" are set by the reporter and overwrite any supplied values.
func (r *Reporter) Emit(event string, fields map[string]any) {
	if r == nil || !r.enabled {
		return
	}
	line := make(map[string]any, len(fields)+2)
	for k, v := range fields {
		line[k] = v
	}
	line["event"] = event
	line["ts"] = time.Now().UTC().Format(time.RFC3339Nano)

	r.mu.Lock()
	defer r.mu.Unlock()
	_ = r.enc.Encode(line)
}

// Logf emits a human-readable log event.
func (r *Reporter) Logf(level, msg string) {
	r.Emit("log", map[string]any{"level": level, "message": msg})
}
