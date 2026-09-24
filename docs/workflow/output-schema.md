# Output Schemas

The machine-readable contracts between agents and Go. Agents emit a fenced ```json block;
Go parses it. Keep these in sync with `internal/model/types.go`.

The opt-in [thesis schema v2](thesis-research.md#plans-and-enforcement) additionally
uses `internal/model/research.go`: per-company dossiers, evidence claims and
requests, challenges, explicit selection decisions and thesis execution plans.
`ideas.json` and metadata identify `research_mode: "thesis"` and `schema_version: 2`.
An explicit empty ideas array is valid; a missing array is a parsing failure.
Thesis dossiers additionally require `long_case`, `short_case`, `no_trade_case`
and `preferred_direction` (BUY, SELL, NONE). Each research/review claim carries
`passages`: objects with `evidence_id`, verbatim `quote` (at least 30 characters)
and `issuer_role`. Evidence snapshots can carry `source`, `parent_id` and
`truncated`. Research artifacts include computed `temporal_facts`; model inputs
persist the selected excerpts. Early event deferrals have `not_run` outcomes
and watchlist decisions blocked by `awaiting_event` or `awaiting_prices`.
Older saved schema-v2 trade plans remain readable; new dossier requirements
apply to newly generated research.

Optional thesis fields include dossier `entry_conditions` and `monitoring`, plan
`monitoring`, reviewer `conditions_reviewed` and `compaction_assessment`, and
Go-computed result `research_summary`. Research outcomes record contract recovery
and actual repair/compaction counts separately from transport and parsing. Writing
length targets are advisory under unchanged total byte budgets. See the thesis
workflow's reliability and conditional-research sections for their validation rules.


The contracts below describe the legacy specialist/scoring path.

## Shortlist (Scout → orchestrator)

Each scout ends its report with:

````
```json
{
  "index": "sp500",
  "candidates": [
    { "ticker": "AAPL", "name": "Apple Inc.", "bias": "bullish", "reason": "breakout above range on volume ahead of product cycle" }
  ]
}
```
````

`bias`: `bullish` | `bearish` | `neutral` — any other value is read as `neutral`, and a
neutral nomination is merged as if the pre-screen supported neither direction.

The orchestrator **validates every nomination against the constituent list that scout was
handed**: a symbol not in it is dropped and logged, and the surviving names take their
ticker spelling, company name, sector and index from the universe row rather than from the
model. It then merges all scouts, dedupes by canonical ticker (including cross-listings
like `ASML`/`ASML.AS`), and trims the combined shortlist to `max_shortlist` (default 12)
by pre-screen merit — see `independent-research.md`, Stage 1.

`shortlist.json` therefore carries more than the scout wrote: `sector` and `index` come
from the universe, and `setup` — the pre-screen archetype, `continuation` / `pullback` /
`base` — comes from the name's own pre-screen row rather than from the scout, since the
scout picks which table to nominate from but the classification is computed from price
data. `bias` and `reason` are the scout's and travel on into every specialist and Chief
Analyst prompt. Two more fields record what the merge found: `nominations` (how many
scouts wanted this name in this direction, omitted when 1) and `contested` (the indices
that nominated it the *other* way). Both feed the merit sort, and `setup` gates the
archetype reserve.

## Pre-screen (`runs/<ts>/prescreen.json`)

Written by Stage 0.5 before any model call. One row per constituent of the selected
indices, ranked best-composite-first with excluded rows last:

````
```json
{
  "as_of": "2026-08-28",
  "indices": ["sp500", "nq100"],
  "params": {
    "top_per_index": 15, "pullback_per_index": 8, "base_per_index": 5, "bottom_per_index": 5,
    "adv_min_usd": 20000000, "min_bars": 60, "vol_trend_flag": 1.5,
    "formula": "0.5·z(mom12-1) + z(ret63d) − 0.5·strZ − 0.35·stretch21, each penalty only when that move runs with the composite; mom/ret63d z-scored within index, strZ and stretch21 already per-name sigma units"
  },
  "rows": [
    { "ticker": "NVDA", "name": "NVIDIA Corporation", "sector": "Information Technology",
      "index": "sp500", "as_of": "2026-08-28", "bars": 501, "close": 176.42,
      "mom_12_1": 0.482, "ret_63d": 0.191, "ret_21d": 0.064, "ret_5d": 0.012,
      "rs_63": 0.114, "str_z": 0.4, "vol_yz_20": 0.38, "vol_trend": 1.12,
      "regime": "trending", "adv_usd": 3.1e10, "adv_local": 3.1e10, "currency": "USD",
      "price_to_52w_high": 0.94, "stretch_21": 0.62,
      "setup": "continuation", "trend": 2.53, "score": 2.31 }
  ],
  "errors": ["005930.KS: yahoo 005930.KS: empty chart result"]
}
```
````

An excluded row carries `"excluded"` with the reason (`illiquid …`, `insufficient history
…`, `no price history`), `"score": 0` and **no `setup`** — it is not a candidate in any
shape; it is never ranked and never contributes to the within-index mean or standard
deviation. `params.formula` is recorded so a row's `score` is legible without reading the
source.

`trend` is the composite *before* its extension penalties, and `score` is what remains
after them. A `score` well below its `trend` means the name was charged for how far it has
already travelled: `stretch_21` (the 21-day return in units of the name's own 21-day sigma)
and `str_z` (the same over five days) are what it was charged on. `setup` is the archetype
the row was classified under — `continuation`, `pullback` or `base` — and it travels
onward to `shortlist.json` and to each idea in `ideas.json`.

## Specialist report (each specialist → Chief Analyst)

Human-readable analysis, then a structured tail:

````
```json
{
  "domain": "quant",
  "scores": [
    { "ticker": "AAPL", "bias": "bullish", "strength": 7, "note": "positive 12-1 momentum, trending VR(5), calm vol [verified]" }
  ],
  "missing": []
}
```
````

`domain`: `news` | `fundamentals` | `quant` | `sentiment` | `macro`. `strength`: integer
`0–10` per each persona's anchored rubric. `missing`: tickers the specialist could not
assess.

The legacy `technicals` domain is gone: the quant stage computes everything its
AlphaVantage `GLOBAL_QUOTE` call carried, from a full 2-year series rather than a single
snapshot and for every listing rather than US ones. Old artifacts still render — the TUI
and `DomainWeights` both still read the name — but no new run produces one, and it no
longer carries weight in the scoring.

### The no-data convention

A ticker belongs in **exactly one** of the two arrays:

- in `scores` **iff** the specialist evaluated it against data the run supplied;
- in `missing` **iff** the specialist had no data for it.

There is no third convention. Do not score a name at `strength: 0` or `1` to mean
"no data", and do not silently omit it — those were the three different dialects
news, fundamentals and sentiment each invented, and they corrupted every
aggregate that read across domains.

**The app enforces this after the fact and does not take the agent's word for it.**
Before a report is written to disk or shown to the Chief Analyst, the orchestrator
compares `scores` against the coverage it actually assembled and rewrites the tail:

- a score for a shortlisted ticker the domain had no verified data for is
  **deleted**, and the ticker is unioned into `missing` (recorded in the run's
  `domains[].corrected_scores`);
- a score whose ticker is really the shortlisted **company's name** — `OCBC` for
  `O39.SI`, `KAKAO` for `035720.KS` — is **resolved** to the symbol and kept, with the
  tail rewritten so the rest of the pipeline can key on it. The shortlist block every
  specialist reads carries `name` beside `ticker`, and macro answered in the former on
  2026-09-03; all three scores were struck as off-shortlist, and those names then shipped
  as ideas 3 and 5 on one domain each. Three alias forms are recognised — the full name,
  its first word, and its initials — and only where they collide with nothing else on the
  shortlist and shadow no real ticker;
- a score for a ticker that was never on the shortlist is **deleted** and nothing
  is added to `missing` — the run never asked about it (recorded in
  `domains[].off_shortlist_scores`);
- a score for a ticker the agent *itself* put in `missing` is **deleted**: a report
  that disclaims its own number should not have that number weighted (recorded in
  `domains[].self_contradicted_scores`);
- when any score is deleted, an **enforcement notice** is prepended to the report
  naming those tickers and stating that any prose about them is unscored context.
  Deleting the number is not enough on its own: on 2026-09-01 macro's scores for
  four non-US names were correctly removed while its paragraphs about them stayed,
  and the Chief read the prose and took two −3 adjustments from a domain the app had
  just ruled could not see those names;
- a report with no parseable JSON tail is not a report. The domain is marked
  `failed`, whatever the process exit code said.

Nothing is deleted for a fourth case, but it is **recorded**: a `neutral` score on a
ticker the domain did have evidence for lands in `domains[].neutral_scores`. A genuine
standoff is a legitimate verdict, so the number stands — but sign 0 contributes nothing
to the weighted sum while consuming the domain's full weight, which makes it *costlier
than a gap* and earns none of the coverage relief a gap would. Before it was recorded it
was indistinguishable from a name the domain had nothing on: news scored AMGN neutral 0
while its own report carried a triple-sourced regulatory suspension, and the run had no
way to see the difference.

Coverage is computed per domain, and it is per *evidence kind*, not per fact — a domain
counts a ticker covered only when it holds a fact of its own kind for it
(`marketdata.HasDomainEvidence`). News used to count itself grounded on the shared
earnings-calendar entry alone, so 2330.TW read as covered with every headline rate-limited
away. Two domains ground on something other than a provider fact: **macro** on the
computed regime block for the benchmark of the name's own exchange — which is global, and
is what its persona calls its primary evidence — with FRED's US series as context on top
rather than as the gate; and **sentiment** on the *computed* positioning verdict
(`marketdata.HasPositioningSignal`), not on whether the fetch returned rows, because
scheduled insider selling and a put/call near 1.0 are the resting state of the market.
EDGAR and the option chain reach US listings only (or a foreign listing's US line); Yahoo's
chart and headline feeds, which ground quant and news, are global and keyless.

## Final trade ideas (Chief Analyst → Go → TUI)

The deliverable: direction, confidence, trade mechanics, quick why.

````
```json
{
  "mode": "independent",
  "generated_at": "2026-06-01T14:30:05Z",
  "ideas": [
    {
      "rank": 1,
      "ticker": "AAPL",
      "name": "Apple Inc.",
      "index": "nq100",
      "direction": "BUY",
      "confidence": 78,
      "setup": "pullback",
      "entry": 212.0,
      "stop": 201.5,
      "target": 233.0,
      "risk_reward": 2.0,
      "timeframe_days": 15,
      "position_note": "half size into Jul 30 earnings",
      "why": "Momentum confluence with upbeat product-cycle catalyst; macro and fundamentals supportive, sentiment not yet crowded.",
      "price_at_generation": 211.4,
      "base_confidence": 72,
      "domain_scores": {"quant": 8, "news": 6, "fundamentals": 4, "sentiment": 0, "macro": -2},
      "consensus": 0.72,
      "currency": "USD",
      "shares": 47,
      "notional": 9964.0,
      "risk_amount": 493.5,
      "expectancy_bps": 34.2,
      "expectancy_r": 0.041,
      "breakeven_win_rate": 0.333
    }
  ],
  "notes": "Optional: caveats, missing-data flags, or why fewer than 5 ideas were returned."
}
```
````

Field rules:
- `direction`: `BUY` | `SELL`.
- `confidence`: integer `0–100` (see `scoring.md`).
- `entry_type` (set by Go, never by the model): `market_on_open` for every idea generated
  since 2026-09-23 unless `risk.entry_type = "limit"`. Absent means a limit — every older
  `ideas.json` — and the scoreboard replays it as one.
- `entry`/`stop`/`target`: prices in the ticker's local currency, consistent with the
  verified last close. For `market_on_open`, `entry` is replaced by the verified last close
  (a reference price; the fill is the next open), `stop` is a catastrophe stop widened in Go
  to at least `catastrophe_stop_sigma`·σ_daily·√h, and `target` is optional and
  informational. For a limit: BUY stop < entry < target; SELL target < entry < stop; stops
  1–2 × σ_daily·√h from entry (h = `timeframe_days`).
- `risk_reward`: |target−entry| / |entry−stop| — Go recomputes it and corrects claims off
  by more than 20%.
- `timeframe_days`: expected holding period in trading days (5–20).
- `position_note`: sizing/hedging guidance (may be empty).
- `why`: 1–2 sentences, concrete.
- `ideas`: length 5 for independent research, 1 for single stock — fewer allowed if the
  bar isn't met (explain in `notes`).

Written by Go, never by the model (a value the Chief supplies for any of these is
overwritten):
- `price_at_generation`: the verified last close from the quant pack, the scoreboard's P&L
  baseline.
- `base_confidence`: the computed weighted domain score this idea's confidence was anchored
  to, **for the direction the idea proposes** (0 when the domains read the other way). See
  `scoring.md`.
- `domain_scores`: the per-domain signed strengths (−10…+10) behind that base, recorded so
  the scoreboard can attribute a result to the domains that called it.
- `consensus`: `|Σ w·sign·strength| / Σ w·strength` ∈ [0, 1], rounded to two places — how much
  of the evidence's magnitude survived the domains disagreeing. 1 means every domain that
  spoke pointed the same way; 0 means they cancelled exactly. It reads no evidence
  `base_confidence` does not already read and **changes no ranking, cap or band**; it is a
  second axis, because `base` is one number doing two jobs (*how much evidence* and *how
  much agreement*) and a thin unanimous name and a thick split one land in the same place.
  Shown as `agree` in the Chief's base-score table and under the TUI confidence bar, and
  bucketed by the scoreboard's `by_consensus` attribution. See `scoring.md`.
- `currency`: the ISO code `entry`, `stop` and `target` are quoted in — what an order is
  actually placed in.
- `shares` / `notional` / `risk_amount`: the computed position — the account's risk budget
  divided by the idea's own stop distance, capped at 25% of equity. The budget is converted
  into `currency` before it meets the stop distance, and `notional`/`risk_amount` come back
  **in USD** so a book spanning four exchanges adds up to one exposure. An absent `shares`
  means no whole share could be bought; the run says so in its warnings rather than shipping
  an idea that merely looks complete. (Doing this arithmetic in mixed units is how the
  2026-09-01 run shipped its first and third ideas — both Tokyo listings — with no position
  at all.)
- `expectancy_bps`: the simulated expected value in basis points of entry, net of costs.
  Kept because it is what an operator reads and what past runs recorded — it is **not**
  what the gate judges.
- `expectancy_r`: the same expectancy divided by the trade's own risk (`|entry−stop|/entry`),
  rounded to three places. This is the figure the risk gate tests against `min_expectancy_r`.
  In basis points expectancy is proportional to the stop distance, so a floor denominated
  in bps grades the name's volatility rather than its construction: on 2026-09-01 five ideas
  with near-identical normalised geometry scored +28.7 down to −3.1 bps in exact order of
  `σ_daily`, and a 10bps floor dropped the three calmest. Once `n_closed ≥ 30` this figure
  is the simulation blended with the measured record. See `scoring.md`.
- `breakeven_win_rate`: `risk / (risk + reward)`, the hit rate the geometry alone demands.

An idea that violates a hard risk limit (stop band, target ceiling, reward:risk floor,
liquidity, or an expectancy below `min_expectancy_r`) is **dropped** after one corrective
re-prompt, with the reason appended to `notes`. Fewer than 5 ideas is the intended outcome in that case. See
`scoring.md` for the limits.

All mechanics fields are optional (`omitempty`) so ideas.json from older runs still loads.

## Run metadata (`runs/<ts>/metadata.json`)

`model.RunMeta` — the run's account of itself. `orchestration.md` tables the provenance
fields (`engine`, `synthesis_model`, `stages`, `data_errors`, `persona_sha`) and the
shape-change warnings `data_errors` carries; the two the scoring path reads are:

- `thinly_covered`: the shortlisted tickers the run's sources could ground **less than 0.6
  of the total domain weight** for (`thinCoverage`, `basescore.go`) — the same threshold
  that caps their confidence at 55. It is deliberately **not** in `warnings`: a known
  structural limit is not a warning, and SEC filings and listed option chains are US
  instruments, so a foreign listing with no US line cannot be reached by fundamentals or
  sentiment however well the run went. `max_thinly_covered` caps how many of these may take
  shortlist slots, and defaults to **0**: the risk gate's evidence floor deletes an idea
  scored by quant alone, so such a name cannot reach the output and a slot spent on it buys
  five specialist reports for a candidate guaranteed to be dropped. The field therefore
  reads empty on a default run; a negative `max_thinly_covered` re-admits them.

  It was `quant_only` until news and macro became globally groundable. After that, "no
  provider reaches this at all" was true of nothing — the field would have read empty on
  every run while three of five domains were still missing on some names — so the field now
  measures the outcome (weight actually reachable) rather than the capability (reachable at
  all), in the same units as the base score.

  It stayed empty on every run anyway until 2026-09-03, because news was still counted as
  globally groundable: `quant .35 + news .25 + macro .10 = 0.70` put every listing in the
  universe above the 0.6 floor. It is not — the keyless headline search takes the local
  symbol and answers, but on that run it answered all five unmapped foreign names with the
  same eight stories, none tagged to any of them, and the news domain recorded every one as
  `missing`. News now follows the same reachability rule as fundamentals and sentiment, an
  unmapped foreign listing expects **0.45**, and the field has something to report.

- `warnings`: everything about the *output* a reader must not have to infer — clamped
  confidences, coverage gaps, risk-gate drops, stale prices — plus one entry when a
  provider's daily budget is spent. A spent AlphaVantage key is one fact about one key,
  and it reached the 2026-09-01 run only as an identical `data_error` per ticker, so a key
  with nothing left read like a handful of unlucky names. The per-ticker detail stays in
  `data_errors`; `warnings` carries the single line that says they are all the same fact.

- `domains[].corrective`: what became of the one corrective re-prompt, when one was spent —
  `applied`, `unparseable` or `failed`. Empty means none was attempted.

  There used to be no branch at all for a second call that returned unparseable JSON: no
  log, no warning, and the row still read `status: done, attempts: 2`. On 2026-09-01 that
  spent a five-minute synthesis call for nothing while `ideas.json` announced "after one
  corrective re-prompt" beside a book that was in fact the uncorrected first pass. The
  book's note now says what actually happened, and an `unparseable` or `failed` corrective
  raises a warning.

Each `domains[]` row is a `model.DomainStatus`: `status`, `grounded`, `attempts`,
`duration_ms`, `tokens`, the three enforcement lists above (`corrected_scores`,
`off_shortlist_scores`, `self_contradicted_scores`) plus `neutral_scores`, which records
rather than deletes; `ungrounded` and its subset `abstained`, `fabricated_citations`, and
`scored_names` — the denominator the enforcement lists are only meaningful against. Six deletions out of six is a domain that
invented its entire output; six out of forty is one that overreached.

`domains[].usage` retains an entry for every attempted engine call, including
failed or truncated API responses. Optional prompt, completion and total token
counts distinguish missing telemetry from reported zero. Cache and reasoning
counts are subsets, not additional tokens. `tokens` is the compatibility sum of
reported completion tokens across attempts. Claude structured output records
whole-tree model usage when available; main-loop-only or interrupted telemetry
is marked `incomplete`. CLI wrappers returning plain text and engines without
telemetry retain unavailable counts.
See [evaluation diagnostics](thesis-research.md#explicit-pairing-and-evaluation-diagnostics)
for aggregation and completeness semantics.

## Go types

`internal/model/types.go` mirrors these:

```go
type TradeIdea struct {
    Rank       int    `json:"rank"`
    Ticker     string `json:"ticker"`
    Name       string `json:"name"`
    Index      string `json:"index"`
    Direction  string `json:"direction"`  // BUY | SELL
    Confidence int    `json:"confidence"` // 0-100
    Why        string `json:"why"`

    EntryType     string  `json:"entry_type,omitempty"` // market_on_open | "" (limit)
    Entry         float64 `json:"entry,omitempty"`
    Stop          float64 `json:"stop,omitempty"`
    Target        float64 `json:"target,omitempty"`
    RiskReward    float64 `json:"risk_reward,omitempty"`
    TimeframeDays int     `json:"timeframe_days,omitempty"`
    PositionNote  string  `json:"position_note,omitempty"`

    PriceAtGeneration float64        `json:"price_at_generation,omitempty"`
    BaseConfidence    int            `json:"base_confidence,omitempty"`
    DomainScores      map[string]int `json:"domain_scores,omitempty"`
    Consensus         float64        `json:"consensus,omitempty"` // 0-1

    Currency         string  `json:"currency,omitempty"`
    Shares           int     `json:"shares,omitempty"`
    Notional         float64 `json:"notional,omitempty"`  // USD
    RiskAmount       float64 `json:"risk_amount,omitempty"` // USD
    ExpectancyBps    float64 `json:"expectancy_bps,omitempty"`
    ExpectancyR      float64 `json:"expectancy_r,omitempty"`
    BreakevenWinRate float64 `json:"breakeven_win_rate,omitempty"`
}

