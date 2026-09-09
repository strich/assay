# assay

A single-binary code reviewer for GitHub Actions. It builds a review plan
specific to each pull request, runs focused reviewers against that plan, and
reports only the findings that survive an adversarial challenge. The name is the
job: an assay establishes what a sample actually contains, by test rather than
by opinion.

```bash
assay --pr https://github.com/owner/repo/pull/123
```

It clones the PR, runs the pipeline, posts the findings that survive, and exits
with a status. No daemon, no registry, no callback URL.

## Quick start

```bash
go build -o bin/assay ./cmd/assay

export OPENROUTER_API_KEY=...        # or ANTHROPIC_API_KEY / OPENAI_API_KEY / GOOGLE_API_KEY
export GH_TOKEN=...                  # required to post; pass --dry-run to skip
bin/assay --pr https://github.com/owner/repo/pull/123
```

Review a local repository or a raw diff instead:

```bash
assay --repo . --base-ref main --head-ref HEAD --dry-run
assay --diff changes.diff --dry-run --output json
```

`assay --help` lists every flag. Exit codes: `0` success, `2` usage or bad
input, `3` configuration or credential failure, `4` review failed.

## How it works

| # | Stage | Kind | What it does |
|---|---|---|---|
| 1 | Intake | LLM + gate | Classify PR type and complexity; derive review depth. |
| 2 | Anatomy | code + LLM | Cluster changed files into coherent units of change. |
| 3 | Meta-selectors | 3 parallel LLM | Semantic, mechanical and systemic lenses each propose review dimensions. |
| 4 | Review | N parallel LLM | One focused reviewer per dimension, findings stream as they land. |
| 5 | Layer | LLM, adversarial | Verify findings against real source, challenge them, synthesize compound risks. |
| 6 | Synthesis | deterministic | Score, dedup, normalise severity, decide what blocks. No model involved. |
| 7 | Output | deterministic | Map findings to diff lines and post. |

The prompts in `internal/prompts/` are the benchmark-validated component and
port byte for byte. Any prompt change is a separate, measured commit — never
bundled into a migration.

## The single seam

Every model call goes through `internal/harnessx`, which invokes
`opencode run --format json` directly. There is no SDK in between, so assay
sees the child's stdout and stderr verbatim, the per-step cost and token counts
opencode reports, and the raw output of every failed schema attempt.

- **One model string, one owner.** A role resolves to a tier; a tier resolves to
  a model. That happens in one function.
- **Structured output is schema-validated with a bounded retry.** opencode's
  contract is a written file rather than native tool-calling, which is weaker,
  so the raw output of every attempt is preserved on failure.
- **Every subprocess has an explicit, configurable timeout.**
- **Budget is an accountant, not a hope.** Cost and tokens come back from
  opencode per invocation, are tallied against a configurable ceiling, and are
  reported either way.

## Configuration

Blank means unset, everywhere, at read time.

| Env | Default | Purpose |
|---|---|---|
| `ASSAY_MODEL_BUDGET` | `openrouter/deepseek/deepseek-v4-flash-0731` | Model for the budget tier. |
| `ASSAY_MODEL_MID` | same | Model for the mid tier. |
| `ASSAY_MODEL_PREMIUM` | same | Model for the premium tier. |
| `ASSAY_OPENCODE_BIN` | `opencode` | Path to the opencode binary. |
| `ASSAY_MAX_COST_USD` | `2.0` | Per-review cost ceiling, enforced on measured spend. |
| `ASSAY_MAX_DURATION_SECONDS` | `3600` | Per-review wall-clock ceiling. |
| `ASSAY_SCHEMA_RETRIES` | `2` | Follow-up attempts after schema-invalid output. |
| `ASSAY_TRANSIENT_RETRIES` | `2` | Retries for transient provider errors. |
| `ASSAY_LLM_TIMEOUT_SECONDS` | `1800` | Per-invocation subprocess timeout. |
| `ASSAY_GIT_TIMEOUT_SECONDS` | `600` | Every git subprocess timeout. |
| `ASSAY_SKIP_GIT_LFS` | `1` | Skip LFS smudge; reviews read source, not binaries. |
| `ASSAY_WORKDIR` | temp dir | Clone workspace. |
| `ASSAY_REPO_PATH` | cwd | Fallback working tree when no repo/diff/PR is given. |
| `ASSAY_EVIDENCE_PACK` | `1` | Pre-read dimension target files for reviewers. |
| `ASSAY_POSTWORTHINESS_GATE` | `0` | Extra precision pass before the heavy layer. |
| `OPENROUTER_API_KEY` etc. | — | Provider credentials, forwarded to opencode. |
| `GH_TOKEN` | — | GitHub token; required to post. |
| `GITHUB_APP_ID` / `GITHUB_APP_PRIVATE_KEY` | — | Optional GitHub App auth (installation tokens). |

