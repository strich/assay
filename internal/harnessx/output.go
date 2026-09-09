package harnessx

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

// Structured output is a written-file contract: opencode's agent writes JSON to
// a deterministic path and assay validates it against the caller's schema. This
// is weaker than native tool-calling, so every failed attempt's raw output is
// preserved in Result.Attempts. The mitigation is logging, not cleverness.

const (
	outputFilename = ".assay_output.json"
	schemaFilename = ".assay_schema.json"
	// largeSchemaTokenThreshold is the rough size (chars/4) past which the
	// schema is spilled to a file instead of inlined in the prompt.
	largeSchemaTokenThreshold = 4000
)

// OutputPath is the deterministic structured-output file for a call.
func OutputPath(dir string) string { return filepath.Join(dir, outputFilename) }

// SchemaPath is the file the schema is spilled to for large schemas.
func SchemaPath(dir string) string { return filepath.Join(dir, schemaFilename) }

// buildOutputSuffix renders the OUTPUT REQUIREMENTS instruction.
func buildOutputSuffix(schema map[string]any, dir string) string {
	outputPath := OutputPath(dir)
	schemaJSON, err := json.MarshalIndent(schema, "", "  ")
	if err != nil {
		return fmt.Sprintf(
			"\n\n---\n"+
				"CRITICAL OUTPUT REQUIREMENTS:\n"+
				"You MUST use your Write tool to create this file: %s\n"+
				"The file MUST contain ONLY valid JSON.\n"+
				"Do NOT output the JSON in your response text — write it to the file.",
			outputPath,
		)
	}
	if estimateTokens(string(schemaJSON)) > largeSchemaTokenThreshold {
		_ = writeSchemaFile(string(schemaJSON), dir)
		return fmt.Sprintf(
			"\n\n---\n"+
				"CRITICAL OUTPUT REQUIREMENTS:\n"+
				"Read the JSON Schema at: %s\n"+
				"You MUST use your Write tool to create this file: %s\n"+
				"The file MUST contain ONLY valid JSON conforming to that schema.\n"+
				"Do NOT output the JSON in your response text — write it to the file.",
			SchemaPath(dir), outputPath,
		)
	}
	return fmt.Sprintf(
		"\n\n---\n"+
			"CRITICAL OUTPUT REQUIREMENTS:\n"+
			"You MUST use your Write tool to create this file: %s\n"+
			"The file MUST contain ONLY valid JSON matching the schema below.\n"+
			"Do NOT output the JSON in your response text — write it to the file.\n\n"+
			"Required JSON Schema:\n%s\n\n"+
			"Write ONLY valid JSON to the file. No markdown fences, no comments, no extra text.",
		outputPath, string(schemaJSON),
	)
}

// buildFollowupPrompt renders the retry prompt after a schema failure. The
// "PREVIOUS ATTEMPT FAILED" marker is deliberate: test harnesses key off it to
// leave the partial output file untouched rather than clobbering it.
func buildFollowupPrompt(errorMessage, dir string, schema map[string]any) string {
	outputPath := OutputPath(dir)
	schemaPath := SchemaPath(dir)

	var b strings.Builder
	fmt.Fprintf(&b, "PREVIOUS ATTEMPT FAILED. The JSON output at %s failed validation.\n", outputPath)
	fmt.Fprintf(&b, "Error: %s\n\n", errorMessage)

	if schema != nil {
		schemaJSON, err := json.MarshalIndent(schema, "", "  ")
		if err != nil {
			fmt.Fprintf(&b, "The schema could not be serialized (%v).\n", err)
			fmt.Fprintf(&b, "Write valid JSON to %s and include all expected top-level fields.\n\n", outputPath)
		} else if estimateTokens(string(schemaJSON)) > largeSchemaTokenThreshold {
			if _, statErr := os.Stat(schemaPath); statErr == nil {
				fmt.Fprintf(&b, "The required JSON Schema is at: %s\nRe-read the schema file carefully.\n", schemaPath)
			} else {
				_ = writeSchemaFile(string(schemaJSON), dir)
				fmt.Fprintf(&b, "The required JSON Schema has been written to: %s\nRead that file for the exact expected structure.\n", schemaPath)
			}
		} else {
			fmt.Fprintf(&b, "The JSON MUST conform to this schema:\n%s\n\n", string(schemaJSON))
		}
	}

	fmt.Fprintf(&b, "Use your Write tool to create or overwrite the file: %s\n", outputPath)
	b.WriteString("The file must contain ONLY valid JSON matching the schema. No markdown fences, no extra text, no comments.\n")
	b.WriteString("Each field defined in the schema must be present as a top-level key in your JSON object.")
	return b.String()
}

