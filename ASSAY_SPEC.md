# Assay

**A single-binary code reviewer for GitHub Actions.** It builds a review plan specific to
each pull request, runs focused reviewers against that plan, and reports only the findings
that survive an adversarial challenge — which is what the name is for: an assay establishes
what a sample actually contains, by test rather than by opinion.

This is its spec. The pipeline is inherited because it is the asset; the platform is
discarded because it is the liability.

| | |
|---|---|
| Spec | v1 |
| Owner | Brightrock Games |
| First target | `BrightrockGames/Kaiju` |
| Basis | `pr-af` @ `76ad7f3` |

---

## 1. Job — what we are actually trying to do

Review every pull request on a large Unity monorepo, automatically, from GitHub Actions —
and post findings a game engineer would act on, anchored to real files and lines, inside a
budget we set.

These are not preferences. Each was measured during a failing run, and each broke something
in the stock configuration.

| Constraint | Detail |
|---|---|
| **~4.5 min** | Wall-clock to clone Kaiju. The stock `git` subprocess timeout was hardcoded at 30 s, so the run could never get past checkout. |
| **Git-LFS** | Repo is LFS-tracked. Smudging pulls binary content nobody reviews; `GIT_LFS_SKIP_SMUDGE=1` must be the default, and switchable. |
| **`ghs_` token** | CI authenticates with a GitHub Actions/App installation token, valid only with `x-access-token` as the basic-auth username. A PAT-shaped clone URL fails. |
| **Budget: config** | A per-review ceiling on spend and duration set *per run*, not fixed in the design. What must be fixed is that the ceiling is enforced and the actuals reported. |
| **csproj committed** | Kaiju commits its project files, so a C# language server can load the solution in CI. Unity is not installed, so `UnityEngine` references do not resolve — which bounds what that buys. See §6. |
| **Monorepo scale** | Needs path ignores, bounded reviewer concurrency, and bounded review depth, or one PR fans out without limit. |
| **Live progress** | A 30–60 minute job that prints nothing is indistinguishable from a hang. |
| **House rules** | Repo-specific guidance (`AGENTS.md`) must reach reviewers as data — able to add context, never to lower the bar. |

---

## 2. Thesis — what PR-AF believes, and why we keep it

The claim is narrow and testable: **don't hand one model a diff and ask for a review.**
Instead, design a review plan specific to this PR, run focused reviewers against that plan,
ground every finding in code actually read, then attack the findings before reporting them.

The payoff is that quality comes from *more inference passes* rather than a more expensive
model — which is what makes a cheap model competitive. PR-AF reports 0.706 golden recall on
the Martian Code-Review-Bench subset across 42 compared tools, on a single open model. That
number is the reason to inherit the design rather than start from a blank page.

### The seven stages

| # | Stage | Kind | What it does |
|---|---|---|---|
| 1 | Intake | LLM + gate | Classify PR type and complexity; derive review depth. Cheap, and it sets the budget for everything downstream. |
| 2 | Anatomy | code + LLM | Cluster changed files into coherent units of change, so reviewers see a feature rather than a scatter of hunks. |
| 3 | Meta-selectors | 3 parallel LLM | Three lenses — semantic, mechanical, systemic — each propose review dimensions. The load-bearing idea: the model designs the review instead of executing a fixed checklist. |
| 4 | Review | N parallel LLM | One focused reviewer per dimension, each with a narrow brief and the code it needs. Findings stream as they land. |
| 5 | Layer | LLM, adversarial | Verify findings against real source, challenge adversarially, cross-reference interactions, loop on uncovered clusters. Where false positives die. |
| 6 | Synthesis | deterministic | Score, deduplicate, normalise severity to `critical \| important \| suggestion \| nitpick`, decide what blocks. No model involved — correctly. |
| 7 | Output | deterministic | Map findings to diff lines and post. Anchoring to `diff_line`/`diff_side` is what makes a comment land in the right place. |

Two instincts here are right and assay should keep both: **the adversarial layer**, and
**keeping scoring, dedup and posting entirely deterministic.**

---

## 3. Diagnosis — where fourteen commits went

Not one touched the review pipeline. Every fix was in the scaffolding between GitHub Actions
and the first LLM call — which means the thesis above is still *untested* in this
environment, and the thing that failed is not the thing that matters.

