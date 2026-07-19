# Scoring & Ranking

How the Chief Analyst turns five domain reports into a **confidence score (0–100)** and a
**ranked** list of ideas. This rubric lives in the prompt assembled for
`agents/chief-analyst.md`.

## Per-domain signal

Each specialist scores every shortlisted ticker on its domain as:

- **bias**: `bullish` | `bearish` | `neutral`
- **strength**: `0–10` (conviction within that domain)

## Confluence model

The trade **direction** is the dominant bias across domains. **Confidence** rewards
*agreement* across domains and *strength*, and penalizes *conflict* and *missing data*.

Default weights (the orchestrator injects the authoritative set into the Chief Analyst
prompt; these are the `orchestrator.Config` defaults):

| Domain        | Weight |
|---------------|--------|
| Fundamentals  | 0.30   |
| Quant         | 0.20   |
| News/catalyst | 0.20   |
| Macro         | 0.15   |
| Sentiment     | 0.15   |

(The Quant domain replaced the former "Technicals" specialist: computed statistical
metrics — momentum, price-to-52-week-high, reversal z-score, Yang-Zhang volatility,
variance-ratio regime — interpreted by the model, no classic chart TA.)

Confidence (0–100) is driven by:

1. **Alignment** — share of weighted domains agreeing with the chosen direction.
2. **Conviction** — weighted average domain strength on the agreeing side.
3. **Penalties** — subtract for strong opposing signals (a high-strength conflicting
   domain), for missing/failed specialist reports, and for a near-term unhedged catalyst
   risk (e.g. earnings inside the swing window) when it cuts against the thesis.

Calibration guide:

| Confidence | Meaning                                                            |
|------------|--------------------------------------------------------------------|
| 80–100     | Strong multi-domain confluence, clean setup, catalyst supportive   |
| 60–79      | Good alignment, minor conflicts or one weak/missing domain         |
| 40–59      | Mixed; tradeable but speculative                                   |
| < 40       | Don't surface as a top idea                                        |

## Ranking & selection

1. Compute direction + confidence for every shortlisted ticker.
2. Rank by confidence descending.
3. Take the **top 5** (independent research) or **top 1** (single stock).

**Tie-breaks** (in order): undervalued/relative-strength laggards → stronger quant
confluence → stronger near-term catalyst → better risk/reward → less crowded positioning.

**Diversification guard:** avoid 5 ideas that are effectively the same bet (same
sector/region all one direction). If the top 5 are over-concentrated, the analyst may swap
in the next-best idea that adds breadth, noting why.

## Honesty rules

- Never fabricate a score to fill the list. If fewer than 5 names clear a reasonable bar,
  return fewer and say so in the rationale.
- Lower confidence explicitly when specialist reports are missing or contradictory.
