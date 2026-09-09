#!/usr/bin/env bash
# Run assay over one Martian-bench PR, pinned to a single model, as a dry run.
#
# This is the assay-side entry point for the parity check: run the recorded
# problems through the CLI and compare against scoreboard.jsonl. The archived
# campaign.py drives the old HTTP node and reproduces the recorded baseline; this
# script is what produces assay's numbers.
#
# Usage:
#   bash benchmark/martian-code-review-bench/scripts/run_node.sh <pr_url> [extra assay flags]
#
# Env:
#   ASSAY_MODEL_BUDGET/_MID/_PREMIUM   model ids (default: openrouter/z-ai/glm-5.2)
#   ASSAY_MAX_COST_USD                 default 8.0 (uncapped for quality)
#   ASSAY_MAX_DURATION_SECONDS         default 2400
#   ASSAY_WORKDIR                      clone workspace (default /tmp/assay-work)
#   OPENROUTER_API_KEY                 required
set -euo pipefail

if [[ $# -lt 1 ]]; then
  echo "usage: $0 <pr_url> [assay flags...]" >&2
  exit 2
fi

ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
cd "$ROOT"

if [[ -f .env ]]; then
  set -a
  # shellcheck disable=SC1091
  source .env
  set +a
fi

if [[ -z "${OPENROUTER_API_KEY:-}" ]]; then
  echo "[run_node] OPENROUTER_API_KEY is not set" >&2
  exit 1
fi

MODEL="${ASSAY_MODEL:-openrouter/z-ai/glm-5.2}"
export ASSAY_MODEL_BUDGET="${ASSAY_MODEL_BUDGET:-$MODEL}"
export ASSAY_MODEL_MID="${ASSAY_MODEL_MID:-$MODEL}"
export ASSAY_MODEL_PREMIUM="${ASSAY_MODEL_PREMIUM:-$MODEL}"
export ASSAY_MAX_COST_USD="${ASSAY_MAX_COST_USD:-8.0}"
export ASSAY_MAX_DURATION_SECONDS="${ASSAY_MAX_DURATION_SECONDS:-2400}"
export ASSAY_WORKDIR="${ASSAY_WORKDIR:-/tmp/assay-work}"

if [[ -z "${GH_TOKEN:-}" ]] && command -v gh >/dev/null 2>&1; then
  export GH_TOKEN="$(gh auth token 2>/dev/null || true)"
fi
[ -n "${GH_TOKEN:-}" ] && echo "[run_node] GH_TOKEN set" || echo "[run_node] WARNING: no GH_TOKEN"

mkdir -p bin
go build -o bin/assay ./cmd/assay

PR_URL="$1"
shift

echo "[run_node] model=$ASSAY_MODEL_PREMIUM budget=\$$ASSAY_MAX_COST_USD / ${ASSAY_MAX_DURATION_SECONDS}s"
exec bin/assay --pr "$PR_URL" --depth deep --dry-run --output json "$@"
