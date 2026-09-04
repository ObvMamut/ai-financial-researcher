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

What counts as data is the domain's **own** evidence, not the context every domain is
handed (`coveredBy` in `internal/orchestrator/coverage.go`):

- **quant** — a row in the computed metrics pack.
- **news** — at least one headline. A bare `Next earnings` date does not cover the domain:
  the calendar is one bulk request and the headline feed is a per-ticker one, and they fail
  independently. On 2026-09-01 the free AlphaVantage key hit its 25-request daily budget
  mid-run, 2330.TW was recorded as grounded for news carrying only a date, and the gap
  therefore never reached `coverageGaps` — the run could not see its own starvation.
- **fundamentals** — at least one verified EDGAR fact.
- **sentiment** — a *directional* computed positioning verdict
  (`marketdata.HasPositioningSignal`), not merely a fetch that returned rows. A quiet name
  is an abstention, recorded separately so it does not degrade the run. See
  *Positioning legs* below for what the verdict is computed from.
- **macro** — a computed market regime for the benchmark this name is measured against
  (`quant.Metrics.Benchmark` present in `quant.Pack.Benchmarks`). Macro grounded on
  `IsUSListing && FRED facts` until 2026-09-02, which was the rule from when FRED was the
  whole of its evidence; the computed regime block had since been added to the macro prompt
  and the predicate never moved. The domain was handed evidence for twelve names, scored
  twelve, and had seven deleted for having none — past the confabulation threshold, which
  marked a run degraded for an enforcement error rather than an agent one.

## Positioning legs

The sentiment domain is the one place where the verdict is computed in Go and the agent is
bound by it (`internal/marketdata/insidersignal.go`). It has six legs, each with its own
classifier and its own abstention, combined into one `Positioning signal (computed)` fact
that names every leg and then either states `Directional evidence: …` or says nothing was
found. A name where nothing was found goes to `missing`.

| Leg | Source | Reads | Votes when |
| --- | --- | --- | --- |
| insider | SEC Form 4, 45d | open-market buys and sales by role | an officer buys ≥$100k, any two insiders buy, a 10% owner adds ≥$250k, one seller moves ≥33% of their holding, or three sellers act *and the window was not pre-declared* |
| options | Yahoo chain, front 2 expiries | put/call **open interest** | the ratio is outside 0.45–2.0 **and** 1.5× clear of the run's own median — contrarian, against the crowded side |
| flow | Yahoo chain, front 2 expiries | traded **volume** | one side leads by 2.5× in dollars, its heaviest strike trades ≥3× its own open interest, and that volume sits ≥1% away from spot on the side a directional bet would take — *with* the direction, not against it |
| planned sales | SEC Form 144, 45d | proposed sales, split scheduled vs not | ≥$5M of unscheduled notices, or two unscheduled filers. Bearish or silent only: there is no form an insider files to announce a purchase |
| activist stakes | SEC 13D/13G, 90d | who crossed 5% and why | a **new** 13D. A 13G is the index owning the index; a 13D/A may be an exit as easily as an add |
| institutional | SEC 13F, 23 tracked managers | quarter-over-quarter change | a manager opened, closed or moved ≥50% of a ≥$25M position, and the quarter is under 150 days old |

Three rules run through all six:

- **Abstention is first-class.** Each leg's default is silence, and a name whose every leg is
  quiet is an abstention (`abstainedFor`) rather than a coverage gap — the run is not
  degraded by evidence correctly declining to say anything.
- **Volume and open interest are different facts.** Open interest is a stock of positions
  with nobody left to add, which is why the crowding leg is contrarian; volume is the adding
  itself, which is why the flow leg is not. The two may disagree, and that is information.
- **Scheduled selling is not selling.** Officers are paid in stock and sell on plans adopted
  months earlier. The Form 144 leg refuses to read a plan as a view, and it also *suppresses*
  the Form 4 breadth test when the same window's notices are entirely scheduled and cover at
  least half the Form 4 sale value. That cross-reference was added after raising the Form 4
  document cap from 5 to 12 turned NKE's five routine, plan-declared disposals — $400k in
  total — into a bearish verdict. The deeper window had not found more information, it had
  found more of the payroll. Depth is not suppressed the same way: a plan that takes a third
  of somebody's holding is still a third of their holding gone.

Reachability is uneven and is not a failure. Listed options are a US instrument and Form 4
is a US filing, so a foreign primary listing reaches the sentiment domain with the 13D/G and
13F legs at most. A foreign private issuer gets no insider leg at all — "no Form 4 filings in
45 days" would be a statement about US filing law dressed as a statement about its insiders —
but it does get the other legs, which are not Section 16 evidence.

## Weights

| Domain        | Weight |
|---------------|--------|
| Quant         | 0.35   |
| News/catalyst | 0.30   |
| Fundamentals  | 0.18   |
| Sentiment     | 0.17   |
| Macro         | **0.00** |

