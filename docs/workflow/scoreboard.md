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

Daily bars come from the keyless Yahoo chart API through the shared cache, and
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
- **`cfr scoreboard`**: the same as plain text, plus the slice lines.
- **`cfr scoreboard --json`**: the full `scoreboard.Summary`
  (`entries[]`, `replay`, `scored`, `closed`, `wins`, `losses`, `win_rate`,
  `avg_pnl_pct`, `avg_r`, `avg_excess_pnl_pct`, `by_outcome`, `by_direction`,
  `by_index`, `by_confidence`, `by_domain`, `skipped`, `run_count`).
- **`cfr scoreboard --legacy`**: the old mark-to-current-price numbers, for
  comparison against the replay on the same runs.

## Configuration

| Key | Default | Meaning |
| --- | --- | --- |
| `[scoreboard] fill_window_days` | `3` | sessions a limit entry stays live (`CFR_FILL_WINDOW_DAYS`, `--fill-window`) |
| `keep_runs` | `100` | run directories retained, i.e. how much history there is to score |
