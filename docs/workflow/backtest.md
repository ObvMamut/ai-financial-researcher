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
  year of warm-up (`5y` for the default 4 years, `10y` up to 9 years). Beyond
  that — `--years 10` and up — `range=max` is not used: probed 2026-09-25, Yahoo
  answers `range=max&interval=1d` with **3-month bars** regardless of
  `interval` (`meta.dataGranularity="3mo"`, 169 bars back to 1984 for AAPL),
  where `range=10y` and an explicit window both still answer daily. Spans past
  10 years are instead requested with an explicit `period1`/`period2` window
  (unix seconds) and `interval=1d`, computed at the moment of the request so the
  cache key itself never carries an absolute date
  (`marketdata.YahooClient.chartSpan`); a response whose `meta.dataGranularity`
  is present and isn't `1d` is refused with an error rather than silently fed to
  the pipeline (`internal/marketdata/yahoo.go`). Before this, a `--years 10`
  replay silently shrank to 20 weekly rebalances spanning about 2.3 years,
  because almost no name ever reached the 253 daily bars the 12-1 term needs.
  Reads go through the shared data cache and are cache-first: a series younger
  than `--cache-age` (default 7 days) is not re-requested. The lab makes no
  model call and reads no credential.
- **Earnings-release dates (US only).** When `providers.contact_email` is set,
  each sp500/nq100 member's 8-K Item 2.02 filing dates are read from SEC's
  keyless per-issuer submissions JSON (`marketdata.NewFilingHistorySource`),
  from half a year before the first rebalance on, into `Data.Filings`. Names SEC
  cannot resolve are listed in the report's `filings_unavailable` and carry NaN
  earnings signals. Without a contact address nothing is fetched, both earnings
  signals are NaN for every name, and `filings_note` says so. eu50 and asia100
  have no source and are never asked for.
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
- **Shortfall warning.** The extra year fetched above is warm-up for the 253-bar
  requirement, not slack in the replay window: the first surviving rebalance
  should land within a few weeks of `end - years` (what `--years` asked for),
  the gap being holidays and the 70% eligibility threshold above, not a whole
  year. The report compares the two, and if the actual first date falls more
  than 8 weeks later — the sign of a genuine data problem such as the one above,
  not ordinary warm-up — the text report prints a `WARNING:` line and the JSON
  result carries `requested_start`, `shortfall` and `shortfall_note` alongside
  the actual `first_date`/`last_date`/`dates` it always records.
- **Known departures from a live run.** There is no liquidity floor, because a
  USD turnover needs each past date's FX rate. There is no drift archetype: the
  pre-screen row's `ReportDate` is left unset, so `classifySetups` and the
  composite are exactly what a live run without an SEC contact address
  computes, and the composite does not use drift either way. Drift is measured
  instead as its own signal, `drift`, and its event is the 8-K Item 2.02
  earnings release where a live run uses the 10-Q/10-K filing date — often the
  same day for large filers, sometimes weeks later, so the lab reads the
  release itself. Prices are Yahoo's for every symbol.
- **Targets.** For horizons of 5, 10 and 15 sessions: the close-to-close return
  less the benchmark's return over the same dates (`universe.BenchmarkFor`), and
  the beta-adjusted version `r − β·r_bench`, with β taken from
  `quant.Compute` at *d*.
