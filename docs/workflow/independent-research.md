# Workflow: Independent Research

Goal: from a universe of 274 names (curated index samples, see `universe.md`), produce
**5 ranked swing-trade ideas**.

A funnel keeps cost bounded: an in-process quant pre-screen ranks the whole universe,
cheap screening filters that ranking, a fixed set of specialist agents analyze only the
shortlist, then Claude synthesizes.

## Stage 0.5 — Universe-wide pre-screen (in-process, no model)

`internal/orchestrator/prescreen.go` fetches ~2 years of daily OHLCV for **every**
constituent of the selected indices (plus each index benchmark) and computes the
`internal/quant` metrics for all of them. Cold, that is ~275 Yahoo requests at the
client's ~4/s pace (≈70s); warm, it is served from the shared data cache and costs
nothing. No model is called.

**Composite**, z-scored *within each index* so a quiet index still nominates its own
leaders instead of losing every slot to the strongest one:

```
score = z(mom12-1) + 0.5·z(ret63d) − 0.5·strZ
```

The last term applies **only when the recent move runs with the trend** (`sign(strZ) == sign(mom12-1)`). That is the short-term
reversal case: a name that has just spiked on top of an uptrend gives the spike back,
while an uptrend that has just dipped is a pullback entry. The rule is symmetric, so a
name that has already collapsed is penalised as a short for the same reason.

Note that `strZ` enters **raw**, while the other two terms are z-scored within the index.
That is not an inconsistency: `strZ` is already a z-score — the trailing 5-day return
standardised against that name's *own* one-year distribution of 5-day returns — so it is
unit-free and on the same scale as the others. Standardising it a second time across the
index is what the term used to do, and it broke the sign the gate had just tested: in a
broad rally the index mean of `strZ` is positive, so a mildly extended name has a negative
cross-sectional z and `− 0.5·z` paid it a *bonus* for extending. On the 2026-09-01 universe
the gate fired on 97 names and 5 of them were rewarded rather than charged.

The composite is a **signed long ranking**: high means a strong long, low means a strong
short.

There is no separate relative-strength term, because standardising within the index *is*
the relative-strength adjustment. The formula used to read
`z(mom12-1) + 0.5·z(ret63d) + 0.5·z(rs63)` with `rs63 = ret63d − benchmark ret63d`, but the
benchmark return is fetched once per index and these z-scores are taken within an index, so
every member had the same constant subtracted — and a z-score is invariant to that.
`z(rs63) ≡ z(ret63d)` identically: the 2026-09-01 artifact has `ret_63d − rs_63` taking
exactly one distinct value per index, to nine decimals. The third signal was the second one
counted twice, so the real weight on the 63-day return was 1.0 rather than the documented
0.5. `RS63` remains a displayed column, because it reads more directly than a z-score, but
it cannot earn its own term.

Each term is **winsorised at the 2nd/98th percentile within the index** before
standardising. Cross-sectional factor scores are routinely decided by one extreme value:
on 2026-08-28 Micron's 12-1 momentum read +644% against an S&P sample whose next-best was
+74%, scoring it +7.0 — three times the runner-up — while inflating the standard deviation
enough to squash every other name toward zero. Clipping the tails keeps the outlier ranked
first, where it belongs, without letting it set the scale for the other ninety-odd names.

