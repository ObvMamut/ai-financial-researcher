# Orchestration

How the Go app drives the `claude` and `gemini` CLIs. This is the contract between
`internal/orchestrator` (runner + pool) and the agent personas.

## Invocation

Agents are CLI subprocesses run in headless/print mode:

- Gemini: `gemini -p "<assembled prompt>"`
- Claude: `claude -p "<assembled prompt>"`

The runner:
1. Assembles the prompt (see "Prompt assembly").
2. Spawns the subprocess with a context-based **timeout** (default a few minutes,
   configurable).
3. Captures **stdout** (the report) and **stderr** (diagnostics).
4. Writes the report to `runs/<timestamp>/<agent>.md` (see `store`).
5. Returns a `model.Report` with status `done` or `failed`.

> Pass the prompt via stdin or an argv-safe mechanism rather than naive shell
> interpolation — prompts are multi-line and contain quotes. Never build the command with
> string concatenation into a shell.

## Prompt assembly

```
<persona from agents/<role>.md>

## Task context
- Mode: independent-research | single-stock
- Run timestamp: <ts>
- Shortlist / ticker: <names>
- Prior reports (for Chief Analyst): <inlined report markdown>

## Output format
<the exact format block from the persona / output-schema.md>
```

The agent registry (`internal/agents`) loads each persona `.md` once and composes the
context + output sections per call.

## Concurrency

- A **bounded worker pool** runs subprocesses in parallel, default cap **4** (configurable).
- Stage ordering is enforced by the orchestrator: Stage 0.5 ranks the whole selected
  universe in-process (no model call) → scouts screen that ranking and complete →
  Stage 1.5 fetches price data and computes quant metrics for the shortlist (mostly cache
  hits from Stage 0.5, and computing each index benchmark's own metrics as the market
  regime) → specialists run → the weighted **base scores** are computed
  in-process from the specialist tails → Chief Analyst runs last (it depends on all
  specialist reports).
- Within a stage, agents run concurrently up to the cap.

## Timeouts, errors, retries

- Each agent has a per-stage timeout: Screening 5m, Analysis 5m, Synthesis 15m by default
  (`timeouts.screening`/`.analysis`/`.synthesis`, or `CFR_SYNTHESIS_TIMEOUT` etc.). Synthesis
  was raised from 5m after every real run to date SIGKILLed the Chief Analyst mid-attempt
  (601s ≈ 2 attempts × the old 300s budget) with no data yet on how long an uninterrupted call
  actually takes.
- **Each timeout is a per-attempt budget, never divided across retries.** A 2-attempt policy on
  a 5-minute timeout can take up to 10 minutes wall clock, not 5. On timeout/non-zero exit, the
  report is marked `failed` and the run **continues** — a missing domain degrades quality but
  must not crash the run.
- Retries use exponential backoff (`retry.base_delay`, doubling per attempt, capped at
  `retry.max_delay`) with optional jitter (`retry.jitter`) to avoid synchronized retry storms
  against a rate-limited endpoint. `retry.max_attempts` (default 2, i.e. one retry) governs
  screening/analysis and the DeepSeek fallback's own transient-error retries (429/5xx).
- The primary Chief Analyst call uses its own attempt budget, `synthesis_max_attempts`
  (default **1**, no retry) instead of `retry.max_attempts`: a synthesis timeout means "too
  slow," not "flaky," so retrying identically just delays reaching the fallback below.
- The Chief Analyst is told which specialist reports are missing. It does **not** discount
  for them by hand: weighted coverage is already priced into the computed base score and
  its cap (`docs/workflow/scoring.md`).

### Where the DeepSeek fallback sits

When the primary Chief Analyst call fails (timeout, non-zero exit, or empty output) or
succeeds but returns unparseable JSON, and `chief_fallback` is configured (its own dedicated
`api_key`, off by default), the orchestrator attempts one DeepSeek call
(`attemptChiefFallback`, `internal/orchestrator/fallback.go`) before falling through to the
mechanical `buildDegradedIdeas` path:

```
claude CLI (synthesis_max_attempts, default 1)
  ├─ success, parses          → validate → risk gate → done
  └─ fails / unparseable JSON → chief_fallback configured?
                                   ├─ yes → DeepSeek call (retry.max_attempts)
                                   │          ├─ success, parses → validate → risk gate → done
                                   │          └─ fails / unparseable JSON → buildDegradedIdeas
                                   └─ no  → buildDegradedIdeas
```

The fallback gets one attempt through the pipeline — no corrective re-prompt of its own — since
it is already a resilience measure for the single most expensive step in the run. The
corrective re-prompt path (risk-gate/validation findings on an otherwise-successful primary
call) is unaffected and does not invoke the fallback; a failed re-prompt keeps the
pre-correction ideas, as before.

## Enforcing coverage on specialist output

Non-empty stdout is not success. Between a specialist returning and its report being
written, the orchestrator rewrites the report's structured tail to match the data the
run actually assembled (`internal/orchestrator/enforce.go`), following the no-data
convention in `output-schema.md`:

1. Scores for shortlisted tickers the domain had no verified data for are deleted and
   the tickers unioned into `missing` → `domains[].corrected_scores`.
2. Scores for tickers that were never on the shortlist are deleted →
   `domains[].off_shortlist_scores`.
3. A report with no parseable JSON tail fails the domain (`status: failed`,
   `err: "no structured JSON tail…"`). A refusal, a truncated response, or free prose
   is not a domain report. Before that, the same prompt is sent **once more**: on
   2026-10-07 the news report stopped mid-sentence with `finish_reason: "stop"`
   (a `"length"` cut is refused by the API engine itself and never retried), and
   losing the domain cost four names their only non-price evidence. `attempts`,
   `tokens` and `usage` cover both calls; only a second untailed answer fails it.

The rewrite is in place — same prose, same fences, only the tail's `scores` and
`missing` arrays change — so the artifact on disk is exactly what the Chief Analyst
read. An already-honest report is passed through byte-identical.

This runs after citation scrubbing and before `WriteReport`. It replaced an advisory
warning (`overclaimedCoverage`) that logged the discrepancy and shipped the invented
scores anyway.

Once a tail is enforced, `parseSpecialistLabels` (`labels.go`) reads its `labels` array
(news, fundamentals, quant, sentiment). Keys the enforcement does not model survive the
rewrite untouched, so the labels on disk are the ones the agent wrote; the parse is what
narrows them — unknown for anything malformed, no veto without a closed-enum reason, and
nothing for a name the domain had no verified data for.

## Selection (`selection`)

Legacy independent runs choose their book one of two ways (`independent-research.md`,
Stage 3):

```
selection = "merit_veto" (default)                selection = "chief"
  macro call skipped; regime computed in Go         macro runs as before
  merit order − vetoes − per-idea gate refusals     Chief Analyst ranks and prices
  → top 5 (sector cap), scout direction,            → validate → risk gate
    market_on_open, catastrophe stop, time exit       → corrective re-prompt → drop
  → Chief Writer (one call, chiefTarget)            → DeepSeek fallback / degraded
      ├─ parses → prose, closed-enum vetoes
      │           (refill from reserves),
      │           shadow_rank recorded
      └─ fails / unparseable → ship the
                  selection without prose, degraded
  → final risk gate (sizing, book findings)
```

The Chief Writer runs on the selected Chief engine through the same `chiefTarget` as the
Chief Analyst, and its row in `metadata.json`'s `domains` is still `chief-analyst`. No
corrective re-prompt and no DeepSeek fallback run under `merit_veto`: the Chief's answer
no longer decides what ships, so a failure costs prose, not ideas. Both policies write
`data/selection.json`.

## The risk gate

