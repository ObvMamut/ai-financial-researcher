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
- Stage ordering is enforced by the orchestrator: scouts complete → Stage 1.5 fetches
  price data and computes quant metrics in-process (no model call) → specialists run →
  Chief Analyst runs last (it depends on all specialist reports).
- Within a stage, agents run concurrently up to the cap.

## Timeouts, errors, retries

- Each agent has a timeout. On timeout/non-zero exit, the report is marked `failed` and
  the run **continues** — a missing domain degrades quality but must not crash the run.
- One retry on transient failure (non-zero exit with empty stdout) is allowed; otherwise
  mark `failed`.
- The Chief Analyst is told which specialist reports are missing so it can lower confidence
  accordingly.

## Artifacts

Everything for a run is written under `runs/<timestamp>/`:

```
runs/2026-06-01T14-30-05/
  scout-sp500.md  scout-nq100.md  scout-eu50.md  scout-asia100.md
  shortlist.json
  prices/<ticker>.json    # raw daily OHLCV per shortlisted ticker (Yahoo, '^' → '_')
  quant.json              # computed quant metrics pack (Stage 1.5)
  data/<domain>.json      # provider data packs (EDGAR/FRED/AV, when keys are set)
  news.md  fundamentals.md  quant.md  sentiment.md  macro.md
  chief-analyst.md        # full synthesis (human-readable)
  ideas.json              # parsed []TradeIdea incl. entry/stop/target (machine-readable)
  metadata.json           # run outcome, domain statuses, warnings, weights
```

## Progress reporting to the TUI

The orchestrator emits progress events (per-agent status transitions + log lines) over a
channel. The TUI converts them to Bubble Tea messages and renders the status panel. The
orchestrator never imports TUI types.
