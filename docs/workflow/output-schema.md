# Output Schemas

The machine-readable contracts between agents and Go. Agents emit a fenced ```json block;
Go parses it. Keep these in sync with `internal/model/types.go`.

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
from the universe, while `bias` and `reason` are the scout's and travel on into every
specialist and Chief Analyst prompt. Two more fields record what the merge found:
`nominations` (how many scouts wanted this name in this direction, omitted when 1) and
`contested` (the indices that nominated it the *other* way). Both feed the merit sort.

## Pre-screen (`runs/<ts>/prescreen.json`)

Written by Stage 0.5 before any model call. One row per constituent of the selected
indices, ranked best-composite-first with excluded rows last:

````
```json
{
  "as_of": "2026-08-28",
  "indices": ["sp500", "nq100"],
  "params": {
    "top_per_index": 15, "bottom_per_index": 5,
    "adv_min_usd": 20000000, "min_bars": 60, "vol_trend_flag": 1.5,
    "formula": "0.5·z(mom12-1) + z(ret63d) − 0.5·strZ when the recent move runs with the trend; mom/ret63d z-scored within index, strZ already a per-name z-score"
  },
  "rows": [
    { "ticker": "NVDA", "name": "NVIDIA Corporation", "sector": "Information Technology",
      "index": "sp500", "as_of": "2026-08-28", "bars": 501, "close": 176.42,
      "mom_12_1": 0.482, "ret_63d": 0.191, "ret_21d": 0.064, "ret_5d": 0.012,
      "rs_63": 0.114, "str_z": 0.4, "vol_yz_20": 0.38, "vol_trend": 1.12,
      "regime": "trending", "adv_usd": 3.1e10, "adv_local": 3.1e10, "currency": "USD",
      "price_to_52w_high": 0.94,
      "score": 2.31 }
  ],
  "errors": ["005930.KS: yahoo 005930.KS: empty chart result"]
}
```
````

An excluded row carries `"excluded"` with the reason (`illiquid …`, `insufficient history
…`, `no price history`) and `"score": 0`; it is never ranked and never contributes to the
within-index mean or standard deviation. `params.formula` is recorded so a row's `score`
is legible without reading the source.

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
- `entry`/`stop`/`target`: prices in the ticker's local currency, consistent with the
  verified last close. Ordering — BUY: stop < entry < target; SELL: target < entry < stop.
  Stops should sit 1–2 × σ_daily·√h from entry (h = `timeframe_days`), per the quant pack.
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
  shortlist slots.

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
coverage cap binds over the band — see `scoring.md`), level ordering per direction, entry
within ±5% of the verified last close, stop distance within 0.5–5 × σ_daily·√h, risk_reward
recomputed from levels. Hard level-ordering violations, every risk-gate violation, and a
confidence more than twice the band out trigger one corrective re-prompt of the chief.
Malformed output → degraded mechanical fallback from specialist scores (confidence ≤ 55),
never a crash.
