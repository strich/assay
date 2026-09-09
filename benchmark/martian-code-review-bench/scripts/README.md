# Reproduction scripts

These produce the data in this directory. Run them from the **assay repo root**.
They read `OPENROUTER_API_KEY` from the environment, or from the repo-root `.env`
when present. Raw per-run transcripts and caches are written to a gitignored
`_glm52_bench/` scratch dir at the repo root.

> **Two runners.** `run_node.sh` runs the **assay CLI** on one PR — this is the
> parity path. `campaign.py`, `ensemble.py`, `all_metrics.py` and
> `honest_compare.py` reproduce the **recorded PR-AF baseline** against the
> archived HTTP node and are kept as provenance for `scoreboard.jsonl`.

| script | what it does |
|---|---|
| `run_node.sh` | Builds the assay binary and runs `assay --pr <url> --depth deep --dry-run --output json` with every tier pinned to one model. This is how assay's parity numbers are produced. |
| `campaign.py` | Archived: runs a blind `depth=deep` review for each problem in `../problems.json` against the old HTTP node, LLM-judges findings against the goldens for **recall**, and writes `../scoreboard.{md,jsonl}` + `../results/<id>.json`. |
| `ensemble.py` | Archived: self-consistency escalation for the baseline campaign. |
| `all_metrics.py` | Golden-only precision/recall/F1 on the posted-comment basis, ranked against every leaderboard tool from the cloned Martian dataset. |
| `honest_compare.py` | Honest scoring (Framing C, see `../RESULTS.md`): credits real non-golden bugs, applied uniformly to PR-AF and the leaders (cubic-v2, cubic-dev). |

## Run

```bash
# parity: assay on one PR (repeat per problem in problems.json)
bash benchmark/martian-code-review-bench/scripts/run_node.sh https://github.com/owner/repo/pull/N

# baseline provenance (archived HTTP node)
bash benchmark/martian-code-review-bench/scripts/run_node_legacy.sh     # terminal 1
uv run python benchmark/martian-code-review-bench/scripts/campaign.py  # terminal 2
```

`all_metrics.py` and `honest_compare.py` additionally need Martian's cloned offline
dataset. Set `CRBENCH_RESULTS_DIR` to the offline `results/` directory, or set
`CRBENCH_JUDGE_FILE` directly for `honest_compare.py`.

## Knobs (env)

`CAMPAIGN_CONCURRENCY` (default 3) · `CAMPAIGN_DEPTH` (deep) · `CAMPAIGN_MAX_COST`
· `CAMPAIGN_MAX_DURATION` · `CAMPAIGN_LIMIT` (cap unsolved problems per invocation)
· `CAMPAIGN_FORCE` (comma-ids to re-run) · `ENSEMBLE_PASSES` (default 2).
