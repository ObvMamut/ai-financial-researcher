# Scoreboard

For opt-in thesis runs, use `--research-compare` to separate research mode,
schema/model/persona cohorts and compare calls at both 10 and 15 sessions.
See [thesis evaluation](thesis-research.md#evaluation). Ordinary thesis replay
respects the absolute entry and thesis deadlines rather than restarting the
holding window after a late fill. Thesis observations have an unscored confidence
bucket and are excluded from legacy calibration.

How past trade ideas are measured against reality. Implemented in
`internal/scoreboard`; surfaced in the TUI ("Scoreboard" on the home screen) and
headless via `cfr scoreboard [--json] [--legacy] [--fill-window N]`.

The default measurement is a **path replay**: every idea is simulated forward
through its own daily bars, as the trade it actually specified. The old
mark-to-current-price math is still available behind `--legacy`, only so the two
numbers can be compared on the same history.

## The replay

For every saved run under `runs/`, each idea in `ideas.json` is walked forward
from the session *after* `generated_at` (the generation-day bar is the one the
idea was priced off; entering on it would be hindsight).

Daily bars come through the routed price source — Alpaca for US listings where a key is
configured, the keyless Yahoo chart API otherwise — via the shared cache, and
fall back to the run's own saved `prices/` — which is what keeps an old run
scorable after a ticker is delisted or renamed.

### 1. Does the entry fill?

The idea's `entry` is treated as a limit that stays live for
`fill_window_days` sessions (default **3**). Past that, the setup the idea
described is not the setup in front of you.

- A bar whose range brackets the limit fills **at the limit**.
- A bar that *opens* through the limit in the trader's favour (below it for a
  BUY, above it for a SELL) fills **at the open** — better than asked. This is
  the one place optimism is arithmetically correct.
- No such bar once the window has **run out** → outcome `unfilled`. **An unfilled
  limit is not a flat trade**; it is a trade that did not happen, and it is
  excluded from the win rate entirely.
- No such bar while the window is **still live** (fewer sessions have passed than
  `fill_window_days`) → `open` with no fill. An idea one session old has not
  failed to fill; it has not been given its chance. `bars_held` is then the
  sessions since generation, and `entry_filled` is absent.

### 2. Which barrier comes first?

From the fill bar forward, up to `timeframe_days` bars (default 10):

- **Stop before target on a bar that touches both.** A daily bar does not say
  which came first, and assuming the target is how a backtest flatters itself
  into a strategy nobody can trade.
- **A breached stop books the realized price, not the stop.** A BUY stopped at 95
  on a bar that opened at 90 exits at 90 — the stop level never traded. Booking
  the level assumes a fill that did not exist, and does it most on exactly the
  days it matters.
- **A touched target books the target** (or the open, if the bar gapped past it):
  a resting limit fills at its price or better.
- **Horizon reached** → `expired` at that bar's close.
- **Not enough history yet** → `open`, marked to the last close.
- **No usable history at all** → `error`.

### 3. What the trade is worth

- **`pnl_pct`** — direction-aware return from the **fill**, not from
  price-at-generation.
- **`risk_adj_pnl`** — the same result in **R**: multiples of the distance from
  the fill to the stop. A +3% win on a 1% stop and a +3% win on a 6% stop are not
  the same result, and averaging percentages hides that.
