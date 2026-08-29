# Claude Financial Researcher

A terminal dashboard that orchestrates AI agents to propose **swing trades** across curated
large-cap samples of the **S&P 500, Nasdaq 100, EuroStoxx 50, and Asia** (98 / 59 / 50 / 67
names respectively — representative samples, not full index memberships; add rows to
`internal/universe/data/*.csv` to widen them).

It runs the `claude` and `agy` (Antigravity/Gemini) CLIs as subprocesses — **no model API
keys required**. Gemini does the cheap, parallel research; Claude does the heavy synthesis
and scoring. Price history comes from the keyless Yahoo Finance chart API, and every trade
level is checked against **verified, locally computed statistics** (momentum, Yang-Zhang
volatility, variance ratios — no chart TA).

## What it does

- **Independent research** — pick which index samples to screen, agents shortlist the most
  promising setups (capped at 12, cross-listings deduped), a quant stage computes verified
  statistics from real price history, specialists analyze news / fundamentals / quant /
  sentiment / macro, and the Chief Analyst returns **5 ranked trade ideas** — each with a
  **direction**, **entry / stop / target / risk-reward / timeframe**, a **confidence score
  (0–100)**, and the reasoning.
- **Input a stock** — run the same analysis on a single ticker for one focused verdict.
- **Run history** — browse past runs, their ideas, and every agent's full report; the
  detail view draws a braille price chart with the trade levels overlaid.
- **Scoreboard** — how past ideas have performed against today's prices (win rate,
  direction-aware P&L).

## Requirements

- [Go](https://go.dev/) 1.24+
- [`claude`](https://claude.com/claude-code) CLI, logged in
- [`agy`](https://antigravity.google/) CLI, logged in (override with `CFR_GEMINI_BIN`)

Verify they are on your `PATH`:

```bash
go version && claude --version && agy --version
```

## Usage

```bash
go run ./cmd/cfr                                    # TUI
go run ./cmd/cfr run --indices sp500,eu50 --json    # headless (JSON on stdout, logs on stderr)
go run ./cmd/cfr run --ticker ASML.AS               # headless single-stock verdict
go run ./cmd/cfr scoreboard                         # past-idea performance
```

Headless exit codes: `0` complete, `3` degraded (partial results), `1` error, `2` usage.

While agents run, a live status panel shows each agent's progress; results appear when the
Chief Analyst finishes. Full per-agent reports, price history, and computed quant metrics
are saved under `runs/<timestamp>/`.

Configuration is optional: copy `cfr.toml.example` to `./cfr.toml` or
`~/.config/cfr/config.toml` (env vars `CFR_*` override files; flags override everything).

## How it works

```
TUI / cfr run ──▶ Orchestrator ──▶ scouts (agy) ──▶ quant stage (in-process, Yahoo OHLCV)
                                 ──▶ specialists (agy, parallel) ──▶ chief analyst (claude)
```

See `docs/workflow/` for the full pipeline, scoring rubric, and output schema, and
`agents/*.md` for the agent personas (runtime data — edit without recompiling).

## Disclaimer

This is a research tool, not financial advice. Trade ideas are AI-generated and may be
wrong. Do your own due diligence.
