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
Tuesday" to ON Semiconductor. Fix round 2 below extends the exact-case rule to *every* root and ADR
symbol, and reads company names and aliases as proper nouns, because many longer roots are ordinary
words too.

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
`mentionsCompanyName`, the same word-boundary rule as the primary name, read as a proper noun since fix
round 2 (below).
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

### Word-like ticker roots and common-word aliases (fix round 2, final branch review)

The whole-branch review probed the rule with a market wrap tagging seven symbols and found three of them
counted as coverage through ordinary words: NOW ("Stocks **now** higher…"), COST ("…input **cost** for
retailers") and LOW ("…record **low** volatility"). The exact-case rule covered only roots of two letters or
fewer; NOW, COST and LOW are three and four. The universe carries more of the same shape — NET, TEAM, SNOW,
CAT, DIS, META — and each is the F3 defect again, reached through a word instead of a tag list.

- **Ticker roots and ADR symbols now always match case-sensitively** (`mentionsSymbol`). Reporters write a
  ticker in capitals — "ServiceNow (NOW)", "NASDAQ:COST", a `$LOW` cashtag — so nothing real is lost. An
  all-capitals headline ("STOCKS NOW HIGHER") would still match; a residual, named below.
- **Company names and aliases match as proper nouns** (`mentionsCompanyName`): any case except the first
  letter, which must be as given. `aliases.csv`'s "Meta" is the case that needed it — "meta-analysis"
  otherwise names Meta — and the universe's own names carry the same risk ("Apple", "Target", "Visa").
  "Trade desk" and an all-capitals "SAMSUNG" still match. A name of two letters or fewer must match exactly
  (none today). Residual: a sentence-initial common word ("Meta-analysis finds…") still reads as the name;
  the rule cannot tell a capital that starts a sentence from one that marks a name.

`TestIsSubjectRelevantRequiresExactCaseForAWordLikeRoot` holds the reviewer's three probe sentences under a
seven-symbol tag list (none relates) and "ServiceNow (NOW) shares…" and "$NOW…" (both relate);
`TestIsSubjectRelevantReadsANameAsAProperNoun` holds "meta-analysis" (does not relate) against "Meta
unveils…" and an all-capitals "FACEBOOK…" (both relate). Both fail on the previous rule.

The item label and the all-failed warning were also made true of both feeds. A non-subject item used to be
labelled `surfaced by search, not tagged to this ticker`, which is false for an Alpaca item that *is* tagged
but is about another company; it is now `context, not about this company`, and `agents/news.md` names the
new label with the same instruction (context, not coverage; a bias must not rest on it alone). The warning
for a feed where no item is about the company no longer calls every such case "a search fallback" — SAP.DE's
was four tagged Alpaca stories — and says instead that each item was either untagged or tagged on a story
about other companies.

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
three times: against the rule as first shipped; after fix round 1 (leading `"The"`, trailing `&`/`and`,
parenthetical aliases, `SpA`/`KGaA`, and `aliases.csv`); and after fix round 2 (exact-case roots,
proper-noun names). The third run called the production matching code itself — a temporary exported
wrapper around `isSubjectRelevant`'s text branches (`mentionsSymbol`, `mentionsCompanyName`), deleted after
use — rather than local copies. All three produced the identical table below. Saved runs predate the label
rename, so the audit still reads the old `tagged to this ticker` label to find what the old rule called
coverage.

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
  this task in every round.
- **A sentence-initial common word reads as a proper-noun name** ("Meta-analysis finds…" names Meta), and an
  all-capitals headline reads a word-like root as the ticker ("STOCKS NOW HIGHER"). See fix round 2.
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
the remaining 5 (CRWD on 2026-09-04; MRK, MSFT, REGN and TTD on 2026-09-24) are Alpaca/Yahoo-only cases
where at least one fact still names the ticker root, the ADR symbol or the company name in its headline or
summary, so nothing is lost for them.

**What this table cannot say.** 39 of the 45 — the large majority — are carried by AlphaVantage, which A2
does not touch: its coverage is decided by `articlesFor`'s own per-ticker `ticker_sentiment` entry, with no
relevance threshold. The acceptance result re-derives only 6 of 45 ideas, and "one flip" is a statement
about those 6, not about how often shipped coverage rested on items that were not about the company. As a
supplementary check only (no change to AlphaVantage, and not part of the result), fix round 2's audit also
ran the same text rule over each of the 39 tickers' AlphaVantage headlines and summaries: every one has at
least one that names the company under the new rule, so none of the 39 rests solely on items this rule
would call context.

**One idea, in one run, flips — and it is exactly the SAP.DE case F3 documented.** `SAP.DE` shipped at
`domain_scores: {news: 0, quant: -4}` on 2026-09-24; with `news` correctly re-derived as uncovered, only
`quant` remains, which `checkPriceOnlyEvidence` refuses ("scored by quant alone... its whole case is the
ranking that selected it"). This confirms the plan's Lead 1 claim exactly: *"Once relevance is fixed, the
existing floor drops it (quant alone)."* No other historical idea's evidence floor outcome changes under
this method. Given the conservative bias above, a live re-run could show this count as low as it is here
but not lower.

**Neither fix round changed a number in this table**, which is worth explaining rather than leaving
implicit: MRK, TTD and REGN — the three tickers whose real headlines the fix-round review found — all ship
in the *same* 2026-09-24 run as SAP.DE, all carry a `news` domain score, and none flips, before or after
either round. Each of the three has a *different* saved headline for the same ticker that already matches on
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

Fix round 2 (final branch review):

- `internal/marketdata/newsfilter.go` — `mentionsSymbol` matches every root and ADR symbol exactly;
  `mentionsCompanyName` (via `mentionsName`) matches names and aliases as proper nouns; the non-subject
  label and the none-about-the-company warning reworded to be true of both feeds.
- `agents/news.md` — names the new label, same instruction.
- `docs/workflow/output-schema.md` — the case rule added to the subject-relevance paragraph.
- Tests: `TestIsSubjectRelevantRequiresExactCaseForAWordLikeRoot`,
  `TestIsSubjectRelevantReadsANameAsAProperNoun` (new); label and warning substrings in
  `alpacanews_test.go` and `yahoonews_test.go` updated.

## 2026-09-27 addendum (A1): AlphaVantage gets the same rule

Plan: `docs/plans/2026-09-27-close-news-gap-test-drift.md`, Wave A, task A1.

### The gap this closes

A2 (above) fixed Alpaca and Yahoo. It never touched AlphaVantage: `articlesFor`
(`internal/marketdata/alphavantage.go`) kept *any* feed item carrying a `ticker_sentiment`
entry for the requested ticker, with no relevance threshold and no call to
`isSubjectRelevant`. A `ticker_sentiment` entry is AlphaVantage's own tag-list membership —
the same claim Alpaca's `symbols` list and Yahoo's `relatedTickers` list make, and no
stronger a claim that the story is *about* this company. 39 of the 45 news-scored shipped
ideas in the `runs/` history audited below rest on AlphaVantage coverage; none of A2's fix
reached them.

### The fix

`articlesFor` now takes `ctx` and the original CFR ticker. An item counts as coverage if its
headline or summary names the company — `namesCompanyInText` (`newsfilter.go`): ticker root,
ADR symbol, company name, or a curated alias, the same text match `isSubjectRelevant` itself
uses — **or** its own `relevance_score` clears `AVRelevanceFloor`.

This deliberately does **not** reuse `isSubjectRelevant`'s tag-membership gate or its "≤3
symbols" fallback (a short tag list is informative on its own). The first version of this fix
did reuse it, built from the item's full `ticker_sentiment` array on the theory that it would
mean the same thing here as for Alpaca/Yahoo. Branch review caught that this makes the
fallback a near no-op for AlphaVantage specifically: the repo's own captured sample
(`internal/marketdata/testdata/av_news_sentiment_sample.json`) tags every one of its 52 items
with only one or two tickers, and a real multi-holdings alert (the AFG Fiduciary example
below) is no exception — real AV items essentially never carry a tag list long enough to fail
that branch, so it would have waved almost everything through regardless of what the text
says, leaving the relevance floor as the only check actually doing anything. AlphaVantage
already supplies the thing that fallback approximates for Alpaca/Yahoo (a confidence that a
short tag list is meaningful) directly, as `relevance_score`, so the two checks stay
independent and are combined with OR instead of being folded into one shared branch.

A non-subject item mixed into a feed that also has at least one relevant item is still
rendered as a headline fact — AlphaVantage's format carries its own per-article relevance and
sentiment score, worth keeping visible as context — but labelled `context, not about this
company` (the identical phrase A2 gave Alpaca/Yahoo, so one instruction in `agents/news.md`
now covers both feeds), and it is dropped from the domain's aggregate `News Sentiment Score`,
which is now a mean over subject-relevant articles only.

**If *every* item AlphaVantage returns for a ticker is non-subject, the feed now emits no
Facts at all — only a warning**, mirroring `newsfilter.go`'s `headlineFacts` exactly (the same
all-non-subject suppression A2 gave Alpaca/Yahoo). The first version of this fix printed every
item as a Fact regardless, on the reading that the brief's "non-subject items become...
facts" described a per-item relabel, not a whole-feed rule. Branch review found this was the
SAP.DE defect again, undone: `HasDomainEvidence` (`pack.go`) counts any non-empty Facts list
(other than the earnings-date/US-line notes) as this domain having something to say about the
ticker, so a ticker whose only AlphaVantage evidence was a market wrap still counted as
news-covered, still got scored by the news specialist, and would still have cleared
`checkPriceOnlyEvidence` — exactly what this task exists to close, one provider over. Fixed by
matching `headlineFacts`'s behaviour precisely rather than reinterpreting it.

### Choosing `AVRelevanceFloor`: the relevance-score distribution

The ruling was to pin the floor from the saved data, not guess it. Method: every AlphaVantage
headline fact across `runs/*/data/news.json` (label shape `Headline N (relevance R, sentiment
...)`) was re-tested against `namesCompanyInText` directly — the same text match, and the same
absence of a tag-membership gate or "≤3 symbols" fallback, that `articlesFor` itself now uses
— with the same universe-backed `ctx` a live run wires (company names + `aliases.csv`). Unlike
the Alpaca/Yahoo re-derivation elsewhere in this doc, no reconstruction or padding of a tag
list is needed here at all: AlphaVantage's real behaviour never reads one for this check.

**Result, 628 AlphaVantage headline facts across 17 runs and 33 tickers:**

| | n | min relevance | max | mean |
|---|---|---|---|---|
| Passes the text rule | 550 | 0.980 | 1.000 | 0.9997 |
| Fails the text rule | 78 | 0.650 | 1.000 | 0.9738 |

This is a less tidy picture than "relevance separates subject from non-subject": **72 of the
78 text-rule fails carry relevance 1.00** — the same score the bulk of the passes carry.
AlphaVantage's own score does not, on this evidence, reliably distinguish a story that is
about this company from one it scores highly for some other reason. Three concrete fails at
relevance 1.00, read by hand:

- `ISRG`, *"Robot-assisted surgery shows clinical advantages over laparoscopic surgery for
  gastric cancer"* — plausibly real coverage of Intuitive Surgical's clinical domain, but the
  captured headline/summary never says "Intuitive Surgical" or "ISRG", so the text rule can't
  confirm it. A genuine miss on this rule's side, not a false high score from AlphaVantage.
- `MU`, *"Micron Announces Leadership Appointments to Accelerate Innovation and Growth"* and
  `MRVL`, *"Marvell Falls 4% Ahead of August 27 Earnings..."* — both real, on-topic headlines
  that name the company by its short form only ("Micron", "Marvell"), not the universe CSV's
  full normalized name ("Micron Technology", "Marvell Technology"). This is the same residual
  limit the main doc already names for Regeneron/Disney/Infineon/Daikin ("a multi-word
  normalized name where a headline commonly uses just its leading distinctive word") —
  confirmed here on two more names, not a new defect.

The only place relevance actually separates the two populations is the low end: the sample's
six weakest fails (relevance 0.65–0.71) are all genuine mismatches — the weakest, read by
hand, is a MarketBeat 13F-holdings alert titled *"AFG Fiduciary Services Limited Partnership
Has $3.87 Million Holdings in Amazon.com, Inc. $AMZN"* (`runs/2026-08-31T09-39-06/data/news.json`,
`ByTicker["TTD"]`), which names Amazon, not Trade Desk, yet carried a `TTD` `ticker_sentiment`
entry at relevance 0.65 — a two-ticker item (AMZN, TTD), the shape a real AlphaVantage item
actually has, not a market-wrap-sized tag list. The 78 fails split into exactly two clusters
with nothing between them: 6 at 0.65–0.71, and 72 sitting at the 1.00 ceiling. **Every floor
value in the empty band between them, (0.71, 1.00], produces the identical partition on this
data** — it excludes the same six low mismatches and admits the same 72 ceiling-scored items
regardless of exactly where in that band it sits. The data does not prefer 0.98 to, say, 0.90
or 1.00; it only rules out anything at or below ~0.71.

**Pinned: `AVRelevanceFloor = 0.98`** — chosen within that indifferent band, not because the
data determines that value over another one in it, but because it is also exactly the weakest
relevance any text-confirmed article in this pipeline's whole saved history has carried: a
floor that reads "at least what real coverage has always scored" rather than an arbitrary pick
from an otherwise indifferent range. It is not a claim that the floor discriminates well at
the high end (the data says plainly that it does not) — text-passing items count regardless
of the floor's exact value, since the floor is an OR route, not a requirement. The residual —
a subject-relevant item that fails the text rule and scores below 0.98, or one that fails the
text rule, scores at or near 1.00, and is not actually about the company — is named here
rather than silently absorbed: it is a live limit of AlphaVantage's own relevance score, not
of the text match, and no floor value fixes it.

### Acceptance audit: re-run with AlphaVantage included

`internal/orchestrator/news_relevance_audit_test.go`'s `TestNewsRelevanceAcceptanceAudit`,
gated behind `CFR_NEWS_RELEVANCE_RUNS_DIR` (a prior round of this audit was a script that was
never committed — the "test drift" this branch is named for; it is committed now, so a future
rule change can re-run it instead of re-deriving these numbers by hand). Re-run against this
repository's own `runs/` directory:

```
28 runs with both ideas.json and data/news.json, 45 news-scored shipped ideas,
1 flip to uncovered, 1 of those then fails the evidence floor
```

Identical to the pre-A1 result: **SAP.DE is still the only flip**, and it still fails
`checkPriceOnlyEvidence` on `quant` alone. AlphaVantage's inclusion changes nothing in this
particular history, and the reason is now confirmed rather than assumed: of the 39 shipped
ideas resting on AlphaVantage coverage, every one has at least one headline that clears the
new rule — via the text match for the large majority (a 33-ticker, 628-item sample where 550
pass outright), and the six historical AlphaVantage-only-covered tickers whose text match
happened to hold were already established in the pre-A1 audit (fix round 2's supplementary
check, main doc above). No historical idea was resting solely on the kind of item this task
closes off. That is a statement about this saved history, not a guarantee about a future run —
a name reachable only through a wrap-tagged, low-relevance AlphaVantage item, with no Alpaca
or Yahoo coverage either, is exactly the case A1 now catches.

The audit's own re-derivation (`reDeriveNewsCoverage`) re-tests an AlphaVantage-labelled fact
with `NamesCompanyInText` directly — no padded tag list, since AlphaVantage's real rule never
reads one — and an Alpaca/Yahoo-labelled fact with the padded `IsSubjectRelevant` call, as
before. It also treats a fact already labelled `context, not about this company` (a label only
this fix's own code can have written) as evidence for neither the old rule nor the new one,
rather than reading its "Headline N (relevance ...)" prefix alone and counting it as an old-rule
coverage claim it never was: since A1 also suppresses every AlphaVantage Fact for a ticker with
no relevant item at all, that label can now only appear on one item inside a mixed feed that
has at least one other, genuinely relevant item, so it never actually changes a verdict in this
history — but a future re-run over post-fix runs would otherwise misread it.

### Files changed

Initial pass:

- `internal/marketdata/alphavantage.go` — `articlesFor` takes `ctx` and the original ticker
  and sets `article.relevant`; `AVRelevanceFloor` (0.98, exported so the audit test can't
  drift from production); the aggregate `News Sentiment Score` sums only relevant articles;
  every headline fact is labelled `tagged to this ticker` or `context, not about this
  company`.
- `internal/marketdata/newsfilter.go` — `IsSubjectRelevant`, an exported wrapper around
  `isSubjectRelevant`, so a cross-package audit can call the real production rule instead of a
  hand-copied one that has to be "kept in sync" (the prior round's own words for exactly this
  risk).
- `internal/orchestrator/news_relevance_audit_test.go` (new, committed this time) —
  `TestNewsRelevanceAcceptanceAudit`, gated on `CFR_NEWS_RELEVANCE_RUNS_DIR`.
- `agents/news.md`, `docs/workflow/output-schema.md` — the keyed feed's headlines are now
  described as carrying the same `tagged to this ticker` / `context, not about this company`
  label as the global feed, and the floor is named.
- Tests: `TestNewsSentimentExcludesAWrapTaggedAtLowRelevance` (new, `alphavantage_test.go`).

Fix round 1 (2026-09-27 review — see "The fix" and "Choosing `AVRelevanceFloor`" above for
what changed and why):

- `internal/marketdata/alphavantage.go` — `articlesFor` no longer builds or passes a tag list;
  it calls `namesCompanyInText` directly. When no article for a ticker is subject-relevant,
  `fetchNewsSentiment` now returns no Facts at all (only a warning), matching
  `headlineFacts`'s all-non-subject suppression exactly, so `HasDomainEvidence` sees an empty
  feed rather than a wrap it would count as coverage. `AVRelevanceFloor`'s comment reframed
  per Minor 1 below.
- `internal/marketdata/newsfilter.go` — `isSubjectRelevant`'s text-matching core factored out
  into `namesCompanyInText` (unexported) and exported as `NamesCompanyInText`, so AlphaVantage
  and the audit can use the text match without `isSubjectRelevant`'s tag-membership gate or
  "≤3 symbols" fallback.
- `internal/orchestrator/news_relevance_audit_test.go` — `reDeriveNewsCoverage` takes separate
  `avRelevant` (`NamesCompanyInText`, no padding) and `globalFeedRelevant` (padded
  `IsSubjectRelevant`, unchanged) functions, and treats a fact already labelled `context, not
  about this company` as coverage under neither rule (Minor 2) instead of reading its
  "Headline N (relevance ...)" prefix alone.
- `docs/workflow/output-schema.md` — the AV paragraph corrected: `namesCompanyInText` not
  `isSubjectRelevant`, no tag-membership gate, and the all-non-subject suppression named
  explicitly.
- Tests: `TestNewsSentimentExcludesAWrapTaggedAtLowRelevance`'s fixture rebuilt from the real
  two-tag AFG-Fiduciary shape (the four-tag version was invented, and would have been rescued
  by the fallback this fix removes for AV); new
  `TestNewsSentimentEmitsNoFactsWhenEveryItemIsNonSubject`, asserting
  `HasDomainEvidence("news", td) == false` on an all-wrap feed, as the review asked; four
  pre-existing tests (`TestNewsSentimentKeepsHeadlines`, `TestNewsSentimentHyphenatesShareClasses`,
  `TestAlphaVantageFollowsTheUSLine`, `TestEarningsCalendarSurvivesAQuotaMessage`) updated to
  wire a company-name `ctx` or raise a fixture's relevance score, matching what a live run
  always provides, now that AV no longer gets the fallback's free pass.