These are the `orchestrator.Config` defaults, overridable in `[weights]` in `cfr.toml`, and
the authoritative set is injected into the Chief Analyst prompt.

### Why Macro does not vote

The domain still runs and its regime read still reaches the Chief. It is read as *context*
— the authority on what each market is doing — and it moves no number.

A regime is one fact about a market, shared by every name that trades in it, and a run asks
about a dozen names drawn from three or four markets. Scored per name, three facts became
twelve, and `Σ w·sign·strength` cannot tell those apart from twelve independent
confirmations. The persona's own strength cap was written against this ("if this domain
scores twelve names 7, it has said nothing that distinguishes any of them") and could only
ever bound the magnitude, never the shape.

The shape also pointed one way. The persona instructed the analyst to judge whether the
regime supported each name's **nominated direction** — a confirmation task, structurally
unable to return the opposite sign, asked about a direction chosen by a price screen and so
already running with the tape. Measured over the stored runs it never did return it: 12 of
12 names in each of the last three runs, with signed scores correlating 0.85–0.99 against
the quant domain's, which reads the same price history.

Two consequences made it worse than merely redundant:

- a regime read needs no filing and no option chain, so macro reached **every** name. It
  was therefore the vote that carried the *thinly covered* ones: on 2026-09-04 BAYN.DE and
  DSFIR.AS shipped at ranks 4 and 5 scored by quant and macro alone, and both reported
  `agree: 100%`;
- because `Consensus` is computed over the domains that scored a name, a domain that cannot
  disagree inflates it. The figure built to detect one signal wearing several hats was being
  fooled by exactly that.

A weight of zero removes all of it in one place: `computeBaseScores` skips a non-positive
weight before accumulating, so macro casts no vote, enters neither side of the `Consensus`
ratio, and adds nothing to covered weight — which also drops an unmapped foreign listing's
expected coverage to quant alone, where `max_thinly_covered` can see it.

### Blinding the price-derived domains

The **Quant and Macro** prompts are assembled without the scout's nominated direction or its
reason (`agents.blindToDirection`). Both read evidence derived from the same price history
the nomination is: Stage 0.5 computes a composite from trailing returns whose *sign is the
direction*, the scout nominates in it and quotes the composite's own figures, and the
shortlist line carried both into every specialist prompt.

The timing says it was the prompt and not a coincidence of style. Before the pre-screen
existed the quant domain agreed with the nomination 3/3, 1/2, 5/9 and 0/5 — noisy, which is
what an independent domain looks like. Every run after it is 11/12 or 12/12.

News, fundamentals and sentiment keep the line. Their evidence — headlines, filings,
positioning — is not derivable from the price series, so a stated thesis is something they
can genuinely contradict, and they do: fundamentals dissents on 40% of names and sentiment
on 29%.

The setup archetype survives blinding because it is a shape and not a side: "pullback"
describes a trend resting against itself and is carried by names in both directions.