**Hard exclusions** (dropped before scoring, and left out of the mean and standard
deviation so they cannot distort other names' z-scores):

| Rule | Reason |
|---|---|
| `AvgDollarVol20USD < risk.adv_min_usd` (default $20M) | A swing position sized off a real account cannot be entered or exited in it. |
| Turnover known but no FX rate for its currency | Tradeable size cannot be established, so the name is not a candidate. |
| `Bars < 60` | Too little history for the momentum term to mean anything. |
| No price history at all | Fetch failed; recorded as an error and an excluded row. |

The floor is compared against **converted** turnover. `AvgDollarVol20` is `Σ close·volume`
in the listing's own currency, and comparing it to a dollar floor excluded exactly one name
out of 274 on 2026-09-01 while asia100's median read "$10,015M" — SK Hynix's ₩6.76tn
printing as "$6,760,821M". A Tokyo name turning over ¥20M a day, about $135k and
untradeable, cleared a $20M floor by a factor of 150. Rates come from Yahoo's keyless FX
pairs, one request per distinct currency per run; London's GBp is divided by 100.

`volTrend > 1.5` is flagged, not excluded: expanding volatility makes the level
arithmetic downstream unstable and the scout is told to say so.

Every constituent gets a row — including excluded ones — in `runs/<ts>/prescreen.json`,
together with the parameters and the formula string. The price series themselves stay in
the shared data cache rather than filling the run directory with a few hundred files;
Stage 1.5 reads back the dozen it needs for free.

## Stage 1 — Screening (Scouts)

- One **Scout** subprocess per index: SP500, NQ100, EU50, Asia100 (4 parallel).
- Each scout receives the **pre-screen table** for its index — the top
  `prescreen_top_per_index` rows (default 15) plus the bottom 5 as short candidates, with
  every measured column — followed by the full constituent list. It returns **~5–10
  nominations** with a one-line reason and a bias, each reason citing a column from the
  table. Names outside the table may be nominated only on reasoning that does not depend
  on price data the scout was not given.
- **Nominations are validated against the index's own constituent list.** A symbol that
  is not in it is dropped and logged: a hallucinated ticker used to reach the shortlist
  and consume a data fetch and an analysis slot on a company the run never screened.
  Ticker spelling, company name, sector and index are then taken from the universe row,
  not from the model.
- The orchestrator **merges and dedupes** the four shortlists (exact repeats and
  cross-listings like `ASML`/`ASML.AS` collapse, preferring the unsuffixed primary
  listing), then trims to `max_shortlist` (default 12) by **merit**
  (`universe.CapMerit`): each nomination scores its pre-screen composite *aligned with the
  direction it was nominated in* — `+score` for bullish, `−score` for bearish, 0 for
  neutral or for a name with no computed row. At most `max_per_index` (default 5) names
  come from one index; if that leaves slots empty they are backfilled in pure score
  order, so a single-index run still returns a full shortlist.
- The final shortlist is recorded in `runs/<ts>/shortlist.json`, carrying each name's
  sector and the scout's bias and reason.

Persona: `agents/scout.md`. Output: see `output-schema.md` (shortlist schema).

## Stage 1.5 — Price data & quant metrics (in-process, no model)

For every shortlisted ticker the orchestrator fetches ~2 years of daily OHLCV from the
keyless Yahoo Finance chart API (`internal/marketdata/yahoo.go`, rate-limited + cached
daily) and computes the statistical pack in `internal/quant`: return ladder, 12-1
momentum, price-to-52-week-high, turnover-conditioned short-term-reversal z-score,
Yang-Zhang volatility (20d/60d), Lo-MacKinlay variance ratios + regime tag, drawdown/tail
shape, dollar volume, beta vs the index benchmark, and vol-scaled stop/target distances
(k·σ_daily·√h). Raw series land in `runs/<ts>/prices/<ticker>.json`, metrics in
`runs/<ts>/quant.json`. Fetch failures degrade that ticker to ungrounded — never abort.

## Stage 2 — Deep analysis (5 specialists, parallel)

Each specialist produces **one report covering the entire shortlist** (5 subprocess calls
total — not per ticker):

| Specialist    | Persona                  | Focus                                             |
|---------------|--------------------------|---------------------------------------------------|
| News          | `agents/news.md`         | Headline flow + the verified earnings calendar    |
| Fundamentals  | `agents/fundamentals.md` | Filed figures + Go-computed market cap / P/E / P/S |
| Quant         | `agents/quant.md`        | Interprets the computed statistical pack (no TA)  |
| Sentiment     | `agents/sentiment.md`    | Insider Form 4 filings + option positioning       |
| Macro         | `agents/macro.md`        | Computed benchmark regime + the FRED backdrop     |

The quant specialist receives the full computed pack as ground truth; news and sentiment
get compact verified price lines so their narratives stay anchored. The news pack also
carries a **verified next-earnings date** per name, fetched as a single bulk
`EARNINGS_CALENDAR` request covering the whole shortlist (one of the free tier's 25 daily
requests, cached per UTC day). The persona may state a date **only** if it appears there:
previously it was asked for earnings dates it had no way to know, and on a search-less
engine it supplied plausible ones from memory. An unresolved earnings date inside the
window caps that ticker's news strength at 5, and the validator flags any idea whose
holding period spans one without acknowledging it.

Sentiment has its own sources. It previously read AlphaVantage's news-sentiment scores —
the same call the news domain makes — so two of five nominally independent domains agreed
with each other by construction. It now reads **SEC Form 4** insider filings (keyless;
open-market purchases and sales only, with grants, option exercises and tax withholding
excluded as compensation mechanics) and the **Yahoo option chain** (put/call open interest
over the front two expiries, plus ATM implied volatility to compare against the computed
realized vol). Both are merged into one pack — `BuildPack` no longer stops at the first
provider that answers. The options endpoint is intermittently crumb-gated; a 401 degrades
sentiment to insider filings alone and is recorded, never guessed around.

Fundamentals gets the price context and **computed multiples**. EDGAR now also extracts
shares outstanding (a `dei` cover-page fact, not a GAAP one), diluted EPS (quoted in
USD/shares) and year-over-year revenue growth from the two freshest annual frames; the
orchestrator divides those against the verified last close to produce market cap, P/E and
P/S, each line showing its own inputs. Where the filed figure is more than 400 days older
than the price, the block says the multiple is **not computable** and why — an absent
number gets filled in from recollection, a stated refusal does not. The persona caps
strength at 4 on figures older than 13 months.

Macro is scoped to the regime. `quant.Pack.Benchmarks` computes the same metrics for each
index benchmark the run touched — series that were already fetched for beta and relative
strength and then never read as a market signal — and `RegimeBlock()` renders them into the
macro prompt and the Chief's quant reference. The persona is capped at strength 5 unless a
dated, verified driver names the ticker's sector: a regime is a backdrop shared by every
name on the shortlist, so twelve names scored 7 is twelve counts of the same fact. Every specialist — and
the Chief Analyst — receives the shortlist as a block carrying each name's sector, source
index, and the scout's bias and reason, so a domain can confirm or contradict the thesis
the name was nominated on instead of describing the company from scratch. Each report
scores every shortlisted ticker on its domain (bias + strength) so the Chief Analyst can
measure confluence.

## Stage 3 — Synthesis (Claude Chief Analyst)

Persona: `agents/chief-analyst.md`. Reads all 5 specialist reports, the **computed base
scores** (the weighted domain confluence, already calculated — see `scoring.md`) and a
compact verified quant reference; adjusts each base by at most `chief_adjust_band` points
with a named reason, ranks, and emits the **top 5** ideas — including entry/stop/target derived from the
vol-scaled distances — as a fenced ```json block (schema in `output-schema.md`). Go parses
it into `[]model.TradeIdea`, validates the mechanics, and the TUI renders the results.

## Flow summary

```
Stage 0.5: Yahoo OHLCV for all 274 constituents → quant composite, ranked
           per index (no model call) → runs/<ts>/prescreen.json
        │
4 Scouts (parallel), each screening its index's ranked table
        │
merged shortlist (validated, deduped, trimmed to 12 by merit)
        │
Stage 1.5: quant metrics for the shortlist (cache hits from Stage 0.5)
        │
5 Specialists (parallel, each covers shortlist; quant gets the computed pack,
              news gets the bulk earnings calendar)
        │
computed base scores (weighted domain confluence, no model call)
        │
Chief Analyst (Claude) → top 5 ideas (direction, confidence, entry/stop/target, why)
        │
validation: confidence clamped to base ± band, levels, events, attribution
```

## Quality / degradation

- If the pre-screen cannot price a name, that name is an excluded row with a recorded
  error; if it cannot price anything for an index, that scout falls back to the plain
  constituent list rather than being handed an empty table.
- If a scout fails, the run proceeds with the remaining indices.
- If a specialist fails, the Chief Analyst is told; the confidence effect is automatic —
  weighted coverage caps the computed base score for every name that domain would have
  covered.
- If the earnings calendar cannot be fetched, the run keeps its headlines and records the
  failure; no name gets a guessed date.
- Minimum to produce results: at least 2 specialist reports and a non-empty shortlist.
