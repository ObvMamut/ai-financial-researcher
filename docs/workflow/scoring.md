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
weighted = Σ wᵈ · signᵈ · strengthᵈ / 10        (over the domains that scored it)
covered  = Σ wᵈ                                 (over the domains that scored it)
total    = Σ wᵈ                                 (over all five domains)
base     = weighted / total              ∈ [−1, 1]
```

`base` divides by the **total** weight, not the covered weight: a domain with no data for
a name casts an explicit neutral vote. So thin coverage lowers `base` directly, and a
quant-only name cannot arithmetically exceed 35.

This used to divide by `covered`, which inverted the ordering it was meant to protect.
Missing domains then had no effect on the magnitude at all: one loud domain renormalised
to 50–60 and landed exactly on its coverage cap, while five domains that partly disagreed
averaged down to 35. The 2026-09-01 run shipped three quant-only foreign listings at 40
above the one name all five domains had read at 35, and the caps below were functioning as
a floor-boost for thin evidence rather than as a ceiling on it.

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

Since `base` now divides by the total weight, these caps rarely bind — a quant-only name
tops out at 35 on the arithmetic alone. They are kept as a redundant floor: they cost
nothing and they keep holding if the weights are reconfigured.

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

## The track record fed back to the Chief

Each idea records `base_confidence` and `domain_scores`, and the replayed scoreboard scores
those against what the trades actually did. `internal/scoreboard/calibration.go` reduces
that to `.data/calibration.json`: overall win rate, average R, average hold, and per-domain
/ per-confidence-bucket / per-direction records over **closed trades only**.

- Written by `cfr scoreboard` (both the CLI and the TUI screen), and refreshed by a run
  itself when the stored copy is older than 24h. A run never blocks on it: the refresh is
  bounded and best-effort, and with no record the Chief is simply not told one.
- Injected into the chief prompt as a `### Track record (computed from N closed ideas)`
  block at `n_closed ≥ 10` (`MinClosedForFeedback`) — below that the sample cannot
  distinguish a 45% domain from a 55% one, and a Chief told otherwise would spend its band
  on noise.
- Copied into `runs/<ts>/calibration.json`, so a past run's decisions can be read against
  the record that was in front of it.
- The persona's use of it is bounded by the same ±band: a domain whose backing wins near
  half the time carries no information and earns no adjustment; a bucket whose realized win
  rate is far from the confidence it stated is a bucket to move away from; every cell
  carries its own `n` so a 100% built on two trades cannot read as one built on twenty.

## Ranking & selection

1. Compute direction + confidence for every shortlisted ticker.
2. Rank by adjusted confidence descending.
3. Take the **top 5** (independent research) or **top 1** (single stock).

**Tie-breaks** (in order): better risk/reward → stronger quant confluence → stronger dated
catalyst → less crowded positioning.

**Diversification guard:** avoid 5 ideas that are effectively the same bet. It is enforced
by the risk gate below, on correlation and sector count rather than on sector count alone.

## The risk gate

After validation, `applyRiskGate` (`internal/orchestrator/riskgate.go`) applies the
deterministic risk policy. Every limit here was a sentence in `agents/chief-analyst.md`
before it was a number, and the 2026-08 runs answered those sentences at the cheapest edge
of every band they allowed: a cluster of 1.0σ stops against "usually 1–2σ", and
reward:risk ratios between 1.52 and 1.61 against "≥ 1.5 preferred".

Let `σ = σ_daily · √h · close` for the idea's own `timeframe_days`.

**Per idea — hard.** A violation earns one corrective re-prompt; an idea still violating
after it is **dropped with the reason recorded**. Returning four ideas is the intended
outcome, not a shortfall.

| Check | Limit | Config |
|---|---|---|
| Stop distance | `1.0σ … 2.0σ` | `stop_sigma_min`, `stop_sigma_max` |
| Target distance | `≤ 3.5σ` | `target_sigma_max` |
| Reward:risk | `≥ 1.8` | `rr_min` |
| Liquidity | `AvgDollarVol20USD ≥ $20M` (FX-converted) | `adv_min_usd` |
| Expectancy | `> 0` | `cost_bps`, `edge_sigma_daily` |

**Expectancy** is a seeded Monte-Carlo first-passage simulation (5,000 lognormal paths,
seed derived from the idea, so the same idea always scores the same number). Two details
decide whether the number means anything:

- The step is `exp(μ − σ²/2 + σz)`, not `exp(μ + σz)`. Without the Itô term the *expected
  price* grows at `μ + σ²/2`, which pays every idea free return in proportion to its
  volatility — exactly backwards.
- A breached stop books at the price that breached it, not at the stop level. A daily step
  from 92 to 85 through a stop at 90 loses 15%, not 10%. A target books exactly, because a
  limit order at that price fills at that price or better.

Together those two were worth about +40bps to every idea scored — more than the entire
edge prior. `edge_sigma_daily` is that prior, in units of σ_daily per day, and it is small
on purpose: at 0.05 every geometry the bands permit measures between +97 and +149bps and
the check never fires; at 0.02 the same geometries spread from +10 to +64bps.

**Once the pipeline has a record, the prior is replaced by it.** At `n_closed ≥ 30` closed
ideas (`scoreboard.MinClosedForEdge`) the simulation runs on the *measured* average R per
closed trade instead. The conversion is a derivation, not another assumption: the
simulation accumulates `edge·σ·days` of return over the holding period, and the record says
a trade of this kind returns `avgR` multiples of its own risk, which for this idea is
`avgR·|entry − stop|/entry`. Setting the two equal gives

```
edge_sigma_daily = avgR × (|entry − stop| / entry) / (σ_daily × timeframe_days)
```

clamped to ±0.05 in both directions — beyond that the check stops discriminating between
geometries, and no finite sample should be allowed to switch it off or reject everything on
arithmetic alone. A **negative** measured record produces a negative edge and the gate stops
shipping, which is the correct response to a system that is losing money.

Each idea records `expectancy_bps` and `breakeven_win_rate` (`risk / (risk + reward)` — the
hit rate the geometry alone demands).

**Per idea — penalties**, applied in Go and logged:

- A verified earnings date inside the holding window that neither `position_note` nor `why`
  mentions: **−10**.
- A date in the prose that appears in none of the run's verified facts: **−10**. This warns
  rather than rejects — a date can be legitimately derived — but a dated claim is the most
  persuasive thing a report carries and the easiest to invent.

**Position sizing** is computed, never authored: `risk$ = account_equity ×
risk_per_trade_pct / 100`, `shares = floor(risk$ / |entry − stop|)`, with any one position
capped at 25% of equity. `shares`, `notional` and `risk_amount` go into `ideas.json`.
"Half size" is not a position.

**Book level — re-prompt, never a drop.** Dropping a sound idea because of its neighbour is
not a risk control.

- Two same-direction ideas whose daily returns correlate above `max_pair_corr` (0.75).
- More than 2 ideas sharing a sector.
- Average absolute beta, or net signed beta, beyond `max_portfolio_beta` (1.5).
- All ideas one direction: **logged only**. Deliberately not a short quota — in the runs
  this system has produced, the token short was reliably the worst idea in the book.

## Degraded path

When the Chief Analyst (the `claude` CLI) fails or emits unparseable JSON, the orchestrator
first attempts `attemptChiefFallback` — an optional, off-by-default DeepSeek resilience call
(`chief_fallback`, gated on its own dedicated API key) that reuses the same
`agents/chief-analyst.md` prompt. Unlike the mechanical path below, a successful fallback
runs through the same `parseIdeas → validateIdeas → applyRiskGate → dropViolating` pipeline a
successful primary call goes through, so it keeps real entry/stop/target and a risk-checked
construction rather than only a base score. `ideas.Notes` records that the primary call failed
and synthesis completed via the fallback engine; `metadata.json` carries a
`"chief-analyst-fallback"` domain entry and `synthesis_fallback_engine`.

Only if the fallback is unconfigured, or it also fails or fails to parse, does the run fall
through to `buildDegradedIdeas`, which ships the base scores directly, capped at **55** — the
same arithmetic, with no cross-domain reasoning applied, so it must never read as confidently
as a real synthesis. It is the same function; the fallback ranking and the numbers the Chief
was shown cannot drift apart.

## Honesty rules

- Never fabricate a score to fill the list. If fewer than 5 names clear a reasonable bar,
  return fewer and say so.
- Missing and conflicting data are already priced into `base` and `cap`. Discounting for
  them again in the adjustment is double-counting.
