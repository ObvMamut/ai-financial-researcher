# Plan v5 — is the holding period the problem? (H1/H2 horizon test)

## Context

11 pre-registered lab tests have run (C1, C3, C4, E1, E2-1, E2-3, E2-off, E3, D1, D2, D3); none
shows an edge at the live 10–15-session hold. The only term with a consistent sign is 12-1
momentum, a monthly-horizon factor. v5 asks once, pre-registered, whether the screen or momentum
earns at 21 and 63 sessions. D3 is not revisited. No live `cfr run`, no model spend, stdlib only.

This file is copied verbatim to `docs/plans/2026-10-01-horizon-test.md` as the first execution
step, with a status table appended at the end.

## What the lab can already do (finding for question 1)

It cannot score 21 or 63 sessions today:
- `internal/backtest/panel.go:29` — `Horizons = [3]int{5, 10, 15}`; `Record.XS/BX` are `[3]`.
- `analyze.go` — `cell.ic/bic` are `[NumSignals][3]`; `evaluate` hardcodes `nwLags(10)`.
- `barrier.go` `pickTrades` is fixed at 15 sessions (`xs15/bx15`, `barrierHorizon`).
- Newey-West is already general: `stats.go` `NeweyWestT(xs, lags)`, `nwLags(h) = h/5+1`
  (21 → 5 lags, 63 → 13 lags, which covers 63 sessions ≈ 12.6 weekly rebalances of overlap).
- No p-values or multiple-testing correction anywhere.

So the extension is: two more horizons, a horizon-parametrised top-5 book, a lag parameter on
`evaluate`, and a Holm block. Nothing else changes.

## Rulings (recorded, no questions asked)

1. **Horizons become `[5]int{5, 10, 15, 21, 63}`**, appended so indices 0–2 (and every
   `[1]`/`[2]`/`bookHorizon` use) keep their meaning. JSON arrays grow from 3 to 5 entries; every
   pre-existing figure must be bit-identical (checked against the 2026-09-30 artifact).
2. **H1 picks are exactly E1's picks**: each (date, index)'s five largest |composite|, non-zero
   score, known σ, held at the composite's sign — the selection is extracted from `pickTrades`
   into a shared helper, not copied. Cross-listed names are not deduped, as in E1/barrier.
   Per-date statistic: mean over that date's picks of `dir·BX[h] − 0.003` (30bp once per name,
   whatever the horizon). Region series: the same mean over that region's picks only.
   A pick with NaN `BX[h]` (window past the data) is dropped; a date with none is dropped.
3. **H2** is `mom12_1`'s beta-adjusted per-date IC (`cell.bic[SigMom12_1][h]`), averaged across
   indices per date like C1/C3/C4, via the existing `evaluate` with a lags argument.
4. **Bar (all four tests)**: the existing `evaluate` bar — Newey-West t > +2.5 with
   `nwLags(h)` lags, positive mean in both halves (same median-date split) and in US, EU and Asia.
   Direction positive. Tests: **H1-21, H1-63, H2-21, H2-63**.
5. **A 15-session H1 row is reported as a reference** (`status: "comparison"`), so the new
   horizons read against the live one. It is not a test and is not in the count or the family.
6. **Holm family = every test with status `run` in the report** = 11 + 4 = **15** at
   `--years 10`. The run recomputes the earlier 11 from the same cache, so one report holds the
   whole family. p-values:
   - one-sided t tests (C1, C3, C4, D1, D2, D3, H1-21, H1-63, H2-21, H2-63): `p = 1 − Φ(t)`,
     normal approximation to the NW t (`math.Erfc`);
   - E2-1/E2-3/E2-off (registered two-sided): `p = 2(1 − Φ(|t|))`;
   - E1 (majority-of-years rule, no t): one-sided binomial sign test,
     `P(X ≥ positive years | n = years, ½)`;
   - E3 (diagnostic decision rule, no statistic): `p = 1`, kept in the family so m stays 15 and
     every other adjustment is at least as strict.
   Holm: sort ascending, `adj_(i) = max_{j≤i} min(1, (m−j+1)·p_(j))`. Reported, not a gate: the
   decisions below use the registered bar. With m = 15, the smallest p must be < 0.0033
   (t > 2.71) to survive Holm at 5%, and the write-up says whether any pass does.
7. **Decisions fixed before the run**:
   - **Any of H1-21, H1-63, H2-21, H2-63 passes** → the write-up says so with its Holm p; a
     follow-up plan (separate, not this one) changes the live time exit and the scoreboard
     horizon. No live behaviour changes here.
   - **All four fail** → a one-page keep/cut/stop memo for the owner
     (`docs/research/2026-10-01-keep-cut-stop.md`): keep CFR as a measurement/research tool, cut
     the live cadence, or stop — evidence, cost and consequence of each, and a recommendation.
     No code change to live behaviour; the owner decides. Docs, CLAUDE.md and the TUI no-edge
     line extend to "the screen at 15/21/63 sessions and 12-1 momentum at 21/63".
8. **One run.** If any earlier figure differs from the 2026-09-30 artifact (e.g. a series
   aged past `--cache-age` and was refetched), that is recorded with its cause; there is no
   second run. A run showing unavailable symbols or a shortfall warning is recorded as failed
   evidence and stops the plan for the owner, rather than being retried silently.

