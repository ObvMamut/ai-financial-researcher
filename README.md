# Claude Financial Researcher

A terminal dashboard that orchestrates AI agents to propose **swing trades** across curated
large-cap samples of the **S&P 500, Nasdaq 100, EuroStoxx 50, and Asia** (98 / 59 / 50 / 67
names respectively — representative samples, not full index memberships; add rows to
`internal/universe/data/*.csv` to widen them).

By default, it runs the `claude` and `agy` (Antigravity/Gemini) CLIs as subprocesses
with **no model API keys required**. Gemini does the cheap, parallel research; Claude does
the heavy synthesis and scoring. The Chief Analyst engine is selectable: `chief_engine=api`
routes synthesis through an OpenAI-compatible API (e.g., DeepSeek) with dedicated credentials
instead; omitting this setting preserves the default behaviour. Price history comes from the
keyless Yahoo Finance chart API, and every trade level is checked against **verified, locally
computed statistics** (momentum, Yang-Zhang volatility, variance ratios — no chart TA).

## What it does

An opt-in [thesis research mode](docs/workflow/thesis-research.md) adds per-company
source reading, research rounds, a separate challenge and Claude-led selection for
10–15 trading sessions. Run `go run ./cmd/cfr run --research-mode thesis --json`.
It includes reviewed conditional plans with explicit entry checks and monitoring.
It can return zero ideas and distinguishes research failures from completed
watchlist/rejected decisions, with visible research and review counts. Legacy remains
the default; better trading performance has not yet been established. Compare
future runs with `go run ./cmd/cfr scoreboard --research-compare`. Add `--offline`
to inspect saved artifacts without provider calls. Explicit pairing audits and
assumed execution-cost scenarios are described in the
[evaluation workflow](docs/workflow/thesis-research.md#explicit-pairing-and-evaluation-diagnostics).

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

- [Go](https://go.dev/) 1.27.1+ (see `go.mod`)
- **Chief Analyst:** `claude` CLI (omitting `chief_engine` or `chief_engine=claude`
  requires `claude` logged in; `chief_engine=api` does not)
- **Cheap research:** `agy` CLI, logged in, unless `cheap_engine=api` or `cheap_engine=local`
  (override `agy` binary with `CFR_GEMINI_BIN`)

For the default configuration (Claude CLI + agy CLI):

```bash
go version && claude --version && agy --version
```

For API-only (no Claude CLI needed):

```bash
go version
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
                                 ──▶ specialists (agy, parallel) ──▶ chief analyst (claude | api)
```

See `docs/workflow/` for the full pipeline, scoring rubric, and output schema, and
`agents/*.md` for the agent personas (runtime data — edit without recompiling).

## Developing with Codex or Claude Code

Open this repository in Codex and work from the repository root. Codex discovers
[`AGENTS.md`](AGENTS.md), which provides a code map, validation commands, and points
to [`CLAUDE.md`](CLAUDE.md) for the detailed shared development guidance. This uses
the standard [Codex project instructions](https://developers.openai.com/codex/guides/agents-md)
mechanism; no additional project configuration is required.

Claude Code can continue using `CLAUDE.md` and its existing `.claude/` settings.
The application's research engines and runtime personas in `agents/` retain their
existing roles. Codex support here is for developing the application.

Build and validate with `go build ./...`, `go test ./...`, and `go vet ./...`.
Tests use fake model CLIs and local HTTP fixtures without model credentials.

## Disclaimer

This is a research tool, not financial advice. Trade ideas are AI-generated and may be
wrong. Do your own due diligence.

For a prospective comparison, `cfr research-pair --out .data/pair-2026-09-09
--ticker AAPL --omit-alphavantage` collects a common evidence corpus and runs
legacy and thesis with isolated inputs. Later use `cfr research-pair --evaluate
.data/pair-2026-09-09 --refresh` to save outcome prices separately. See the
[paired-evaluation workflow](docs/workflow/thesis-research.md#collecting-a-prospective-frozen-pair)
for configuration, registered assumptions and 10/15-session maturity limits.

### Acceptance manifest

`go run ./cmd/cfr acceptance-manifest --indices sp500,eu50` writes resolved,
redacted configuration and source/persona/code hashes without starting models or
fetching market data. See the [continuation and regression record](docs/plans/2026-09-22-continuation.md)
for offline checks and the live acceptance work still pending.
