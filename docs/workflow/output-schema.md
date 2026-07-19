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

`bias`: `bullish` | `bearish` | `neutral`. The orchestrator merges all scouts, dedupes by
canonical ticker (including cross-listings like `ASML`/`ASML.AS`), and caps the combined
shortlist at 12 names balanced across indices.

## Specialist report (each Gemini specialist → Chief Analyst)

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

`domain`: `news` | `fundamentals` | `quant` | `sentiment` | `macro` (parsers also accept
the legacy `technicals` from old runs). `strength`: integer `0–10` per each persona's
anchored rubric. `missing`: tickers the specialist could not assess.

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
      "why": "Momentum confluence with upbeat product-cycle catalyst; macro and fundamentals supportive, sentiment not yet crowded."
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
confidence ∈ [0,100], level ordering per direction, entry within ±10% of the verified
last close, stop distance within 0.5–5 × σ_daily·√h, risk_reward recomputed from levels.
Hard ordering/diversification violations trigger one corrective re-prompt of the chief.
Malformed output → degraded mechanical fallback from specialist scores (confidence ≤ 55),
never a crash.