| Commit | What broke | Rule it implies |
|---|---|---|
| `fc4191f` | 30 s hardcoded `git` timeout killed a 4.5-minute clone | Every subprocess timeout is configurable, defaulting generous |
| `5d286b8` | Clone failed with Actions/App tokens | Encode the auth scheme the token type requires, once |
| `7787d0e` | CI printed `Error details: None` | A failure path that loses the reason is a bug, not a log level |
| `f547f54` | Blocking git on the event loop starved heartbeats; node marked inactive | Never block the loop that proves you are alive |
| `b102e90` | Progress stream died during a 4.4-minute quiet stretch | Silence is not failure; reconnect and keep tailing |
| `db77ef0` | Intake returned `{}` on provider failure, recorded as success | Fail with the provider's own error; never launder it downstream |
| `9841ea6` | OpenRouter key posted to `api.deepseek.com` by LiteLLM prefix inference | Credentials and endpoint are chosen together or not at all |
| `0671c74` | `opencode run -m` named an undeclared provider; exit 1 in 2 s, no output | One owner for the model string; normalise at the edge |
| `8c27774`, `c1a9711` | Documented env vars never forwarded through Compose | A knob documented but not wired is worse than absent |
| `623cb8d`, `76ad7f3` | Blank-vs-unset env vars silently skipped logic | Blank means unset, everywhere, at read time |

### The decisive evidence

Upstream commit `48ae7ee` ("drop invalid `openrouter/` prefix") removed that prefix from the
default model across **eight files**. Its message is explicit: the aforge harness passes the
model to OpenRouter verbatim, so the prefixed form returned 400 and every reasoner failed.

That fix is also what broke opencode, which *requires* the prefix to resolve a provider.
Four consumers of one string — LiteLLM consumes the prefix, the Go AI client needs it
stripped, opencode needs it present, aforge strips it itself — and no single place owns the
contract. So a correct fix for one consumer is a regression in another, and neither side is
wrong.

This is not a bug that got missed. It is a design with no owner for its most load-bearing
value, and it will keep producing this class of failure.

### The other structural causes

- **Two implementations, synced by hand.** 8,025 lines of Python and 15,129 of Go
  implementing the same node. Every fix had to be made twice, and the two had already
  diverged.
- **Configuration derived twice.** A shell entrypoint computes the harness model config; the
  node computes it again independently. A test now asserts the two agree — a confession, not
  a safeguard.
- **Information destroyed at each layer.** opencode printed 218 bytes explaining itself; the
  SDK logged the byte count and discarded the content. That was the answer, unreachable for
  several rounds of debugging.
- **A control plane for a batch job.** DID/VC issuance, agent registration, SSE buses,
  heartbeats, inactivity marking — all debugged, none needed to review a pull request.
- **A closed binary in the critical path.** aforge is fetched from a vendor URL at image
  build and cannot be inspected when it misbehaves.

---

## 4. Scope — keep the pipeline, drop the platform

Assay is best read as a diff against what exists today.

```diff
  pr-af → assay

+ 7-stage pipeline: intake → anatomy → meta-selectors → review → layer → synthesis → output
+ Adversarial verification of every finding          // where precision comes from
+ Deterministic scoring, dedup, severity, diff-line anchoring
+ The prompt corpus                                  // benchmark-validated, the real IP
+ Budget caps, path ignores, bounded concurrency and depth
+ AGENTS.md as delimited, non-authoritative guidance
+ opencode as the single LLM seam, invoked directly  // no SDK in between
- AgentField control plane                           // registration, DID/VC, SSE, heartbeats
- aforge                                             // closed binary, vendor download
- The second LLM seam                                // .ai() alongside .harness()
- Second implementation in another language
- Model strings with per-consumer meaning
- Config derived in both a shell entrypoint and the node
```

### Target shape

- **One process, one language, one command.** `assay --pr <url>`. It clones, reviews, posts,
  exits with a status. No daemon, no registry, no callback URL.
- **One LLM seam, not two.** Today there are two — `.ai()` and `.harness()` — each with its
  own model-string convention, and that duplication is what sent an OpenRouter key to
  `api.deepseek.com`. One seam removes the whole bug class, and is the single largest
  simplification available.
- **That seam is opencode, invoked directly.** Roughly a hundred lines calling
  `opencode run --format json -m <model>` and parsing the event stream, with no SDK in
  between. That buys agentic file navigation, per-role model selection via `-m`, and real
  cost and token figures from `step_finish` events — so the budget cap is enforced on
  measured spend rather than an estimate. Owning the invocation is what fixes the opacity:
  the child's stdout is yours to surface instead of being counted and discarded.
- **Delete aforge.** A closed binary fetched from a vendor URL at image build, with its own
  prefix-stripping nobody can inspect. Removes a Dockerfile stage, a provider branch, and
  half the model-string confusion.