After validation, `applyRiskGate` (`internal/orchestrator/riskgate.go`) sizes every idea
and checks its geometry, liquidity and simulated expectancy against the `[risk]` policy,
plus book-level correlation, sector concentration and beta. Violations join the corrective
re-prompt; per-idea violations that survive it cost the idea its place, with the reason in
both the run warnings and `ideas.json`'s `notes`. Limits and rationale:
`docs/workflow/scoring.md`.

## Anchoring confidence

Between the specialists and the Chief, `computeBaseScores`
(`internal/orchestrator/basescore.go`) does the weighting the Chief used to be asked to do
in prose. The result is injected into the chief prompt as a **"Computed base scores
(authoritative)"** table — above the specialist reports, so the arithmetic anchors the read
rather than the other way round — and enforced afterwards by `anchorConfidence`: confidence
outside `base ± chief_adjust_band` is clamped and warned about, and more than twice the
band out costs one corrective re-prompt. The same function feeds the degraded fallback, so
the fallback ranking and the Chief's own table can never disagree. Formula, caps and band:
`docs/workflow/scoring.md`.

## Artifacts

Everything for a run is written under `runs/<timestamp>/`:

```
runs/2026-06-01T14-30-05/
  prescreen.json          # universe-wide quant ranking (Stage 0.5), one row per
                          # constituent incl. excluded ones + the params that ranked
                          # them. The few hundred price series behind it stay in the
                          # shared data cache rather than in the run directory.
  scout-sp500.md  scout-nq100.md  scout-eu50.md  scout-asia100.md
  shortlist.json          # merged, validated, merit-trimmed; carries sector + the
                          # scout's bias and reason
  prices/<ticker>.json    # raw daily OHLCV per shortlisted ticker (Alpaca or Yahoo, '^' → '_')
  quant.json              # computed quant metrics pack (Stage 1.5)
  data/<domain>.json      # provider data packs (EDGAR/FRED/AV, when keys are set).
                          # No data/quant.json: no provider serves that domain, so
                          # the file was a relabelled copy of macro.json with an
                          # empty ByTicker — it misrepresented what quant saw.
  data/selection.json     # legacy independent runs: every shortlisted name in merit
                          # order with its labels, vetoes, exclusion reason, domain
                          # scores, shipped rank and the Chief's shadow rank
  news.md  fundamentals.md  quant.md  sentiment.md  macro.md   # macro.md: chief policy only
  chief-analyst.md        # full synthesis, or the merit_veto write-up (human-readable)
  ideas.json              # parsed []TradeIdea incl. entry/stop/target (machine-readable)
  metadata.json           # run outcome, domain statuses, warnings, weights, provenance
```

### metadata.json

Beyond the outcome and per-domain statuses:

| Field | What it records |
|---|---|
| `engine`, `engine_model` | which cheap-research engine and model ran the scouts and specialists |
| `synthesis_model` | the Claude model the Chief Analyst used |
| `stages` | wall-clock ms per stage: `prescreen`, `screening`, `quant`, `analysis`, `synthesis`. Only per-agent durations were kept before, leaving the in-process stages — most of a run's wall time — unaccounted for |
| `data_errors` | every provider failure from every pack, prefixed by domain, **and every provider warning** — a figure or a whole leg a source withheld, and why. These previously lived only in `data/<domain>.json`, so a run that lost eight tickers to rate limiting read like one that lost none |
| `persona_sha` | short hash per persona file. Personas are runtime data, editable with no code change, so nothing else makes a run's outcome attributable to the prompts that produced it |
| `build_revision`, `build_dirty`, `build_time` | the *cfr* binary's own git commit, whether its working tree was clean, and when it was built (`model.CurrentBuildInfo`, `runtime/debug.ReadBuildInfo`) — which code produced this artifact, not which config. `go build` embeds these from a git checkout; `go run`/`go test` binaries generally carry none of them, and that is recorded honestly (empty/absent), never guessed |

#### Shape-change warnings in `data_errors`

