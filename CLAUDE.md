# CLAUDE.md — Claude Financial Researcher

Guidance for Claude Code when developing **this Go codebase**. (For your role as the
*Chief Analyst agent at runtime*, see `agents/chief-analyst.md` instead — that is a
separate concern from building the app.)

## What this project is

A Go TUI dashboard that orchestrates AI agents to propose **swing trades**. Two modes:

1. **Independent research** — agents screen four curated index samples, then deep-analyze
   a shortlist and return **5 ranked trade ideas**. The universe files are *representative
   samples*, not full index memberships: `sp500.csv` holds 98 names, `nq100.csv` 59,
   `asia100.csv` 67, `eu50.csv` 50. Scouts see exactly those names and the run log says
   how many ("screening 98 of sp500"). Widening coverage means adding rows to
   `internal/universe/data/*.csv`.
2. **Input a stock** — same deep-analysis agents run on one user-supplied ticker
   (screening skipped), returning a single verdict.

## Hard constraints

- **Model access: CLI subprocess by default; a keyed API is allowed for the *cheap-research
  role*, and, as a reviewed reliability exception, for a *last-resort Chief Analyst
  fallback*.** Heavy synthesis (Chief Analyst) primarily stays a `claude` CLI shell-out in
  headless/print mode (`-p`) — do not add an SDK or HTTP call for the synthesis role's normal
  path. The one deliberate exception is `chief_fallback`: an off-by-default HTTP call on the
  same `apiengine.go` engine (`internal/orchestrator/fallback.go`'s `attemptChiefFallback`),
  gated on its own `api_key` alone, that fires only after the primary `claude` call has
  exhausted its own attempt budget (`synthesis_max_attempts`, default 1) or its JSON failed to
  parse — sitting strictly before the mechanical `buildDegradedIdeas` fallback. It defaults to
  DeepSeek's `deepseek-reasoner` (a smarter tier than the cheap role's `deepseek-chat`, since
  this is a resilience call for the single most important step in the pipeline) and needs its
  own dedicated credentials (`[chief_fallback]` / `CFR_CHIEF_FALLBACK_*`) — never inherited
  from `[api]`/`[local]`, so turning on `cheap_engine=api` can never silently also enable this
  spend. The cheap-research role (scouts + specialists) may run on either a CLI or an
  OpenAI-compatible HTTP endpoint, chosen by `cheap_engine`:
  - `cheap_engine = "gemini"` (default) — the `agy` (Antigravity) CLI. Google discontinued
    the free `gemini` CLI tier ("IneligibleTierError… migrate to Antigravity"); `agy`
    exposes the same `-p`/`--model` interface. Binaries overridable via `CFR_GEMINI_BIN` /
    `CFR_CLAUDE_BIN`. `agy` auth is throttled to 1 concurrent process by default (concurrent
    launches contend on the OS keyring, time out silent auth, and escalate to a browser
    login); raise with `CFR_GEMINI_CONCURRENCY`.
  - `cheap_engine = "api"` — the native OpenAI-compatible HTTP engine in
    `internal/orchestrator/apiengine.go` (stdlib only, no SDK). Generic `/chat/completions`
    (`base_url` + `model` + key), so DeepSeek / OpenRouter / OpenAI / local vLLM all work.
    Added because `agy`/Gemini's free tier is unreliable (empty specialist outputs once the
    Gemini AI Pro subscription lapsed). Configure via `[api]` / `CFR_API_BASE_URL`,
    `CFR_API_MODEL`, `CFR_API_KEY` (or `DEEPSEEK_API_KEY`). The `agy` keyring throttle does
    not apply, so specialists parallelize up to `workers`.
  - `cheap_engine = "local"` — the *same* `CLIApi` HTTP engine pointed at a local
    OpenAI-compatible server (Ollama/llama.cpp/vLLM). It is a config selector, not a new
    runner engine: it resolves to `CLIApi` with the `[local]` config. Key is optional
    (local servers don't authenticate), and calls are throttled to `local_concurrency`
    (default 1) because one GPU can't run the 5 specialists at once. Configure via
    `[local]` / `CFR_LOCAL_BASE_URL`, `CFR_LOCAL_MODEL`, `CFR_LOCAL_KEY`,
    `CFR_LOCAL_CONCURRENCY`. Engine selection + validation live in `resolveCheapEngine`
    (`internal/orchestrator/orchestrator.go`); the pool's throttle is generic
    (`throttleCLI`/`throttleSem` in `pool.go`), covering gemini and local.
  (HTTP to *market-data* sources remains fine and expected — `internal/marketdata` talks to
  the keyless Yahoo Finance chart and option-chain APIs and to SEC EDGAR, and optionally
  FRED/AlphaVantage when keyed.)
- **Cost split:** the **cheap engine** (agy CLI, remote API, *or* a local model) does cheap,
  parallel research (screening + domain reports); **Claude** does the single heavy
  synthesis/scoring step (Chief Analyst). The split holds whichever cheap engine is selected,
  and in the common case (no `chief_fallback` configured, or the primary call succeeding)
  it holds exactly as before — the DeepSeek fallback is a reviewed reliability exception for
  when that single heavy call fails, not an abandonment of the split.
- Agent personas live in `agents/*.md` and are loaded at runtime — they are *data*, not
  Go source. Editing a persona must not require recompiling.

## Architecture map

```
cmd/cfr/        entry point + subcommands: bare = TUI, `run` (headless), `scoreboard`
internal/
  tui/          Bubble Tea screens: app (router), home, run (status), results,
                history, reports, scoreboard
  orchestrator/ pipeline driver, runner (CLI subprocess + OpenAI-compatible API
                engine in apiengine.go), bounded worker pool, optional DeepSeek
                Chief Analyst fallback (fallback.go)
  agents/       persona registry: load agents/*.md, assemble prompts
  universe/     index constituents (data/*.csv), dedupe/cap, benchmark symbols
  quant/        pure-stdlib statistical metrics (momentum, YZ vol, VR, …) — no TA
  marketdata/   HTTP data providers: Yahoo chart API (keyless), EDGAR/FRED/AV (keyed);
                exchange.go maps a ticker suffix to its currency and market close,
                fx.go converts turnover/sizing to USD, yahoocrumb.go does the
                cookie+crumb handshake the option chain now requires
  model/        shared types: Report, TradeIdea, RunState, AgentStatus
  store/        run artifacts under runs/<timestamp>/ (reports, prices/, quant.json,
                prescreen.json)
  config/       settings: defaults → ~/.config/cfr/config.toml → ./cfr.toml → env
  scoreboard/   past-idea performance: each idea replayed through its own daily
                bars (fill, then first barrier touched); `--legacy` keeps the old
                mark-to-current-price math
agents/*.md     agent persona prompts (runtime data)
agents.v1/      frozen pre-overhaul personas: the control arm of the persona A/B
                (CFR_AGENTS_DIR=agents.v1); never edited
testdata/fakebin/ fake agy/claude CLIs for hermetic tests + cheap manual TUI runs
docs/workflow/  workflow + scoring + schema specs (source of truth for behavior)
```

The pipeline and scoring rules are specified in `docs/workflow/`. When behavior is
ambiguous, those docs are the source of truth — keep code and docs in sync.

## Pipeline (independent research)

0. **Stage 0.5 — pre-screen (in-process, no model):** fetch 2y daily OHLCV for *every*
   constituent of the selected indices, compute `internal/quant` metrics, and rank each
   index on a composite (`z(mom12-1) + 0.5·z(ret63d)`, minus a short-term reversal penalty
   when the recent move runs with the trend; standardising within the index *is* the
   relative-strength adjustment, so there is no separate `rs63` term — it was arithmetically
   identical to `z(ret63d)`). Illiquid and short-history names are excluded, against turnover
   **converted to USD** (`internal/marketdata/fx.go`). Persists `prescreen.json`; the price
   series stay in the data cache.
1. **Scouts (cheap engine):** one call per index, each screening *its index's ranked
   table* → ~5–10 nominations each. Nominations outside the index's constituent list are
   dropped. Orchestrator merges/dedupes (incl. cross-listings) and trims to
   `max_shortlist` by merit — the pre-screen composite aligned with the nominated
   direction — capped at `max_per_index` per index.
2. **Stage 1.5 (in-process, no model):** compute `internal/quant` metrics for the
   shortlist (mostly cache hits from Stage 0.5), persist `prices/` + `quant.json`.
3. **Specialists (cheap engine, parallel):** News, Fundamentals, Quant, Sentiment, Macro.
   Each writes **one** report covering the whole shortlist (5 calls total — not
   per-ticker). The quant specialist interprets the computed pack; no chart TA anywhere.
   News additionally carries a bulk-fetched verified earnings calendar; sentiment reads
   SEC Form 4 insider filings and the Yahoo option chain, not news tone.
3.5. **Base scores (in-process, no model):** `basescore.go` does the weighting itself —
   `Σ w·sign·strength/10` over the **full** domain weight, so a domain with no data for a
   name votes 0 and thin coverage lowers the score directly (weighted-coverage caps remain
   as a redundant floor). Dividing by the *covered* weight instead inverted the ordering:
   one loud domain kept its full magnitude and landed on its cap while five partly
   disagreeing domains averaged down. The result is both shown to the Chief and enforced
   against its output.
4. **Chief Analyst (Claude):** reads the 5 reports + the computed base-score table +
   compact verified quant lines + — once ≥10 past ideas have closed — the pipeline's own
   replayed track record, adjusts each base by at most `chief_adjust_band` points with a
   named reason, ranks, and emits the final 5 ideas (with entry/stop/target derived from
   vol-scaled distances) as a fenced ```json block that Go parses into
   `[]model.TradeIdea`. Confidence outside the band is clamped in Go.
5. **Risk gate (in-process, no model):** `riskgate.go` sizes each idea from the account's
   risk budget and checks stop/target bands, reward:risk, liquidity and simulated
   expectancy, plus book-level correlation, sector and beta. Violations buy one corrective
   re-prompt; per-idea violations that survive it drop the idea. Shipping fewer than 5
   ideas is the intended outcome. At ≥30 closed ideas the expectancy simulation swaps its
   assumed edge for the measured one.

The loop closes through `internal/scoreboard`: past ideas are replayed through their own
daily bars, reduced to `.data/calibration.json`, and fed back into both step 4 and step 5.

Single-stock mode: shortlist = `[ticker]`, skip step 1, `topN = 1`.

## Commands

```bash
go build ./...        # build
go test ./...         # unit tests
go run ./cmd/cfr      # launch the TUI
go run ./cmd/cfr run --indices sp500,eu50 --json   # headless run (exit 0 ok / 3 degraded)
go run ./cmd/cfr scoreboard                        # past-idea performance (path replay)
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