- **`benchmark_pnl_pct` / `excess_pnl_pct`** — the index's return over the same
  holding window (`universe.BenchmarkSymbol` for the idea's index) and the idea's
  return net of it. For a SELL the benchmark's move is *added*: being short into
  a 4% decline is 4% of the result the market handed you. A long that made 4%
  while its index made 6% did not work.

## The horizon measurement: was the *call* right?

The replay above answers a trader's question — which barrier was touched first. It cannot
answer this project's question, which is whether the name moved the way the idea said over
the next two to three weeks. The two come apart in both directions: an idea can be right
about the fortnight and still be stopped out by noise on day two, and an idea can be flatly
wrong and expire having touched neither barrier. Three of the first five closed trades
expired exactly that way, and the record had no way to say whether any of them had been
right.

So every replayed idea also carries a **horizon read**, computed in `horizon.go` over the
idea's own `timeframe_days`:

| figure | anchored at | who it exists for |
| --- | --- | --- |
| **the call** | the close the idea was generated off | every idea, *including the ones whose limit never traded* |
| **the trade** | the fill | ideas that filled |

The call is the directional claim with the entry-limit lottery removed: an idea whose limit
never traded still said the stock would fall, and was still either right or wrong about
that. Scoring the pipeline only on the calls that happened to get a fill measures the
limits, not the reads.

Both are also reported net of the name's own index benchmark, because *"the long was right"*
and *"the market went up"* are not the same finding. The gap between `right` and
`beat bench` is how much of the record is the market.

A window that has not elapsed yet is **not** a miss. It is excluded, and the count of
excluded ones is printed, so a recent idea can never read as a flat result.

## Independence: bets, not tickets

A count of ideas is not a count of observations. Five runs were fired on 2026-09-01 and
three on 2026-08-29; September's 41 ideas are 22 distinct calls; STLAM.MI SELL and AMGN BUY
each appear five times. Re-proposing the same name in the same direction the same afternoon
does not produce a second data point about whether that call was right — it produces the
same data point again, and a win rate counting tickets reads it as five.

This defeats the thresholds that exist to stop a thin sample being read as a finding:
`MinClosedForFeedback` gates whether the Chief is shown a track record at all, and
`MinClosedForEdge` swaps the risk gate's assumed edge for a measured one. A duplicated
sample reaches both without ever reaching the evidence.

So **every cell is counted over deduplicated entries**: one observation per
(ticker, direction) inside `DefaultDedupeWindowDays` (**7 calendar days**, which is five
sessions), keeping the earliest — a re-proposal must not be scored off a better later price
than the one originally published. Nothing is hidden: the rows still list every idea and the
dropped count is printed above them.

Two deliberate exceptions:

- the **outcome tally** (`Outcomes: target 1 · stop 1 · …`) is a census of what happened to
  every idea, so it counts all of them;
- the **fill record** counts all of them too, because a re-proposal carries its *own* limit
  at its own price. It is a genuine second observation of whether a limit that far out
  fills, while being the same observation of whether the call was right.

An entry with no parseable timestamp is kept. A missing date is not evidence of duplication,
and dropping on it would quietly delete the oldest runs.

## Control arms: does the model stack beat its own arithmetic?

`cfr scoreboard --control`.

The pipeline is a funnel. Stage 0.5 ranks the universe on a computed composite whose *sign
is a direction*; the scouts screen that ranking, the specialists analyse the twelve names it
passed, and the Chief picks five. Every stage above the pre-screen can re-rank the funnel's
output; none can reach a name the funnel did not pass. That makes one question decide
whether the model stages earn their cost, and nothing else in this repository can answer it:
**would taking the top of the ranking, with no model called at all, have done as well?**

Every set of calls is scored through one identical procedure — same anchor, same horizon,
same benchmark arithmetic:

| arm | what it is |
| --- | --- |
| `composite` | the pre-screen's own strongest rows, direction = the sign of its score. No model involved at any point. |
| `shortlist` | the twelve names the funnel passed, at the bias the scouts gave them. Scouts and merge included; specialists and Chief not. |
| `shipped` | what `ideas.json` actually contains, for **legacy-mode** runs only. |
| `thesis` | what `ideas.json` contains for thesis-mode runs (mode from `ideas.json`, else `metadata.json`). |
| `thesis-lean` | every researched name in a thesis run at its dossier `lean` (`BUY`/`SELL`), read by key from `runs/<run>/data/research-<hexticker>.json` → `dossier.lean`, with `dossier.conviction` (1–5) recorded per call. Absent or `NONE` leans are counted as `non_directional`, not scored. Anchored at the pre-screen close, as the shortlist is. Scored even when the run shipped nothing. |
| `thesis-lean-backfill` | the hand-judged leans in `docs/research/2026-09-23-evidence/thesis/leans.csv` (runs that predate the lean field). `BUY (weak)` → BUY with `lean_strength: weak`; `none evident` is non-directional. Anchored at the run's generation close (`closeOnOrBefore`), measured against the benchmark of the index the run screened the name under. A missing file leaves the arm empty. |

Read it as a chain: **shipped over composite** is what the whole model stack adds, and
**shipped over shortlist** is what the specialists and the Chief add on top of the screening
they were handed. **thesis-lean over shortlist** is what per-company source research adds
over the names it was handed — the difference the thesis switch-off rule is written against
(improvement plan §3.5): thesis stays on while that interval overlaps or beats zero, and is
switched off (code kept) only if, after at least 8 market weeks, its whole 95% interval sits
below zero. The backfill arm is differenced against the shortlist too, but it is a reader's
judgement of old dossiers, not the researcher's own lean, and is never pooled with
`thesis-lean`.

### Beta-hedged excess

Every scored call also carries `call_beta` and `call_hedged_excess_pct`: the direction-signed
return less *beta* units of its benchmark rather than one. Beta is point in time — the last
120 aligned daily log returns up to the anchor date on the same series the call is scored on,
60 at minimum, else no hedged figure. Each arm reports the average as `beta_hedged`
(`n`, `avg_beta`, `avg_hedged_excess_pct`) and the text table prints it as the `β-hedged`
column. The gap between `avg excess` and `β-hedged` is the arm's market exposure, not its
selection: the backtest's longs earned +1.32% excess against the shorts' +0.11%, which is
the shape carrying more beta than the benchmark produces in a rising market.

### Whole-universe IC

`universe_ic` (always at **10 and 15** sessions, whatever `--horizon` says): for each run with
a `prescreen.json`, the Spearman rank correlation between every non-excluded row's signed
composite score and that name's realised benchmark-excess return. It is computed **within
each index** and the indices averaged, because the pre-screen standardises within the index;
an index needs 10 completed rows. One run yields one IC over ~268 names instead of five calls,
which makes it the fastest honest read on whether the screen works out of sample.

A ranking is counted once per (index, anchor session), the anchor session being the last bar
on or before the generation date in the index's own benchmark: when an earlier run already
ranked an index for that session, the later run's ranking of it is dropped
(`repeat_rankings_dropped`). A Saturday and a Sunday run rank the same Friday closes, and a
run on a US holiday repeats the US ranking while its European rows have moved on. The
closes themselves are not the key: a run during market hours sees a live partial bar, so
five runs one afternoon carry five slightly different closes and are still one ranking. `mean_ic` is the plain average over runs; `ci` is the week-clustered
bootstrap interval on it with runs as the units, grouped by ISO week. `per_run` lists each
run's IC and per-index figures. Prices come through the same series cache as the arms, so
each symbol is fetched once however many runs rank it.

The composite arm takes rows from **both ends** of the ranking. The composite is a signed
long ranking, so its strongest calls sit at both extremes — `|score|` is the conviction and
its sign is the direction — and taking the top *n* would produce a long-only arm that
measures a bull market rather than the ranking. It is the same defect the scout tables were
split by direction to fix.

Deliberately absent: entries, stops, targets and fills. A control arm has no levels — the
pre-screen never proposed any — so comparing on barrier outcomes would be comparing a trade
against a call. Everything here is the call.

Every arm is scored over one fixed horizon (`--horizon`, default **15** sessions) rather
than each idea's own `timeframe_days`: the control arms cannot state a holding period, and a
comparison in which one arm picks its own window is not a comparison.

`MinArmN` (**30**) is the count below which the report prints its two differences and then
says in as many words that they are noise. That refusal is the important half — three arms
of five calls will differ by twenty points on chance alone.

Each arm's average excess carries a **95% bootstrap interval** (`excess_ci`), and the two
differences carry one each (`diffs`). The resampling unit is the ISO week the call was
generated in, not the call: calls from one week ride one market, and resampling them as
independent draws would report precision the sample does not have. A difference resamples
the weeks of both arms together, so a market move both arms shared is not counted as
disagreement between them. An arm whose closed calls span fewer than two weeks gets no
interval. The seed is fixed, so unchanged history prints an unchanged interval. With the
single-digit week counts this history has, a percentile bootstrap is optimistic — read a
bound that barely clears zero as a reason to keep measuring, not as a result.

### The success criterion (pre-registered 2026-09-23, corrected the same day)

A two-week edge of 50bp against ~6% idiosyncratic two-week volatility needs roughly **550
independent calls** to show at t = 2 — years of five-idea runs. The live report is therefore
a *monitoring* instrument, not a decision rule:

- Mechanics and signals are adopted or dropped in the backtest lab (`cfr backtest`), which
  has thousands of point-in-time observations.
- Model stages are judged by shadow arms scored over the **whole shortlist and every thesis
  lean**, not by the five shipped ideas, and only once their difference interval excludes
  zero. `n ≥ 60` independent calls is the floor below which no arm difference is read at all.

The standing comparison is still `shipped − composite` at 10 sessions, with
`shipped − shortlist` as the proxy while the composite arm is thin.

At registration (48 runs, 2026-06-01 → 2026-09-23) neither difference cleared it:
`shipped − shortlist` was −0.86% [−1.98%, +0.28%] at 10 sessions and −0.73% [−1.66%, +0.24%]
at 15. The scouts' shortlist alone was +1.42% [+0.29%, +2.21%] at 15 sessions over 8 weeks.

Runs generated before Stage 0.5 existed carry no `prescreen.json`, so the composite arm
skips them and says how many. The shortlist arm does not need one: when a row is missing it
takes its anchor from the name's own bars, at the last close on or before the run.

## Win rate is over closed trades only

`closed` = `target` + `stop` + `expired`. `open` and `unfilled` rows are not
losses, and `error` rows are not trades. `scored` counts the rows that carry a
P&L at all — the closed trades plus the open positions that actually filled.

This is the fix for a measured artefact, not a preference: the 32% win rate that
motivated this rewrite came from a history where roughly a third of the rows were
0.00% — ideas whose limit never filled or which had barely aged — all of them
counted in the denominator and none in the numerator.

## Slices

Every closed trade is also accumulated into `by_direction`, `by_index`,
`by_confidence` (`<40` / `40-59` / `60-79` / `80+`) and `by_domain`. Each slice
carries `n`, `wins`, `losses`, `win_rate`, `avg_pnl_pct`, `avg_r`.

`by_domain` scores a domain over the closed trades it **backed** — where its
signed score in `domain_scores` (recorded at generation, bullish-positive) agreed
with the direction actually taken. A domain is judged on the trades it argued
for; scoring it on trades it called the other way, or had no view on, measures
nothing. A domain that keeps backing losers is the one to reweight in
`[weights]`.

## Attribution: which *kind* of call worked

The slices above answer "did it work". Attribution (`internal/scoreboard/attribution.go`)
answers "which kind of it". Over the same closed trades it accumulates four further cells,
each carrying its own `n`:

| Cell | Key | Why it exists |
| --- | --- | --- |
| `by_setup` | `buy/wide-stop`, `sell/tight-stop`, … | direction crossed with how much room the trade gave itself (stop distance as a share of entry: `<5%` tight, `<10%` medium, else wide). This is the shape a "why did it work" answer actually lives in. |
| `by_coverage` | how many domains scored the name | does thin evidence lose? The weights are not recorded per idea, so this counts domains rather than weight — coarser, but recoverable from what the idea stored. |
| `by_consensus` | the recorded `consensus` band | the other half of the same question: does thin-but-unanimous beat well-covered-but-split? |
| `by_sector` | the shortlist's sector for the name | joined from the producing run's `metadata.json`, which is the only place sector lives. |

And a **fill record**, which measures the thing every other slice takes for granted:

```
fills: 22 of 49 replayable ideas filled, 0 never traded their limit,
       27 still inside the window, 0 had no usable history, 45 had no levels to replay
entry at the close (±0.25%): n=5, 100% filled, avg +0.49R over 5 closed
```

`by_offset` buckets ideas by where the limit sat relative to the price at generation,
**signed toward the trade's own direction** — a buy limit 1% below and a sell limit 1% above
are the same decision and belong in the same bucket. An entry rule that does not fill is not
a strategy however good the ideas behind it, and until this table existed nothing in the
system measured it.

`Lines(minN)` and `Cells(minN)` both suppress cells under `MinCellN` (**5** closed trades).
A cell below that cannot support a claim, so it is not shown and — see below — cannot be
written about either.

## Calibration: feeding the record back

`Calibrate` reduces a replayed summary to `.data/calibration.json` — overall win rate,
average R, average hold, and the same per-domain / per-confidence / per-direction buckets,
all over closed trades. It is written by `cfr scoreboard` and by the TUI screen, and
refreshed by a run when the stored copy is more than 24h old (bounded and best-effort; a
run never fails for want of it).

Two consumers:

- **The Chief Analyst**, at `n_closed ≥ 10`: a ≤15-line `### Track record` block in its
  prompt, and a copy in `runs/<ts>/calibration.json`.
- **The risk gate**, at `n_closed ≥ 30`: the expectancy simulation's assumed edge is
  replaced by the measured average R (see `scoring.md`).

Both thresholds exist because a thin record is worse than none: it reads as evidence and is
noise.

## Post-mortem: the part counting cannot do

Attribution counts outcomes. Only the reasoning recorded with each past idea says what the
winners had in common, and that is prose. So one cheap-engine call
(`agents/post-mortem.md`, driven by `internal/orchestrator/postmortem.go`) reads the
attribution table plus one line per closed trade — ticker, direction, base score, consensus,
per-domain scores, the Chief's own `why` at the time, outcome, R, bars held — and writes
lessons.

The split is the same one every domain in this system uses: **Go counts, the model reads.**

- **Gated at `MinClosedForPostMortem` (10 closed trades)**, matching the track record's
  threshold. Below it the block is simply absent.
- **Enforced against the counted cells.** A lesson must name a cell that appears in the
  attribution table, spelled the way that table spells it, with at least `MinCellN` closed
  trades behind it. One that does not is deleted and the deletion is reported — in the run
  log, and in `metadata.json`'s `post-mortem` row alongside every other domain's corrected
  scores. A lesson that *overstates* its own `n` is not deleted; the table's number replaces
  the claimed one, and the correction is recorded. At most `MaxLessons` (**8**) survive.
  This is the same guard that catches a specialist scoring a name it had no data on, and it
  is here for a stronger reason: a confident sentence about the system's own performance is
  the easiest output in the pipeline to invent and the hardest for a reader to check.
- **Weight suggestions are advisory and never applied.** The tail may propose nudging a
  domain up or down; a suggestion naming something that is not a domain, not a direction, or
  carrying no reason is dropped. The rest surface in the run log for a human to act on by
  editing `[weights]`. Nothing in the pipeline reweights itself.
- **A run never blocks on it.** A failed call, an unparseable tail, or a missing persona
  (`agents.v1` has none, so the A/B control arm runs without one) leaves the Chief simply
  untold — which is the state every fresh install starts in.
- **Stored** as `.data/postmortem.json` with the same 24h staleness rule as the calibration,
  copied into `runs/<ts>/`, and written as `runs/<ts>/post-mortem.md` so it appears in the
  reports screen.

The surviving lessons reach the Chief as a `### Lessons from N closed ideas` block beside the
track record. They qualify the base scores inside the existing `chief_adjust_band`; they are
not a second scoring layer and they are not evidence about any name in today's shortlist.

Adding a persona file changes `persona_sha`, so the post-mortem's arrival registers as a new
A/B arm. That is expected, not a bug.

## Persona A/B

Personas are runtime data: they change without a code change, and a run's outcome is only
attributable if the exact prompt text behind it is identifiable. Every run records
`persona_sha` (a hash per role) and `persona_set` (the directory they came from) in
`metadata.json`, and the replay labels every idea with a key combining the two — e.g.
`agents@4f1a2b`. Editing one persona in place changes the digest, so a mid-experiment
tweak shows up as a third arm rather than contaminating the second.

`agents.v1/` is the frozen control: the personas exactly as they stood at commit
`f137f2a`, before the research-quality overhaul rewrote them. Run against it with

```bash
CFR_AGENTS_DIR=agents.v1 go run ./cmd/cfr run --json
```

alternating with ordinary runs. `cfr scoreboard` then prints a **Persona A/B** block
comparing the arms on closed ideas: n, win rate, average R, average P&L. Each line says so
when its arm is under `MinClosedPerArm` (**15** closed ideas) — the failure mode of an A/B
is not a wrong number, it is a right number read too early.

Two things the comparison does *not* control for, and should be read with:

- **Both arms run today's pipeline.** The computed base scores, the risk gate and the
  verified earnings calendar apply either way. The question being answered is which prompt
  set is better *under this pipeline*, not whether the overhaul as a whole helped — the
  pipeline is not going back.
- **Both arms see the same track record**, since the calibration block is pooled across
  runs. It is a constant across the arms, so it cannot bias the comparison between them,
  but it does mean neither arm is being measured in isolation from the feedback loop.

## Skipped ideas

Ideas with neither `price_at_generation` nor an `entry` are counted as `skipped`
and never replayed — there is no honest baseline to measure from.

## Retention

`CleanupOldRuns` keeps the newest `keep_runs` run directories (default **100**,
raised from 20 specifically so the scoreboard has history to work with).
Configure via `keep_runs` in `cfr.toml` or `CFR_KEEP_RUNS`.

## Output

- **TUI**: headline (win rate over closed, avg P&L, avg R), the outcome tally,
  and scrollable per-idea rows with fill → exit, coloured P&L, R, and the outcome
  that ended the trade.
- **`cfr scoreboard`**: the same as plain text, plus the slice lines and the
  attribution cells (replay mode only — the legacy math has no notion of a trade
  closing, so it produces none of them).
- **`cfr scoreboard --json`**: the full `scoreboard.Summary`
  (`entries[]`, `replay`, `scored`, `closed`, `wins`, `losses`, `win_rate`,
  `avg_pnl_pct`, `avg_r`, `avg_excess_pnl_pct`, `horizon`, `horizon_trade`,
  `duplicates`, `by_outcome`, `by_direction`, `by_index`, `by_confidence`,
  `by_domain`, `skipped`, `run_count`).
- **`cfr scoreboard --legacy`**: the old mark-to-current-price numbers, for
  comparison against the replay on the same runs.
- **`cfr scoreboard --control`**: the arms above, the beta-hedged column and the
  universe IC, with `--horizon` to set the window the arms are scored over. The lean
  backfill is read from `docs/research/2026-09-23-evidence/thesis/leans.csv` relative to
  the working directory. `--json` emits a `scoreboard.ControlReport`.
  It is never folded into the stored track record: it answers a question about
  the pipeline's construction, not about its trades.
- **`cfr postmortem`**: the attribution table computed fresh, plus whatever
  lessons are stored in `.data/postmortem.json`. `--min-n` lowers the cell floor
  for reading (it does not lower what a lesson may be written about);
  `--json` emits `{attribution, post_mortem}` — the attribution's own shape,
  kept out of `cfr scoreboard --json` so that stays a bare `scoreboard.Summary`.

## Configuration

| Key | Default | Meaning |
| --- | --- | --- |
| `[scoreboard] fill_window_days` | `3` | sessions a limit entry stays live (`CFR_FILL_WINDOW_DAYS`, `--fill-window`) |
| `--horizon` | `15` | sessions each control arm is scored over (`cfr scoreboard --control` only) |
| `keep_runs` | `100` | run directories retained, i.e. how much history there is to score |

Two thresholds are constants rather than settings, because they are statements about when a
number becomes readable rather than preferences: `MinClosedForPostMortem` (**10** closed
trades before any lesson is drawn) and `MinCellN` (**5** closed trades before a cell may be
shown or written about). `DefaultDedupeWindowDays` (**7**) and `MinArmN` (**30**) are
constants for the same reason: they say when a count is a sample, not what anyone prefers.

## Research evaluation

`--research-compare` now reports per-run failures, available regional coverage,
latency, token-accounting completeness and overlapping call windows alongside
cohort returns. Missing result artifacts are retained as failures; valid empty
runs remain separate. Optional `--research-pairs <manifest>` audits named
legacy/thesis pairs and declared input-snapshot hashes. `--offline` restricts
this comparison to saved prices. `--research-cost-bps <number>` adds an explicitly
assumed execution-cost scenario for closed barrier replays, separate from
10/15-session directional calls. These flags require `--research-compare` and
never update calibration. See the [pairing contract and manifest example](thesis-research.md#explicit-pairing-and-evaluation-diagnostics).


Research diagnostics separately report attempted versus deferred companies,
readable research with substantive challenge versus failure, explicit versus
historically inferred truncations, capacity failures, per-stage usage and request
outcomes. `attempts_with_incomplete_usage` prevents absent historical telemetry
from being interpreted as known zero usage. Primary Chief failure and fallback
success are separate fields; invalid fallback JSON is not success. Source-host
counts describe delivery endpoints and must not be presented as independent
reporting origins.
