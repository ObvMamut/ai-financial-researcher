# Universe

The tradeable universe is four indices. Constituent lists live as CSVs in
`internal/universe/data/` and are loaded by `internal/universe`.

| Key      | Index            | Approx. size | Notes                                  |
|----------|------------------|--------------|----------------------------------------|
| `sp500`  | S&P 500          | ~500         | US large cap                           |
| `nq100`  | Nasdaq 100       | ~100         | US tech-heavy (overlaps SP500)         |
| `eu50`   | EuroStoxx 50     | ~50          | Eurozone blue chips                    |
| `asia100`| Asia 100 (broad) | ~100         | Curated pan-Asia large caps            |

The `nq100` sample (56 names) holds only current Nasdaq-100 members per
`data/history/nq100_changes.csv` (enforced by `TestNQ100SampleHoldsOnlyCurrentMembers`).

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

  A merge is not a discard. Two scouts nominating the same name **in the same direction**
  is the strongest agreement this stage produces and raises `Candidate.Nominations`; two
  nominating it in **opposite** directions records the other index in `Candidate.Contested`
  and keeps the first-seen reading. Both used to vanish into "whichever came first", which
  is how QCOM shipped bearish on 2026-09-03 — nominated `+1.770` bullish by nq100 and
  `−1.596` bearish by sp500, resolved by iteration order. `meritScore` reads both.
- `CapMerit(candidates, max, maxPerIndex, score)` — trim the merged shortlist to at most
  `max` names (config `max_shortlist`, default 12), keeping the highest-scoring
  nominations and letting no index contribute more than `maxPerIndex` (default 5) before
  a pure-merit backfill fills any slots the cap left empty. `score` is supplied by the
  caller — the orchestrator aligns each candidate's Stage 0.5 composite with the direction
  it was nominated in — so this package holds no scoring policy. It replaced
  `CapBalanced`, whose round-robin treated "first name the scout typed" as a ranking.
  The result is always ordered best-first, including when nothing needed trimming.
- `Lookup(ticker)` — resolve a user-entered ticker to a known constituent (single-stock mode).

### Symbol spelling per source

The CSVs carry the canonical symbol every source but one agrees on. Yahoo's chart endpoint
is the exception for **US class shares**: it writes `BRK-B`, not `BRK.B`, and 404s the
dotted form. `marketdata.yahooSymbol` converts on the way out — a dot followed by a known
*exchange* suffix (`ASML.AS`, `7203.T`) is a foreign listing and is left alone. Only
Berkshire needs this today; every run silently lost it until the universe-wide pre-screen
made the 404 visible.

## Refresh

Constituent lists drift over time. They are static CSVs checked into the repo; refreshing
them is a manual/periodic task (out of scope for the core app). Document the source used
when updating a CSV in a comment header line (`# source: ... as of YYYY-MM-DD`).

## Foreign listings and US lines

Half of an all-indices shortlist is non-US by construction, and every per-ticker
provider here is US-only: SEC EDGAR has no filings for a Taipei listing, and
AlphaVantage rejects a dotted symbol outright ("Invalid ticker format:
000660.KS") — a rejection that still costs one of the 25 daily requests.

`internal/marketdata/data/adr_map.csv` maps a foreign primary listing to the US
symbol that trades the same company, and the providers ask under that symbol.
2330.TW is fetched as TSM, 9988.HK as BABA, LIN.DE as LIN (a genuine 10-K filer).
Facts stay keyed to the ticker the run asked about and carry a "US line: TSM"
note, so an agent never mistakes ADR coverage for local-market coverage.

Only **major-exchange (NYSE/NASDAQ)** lines are mapped, and the `venue` column
records the audit that decided it. Widening the map to the sponsored OTC ADRs
covering most of the remainder — BAYRY, BMWYY, TOELY, SFTBY, TCEHY — was
specified, probed against live Alpaca on 2026-09-03, and abandoned on the
evidence: **68 of 77 candidate OTC lines returned zero headlines over 21 days**,
and the nine that returned any averaged three. A mapping is not free — `Reachable`
stops counting the name as structurally quant-only — so a row yielding no news
converts an honest coverage gap into a domain scoring a name on nothing. OTC bars
could not have validated those rows either: `feed=otc` answers HTTP 403,
"subscription does not permit querying OTC data".

That audit also caught a row that had gone stale in the other direction.
Telefonica's NYSE ADR stopped trading on **2026-01-16** and the issuer filed to
suspend its SEC reporting obligations four days later, so `TEF.MC` had been
resolving to a dead symbol while EDGAR still served its pre-deregistration
filings as though current. It is now venued `DELISTED`: the row stays in the file
as documentation and the loader does not resolve it, so re-adding it from a stale
reference trips a test rather than the pipeline. The same pass added `2412.TW` →
`CHT`, a live NYSE line (168 sessions in 2026, 17 headlines) the map had missed.

Names with no US line at all (Samsung, SK Hynix, TCS, the .BK and .SI rows) stay
quant-only and the run says so.

Section 16 exemption is decided on the **resolved** symbol, not the requested
one. ASML sits in `nq100` as the bare US line and in `eu50` as `ASML.AS`, so one
issuer enters under two spellings; testing the request exempted the dotted one
and handed the other "no Form 4 filings in the last 45 days" read as *no signal*.
The map's own `us_line` column is the foreign-private-issuer registry that
separates them.

`marketdata.Reachable(ticker)` is the single question the coverage bookkeeping
asks — "can any per-ticker provider get anything for this name" — and
`marketdata.IsForeignSuffix` is the one suffix table, shared with this package's
cross-listing dedupe. Macro is the exception: FRED's series describe the US
economy, and there is no ADR equivalent for a backdrop, so macro grounds US
listings only.
