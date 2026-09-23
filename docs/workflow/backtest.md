# Backtest lab

`cfr backtest [--years N] [--indices a,b] [--cache-age D] [--json]` replays the
Stage 0.5 pre-screen week by week over every universe constituent and scores it
against what the market did next. Implemented in `internal/backtest`; the report
is written to `.data/backtest/<timestamp>.json` and summarised on stdout.

It exists because the live record cannot decide anything soon: a 50bp edge over
two weeks needs about 550 independent calls, and a run ships five. One replay
gives about 55,000 (name, week) observations. Decisions on trade mechanics and on
screen signals come from here. Decisions on model stages come from live shadow
arms, because a model cannot be replayed without look-ahead.

## Method

- **Data.** Each constituent's and each benchmark's daily bars come from Yahoo's
  keyless chart endpoint over the shortest range that covers the replay plus one
  year of warm-up (`5y` for the default 4 years). Reads go through the shared
  data cache and are cache-first: a series younger than `--cache-age` (default 7
  days) is not re-requested. The lab makes no model call and reads no
  credential.
- **Rebalance dates.** Every Friday from `--years` before the most recent Friday
  up to that Friday.
- **Point in time.** At each date *d*, each series is cut at its last bar on or
  before *d*. `quant.Compute` runs on the cut series, and its benchmark is cut at
  the same bar. `orchestrator.NewPrescreenRow`, `ApplyPrescreenExclusions` and
  `ScorePrescreen` then score that date's cross-section. This is the shipping
  code, not a copy of it. The look-ahead test checks that every signal at *d* is
  identical whether the bars after *d* are removed, kept, or scrambled.
- **Eligibility.** A name is scored on a date only when it has the 253 bars the
  12-1 term needs and printed a bar within 5 days of *d*. A date is dropped when
  any index has fewer than 70% of the most names it ever has scorable, which
  removes the warm-up weeks.
- **Known departures from a live run.** There is no liquidity floor, because a
  USD turnover needs each past date's FX rate. There is no drift archetype,
  because no point-in-time filing dates are cached. The composite does not use
  drift either way. Prices are Yahoo's for every symbol.
- **Targets.** For horizons of 5, 10 and 15 sessions: the close-to-close return
  less the benchmark's return over the same dates (`universe.BenchmarkFor`), and
  the beta-adjusted version `r − β·r_bench`, with β taken from
  `quant.Compute` at *d*.
- **Signals.** The composite (`score`) and `trend`, their inputs, and the
  standard free price signals: `mom12_1`, `ret63`, `strz`, `stretch21`, `rev5`,
  `rev21`, `hi52`, `lowvol`, `indmom`, `idio_rev5`, `overnight21` and
  `intraday21`. The pre-registered variants `c1_mom_weighted` and `c3_news_rev`
  are added too. Every signal is oriented so that a higher value is bullish.
- **Statistics.**
  - Rank IC is the Spearman correlation within one index on one date, with at
    least 15 pairs. It is averaged across indices per date, giving one series
    per signal.
  - IC is the mean of that series. ICIR is the mean over its standard
    deviation.
  - The t-statistic is Newey-West with `h/5 + 1` lags, because the forward
    windows overlap.
  - Each is reported for all indices, per region (US = sp500 + nq100, EU, Asia)
    and per half. The second half starts at the median date.
- **Quintile spread.** The mean excess of the top fifth minus the bottom fifth,
  at 10 and 15 sessions. It is shown gross and net of 30bp round trip on each
  leg. The top fifth alone is shown net of 30bp.
- **Barrier study.**
  - The trades are the composite's five largest |score| per index per week, in
    the direction of the score's sign.
  - Entry is at the next session's open, and costs are 30bp round trip.
  - The holding period is 15 sessions, under three exit rules: hold to the time
    exit, a stop at 2σ·√15 only, and the live 9% stop with a 15% target.
  - A gap through a barrier exits at the open. A bar that touches both barriers
    counts as the stop.
- **Survivorship.** The universe is today's constituents only. Names that left
  the indices during the replay are missing from every past date. That flatters
  momentum and long-side returns, so every report prints this caveat, and every
  positive number is an upper bound.

## Parity with the Python study

The spec is `docs/research/2026-09-23-evidence/backtest/`. The plan's parity bar
is a composite IC10 of 0.012 ± 0.005, and a no-barrier 15-session hold of
+0.71% ± 0.1% net per trade. Results are recorded below.

## Pre-registered signal tests (registered 2026-09-23, before any test was run)

This list, each test's statistic and the adoption bar are fixed here and
committed *before* the lab first runs on real data. Each test runs once. If a
test is changed after it has been seen, it counts as a new test.

**Adoption bar**, applied to every test that runs:

1. The Newey-West t of the per-date statistic at the **10-session horizon**
   (3 lags) exceeds **+2.5**. The pre-registered direction is positive for every
   test.
2. The statistic's mean is positive in **both halves** of the sample.
3. The statistic's mean is positive in **every region**: US, EU and Asia.

A test is adopted only if it passes all three. The report states how many tests
were run. Anything adopted ships behind `prescreen_version = 2`, with v1 kept as
the default and as the control arm. Null results are recorded here like any other
result.

| Test | Hypothesis | Statistic |
|---|---|---|
| **C1**: momentum weighting | ret63's IC is about 0. Raising 12-1 momentum's weight relative to it improves the composite. Variant: `trend = 1.0·z(mom12-1) + 0.5·z(ret63d)`, with penalties and clamps unchanged. This keeps the total weight at 1.5 and is computed by the shipping `ScorePrescreen` with its two inputs swapped. | Per-date IC10(variant) − IC10(shipped composite) |
| **C2**: earnings-announcement premium | Names with a verified earnings announcement inside the holding window earn a premium, so tilt long into them. | Per-date IC10 of an "announcement inside the next 10 sessions" indicator. **Runs only if** a point-in-time historical announcement-date source exists among the existing providers or cache. Otherwise it is recorded as untestable, not approximated. |
| **C3**: news-conditioned residual reversal | Residual moves without news revert, and moves with news continue. The residual 5-day move is `log(c/c₋₅) − β·log(b/b₋₅)`. The news proxy is abnormal volume: one of the last 5 sessions traded ≥ 2× the mean volume of the 20 sessions before them. Signal: `+resid` with news, `−resid` without. | Per-date IC10 of the C3 signal |
| **C4**: beta-adjusted targets | The composite's edge is selection rather than bull-market beta, so it survives when the target is `r − β·r_bench` instead of `r − r_bench`. This measures, and does not change, the screen. Every signal's IC is also reported against this target. | Per-date IC10 of the shipped composite against the beta-adjusted target |
| **C5**: breadth | The composite's t-stat holds on a wider liquid US universe. | **Runs only if** that universe exists without a new list of several hundred names from paid sources. Otherwise it is skipped and noted. |

## Results

Not yet run.
