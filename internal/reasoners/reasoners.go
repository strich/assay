// Package reasoners ports the 16 router reasoners of the assay pipeline
// (intake_phase … coverage_gate).
//
// Each reasoner is a plain function
//
//	func XxxPhase(ctx context.Context, deps Deps, in XxxInput) (map[string]any, error)
//
// that builds its prompt with the byte-verbatim builders in internal/prompts,
// invokes the model through the single harnessx seam, and returns the exact
// snake_case key set the pipeline consumes. When the model could not produce a
// schema-valid result, each reasoner applies its own deterministic fallback — a
// seeded default struct, an empty key set, or a keep-everything index list —
// never a silent success. Transport errors propagate.
//
// The prompts are the benchmark-validated component and port byte for byte. Do
// not improve them here; any prompt change is a separate, measured commit.
package reasoners

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/strich/assay/internal/harnessx"
)

// Deps carries the single injectable capability a reasoner uses: the LLM seam.
type Deps struct {
	LLM harnessx.Caller
}

// aiStructured runs a structured call through the same seam as every other
// reasoner. It exists for the two non-agentic gates (intake, coverage), which
// supply a strict schema explicitly rather than deriving one from a Go type.
//
// A transport error is returned as-is (the caller applies its gate fallback);
// a schema-invalid result is an error naming the cause. The runner owns the
// bounded retry, so there is no second retry loop here.
func aiStructured(ctx context.Context, caller harnessx.Caller, prompt, system string, schema json.RawMessage, dest any, role string) error {
	if caller == nil {
		return fmt.Errorf("reasoners: LLM seam is nil")
	}
	destV := reflect.ValueOf(dest)
	if destV.Kind() != reflect.Pointer || destV.IsNil() {
		return fmt.Errorf("reasoners: aiStructured dest must be a non-nil pointer, got %T", dest)
	}
	var schemaMap map[string]any
	if len(schema) > 0 {
		if err := json.Unmarshal(schema, &schemaMap); err != nil {
			return fmt.Errorf("reasoners: invalid strict schema: %w", err)
		}
	}
	res, err := caller.Run(ctx, prompt, schemaMap, dest, harnessx.Options{SystemPrompt: system, Role: role})
	if err != nil {
		return err
	}
	if res == nil || res.Parsed == nil {
		msg := "schema validation failed"
		if res != nil && res.ErrorMessage != "" {
			msg = res.ErrorMessage
		}
		return fmt.Errorf("Could not parse structured response: %s", msg)
	}
	return nil
}

// dumpMap is the Go analogue of pydantic model_dump(): a JSON round trip that
// yields a map containing every field (the schemas structs carry no omitempty),
// with nil pointers as JSON null.
func dumpMap(v any) (map[string]any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("reasoners: dump %T: %w", v, err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("reasoners: dump %T: %w", v, err)
	}
	return m, nil
}

// dumpSlice dumps each element like `[x.model_dump() for x in xs]`. A nil slice
// dumps to an empty (non-nil) list.
func dumpSlice[T any](xs []T) ([]any, error) {
	out := make([]any, 0, len(xs))
	for i := range xs {
		m, err := dumpMap(&xs[i])
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}
