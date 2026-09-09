// Package assay is a single-binary code reviewer for GitHub Actions.
//
// It builds a review plan specific to each pull request, runs focused reviewers
// against that plan, and reports only the findings that survive an adversarial
// challenge. The name is the job: an assay establishes what a sample actually
// contains, by test rather than by opinion.
//
// The pipeline has seven stages:
//
//	intake → anatomy → meta-selectors → review → layer → synthesis → output
//
// Intake classifies the PR and sets the depth. Anatomy clusters the changed
// files into coherent units. Three meta-selectors design the review dimensions.
// One focused reviewer runs per dimension, streaming findings. The layer
// verifies findings against real source, challenges them adversarially, and
// synthesizes compound risks. Synthesis scores, dedups and normalises severity
// with no model involved. Output maps findings to diff lines and posts.
//
// The whole tool is one process and one command:
//
//	assay --pr https://github.com/owner/repo/pull/123
//
// Every model call goes through one seam (internal/harnessx), which invokes
// `opencode run --format json` directly. That seam owns the model string, the
// schema-validated structured output, the bounded retry, the subprocess timeout
// and the measured cost and token accounting, so budget caps are enforced on
// measured spend rather than an estimate. Progress is newline-delimited JSON on
// stdout. It is a batch job: stateless, rerunnable, and safe to run again.
package assay
