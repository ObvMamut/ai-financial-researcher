# Scoring & Ranking

How five domain reports become a **confidence score (0–100)** and a **ranked** list of
ideas.

The weighting is done **in Go**, before the Chief Analyst sees anything
(`internal/orchestrator/basescore.go`). The Chief adjusts the result within a fixed band
and is clamped to it. This document is the specification for that arithmetic; the persona
`agents/chief-analyst.md` states the same rules to the model.

## Per-domain signal

Each specialist scores every shortlisted ticker in its domain's structured JSON tail:

- **bias**: `bullish` | `bearish` | `neutral`
- **strength**: `0–10` (conviction within that domain)

A ticker appears in `scores` **or** in `missing`, never both and never neither. The
orchestrator enforces this before the Chief reads the report: a score for a ticker the
domain had no verified data for is deleted and the ticker moved into `missing`
(`internal/orchestrator/enforce.go`). So "covered" below means *the run supplied that
domain with data for that name*, not *the model claimed to know it*.

## Weights

| Domain        | Weight |
|---------------|--------|
| Quant         | 0.35   |
| News/catalyst | 0.25   |
| Fundamentals  | 0.15   |
| Sentiment     | 0.15   |
| Macro         | 0.10   |

These are the `orchestrator.Config` defaults, overridable in `[weights]` in `cfr.toml`, and
the authoritative set is injected into the Chief Analyst prompt.

They are mapped to the **5–20 trading-day horizon this system actually trades**.
Fundamentals previously led at 0.30, which was borrowed from long-horizon equity research:
a rich or cheap multiple says little about the next three weeks, and it is also the
worst-covered domain (US filers only, quarterly, frequently stale). Quant leads because it
is the only domain covered for *every* name, computed rather than recalled, and measured
over exactly this horizon. News is second because a dated catalyst inside the window is the
one thing that reliably moves a swing trade.

(The Quant domain replaced the former "Technicals" specialist: computed statistical metrics
— momentum, price-to-52-week-high, reversal z-score, Yang-Zhang volatility, variance-ratio
regime — interpreted by the model, no classic chart TA.)

## Base score (computed)

For each shortlisted ticker, over the domains **d** that scored it:

```
signᵈ    = +1 bullish, −1 bearish, 0 neutral
weighted = Σ wᵈ · signᵈ · strengthᵈ / 10
covered  = Σ wᵈ
base     = weighted / covered            ∈ [−1, 1]
```

`base` is renormalised over the domains actually present, so a name three domains covered
is not penalised twice for the two that were absent — coverage is handled by the caps
below instead.

- **direction** = the sign of `base` (positive → BUY, negative → SELL). Exactly zero, or
  no coverage at all, means no direction and no idea.
- **confidence** = `round(|base| · 100)`, then capped.

### Coverage caps

Let `coverage = covered / Σ all weights`.

| Coverage      | Max confidence |
|---------------|----------------|
| < 0.4         | 40             |
| 0.4 … < 0.6   | 55             |
| ≥ 0.6         | 100            |

A name only the quant domain could reach (0.35 of the weight) caps at 40 however emphatic
that domain is. This replaced a prose rule in the persona that counted *reports* rather
than weight, so losing macro (0.10) was penalised exactly as hard as losing quant (0.35).

## The Chief's adjustment band

The Chief Analyst starts from `base` and may move it by at most **±`chief_adjust_band`**
(default 10), naming each adjustment. The permitted reasons are enumerated in
`agents/chief-analyst.md` under *Adjustments*; each idea's reasoning must carry a
**Confluence Math** line of the form `base <N> <±adj: reason> … = <confidence>`.

Enforcement (`anchorConfidence` in `internal/orchestrator/validate.go`):

- The band is anchored to the base **for the direction the idea proposes**. A BUY when the
  domains read bearish starts from 0, so it can carry at most `band` confidence.
- Confidence outside `[base − band, base + band]` is **clamped**, and the clamp is recorded
  in the run's warnings.
- The coverage cap binds over the band: `55 + 10` is still 55.
- Deviation greater than `2 × band` triggers **one corrective re-prompt** before the clamp
  stands — that far out is not a judgment the arithmetic missed, it means the model scored
  a different thesis than its own domains reported.
- A ticker with no computed base is warned about and left alone rather than zeroed.

Each idea records `base_confidence` and `domain_scores` in `ideas.json`, so the scoreboard
can later ask which *domains* were right, not only whether the trade worked.

Calibration guide (what the final number should mean):

| Confidence | Meaning                                                          |
|------------|------------------------------------------------------------------|
| 80–100     | Strong multi-domain confluence, clean setup, catalyst supportive |
| 60–79      | Good alignment, minor conflicts or one weak/missing domain       |
| 40–59      | Mixed; tradeable but speculative                                 |
| < 40       | Don't surface as a top idea                                      |

## Ranking & selection

1. Compute direction + confidence for every shortlisted ticker.
2. Rank by adjusted confidence descending.
3. Take the **top 5** (independent research) or **top 1** (single stock).

**Tie-breaks** (in order): better risk/reward → stronger quant confluence → stronger dated
catalyst → less crowded positioning.

**Diversification guard:** avoid 5 ideas that are effectively the same bet (same
sector/region all one direction). More than 3 ideas in one sector triggers a corrective
re-prompt.

## Degraded path

When the Chief Analyst fails or emits unparseable JSON, `buildDegradedIdeas` ships the base
scores directly, capped at **55** — the same arithmetic, with no cross-domain reasoning
applied, so it must never read as confidently as a real synthesis. It is the same function;
the fallback ranking and the numbers the Chief was shown cannot drift apart.

## Honesty rules

- Never fabricate a score to fill the list. If fewer than 5 names clear a reasonable bar,
  return fewer and say so.
- Missing and conflicting data are already priced into `base` and `cap`. Discounting for
  them again in the adjustment is double-counting.