- **Structured output stays schema-validated with a bounded retry** — opencode's contract is
  a written file rather than native tool-calling, which is weaker, so the raw output must be
  preserved verbatim on every failed attempt. That is the one place this design is worse than
  a direct HTTP client, and the mitigation is logging, not cleverness.
- **Progress is newline-delimited JSON on stdout.** CI tails the process. No event bus, no
  reconnect logic, no stream to lose.
- **Budget is an accountant, not a hope.** Cost and tokens come back from opencode per run;
  tally against a configurable ceiling and abort with partial results when reached. Report
  the actuals either way — a cap chosen without measured spend is a guess.
- **Stateless.** It is a batch job. Idempotency key is `(repo, head_sha)`; rerunning is free
  and safe.

---

## 5. Path — Go is right, and the port already exists

`go/` is not a stub. It is a complete second implementation of the whole pipeline: 1,923
lines of prompts and 6,083 lines of orchestration, phases, schemas, scoring and output
mapping. So assay is not "rewrite in Go and copy the good parts" — the translation is done.
The job is to cut the platform out from under it.

| | Files | What |
|---|---|---|
| **Survive as-is** | 64 | No SDK import at all. Includes the entire prompt corpus — pure string building. |
| **Call seam swapped** | 15 | 13 reasoners plus the orchestrator and harness runner. |
| **Deleted outright** | 8 | Node registration, callbacks, pause/HITL plumbing, platform fatal handling. |

23 of 87 non-test Go files touch the SDK, and most only at a single call site. The prompts —
the part carrying the recall number — import nothing but the standard library.

### Why Go suits this target

- **It deletes the layer that produced the bugs.** A static binary needs no Compose, no
  container networking, no entrypoint generating config, no env forwarding. Five of the
  fourteen fixes were in exactly that layer; they cease to exist rather than being fixed.
- **The fan-out fits the language.** N parallel reviewers with bounded concurrency and
  streaming results is `errgroup` plus channels. And the event-loop starvation bug is an
  asyncio failure mode Go does not have.
- **Actions runs it directly.** Prebuilt binary or a composite action; no image build on
  every run.
- **One honest cost:** token counting for budget caps is weaker in Go — the tokenizer ports
  lag. Count the *actual* usage opencode returns per run instead of estimating ahead.

---

## 6. Context — language support as capabilities, not as a language

The framework should be language-generic with C# understood deeply first. Those pull in
opposite directions only if language support is a branch in the code. Make it a **capability
the reviewer probes** and they stop fighting: every stage asks what grounding is available
and adapts, so a language with nothing but a file extension still gets a review, and C# gets
a better one.

| Capability | Needs | What it grounds |
|---|---|---|
| **Text** | Nothing — always present | Diff, file reads, symbol grep, repo conventions. The floor, and the only rung PR-AF currently stands on. |
| **Structure** | Project files in the repo | File-to-assembly map and module boundaries. A change crossing an `asmdef` boundary is itself an architectural signal. |
| **Symbols** | A language server that loads | Real find-references and go-to-definition, replacing grep guesses about who calls what. |
| **Diagnostics** | A project that compiles | Compiler and analyzer output as candidate findings for the model to triage rather than invent. |
| **HotPath** | A framework hint pack | Call-frequency context — the grounding performance findings need and cannot get from a diff. |
| **History** | Commit history, by API or clone depth | How this code got this way: prior intent, churn, and the repo's own fix idiom. See §9. |

### Where C# lands

Treat the following as the shape of a spike rather than settled fact, since the details are
specific to how Kaiju's project files were generated.

- **Structure: available now.** The committed `.csproj` files carry the compile item lists,
  so the file-to-assembly map needs no Unity and no compilation. Cheap and immediately
  useful.
- **Symbols: likely available, partially.** A Roslyn-based server can load the solution and
  resolve types *defined in your own code* even while `UnityEngine` references fail. Since
  "who calls this method" is usually answered within the repo, the high-value part probably
  works. Worth measuring: load the solution on a fresh clone and count what resolves.
- **Diagnostics: needs one more step.** Semantic analyzers on a project with thousands of
  unresolved-type errors produce noise, not findings. That needs the Unity assemblies — a
  GameCI image with the editor, or a reference-assembly package. Syntax-only analyzers
  (naming, style) work without it, and are the least interesting ones.
- **HotPath: author it.** Nothing external provides this; it is a hint pack you write, and it
  is the highest-value C#-specific work here.

Worth naming the real difficulty: **C# is not the hard language — Unity is the hard build
system.** Plain .NET resolves under `dotnet build` and reaches Diagnostics with no special
handling.

