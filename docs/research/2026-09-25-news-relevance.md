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
mapping), the company name, or one of its aliases, **or** its tag list carries three symbols or fewer. Tag
membership is still required first: dropping it would let a short tag list on a completely unrelated story
(the Namibia/oil search-fallback case `TestYahooNewsRefusesAFeedThatTagsNothingToTheTicker` already guards
against) count as coverage for whichever ticker happened to be fetched. A short root or ADR symbol (≤2
letters — "ON", "A", "T") only counts spelled the way a ticker actually is (case-sensitive, which also
catches a cashtag like `$ON` for free); a plain case-insensitive match would tag "shares moved **on**
Tuesday" to ON Semiconductor.

The rule is shared by both feeds (`alpacanews.go:130`, `yahoonews.go`), as the controller ruling required.
Company names and aliases are not reachable inside `internal/marketdata` directly — `internal/universe`
already imports `marketdata` for `IsForeignSuffix`, so importing back would cycle — so the orchestrator
wires a `ticker → []name` lookup into the same `context.Context` that already threads down to every
provider's `Fetch` call (`marketdata.WithCompanyNames`, wired once in `internal/orchestrator/orchestrator.go`
right after the universe loads, covering both the legacy and thesis research modes). Every existing test,
and `internal/marketdata/research_capture.go`'s frozen-snapshot capture used by `research-pair` (which
collects before an orchestrator run exists to wire anything into), run on an unwrapped context and degrade
to root/ADR matching only — no company-name match, same as no run had before this feature.

### `normalizeCompanyName` (fix round 1)

The first review pass found `normalizeCompanyName` producing needles that could never match a real
headline, because they no longer began or ended on a word character — `mentionsText`'s `\b` boundary
requires exactly that, and a needle ending in a non-word character can never close against the space that
follows a real word in prose. Four separate defects, found against the actual universe CSVs:

- **A trailing `&` or dangling `and`**, left behind once the suffix loop stripped the corporate suffix
  after it: `"Merck & Co. Inc."` → `"Merck &"`, `"Eli Lilly and Company"` → `"Eli Lilly and"`,
  `"JPMorgan Chase & Co."` → `"JPMorgan Chase &"`, same for Wells Fargo, Deere, S&P Global's parent forms.
  Fixed by stripping a trailing `&`/`AND` token in the same suffix loop, repeatedly, so `"Henkel AG & Co.
  KGaA"` reduces all the way to `"Henkel"`. A real `"and"` in the *middle* of a name ("Air Products and
  Chemicals", "Nippon Telegraph and Telephone") is untouched, because it is never the *last* token.
- **A leading `"The"`**, part of the legal name but never how a headline refers to the company: `"The
  Trade Desk Inc."` normalized to `"The Trade Desk"`, which cannot match a headline that opens "Trade Desk
  Plans...". Six universe names carry it (Boeing, Coca-Cola, Walt Disney, Home Depot, Goldman Sachs
  Group, Trade Desk); all now strip it.
- **A parenthetical alias kept inside the needle**: three asia100/eu50 names spell their common brand in
  parentheses after an unrecognizable legal name — `"Industria de Diseño Textil SA (Inditex)"`, `"Fast
  Retailing Co. Ltd. (Uniqlo)"`, `"Hon Hai Precision Industry (Foxconn)"`. A literal `"(Inditex)"` never
  appears in a headline. `normalizeCompanyName` now removes the parenthetical from the needle and returns
  its contents as a second needle, checked the same way (`mentionsCompanyName`).
- **`SpA` and `KGaA` missing from the suffix list**: four Italian eu50 names (Intesa Sanpaolo, UniCredit,
  Eni, Enel) and one German one (Henkel) normalized with the suffix still attached. Added.

`internal/marketdata/newsfilter_test.go`'s `TestNormalizeCompanyNameStripsCorporateSuffixes` now tables
all of these, plus the "&"/"and" cases that must survive untouched. `TestNormalizedNamesAreWellFormed`
(`internal/marketdata/newsfilter_universe_test.go`) is the table test the review asked for "over the real
universe (`universe.Load` or equivalent)": `marketdata` cannot import `universe` (the cycle above), so it
reads the four CSVs directly by relative path instead, and asserts every normalized name (and every
extracted alias, and every `aliases.csv` row) begins and ends on a word character — the exact property
that made all four defects above unmatchable. `TestIsSubjectRelevantMatchesRealHeadlinesNormalizeCompanyNameOnceMissed`
regression-tests the three real headlines the review named: `"Merck Advances European Regulatory
Approval..."` (MRK, fixed by the `&` strip), `"Trade Desk Plans 15% Job Cut..."` (TTD, fixed by the `The`
strip), and `"Regeneron Highlights EYLEA HD Momentum..."` (REGN — not fixed by any text-matching change;
see Residual limits below).

### `internal/universe/data/aliases.csv` (fix round 1)