### Model tiers and roles

| Role | Tier | Why |
|---|---|---|
| `intake_gate` | budget | Fast classification. |
| `intake_fallback` | mid | Only when the cheap gate is unsure. |
| `anatomy_semantic` | mid | Narrative understanding of the change. |
| `planner` | premium | Designs the review; everything inherits its blind spots. |
| `reviewer` | premium | Reads the code; most tokens and cost. The depth profile overrides this tier. |
| `cross_ref` | premium | Interaction between findings needs the strongest reasoning. |
| `adversary` | premium | Precision comes from here; a weak critic confirms everything. |
| `coverage_gate` | budget | Near-mechanical completeness check. |
| `dedup_gate` | budget | Near-mechanical near-duplicate detection. |

The tier-to-model assignments are deliberately configuration, not policy: a
routine PR on budget models and a release PR on frontier models are the same
code path with different numbers. Defaulting every tier to one model is honest
until the assignments are chosen from evidence — for the critic roles, prefer a
different model family, not a bigger sibling.

## Progress and spend

Progress is newline-delimited JSON on stdout. One event per line:

```json
{"event":"phase_start","phase":"review","dimensions":6,"ts":"..."}
{"event":"reviewer_done","dimension":"Error handling","findings":3,"ts":"..."}
{"event":"llm_call","role":"reviewer","tier":"premium","model":"...","cost_usd":0.12,...}
{"event":"spend","cost_usd":1.04,"cost_known":true,"llm_calls":37,...}
{"event":"result","result":{ ...full ReviewResult... }}
```

`--output json` discards progress and prints exactly one JSON document: the
`ReviewResult`.

## Capabilities

Grounding is probed and logged at startup, so a run states what it stood on:

- **Text** — always available: diff, file reads, symbol grep.
- **Structure** — project files found in the tree.
- **Symbols / Diagnostics / HotPath / History** — reported unavailable until
  their spikes land; a finding class that depends on a missing capability is
  phrased conditionally rather than asserted on a guess.

## GitHub Actions

See [`.github/workflows/assay.yml`](.github/workflows/assay.yml). The workflow
builds the binary and runs `assay --pr "$PR_URL"` with the job's own
`GITHUB_TOKEN` (a `ghs_` installation token, which the clone URL handles with
`x-access-token`) and an `OPENROUTER_API_KEY` secret. The clone is
LFS-skipping by default and bounded by `ASSAY_GIT_TIMEOUT_SECONDS`.

## Known limitations

- **A repo under review is untrusted input.** assay passes repository guidance
  to the model through a delimited, non-authoritative prompt section, but
  opencode also runs inside the checkout and may load that repository's own
  `AGENTS.md`, config and plugins. Run the job with least privilege and treat
  anything the repository can influence as untrusted.
- **Spilled context files use fixed names** under `<repo>/.pr-af-context/`
  (kept for prompt byte-parity). Concurrent reviewers that spill very large
  context can overwrite each other's file; the prompt corpus pins the names.
- **Windows:** opencode installed as an npm `.cmd` shim may need the native
  executable path in `ASSAY_OPENCODE_BIN`.

## Development

```bash
make check     # build + vet + tests
make e2e       # offline end-to-end: real CLI, mock opencode, zero network
```

The offline e2e (`test/e2e/run.sh`) builds `test/mockcli` as a fake `opencode`,
creates a two-commit fixture repo, and runs the full pipeline through the CLI
with no control plane, no API key and no network.

Benchmark parity is measured against the recorded PR-AF baseline in
`benchmark/martian-code-review-bench/`. See that directory's README; do not
proceed past the parity milestone on a regression.

## Status

The pipeline is inherited from PR-AF at `76ad7f3`; the platform around it is
gone. Recall has **not yet been independently reproduced on the Martian
benchmark** — that is the gate the project has not cleared. Until it is,
treat the 0.706 figure as the baseline to match, not assay's result.
