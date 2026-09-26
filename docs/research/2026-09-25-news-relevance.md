# News relevance (A2): what changed and what it did to shipped history

Plan: `docs/plans/2026-09-25-inputs-and-lab-before-edge.md`, finding F3, task A2.

## The defect

Alpaca's news query is filtered server-side by `symbols=<ticker>`, so every item it returns already
carries the requested ticker in its `symbols` list. `relatesTo` then confirmed exactly that membership
and called it `Related`. Tag membership and "the company is the subject of this piece" are different
claims: SAP.DE's four "tagged" items on 2026-09-24 (`runs/2026-09-24T12-58-48/data/news.json`
`ByTicker["SAP.DE"]`) were three Benzinga premarket notes about AMD, NVIDIA and Micron (each names SAP
once, deep in the body, as a software name an AI pullback could rotate into) and a European-market-close
wrap that tags a dozen companies in one summary table. None names SAP in its headline or summary. Alpaca
called all four coverage; the news specialist scored SAP.DE 0 on them; that non-empty `domain_scores["news"]`
entry is what carried SAP.DE past the evidence floor (`checkPriceOnlyEvidence`, `riskgate.go`), which only
checks whether a domain scored the name at all, never what it read.

## The fix

`internal/marketdata/newsfilter.go` now decides `Related` (renamed in spirit, not in the field itself, to
"is this item's subject the company") with `isSubjectRelevant`: an item counts as coverage only if its
headline or summary names the ticker root (`SAP.DE` → `SAP`), the ADR symbol (via the existing `adr.go`
mapping), or the company name (the universe CSV's own name column, normalized: legal suffixes — SE, AG,
Inc, Corp, N.V., Holdings, Ltd, plc, and so on — and a trailing share-class token — "Alphabet Inc. Class A"
— stripped), **or** its tag list carries three symbols or fewer. Tag membership is still required first:
dropping it would let a short tag list on a completely unrelated story (the Namibia/oil search-fallback
case `TestYahooNewsRefusesAFeedThatTagsNothingToTheTicker` already guards against) count as coverage for
whichever ticker happened to be fetched. A short root or ADR symbol (≤2 letters — "ON", "A", "T") only
counts spelled the way a ticker actually is (case-sensitive, which also catches a cashtag like `$ON` for
free); a plain case-insensitive match would tag "shares moved **on** Tuesday" to ON Semiconductor.

The rule is shared by both feeds (`alpacanews.go:130`, `yahoonews.go`), as the controller ruling required.
The company name is not reachable inside `internal/marketdata` directly — `internal/universe` already
imports `marketdata` for `IsForeignSuffix`, so importing back would cycle — so the orchestrator wires a
`ticker → name` lookup into the same `context.Context` that already threads down to every provider's
`Fetch` call (`marketdata.WithCompanyNames`, wired once in `internal/orchestrator/orchestrator.go` right
after the universe loads, covering both the legacy and thesis research modes). Every existing test, and
`internal/marketdata/research_capture.go`'s frozen-snapshot capture used by `research-pair` (which
collects before an orchestrator run exists to wire anything into), run on an unwrapped context and
degrade to root/ADR matching only — no company-name match, same as no run had before this feature.

AlphaVantage already decides its own per-article, per-ticker relevance: `articlesFor`
(`alphavantage.go`) keeps only feed items carrying a `ticker_sentiment` entry for the requested ticker
and records its `relevance_score`, used both to sort headlines and to weight the aggregate sentiment. Per
the controller ruling ("if AV already has a per-ticker relevance score it uses, leave it and say so"),
this task makes no change to `alphavantage.go`. It is a real, if unenforced, gap in the same direction as
F3: `articlesFor` applies no relevance *threshold*, so a very low-relevance mention could in principle
still flip an AlphaVantage-only ticker's coverage to true the way SAP.DE's tag membership did. No run in
the acceptance sample below shows this happening (every AlphaVantage-grounded idea's headlines carry
`relevance 1.00`), but it is a possible future finding, not something this task closes off.

## Acceptance analysis: re-deriving shipped history

**Method.** A throwaway Go test (`internal/orchestrator`, not committed — gated behind
`CFR_NEWS_RELEVANCE_RUNS_DIR`, skipped otherwise) walked every run under `runs/` that has both
`ideas.json` and `data/news.json`. For each shipped idea whose `domain_scores` included a `news` entry:

