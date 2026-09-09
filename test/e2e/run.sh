#!/usr/bin/env bash
# run.sh — one-command offline E2E for assay.
#
# Builds the assay binary and a mock `opencode` CLI, creates a local fixture git
# repo with a two-commit diff, then runs the real seven-stage pipeline through
# the real CLI:
#
#   assay --repo <fixture> --base-ref HEAD~1 --head-ref HEAD --dry-run --output json
#
# The mock opencode answers every seam call deterministically, so the whole run
# is ZERO LLM and ZERO GitHub writes. The script asserts the run succeeded,
# produced findings, completed every phase, and that the expected reasoners were
# actually invoked (from ASSAY_MOCK_STATE_DIR/invocations.jsonl).
#
# There is no control plane, no registry, no API key and no network: that is the
# point of the CLI.
#
# Usage: ./run.sh [--keep]
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
TS="$(date +%Y%m%d-%H%M%S)"
RUN_DIR="$HERE/runs/$TS"
SHIM="$RUN_DIR/shim"
STATE="$RUN_DIR/state"
FIXTURE="$RUN_DIR/fixture-repo"
KEEP=0
[[ "${1:-}" == "--keep" ]] && KEEP=1

log() { printf '\033[1;36m[e2e]\033[0m %s\n' "$*"; }
ok()  { printf '\033[1;32m[ ok ]\033[0m %s\n' "$*"; }
err() { printf '\033[1;31m[FAIL]\033[0m %s\n' "$*" >&2; }

ASSERT_FAILS=0
assert() { if [[ "$1" -eq 0 ]]; then ok "$2"; else err "$2"; ASSERT_FAILS=$((ASSERT_FAILS + 1)); fi; }

# winpath converts an MSYS/Cygwin path to a native one so the Windows binaries
# under git-bash/WSL interop can open it. Identity on Linux/macOS.
winpath() {
  case "$(uname -s)" in
    MINGW*|MSYS*|CYGWIN*) cygpath -w "$1" ;;
    *) printf '%s' "$1" ;;
  esac
}

cleanup() {
  if [[ "$KEEP" -eq 1 ]]; then
    log "--keep: run artifacts left in $RUN_DIR"
  fi
}
trap cleanup EXIT

for bin in go git; do
  command -v "$bin" >/dev/null 2>&1 || { err "'$bin' is required but not on PATH"; exit 1; }
done

mkdir -p "$SHIM" "$STATE" "$FIXTURE"

# Go on Windows requires an .exe suffix for exec.LookPath to find a binary.
EXE=""
case "$(uname -s)" in
  MINGW*|MSYS*|CYGWIN*) EXE=".exe" ;;
esac

# ---------------------------------------------------------------------------
# 1. Build assay and the mock opencode CLI
# ---------------------------------------------------------------------------
log "building mockcli -> $SHIM/opencode$EXE"
(cd "$ROOT" && GOWORK=off go build -o "$SHIM/opencode$EXE" ./test/mockcli/) || { err "mockcli build failed"; exit 1; }
log "building assay -> $RUN_DIR/assay$EXE"
(cd "$ROOT" && GOWORK=off go build -o "$RUN_DIR/assay$EXE" ./cmd/assay/) || { err "assay build failed"; exit 1; }

# Materialize the scenario in sync with the mock's baked default.
"$SHIM/opencode$EXE" -dump-scenario > "$RUN_DIR/scenario.json"

# ---------------------------------------------------------------------------
# 2. Fixture repo: commit 1, then a change on HEAD
# ---------------------------------------------------------------------------
log "creating fixture repo at $FIXTURE"
(
  cd "$FIXTURE"
  git init -q
  git config user.email e2e@example.invalid
  git config user.name e2e
  cat > service.py <<'EOF'
def fetch(url):
    return {"url": url}
EOF
  git add -A && git commit -qm "add service"
  cat > service.py <<'EOF'
def fetch(url):
    result = {"url": url}
    return result


def retry(fn, attempts=3):
    last = None
    for _ in range(attempts):
        try:
            return fn()
        except Exception as exc:  # noqa: BLE001
            last = exc
    raise last
EOF
  git add -A && git commit -qm "add retry wrapper"
)

# ---------------------------------------------------------------------------
# 3. Run the CLI end to end
# ---------------------------------------------------------------------------
log "running assay"
set +e
PATH="$SHIM:$PATH" \
ASSAY_OPENCODE_BIN="$(winpath "$SHIM/opencode$EXE")" \
ASSAY_MOCK_STATE_DIR="$(winpath "$STATE")" \
ASSAY_MOCK_SCENARIO="$(winpath "$RUN_DIR/scenario.json")" \
ASSAY_MODEL_BUDGET="anthropic/mock" \
ASSAY_MODEL_MID="anthropic/mock" \
ASSAY_MODEL_PREMIUM="anthropic/mock" \
ANTHROPIC_API_KEY="offline-mock" \
"$RUN_DIR/assay$EXE" --repo "$(winpath "$FIXTURE")" --base-ref HEAD~1 --head-ref HEAD \
  --dry-run --output json > "$RUN_DIR/result.json" 2> "$RUN_DIR/stderr.log"
RUN_EXIT=$?
set -e

if [[ "$RUN_EXIT" -eq 0 ]]; then
  ok "assay exited 0"
else
  err "assay exited $RUN_EXIT (see $RUN_DIR/stderr.log)"
  ASSERT_FAILS=$((ASSERT_FAILS + 1))
fi

# ---------------------------------------------------------------------------
# 4. Assert the result
# ---------------------------------------------------------------------------
RESULT="$RUN_DIR/result.json"
grep -q '"total_findings":[1-9]' "$RESULT"
assert $? "result contains findings"

grep -q '"body":"[^"]' "$RESULT"
assert $? "result carries a non-empty review body"

for phase in intake anatomy meta_selectors review adversary cross_ref coverage synthesis output; do
  grep -q "\"$phase\"" "$RESULT"
  assert $? "phase completed: $phase"
done

# ---------------------------------------------------------------------------
# 5. Assert the reasoners actually ran (mock invocation log)
# ---------------------------------------------------------------------------
INV="$STATE/invocations.jsonl"
for role in intake_gate meta_semantic review_dimension evidence_verifier adversary extract_obligations coverage_gate; do
  grep -q "\"role\":\"$role\"" "$INV"
  assert $? "reasoner invoked: $role"
done

log "artifacts: $RUN_DIR"
if [[ "$ASSERT_FAILS" -gt 0 ]]; then
  err "$ASSERT_FAILS assertion(s) failed"
  exit 1
fi
ok "e2e passed"
