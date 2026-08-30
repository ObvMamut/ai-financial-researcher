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
  hits from Stage 0.5) → specialists run → the weighted **base scores** are computed
  in-process from the specialist tails → Chief Analyst runs last (it depends on all
  specialist reports).
- Within a stage, agents run concurrently up to the cap.

## Timeouts, errors, retries

- Each agent has a timeout. On timeout/non-zero exit, the report is marked `failed` and
  the run **continues** — a missing domain degrades quality but must not crash the run.
- One retry on transient failure (non-zero exit with empty stdout) is allowed; otherwise
  mark `failed`.
- The Chief Analyst is told which specialist reports are missing. It does **not** discount
  for them by hand: weighted coverage is already priced into the computed base score and
  its cap (`docs/workflow/scoring.md`).

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
   is not a domain report.

The rewrite is in place — same prose, same fences, only the tail's `scores` and
`missing` arrays change — so the artifact on disk is exactly what the Chief Analyst
read. An already-honest report is passed through byte-identical.

This runs after citation scrubbing and before `WriteReport`. It replaced an advisory
warning (`overclaimedCoverage`) that logged the discrepancy and shipped the invented
scores anyway.

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
  prices/<ticker>.json    # raw daily OHLCV per shortlisted ticker (Yahoo, '^' → '_')
  quant.json              # computed quant metrics pack (Stage 1.5)
  data/<domain>.json      # provider data packs (EDGAR/FRED/AV, when keys are set).
                          # No data/quant.json: no provider serves that domain, so
                          # the file was a relabelled copy of macro.json with an
                          # empty ByTicker — it misrepresented what quant saw.
  news.md  fundamentals.md  quant.md  sentiment.md  macro.md
  chief-analyst.md        # full synthesis (human-readable)
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
| `data_errors` | every provider failure from every pack, prefixed by domain. These previously lived only in `data/<domain>.json`, so a run that lost eight tickers to rate limiting read like one that lost none |
| `persona_sha` | short hash per persona file. Personas are runtime data, editable with no code change, so nothing else makes a run's outcome attributable to the prompts that produced it |

Each `domains[]` row carries `tokens` — the completion-token count the engine
reported, when it reports one (the CLI engines do not), so a run's cost is
visible in its own artifacts. Rows also carry `corrected_scores` and `off_shortlist_scores`
(see the enforcement section above), and every model call gets a row — the four
scouts and the Chief Analyst included, not just the five specialists.

## Progress reporting to the TUI

The orchestrator emits progress events (per-agent status transitions + log lines) over a
channel. The TUI converts them to Bubble Tea messages and renders the status panel. The
orchestrator never imports TUI types.
