# CLAUDE.md — Claude Financial Researcher

Guidance for Claude Code when developing **this Go codebase**. (For your role as the
*Chief Analyst agent at runtime*, see `agents/chief-analyst.md` instead — that is a
separate concern from building the app.)

## What this project is

A Go TUI dashboard that orchestrates AI agents to propose **swing trades**. Two modes:

1. **Independent research** — agents screen the SP500, NQ100, EU50, and Asia100, then
   deep-analyze a shortlist and return **5 ranked trade ideas**.
2. **Input a stock** — same deep-analysis agents run on one user-supplied ticker
   (screening skipped), returning a single verdict.

## Hard constraints

- **No model API keys.** All model work happens by shelling out to model CLIs in
  headless/print mode (`-p`). Never add an SDK or HTTP call to a *model provider*.
  (HTTP to *market-data* sources is fine and expected — `internal/marketdata` talks to
  the keyless Yahoo Finance chart API, and optionally EDGAR/FRED/AlphaVantage when keys
  are configured.) The cheap-research role
  defaults to the `agy` (Antigravity) CLI — Google discontinued the free `gemini` CLI tier
  ("IneligibleTierError… migrate to Antigravity"); `agy` exposes the same `-p`/`--model`
  interface. Heavy synthesis still uses `claude`. Each binary is overridable without
  recompiling via `CFR_GEMINI_BIN` / `CFR_CLAUDE_BIN`. `agy` auth is throttled to 1
  concurrent process by default — concurrent `agy` launches contend on the OS keyring,
  time out silent auth, and escalate to an interactive browser login; raise the cap with
  `CFR_GEMINI_CONCURRENCY` if your keyring tolerates it.
- **Cost split:** **Gemini** does cheap, parallel research (screening + domain reports);
  **Claude** does the single heavy synthesis/scoring step (Chief Analyst).
- Agent personas live in `agents/*.md` and are loaded at runtime — they are *data*, not
  Go source. Editing a persona must not require recompiling.

## Architecture map

```
cmd/cfr/        entry point + subcommands: bare = TUI, `run` (headless), `scoreboard`
internal/
  tui/          Bubble Tea screens: app (router), home, run (status), results,
                history, reports, scoreboard
  orchestrator/ pipeline driver, subprocess runner, bounded worker pool
  agents/       persona registry: load agents/*.md, assemble prompts
  universe/     index constituents (data/*.csv), dedupe/cap, benchmark symbols
  quant/        pure-stdlib statistical metrics (momentum, YZ vol, VR, …) — no TA
  marketdata/   HTTP data providers: Yahoo chart API (keyless), EDGAR/FRED/AV (keyed)
  model/        shared types: Report, TradeIdea, RunState, AgentStatus
  store/        run artifacts under runs/<timestamp>/ (reports, prices/, quant.json)
  config/       settings: defaults → ~/.config/cfr/config.toml → ./cfr.toml → env
  scoreboard/   past-idea performance vs current prices (win rate, P&L)
agents/*.md     agent persona prompts (runtime data)
testdata/fakebin/ fake agy/claude CLIs for hermetic tests + cheap manual TUI runs
docs/workflow/  workflow + scoring + schema specs (source of truth for behavior)
```

The pipeline and scoring rules are specified in `docs/workflow/`. When behavior is
ambiguous, those docs are the source of truth — keep code and docs in sync.

## Pipeline (independent research)

1. **Scouts (Gemini):** one subprocess per index → shortlist of ~5–10 names each.
   Orchestrator merges/dedupes (incl. cross-listings) and caps at 12, balanced per index.
2. **Stage 1.5 (in-process, no model):** fetch 2y daily OHLCV per shortlisted name from
   Yahoo, compute `internal/quant` metrics, persist `prices/` + `quant.json`.
3. **Specialists (Gemini, parallel):** News, Fundamentals, Quant, Sentiment, Macro.
   Each writes **one** report covering the whole shortlist (5 calls total — not
   per-ticker). The quant specialist interprets the computed pack; no chart TA anywhere.
4. **Chief Analyst (Claude):** reads the 5 reports + compact verified quant lines, scores
   confluence, ranks, emits the final 5 ideas (with entry/stop/target derived from
   vol-scaled distances) as a fenced ```json block that Go parses into `[]model.TradeIdea`.

Single-stock mode: shortlist = `[ticker]`, skip step 1, `topN = 1`.

## Commands

```bash
go build ./...        # build
go test ./...         # unit tests
go run ./cmd/cfr      # launch the TUI
go run ./cmd/cfr run --indices sp500,eu50 --json   # headless run (exit 0 ok / 3 degraded)
go run ./cmd/cfr scoreboard                        # past-idea performance
```

Configuration: `cfr.toml.example` documents every key. Precedence: defaults →
`~/.config/cfr/config.toml` → `./cfr.toml` → `CFR_*` env vars → flags.

## Conventions

- Standard library + `charmbracelet/{bubbletea,lipgloss,bubbles}` for the TUI.
- Keep packages small and single-purpose; the runner must not know about TUI types and
  vice-versa — they communicate via `model` types and channels.
- Subprocess calls always have a timeout and capture stderr; a failed agent degrades the
  run gracefully (mark it `failed`, continue) rather than crashing the TUI.
- The Chief Analyst's JSON is untrusted: parse defensively and surface parse errors as a
  recoverable error state.