- **Signals.** The composite (`score`) and `trend`, their inputs, and the
  standard free price signals: `mom12_1`, `ret63`, `strz`, `stretch21`, `rev5`,
  `rev21`, `hi52`, `lowvol`, `indmom`, `idio_rev5`, `overnight21` and
  `intraday21`. The pre-registered variants `c1_mom_weighted` and `c3_news_rev`
  are added too. Every signal is oriented so that a higher value is bullish.
  Two earnings signals are US-only and NaN elsewhere:
  - `drift`: the live drift leg's decayed reaction (`orchestrator.EarningsDrift`,
    the shipping `computeDrift` and `Score`) to the latest Item 2.02 release
    dated **strictly before** the rebalance session, on the series and
    benchmark cut at that session. SEC gives a filing date and no acceptance
    time, so a release dated on the session itself may have come after its
    close and is not used. The reaction window is the live one: from the close
    before the filing date to the close of the session after it. NaN when there
    is no such release, the window cannot be built, or more than 25 sessions
    have closed since it; 0 when the market has retraced the whole reaction.
  - `earn_window`: C2's indicator — 1 when the next release is predicted inside
    the next 10 sessions, 0 when not. SEC gives past dates only, so the next
    release is **extrapolated from the filer's cadence**: the latest release
    strictly before the session plus 91 days, ±7 days. Sessions are counted as
    weekdays, so a holiday stretches the horizon by a day. NaN with no past
    release, or once the whole predicted window has passed without one (a
    broken cadence predicts nothing). This is an approximation, not a
    calendar: a filer that moves its release by more than a week is misread.
  Both are summarised in the per-signal tables like every other signal; their
  pre-registered tests are separate.
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
- **US-scoped tests (`internal/backtest/scoped.go`).** `drift` and `earn_window`
  exist for US names only, so the every-region leg of the bar cannot be met by
  construction: there is no point-in-time EU or Asia release source. A test
  registered as *US-scoped* is evaluated on US names alone and, if it passes,
  may be adopted for US names only. Its bar is otherwise the lab's: the
  Newey-West t clears **2.5** in the registered direction, the mean has that
  sign in **both halves** (same median-date split), and the target is the
  beta-adjusted excess.
  - **The US scope is sp500 ∪ nq100, one row per ticker per date.** 35 of
    nq100's 56 names are also in sp500. The per-region rows of the signal tables
    average the two indices' per-date ICs, which reads a cross-listed name twice
    on one date. A scoped statistic instead pools the region into one
    cross-section per date. A name in both indices enters once, as its sp500 row,
    measured against ^GSPC. nq100-only names keep their ^NDX benchmark
    (`scopeRecords`, `scopedCells`).
  - The minimum cross-section stays at 15 pairs. It applies to the pooled US
    cross-section, of about 119 names, rather than to each index's. A date where
    fewer than 15 US names carry a finite signal has no IC. Each scoped result
    reports how many dates were usable, overall and per half (`n_dates`,
    `half_n_dates`), beside the scope's date count.
  - `earn_window` is 0/1, so its scoped statistic is the per-date rank IC10.
    Ties take average ranks. Its quintile spreads are not read.
  - **Signal book** (`signalBookTest`): each week, the five US names with the
    largest |signal|, each held at the signal's sign for 15 sessions and
    equal-weighted. A NaN or zero signal is not held. When fewer than five names
    are eligible, the book holds what there is, and the report gives the
    full-week count and the mean book size. The statistic is the weekly
    beta-adjusted excess, net of the lab's 30bp paid once per name. Its
    Newey-West t uses `nwLags(15)` lags, and it is tested against the same
    scoped bar.
  - The report's `us_scoped` block (text: "US-scoped looks") shows `drift`'s and
    `earn_window`'s scoped IC10 and the drift book against that bar. These
    entries have `status: "unregistered"` and are not counted in `tests_run`;
    a scoped test is counted only once it is registered below.
- **Per-calendar-year slices (E1).** Alongside the halves, the composite's IC10
  and IC15 (plain and beta-adjusted) are also reported for every calendar year
  the sample covers, each with its date count. A default 4-year run shows two
  halves; `--years 10` shows ten years, which is what lets E1 (below) ask
  whether an edge holds across regimes rather than only across one split.
  Survivorship (above) gets worse the further back a year sits, because its
  missing names are disproportionately past losers rather than a random sample
  — so whenever `--years` exceeds the default, the report adds a further
  caveat to weight this table, and the top-5 excess below, over the barrier
  study's raw long-only returns.
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
  - The same picks' directional 15-session benchmark-excess return ("top-5
    excess") is also reported per calendar year, plain and beta-adjusted, both
    gross and net of the 30bp each trade pays once, alongside the per-year IC
    above. A year the replay enters late or leaves early is starred as partial.
    E1 reads the net beta-adjusted column.
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

