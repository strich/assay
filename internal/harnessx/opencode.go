package harnessx

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/strich/assay/internal/budget"
)

// This file parses opencode's `--format json` event stream. opencode emits one
// JSON object per line (step_start / text / tool_use / step_finish / error),
// from which assay recovers the final message, per-step cost, token counts and
// turn count — the measured figures the budget cap is enforced on.

// rawAttempt is one opencode process's parsed outcome, before schema handling.
type rawAttempt struct {
	Prompt       string
	Result       string
	Stdout       string
	Stderr       string
	ExitCode     int
	IsError      bool
	ErrorMessage string
	FailureType  FailureType
	CostUSD      *float64
	Tokens       budget.Tokens
	NumTurns     int
	DurationMS   int
	Attempt      Attempt
}

// toAttempt snapshots the attempt verbatim for the Result.
func (r rawAttempt) toAttempt() Attempt {
	return Attempt{
		Prompt:       r.Prompt,
		Result:       r.Result,
		Stdout:       r.Stdout,
		Stderr:       r.Stderr,
		ExitCode:     r.ExitCode,
		ErrorMessage: r.ErrorMessage,
		CostUSD:      r.CostUSD,
		Tokens:       r.Tokens,
		DurationMS:   r.DurationMS,
	}
}

// parseOpenCodeEvents decodes the JSONL stream, skipping lines that are not
// valid JSON (opencode interleaves plain-text log lines).
func parseOpenCodeEvents(stdout string) []map[string]any {
	var events []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		events = append(events, event)
	}
	return events
}

// finalText reconstructs the final assistant text from the event stream.
func finalText(events []map[string]any) string {
	var resultText string
	var currentParts []string

	for _, event := range events {
		eventType, _ := event["type"].(string)
		switch eventType {
		case "step_start":
			currentParts = nil
		case "item.completed":
			if item, ok := event["item"].(map[string]any); ok {
				if it, _ := item["type"].(string); it == "agent_message" {
					if text, ok := item["text"].(string); ok && text != "" {
						resultText = text
					}
				}
			}
		case "result":
			if r, ok := event["result"].(string); ok {
				resultText = r
			} else if r, ok := event["text"].(string); ok {
				resultText = r
			}
		case "turn.completed":
			if text, ok := event["text"].(string); ok && text != "" {
				resultText = text
			}
		case "message", "assistant":
			if content, ok := event["content"].(string); ok && content != "" {
				resultText = content
			} else if content, ok := event["text"].(string); ok && content != "" {
				resultText = content
			}
		case "text":
			content := stringField(event, "text")
			if content == "" {
				content = stringField(event, "content")
			}
			if content == "" {
				if part, ok := event["part"].(map[string]any); ok {
					content = stringField(part, "text")
				}
			}
			if content != "" {
				currentParts = append(currentParts, content)
				resultText = strings.Join(currentParts, "")
			}
		}
	}
	return resultText
}

// turnsFromEvents counts one turn per step_start, or per tool_use when the
// stream carries no step markers.
func turnsFromEvents(events []map[string]any) int {
	stepStarts, toolUses := 0, 0
	for _, event := range events {
		switch t, _ := event["type"].(string); t {
		case "step_start":
			stepStarts++
		case "tool_use":
			toolUses++
		}
	}
	if stepStarts > 0 {
		return stepStarts
	}
	return toolUses
}

// costFromEvents sums per-step costs from step_finish events, returning nil
// when no step reported a cost (unknown, not free).
func costFromEvents(events []map[string]any) *float64 {
	total := 0.0
	found := false
	for _, event := range events {
		if t, _ := event["type"].(string); t != "step_finish" {
			continue
		}
		part, ok := event["part"].(map[string]any)
		if !ok {
			continue
		}
		if cost, ok := part["cost"].(float64); ok {
			total += cost
			found = true
		}
	}
	if !found {
		return nil
	}
	return &total
}

// tokensFromEvents sums per-step token counts from step_finish.part.tokens.
func tokensFromEvents(events []map[string]any) budget.Tokens {
	var total budget.Tokens
	for _, event := range events {
		if t, _ := event["type"].(string); t != "step_finish" {
			continue
		}
		part, ok := event["part"].(map[string]any)
		if !ok {
			continue
		}
		tokens, ok := part["tokens"].(map[string]any)
		if !ok {
			continue
		}
		total.Input += intField(tokens, "input")
		total.Output += intField(tokens, "output") + intField(tokens, "reasoning")
		if cache, ok := tokens["cache"].(map[string]any); ok {
			total.CacheRead += intField(cache, "read")
			total.CacheWrite += intField(cache, "write")
		}
	}
	return total
}

// eventError pulls a meaningful failure message from an in-band "error" event.
func eventError(events []map[string]any) string {
	for _, event := range events {
		if t, _ := event["type"].(string); t != "error" {
			continue
		}
		for _, key := range []string{"message", "error", "text"} {
			if v := strings.TrimSpace(stringField(event, key)); v != "" {
				return truncate(v, 1000)
			}
		}
		if part, ok := event["part"].(map[string]any); ok {
			for _, key := range []string{"message", "error", "text"} {
				if v := strings.TrimSpace(stringField(part, key)); v != "" {
					return truncate(v, 1000)
				}
			}
		}
		if b, err := json.Marshal(event); err == nil {
			return truncate(string(b), 1000)
		}
		return ""
	}
	return ""
}

// stderrErrorPatterns mark stderr that carries a real failure rather than noise
// such as the one-time SQLite migration prelude.
var stderrErrorPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?m)^Error:`),
	regexp.MustCompile(`\bModel not found\b`),
	regexp.MustCompile(`\bAuthenticationError\b`),
	regexp.MustCompile(`\bUnauthorized\b`),
	regexp.MustCompile(`\bAPIError\b`),
	regexp.MustCompile(`\binvalid.{0,10}api.?key\b`),
	regexp.MustCompile(`\bcredit balance\b`),
}

func matchesStderrError(stderr string) bool {
	for _, pat := range stderrErrorPatterns {
		if pat.MatchString(stderr) {
			return true
		}
	}
	return false
}

// extractStderrError pulls the meaningful failure line and a small context
// window out of stderr.
func extractStderrError(stderr string) string {
	lines := strings.Split(stderr, "\n")
	for i, line := range lines {
		for _, pat := range stderrErrorPatterns {
			if pat.MatchString(line) {
				start := i - 1
				if start < 0 {
					start = 0
				}
				end := i + 5
				if end > len(lines) {
					end = len(lines)
				}
				return truncate(strings.TrimSpace(strings.Join(lines[start:end], "\n")), 1000)
			}
		}
	}
	return truncate(stderr, 1000)
}

// stringField returns m[key] when it is a non-empty string, else "".
func stringField(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// intField returns m[key] as an int, or 0.
func intField(m map[string]any, key string) int {
	switch v := m[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return int(n)
		}
	}
	return 0
}

// truncate caps s at n runes.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
