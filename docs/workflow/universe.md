# Universe

The tradeable universe is four indices. Constituent lists live as CSVs in
`internal/universe/data/` and are loaded by `internal/universe`.

| Key      | Index            | Approx. size | Notes                                  |
|----------|------------------|--------------|----------------------------------------|
| `sp500`  | S&P 500          | ~500         | US large cap                           |
| `nq100`  | Nasdaq 100       | ~100         | US tech-heavy (overlaps SP500)         |
| `eu50`   | EuroStoxx 50     | ~50          | Eurozone blue chips                    |
| `asia100`| Asia 100 (broad) | ~100         | Curated pan-Asia large caps            |

## CSV format

`internal/universe/data/<key>.csv`:

```csv
ticker,name,exchange,country,sector
AAPL,Apple Inc.,NASDAQ,US,Information Technology
ASML.AS,ASML Holding,Euronext Amsterdam,NL,Information Technology
7203.T,Toyota Motor,TSE,JP,Consumer Discretionary
```

## Ticker normalization

Symbology differs by venue. Store the **canonical** symbol the research CLIs understand and
keep the exchange/country so agents can disambiguate:

- US: plain symbol (`AAPL`, `MSFT`).
- Europe: exchange-suffixed where needed (`ASML.AS`, `MC.PA`, `SAP.DE`).
- Asia: numeric + suffix (`7203.T` Tokyo, `0700.HK` Hong Kong, `005930.KS` Korea).

`internal/universe` exposes:
- `Load()` — read all CSVs.
- `Constituents(indexKey)` — names for one index.
- `Dedupe(candidates)` — merge overlaps by canonical ticker, including **cross-listings**:
  the same company nominated under a known exchange suffix and unsuffixed (`ASML` +
  `ASML.AS`) collapses to the unsuffixed primary listing. A stem match alone is never
  enough — the company name must also match (`SAN.MC` Santander vs `SAN.PA` Sanofi stay
  separate, as do class-share tickers like `BRK.B`).
- `CapBalanced(candidates, max)` — trim the merged shortlist to at most `max` names
  (orchestrator uses 12), round-robin across source indices so one index can't dominate.
- `Lookup(ticker)` — resolve a user-entered ticker to a known constituent (single-stock mode).

## Refresh

Constituent lists drift over time. They are static CSVs checked into the repo; refreshing
them is a manual/periodic task (out of scope for the core app). Document the source used
when updating a CSV in a comment header line (`# source: ... as of YYYY-MM-DD`).
