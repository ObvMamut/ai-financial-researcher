# Point-in-time index membership

`sp500_changes.csv` and `nq100_changes.csv` are the S&P 500's and the
Nasdaq-100's membership on every date since 2015-01-01.
`internal/universe/history.go` loads it, and the lab's point-in-time universe
reads it. It is data, so rebuild it rather than editing rows.

## Rebuild

```sh
curl -s -A "cfr" -o list.wiki "https://en.wikipedia.org/w/index.php?title=List_of_S%26P_500_companies&action=raw"
curl -s -A "cfr" -o hist.wiki "https://en.wikipedia.org/w/index.php?title=Historical_components_of_the_S%26P_500&action=raw"
python3 -I wikichanges.py hist.wiki > changes.json
python3 -I build_sp500.py list.wiki changes.json sp500_renames.csv > sp500_changes.csv
go test ./internal/universe
```

Wikipedia's changes table does not record ticker changes. Those go in
`sp500_renames.csv`, one dated row each. Date each one from the old symbol's
last bar on Alpaca.

## Validation (built 2026-10-07)

**Against today's list.** Replaying the 273 changes and 36 renames forward from
the 2015 baseline reproduces today's 503 constituents exactly. Membership stays
between 501 and 505 on every date.

**Against hanshof/sp500_constituents** (MIT snapshot file, tickers as traded):

| date | ours | hanshof | only ours | only hanshof |
|---|---|---|---|---|
| 2016-06-30 | 505 | 479 | 28: AA AGN BHI CHK DNB DO DOW DPS EMC FOX FOXA GAS HAR HCP HOT IR LLL MNK MON PX RIG SE SIG TYC URBN WYN XL YHOO | CPRI KDP |
| 2018-06-29 | 505 | 496 | 11 | CPRI KDP |
| 2020-06-30 | 505 | 501 | DOW FOX FOXA IR | — |
| 2022-06-30 | 503 | 498 | CEG DOW FOX FOXA IR | — |
| 2024-06-28 | 503 | 503 | — | — |

- **Why hanshof is not the source.** Every name in "only ours" is a real member that
  hanshof omits, and those are mostly companies that later left the index (CHK, DO,
  RIG, MNK, YHOO). Those departures are exactly what a survivorship-free universe
  has to keep.
- **Hanshof's own errors.** Its two extra names, CPRI and KDP, are tickers it uses
  before they existed; the companies traded as KORS and DPS at the time.

**Pricing.** Alpaca's bars API takes an `asof` date that resolves a symbol to the
company that held it then. FB as of 2020 is Meta, and FI as of 2018 is Frank's
International rather than Fiserv. So the lab prices each interval with `asof` set
to that interval's last day, when its ticker was certainly valid.

## Nasdaq-100

`nq100_changes.csv` is built by `build_nq100.py` from jmccarrell/n100tickers
(MIT). That project publishes yearly files, each giving January 1 membership
plus every dated change. The build checks each year's January 1 list against
the previous year replayed through its changes, and all 2015–2026 lists match.
Membership stays between 101 and 107 tickers.

Spot-checks match the public record:
- AZN replaced XLNX on 2022-02-22.
- The 2023-12-18 reconstitution added CCEP, CDW, DASH, MDB, ROP and SPLK, and
  removed ALGN, EBAY, ENPH, JD, LCID and ZM.
- FB became META on 2022-06-09.

The project's own `nq100.csv` sample contains 11 names that are not current
members (NET, SNOW, OKTA, TTD and others). It was chosen as a representative
sample, not as the membership list.
