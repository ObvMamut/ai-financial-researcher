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

- **Model access: the Chief Analyst engine is selected by `chief_engine`; the
  cheap-research role is selected by `cheap_engine`.** Heavy synthesis (Chief
  Analyst) runs either as a `claude` CLI shell-out in headless/print mode (`-p`)
  or through the OpenAI-compatible HTTP engine in `internal/orchestrator/apiengine.go`,
  chosen by `chief_engine` (`claude` | `api`; omitted means `claude`, so a
  configuration that does not set it behaves exactly as it did before). The
  selector exists and is tested. The operator has since switched this project's
  untracked local `cfr.toml` to `chief_engine = "api"` (DeepSeek
  `deepseek-v4-pro`) with `research_mode = "thesis"`, so a local run is not a
  Claude Chief run unless overridden; `chief_engine=claude` switches it back in
  one setting. An API Chief needs its own dedicated credentials (`[chief_api]` / `CFR_CHIEF_API_*`),
  **never** inherited from `[api]`, `[local]` or `DEEPSEEK_API_KEY`, so turning
  on `cheap_engine=api` can never silently also spend on synthesis. Do not add
  an SDK — the API Chief reuses the existing stdlib HTTP engine.
  The separate off-by-default `chief_fallback` engine
  (`internal/orchestrator/fallback.go`'s `attemptChiefFallback`) is unchanged and
  remains a Claude-primary-only resilience call: it fires after the primary
  `claude` call exhausts `synthesis_max_attempts` or its JSON fails to parse,
  strictly before the mechanical `buildDegradedIdeas` fallback, and it is never
  attempted when `chief_engine=api`. `[chief_fallback].enabled` distinguishes
  an omitted key (fallback stays enabled when credentials are present, as
  before) from an explicit `false`. The cheap-research role (scouts + specialists) may run on either a CLI or an
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
  parallel research (screening + domain reports); **the selected Chief engine** does the single heavy
  synthesis/scoring step (Chief Analyst). The split holds whichever cheap engine is selected,
  and in the common case (no `chief_fallback` configured, or the primary call succeeding)
  it holds exactly as before — the DeepSeek fallback is a reviewed reliability exception for
  when that single heavy call fails, not an abandonment of the split.
  The split is about *which model does which work*, not about which transport
  carries it: an API Chief is still one heavy synthesis call on a strong model,
  addressed through its own credentials and its own cap, never the cheap
  research model.
  The second reviewed exception is dossier compaction: the one repair of an
  oversized thesis dossier runs on the selected Chief engine, because the cheap
  model measurably cannot make the cut (15 recorded compactions at 0.61–0.99 of
  narrative size whatever was asked; the Chief model met 12/12 per-field
  budgets). It fires at most once per researcher response (each research round
  and the revision can each earn one), only when that response is over budget,
  and runs in parallel up to `workers`; the model returns only the twelve
  narrative fields — Go splices them into the original payload.
  The third reviewed exception is opt-in: `[research] researcher_engine = "chief"`
  (env `CFR_RESEARCH_RESEARCHER_ENGINE`) moves only the thesis-researcher calls
  onto the selected Chief engine and its dedicated credentials, with the ordinary
  retry budget (`researchTarget` in `chief.go`). The default is `"cheap"`, which
  keeps the split exactly as before. It exists because six live thesis runs on
  2026-09-24 got every name researched and still shipped nothing: an audit found
  the challenger's grounding and direction objections to the cheap model's
  dossiers correct, so research quality, not the gate, was the limit. The
  challenger, triage, discovery and schema repairs stay on the cheap engine.
- Agent personas live in `agents/*.md` and are loaded at runtime — they are *data*, not
  Go source. Editing a persona must not require recompiling.

## Architecture map

```
cmd/cfr/        entry point + subcommands: bare = TUI, `run` (headless), `scoreboard`,
                `postmortem`, `backtest`
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
                edgarreports.go walks SEC's *daily* index for the last 10-Q/10-K
                per filer — one ~1MB file per session, immutable once published
                so it caches forever and a run fetches only new days, where the
                quarterly index is 55MB and per-issuer submissions are ~150
                requests; it feeds Stage 0.5's drift leg;
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
  backtest/     the lab (`cfr backtest`): point-in-time weekly replay of the
                pre-screen over every constituent with the shipping quant/scoring
                code, no models; rank ICs, barrier grid and the pre-registered
                signal tests; spec and results in docs/workflow/backtest.md
agents/*.md     agent persona prompts (runtime data)
agents.v1/      frozen pre-overhaul personas: the control arm of the persona A/B
                (CFR_AGENTS_DIR=agents.v1); never edited
testdata/fakebin/ fake agy/claude CLIs for hermetic tests + cheap manual TUI runs
docs/workflow/  workflow + scoring + schema specs (source of truth for behavior)
```

The pipeline and scoring rules are specified in `docs/workflow/`. When behavior is
ambiguous, those docs are the source of truth — keep code and docs in sync.

## Pipeline (independent research)

The numbered pipeline below describes the default `research_mode = "legacy"`.
The opt-in `thesis` path branches after the pre-screen into event/price discovery,
per-company source research, challenge and Claude-led selection. Its source of
truth is [thesis-research.md](docs/workflow/thesis-research.md); implementation is
`internal/orchestrator/thesis*.go`, runtime personas are `agents/thesis-*.md`.
It has schema v2, a 10–15-session horizon, no weighted confidence anchor, and no
mechanical trade fallback. Writing targets are advisory within hard byte budgets;
a complete oversized dossier may use its one repair allowance for evidence-preserving
narrative compaction, explicitly checked by the challenger. Reviewed entry conditions
and future monitoring are separate from unresolved core evidence. Every dossier gives a
`lean`/`conviction` (NONE needs a `none_reason`) and event labels; challenger issues are
categorised, and only blocking categories, unsupported core claims or disputed claims keep a
dossier off supported — disclosed risks ship as the plan's `risks`. All-failed research
skips Chief synthesis and still persists degraded decisions and research counts. Preserve the configured Chief/cheap-engine split.
`scoreboard --research-compare` compares separate cohorts at 10 and 15 sessions.
`research-pair` explicitly registers and collects a common frozen corpus for
legacy/thesis arms, with private caches and disabled model tools. It requires
the existing API/local cheap engine and uses the configured Chief engine with its dedicated credentials.
Its later `--evaluate --refresh` path saves outcome prices separately, without
rerunning models or changing calibration. See the paired-evaluation section in
the thesis workflow for acquisition cutoffs and maturity limits.

0. **Stage 0.5 — pre-screen (in-process, no model):** fetch 2y daily OHLCV for *every*
   constituent of the selected indices (US names batched through Alpaca in a handful of
   multi-symbol requests when a key is configured, the rest one-at-a-time from Yahoo), compute `internal/quant` metrics, and rank each
   index on `trend` = `0.5·z(mom12-1) + z(ret63d)`, less two extension penalties —
   `0.5·strZ` (5-day) and `0.35·stretch21` (21-day, `ret21d/(σ_daily·√21)`) — each applied
   only when that move runs with `trend`, and **clamped at neutral so a penalty can
   discount a trend but never reverse it**. Standardising within the index *is* the
   relative-strength adjustment, so there is no separate `rs63` term — it was arithmetically
   identical to `z(ret63d)`. Every row is then classified first-match-wins into a **setup
   archetype** — `drift`, `pullback` (composite and last month disagree), `base` (vol
   contracting, price flat), or `continuation` — because every term in the composite is a
   trailing return, so its top is by construction the names that have already run.
   `drift` is tested first and is the only archetype not read off trailing returns: the
   name filed a 10-Q/10-K inside the last 25 sessions (SEC's daily index via
   `marketdata/edgarreports.go`; US filers only, additive — no `contact_email` means no
   drift leg) and repriced on it by ≥1.5σ over a two-session window measured net of its
   benchmark, with the decayed reaction still clearing half that. It exists because every
   other term is a 3- or 12-month factor while this system holds for 2–3 weeks, and
   post-earnings drift is the one documented effect on that clock. Its direction is the
   sign of the *reaction*, not of the composite — a name that ran all year and then missed
   is a short sitting at the top of the ranking — so both its table and `meritComposite`
   split on the drift rather than on the score. Illiquid and
   short-history names are excluded, against turnover **converted to USD**
   (`internal/marketdata/fx.go`). Persists `prescreen.json`; the price series stay in the
   data cache.
1. **Scouts (cheap engine):** one call per index, each screening *eight disjoint ranked
   tables* — drift, pullback and base each split into a long and a short half, plus
   continuation and the bottom of the ranking — → ~5–10 nominations each. The counter-trend archetypes are
   split by direction because the composite is a signed long ranking, so a section ranked
   by it is long-only whatever the classifier found: the best shorts carry the most
   negative scores and sit at the far end of a best-first walk. Nominations outside the index's constituent list are
   dropped. Orchestrator merges/dedupes (incl. cross-listings) and trims to
   `max_shortlist` by merit — the pre-screen composite **scaled by the coverage the run can
   bring to the name**, aligned with the nominated direction, plus a bonus per agreeing
   scout and a penalty when another scout nominated the same name the other way — capped
   at `max_per_index` per index and at `max_per_sector + 1` per sector. The sector cap
   exists because the risk gate refuses more than `max_per_sector` ideas in one sector and
   nothing upstream knew that; the shortlist carries one spare per sector so the gate has
   something to choose between rather than only something to truncate. The agreement bonus
   is paid only for *independent* nominations: 35 of nq100's 56 names are also in sp500, so
   two scouts naming one of those are two readings of one price history, not cross-index
   agreement. `shortlist_reserve` slots are held for non-`continuation` archetypes in a
   pass that runs first, filled only by candidates clearing `shortlist_reserve_min_merit`
   so the list ships short rather than padded; without it the merit sort simply undoes the
   archetypes, since merit *is* the composite and the composite rewards having run. A name
   three of the scoring domains cannot reach (an unmapped foreign listing: quant alone,
   0.35 of the weight) is excluded outright — `max_thinly_covered` defaults to 0, because
   the risk gate's evidence floor deletes a quant-only idea and reserving slots for such a
   name spends five specialist reports on a candidate that cannot ship. A negative value
   re-admits them for an experiment.
2. **Stage 1.5 (in-process, no model):** compute `internal/quant` metrics for the
   shortlist (mostly cache hits from Stage 0.5), persist `prices/` + `quant.json`. A name
   whose newest bar still trails its own market's last completed session after the one
   forced refetch leaves the shortlist here, before the specialists are paid for it: every
   level is computed to the cent off that close, so the name cannot produce an actionable
   idea and used to be deleted after synthesis instead. Single-stock mode never drops, and
   nor does a drop that would empty the shortlist.
3. **Specialists (cheap engine, parallel):** News, Fundamentals, Quant, Sentiment, Macro.
   **Quant and Macro are blinded** to the direction the scout nominated
   (`agents.blindToDirection`): both read evidence derived from the same price history the
   nomination is, and telling them the answer is what made quant agree with it on 11-12 of
   12 names in every run since the pre-screen existed, against 3/3, 1/2, 5/9 and 0/5 before
   it. **Macro carries zero weight** — a regime is one fact per market, and scoring it per
   name turned three facts into twelve confirmations. Under the default
   `selection = "merit_veto"` the macro call is **not made**: `regime.go`'s
   `computedRegimeLines` gives the Chief one line per benchmark (63-session return sign,
   21-session realized vol against its 1y median) from bars already fetched; under
   `selection = "chief"` macro still runs and the Chief reads it as context.
   Each writes **one** report covering the whole shortlist (4 or 5 calls total — not
   per-ticker). News, fundamentals, quant and sentiment keep their scores and also emit
   closed-schema **labels** per name (`move_driver`, `pending_binary_event` + date,
   `corporate_action`, and a `veto` whose `veto_reason` must be one of
   `binary_event_inside_window|corporate_action_pending|halted_or_illiquid|data_error|
   fraud_or_litigation_shock`); `labels.go` parses them defensively — malformed is
   unknown, unknown is never a veto, an out-of-enum reason is refused, and a label for a
   name the domain had no verified data for is dropped. The quant specialist interprets the computed pack; no chart TA anywhere.
   Fundamentals additionally reads the verified earnings-reaction table (`driftBlock`, the
   same one the Chief gets): its other evidence is one reporting period plus a YoY growth
   rate, which is a description of a company rather than of the next fifteen sessions, and
   post-earnings drift is the one documented fundamental effect on this clock. Its persona
   is written around the horizon — a rich multiple alone is not bearish over 15 sessions —
   and caps the reaction at strength 5 where the name's setup is already `drift`, so the
   domain cannot re-vote the screen's own ranking. Per-role prompt assembly lives in
   `specialistDataBlock`.
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
   across the board, which had pinned every run's output under 46. One domain leaves that
   reference for one name, under one condition: its own *computed* verdict said it looked and
   found nothing directional (`standDowns` over `abstainedFor`; sentiment alone today). A gap
   and an abstention were arithmetically identical and are not the same fact — sentiment
   abstains on ~70% of names by design, and charging its full 0.17 for that was a flat
   ~21-point haircut for the domain working correctly. `signed` and `covered` still measure
   against the full weight, so the coverage caps keep their grip and a loud-domain-plus-
   abstentions name still cannot outrank a well-covered one. Measured over the 12 runs to
   2026-09-05, the best base on a board runs 41–67. The result is both shown to the Chief and
   enforced against its output; `internal/tui`'s confidence bar and `internal/scoreboard`'s
   buckets read the same scale, and all three must move together.
3.75. **Post-mortem (cheap engine, one call):** once ≥10 past ideas have closed,
   `internal/scoreboard/attribution.go` counts the record by setup shape, coverage,
   consensus, sector and fill rate, and one cheap-engine call turns those cells into prose
   lessons. Every lesson must name a cell that exists with ≥5 closed trades or it is
   deleted, exactly as a specialist's ungrounded score is. Weight suggestions are advisory
   and never applied. Never blocks a run; surfaced by `cfr postmortem`.
4. **Selection (`selection`, legacy independent mode only).** The 2026-09-23 attribution
   found no measurable value above the funnel (domain ICs −0.08…+0.07; Chief picks +0.55%
   vs +0.58% for the names it left out), so the default **`merit_veto`**
   (`selection.go`) takes ranking away from the models: Go orders the shortlist by the
   funnel's own `meritScore`, drops names with no scout direction, names any specialist
   vetoed and names the per-idea risk gate (incl. the evidence floor) refuses, holds each
   sector to `max_per_sector`, and ships the top 5 **in the scout's direction** as
   `market_on_open` ideas (reference close, catastrophe stop, no target, 15-session time
   exit; confidence = the base score, informational). The Chief is called once with
   `agents/chief-writer.md`: it writes `why`/`position_note` for the book and five
   reserves, may veto from the same closed enum (the slot refills from the reserves in
   merit order), may not add, flip or reorder names, and returns `shadow_rank` — its own
   ranking of the whole shortlist, persisted in `ideas.json` and `data/selection.json`,
   never acted on. A failed or unparseable Chief ships the selection without prose
   (`degraded`); no corrective re-prompt or DeepSeek fallback runs under this policy.
   Both policies write `data/selection.json` (every shortlisted name: merit rank, labels,
   vetoes, exclusion reason, domain scores, shipped rank, shadow rank), which feeds the
   scoreboard's `chief-shadow` and `vetoed` arms. `selection = "chief"` (A/B control) is
   the Chief Analyst path below; single-stock mode always uses it.

   **Chief Analyst (Claude, `selection = "chief"`):** reads the 5 reports + the computed base-score table +
   compact verified quant lines + — once ≥10 past ideas have closed — the pipeline's own
   replayed track record and the lessons drawn from it, adjusts each base by at most
   `chief_adjust_band` points with a named reason, ranks, and emits the final 5 ideas (with entry/stop/target derived from
   vol-scaled distances) as a fenced ```json block that Go parses into
   `[]model.TradeIdea`. Confidence outside the band is clamped in Go. New ideas (both
   research modes) are `entry_type: "market_on_open"`: entry is re-based on the verified
   last close, the fill is the next open, the stop is a catastrophe stop floored in Go at
   `catastrophe_stop_sigma·σ_daily·√h` (default 2.0), the target is optional, and the
   position exits on time — the 2026-09-23 backtest found every target and every nearer
   stop cost return, and live patient limits were adversely selected. `risk.entry_type =
   "limit"` restores the asymmetric `entry_patience_sigma`/`entry_chase_sigma` limit band,
   which binds only on limit ideas (and on every pre-field `ideas.json` the scoreboard
   replays).
5. **Risk gate (in-process, no model):** `riskgate.go` sizes each idea from the account's
   risk budget and checks stop/target bands, reward:risk, liquidity and simulated
   expectancy (for market-on-open ideas only the catastrophe-stop band and liquidity;
   expectancy is recorded, not gated), plus book-level correlation, sector and beta-adjusted exposure measured
   against the account rather than averaged over the idea count. It also enforces an
   **evidence floor** (independent mode only): an idea scored by quant alone, or by no
   domain at all, is a screen output rather than a research conclusion and is dropped.
   Under `merit_veto` the per-idea checks run on each merit candidate before selection
   (a refusal makes it ineligible, recorded as `risk_gate`) and the final book is gated
   once more for sizing and book-level findings; there is no re-prompt. Under `chief`, violations buy one corrective
   re-prompt, which offers both available answers — delete the idea, or replace it from the
   scored board the re-prompt lists, never to keep the count and never with a name a single
   domain carries; per-idea violations that survive it drop the idea. Shipping fewer than 5
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
go run ./cmd/cfr scoreboard --control              # shipped vs composite, shortlist, Chief shadow, vetoed
go run ./cmd/cfr postmortem                        # attribution cells + the stored lessons
go run ./cmd/cfr backtest                          # point-in-time pre-screen replay (keyless Yahoo, no models)
go run ./cmd/cfr acceptance-manifest               # resolved configuration and hashes; no model or data requests
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
