# N1–N3 on the point-in-time lab: all three fail (2026-10-08)

This was the registered last round: three published price-only anomalies the composite does not
contain (MAX, IVOL and FIP), run once on the survivorship-free US universe. All three fail the
registered bar. Under the decision fixed at registration, the owner memo moves to option 2 and
live runs are suspended.

## The run

- **Command:** `cfr backtest --universe pit --indices sp500,nq100 --years 9 --json`, built from
  `513ddde` and started 2026-10-08 10:11Z.
- **Isolation:** run with `env -u` for AlphaVantage, FRED, DeepSeek and CFR_API keys.
- **Evidence:** `2026-10-08-evidence/anomalies-pit-9y.json` and `.log`. The lab's own copy is
  `.data/backtest/2026-10-08T10-11-36.json`.

**Checks:**
- Window 2017-09-29..2026-09-25, 470 dates and 283,330 rows (json:4-12).
- `universe: pit`, `prices: 842 symbols, 0 unavailable`.
- No shortfall.
- `anomalies.register_family.family_size` 21.

**Universe change.** `DOW@2019-04-02` is now priced. The PIT retry from its interval start
(b4a417f) supplies its bars, and the S&P count is 692 names against 691 on 2026-10-07.

**The single run.** This was the only run. There was no real-data backtest between the signals
entering the code (3f2ba7f) and this run.

## Results (bar: Newey-West t > +2.5 with 5 lags, positive in both halves)

| Test | Mean β-adj. IC21 | NW t (5) | n | H1 / H2 | p | Holm p (m = 21) | Result |
|---|---:|---:|---:|---|---:|---:|---|
| N1 MAX (−max daily return, 21 sessions) | +0.0202 | 1.66 | 467 | +0.0322 / +0.0080 | 0.049 | 0.879 | **Fails**: t below 2.5 |
| N2 IVOL (−market-model residual σ, 63 sessions) | +0.0284 | 2.13 | 467 | +0.0357 / +0.0210 | 0.017 | 0.333 | **Fails**: t below 2.5 |
| N3 FIP (−mom12_1·ID) | +0.0062 | 0.45 | 467 | −0.0028 / +0.0153 | 0.328 | 1 | **Fails**: t below 2.5 |

Source: json:8705, 8727, 8749 (`anomalies.tests`) and json:8784-8864 (`register_family`).

**Holm.** The register family has 21 tests, and its smallest Holm p is 0.223 (H1-63, p 0.0106 × 21). Among the new tests, the smallest is 0.333 (N2). No p on the
register is below 0.05/21. To survive Holm at 5%, a test needs t > 2.82; the closest new result,
N2, has t 2.13.

**Low-volatility disclosure, as registered.** `lowvol`, the total σ over 63 sessions that was
printed before, has a β-adjusted IC21 of +0.0300 at t 1.91 in this run. N2, the residual σ, is
+0.0284 at t 2.13. The two point the same way: low-risk names did modestly better net of β. Both
are positive in both halves, and neither clears the bar.

**Honest prior.** All three are monthly-horizon effects that are documented to have decayed after
publication. The round confirms it on this universe. MAX and IVOL keep the published sign at
about two thirds (N1) and 85% (N2) of the bar's t. FIP shows nothing.

## The recomputed in-report figures (not re-decided)

The run also recomputed the 11 in-report tests. Against 2026-10-07 (`pit-9y.json`), t moves by
0.03 or less, except for E2's sector-cap book (E2-3 −0.56 → −1.19, E2-off 0.00 → −0.10, E2-1
1.50 → 1.51):

| | C1 | C3 | C4 (PIT-IC10) | H1-63 (PIT-H1-63) | H2-21 | H2-63 |
|---|---:|---:|---:|---:|---:|---:|
| 2026-10-07 | 1.99 | 1.08 | −0.30 | 1.30 | −0.11 | −0.05 |
| 2026-10-08 | 1.97 | 1.08 | −0.29 | 1.29 | −0.11 | −0.05 |

**Causes.**
- DOW is now priced.
- v8 replaced 11 nq100 rows. The PIT universe takes a member's sector from today's sample first,
  so some sector labels moved. That reaches the sector-capped E2 books and `indmom` only.
- Alpaca's refetch of every series may contribute too.

The registered PIT results of 2026-10-07 stand as decided.

**E3.** E3's rule fires again: the short side loses β-adjusted, −0.567% / −1.003% by half. As on
2026-10-07, it is not a registered decision on a 9-year US run. E3 was decided on the 10-year
three-region run, where it did not trigger, so no test follows.

## Decision applied

The registered decision for all three failing is applied: the memo moves to option 2, and live
runs are suspended by owner decision. The register now holds 21 tests, and none has passed.
OOS-H1-63 is the only open question, and it cannot be evaluated before about 2027-12.