// estimateTokens gives a rough token count (~4 chars per token).
func estimateTokens(text string) int { return len(text) / 4 }

// writeSchemaFile writes the schema JSON beside the output file.
func writeSchemaFile(schemaJSON, dir string) error {
	if err := os.MkdirAll(filepath.Dir(SchemaPath(dir)), 0o700); err != nil {
		return err
	}
	return os.WriteFile(SchemaPath(dir), []byte(schemaJSON), 0o600)
}

// parseAndValidate reads the written output file, falls back to JSON extracted
// from the model's final text, then enforces the schema. dest is populated on
// success.
func parseAndValidate(dir string, schema map[string]any, dest any, stdoutFallback string) (map[string]any, error) {
	outputPath := OutputPath(dir)

	data, err := readAndParse(outputPath)
	if err != nil && stdoutFallback != "" {
		data, err = tryParseFromText(stdoutFallback)
	}
	if err != nil {
		return nil, fmt.Errorf("%s", diagnoseOutputFailure(outputPath, schema))
	}
	if verr := validateAgainstSchema(data, schema); verr != nil {
		return nil, verr
	}
	if dest != nil {
		b, mErr := json.Marshal(data)
		if mErr == nil {
			if uErr := json.Unmarshal(b, dest); uErr != nil {
				return nil, fmt.Errorf("output does not match the expected shape: %v", uErr)
			}
		}
	}
	return data, nil
}