## Execution (subagent-driven; controller = me)

**Step 0 — push (controller).** `git diff --stat origin/main..main -- '*.toml' '.env*'` must be
empty; `git diff origin/main..main | grep -i '0x6d616d7574\|contact_email *='` must show nothing
but example/doc placeholders. Then
`git -c credential.helper='!gh auth git-credential' push https://github.com/ObvMamut/ai-financial-researcher.git main`.

**Task 1 — plan + pre-registration (controller), one commit before any code or run.**
- Write `docs/plans/2026-10-01-horizon-test.md` (this plan).
- `docs/workflow/backtest.md`: add "Pre-registered horizon tests (v5, registered 2026-10-01)" —
  H1/H2 table, the bar, ruling 6's p-value definitions and family size, ruling 7's decisions;
  bump the register count to 15. Commit: "v5: pre-register H1/H2 horizon tests and Holm family".

**Task 2 — horizons + H2 (subagent, TDD).** `panel.go` Horizons/XS/BX to 5; `analyze.go`
`cell.ic/bic`, `SignalStats.IC/IR/TNW` to `[5]`; `evaluate(r, cells, mid, lags, f)` with existing
callers passing `nwLags(10)`; new `horizonTests` adding H2-21/H2-63. Text signal table gains
IC21/tNW21/IC63/tNW63. Tests: forward XS/BX at 21/63 against a hand-built series, NaN tail,
look-ahead test still green, C-series figures unchanged on the existing fixtures.

**Task 3 — H1 horizon book (subagent, TDD).** New `internal/backtest/horizon.go`:
`topPicks(recs)` shared with `pickTrades`; `horizonBook(recs, h, mid)` → per-date net series,
halves, regions → `TestResult` via the same bar (refactor `evaluate`'s bar check into a helper
both use, rather than duplicating it). H1-21, H1-63 as tests; H1-15 as comparison. `Result.Horizon`
block (`horizon` JSON key) with per-horizon mean gross/net, weeks, and the tests; text section
"=== v5 horizon tests (H1, H2) ===". Count `Horizon.Tests` in `TestsRun`. Tests: hand-computed
two-date panel, region leg flips a verdict, H1-15 not counted.

**Task 4 — Holm (subagent, TDD).** New `internal/backtest/holm.go`: `TestResult.P`, `PHolm`
(`p`, `p_holm` JSON), set per ruling 6 in `evaluate`/`applyScopedBar` (sign-aware), E2 paired
tests, `e1Decision` (binomial), `e3Decision` (1). `Result.MultipleTesting` (`family_size`, rows
sorted by p) and a text section. Tests: Φ at known points, Holm on a textbook example
(monotonicity, cap at 1), E1 5-of-11 → p = 0.7256, family size = tests_run.

Each task: spec-compliance review then code-quality review subagent; gofmt, `go vet ./...`,
`go test ./...` clean before the next task.

**Task 5 — the one run (controller).**
`go build -o <scratch>/cfr ./cmd/cfr`, then from repo root
`env -u ALPHAVANTAGE_API_KEY -u FRED_API_KEY -u DEEPSEEK_API_KEY -u CFR_API_KEY <scratch>/cfr backtest --years 10 --json > docs/research/2026-10-01-evidence/horizon-10y.json 2> docs/research/2026-10-01-evidence/run-log.txt`.
Check: 0 unavailable, no `shortfall`, 119 filings resolved, `tests_run` 15, family_size 15;
`jq -S` comparison of `preregistered`, `decisions`, `book_grid`, `per_year`, `sides`, `barrier`,
`us_scoped` (minus new `p`/`p_holm` fields) and the first three entries of each signal array
against `docs/research/2026-09-30-evidence/lab-drift-10y.json`.

**Task 6 — write-up and sync (controller + subagent).**
- `docs/research/2026-10-01-lab-horizon.md`: run checks, H1/H2 table, H1-15 reference, Holm
  table (15 rows), every number cited `json:<line>`.
- `docs/workflow/backtest.md`: results section "Run of 2026-10-01", register status lines.
- Apply ruling 7: memo (all fail) or follow-up pointer (any pass).
- `CLAUDE.md` backtest map + Stage 0.5 paragraph, `internal/tui/results.go` `screenNoEdgeLine`
  and `results_test.go` updated to match the outcome.
- Update memory note `cfr-edge-evidence-2026-09`.

**Task 7 — final review.** Whole-branch code review subagent; `superpowers:verification-before-completion`;
commit. No further push unless asked.

## Verification

- `gofmt -l .` empty, `go vet ./...`, `go test ./...` — output shown.
- The run's artifact shows 0 unavailable, no shortfall, `tests_run: 15`, `family_size: 15`.
- Earlier-figure equality check (Task 5) passes, or the difference is recorded with its cause.
- Every number in the write-up and memo is traceable to a `json:<line>` in the artifact.

## Status

| Step | State |
|---|---|
| 0 push | done: origin/main = e4a3565 |
| 1 pre-registration | done (this commit) |
| 2 horizons + H2 | pending |
| 3 H1 horizon book | pending |
| 4 Holm | pending |
| 5 the one run | pending |
| 6 write-up and sync | pending |
| 7 final review | pending |
