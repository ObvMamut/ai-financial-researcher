# CLAUDE.md — Claude Financial Researcher

Guidance for Claude Code when developing **this Go codebase**. (For your role as the
*Chief Analyst agent at runtime*, see `agents/chief-analyst.md` instead — that is a
separate concern from building the app.)

## What this project is

A Go TUI dashboard that orchestrates AI agents to propose **swing trades**. Two modes:

1. **Independent research** — agents screen four curated index samples, then deep-analyze
   a shortlist and return **5 ranked trade ideas**. The universe files are *representative
   samples*, not full index memberships: `sp500.csv` holds 98 names, `nq100.csv` 56,
   `asia100.csv` 67, `eu50.csv` 47. Scouts see exactly those names and the run log says
   how many ("screening 98 of sp500"). Widening coverage means adding rows to
   `internal/universe/data/*.csv`; `universe_test.go` rejects a duplicate ticker within one
   file and a row whose columns have shifted.
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
  FRED/AlphaVantage/Alpaca when keyed. Alpaca is a market-data source like the others and
  sits *inside* this rule, not as an exception to it: the constraint is about model access.)
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
cmd/cfr/        entry point + subcommands: bare = TUI, `run` (headless), `scoreboard`,
                `postmortem`
internal/
  tui/          Bubble Tea screens: app (router), home, run (status), results,
                history, reports, scoreboard
  orchestrator/ pipeline driver, runner (CLI subprocess + OpenAI-compatible API
                engine in apiengine.go), bounded worker pool, optional DeepSeek
                Chief Analyst fallback (fallback.go)
  agents/       persona registry: load agents/*.md, assemble prompts
  universe/     index constituents (data/*.csv), dedupe/cap, benchmark symbols
  quant/        pure-stdlib statistical metrics (momentum, YZ vol, VR, …) — no TA
  marketdata/   HTTP data providers: Yahoo chart + search-news + option chain
                (keyless), EDGAR (keyless), FRED/AV/Alpaca (keyed);
                prices.go routes daily bars per symbol — alpaca.go for US
                equities (batched, many symbols per request), yahoo.go for
                foreign listings, index benchmarks and FX; alpacanews.go and
                yahoonews.go both serve the news domain and share newsfilter.go;
                exchange.go maps a ticker suffix to its currency and market close,
                fx.go converts turnover/sizing to USD, yahoocrumb.go does the
                cookie+crumb handshake the option chain now requires;
                insidersignal.go computes the six-leg sentiment positioning
                verdict from edgarform4/edgarform144/edgarstakes/edgar13f and
                optionflow — the agent obeys it, it does not derive it;
                adr.go resolves a foreign listing to its US line from
                data/adr_map.csv, whose `venue` column gates the row (only
                NYSE/NASDAQ resolve; OTC and DELISTED rows are audit records
                the loader skips) and doubles as the foreign-private-issuer
                registry the Form 4 exemption reads
  model/        shared types: Report, TradeIdea, RunState, AgentStatus
  store/        run artifacts under runs/<timestamp>/ (reports, prices/, quant.json,
                prescreen.json)
  config/       settings: defaults → ~/.config/cfr/config.toml → ./cfr.toml → env
  scoreboard/   past-idea performance: each idea replayed through its own daily
                bars (fill, then first barrier touched); `--legacy` keeps the old
                mark-to-current-price math; horizon.go asks the different question
                of whether the *call* was right over the idea's own holding period
                (anchored at generation, so an unfilled idea still counts) net of
                its benchmark; dedupe.go counts bets rather than tickets, one per
                (ticker, direction) per week, so five runs in one afternoon are one
                observation; control.go (`--control`) scores what shipped against
                the pre-screen composite alone and against the funnel's shortlist,
                to answer whether the model stages beat their own arithmetic;
                attribution.go counts the record by setup/coverage/consensus/sector/
                fill and postmortem.go enforces the lessons an agent draws from it
                against those counts
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
   constituent of the selected indices (US names batched through Alpaca in a handful of
   multi-symbol requests when a key is configured, the rest one-at-a-time from Yahoo), compute `internal/quant` metrics, and rank each
   index on `trend` = `0.5·z(mom12-1) + z(ret63d)`, less two extension penalties —
   `0.5·strZ` (5-day) and `0.35·stretch21` (21-day, `ret21d/(σ_daily·√21)`) — each applied
   only when that move runs with `trend`, and **clamped at neutral so a penalty can
   discount a trend but never reverse it**. Standardising within the index *is* the
   relative-strength adjustment, so there is no separate `rs63` term — it was arithmetically
   identical to `z(ret63d)`. Every row is then classified first-match-wins into a **setup
   archetype** — `pullback` (composite and last month disagree), `base` (vol contracting,
   price flat), or `continuation` — because every term in the composite is a trailing
   return, so its top is by construction the names that have already run. Illiquid and
   short-history names are excluded, against turnover **converted to USD**
   (`internal/marketdata/fx.go`). Persists `prescreen.json`; the price series stay in the
   data cache.
1. **Scouts (cheap engine):** one call per index, each screening *six disjoint ranked
   tables* — continuation, pullback and base each split into a long and a short half, plus
   the bottom of the ranking — → ~5–10 nominations each. The counter-trend archetypes are
   split by direction because the composite is a signed long ranking, so a section ranked
   by it is long-only whatever the classifier found: the best shorts carry the most
   negative scores and sit at the far end of a best-first walk. Nominations outside the index's constituent list are
   dropped. Orchestrator merges/dedupes (incl. cross-listings) and trims to
   `max_shortlist` by merit — the pre-screen composite aligned with the nominated
   direction, plus a bonus per agreeing scout and a penalty when another scout nominated
   the same name the other way — capped at `max_per_index` per index. `shortlist_reserve`
   slots are held for non-`continuation` archetypes in a pass that runs first, filled only
   by candidates clearing `shortlist_reserve_min_merit` so the list ships short rather than
   padded; without it the merit sort simply undoes the archetypes, since merit *is* the
   composite and the composite rewards having run. A name three of the
   five domains cannot reach (an unmapped foreign listing: 0.45 of the weight) is capped
   by `max_thinly_covered`.
2. **Stage 1.5 (in-process, no model):** compute `internal/quant` metrics for the
   shortlist (mostly cache hits from Stage 0.5), persist `prices/` + `quant.json`.
3. **Specialists (cheap engine, parallel):** News, Fundamentals, Quant, Sentiment, Macro.
   Each writes **one** report covering the whole shortlist (5 calls total — not
   per-ticker). The quant specialist interprets the computed pack; no chart TA anywhere.
   News additionally carries a bulk-fetched verified earnings calendar; sentiment reads
   positioning, not news tone — Form 4 insider trades, Form 144 planned sales, 13D/13G
   ownership schedules, 23 tracked managers' 13F changes, and the Yahoo option chain's
   open interest *and* traded volume as two separate legs. Whether that
   positioning is *directional* is decided in Go (`marketdata/insidersignal.go`), not by the
   agent: routine scheduled selling and a put/call ratio near 1.0 are the resting state of
   the market, and reading them as bearish gave the domain one bullish score in 34 across
   four runs and a standing ~9-point levy on every long. A name whose computed verdict is
   non-directional goes to `missing` — an abstention, recorded separately from a coverage
   gap so it does not degrade the run.
3.5. **Base scores (in-process, no model):** `basescore.go` does the weighting itself —
   `Σ w·sign·strength/10` over the **full** domain weight, so a domain with no data for a
   name votes 0 and thin coverage lowers the score directly (weighted-coverage caps remain
   as a redundant floor). Dividing by the *covered* weight instead inverted the ordering:
   one loud domain kept its full magnitude and landed on its cap while five partly
   disagreeing domains averaged down. That sum is then scored against
   `model.ReferenceTotal` — the strongest verdict the five rubrics permit (strength 8, or 5
   for macro, whose persona caps itself there) — rather than against an unreachable 10
   across the board, which had pinned every run's output under 46. It is one constant for
   every name, so it rescales without reordering. The result is both shown to the Chief and
   enforced against its output; `internal/tui`'s confidence bar and `internal/scoreboard`'s
   buckets read the same scale, and all three must move together.
3.75. **Post-mortem (cheap engine, one call):** once ≥10 past ideas have closed,
   `internal/scoreboard/attribution.go` counts the record by setup shape, coverage,
   consensus, sector and fill rate, and one cheap-engine call turns those cells into prose
   lessons. Every lesson must name a cell that exists with ≥5 closed trades or it is
   deleted, exactly as a specialist's ungrounded score is. Weight suggestions are advisory
   and never applied. Never blocks a run; surfaced by `cfr postmortem`.
4. **Chief Analyst (Claude):** reads the 5 reports + the computed base-score table +
   compact verified quant lines + — once ≥10 past ideas have closed — the pipeline's own
   replayed track record and the lessons drawn from it, adjusts each base by at most
   `chief_adjust_band` points with a named reason, ranks, and emits the final 5 ideas (with entry/stop/target derived from
   vol-scaled distances) as a fenced ```json block that Go parses into
   `[]model.TradeIdea`. Confidence outside the band is clamped in Go. The entry band is
   **asymmetric**: a limit on the patient side (a long below the last close, a short above
   it) may reach `entry_patience_sigma·σ_daily·√5` (default 1.5), one on the chasing side
   only `entry_chase_sigma` (default 0.5) — waiting risks an `unfilled`, chasing risks a
   fill at the top of the move.
5. **Risk gate (in-process, no model):** `riskgate.go` sizes each idea from the account's
   risk budget and checks stop/target bands, reward:risk, liquidity and simulated
   expectancy, plus book-level correlation, sector and beta-adjusted exposure measured
   against the account rather than averaged over the idea count. Violations buy one corrective
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
go run ./cmd/cfr scoreboard --control              # shipped vs the composite alone vs the shortlist
go run ./cmd/cfr postmortem                        # attribution cells + the stored lessons
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
