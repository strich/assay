#!/usr/bin/env bash
# Launch the archived PR-AF Python node with the whole pipeline pinned to GLM-5.2
# via OpenRouter. This reproduces the recorded baseline in ../scoreboard.jsonl;
# the assay parity runner is run_node.sh.
#
# The archived node is not part of this repository. Point PR_AF_ROOT at a pr-af
# checkout (basis 76ad7f3 or the commit the baseline was recorded on).
set -euo pipefail

if [[ -z "${PR_AF_ROOT:-}" || ! -f "${PR_AF_ROOT}/main.py" ]]; then
  echo "run_node_legacy.sh: set PR_AF_ROOT to a pr-af checkout containing main.py" >&2
  exit 2
fi
cd "$PR_AF_ROOT"

if [ -f .env ]; then
  set -a
  # shellcheck disable=SC1091
  source .env
  set +a
fi

export NODE_ID=pr-af
export AGENTFIELD_SERVER="${AGENTFIELD_SERVER:-http://localhost:8080}"
export AGENT_CALLBACK_URL="${AGENT_CALLBACK_URL:-http://127.0.0.1:8004}"

# --- the experiment: GLM-5.2 everywhere ---
export PR_AF_PROVIDER=opencode
export PR_AF_MODEL=openrouter/z-ai/glm-5.2     # .harness() -> opencode -m
export PR_AF_AI_MODEL=openrouter/z-ai/glm-5.2  # .ai()      -> litellm (needs openrouter/ prefix too)
export PR_AF_MAX_TURNS=60

# Generous budget for the hardest Martian-bench PR (keycloak/keycloak#32918).
export PR_AF_MAX_COST_USD=8.0
export PR_AF_MAX_DURATION_SECONDS=2400

export PR_AF_WORKDIR="${PR_AF_WORKDIR:-/tmp/pr-af-work}"

# Authenticated GitHub token (from gh CLI) so fetch_pr + clone are not capped at
# the 60 req/hr unauthenticated limit during a 38-PR campaign.
if [ -z "${GH_TOKEN:-}" ]; then
  export GH_TOKEN="$(gh auth token 2>/dev/null || true)"
fi
[ -n "${GH_TOKEN:-}" ] && echo "[run_node_legacy] GH_TOKEN set" || echo "[run_node_legacy] WARNING: no GH_TOKEN"

echo "[run_node_legacy] model=$PR_AF_MODEL server=$AGENTFIELD_SERVER budget=\$$PR_AF_MAX_COST_USD / ${PR_AF_MAX_DURATION_SECONDS}s"
exec uv run python main.py