A second, independent gap: some tickers' headlines commonly use a common/brand name that shares no text
at all with either the ticker or the suffix-stripped legal name — `GOOGL`/`GOOG` ("Alphabet" vs.
"Google"), `7203.T` ("Toyota Motor" vs. "Toyota"), `2330.TW` ("Taiwan Semiconductor Manufacturing" vs.
"TSMC"), `005930.KS` ("Samsung Electronics" vs. "Samsung"), `META` ("Meta Platforms" vs. "Meta"/"Facebook").
A numeric or local-script root (`7203`, `2330`, `005930`) never appears in an English headline at all, so
these tickers have no other route to a match.

Per the controller ruling, this is a new committed data file, `internal/universe/data/aliases.csv`
(`ticker,alias`, one row per alias), loaded the same way `internal/marketdata/data/adr_map.csv` is, and fed
into the same `WithCompanyNames` lookup as the primary CSV name — every alias goes through
`mentionsCompanyName`/`mentionsSymbol`, the identical word-boundary and ≤2-letter rule as the ticker root.
A new universe test, `TestAliasTickersExistInTheUniverse`, rejects an alias row for a ticker not in any
universe file, mirroring `TestConstituentFieldsAreInTheRightColumns`'s existing guard against a
shifted/mistyped ticker.

The file is deliberately short — seven rows, five tickers — populated only where verification showed the
primary name's own normalization genuinely fails to reach the common name:

| Ticker | CSV name → normalized | Alias added | Why |
|---|---|---|---|
| `GOOGL`, `GOOG` | Alphabet Inc. Class A/C → **Alphabet** | Google | headlines say "Google", not "Alphabet" |
| `META` | Meta Platforms Inc. → **Meta Platforms** | Meta, Facebook | headlines drop "Platforms"; "Facebook" is still common press usage |
| `7203.T` | Toyota Motor Corporation → **Toyota Motor** | Toyota | headlines commonly drop "Motor" too |
| `2330.TW` | Taiwan Semiconductor Manufacturing → *(unchanged, no suffix to strip)* | TSMC | the full legal name is never spelled out in a headline |
| `005930.KS` | Samsung Electronics Co. Ltd. → **Samsung Electronics** | Samsung | headlines commonly drop "Electronics" too |

Three of the reviewer's other named examples needed **no alias**, because they were already reachable —
confirmed by tracing `normalizeCompanyName` against the real CSV row, not assumed:

- **Tencent** (`0700.HK`, "Tencent Holdings Ltd.") already normalizes to `"Tencent"` — `"Holdings"` is
  already a recognized suffix.
- **Inditex** (`ITX.MC`) and **Uniqlo** (`9983.T`) are already extracted as the parenthetical alias
  described above, from their own CSV name — no separate `aliases.csv` row needed.
- **Foxconn** (`2317.TW`) likewise, from `"Hon Hai Precision Industry (Foxconn)"`.

`TestIsSubjectRelevantMatchesACuratedAlias` covers the mechanism end to end (GOOGL/"Google").

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
   (`marketdata.USLine`), or the company name or one of its aliases (`universe.Load().Lookup(ticker).Name`
   plus `universe.AliasesFor(ticker)`, each normalized and matched by local copies of
   `normalizeCompanyName`/`mentionsCompanyName` kept in sync with the fix round below)? If none does, the
   ticker's news coverage flips to uncovered.
3. For a flipped ticker, `news` was dropped from a copy of its `domain_scores` and the real
   `checkPriceOnlyEvidence` (`internal/orchestrator/riskgate.go`, unexported, called directly since the
   test lives in the same package) was run against what remained, to answer whether the idea would now
   fail the evidence floor. The floor does not apply in single-stock mode, matching production
   (`applyRiskGate`); the two single-stock runs in the sample had no `domain_scores` recorded at all, so
   nothing was re-derived for them regardless.

No model was called; the only I/O is reading saved JSON already on disk and running pure functions. Run
twice: once against the rule as first shipped, once after the fix round below (leading `"The"`, trailing
`&`/`and`, parenthetical aliases, `SpA`/`KGaA`, and `aliases.csv`); both produced the identical table
below.

**Limits.** A saved `news.json` keeps only the *rendered* `Fact` (headline, summary, label), never the
provider's raw `symbols`/`relatedTickers` array a fact came from. The "≤3 symbols" branch of
`isSubjectRelevant` cannot be re-checked from that: some items a live re-run would still count as
coverage (a short tag list with no textual mention of the company at all) are counted here as lost. This
makes the method **conservative** — an upper bound on how many ideas would actually flip, not an exact
replay. It is also a plain word/phrase match, not a semantic one, so it can still miss a genuine mention
phrased unusually. Both limits were already flagged in the plan's own F3 evidence note ("the check is a
name/ticker substring match, so it is indicative only").

Residual classes the fixed rule still misses, named rather than silently absorbed into "indicative only":

