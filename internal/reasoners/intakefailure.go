package reasoners

// intakefailure.go: what Phase 1 reports when the model provider will not
// answer. Mirrors the Python node's IntakeUnavailableError /
// _intake_failure_message.

import (
	"strings"
)

// IntakeUnavailableError means Phase 1 could not classify the PR because the
// model provider failed, as opposed to producing an empty classification.
//
// A real run had aforge fail three times with \"API error (401): User not
// found.\" while IntakePhase still returned an empty map, which mapToStruct
// turned into a zero-valued IntakeResult — so the review continued with no
// pr_type, complexity or summary. The provider's own message is the only
// useful thing to report, so report it.
type IntakeUnavailableError struct{ Message string }

func (e *IntakeUnavailableError) Error() string { return e.Message }

// intakeFailureMessage explains an empty intake classification, quoting the
// provider verbatim. Both underlying calls are reported when both failed: they
// usually share a root cause, and the .ai() gate is where it surfaced first.
func intakeFailureMessage(harnessErr string, gateErr error) string {
	parts := []string{"intake classification produced no usable output"}
	if msg := strings.TrimSpace(harnessErr); msg != "" {
		parts = append(parts, "harness error: "+msg)
	}
	if gateErr != nil {
		if msg := strings.TrimSpace(gateErr.Error()); msg != "" {
			parts = append(parts, "ai gate error: "+msg)
		}
	}
	if len(parts) == 1 {
		// No provider message at all — say where to look rather than nothing.
		parts = append(parts,
			"the provider returned no error message either; check the node logs")
	}
	parts = append(parts,
		"verify the harness credentials and that ASSAY_MODEL is a model the "+
			"configured provider will serve")
	return strings.Join(parts, "; ")
}