> **On opencode's LSP:** it exists, but it is a diagnostics feedback loop for a *coding*
> agent — it starts a server when a file is opened or modified and feeds diagnostics back —
> not a general semantic-context service for a reviewer asking "who calls this across the
> monorepo". It is also off by default. Useful, but not the same thing as the Symbols
> capability above.

### Framework knowledge as data

A hint pack is declarative, not code — so a second framework is an authoring job rather than
a refactor. For Unity the contents are concrete:

- Per-frame entry points: `Update`, `FixedUpdate`, `LateUpdate`, `OnGUI` — and that a method
  reached from one is on a hot path.
- Allocation smells in those paths: `GetComponent` in a loop, `Find` calls, LINQ in
  `Update`, boxing in `foreach`, string concatenation per frame.
- Editor-versus-runtime boundaries: `#if UNITY_EDITOR`, `Editor/` folders — a perf finding in
  editor-only code is noise.
- Serialisation semantics: `[SerializeField]`, and that renaming such a field silently breaks
  existing scene and prefab data. That class of bug is invisible to a general reviewer and
  expensive in practice.
- Coroutine and lifecycle ordering assumptions.

Two rules keep this honest:

1. Capabilities are **probed and logged at startup**, so a run states what grounding it had.
   Otherwise you cannot tell a thin review from a thorough one after the fact.
2. **A finding class may be gated on the capability that grounds it.** Without HotPath,
   performance claims are phrased conditionally or suppressed, rather than asserted on a
   guess. That is the falsifiability rule from the literature, applied structurally.

---

## 7. Roles — mixed models, by role rather than by phase

Cheap models for exploration and an expensive one for the decisions that matter is the right
shape, and PR-AF already has the taxonomy. `ModelConfig` assigns a tier to each of nine
roles, with a rationale worth keeping: *plan quality is review quality, so the planner gets
premium.*

| Role | Tier | Why |
|---|---|---|
| `intake_gate` | budget | Fast classification; a wrong call costs one re-run, not a bad review |
| `intake_fallback` | mid | Only reached when the cheap gate is not confident |
| `anatomy_semantic` | mid | Narrative understanding of what the change is |
| `planner` | premium | Designs the review. Everything downstream inherits its blind spots |
| `reviewer` | premium | Reads the code. Where most tokens and most cost live |
| `cross_ref` | premium | Interaction between findings needs the strongest reasoning |
| `adversary` | premium | Precision comes from here; a weak critic confirms everything |
| `coverage_gate` | budget | Completeness check, near-mechanical |
| `dedup_gate` | budget | Near-duplicate detection, near-mechanical |

> **Declared but not wired.** Nothing reads `models.<role>`, and no mapping turns
> `budget`/`mid`/`premium` into model IDs. All nine roles run on the single `PR_AF_MODEL`.
> The table documents an intent, not behaviour — the third instance of this pattern, after
> the unwired Compose env vars and the unwired budget caps.
>
> The good news is that the hard part — deciding which roles tolerate a cheap model — is
> already done and reasoned. Assay inherits the taxonomy and only has to implement
> resolution.

### What to implement

- **Three model IDs, not one.** `ASSAY_MODEL_BUDGET` / `_MID` / `_PREMIUM`, resolved to a
  concrete `(provider, model)` at the single call seam. One function, one place — the tier is
  the only indirection.
- **Premium means a different family, not just a bigger sibling.** For the critic roles
  (`adversary`, `cross_ref`) the point is to decorrelate training errors, so pick another
  vendor rather than a larger model from the same one.
- **The economics work because the critic reads findings, not code.** `adversary` and
  `cross_ref` see candidate findings plus targeted evidence — a small fraction of the tokens
  the reviewer consumes. Premium there is cheap. `reviewer` at premium is the expensive line
  item and the real cost knob; that is where depth profiles should bite.
- **Record the model per finding.** Which model produced and which confirmed each finding, in
  the output. Without it you cannot tell whether the cheap tier is costing recall, and the
  mix becomes unfalsifiable.

---

## 8. Prior art — what the literature adds

The design is broadly convergent with 2026 work on multi-agent review, which means there are
validated upgrades available. But the multi-agent precision literature is overwhelmingly
**security and defect discovery** — CVEs, OWASP, SAST triage. Assay's targets are wider:
architecture, performance, maintainability. So the mechanisms transfer and the *domain
assumptions do not*, and the difference is load-bearing.

### Mechanisms that transfer

These control LLM overconfidence rather than reason about vulnerabilities, so they are
domain-agnostic.