1. If any of the ticker's saved news facts is an AlphaVantage headline (label carries `relevance` —
   the shape `articlesFor` produces, distinct from Alpaca/Yahoo's `tagged to this ticker` /
   `surfaced by search, not tagged to this ticker` labels), coverage does not flip: AlphaVantage is
   unchanged by this task.
2. Otherwise, every fact the old rule labeled `tagged to this ticker` was re-tested: does its headline
   or summary (the same two fields `isSubjectRelevant` reads) name the ticker root, the ADR symbol
   (`marketdata.USLine`), or the company name (`universe.Load().Lookup(ticker).Name`, normalized by a
   local copy of `normalizeCompanyName`)? If none does, the ticker's news coverage flips to uncovered.
3. For a flipped ticker, `news` was dropped from a copy of its `domain_scores` and the real
   `checkPriceOnlyEvidence` (`internal/orchestrator/riskgate.go`, unexported, called directly since the
   test lives in the same package) was run against what remained, to answer whether the idea would now
   fail the evidence floor. The floor does not apply in single-stock mode, matching production
   (`applyRiskGate`); the two single-stock runs in the sample had no `domain_scores` recorded at all, so
   nothing was re-derived for them regardless.

No model was called; the only I/O is reading saved JSON already on disk and running pure functions.

**Limits.** A saved `news.json` keeps only the *rendered* `Fact` (headline, summary, label), never the
provider's raw `symbols`/`relatedTickers` array a fact came from. The "≤3 symbols" branch of
`isSubjectRelevant` cannot be re-checked from that: some items a live re-run would still count as
coverage (a short tag list with no textual mention of the company at all) are counted here as lost. This
makes the method **conservative** — an upper bound on how many ideas would actually flip, not an exact
replay. It is also a plain word/phrase match, not a semantic one, so it can still miss a genuine mention
phrased unusually. Both limits were already flagged in the plan's own F3 evidence note ("the check is a
name/ticker substring match, so it is indicative only").

**Result.** 28 of 29 saved legacy runs under `runs/` have both artifacts and at least one shipped idea
(the 29th, `2026-06-14T10-24-59`, shipped no ideas at all). Across them, 45 shipped ideas carried a
`news` domain score.

| Run | Mode | Ticker | Old `domain_scores` | Flips to uncovered | Would fail the evidence floor |
|---|---|---|---|---|---|
| `2026-09-24T12-58-48` | independent | SAP.DE (news=0) | news+quant | yes | **yes** — quant alone |

The other 44 shipped ideas with a news score keep their coverage: 39 have at least one AlphaVantage
headline fact for that ticker (untouched by this task) and are excluded from re-derivation by design;
the remaining 5 are Alpaca/Yahoo-only cases where at least one fact still names the ticker root, the ADR
symbol or the company name in its headline or summary, so nothing is lost for them.

**One idea, in one run, flips — and it is exactly the SAP.DE case F3 documented.** `SAP.DE` shipped at
`domain_scores: {news: 0, quant: -4}` on 2026-09-24; with `news` correctly re-derived as uncovered, only
`quant` remains, which `checkPriceOnlyEvidence` refuses ("scored by quant alone... its whole case is the
ranking that selected it"). This confirms the plan's Lead 1 claim exactly: *"Once relevance is fixed, the
existing floor drops it (quant alone)."* No other historical idea's evidence floor outcome changes under
this method. Given the conservative bias above, a live re-run could show this count as low as it is here
but not lower.

## Files changed

- `internal/marketdata/newsfilter.go` — `isSubjectRelevant`, `relatesTo` (moved from `yahoonews.go`),
  `tickerRoot`, `mentionsSymbol`/`mentionsCompanyName`/`mentionsText`, `normalizeCompanyName`,
  `WithCompanyNames`/`companyNameFor`.
- `internal/marketdata/alpacanews.go` — `Related` now calls `isSubjectRelevant`.
- `internal/marketdata/yahoonews.go` — `Related` now calls `isSubjectRelevant`; `newsData` threads `ctx`;
  old local `relatesTo` removed (moved).
- `internal/orchestrator/orchestrator.go` — wires `marketdata.WithCompanyNames` from the loaded universe
  once, before Stage 0.5, covering every research mode.
- Tests: `internal/marketdata/newsfilter_test.go` (new), additions to
  `internal/marketdata/alpacanews_test.go` (the SAP.DE wrap fixture and a genuine-story positive control).
