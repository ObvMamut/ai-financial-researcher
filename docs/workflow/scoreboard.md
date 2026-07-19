# Scoreboard

How past trade ideas are measured against reality. Implemented in
`internal/scoreboard`; surfaced in the TUI ("Scoreboard" on the home screen) and
headless via `cfr scoreboard [--json]`.

## What it measures

For every saved run under `runs/`, each idea in `ideas.json` that carries a
`price_at_generation` (the verified last close from the quant pack, backfilled by
the orchestrator after validation) is scored:

- **Current price** — latest daily close from the keyless Yahoo chart API, via the
  shared daily-keyed cache in `.data/` (repeat scoreboard views are free until the
  next calendar day).
- **P&L %** — direction-aware return vs `price_at_generation`:
  `(current − baseline) / baseline × 100`, negated for SELL ideas.
- **Target / stop hit** — whether the *current* price is beyond the idea's target
  or stop (direction-aware). Only evaluated when the idea has both levels.

Aggregates: win rate (`wins / scored`, where a win is P&L > 0), average P&L, and
counts of scored vs skipped ideas.

## Known limitation: current-price-only path

Target/stop hits are judged on the **latest close only**, not the price path. An
idea that touched its target last week and has since retraced shows the retraced
value and no `target hit` flag; one that gapped through its stop and recovered
shows no `stop hit`. Judging the true path would require replaying daily (really
intraday) history per idea since generation — deliberately out of scope for now.
Read the flags as "where the trade stands today", not "what a real fill would
have done".

## Skipped ideas

Ideas without `price_at_generation` (runs predating the field) are counted as
`skipped` and never scored — there is no honest baseline to measure from.
Ideas whose current price cannot be fetched appear in the list with an error
note and are excluded from the aggregates.

## Retention

`CleanupOldRuns` keeps the newest `keep_runs` run directories (default **100**,
raised from 20 specifically so the scoreboard has history to work with). Configure
via `keep_runs` in `cfr.toml` or `CFR_KEEP_RUNS`.

## Output

- **TUI**: summary line (win rate, W/L, avg P&L) + scrollable per-idea rows with
  baseline → current price, colored P&L, and target/stop marks.
- **`cfr scoreboard`**: same as plain text.
- **`cfr scoreboard --json`**: the full `scoreboard.Summary` structure
  (`entries[]`, `scored`, `wins`, `losses`, `win_rate`, `avg_pnl_pct`, `skipped`,
  `run_count`) for scripting.