- **A multi-word normalized name where a headline commonly uses just its leading distinctive word.**
  `"Regeneron Pharmaceuticals Inc."` normalizes to `"Regeneron Pharmaceuticals"`, which does not match a
  headline that only says "Regeneron"; the same shape applies to `"The Walt Disney Company"` →
  `"Walt Disney"` (headlines often say just "Disney"), `"Infineon Technologies AG"` → `"Infineon
  Technologies"` (often just "Infineon"), `"Daikin Industries Ltd."` → `"Daikin Industries"` (often just
  "Daikin"). **Deliberately not fixed**: the controller ruling explicitly rejected matching on a bare
  leading token of a multi-word name as a general rule ("too many false positives"), so these rely on the
  ≤3-symbols branch (real for a single-company story, as REGN's regression test demonstrates) or a
  specific, verified `aliases.csv` row when a case is common enough to justify one — none of these three
  met that bar this round.
- **A numeric-root foreign ticker whose common Western acronym isn't in `aliases.csv` yet.** `9432.T`
  (Nippon Telegraph and Telephone, commonly "NTT") and `MC.PA` (LVMH Moët Hennessy Louis Vuitton SE,
  commonly "LVMH") are exactly the pattern `aliases.csv` exists for, and were considered, but held back
  this round to keep the file to the reviewer-named, individually-verified set (see above). A future
  conservative addition, not a defect in the mechanism.
- **AlphaVantage's own `relevance_score` has no enforced threshold** (already noted above) — unchanged by
  this task either round.
- **The raw `symbols`/`relatedTickers` array isn't preserved in saved `news.json`** (already noted above)
  — the acceptance method's own structural limit, unrelated to the relevance rule itself.

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

**The fix round changed no number in this table**, which is worth explaining rather than leaving
implicit: MRK, TTD and REGN — the three tickers whose real headlines the fix-round review found — all ship
in the *same* 2026-09-24 run as SAP.DE, all carry a `news` domain score, and none flips, before or after
the round. Each of the three has a *different* saved headline for the same ticker that already matches on
the bare ticker root, independent of any normalizeCompanyName fix: "Merck **(MRK)** Declines More Than
Market..." (Zacks), "Trade Desk **(TTD)** Stock May Be 33% Undervalued..." (Simply Wall St.), and
"Regeneron Pharmaceuticals **(NASDAQ:REGN)** has outperformed the market..." (Benzinga, in the summary of
its first headline). Since `auditStillCovered` only needs *one* surviving fact per ticker, this particular
historical run was never depending on the fix to stay covered.

The fix is still real and necessary — `TestIsSubjectRelevantMatchesRealHeadlinesNormalizeCompanyNameOnceMissed`
isolates the *specific* headline text the review flagged ("Merck Advances European Regulatory Approval...",
"Trade Desk Plans 15% Job Cut...", neither of which contains a bare "MRK"/"TTD") and shows each failing
without the fix. It simply is not the headline that happened to decide MRK's or TTD's coverage in *this*
saved run — a different provider, or a different day's headline mix, could easily have surfaced only the
unmatchable ones. The acceptance table answers "did shipped history change"; the regression tests answer
"does the fixed code do what it claims on the case that motivated it" — both are true, and neither
substitutes for the other.

## Files changed

Initial pass:

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

Fix round 1 (this section):

- `internal/marketdata/newsfilter.go` — `normalizeCompanyName` now strips a leading `"The"` and a
  trailing `&`/`AND`, extracts a parenthetical alias, and adds `SpA`/`KGaA` to `companySuffixes`; it
  returns `(normalized, alias)` instead of a single string. `mentionsCompanyName` checks both.
  `WithCompanyNames`'s lookup now returns `[]string` (the CSV name plus any aliases), and
  `companyNameFor` is renamed `companyNamesFor` to match; `isSubjectRelevant` loops over every name.
- `internal/universe/aliases.go`, `internal/universe/data/aliases.csv` (new) — the curated alias table
  and its loader/accessor (`AliasesFor`), following `internal/marketdata/adr.go`'s existing pattern.
- `internal/orchestrator/orchestrator.go` — the `WithCompanyNames` closure now also appends
  `universe.AliasesFor(ticker)`.
- `docs/workflow/output-schema.md` — a paragraph on the coverage-per-evidence-kind section stating the
  subject-relevance rule and pointing here.
- Tests: `internal/marketdata/newsfilter_universe_test.go` (new, the real-universe well-formedness table
  test); `internal/universe/aliases_test.go` (new, `TestAliasTickersExistInTheUniverse`); expanded
  `TestNormalizeCompanyNameStripsCorporateSuffixes`, new
  `TestIsSubjectRelevantMatchesRealHeadlinesNormalizeCompanyNameOnceMissed` and
  `TestIsSubjectRelevantMatchesACuratedAlias` in `internal/marketdata/newsfilter_test.go`; the
  `WithCompanyNames` call sites in `internal/marketdata/alpacanews_test.go` and
  `internal/marketdata/newsfilter_test.go` updated for the `[]string` signature.