## Pre-registered lab tests (Wave B, registered 2026-09-25)

Tests below are pre-registered here, before they are run, the same way C1–C5
were. Each still runs once and adds to the tests-run count above; a test
changed after it has been seen counts as a new one.

| Test | Hypothesis | Statistic and decision |
|---|---|---|
| **E1**: long history | The edge is a property of the composite, not of the one regime the default 4-year window happens to sample. `cfr backtest --years 10` reaches back through 2018 Q4, 2020 and 2022 as well as the sample already covered. | Composite IC10/IC15 (plain and beta-adjusted) and the top-5 picks' 15-session excess (plain and beta-adjusted, gross and net), one figure per calendar year, from a `--years 10` run. **E1 does not use the adoption bar above**; it decides by calendar year. Decision, verbatim from the plan (§4): *"if the composite's beta-adjusted top-5 excess is not positive in a majority of years, the docs stop describing the screen as having an edge, and the TUI says so."* **Rule as applied (pinned 2026-09-26):** the figure is the beta-adjusted top-5 excess **net of the lab's 30bp** (`top5_excess_beta_adjusted_net_pct`), and "years" is **every calendar year in the replay**, partial years included, with the full-year count reported beside it. The report applies it itself (`decisions`, `e1Decision`). **Gated on `--years 10` (fixed 2026-09-27):** the rule was pre-registered at ten calendar years, so `e1Decision` only decides — `Status: "run"`, counted in `tests_run` — when the run's `--years` is 10 or more. Below that, the per-year table still computes and its `Note` still counts years the same way, but the report labels it `"comparison look, not a decision"` (`Status: "comparison"`, JSON `decisions[].status`), and it is excluded from `tests_run`; nothing fires. Before this, a default 4-year `cfr backtest` printed `E1 → not triggered` as if the majority-of-years rule had been evaluated on four years of one regime, which it was never registered to be. **This pinning came after the gross result had been read, and it changed the outcome.** The plan's wording did not say gross or net. Read gross, the figure is positive in 7 of 11 years (5 of 9 full years) and E1 passes. Read net, it is positive in 5 of 11 (3 of 9 full years) and E1 fires. The controller chose net, because every other top-trade figure in this lab is quoted net and an edge that does not survive the lab's own cost is not one. **Run 2026-09-26: fires.** The docs no longer describe the screen as having an edge, and the TUI results view says the lab finds none net of cost. See [Run of 2026-09-26](#run-of-2026-09-26-wave-b-e1e3-docsresearch2026-09-25-evidencebacktest). |
| **E3**: which side carries the result | The 2026-09-23 run's Top-5 row split the barrier study's picks into longs (+1.316%) and shorts (+0.111%) on *plain* excess only; that split does not say whether the short side is real selection or C4's market-beta exposure in reverse. | Same picks' 15-session directional excess (`internal/backtest/sides.go`'s `Result.Sides`), split long vs short, plain and beta-adjusted, for the whole sample and each half. **E3 does not use the adoption bar above** either — it is a diagnostic split of the barrier study's own picks, not a new signal test. Decision, registered before this runs on real data: *"if shorts are ≤0 beta-adjusted in both halves, pre-register 'long-only merit_veto' as a config test for the live shipped arm."* **Run 2026-09-26: not triggered.** Beta-adjusted shorts were −1.046% in H1 and +0.127% in H2 (10 years), and −0.246% / +0.681% over 4 years, so no long-only config test is registered. See [Run of 2026-09-26](#run-of-2026-09-26-wave-b-e1e3-docsresearch2026-09-25-evidencebacktest). |
| **E2**: sector-cap grid | One run's default `max_per_sector` (2) held MSFT and SAP.DE to the two IT slots and dropped ORCL on a 0.5% merit gap; one run cannot say whether that cap costs or saves the book. `internal/backtest/book.go` replays a live-shaped weekly book (mechanically standing in for the scout call and the Chief) under `SectorCaps = {1, 2, 3, off}` across every week the panel has. | Per-week difference in beta-adjusted book excess (candidate cap minus the live default's arm, paired by date), Newey-West t at `nwLags(15)`, plus its halves and regions (`BookGrid.PairedTests`). **E2 does not use the adoption bar above** either — like E3, it is a diagnostic replay of the live funnel's own construction, not a new signal test, so its own bar is two-sided: **`|t| > 2.5`, same sign in both halves and every region**, since a departure from the live cap could plausibly help or hurt rather than propose one direction. Decision, registered before this runs on real data: *"a candidate `max_per_sector` value is adopted over the live default only if it clears that bar; otherwise the live default stands."* **Run 2026-09-26: no change.** E2-1 had t −0.50, E2-3 t −0.61 and E2-off t 0.82 (518 weeks). All three fail, so `max_per_sector` stays at 2. See [Run of 2026-09-26](#run-of-2026-09-26-wave-b-e1e3-docsresearch2026-09-25-evidencebacktest). |

## Results

### Run of 2026-09-23 (`.data/backtest/2026-09-23T19-38-26.json`)

- **Sample.** 207 weekly rebalances from 2022-10-07 to 2026-09-18. The second
  half starts on 2024-09-27. The panel has 55,344 rows: sp500 98 names, nq100 56,
  eu50 47, asia100 67. No symbol was unavailable.
- **Survivorship.** The universe is today's constituents only, so every positive
  number below is an upper bound.

**Parity with the Python study.** Every figure matches the Python study to the
printed precision, and the row and date counts match exactly. This is what
porting the same formulas onto the same Yahoo data should produce. The only
known difference is that Go drops a bar with any missing OHLC field, where
Python forward-filled it. That difference does not move a printed digit.

| Figure | Python | Go lab | Plan's bar |
|---|---:|---:|---|
| Composite IC10 (t) | 0.012 (0.75) | 0.012 (0.75) | 0.012 ± 0.005 ✓ |
| Composite IC5 / IC15 | 0.016 / 0.011 | 0.016 / 0.011 | |
| mom12-1 IC10 (t) | 0.027 (1.61) | 0.027 (1.61) | |
| Hold 15 sessions, no barriers, net | +0.712% | +0.712% | +0.71% ± 0.1% ✓ |
| 2σ·√H stop only, net | +0.607% | +0.607% | |
| 9% stop / 15% target, net | +0.297% | +0.297% | |
| Top-5 15-session excess, gross (long / short) | 0.807% (1.316 / 0.111) | 0.807% (1.316 / 0.111) | |

**Barrier grid, by half.**

| Exit rule | H1 | H2 |
|---|---:|---:|
| Hold, no barriers | −0.169% | +1.611% |
| 2σ stop only | −0.296% | +1.527% |
| 9% / 15% (current) | +0.046% | +0.553% |

Longs made +2.23% and shorts −1.36% under the hold rule. The hold's edge comes
entirely from the second half and from the long side.

**Pre-registered tests.** Three tests were run. C2 was untestable and C5 was
skipped. No test passed, so nothing ships and `prescreen_version` was not added.

| Test | Mean of the statistic | NW t | H1 / H2 | US / EU / Asia | Result |
|---|---:|---:|---|---|---|
| C1: momentum weighting | +0.0104 (IC10 0.022 vs 0.012) | 1.59 | +0.017 / +0.004 | +0.010 / +0.017 / +0.006 | **Fails.** The sign is positive in every half and region, but t < 2.5. |
| C2: earnings premium | — | — | — | — | **Untestable.** No existing provider or cache holds point-in-time announcement dates. The Alpha Vantage calendar is keyed and forward-only. EDGAR gives only US 10-Q/10-K *filing* dates, and there is nothing for EU or Asia, so the every-region bar was unreachable. |
| C3: news-conditioned residual reversal | −0.0014 | −0.23 | +0.007 / −0.010 | −0.005 / +0.007 / −0.002 | **Fails.** A null result. |
| C4: beta-adjusted target | +0.0040 | 0.29 | −0.016 / +0.024 | −0.004 / +0.024 / +0.000 | **Fails.** The composite's IC10 falls from 0.012 to 0.004 once beta is removed. |
| C5: breadth | — | — | — | — | **Skipped.** It needs a new list of several hundred US names, which no existing file or keyless source provides. |

**What C4 adds to every signal's reading.** Measured against beta-adjusted
excess:

- **mom12-1** keeps most of its IC10: 0.027 falls to 0.019 (t 1.30).
- **The composite** loses about two thirds of its IC10: 0.012 falls to 0.004.
- **lowvol** flips sign, from −0.026 to +0.016. Its negative IC was the market's
  beta, not stock selection.

Most of what the screen appeared to earn is exposure to a rising market.

### Run of 2026-09-26: Wave B, E1–E3 (`docs/research/2026-09-25-evidence/backtest/`)

This section records the one run of each Wave B test, from a binary built at `b5c955e` with every
key unset. The write-up is `docs/research/2026-09-25-lab-e1-e3.md`, and the raw text and JSON
reports are in the evidence directory. The evidence files were regenerated the same day from
`30cbb62`, which adds the net per-year columns, the `decisions` block and the full `tests_run`
count. Every other number in both reports is bit-identical to the `b5c955e` run (compared with
`jq -S` after deleting `generated_at` and the new fields), and the cache served every series
(0 unavailable, no shortfall warning).

- **Sample (`--years 10`).** 521 weekly rebalances from 2016-10-07 to 2026-09-25, with the second
  half starting on 2021-10-01. The panel has 136,478 rows, drawn from 244 symbols with 0
  unavailable. The requested start was 2016-09-25, and the run printed no shortfall warning. The
  earlier `--years 10` run at `5b2e16d` got 3-month bars from `range=max` and is not evidence.
- **Comparison sample (default 4 years).** 208 rebalances from 2022-10-07 to 2026-09-25, 55,612
  rows, 0 symbols unavailable.
- **Survivorship.** The long-history caveat above applies in full. The early years are the
  weakest upper bounds.

**E1: fires.** This is the composite's beta-adjusted top-5 15-session excess per calendar year, in
%, gross and net of 30bp (`backtest-10y.txt` lines 141–155):

| | 2016* | 2017 | 2018 | 2019 | 2020 | 2021 | 2022 | 2023 | 2024 | 2025 | 2026* |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| gross | +0.428 | +0.035 | +0.286 | −0.090 | +2.171 | −0.271 | −0.575 | −0.191 | +0.905 | +1.049 | +1.373 |
| **net** | **+0.128** | **−0.265** | **−0.014** | **−0.390** | **+1.871** | **−0.571** | **−0.875** | **−0.491** | **+0.605** | **+0.749** | **+1.073** |

\* A partial year, with 13 and 37 dates.

Net, it is positive in **5 of 11** calendar years and **3 of 9** full years (line 184 prints the
verdict). That is not a majority under either count, so E1 fires. Gross, it was 7 of 11 (5 of 9)
and would have passed; the rule was pinned to net after that gross reading, as the register row
says. The rest of the evidence points the same way. The beta-adjusted composite IC10 over the whole
ten years is 0.006, with a Newey-West t of 0.66. 2017 (+0.035% gross) and 2018 (+0.286%) are
positive gross only because the cost is left out. The three losing years in a row, 2021–2023, look
like a regime rather than noise. And the early positive years carry the worst survivorship.

**Consequence, applied.** No file under `CLAUDE.md`, `docs/workflow/`, `agents/` or `internal/tui`
claimed that the screen has an edge. A grep for `edge|alpha|outperform|beats?|predictive|predicts`
found only hypotheses, cost parameters and the one phrase "overstates this screen's edge" in the E2
section below, which now reads "overstates what this screen earns". `CLAUDE.md` now states the E1
result, and the TUI results view of a legacy independent run carries one line saying the lab finds
no edge in the pre-screen net of cost. No selection behaviour or default changed.

The 4-year run's per-year table reads 3 of 5 net (2 of 3 full years); that run is a comparison
look and decides nothing (see below).

**E2: no change, so `max_per_sector` stays at 2.** These are the 10-year book arms (518 weeks
each):

| Cap | Mean β-adj excess | Weekly sd | Worst 4-wk overlapping sum |
|---|---:|---:|---:|
| 1 | 0.661% | 4.903% | −32.403% |
| 2 | 0.738% | 5.243% | −37.812% |
| 3 | 0.686% | 5.308% | −42.448% |
| off | 0.846% | 5.553% | −42.448% |

Paired against cap 2:

- E2-1 has t −0.50, with halves −0.0034 / +0.0018.
- E2-3 has t −0.61, with regions −0.0014 / +0.0007 / +0.0014.
- E2-off has t 0.82, with halves −0.0005 / +0.0027.

None clears |t| > 2.5, and none has a consistent sign. The 4-year run agrees: t 0.30, −0.03 and
1.27.

**E3: not triggered, so no long-only config test is registered.** The beta-adjusted short side
was −1.046% in H1 and +0.127% in H2 over 10 years, and −0.246% / +0.681% over 4 years. It is
therefore not ≤0 in both halves in either sample. The long side is positive beta-adjusted in
every slice (10 years: 1.026% overall, with 1.329% / 0.689% by half).

**Tests run.** The register holds **8 decision tests**: C1, C3 and C4 (2026-09-23), plus E1,
E2-1, E2-3, E2-off and E3 (2026-09-26). None has passed, and E1 fired. Every lab run performs all
eight, and `tests_run` counts them: both regenerated reports print `tests_run: 8` (the `b5c955e`
reports printed 3, because the field then counted only the C-series). On 2026-09-26 the 10-year run
was the deciding one. Its E1, E2 and E3 figures are the 5 Wave B decisions, and its C1, C3 and C4
are 3 recomputations that decide nothing. The 4-year run is 8 more comparison-only looks: C1, C3,
C4, E2-1, E2-3, E2-off, E3 and its per-year E1 table. In all, **16 statistics** were read on
2026-09-26. The C recomputations all fail the bar, with C1 closest (10-year t 1.44, 4-year t 1.62).
E1 fired, which changes wording only; no setting changed.

## E3: which side carries the result (registered 2026-09-25, before this ran)

E3 is registered as a row in the Wave B table above; this section is the
detail behind it. The 2026-09-23 run's Top-5 row above already split the barrier study's five
largest-|score| picks per index per week into longs and shorts on *plain*
15-session excess: +1.316% long against +0.111% short, gross. That split does
not say whether the short side is real selection or just C4's market-beta
exposure in reverse — the same question C4 asked of the composite's IC.

`internal/backtest/sides.go` answers it by adding the beta-adjusted split
those two numbers were missing, for the whole sample and each half. It reuses
`pickTrades`'s selection and `observe`'s `BX` unchanged — it does not
re-select trades or recompute beta adjustment — and reports, per slice (all,
H1, H2), the picks' mean directional 15-session excess, gross, split long vs
short, plain and beta-adjusted: `Result.Sides` (`SidesReport`/`SideStats`),
the `--json` output's `sides` key, and one `=== E3 ===` table in the text
report. No per-region breakdown: the task asked only for overall and per
half.

**Decision rule, registered before this runs on real data:**

> If shorts are ≤0 beta-adjusted in both halves, pre-register "long-only
> merit_veto" as a config test for the live `shipped` arm.

It ran on 2026-09-26 and did not trigger: see [Run of 2026-09-26](#run-of-2026-09-26-wave-b-e1e3-docsresearch2026-09-25-evidencebacktest).

## E2: live-book replay with a sector-cap grid (pre-registered 2026-09-25)

**Why.** On one run, the default `max_per_sector` (2, `riskgate.go`'s
`defaultMaxPerSector`) held MSFT and SAP.DE to the two IT slots and dropped ORCL on a
merit gap of 0.5% (1.4924 vs 1.4847). Neither that ORCL was the better trade (the
Chief's shadow rank has no measured value) nor that either idea's base score (66) meant
much (a separately established measurement problem with the base score itself) can be
claimed from one run. One run cannot size a cap that binds on a coin-flip-sized gap;
this experiment replays the question across every week the panel has instead.

**Question.** Across ~500 weekly books (the panel's date count × close to one book per
date), does `max_per_sector` = 1, 3 or "off" (no cap) change the book's beta-adjusted
excess, its week-to-week variability, or its worst stretch, relative to the live
default of 2 — and if so, which direction?

**Method (`internal/backtest/book.go`).** Each Friday's book is built to mirror the
live construction, mechanically standing in for the two model-dependent steps the lab
cannot call (no scouts, no Chief):

1. **Per index, the top `scoutNominationsPerIndex` (8) names by |composite|** stand in
   for a scout's nominations. `agents/scout.md` asks each scout for "5–10 candidate
   tickers" and no per-run nomination count is recorded to read a measured average
   from instead, so this takes the middle of that range rounded *up*. Rounding up
   matters: at 5 (the range's floor) the next step would never bind, since it defaults
   to 5 itself, collapsing two nomination-capping steps into one.
2. **`max_per_index`** (`orchestrator.DefaultMaxPerIndex`, live default 5, read from
   the orchestrator rather than copied) trims each index's contribution again.
3. **The per-index survivors are merged and deduped by ticker**, keeping the higher
   |composite| reading: 35 of nq100's 56 names also sit in sp500, each standardised —
   and so scored — within its own index's cross-section, so a cross-listed name
   generally carries two different composite values. Pooling per-index survivors
   without this let one ticker take two book slots and two sector-cap slots; the live
   merge step dedupes cross-listings the same way before ranking (CLAUDE.md step 1),
   and the lab already does this for sector momentum (`addIndustryMomentum`'s
   `a.seen[r.Ticker]`, `panel.go`).
4. The deduped pool is walked once in descending |composite| order; a name is skipped
   once its sector already holds `max_per_sector` picks (0 means no cap — "off"),
   exactly how the live `merit_veto` selection's `pickBook`
   (`internal/orchestrator/selection.go`) walks its own merit-ordered list.
5. **The first 5 survivors are the week's book** — reusing `picksPerIndex` and the
   15-session horizon convention from the barrier study (`barrier.go`), since a book
   pick is the same trade the barrier study already prices: top-|composite|, next-open
   entry, held 15 sessions. Each pick carries the direction of its own composite's
   sign and its region, recorded per pick (`BookPick.Dir`, `BookPick.Region`) rather
   than assumed for the whole book, so a later long/short split can read it directly
   and the region leg of the adoption rule below can restrict to one region's picks.

The grid is `SectorCaps = {1, 2, 3, off}` — the live default and its two neighbouring
integers, plus the uncapped baseline.

**What this does not model.** Beside standing in for the scout call itself, two further
live steps are skipped rather than approximated: `max_shortlist` (12 — the merged
shortlist is capped *before* `max_per_index`/`max_per_sector` ever see it; see step 1
of `independent-research.md`) and the shortlist's own `max_per_sector`+1 reservation
(the "one spare per sector" buffer the merit sort carries so the risk gate has
something to choose between). Both are shortlist-construction details the mechanical
top-|composite| stand-in already replaces wholesale — there is no separate shortlist
object in this replay for either cap to act on — so leaving them out does not add a
distinct source of error beyond the one already disclosed above; it is recorded here
for completeness, alongside the "scouts as a composite cut" simplification.

**Metrics**, per cap value and per half (H1/H2, split at the same `mid` date as every
other table here): the number of weeks that produced a book **with a known
beta-adjusted return** (a tail week whose 15-session forward window runs past the data
has a book but no return, and does not count); the mean beta-adjusted excess per book
(r − β·r_bench at 15 sessions, C4's target — the headline figure, since C4 already
found the plain benchmark-excess target overstates what this screen earns); the sd of that
weekly series; and the plain benchmark-excess mean, shown for reference only.

*Worst 4-week figure.* `worst_4wk_overlap_pct` is the most negative sum of any 4
consecutive weekly book returns (`worst4WeekPct` — a rolling worst-month check, not a
peak-to-trough drawdown off a compounded curve, since nothing in this lab compounds
weekly returns into a NAV). Each weekly value is itself a 15-session (~3-week) hold, so
4 consecutive weekly values span holds that already overlap: at any moment roughly 3
weekly cohorts are concurrently open. Turning this into a true capital-scaled
portfolio-level drawdown would require a further, unverified modelling choice (how
much of each cohort's capital is still at risk while the next two are open, e.g. an
even one-third-per-cohort split) that this lab does not make anywhere else. Rather than
introduce that choice quietly, the figure is named and documented for exactly what it
sums — 4 overlapping cohorts' raw returns — and read as a stress indicator, not a
book-level drawdown number.

**Adoption rule.** The Wave B bar applies in full, exactly as the plan's controller
ruling states it: a candidate cap is adopted over the live default only if the
per-week difference in beta-adjusted book excess (candidate minus the live default's
arm, paired by date — a date either side lacks, or where the difference cannot be
computed, is dropped rather than treated as zero — Newey-West t at `nwLags(15)` = 4
lags) clears **|t| > 2.5** and is the **same sign in both halves and every region**.
This bar is two-sided (`|t|`), unlike the one-sided pre-registered signal tests above:
those each propose a specific directional improvement, while a departure from the live
`max_per_sector` could plausibly help or hurt, so both directions must be eligible to
pass, provided the sign is internally consistent everywhere it is checked.

The "every region" leg is computed without rebuilding the grid on region-filtered
panels: a book pools all four indices in one basket by construction, so there is no
single region per week the way a per-index cross-section has one. Instead, each week's
book contributes one *region-restricted* value per region it has a pick in — the mean
beta-adjusted return of just that week's picks in that region — and the region leg of
the adoption bar is checked on that narrower series. `internal/backtest/book.go`'s
`BuildBookGrid` computes one `TestResult` per non-default cap (`BookGrid.PairedTests`,
IDs `E2-1`/`E2-3`/`E2-off`) with this statistic, its overall mean and t, its halves,
and its regions — the same shape the `TestResult` type already gives C1–C5, so the
`--json` output and the text report render it the same way. Every cap comparison
actually run against live data will be recorded here as its own test, exactly as
C1–C5 were, with this rule fixed before that run happens.

**Status.** Code and the unit tests that check the builder (`book_test.go`: the
sector cap, `max_per_index` and the nomination stand-in each independently bind; a
ticker cross-listed in two indices is counted once, keeping the higher |composite|
reading; `off` is exactly the plain top-5 by |composite| with sector composition
ignored; a tail week with a book but no known return does not inflate `Weeks`; and the
paired adoption test's two-sided sign check fails when only one region's sign flips)
are in place. The grid ran on 2026-09-26, and the recorded results are under [Run of 2026-09-26](#run-of-2026-09-26-wave-b-e1e3-docsresearch2026-09-25-evidencebacktest).
