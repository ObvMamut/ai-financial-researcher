# Scoreboard

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
  `avg_pnl_pct`, `avg_r`, `avg_excess_pnl_pct`, `by_outcome`, `by_direction`,
  `by_index`, `by_confidence`, `by_domain`, `skipped`, `run_count`).
- **`cfr scoreboard --legacy`**: the old mark-to-current-price numbers, for
  comparison against the replay on the same runs.
- **`cfr postmortem`**: the attribution table computed fresh, plus whatever
  lessons are stored in `.data/postmortem.json`. `--min-n` lowers the cell floor
  for reading (it does not lower what a lesson may be written about);
  `--json` emits `{attribution, post_mortem}` — the attribution's own shape,
  kept out of `cfr scoreboard --json` so that stays a bare `scoreboard.Summary`.

## Configuration

| Key | Default | Meaning |
| --- | --- | --- |
| `[scoreboard] fill_window_days` | `3` | sessions a limit entry stays live (`CFR_FILL_WINDOW_DAYS`, `--fill-window`) |
| `keep_runs` | `100` | run directories retained, i.e. how much history there is to score |

Two thresholds are constants rather than settings, because they are statements about when a
number becomes readable rather than preferences: `MinClosedForPostMortem` (**10** closed
trades before any lesson is drawn) and `MinCellN` (**5** closed trades before a cell may be
shown or written about).
