# Survivorship-free universe: feasibility (2026-10-07)

**Question** (memo item 2, `2026-10-01-keep-cut-stop.md`). Is there a keyless,
point-in-time source of index membership that can remove the survivorship bias
inflating every positive number on the register? H1-63 is the one most exposed.

**Answer.** Yes for the S&P 500 from 2016 onward. The membership list was never the
blocker. Price history for names that later left the index was, and Alpaca has it.
Nasdaq-100 is likely feasible too, but I have not checked it. STOXX 50 and the Asia
sample have no free source that I found.

## Membership

| Source | Coverage | Access | Spot-check |
|---|---|---|---|
| [hanshof/sp500_constituents](https://github.com/hanshof/sp500_constituents), `sp_500_historical_components.csv` | S&P 500, 1996-01-02 → **2025-08-23**. One row per change date, 3,482 rows, median 467 names per row | MIT, keyless raw download (6.8 MB). Current composition comes from Wikipedia | All 9 checked dates match (below) |
| [jmccarrell/n100tickers](https://github.com/jmccarrell/n100tickers) | Nasdaq-100, 2007-02-01 → 2026-09 | MIT, a Python package (`tickers_as_of`) | not checked |
| STOXX 50, asia100 | — | No free point-in-time source found | — |

S&P spot-checks (first and last date the name appears in the file):

| Name | Known event | In the file |
|---|---|---|
| TSLA | added 2020-12-21 | first 2020-12-21 ✓ |
| UBER | added 2023-12-18 | first 2023-12-17 snapshot ✓ |
| PLTR, DELL | added 2024-09-23 | first 2024-09-22 snapshot ✓ |
| CRWD | added 2024-06-24 | first 2024-06-23 snapshot ✓ |
| SIVB | removed 2023-03-15 | first absent 2023-03-15 ✓ |
| TWTR | removed 2022-11-01 | first absent 2022-11-01 ✓ |
| ATVI | removed Oct 2023, after the Microsoft deal closed | first absent 2023-10-14 ✓ |
| FRC | removed after JPMorgan's takeover, May 2023 | first absent 2023-05-07 ✓ |

Known defects:
- The file ends on 2025-08-23. Thirteen months are missing, and they would have to come
  from Wikipedia's "Selected changes" table.
- One malformed entry exists: `RVTY (Previously PKI)`.

## Prices for names that left the index: the real blocker

- **Yahoo** has nothing for delisted symbols. SIVB, FRC, TWTR and ATVI all return "No data
  found, symbol may be delisted". The lab reads Yahoo, so it could never have included them.
- **Alpaca** (SIP feed, the key the project already uses, no new credential):
  - Daily history for each of those four runs from 2016-01-04 to its last trading day:
    SIVB to 2023-03-09, FRC to 2023-04-28, TWTR to 2022-10-27, ATVI to 2023-10-13.
  - Across **all 691 names in the S&P 500 at any time since 2016**, Alpaca returned bars
    for **685 (99.1%)**.
  - Of the 6 misses, five are renamed tickers: BLL (→ BALL), HRS (→ LHX), JEC (→ J), WLTW
    (→ WTW) and FI. A rename map of a few rows fixes them.
  - DOW is not a renamed ticker (corrected 2026-10-08). Dow Inc. has traded as `DOW` since
    2019-04-01 with no corporate actions. Dow Chemical held the symbol until 2017-08-31.
    Alpaca drops it from a large multi-page batch whose window starts in Dow Chemical's
    years, and prices it when the window starts at Dow Inc.'s listing. Asked for alone from
    2016, it returns a splice: Dow Chemical, then zero-volume flat fills, then Dow Inc. The
    PIT loader now retries an empty interval alone from its own start under the same `asof`.
    See `2026-10-07-pit-lab.md`.
- Alpaca's history starts in 2016, so this gives about 9.5 years. The current lab uses 10.

## What a survivorship-free lab would take

1. **Membership loader.** Read the hanshof CSV, plus a hand-kept tail from Wikipedia
   after 2025-08, into a per-date member set. The panel then takes, for each rebalance
   date, the names in the index *on that date*, not today's 98-name sample.
2. **Price routing in the lab.** `internal/backtest/panel.go` fetches through Yahoo. US
   names would go through `marketdata.AlpacaPrices` with an explicit start date, as live
   runs already do. Batching keeps it to a handful of requests.
3. **Ticker hygiene.** Add the rename map, and check for symbol reuse: a symbol later
   given to a different company would splice two histories. Spot-check every name whose
   Alpaca series has a gap of more than 5 sessions.
4. **Scope.** US only (sp500, and nq100 if its source checks out). EU and Asia stay on
   current membership and stay labelled as survivorship-exposed.

**Recommendation.** If the owner keeps CFR as a research tool, this is the one piece of
infrastructure that changes what the register can say. It would turn every US number
from "flattered by survivorship" into a clean estimate. It is a
moderate change confined to the lab (no live behaviour, no models, no new keys).

It must be registered as a new test before it runs:
- **Recommended:** "E1 and H1 on the survivorship-free US universe, 2016–2025".
- **Not allowed:** a re-test of the old numbers under a new label.

The 2026-10-07 out-of-sample registration (OOS-H1-63) does not depend on this, because
its universe is frozen at registration.
