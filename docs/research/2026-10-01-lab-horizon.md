# Lab tests H1–H2: is the holding period the problem?

Plan: `docs/plans/2026-10-01-horizon-test.md`. The four tests, their bar, the Holm family and the
decisions that follow were registered in `docs/workflow/backtest.md` ("Pre-registered horizon
tests (v5, registered 2026-10-01)") in commit `cc5d6ce`, before any of the code that computes them
existed and before the run. This note records the one run and applies those decisions as written.

**Outcome: H1-21, H1-63, H2-21 and H2-63 all fail.**

- **12-1 momentum** does not get stronger at its own monthly horizon. Its beta-adjusted IC21 has
  t 0.68 and its IC63 has t 0.91. Both are *negative* in the US.
- **The composite's top-5 book** comes closer than any test so far at 63 sessions. It earns +2.14%
  net of 30bp per hold, positive in both halves and in every region, but its t is 2.31, below the
  2.5 bar. After Holm over the 15 tests run to date, its p is 0.16.

No test in the family has a Holm-adjusted p below 0.05. The pre-fixed "all fail" decision
applies. The owner's keep/cut/stop memo is `docs/research/2026-10-01-keep-cut-stop.md`, and
nothing in live behaviour changed.

## How the numbers were produced

- **Build.** The binary was built from `bb14aa9`, the head of branch `horizon-test` after Tasks 2–4
  (`go build -o <scratch>/cfr ./cmd/cfr`). A whole-branch code review ran *before* the run, because
  a fix that changed a figure would have needed a second run. The review diffed the base and head
  builds on one synthetic universe and found every pre-existing figure bit-identical. The one
  commit after the run, `6440753`, changes the TUI line and makes four polish fixes. None of them
  moves a figure in this artifact:
  - the upper-tail p is now computed as `erfc`, which differs from the run's values only beyond
    1e-15;
  - a guard that could not fire was removed;
  - a horizon test with no usable date is now marked untestable, and every test here has 508 or
    more usable dates;
  - a comment and a constant name were changed.
- **Command.** `env -u ALPHAVANTAGE_API_KEY -u FRED_API_KEY -u DEEPSEEK_API_KEY -u CFR_API_KEY cfr
  backtest --years 10 --json`, run from the repo root at 2026-10-01T09:57:58Z, so that
  `./cfr.toml` supplied `providers.contact_email` for the SEC filing source.
- **What it touched.** Keyless Yahoo prices, served from the shared cache, and keyless SEC
  submissions JSON. No model was called.
- **Attempts.** There was exactly one invocation. It exited 0, and nothing was retried.
- **Artifacts** (in `docs/research/2026-10-01-evidence/`):
  - `horizon-10y.json` is the stdout JSON. It is identical to the saved
    `.data/backtest/2026-10-01T09-57-58.json` once `generated_at` is deleted (`jq -S`, `diff`).
  - `run-log.txt` holds the start time, the stderr and the exit code.
- **Citations.** Every number below is cited as `json:<line>` for `horizon-10y.json` or
  `log:<line>` for `run-log.txt`.

**Run checks.**

- **Sample.** 521 weekly rebalances from 2016-10-07 to 2026-09-25, with the second half from
  2021-10-01 (json:5–8). There are 136,478 rows (json:9). The requested start was 2016-09-25
  (json:16), the JSON has no `shortfall` key, and no warning was printed.
- **Coverage.** 244 price symbols with 0 unavailable (log:6). 119 US filers were resolved, with 0
  unavailable (log:7). The JSON has no `unavailable`, `filings_note` or `filings_unavailable` key.
- **Counts.** `tests_run` is 15 (json:7602), and the Holm family size is 15 (json:7604). Both
  equal the register.
- **Earlier tests unchanged.** Every earlier figure matches the 2026-09-30 run
  (`docs/research/2026-09-30-evidence/lab-drift-10y.json`). The check compared `preregistered`,
  `decisions`, `book_grid`, `per_year`, `sides`, `barrier` and `us_scoped` with `jq -S`, after
  deleting the new `p`/`p_holm` fields, plus the 5/10/15-session entries of every signal table.
  All are identical. The same cached prices produced the same screen, so the earlier 11 tests'
  statistics in the family are the ones they were decided on.
- **p-values re-derived.** Every p and Holm-adjusted p in `multiple_testing` was recomputed outside
  Go from the row's t, or from E1's year count, under the registered rules. All 15 agree to 1e-9.
- **Survivorship.** The long-history caveat applies (json:17). See the limits section below: it
  weighs more on a 63-session hold than on a 15-session one.

## Results

| Test | Statistic | Mean | NW t (lags) | n | H1 / H2 | US / EU / Asia | Verdict |
|---|---|---:|---:|---:|---|---|---|
| **H1-21** | top-5 book, β-adj. 21-session excess, net | +0.335% | 1.08 (5) | 516 | +0.282% / +0.390% | +0.549% / +0.104% / +0.127% | **Fails**: t |
| **H1-63** | top-5 book, β-adj. 63-session excess, net | +2.136% | 2.31 (13) | 508 | +1.937% / +2.344% | +3.249% / +1.042% / +1.010% | **Fails**: t |
| **H2-21** | mom12_1 β-adj. IC21 | +0.0088 | 0.68 (5) | 516 | +0.0013 / +0.0164 | −0.0074 / +0.0466 / +0.0033 | **Fails**: t, and US sign |
| **H2-63** | mom12_1 β-adj. IC63 | +0.0179 | 0.91 (13) | 508 | +0.0084 / +0.0278 | −0.0121 / +0.0876 / +0.0081 | **Fails**: t, and US sign |
| H1-15 (reference, not a test) | top-5 book, β-adj. 15-session excess, net | +0.144% | 0.67 (4) | 518 | +0.133% / +0.154% | +0.241% / +0.026% / +0.036% | — |

The rows are drawn from json:7462–7483 (H1-21), json:7484–7505 (H1-63), json:7506–7527 (H2-21),
json:7528–7549 (H2-63) and json:7551–7571 (H1-15). The book means are per hold, net of the 30bp
each name pays once. Gross, the book earns +0.444% over 15 sessions, +0.635% over 21 and +2.436%
over 63, with about 20 picks a week (json:7574–7600).

**H2: momentum does not get stronger at its own horizon.** The case for v5 was that 12-1
momentum, the one term with a consistent sign, is a monthly factor being read on a fortnightly
hold. The momentum row of the beta-adjusted table (json:3452) does not bear that out. Momentum's
beta-adjusted IC is 0.013 at 5 sessions (t 1.64), 0.012 at 10 (t 1.18), 0.012 at 15 (t 1.08),
0.009 at 21 (t 0.68) and 0.018 at 63 (t 0.91). The t falls as the horizon lengthens. In the US,
where most of the record and all of the earnings work sit, both longer horizons have the wrong
sign. The positive overall mean comes from EU, at +0.047 and +0.088.

**H1: the 63-session book is the closest miss on the register, and it still misses.** On a
per-session basis the gross book earns slightly more at 63 sessions (2.436% ≈ 0.58% per 15
sessions) than at 15 (0.444%), and holding longer spreads the same 30bp over more return. Net,
every half and every region is positive. But the t is 2.31 against a bar of 2.5. Its one-sided p
is 0.011, and Holm over the 15-test family raises it to 0.159 (json:7608–7615). The composite's
own rank IC at 63 sessions is 0.014 with t 0.79 (json:3390). The top of the ranking therefore does
better than the ranking as a whole, which is what one would also expect from survivorship (below).
US picks earn three times what EU and Asia picks earn (+3.25% against about +1.0%).

## Multiple testing

The family is every test run to date, **m = 15** (json:7603–7729). Holm-adjusted p, sorted:

| Test | Rule | t | p | Holm p |
|---|---|---:|---:|---:|
| H1-63 | one-sided | 2.31 | 0.011 | 0.159 |
| D3 | one-sided | 1.84 | 0.033 | 0.460 |
| C1 | one-sided | 1.44 | 0.076 | 0.982 |
| H1-21 | one-sided | 1.08 | 0.139 | 1 |
| C3 | one-sided | 0.96 | 0.168 | 1 |
| H2-63 | one-sided | 0.91 | 0.181 | 1 |
| H2-21 | one-sided | 0.68 | 0.248 | 1 |
| C4 | one-sided | 0.66 | 0.255 | 1 |
| E2-off | two-sided | 0.82 | 0.413 | 1 |
| D2 | one-sided | −0.03 | 0.512 | 1 |
| E2-3 | two-sided | −0.61 | 0.541 | 1 |
| E2-1 | two-sided | −0.50 | 0.620 | 1 |
| E1 | binomial sign, 5 of 11 years | — | 0.726 | 1 |
| D1 | one-sided | −0.67 | 0.749 | 1 |
| E3 | no statistic | — | 1 | 1 |

**0 of 15 tests have a Holm p below 0.05.** The smallest is 0.159. Holm is reported as
registered, not used as a gate. Even if H1-63 had cleared t 2.5, it would have needed t > 2.71
(p < 0.0033) to survive Holm at 5%. Two raw p-values fall under 0.05 (H1-63 and D3). If no test
had any effect at all, fifteen looks would produce 0.75 of them on average, so two is not far from
what chance alone delivers.

## What these tests do and do not measure

These limits were disclosed before the run, and the result is read with them:

- **Survivorship weighs more at 63 sessions.** The universe is today's constituents. A name that
  later fell out of an index is missing from every past date, so a book of past top-ranked names
  cannot include the ones that went on to collapse. That flatters long-side returns, and the
  flattery compounds with the hold: over 63 sessions there is more time for a doomed name to fall.
  H1-63's +2.14% is therefore a weaker upper bound than H1-15's +0.14%. Some of the gap between
  the book (t 2.31) and the composite's IC63 (t 0.79) may be this, rather than selection.
- **Few independent observations.** 508 weekly books of a 63-session hold overlap about 12.6 deep.
  The 13-lag Newey-West t accounts for that, but it rests on roughly 40 non-overlapping holds.
- **The halves overlap in calendar time at 63 sessions.** A book dated just before 2021-10-01
  realises its return inside the second half's calendar. That was in the registered split and is
  not a look-ahead, because every signal is computed as of its pick date.
- **Cross-listed names are not deduped**, exactly as in E1 and the barrier study.

None of these limits could lift H2's t (0.68, 0.91) to 2.5 or fix its US sign. Survivorship can
only have *raised* H1-63's figure.

## Decision, applied as registered

The rule was: *all four fail → a one-page keep/cut/stop memo for the owner: keep CFR as a
measurement and research tool, cut the live cadence, or stop. No code change to live behaviour;
the owner decides. Docs, CLAUDE.md and the TUI no-edge line extend to "the screen at 15/21/63
sessions and 12-1 momentum at 21/63".*

- **Memo:** `docs/research/2026-10-01-keep-cut-stop.md`.
- **Register:** **15 decision tests**: C1, C3, C4, E1, E2-1, E2-3, E2-off, E3, D1, D2, D3, H1-21,
  H1-63, H2-21 and H2-63. None has passed, and E1 fired. No Holm-adjusted p is below 0.05.
- **Changes made:** `docs/workflow/backtest.md` records the run. `CLAUDE.md` and the TUI's no-edge
  line now name the longer holds and momentum alone. No setting, default, time exit or scoreboard
  horizon changed.
- **Not triggered:** no test passed, so no follow-up plan changes the live time exit or the
  scoreboard horizon.
- **Not to be done:** re-running H1-63 or D3 in a variant on this data would be fitting to noise,
  the same rule the plan applied to D3. Either can only be confirmed on data this lab has not
  seen. Two such sources exist: weeks that have not happened yet, or a survivorship-free
  universe, which is C5's still-missing input.
