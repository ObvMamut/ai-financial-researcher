# Workflow: Independent Research

Goal: from a universe of 278 names (curated index samples, see `universe.md`), produce
**5 ranked swing-trade ideas**.

A two-stage funnel keeps cost bounded: cheap Gemini screening narrows the universe, then a
fixed set of specialist agents analyze only the shortlist, then Claude synthesizes.

## Stage 1 — Screening (Gemini Scouts)

- One **Scout** subprocess per index: SP500, NQ100, EU50, Asia100 (4 parallel).
- Each scout receives its index's constituent list (from `internal/universe`) and returns
  **~5–10 candidate setups** with a one-line reason and a rough bias (long/short).
- The orchestrator **merges and dedupes** the four shortlists (exact repeats and
  cross-listings like `ASML`/`ASML.AS` collapse, preferring the unsuffixed primary
  listing), then **caps the result at 12 names** round-robin across the source indices
  (`universe.CapBalanced`). The final shortlist is recorded in `runs/<ts>/shortlist.json`.

Persona: `agents/scout.md`. Output: see `output-schema.md` (shortlist schema).

## Stage 1.5 — Price data & quant metrics (in-process, no model)

For every shortlisted ticker the orchestrator fetches ~2 years of daily OHLCV from the
keyless Yahoo Finance chart API (`internal/marketdata/yahoo.go`, rate-limited + cached
daily) and computes the statistical pack in `internal/quant`: return ladder, 12-1
momentum, price-to-52-week-high, turnover-conditioned short-term-reversal z-score,
Yang-Zhang volatility (20d/60d), Lo-MacKinlay variance ratios + regime tag, drawdown/tail
shape, dollar volume, beta vs the index benchmark, and vol-scaled stop/target distances
(k·σ_daily·√h). Raw series land in `runs/<ts>/prices/<ticker>.json`, metrics in
`runs/<ts>/quant.json`. Fetch failures degrade that ticker to ungrounded — never abort.

## Stage 2 — Deep analysis (5 Gemini Specialists, parallel)

Each specialist produces **one report covering the entire shortlist** (5 subprocess calls
total — not per ticker):

| Specialist    | Persona                  | Focus                                             |
|---------------|--------------------------|---------------------------------------------------|
| News          | `agents/news.md`         | Catalysts, headlines, earnings dates              |
| Fundamentals  | `agents/fundamentals.md` | Valuation, growth, balance-sheet health           |
| Quant         | `agents/quant.md`        | Interprets the computed statistical pack (no TA)  |
| Sentiment     | `agents/sentiment.md`    | Positioning, analyst/social sentiment, options    |
| Macro         | `agents/macro.md`        | Regime, rates, sector/region tailwinds & risks    |

The quant specialist receives the full computed pack as ground truth; news and sentiment
get compact verified price lines so their narratives stay anchored. Each report scores
every shortlisted ticker on its domain (bias + strength) so the Chief Analyst can measure
confluence.

## Stage 3 — Synthesis (Claude Chief Analyst)

Persona: `agents/chief-analyst.md`. Reads all 5 specialist reports plus a compact verified
quant reference, applies the rubric in `scoring.md`, ranks the shortlist by cross-domain
confluence, and emits the **top 5** ideas — including entry/stop/target derived from the
vol-scaled distances — as a fenced ```json block (schema in `output-schema.md`). Go parses
it into `[]model.TradeIdea`, validates the mechanics, and the TUI renders the results.

## Flow summary

```
4 Scouts (parallel) → merged shortlist (deduped, capped at 12)
        │
Stage 1.5: Yahoo OHLCV → internal/quant metrics (no model call)
        │
5 Specialists (parallel, each covers shortlist; quant gets the computed pack)
        │
Chief Analyst (Claude) → top 5 ideas (direction, confidence, entry/stop/target, why)
```

## Quality / degradation

- If a scout fails, the run proceeds with the remaining indices.
- If a specialist fails, the Chief Analyst is told and lowers confidence for affected names.
- Minimum to produce results: at least 2 specialist reports and a non-empty shortlist.