**This is an experiment with a stated reading.** Blinding removes the anchoring, not the
shared input — quant still reads bars the composite was computed from. If its agreement
falls materially below 12/12, the anchoring was the mechanism. If it stays there, the
redundancy is the input, and the indicated fix is to let the composite into the base score
as its own named term rather than laundering it through a domain.

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
signᵈ     = +1 bullish, −1 bearish, 0 neutral
weighted  = Σ wᵈ · signᵈ · strengthᵈ / 10       (over the domains that scored it)
covered   = Σ wᵈ                                (over the domains that scored it)
total     = Σ wᵈ                                (over every domain with a positive weight)
signed    = weighted / total             ∈ [−1, 1]   — the raw figure, kept for audit
reference = Σ wᵈ · Rᵈ / 10                      (over every domain with a positive weight)
base      = weighted / reference         ∈ [−1, 1]   — what confidence is read from
```

Two separate normalisations, doing two different jobs.

**Divide by `total`, never by `covered`.** A domain with no data for a name casts an
explicit neutral vote, so thin coverage lowers the score directly. Dividing by `covered`
instead inverted the ordering this is meant to protect: missing domains then had no effect
on the magnitude at all, so one loud domain renormalised to 50–60 and landed exactly on its
coverage cap while five domains that partly disagreed averaged down to 35. The 2026-09-01
run shipped three quant-only foreign listings at 40 above the one name all five domains had
read at 35, and the caps were acting as a floor-boost for thin evidence rather than a
ceiling on it.

**Then score against `reference`, the strongest verdict the rubrics permit.** `Rᵈ` is the
top of the band each persona actually uses — **8** for quant, news, fundamentals and
sentiment, which all reserve 9–10 for "rare". Macro is not in either sum: a zero weight is
skipped by both, which is what makes the demotion above a single edit rather than a special
case threaded through the arithmetic. With the default weights `reference = 0.80`.

Without this the denominator was an unreachable 1.0 — every domain at strength 10 —
so the top quarter of the scale could not be occupied by construction. Across the four runs
from 2026-08-31, the first with this arithmetic live, the highest base *anywhere* was 45.5
and the shipped ideas ran 26–45, while every consumer of the number still treated 50 as
mediocre and 70 as good. Every idea in every run rendered as a red bar. MRK on 2026-09-01
was the strongest three-domain agreement this system can produce — quant +6, news +6,
fundamentals +6 — and scored 39.

`reference` sums over **all** the weighted domains and is therefore the same constant for
every name in a run. That matters: it makes the rescale a positive scalar multiply, so it
changes the scale and provably never the ordering, and it keeps `total`'s guarantee intact.
A per-name reference over the covered domains would reintroduce exactly the defect above.
It is also a fixed table rather than the run's own observed maxima, so confidence stays
comparable across runs — which is what the scoreboard's calibration needs it to be.

- **direction** = the sign of `base` (positive → BUY, negative → SELL). Exactly zero, or
  no coverage at all, means no direction and no idea.
- **confidence** = `round(|base| · 100)`, then capped.

### Consensus — the second axis

```
consensus = |Σ wᵈ · signᵈ · strengthᵈ| / Σ wᵈ · strengthᵈ     ∈ [0, 1]
```

One means every domain that spoke pointed the same way; zero means they cancelled exactly.

It reads no evidence `base` does not already read, and it **changes nothing** — not the
ranking, not the caps, not the Chief's band. It exists because `base` is one number doing
two jobs, *how much evidence* and *how much agreement*, and they are different facts. On
2026-09-01 O39.SI scored 32 on a single loud domain and MRK scored 31 on five that
disagreed; the number said they were equally good ideas, and no consumer of it — the Chief,
the confidence bar, the scoreboard's buckets — could tell which was which.

It is shown as `agree` in the Chief's base-score table (explicitly marked "already inside
`base`, do not adjust for it"), recorded as `consensus` on each idea, and printed under the
confidence bar in the TUI. The scoreboard buckets closed trades by it, which is how the
question *does thin-but-unanimous beat thick-but-split?* eventually gets an answer instead
of a guess.

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

The caps bind only at the extremes — a quant-only name at strength 10 reaches 45 on the
arithmetic and is cut to 40 — and a cap can only ever lower a thin name, so it cannot lift
one past a thick one. Between 0.6 coverage and full coverage the arithmetic does all the
work on its own.

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

Calibration guide (what the final number should mean, on the `reference` scale above):

| Confidence | Meaning                                                            |
|------------|--------------------------------------------------------------------|
| 70–100     | Near-unanimous multi-domain confluence at the top of every rubric   |
| 55–69      | Strong alignment; one domain dissenting or standing down            |
| 40–54      | Good on the heaviest domains, mixed elsewhere                       |
| 25–39      | Thin coverage, or genuine disagreement between domains              |
| < 25       | Don't surface as a top idea                                         |

These are the bands `internal/tui/results.go` colours the bar by and
`internal/scoreboard` buckets the track record by; all three move together, and the last
time they did not, four consecutive runs of perfectly good ideas rendered red.

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
- A past idea is bucketed by **re-scoring its recorded `domain_scores` on today's scale**,
  not by the number its own run printed. This codebase has scored confidence three ways —
  asserted by the model, weighted over total weight, and weighted against `reference` — and
  comparing those numbers directly would be comparing three different measurements. Ideas
  from before `domain_scores` were recorded cannot be re-scored at all; they go to a
  `legacy` bucket and are withheld from the block the Chief reads.

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
| Expectancy | `≥ 0.005R` | `min_expectancy_r`, `cost_bps`, `edge_sigma_daily` |

Every key in that column is **presence-detected**: what you write is what is used, zero
included, and only an omitted key takes its default. Zero is the identity for a floor —
`cost_bps = 0` prices the book frictionless, `rr_min = 0` and `adv_min_usd = 0` turn those
checks off — and it used to be unsayable, discarded once by the config loader and again by
`riskDefaults`. The four ceilings and the two sizing inputs (`stop_sigma_max`,
`target_sigma_max`, `max_pair_corr`, `max_portfolio_beta`, `account_equity`,
`risk_per_trade_pct`) are the exception and are refused at startup if set to zero: a ceiling
of zero disables nothing, it rejects every idea, so it is a typo rather than a policy.
`edge_sigma_daily`, `min_expectancy_r` and `min_expectancy_bps` accept negative values,
which is what a losing record and a deliberately weaker floor look like.

Ordering is checked too, but *after* defaults are filled (`validateRiskPolicy`,
`riskgate.go`), because the config loader runs before `riskDefaults` and so only ever sees
the side you wrote. `stop_sigma_min` above `stop_sigma_max`, or `target_sigma_max` below
`stop_sigma_min`, fails the run at startup — including the realistic case of writing one
side against a default you never saw (`stop_sigma_min = 2.5` against the 2.0 ceiling). An
unsatisfiable band loads clean and then drops every idea at the gate, which is reported as
an ordinary run of risk-gate findings and ships an empty book. A floor *equal* to its
ceiling is allowed: absurd is not the same as meaningless.

**Expectancy** is a seeded Monte-Carlo first-passage simulation (10,000 antithetic
lognormal path *pairs*, seed derived from the idea, so the same idea always scores the same
number). Three details decide whether the number means anything:

- The step is `exp(μ − σ²/2 + σz)`, not `exp(μ + σz)`. Without the Itô term the *expected
  price* grows at `μ + σ²/2`, which pays every idea free return in proportion to its
  volatility — exactly backwards.
- A breached stop books at the price that breached it, not at the stop level. A daily step
  from 92 to 85 through a stop at 90 loses 15%, not 10%. A target books exactly, because a
  limit order at that price fills at that price or better.

- **It is judged in R, not in basis points of entry.** Expectancy in bps is proportional to
  the stop distance, so a floor denominated in it grades the name's volatility rather than
  the construction. The check reduced to roughly `200·σ_daily(%)·h − cost`: on 2026-09-01
  five ideas with near-identical normalised geometry — stop ≈1.3σ, target ≈2.6σ, R:R ≈1.9,
  breakeven ≈34.5% — scored +28.7, +21.4, +8.2, +4.6 and −3.1bps in exact order of
  `σ_daily`, and a floor of 10bps dropped the three calmest, which are the calm trends the
  pre-screen exists to find. Worse, the only lever the re-prompt named — "move the target
  out or the stop in" — *lowers* the number, because a tighter stop is touched more often.
  In R, with costs scaled to liquidity, the same five run +0.032, +0.045, +0.039, +0.015
  and +0.009 (`TestTheBookTheExpectancyGateRefused` keeps them as a fixture).

Together the first two were worth about +40bps to every idea scored — more than the entire
edge prior. `edge_sigma_daily` is that prior, in units of σ_daily per day, and it is small
on purpose: at 0.05 every geometry the bands permit measures between +97 and +149bps and
the check never fires; at 0.02 the same geometries spread from +10 to +64bps.

`min_expectancy_r` (default **0.005**) is set just clear of the simulation's own standard
error — under a basis point, about 0.001R, with antithetic sampling — and no higher. The
*level* of the whole distribution comes from `edge_sigma_daily`, which is an assumption, so
only the ordering is earned; a floor placed inside an assumed distribution measures the
assumption. Setting the previous floor at 10bps because one book's observed spread ran
3.0–24.1 is exactly that mistake, and it cost the next run three of its five ideas.

`cost_bps` is the cost of trading a name at the liquidity floor, scaled down for more
liquid names (`costTiers`): ×0.85 above $50M ADV, ×0.6 above $200M, ×0.35 above $1B. A flat
30bps charged to a mega-cap is not a cost assumption, it is a penalty on the names whose
stops are tightest.

**Once the pipeline has a record, the simulated figure is blended with it.** At
`n_closed ≥ 30` closed ideas (`scoreboard.MinClosedForEdge`) the expectancy becomes

```
w = n_closed / (n_closed + 30)
expectancy_R = w · avgR_measured + (1 − w) · expectancy_R_simulated
```

The simulation supplies the per-idea discrimination the record cannot — the record is one
book-wide number — and the record supplies the level the simulation can only assume. At
`n_closed = 30` they count equally and the record's share grows from there. A **negative**
measured record pulls expectancy down and the gate stops shipping, which is the correct
response to a system that is losing money.

The record used to arrive as a *drift* instead — `avgR·(|entry−stop|/entry)/(σ_daily·h)`,
clamped to ±0.05 — and every term in it was measured, but the arithmetic had a cliff. For
any realistic `avgR` that expression lands far above the clamp: with the 2026-09-01 book and
`avgR` 0.49 it gave 0.157–0.206 on every idea. The 30th closed trade would have flipped the
check from rejecting most of a book to never firing at all, with no regime in between.

Each idea records `expectancy_r`, `expectancy_bps` and `breakeven_win_rate`
(`risk / (risk + reward)` — the hit rate the geometry alone demands). The bps figure is kept
because it is what an operator reads and what past runs recorded; the R figure is the one
the gate judges.

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
- Beta-adjusted gross exposure `Σ|beta × notional| / account_equity`, or the signed
  equivalent, beyond `max_portfolio_beta` (1.5). Measured against the account rather than
  averaged over the idea count, so adding a low-beta name can never satisfy the limit —
  the remedy the finding names is to drop or shrink the exposure.
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