type IdeasResult struct {
    Mode        string      `json:"mode"`
    GeneratedAt string      `json:"generated_at"`
    Ideas       []TradeIdea `json:"ideas"`
    Notes       string      `json:"notes"`
}
```

## Parsing rules

Go extracts the **last** fenced ```json block from the agent's stdout and unmarshals it.
Validation (warn, don't drop, except invalid directions): direction ∈ {BUY,SELL},
confidence ∈ [0,100] **and inside `base_confidence ± chief_adjust_band`** (clamped, and the
coverage cap binds over the band — see `scoring.md`), level ordering per direction, the
**asymmetric entry band** below, stop distance within 0.5–5 × σ_daily·√h, risk_reward
recomputed from levels.

The entry band is asymmetric because bidding for a better price and paying up for a worse
one are not the same trade. An entry on the **patient** side — a long *below* the last
close, a short *above* it — may reach `risk.entry_patience_sigma · σ_daily·√5` (default
1.5); an entry on the **chasing** side may reach only `risk.entry_chase_sigma · σ_daily·√5`
(default 0.5). The worst case on the patient side is a limit that never trades, which the
scoreboard records as `unfilled` rather than as a loss; the worst case on the chasing side
is a filled position at the top of the move.

One number could only ever be set to the chasing width, and that is why the 2026-09-04 run
bid 442 for AMGN against a 444.12 close — half a percent below the market on a name at
0.993 of its 52-week high, when what its own reasoning argued for was waiting. Under a
symmetric 0.5σ√5 there was no wider bid available to place. Hard level-ordering violations, every risk-gate violation, and a
confidence more than twice the band out trigger one corrective re-prompt of the chief.
Malformed output → degraded mechanical fallback from specialist scores (confidence ≤ 55),
never a crash.


Thesis reliability additions retain result schema v2. `thesis.target_method` distinguishes external comparisons from thesis scenarios.
`thesis.target_claim_ids`
references validated numerical comparison claims. Fresh research artifacts use
`contract_version: 2`; challenges carry `dossier_hash` and `claim_reviews`, while
historical repeated-claim challenges remain readable. Prompt profiles, failure
kinds and optional source/comparison provenance are additive metadata. See
[thesis research reliability](thesis-research.md#reliability-contracts-and-diagnostics).

### Prompt profile and capacity fields (additive, `model.PromptProfile`)

Every field below is additive: an absent field on a historical run means
*unrecorded*, never *measured zero* and never *failed*. A profile with no
`response_contract_version` predates the payload-based response contract and
is read under its original semantics — its `bytes` field was the raw response,
full stop.

- `response_contract_version`: `2` once the response is measured as a compact
  JSON payload rather than raw bytes (see `thesis-research.md`'s byte-budget
  table). Distinct from the profile's own `version` field, which is the
  profile schema's version and is unrelated.
- `response` (`model.ResponseMeasure`): `raw_bytes`, `payload_bytes`,
  `normalization` (the method string, `"json.Compact of the fenced payload"`),
  `raw_sha256`, `payload_sha256`, `normalized` (false when no fenced payload
  could be extracted at all, in which case `payload_bytes == raw_bytes`).
- `components_bytes`: a map of named prompt section → its exact redacted byte
  size, from the section-measured assembly (`assembleSections`). Sections are
  a flat, non-overlapping partition of the prompt; every byte is attributed to
  exactly one name, never folded into a neighbour.
- `omitted` on the **prompt profile** (`PromptProfile.Omitted`) names optional
  sections `assembleSections` actually dropped to make an assembled prompt
  fit. Profile version 2 applies the actual redacted input budget; required
  macro context and compaction originals cannot be omitted. For refused inputs,
  the persisted prompt retains the complete input for audit.

  `omitted` on **`DomainStatus`/`Report`** (a different field, same name, a
  different fact) names the *mandatory* requirements implicated when a call
  was refused on input capacity *before* it ever dispatched (`FailureKind:
  "input_capacity"`, `Attempts: 0`) — naming which requirement did not fit,
  rather than leaving the refusal component-less the way the September 15
  audit's capacity failures were. This is the field populated in practice;
  read it, not the prompt profile's, for why a real call was refused.
- `compaction.allowance` (`model.CompactionAllowance`, on the compacting
  domain's `DomainStatus`): `payload_bytes`, `limit`, `protected_bytes`,
  `original_excess`, `narrative_budget`, `per_field` (bytes, not characters),
  `feasible`. `feasible: false` means protected content alone already
  exceeded the response budget and the compaction call was never dispatched —
  a measured, per-company verdict, not a guarantee that every oversized
  dossier recovers.

**Which artifact carries which snapshot.** `input-<name>.json` is written by
`preparePrompt` *before* the call is dispatched, so it can never carry a
response measurement — only the request side (`components_bytes`, and the
prompt profile's own `omitted` for optional sections dropped before dispatch).
`metadata.json`, via each `DomainStatus.Prompt`, carries the same profile
*after* `responseCapacity` has filled in `response` and
`response_contract_version`. Both are correct; they are snapshots at
different points in the same call. Read `metadata.json` for response
measurement, not the `input-*.json` capture.

### Outcome and error-attribution fields (additive)

- `model.OutcomeNotAttempted` (`"not_attempted"`): a call refused before
  dispatch — no subprocess ran, no HTTP request was sent — distinct from
  `model.OutcomeNotRun` (`"not_run"`, research never intended for this
  company, e.g. an early-earnings watchlist skip). The two must never be
  aliased: a capacity refusal and a policy deferral are different facts, and
  collapsing them changes failure/deferred counts.
- `DomainStatus.Omitted` / `Report.Omitted` (`[]string`, JSON `omitted`): the
  mandatory-requirement field described above, attached directly to the
  domain/report record so a reader does not have to cross-reference the
  prompt artifact to see why a call was refused.
- A company's own `Errors` list no longer duplicates a call-outcome failure
  already attributed to a `DomainStatus`; a run's `data_errors` carries each
  such failure once, ticker-tagged, from the domain-attributed pass. An error
  with no `DomainStatus` counterpart (a document-fetch or artifact-persist
  failure) still reaches the aggregate.

### Chief provenance fields (additive, `model.RunMeta`)

- `chief_engine`: the configured primary (`claude` | `api`).
- `chief_model`: the model actually addressed for the accepted response —
  never assumed from the configured engine, since a fallback or a corrective
  retry can address a different model than the primary attempt.
- `chief_attempted`: every engine actually called, in attempt order
  (comma-separated), including a primary that failed before a fallback fired.
- `chief_accepted`: the engine whose output was used for the final result.

`synthesis_model` keeps its pre-existing name and place in `RunMeta`, but not
its pre-existing value: before Task 6 it was unconditionally
`cfg.Models[model.CLIClaude]`, which reported a Claude model name for a run
`chief_engine="api"` or the fallback actually answered. It now carries
`chiefModel` — the model of whichever engine's output actually shipped, the
fallback's if it rescued the run — the same corrected value `chief_model`
above carries. It is no longer the *sole* source of provenance once the Chief
can run on a non-Claude engine — reading it alone still cannot distinguish the
configured primary from what was attempted or accepted, which is what
`chief_engine`/`chief_attempted`/`chief_accepted` are for. An absent `chief_*`
field on a historical run means the run predates engine-selection provenance,
not that the Chief ran on Claude.

`ideas.json` and `cfr run --json`'s stdout encode `IdeasResult`, never
`RunMeta`, so they carry only a compact two-field subset — `chief_engine` and
`chief_accepted` — not the full quartet. `chief_engine` present with
`chief_accepted` empty is a known fact (a Chief ran, or was deliberately
skipped on thesis's all-research-failed path, and nothing was accepted); both
absent means the run predates these fields. Read `metadata.json`'s `RunMeta`
for `chief_model` and the full `chief_attempted` trail.


### Source diagnostics and prompt version 2

Optional `source_diagnostics` on run metadata and company research artifacts is an
array of `{id, provider, ticker?, region?, stage, reason, disposition, message}`.
IDs identify repeated observations; messages are redacted. Disposition values
currently emitted are `failed`, `withheld`, `expected`, and `context`. Reason codes
include `fetch_failed`, `unresolved_symbol`, `stale`, `filtered_items`,
`filtered_unrelated`, `below_threshold`, `not_applicable`, `navigation_only`, and
`provider_warning`. Omitted historical arrays mean unrecorded classification.
Legacy `data_errors` is retained. These additive fields do not change schema v2.

Prompt profile `version: 2` measures and fits redacted wrapped input, dropping
only optional whole sections. Required compaction originals and macro context
cannot be dropped. Preparation failures with zero attempts have payload
`not_attempted`; no response decoding failure is implied. `response_contract_version`
remains independently versioned at 2 for compact JSON response measurement.

Research-comparison rows additionally expose `source_reasons`,
`source_reasons_by_region`, and `stage_progress` (the existing Go-computed
`ResearchSummary`). Reasons count unique IDs. The older `completed_research`
metric retains its reviewed-workflow meaning for compatibility.