Three sources can fail into output that is byte-identical to a quiet market, so each one
says so explicitly through `TickerData.Warnings`, which `pack.go` folds into `data_errors`:

- **Yahoo option chain** — open interest and traded volume arrive in the same object. A
  chain that parsed strikes and open interest but reported a positive `volume` on none of
  them is the volume field being renamed or dropped, not a quiet name. Without the warning
  the whole flow leg (`UnusualOptionsLabel` + `OptionsFlowSignalLabel`) vanishes from every
  ticker at once with no error. The open-interest positioning leg is unaffected and still
  ships; a chain with *some* volume stays a plain `classifyUnusualOptions` abstention and
  warns about nothing.
  The mirror case (volume but no open interest) and an implausible ATM IV warn too, except
  when Yahoo's `marketState` is `PRE`/`PREPRE`: a pre-open chain has not been republished
  for the day, so it becomes one `expected` `off_session` source diagnostic and is not
  cached.
- **Yahoo and Alpaca news** — a feed that returned items and kept none of them reports the drop
  breakdown by reason (missing `providerPublishTime`, past the 21-day cutoff, empty title,
  duplicate). "20 items, 20 with no usable timestamp" is a schema change; "20 items, 20
  older than the cutoff" is a genuinely stale name. An empty `news` array warns about
  nothing — that one really is a quiet name — except for a foreign listing mapped in
  `adr_map.csv`, where Yahoo's empty local feed sends the one ADR query (HDFCBANK.NS,
  PHIA.AS and BBVA.MC all had empty local feeds and issuer news under the ADR on
  2026-10-07). A foreign listing with no ADR mapping and an empty local feed gets one
  company-name search instead (the universe name, normalised; 0 of 89 such names had
  any headline under the local symbol on 2026-10-07, 40 are covered by name). On that
  path a headline counts as about the company only when it is tagged with the listing
  *and* names the company, because Yahoo's tags on a name search are loose and a root can
  collide with a US ticker (AIR.PA vs AAR). A company whose name is its root (CSL Ltd.)
  cannot be separated this way. Alpaca keeps headline-only items for an ADR line (`exclude_contentless`
  stays on for US listings), because those issuers' stories arrive headline-only. Coverage
  stays false either way. Both feeds
  share the accounting (`internal/marketdata/newsfilter.go`) and each names its own
  timestamp field — `providerPublishTime` for Yahoo, `created_at` for Alpaca — so the
  message points at the field that actually changed.
- **AlphaVantage** — the remaining daily budget is logged at run start, and a spent key
  becomes **one** run-level entry in `warnings` rather than being inferred from N identical
  per-ticker `data_errors` (which are still kept). A run with no key configured says
  nothing: an unconfigured optional source is not a failure.

Beyond that, an unsatisfiable risk policy — `stop_sigma_min` above `stop_sigma_max`, or
`target_sigma_max` below `stop_sigma_min`, whether written explicitly or crossing a
`riskDefaults` default — **fails the run before Stage 0.5** rather than dropping every idea
at the gate and shipping an empty book that looks like the Chief wrote nothing sound.

Each `domains[]` row carries `tokens` — the completion-token count the engine
reported, when it reports one (the CLI engines do not), so a run's cost is
visible in its own artifacts. Rows also carry the enforcement lists and the re-prompt
outcome — `corrected_scores`, `off_shortlist_scores`, `self_contradicted_scores`,
`neutral_scores`, `abstained`, `scored_names`, `corrective` — which `output-schema.md`
defines field by field, and every model call gets a row: the four scouts and the Chief
Analyst included, not just the five specialists. Run-level, `thinly_covered` names the
shortlisted tickers the run could ground less than 0.6 of the domain weight for.

## Progress reporting to the TUI

The orchestrator emits progress events (per-agent status transitions + log lines) over a
channel. The TUI converts them to Bubble Tea messages and renders the status panel. The
orchestrator never imports TUI types.