**Refute-or-Promote** — [arXiv:2604.19049](https://arxiv.org/abs/2604.19049), April 2026.
Adversarial stage-gated review: parallel creative and adversarial tracks at every stage,
judged jointly before a finding is promoted. Reports killing ~79% of 171 candidates before
disclosure over a 31-day campaign against compilers and security libraries, yielding 4 CVEs
and an accepted C++ defect report.

> This is PR-AF's adversary layer, further along. Three mechanisms are directly portable:
> **kill mandates** (the critic is told to kill, not to assess neutrally — asymmetric burden
> of proof); **context asymmetry** (give the critic different context than the generator so
> it is not anchored on the same evidence); and a **cross-model critic** (criticise with a
> different model family, targeting correlated training errors). PR-AF currently runs one
> model for everything and shows the adversary the same evidence — so all three are open
> upgrades. Caveat: the paper leans on a human orchestrator to rescue true positives wrongly
> killed, so a fully automated port must be tuned against over-killing.

**QASecClaw** — [arXiv:2605.01885](https://arxiv.org/html/2605.01885v1), May 2026.
Static analysis generates candidates; an LLM filter agent triages them. F1 0.784 → 0.909 with
88.6% fewer false positives on OWASP Benchmark v1.2.

> The transferable form is **LLM as refiner of analyzer output** — reported elsewhere to lift
> static-analysis precision from 0.10 to 0.72. That is the *Diagnostics* capability, and with
> project files committed it is one step away: it needs Unity assemblies resolving, not a
> rearchitecture. **Adopt it as a generator, not as the generator.** Analyzers find local
> correctness and security patterns and essentially nothing architectural, so they raise the
> floor on the classes they cover while the LLM dimensions remain the only route to the
> categories assay exists to catch.

**Agreement weighting** — consensus multi-agent systems. Run several specialised reviewers in
parallel and weight each finding by how many independently agree.

> Nearly free here: the review phase *already* runs N reviewers over distinct dimensions.
> Cross-reviewer agreement is signal synthesis currently discards. Add it as a scoring
> multiplier.

### Where the domain assumptions break

Architecture and performance are measurably harder for LLM review than security.

**SmellBench** — [arXiv:2605.07001](https://arxiv.org/abs/2605.07001). Expert validation
found **63.1% of detected architectural smells were false positives**, with the best agent
resolving 47.7%. [Related work](https://link.springer.com/chapter/10.1007/978-3-032-02138-0_6)
reports near-100% recall with widely varying precision, and prompt specificity lifting
lower-severity detection from 64% to 82%. The papers name the gap explicitly: local code
transformation is tractable; cross-module architectural understanding is not yet.

> Two consequences. **Prompt specificity is the lever** — which is exactly what per-PR review
> dimensions are for, so the meta-selector design is the right response. And **a uniform kill
> mandate would prune architecture hardest**: these findings are less falsifiable from a diff
> than "this dereferences null", so a critic optimised for precision kills the arguable ones
> preferentially. The burden of proof has to vary by finding class.

**Overcorrection and unfalsifiable claims** —
[arXiv:2603.00539](https://arxiv.org/html/2603.00539v1). LLM reviewers systematically
overcorrect on conformance judgement. Separately, roughly half of false negatives in review
rationales fall into a "logic error" class where the model asserts an algorithm is wrong
*without offering a falsifiable counterexample*.

> **Make the counterexample mandatory.** Any correctness or performance finding must state
> concrete inputs or state that produce the wrong behaviour, and a finding that cannot is
> dropped rather than downgraded. A schema requirement, enforced deterministically in
> synthesis — cheap, and it targets the dominant failure mode directly.

**Performance needs grounding, not inference** —
[arXiv:2606.31368](https://arxiv.org/pdf/2606.31368),
[2510.15494](https://arxiv.org/pdf/2510.15494). Memory-optimisation work at codebase scale is
*profiling-guided*; the empirical study of LLM-proposed performance improvements finds
proposals need measurement to separate real wins from plausible ones.

> **The Unity-specific requirement.** The performance classes that matter — per-frame
> allocation, `Update()` cost, GC pressure — depend on *call frequency*, and a diff cannot
> tell you a method runs sixty times a second. The reviewer needs hot-path context. Much of
> that is statically derivable, and supplying it is likely worth more than a stronger model.
> Ungrounded performance findings are the ones a game engineer will dismiss fastest.

### On the benchmark, and on the model

Martian Code-Review-Bench — the one PR-AF's 0.706 claim rests on — was published in February
2026 with an open dataset, judge prompts and pipeline. Two newer benchmarks exist
([arXiv:2603.23448](https://arxiv.org/html/2603.23448v2) Code Review Agent Benchmark,
[arXiv:2603.26130](https://arxiv.org/html/2603.26130v1) SWE-PRBench). Validate against more
than one: a reviewer scored on a single benchmark can regress in ways that benchmark cannot
see.

One finding bears directly on configuration. Recent comparisons put DeepSeek- and
Haiku-class models at the high-recall, high-hallucination end, with Sonnet- and GPT-4o-class
models lower on false-positive rate. `PR_AF_MODEL` is currently
`deepseek/deepseek-v4-flash-0731` — the high-recall, high-FP end. Defensible on cost, but it
makes the adversarial layer load-bearing rather than optional, and it is an argument for the
cross-model critic: generate cheap, criticise with something stronger.

---

## 9. Later — history as review context

A diff shows what changed. History shows what the code has already learned — and a reviewer
that cannot see it will keep proposing things the repo tried and rejected. Worth building as
the *History* capability, after the core lands.

### Mechanism 1 — intent recovery (highest value)

Blame the lines the PR modifies or deletes, then read the commit messages that introduced
them. When a change removes something a previous commit added on purpose, that is a finding
no diff-only reviewer can produce — and it is expensive, because it re-opens a closed bug.

> **There is a worked example in this very repository.** Commit `48ae7ee` dropped the
> `openrouter/` prefix and its message says exactly why: aforge passes the model through
> verbatim, so the prefixed form returned 400. Any later change re-adding that prefix naively
> re-breaks aforge — which is precisely the mistake that then took several rounds to find. A
> reviewer holding that commit message would have flagged it on sight.
>
> The same mechanism kills a large false-positive class in the other direction: blame on an
> odd-looking line often reveals a deliberate workaround, which is the answer to "this looks
> redundant, remove it".

### Mechanism 2 — churn as a severity signal (deterministic)

Hunks and files changed repeatedly — especially by commits whose messages say fix, hotfix or
revert — are empirically the defect-prone ones.

> This belongs in **synthesis as a score multiplier, not in a prompt** — alongside the
> existing `blast_radius_high` and `cross_ref_compound` entries. Deterministic, reproducible,
> costs no tokens. Cheapest item on this list and probably the first to build.

### Mechanism 3 — house idiom for fix suggestions

Prior commits touching the same area show how this codebase actually solves this class of
problem. Turns a generic suggestion into one that matches the repo's conventions, which is
most of the difference between a suggestion an engineer applies and one they close.

### Mechanism 4 — fuzzy commit-message search (build last)

Match the PR's title and description against historical commit messages to surface "we tried
this before". The weakest of the four: message similarity is a loose proxy for relevance and
will surface coincidence as often as precedent.

### Where it plugs in

- **Anatomy** — mark clusters that sit on historical hotspots.
- **Meta-selectors** — a repeatedly-regressed area is a reason to raise a dimension for it.
- **Review dimensions** — blame context alongside each hunk.
- **Adversary** — the strongest new use. History lets the critic *kill* a finding ("this is
  deliberate, see commit X") as readily as confirm one. It is also a natural source of the
  **context asymmetry** the literature recommends: give the critic history the generator
  never saw, and the two decorrelate for free rather than by construction.
- **Synthesis** — the churn multiplier, deterministically.

### The blocker, and the way around it

Both nodes clone `--depth 1 --no-tags`, with a comment stating that only enough history to
read files is needed. So there is no history today at all: `git log` returns one commit and
`git blame` attributes every line to it. This feature is not merely unbuilt, it is currently
impossible.

**Do not fix that by deepening the clone.** Checkout already costs about four and a half
minutes, and full history on a monorepo this size is far worse. Use the GitHub API instead:
GraphQL exposes a blame field and REST returns commits filtered by path, both already
authenticated by the token in the job. The clone strategy stays untouched.

Keep a local-git implementation behind the same capability for cases where history is present
anyway — local development, or a non-GitHub remote. That is the point of expressing it as a
capability rather than a feature.

### Cautions

- **Commit messages are untrusted input.** Anyone who can open a PR can write one. They must
  be delimited as data with the same treatment the PR description already gets, or history
  becomes a prompt-injection surface with a commit attached.
- **Honour `.git-blame-ignore-revs`.** Formatting sweeps, mass renames and licence-header
  commits otherwise dominate every blame result. Git supports this natively via
  `blame.ignoreRevsFile`.
- **History is evidence, not instruction.** Telling a model an area was recently fixed pushes
  it to over- or under-flag; the sycophancy finding applies directly. Prefer deterministic
  scoring wherever the signal can carry it.
- **Bound it hard.** Only the diff's own hunks, N commits per file, and a cap on total
  history tokens — otherwise a monorepo's past consumes the budget the review needed.

---

## 10. Invariants — rules the failures earned

Each generalises a real failure from §3. They cost nothing to hold from day one and are
expensive to retrofit.

- A blank environment variable means unset — resolved at read, in one helper.
- Credentials are trimmed where they are read, never at the point of use.
- Credentials are verified against the provider before any expensive work begins.
- Every subprocess has an explicit, configurable timeout.
- A child process's stdout and stderr are surfaced verbatim when it fails.
- No `catch`/`except` clause discards a provider error.
- An empty result is never a success; the phase fails with the reason.
- No configuration value is derived in two places.
- Blocking work never runs on the loop that reports liveness.
- Every documented knob has a test proving it is wired end to end.

---

## 11. Bounds — non-goals and acceptance

### Explicitly not doing

- A second implementation of the tool. One codebase, one language — this is about the
  reviewer's own source, not the languages it reviews.
- Language-specific branches through the pipeline. New language support is a capability probe
  plus an optional hint pack, or it is not supported.
- A general agent platform, capability registry, or cryptographic audit trail (unless
  compliance actually requires the latter).
- Supporting every harness CLI. One: opencode.
- Multi-tenant serving. One repo, one CI job.

### Done means

- A full review of Kaiju PR #378 completes and posts findings anchored to correct diff lines.
- The cost and duration ceilings are **configuration, not policy**. A routine PR on a budget
  tier and a release PR on frontier models at $20 are the same code path with different
  numbers. What must hold is that the ceiling is honoured, the run aborts with partial results
  when reached, and **actual** spend and duration are reported — so the ceiling can be chosen
  from evidence rather than guessed.
- A wrong or missing credential fails in under ten seconds with a message naming the cause —
  never after the clone.
- Any failure names the component and quotes the underlying error.
- Recall is measured on the same Martian bench subset before and after, so you learn whether
  assay kept the quality. **Without this, assay is a rewrite of unknown value.**

---

## 12. Verdict — fork the Go tree, delete the platform

**Go is the right target. Starting from zero is not.**

The instinct to move to a single Go binary is correct, and for a better reason than language
preference: it deletes the Compose, entrypoint and container layer where five of the fourteen
failures lived. Those bugs stop existing rather than getting fixed.

But `go/` already *is* that implementation, minus the platform. Prompts, phases, schemas,
scoring and diff-line mapping are all there and 64 of 87 files touch no SDK at all. Writing
from scratch and copying pieces back means re-deriving prompt quality with no benchmark to
tell you when you have lost ground — trading a plumbing problem you can see for a quality
problem you cannot.

**Sequence:** fork `go/` into its own repo; delete the eight platform files and aforge;
collapse the two LLM seams into one opencode runner you own outright, about a hundred lines,
so cost, tokens and the child's own output all become visible; wire the tier resolution the
role table already assumes; ship as a static binary invoked directly by Actions.

**Then, in order of value per unit of work:** the Unity hint pack, so performance and
serialisation findings are grounded rather than guessed; a cross-family critic on the two
premium critic roles; a mandatory falsifiable counterexample for correctness and performance
findings, enforced in synthesis; class-aware burden of proof so the adversary does not prune
architecture preferentially; agreement weighting across the reviewers you already run in
parallel. Then the two capability spikes — load the committed solution on a fresh clone and
count what resolves, and decide whether Unity assemblies in CI are worth the Diagnostics
rung.

Measure on more than one benchmark, and record the producing and confirming model on every
finding — otherwise the model mix cannot be evaluated and the tiers become a matter of taste.

One thing still worth five minutes first: `0671c74` fixed the `-m` provider bug and no run has
happened since. Run it once on the current key. If intake passes, you get the first real
evidence about the pipeline itself — the one thing this document cannot tell you, and the
thing assay is betting on.

---

## 13. Kickoff — work order for the first session

Everything above is design rationale. This section is the part an implementer acts on, so it
is deliberately concrete: names, files, contracts, order.

### Standing rule, before anything else

> **Do not improve the prompts.** Every string in `internal/prompts/` ports **byte for
> byte**. They are the only benchmark-validated component, they read as improvable, and they
> are not. Any prompt change is a separate commit, made after the port works, and measured on
> the bench — never bundled into the migration, where a recall regression would be
> indistinguishable from a porting bug.

### Identity

| | |
|---|---|
| Repo | `BrightrockGames/assay` |
| Module | `github.com/BrightrockGames/assay` — flatten the `go/` prefix away; the tree becomes the repo root |
| Binary | `assay`, from `cmd/assay/main.go` |
| Env prefix | `ASSAY_` throughout. No `PR_AF_` survives; a half-renamed config surface is how two of the fourteen bugs hid |

### Port plan, file by file

Counts from the tree at `76ad7f3`. Delete first — it shrinks the problem before the
interesting work starts.

**Delete — platform only, 1,386 lines**

| File | Lines | What |
|---|---|---|
| `internal/node/node.go` | 283 | Agent construction, callbacks |
| `internal/node/register.go` | 185 | Reasoner registration |
| `internal/hitl/review_gate.go` | 434 | Human-in-the-loop pause |
| `internal/hitl/pause.go` | 64 | |
| `internal/gates/merge_gate.go` | 224 | Control-plane merge gate |
| `internal/gates/polish.go` | 72 | |
| `internal/fatal/fatal.go` | 103 | SDK fatal handling |
| `doc.go` | 21 | Rewrite as package doc for assay |

**Change — the call seam, 15 files**

| File | Lines | What |
|---|---|---|
| `internal/harnessx/run.go` | 123 | Becomes the opencode runner you own |
| `internal/orch/orchestrator.go` | 752 | Drop SDK context, keep phase wiring |
| `internal/reasoners/{intake,anatomy,planning,meta,reviewdim}.go` | | Swap the call site |
| `internal/reasoners/{adversary,verify,deepen,compound,coverage}.go` | | Swap the call site |
| `internal/reasoners/{obligations,worthiness,reasoners}.go` | | Swap the call site |

Everything else — `internal/prompts/` (1,923 lines), `internal/orch/phases.go` (1,221),
`output.go` (747), `compound.go`, `helpers.go`, `evidence/`, `github/`, the schemas —
compiles unchanged once the seam is replaced.

### The seam, precisely

One function, replacing both `.harness()` and `.ai()`. Roughly a hundred lines:

1. Build `opencode run --format json --dir <repo> -m <qualified-model> <prompt>`, model
   resolved from the role's tier.
2. Parse the event stream: the result text, per-step cost, token counts.
3. Validate against the caller's schema; on failure retry up to N times *preserving the raw
   output of every attempt*.
4. Return result, parsed value, cost, tokens, and — on failure — the child's stdout and
   stderr verbatim. This is the whole fix for the opacity that cost a week.

### Milestones

1. **Walking skeleton** — *proves the seam.* Binary clones a PR, computes the diff, runs
   *one* review dimension through the new seam, prints findings as JSON to stdout. No
   posting, no caps, no parallelism. If this works the port is de-risked; everything after is
   reconnecting existing code.
2. **Full pipeline** — *all seven stages.* Every reasoner on the new seam, parallel fan-out
   with bounded concurrency, NDJSON progress on stdout. Output still local.
3. **Parity check** — *the gate that matters.* Run the Martian subset in
   `benchmark/martian-code-review-bench/` — `problems.json` and `scripts/` are already there
   — and compare against the recorded `scoreboard.jsonl`. **Do not proceed past this
   milestone on a regression.** This is the only point where a porting error is still cheap
   to find.
4. **Ship it** — *CI-facing.* GitHub posting, configurable cost and duration caps enforced on
   measured spend, tier resolution wired, capability probe logged at startup, Actions workflow
   and a released static binary.
5. **Upgrades** — *each measured separately.* Unity hint pack, cross-family critic, mandatory
   counterexample, class-aware burden of proof, agreement weighting, History capability. One
   at a time, each re-run against the bench, so a quality change is always attributable.

### Decisions left open

Deliberately unspecified, because they need a person or a measurement rather than a guess:
the CLI's exact flag names and exit-code taxonomy; whether findings post as one summary
comment or inline review comments (PR-AF supports both, and `suggestion_mode` already
exists); the tier-to-model assignments themselves; whether Unity assemblies in CI are worth
the Diagnostics rung; and whether the evidence layer keeps its deterministic grep path once
the agent can navigate for itself.

### Using this document

This is a *design* spec, not an API contract. The finding schema, the NDJSON event shape and
the flag surface all still come from reading the existing code — which is correct, since that
code is the source of truth and re-specifying it in prose would create a second place to
drift. Point a new session at both this document and the repo, not this document alone.

---

*Provenance: basis `pr-af` @ `76ad7f3`, BrightrockGames fork of Agent-Field/pr-af.
Constraints measured against `BrightrockGames/Kaiju` PR #378 on ubuntu-24.04 runners. File
and SDK-coupling counts measured across the Go tree at that commit. Recall figures as
reported by the project's own Martian Code-Review-Bench package, not independently
reproduced. Cited papers read from abstracts and summaries, not reproduced.*
