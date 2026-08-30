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
specialist and Chief Analyst prompt.

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
    "formula": "z(mom12-1) + 0.5·z(ret63d) + 0.5·z(rs63) − 0.5·z(strZ) when the recent move runs with the trend; z-scores within index"
  },
  "rows": [
    { "ticker": "NVDA", "name": "NVIDIA Corporation", "sector": "Information Technology",
      "index": "sp500", "as_of": "2026-08-28", "bars": 501, "close": 176.42,
      "mom_12_1": 0.482, "ret_63d": 0.191, "ret_21d": 0.064, "ret_5d": 0.012,
      "rs_63": 0.114, "str_z": 0.4, "vol_yz_20": 0.38, "vol_trend": 1.12,
      "regime": "trending", "adv_usd": 3.1e10, "price_to_52w_high": 0.94,
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
- a score for a ticker that was never on the shortlist is **deleted** and nothing
  is added to `missing` — the run never asked about it (recorded in
  `domains[].off_shortlist_scores`);
- a report with no parseable JSON tail is not a report. The domain is marked
  `failed`, whatever the process exit code said.

Coverage is computed per domain: EDGAR, AlphaVantage and the option chain reach US
listings only (or a foreign listing's US line), FRED's series are US macro, and Yahoo's
chart API — which feeds quant — is global.

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
      "shares": 47,
      "notional": 9964.0,
      "risk_amount": 493.5,
      "expectancy_bps": 34.2,
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
- `shares` / `notional` / `risk_amount`: the computed position — the account's risk budget
  divided by the idea's own stop distance, capped at 25% of equity.
- `expectancy_bps`: the simulated expected value in basis points of entry, net of costs.
- `breakeven_win_rate`: `risk / (risk + reward)`, the hit rate the geometry alone demands.

An idea that violates a hard risk limit (stop band, target ceiling, reward:risk floor,
liquidity, or negative expectancy) is **dropped** after one corrective re-prompt, with the
reason appended to `notes`. Fewer than 5 ideas is the intended outcome in that case. See
`scoring.md` for the limits.

All mechanics fields are optional (`omitempty`) so ideas.json from older runs still loads.

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

    Shares           int     `json:"shares,omitempty"`
    Notional         float64 `json:"notional,omitempty"`
    RiskAmount       float64 `json:"risk_amount,omitempty"`
    ExpectancyBps    float64 `json:"expectancy_bps,omitempty"`
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