// readAndParse reads a JSON object, applying cosmetic repair on retry.
func readAndParse(filePath string) (map[string]any, error) {
	raw, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	content := strings.TrimSpace(string(raw))
	if content == "" {
		return nil, fmt.Errorf("empty file")
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(content), &out); err == nil {
		return out, nil
	}
	repaired := cosmeticRepair(content)
	if err := json.Unmarshal([]byte(repaired), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// cosmeticRepair strips markdown fences, leading prose, trailing commas and
// unclosed braces/brackets. It is naive about braces inside strings, which is
// why it is a fallback and not the first parser.
func cosmeticRepair(raw string) string {
	text := strings.TrimSpace(raw)
	fenceRe := regexp.MustCompile("(?s)^```(?:json)?\\s*\n(.*?)```\\s*$")
	if m := fenceRe.FindStringSubmatch(text); len(m) > 1 {
		text = strings.TrimSpace(m[1])
	}
	if len(text) > 0 && text[0] != '{' && text[0] != '[' {
		for i, ch := range text {
			if ch == '{' || ch == '[' {
				text = text[i:]
				break
			}
		}
	}
	text = regexp.MustCompile(`,\s*([}\]])`).ReplaceAllString(text, "$1")
	openBraces := strings.Count(text, "{") - strings.Count(text, "}")
	openBrackets := strings.Count(text, "[") - strings.Count(text, "]")
	if openBrackets > 0 {
		text += strings.Repeat("]", openBrackets)
	}
	if openBraces > 0 {
		text += strings.Repeat("}", openBraces)
	}
	return text
}

// tryParseFromText extracts JSON from the model's final text when it ignored
// the instruction to write a file.
func tryParseFromText(text string) (map[string]any, error) {
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("empty text")
	}
	fenceRe := regexp.MustCompile("(?s)```(?:json)?\\s*\n(.*?)```")
	for _, m := range fenceRe.FindAllStringSubmatch(text, -1) {
		if len(m) > 1 {
			var data map[string]any
			if err := json.Unmarshal([]byte(strings.TrimSpace(m[1])), &data); err == nil {
				return data, nil
			}
		}
	}
	for _, candidate := range extractJSONBlocks(text) {
		var data map[string]any
		if err := json.Unmarshal([]byte(candidate), &data); err == nil {
			return data, nil
		}
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(cosmeticRepair(text)), &data); err == nil {
		return data, nil
	}
	return nil, fmt.Errorf("could not extract valid JSON from text")
}

// extractJSONBlocks finds top-level { ... } blocks, largest first.
func extractJSONBlocks(text string) []string {
	var candidates []string
	depth, start := 0, -1
	for i, ch := range text {
		switch ch {
		case '{':
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			depth--
			if depth == 0 && start >= 0 {
				candidates = append(candidates, text[start:i+1])
				start = -1
			}
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return len(candidates[i]) > len(candidates[j]) })
	return candidates
}

// validateAgainstSchema compiles and runs the caller's JSON schema. A schema
// that cannot compile is an error: silently accepting unvalidated output is
// exactly the failure this validation exists to prevent.
func validateAgainstSchema(data, schema map[string]any) error {
	schemaBytes, err := json.Marshal(schema)
	if err != nil {
		return fmt.Errorf("schema is not serializable: %w", err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("mem://assay/schema.json", bytes.NewReader(schemaBytes)); err != nil {
		return fmt.Errorf("schema cannot be registered: %w", err)
	}
	compiled, err := compiler.Compile("mem://assay/schema.json")
	if err != nil {
		return fmt.Errorf("schema cannot be compiled: %w", err)
	}
	dataBytes, err := json.Marshal(data)
	if err != nil {
		return nil
	}
	var normalized any
	if err := json.Unmarshal(dataBytes, &normalized); err != nil {
		return nil
	}
	if verr := compiled.Validate(normalized); verr != nil {
		return fmt.Errorf("schema validation failed: %s", conciseSchemaError(verr))
	}
	return nil
}

// conciseSchemaError flattens a validation error tree into leaf failures.
func conciseSchemaError(err error) string {
	ve, ok := err.(*jsonschema.ValidationError)
	if !ok {
		return truncate(err.Error(), 300)
	}
	var leaves []string
	var walk func(e *jsonschema.ValidationError)
	walk = func(e *jsonschema.ValidationError) {
		if len(e.Causes) == 0 {
			loc := e.InstanceLocation
			if loc == "" {
				loc = "/"
			}
			leaves = append(leaves, fmt.Sprintf("%s: %s", loc, e.Message))
			return
		}
		for _, c := range e.Causes {
			walk(c)
		}
	}
	walk(ve)
	if len(leaves) == 0 {
		return truncate(ve.Message, 300)
	}
	sort.Strings(leaves)
	return truncate(strings.Join(leaves, "; "), 400)
}

// diagnoseOutputFailure explains why the output file failed validation.
func diagnoseOutputFailure(filePath string, schema map[string]any) string {
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return "The output file was NOT created."
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Sprintf("Could not read output file: %v", err)
	}
	content := strings.TrimSpace(string(data))
	if content == "" {
		return "The output file exists but is empty."
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		snippet := content
		if len(snippet) > 500 {
			snippet = snippet[:500]
		}
		return fmt.Sprintf("The file contains invalid JSON. Parse error: %v\nFile content (first 500 chars):\n%s", err, snippet)
	}
	props, _ := schema["properties"].(map[string]any)
	expected := make([]string, 0, len(props))
	for k := range props {
		expected = append(expected, k)
	}
	actual := make([]string, 0, len(parsed))
	for k := range parsed {
		actual = append(actual, k)
	}
	sort.Strings(expected)
	sort.Strings(actual)
	return fmt.Sprintf("JSON parses but may not match expected schema.\nExpected top-level keys: %v\nActual top-level keys: %v", expected, actual)
}
