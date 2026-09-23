# Thesis-led research

`research_mode = "thesis"` is an opt-in alternative to the default `legacy`
pipeline. It selects global long and short equity ideas for 10–15 trading
sessions. The implementation changes how research is conducted and checked;
prospective results are still required to establish whether selection improves.

## Running and configuration

```sh
go run ./cmd/cfr run --research-mode thesis --json
go run ./cmd/cfr run --research-mode thesis --ticker ASML.AS --json
go run ./cmd/cfr scoreboard --research-compare
```

Set top-level `research_mode = "thesis"` in `cfr.toml`, or
`CFR_RESEARCH_MODE=thesis`, to use it from the TUI too. Model access is governed
by two selectors: `cheap_engine` picks the research model (gemini/api/local), and
`chief_engine` picks the Chief Analyst (claude | api; omitted means claude, so
existing configurations behave exactly as before). The API Chief needs dedicated
credentials (`[chief_api]` / `CFR_CHIEF_API_*`), never inherited
from `[api]` or `[local]`. Engine selection changes neither persona: `chief-analyst`
for legacy mode, `thesis-chief` for thesis. The separate Chief API fallback remains
optional and disabled without its own credentials. There is no paid search dependency.

The `[research]` settings are `rounds = 3`, `documents = 8`, `candidates = 24`,
and `shortlist = 12`. Limits are 1–6 rounds, 1–32 document attempts, 1–48
candidates, and a shortlist between 1 and the candidate budget. Environment
overrides are `CFR_RESEARCH_ROUNDS`, `CFR_RESEARCH_DOCUMENTS`,
`CFR_RESEARCH_CANDIDATES`, and `CFR_RESEARCH_SHORTLIST`.

`sources_file` (`CFR_RESEARCH_SOURCES_FILE`) optionally adds a CSV with header
`ticker,url` to the bundled, partial issuer investor-relations registry.
`holidays_file` (`CFR_RESEARCH_HOLIDAYS_FILE`) accepts `ticker,date`, with dates
in YYYY-MM-DD form and `*` for closures applying to all tickers. Paths resolve
from the working directory. Neither file is required.

## Research sequence

1. Reuse the verified pre-screen for price history and liquidity eligibility.
   Gather available Alpaca/Yahoo news across eligible names; reserve the small
   AlphaVantage budget for shortlisted research. Build separate price-setup and
   dated-event lanes. Both rotate across selected indices, then alternate into
   the candidate budget. Event triage can nominate names away from the top of
   the price ranking. This covers the curated samples, not entire exchanges.
2. One cheap-model triage selects up to the shortlist budget. Go validates names
   against the supplied candidates. Single-stock mode skips both triage steps.
3. Compute verified quant data and one shared Macro report. Check verified
   earnings dates after fetching news, before fundamentals, positioning, source
   reads or company model calls. Names with fewer than ten sessions before the
   event stay on a post-event watchlist. Each eligible company gets
   its own research loop combining news, fundamentals, positioning, issuer
   documents and available SEC source links. The researcher is not given scout
   direction or the pre-screen score as an answer to confirm.
4. Require a dossier explaining what changed, expectations, what remains
   underappreciated, the price mechanism and timing, what is already priced in,
   the strongest counterargument and observable invalidation. Claims distinguish
   observations from inferences and cite persisted evidence IDs with verbatim
   passages and issuer roles. Dossiers explicitly compare `long_case`,
   `short_case` and `no_trade_case`, and identify `preferred_direction` as
   BUY, SELL or NONE. The selector may select only the reviewed direction. A supported
   dossier needs a substantive cited document, valid citations and no unresolved
   material questions. Headlines or factors alone cannot qualify.
5. A separate challenger call checks the thesis against the evidence. A revision
   request buys one further researcher revision and final challenge, within the
   same document budget. This is a separate review, not an independent data
   source or a promise that the two models have uncorrelated errors.
6. Claude selects up to five ideas, or one in single-stock mode. Zero is valid.
   The selector reads dossiers, challenges, evidence, verified quant and Macro;
   it is not anchored to the old weighted confidence score. A final challenger
   reviews each proposed trade's targets and outcome range. Go checks provenance,
   construction and risk. One corrective Chief call can revise, replace or
   reject plans; surviving violations remove them in model preference order.

### The request/result protocol

Each ordinary research round can request supplied document URLs, links discovered
in fetched pages, recent SEC filing links, or a saved passage using a literal
`query`. Those four kinds — `document`, `filings`, `passage`, `news` — are the
whole set; there is no unrestricted web-search tool.

**Every request is answered, and the answer is returned to the model in the next
prompt.** An answer carries one of five outcomes:

| Outcome | Meaning |
| --- | --- |
| `fulfilled` | New evidence was added; the answer names its IDs. |
| `unsupported` | The operation does not exist, or the URL is not in the evidence. |
| `unavailable` | The operation exists; the source or the passage did not answer. |
| `already attempted` | Answered earlier this run. Repeating it cannot add evidence. |
| `budget exhausted` | The per-company document budget is spent. |

`news` is explicitly not a retrieval operation: the company's news is fetched
once before research begins, and a `news` request returns the IDs already in the
evidence together with the statement that no further retrieval happens. A repeat
of any answered request costs neither budget nor a provider call. The original outcome
and detail remain unchanged; `repeats` counts repeated requests without nesting explanations. A request that
cannot be fulfilled no longer stops the batch — the remaining requests are still
answered, so the model can tell which of its questions went unanswered.

`document` and `filings` spend the per-company document budget, including the
initial source reads; `passage` extraction has its own cap of twice that budget
and reaches only text already stored. Source failures and exhausted budgets
remain visible in `results` and in the run's errors. Worker and engine
concurrency limits, cancellation, subprocess timeouts and retries still apply.

### Output schema and one bounded repair

Each role's persona states its object field by field, with types: an array is an
array, empty as `[]`, and never a string or an object. The thesis roles get their
own capability block; they do not inherit the legacy pipeline's instructions to
emit a `missing` array or a strength score, neither of which exists here.

A payload that does not decode, or that decodes with an invalid enum or claim
kind, buys **exactly one** repair call before any substantive review. The repair
prompt carries the model's own previous output and asks only for a re-serialised
form of it: a repair may not add a claim or an evidence ID, and may not upgrade a
status or a verdict. A repaired payload is recorded as `repaired`, never promoted
to clean research. A payload that still does not decode is a research failure.

Malformed requests are not schema errors. Failing a whole dossier over one badly
formed request would throw away the research to correct the postscript; the
protocol answers the request instead.

## Evidence and time

`data/discovery.json`, `data/candidates.json`, `shortlist.json`,
`data/research.json` and per-company `data/research-<hex ticker>.json` preserve
the funnel and evidence. Each source has an ID, issuer, URL, kind, retrieval
time, extracted text, available publication/event/reporting-period metadata and
any retrieval error. Model reports are saved separately. Provider `as_of` values
remain explicitly labelled; retrieval time does not date an event or a manager's
trade. Old holdings retain their reporting-period information.

No credential configured for this application can appear in a report, in run
metadata, in a prompt, or in the run log. Providers echo the request they were
sent — AlphaVantage answers a rejected call with prose quoting its own query
string, and Go wraps a transport failure in an error carrying the same URL — so
every artifact writer, the engine boundary and the event stream redact configured
secrets, replacing them with `[redacted credential]` while leaving the rest of
the diagnostic intact.

Documents are read through Go, with public-destination checks, bounded redirects,
a 25-second timeout and a 2 MiB response-prefix limit (one additional
byte detects overflow). Oversized accessible pages can supply bounded text with
`truncated: true`; incomplete tags and script/style tails are excluded. The text reader supports accessible
HTML/text/XML/JSON; extraction stores at most 40,000 characters and discovers up
to 80 links. Initial prompts select substantive windows across the entire stored source, including beyond navigation prefixes. Later prompts prioritize verbatim claim passages and their surrounding context, then
substantive documents, under a strict total text budget. Source identity,
reporting-period metadata and truncation markers remain visible. Passage requests
inspect the stored text and preserve a `parent_id` back to the source. Provider
full article bodies are reused without another fetch or budget charge. Discovered
issuer results/release links rank before navigation and PDF-only links; URL date
suffixes break ties, and do not establish publication dates.

Every model call saves its exact redacted assembled prompt, supplied data and size profile to `data/input-<call name>.json`, including
the exact evidence selection for each review. No engine credentials are included;
the existing artifact redaction still applies. `discovery-coverage.json` and
`research-coverage.json` record regional denominators, names with news and usable
documents, and document counts by source. These are coverage counts, not counts
of independent events. AlphaVantage's persistent daily limiter also remembers
provider-confirmed daily exhaustion; a transient per-minute limit does not
exhaust the day. These are extracted-text snapshots, not complete page archives. PDF-only,
paywalled, blocked or JavaScript-only pages may remain unavailable. The partial
issuer registry can be extended without changing code. An unavailable source
cannot be treated as confirming evidence.

Earnings reactions require an actual release timestamp with a quoted source
passage. Filing dates are not announcement times. Go maps the timestamp to the
first priceable session and computes the abnormal reaction against the benchmark;
the challenger checks attribution. Full retracement uses cumulative returns,
not a comparison of z-scores over unequal durations. Unknown timing remains a
gap. The bundled US calendar covers 2026–2027, including published early equity
closes. Other markets and dates outside coverage retain estimated status. Missing
post-event prices or exact benchmark boundary dates retain an awaiting-prices
watchlist condition; an unadjusted return is not an abnormal reaction.

Go supplies the run anchor, the next fifteen session dates, known closures, event
ordering and session distance, publication age where known, and daily versus
10/15-session volatility scales. It never substitutes retrieval time for
publication time. Dates supplied with no time of day remain date-only facts.
Models use these facts rather than counting weekdays or comparing unlike units.

Go verifies each claim's quoted passages against its cited stored documents and
requires an explicit issuer role. A supported challenger must address every
dossier claim by ID and preserve or explicitly dispute its source attribution. New
contract-v2 reviews use `claim_reviews` rather than copying accepted claims; each
entry records `claim_id`, `assessment`, `attribution` and `reason`. The review
includes the supplied `dossier_hash`; revision invalidates earlier reviews.
A conflicting issuer role requires revision. These checks establish quotation
and review consistency; semantic entailment, expectations and causal attribution
still require the independent model review. Public information alone establishes
neither full incorporation nor delayed incorporation. Evidenced continuation
mechanisms are allowed without scheduled catalysts. Neutral positioning is an
abstention; chart-pattern technical analysis is prohibited.

## Plans and enforcement

Schema v2 adds `research_mode: "thesis"`, `decisions`, and a `thesis` object
on selected ideas. Its fields include `why_now`, `invalidation`, `catalyst_window`,
`evidence_quality`, `evidence_ids`, entry/stop/target reasons, `outcome_low`,
`outcome_high`, `entry_expires_on`, `expires_on`, `prerequisites` and
`calendar_estimated`. Ranges are plausible scenarios in listing currency, not
probability intervals. Confidence/base confidence are zero and domain scores
are absent; the UI displays evidence quality and status instead of a percentage.

Every researched candidate has an actionable, conditional, watchlist or rejected
decision with a reason. Only selected plans occur in `ideas`. The Chief response
must include explicit `ideas` and `decisions` arrays, including for a no-trade
result. Malformed output and failed calls degrade the run; they never trigger
mechanically generated thesis trades. Headless exit codes remain 0 complete,
3 degraded, 1 error and 2 usage. A successfully researched no-trade run is complete.

### Research failure is not thesis rejection

A call that never returned, a payload that would not decode, retrieval that
reached no usable source, and a review that examined the thesis and found it
wanting are four different facts. Recording them as one made a run whose research
half failed read as a run that had considered twelve companies and disliked ten.

`metadata.json` carries a `research_outcomes` entry per researched candidate with
independent `transport`, `parsing`, `evidence`, `review` and `decision` fields,
and every call's `DomainStatus` carries `payload` (`ok`, `repaired`, `invalid`)
alongside its status — a call can complete and still return nothing usable, and
an `invalid` payload degrades the run exactly as a failed call does.

A candidate whose research did not complete **cannot** be rejected. Its decision
stays on the watchlist, carries `blocked: "research_failure"`, and states which
half of the pipeline failed. An unavailable independent challenge is recorded as
`unavailable`, never as a rejection, and no challenge is run at all when there is
no readable dossier to review. A decision's `reason` is the selector's own words;
the independent challenge's wording is preserved separately in `review_reason`
and never written over them. `blocked` is also set for a plan the risk gate
removed (`risk`) and for a substantive review rejection (`review_reject`). Upcoming earnings that
leave too little time use `awaiting_event`; missing post-event reaction prices use
`awaiting_prices`. Both retain watchlist status. Early earnings deferrals record
transport, parsing and review as `not_run`, with no model-call failure implied.

Go requires 10–15 sessions, verified fresh prices and volatility, grounded
citations, consistent level ordering, a target inside the stated outcome range
and positive whole-share sizing with verified FX. Position, liquidity, maximum
stop/target, entry-distance, sector, correlation and exposure constraints remain.
Legacy minimum reward:risk, minimum stop width and simulated-expectancy floors
do not select thesis trades. Simulated expectancy is an assumed-edge diagnostic,
not an estimate of the stock's actual expected return.

Entry expires after three projected sessions. The trade has an absolute expiry
from generation; a late fill cannot restart the clock. Verified upcoming earnings
truncate expiry before the event. If that leaves fewer than ten projected
sessions, the plan is rejected. Missing event dates require verification before
entry, and shorts require borrow and cost verification.

The bundled [NYSE schedule](https://www.nyse.com/trade/hours-calendars), verified
on 2026-09-07, supplies US closures for 2026–2027 and their early closes. A
projection is verified only when the entire date window is covered. Other markets
and dates outside coverage skip weekends and supplied closures but remain
estimated and conditional on calendar verification. User closure CSVs add dates;
they do not establish completeness. Earnings expiry uses the last projected
session strictly before the event, not the preceding calendar day. Replay uses actual available bars and the
absolute deadlines; it closes on the final bar on or before expiry once that
deadline is observable. This is conditional paper replay, not evidence of actual
execution or satisfied prerequisites.

## Evaluation

`scoreboard --research-compare` separates research mode, schema, research engine,
model and persona vintages. It reports shipped, composite and shortlist calls
at both 10 and 15 sessions, with pending/unavailable counts, empty runs,
conditional plans, duration and available completion-token accounting. Calls
are deduplicated within cohorts. Ordinary replay preserves thesis status and
absolute expiry; legacy confidence calibration excludes thesis observations.

The comparison measures directional calls rather than net executed profits.
Use deliberately paired future legacy/thesis runs and retain all artifacts,
including empty runs, before drawing conclusions. Different run dates, overlapping
market exposure, conditional execution and small samples limit attribution.
Historical source evidence is not reconstructed. No automatic weights or risk
parameters are tuned from this comparison.

### Explicit pairing and evaluation diagnostics

The research comparison retains every run directory. Missing or malformed
`ideas.json` is a failed result, distinct from a valid empty ideas array.
Per-run diagnostics include outcome, failed calls, invalid/repaired payloads,
raw/distinct provider-error counts, latency, available regional coverage and
usage-accounting completeness. Aborted runs with no metadata remain visible as
unknown. Missing coverage artifacts are unavailable, never zero coverage.

`DomainStatus.usage` contains one entry per attempted engine call, including
failed and truncated API attempts and subsequent retries. It retains reported
prompt, completion, total, cache-hit/miss and reasoning counts. Missing counts
are omitted; reported zeroes are preserved. Cache and reasoning counts are
subsets and are not added to totals. The compatibility `tokens` field now sums
reported completion tokens across all attempts. Legacy artifacts remain readable.
The [API usage fields](https://api-docs.deepseek.com/api/create-chat-completion)
are recorded without fetching pricing or calculating a model bill. Claude print calls now request structured JSON. Whole-tree `modelUsage`
counts take precedence over main-loop `usage`: input plus cache-read and
cache-creation tokens form prompt totals; output tokens form completion totals.
Main-loop-only telemetry and interrupted calls are marked incomplete.
Plain-text CLI wrappers and engines without telemetry retain unavailable counts.
Comparison totals are lower bounds whenever attempts have incomplete counts;
`complete_attempts/attempts` exposes that limitation.

Use the existing comparison with explicit evaluation options:

```sh
go run ./cmd/cfr scoreboard --research-compare --offline --json
go run ./cmd/cfr scoreboard --research-compare --research-pairs pairs.json --offline --json
go run ./cmd/cfr scoreboard --research-compare --research-cost-bps 30 --offline
```

`--offline` reads only each run's saved price snapshots. It makes no provider
calls and does not write calibration or modify run artifacts. Saved snapshots
from generation normally contain no future outcomes, so those calls remain
pending. Omitting `--offline` permits the existing price-provider refresh.

`--research-cost-bps` is an explicit assumed round-trip cost, not an observed
execution charge or a change to risk settings. A closed barrier replay reports
gross P&L and scenario net P&L after subtracting that cost. Open, unfilled and
unmeasurable plans have no net result. Conditional plans remain labelled, and
borrow/slippage/fees are only included to the extent the supplied assumption
includes them. Directional call returns and execution scenarios remain separate.
The scenario uses the configured fill window (overridable with `--fill-window`)
and records `fill_window_days` with each result.

Pairing is never inferred from two nearby timestamps. A manifest names the two
run directories, declared registration time, tolerated generation-time skew,
and hashes of the exact common input snapshots to audit:

```json
{
  "pairs": [{
    "id": "session-2026-09-08",
    "registered_at": "2026-09-08T07:00:00Z",
    "legacy_run": "2026-09-08T08-00-00",
    "thesis_run": "2026-09-08T08-01-00",
    "max_skew_seconds": 60,
    "snapshot_sha256": {
      "data/macro.json": "REPLACE_WITH_THE_64_HEX_SHA256_OF_THE_SAVED_BYTES"
    }
  }]
}
```

The example is a template, not a ready-to-run manifest. Use actual run names
and SHA-256 hashes of the raw file bytes. Each run can occur in only one pair.
Snapshot paths must be local input artifacts: `prescreen.json`, `quant.json`,
or provider JSON under `data/`; model inputs, research outputs and coverage
reports are excluded. Both runs must contain matching files with the declared
hashes. Run modes, tickers/index universes, legacy/thesis arm identities and
same-UTC-day generation times must agree with the declaration. Skew tolerance
is explicit, from zero to 3600 seconds; registration must precede both runs.

A successful audit is labelled `matched_declared_inputs`: it verifies the
specified file scope, not that every source reached by both pipelines was
identical or that the declared registration timestamp is independently proven.
This command does not freeze providers, backfill missing sources or prove
prospective registration. Prepare and retain the common evidence before the
experiment; matching only a Macro file does not establish matched company
research. Source freezing and deliberate live paired collection remain necessary
before the plan's prospective evaluation can be declared complete.

Matched pairs report thesis-minus-legacy mean benchmark-relative directional
returns for shipped/composite/shortlist arms at 10 and 15 sessions. A difference
is emitted only when both arms are nonempty and fully measurable at that horizon.
Empty, pending and unavailable outcomes remain distinct. Failures and mismatches
stay in the report instead of disappearing from the sample. These means describe
each arm's selected calls; they are not portfolio returns or causal estimates.

Weekly deduplication remains in place. The report also counts remaining
same-ticker windows that overlap, including opposite directions, and lists
pairs with overlapping shipped exposures. Pending endpoints use projected
session dates for this diagnostic. No independence, statistical significance,
or model edge is inferred from those dependent observations. Benchmark returns
require exact stock-anchor and outcome dates; a holiday must not advance just
the benchmark to the next session. Saved quant `as_of` dates preserve intraday
price anchors when available. Unreconciled anchor prices remain unavailable.

### Collecting a prospective frozen pair

Use the explicit `research-pair` command to register an exploratory comparison,
collect one common corpus, then run both pipelines:

```sh
go run ./cmd/cfr research-pair --out .data/pair-2026-09-09 --ticker AAPL --omit-alphavantage
go run ./cmd/cfr research-pair --evaluate .data/pair-2026-09-09
go run ./cmd/cfr research-pair --evaluate .data/pair-2026-09-09 --refresh
```

The output directory must be new and its parent must exist. Omit `--ticker` to
use the configured curated universe, or pass `--indices sp500,nq100`.
`--timeout` bounds the whole collection and both arms (default 60 minutes).
No scheduler is started. Exit codes are 0 for both complete, 3 if either arm
degrades or fails, 1 for coordinator/collection errors, and 2 for invalid usage.

The existing cheap engine must be configured as `api` or `local`; the normal
Chief remains Claude CLI. Frozen Claude calls disable tools, MCP, loaded
customizations and skills. The installed CLI must support the corresponding
flags, including `--safe-mode`. Ordinary research engine selection is unchanged. Both legacy and thesis can
use the explicitly configured `chief_fallback` API after Claude fails or returns
unparseable output. Configuring the cheap-research API alone does not enable
the backup. Metadata records backup usage, and a primary failure remains a
degraded run even when the backup succeeds.

Before collection, `registration.json` records the selection, resolved settings
with credentials removed, 10/15-session endpoints, randomized arm order, and
hashes of copied personas and optional source/calendar files. This is a local
timestamped record, not independently witnessed preregistration. Both methods
use the current runtime personas; `agents.v1` remains the separate, untouched
historical persona control. A single pair is a workflow trial, not an independent
sample of every selected stock or evidence of model superiority.

Collection freezes prices, provider packs, discovery news, report dates,
filing pointers and a bounded document corpus before either model runs.
Collection start and cutoff are retained; source retrieval is sequential, so
the corpus is not an atomic market snapshot. Source failures stay explicit.
Alpha Vantage uses the existing persistent provider quota state;
`--omit-alphavantage` skips it and records the choice. Extra collected source
text is also supplied to legacy fundamentals. Each method can select different
excerpts from this common corpus; this constrained evaluation is distinct from
ordinary research with unrestricted retrieval.

The identical corpus is saved as `runs/{legacy,thesis}/data/evaluation-snapshot.json`
inside the experiment. `pairs.json` pins its SHA-256 hash. Each arm has a private
cache, the same generation cutoff, and no calibration or postmortem feedback.
Recorded input hashes are checked before launches. `state.json` retains
not-started, failed, degraded and complete arms; a failed first arm does not
silently remove the second arm from the experiment.

Evaluation without `--refresh` uses the most recent saved outcome snapshot, or
the generation corpus when no outcome snapshot exists. Refresh fetches only
price history into `outcomes/`; it never updates original run evidence or
generation prices. Reports under `evaluations/` record their evaluation clock
and source outcome artifact. Forming daily bars are removed at acquisition;
offline reuse retains the saved outcome acquisition clock, so elapsed time
cannot promote an old intraday quote to a closing price. Model calls and calibration updates are absent
from evaluation. Canceled refreshes do not become completed outcome snapshots.

Optionally register `--cost-bps 30` at collection for an explicit assumed
round-trip execution-cost scenario. Omit it to leave net execution costs
unavailable. This assumption is separate from gross directional returns,
observed fills, and runtime risk parameters; it is retained for subsequent
evaluations rather than selected after seeing returns.

Future endpoints remain pending. Once an endpoint has passed, missing required
session bars are unavailable, not a later substituted endpoint. The research
comparison currently requires the bundled US 2026–2027 session calendar and
conservatively treats daily US bars as complete at 22:00 UTC. Other markets and
dates outside this calendar remain explicitly unmeasurable. Calendar estimates
used by ordinary research are not sufficient for exact outcome measurement.
Text reports display unavailable returns when no observations have been measured;
an empty or pending arm is never rendered as a measured zero return.
No pair can produce completed 10/15-session results before those sessions occur.


## Reliability contracts and diagnostics

Fresh researcher and challenger responses require `contract_version: 2`.
Historical dossiers and repeated-claim reviews remain readable; that compatibility
path cannot certify newly generated output. New dossiers include
`expectations_claim_ids` and `priced_in_claim_ids`, identifying observations or
explicit inferences whose premises the challenger must assess. Reference checks
do not prove causal reasoning or semantic entailment.

Dossiers target twelve material claims, 400 Unicode characters per narrative
field and two quotations of at most 300 characters per claim. These are writing
targets: `writing_diagnostics` records field names and actual lengths/counts;
exceeding them does not invalidate complete research. Three requests per round
and total input/response byte budgets remain hard limits.

A complete schema-valid researcher response over the byte budget may spend the
one repair allowance on compaction of narrative prose only. What room that
compaction has is measured, not assumed: `measureCompaction` decodes the raw
payload, blanks the twelve narrative fields, and re-compacts to find
`protected_bytes` — the floor the call may never cut into — then derives
`narrative_budget = limit − protected_bytes − headroom` (a declared 256-byte
constant covering JSON escaping and UTF-8 expansion the model cannot be
expected to predict), and allocates that budget across the fields in
proportion to each field's current length, with a floor for any field that is
currently non-empty. The model is given the resulting per-field byte
allocations directly — never a fixed character count — so a company with
little narrative to cut is asked to cut little, and a company whose protected
content alone already exceeds the response limit is refused before dispatch
rather than sent a doomed call: `Feasible: false` on the recorded
`CompactionAllowance`, zero model calls, `FailureKind: "input_capacity"`.
The compaction call runs on the configured Chief engine, not the cheap engine,
and returns only the twelve narrative fields. Go splices them into the original
payload's own bytes, so protected fields are identical by construction; fields
the reply omits keep their original text, and anything else the reply contains
is ignored. The spliced payload is then measured against the limit in Go and
still passes the protected-field comparison. The call keeps the ordinary
transient-retry budget (`retry`), not `synthesis_max_attempts`, because it has
no fallback. `[chief_api] compaction_reasoning_effort` optionally sets the
request's `reasoning_effort`: `adaptive` sends `high` when the narratives must
shrink below half their size and `low` otherwise (measured on deepseek-v4-pro:
`low` met a 0.58 cut in 64s but overshot a 0.35 cut by about 11%; `high` met
both, taking up to 271s); a fixed `low`/`high`/`max` is sent as written, and the
default sends none. The effort used is recorded on the compaction's
`allowance.reasoning_effort`.
Feasibility is a measured, per-company verdict, not a guarantee — some
oversized dossiers remain infeasible after this change, and that is a correct
answer, not a regression. Protected wire fields (including unknown extensions),
claims, exact quotations, numerical values, events, requests, uncertainties,
conditions and status must remain unchanged. Go compares them on the spliced
payload before accepting it. Original narratives are persisted in the compaction
call's diagnostics and supplied to subsequent research/challenge as an
explicitly budgeted, mandatory `compaction_originals` section. Optional retrieval
diagnostics/history are omitted first. If original narratives and other required
review evidence cannot fit, preparation refuses the call with a named capacity
failure and zero dispatched attempts. Original review evidence is never dropped. A supported challenge
must explicitly confirm `compaction_assessment: preserved`. A compaction that
fails, changes protected fields, exceeds input capacity or remains over budget
stops without another repair. It does not increase provider token caps.
A complete malformed payload can still receive one formatting repair, never followed
by a compaction retry. Explicit provider `finish_reason=length` is an `output_limit` failure with
one request and preserved usage; it gets no unchanged retry or schema repair.
Transient retries retain their existing policy. A failed revision skips final
challenge. Failed research remains a watchlist, not a substantive rejection.

Default complete-input / response-payload limits (UTF-8 bytes):

| Role | Input | Response |
| --- | ---: | ---: |
| Discovery/triage | 98,304 | 12,288 |
| Researcher/revision | 98,304 | 20,480 |
| Challenger/plan review | 98,304 | 12,288 |
| Chief/corrective/fallback | 196,608 | 24,576 |

The Input column measures the whole assembled prompt, complete, as before. The
Response column does not — it measures `json.Compact` of the last fenced JSON
block in the model's output (`response_contract_version: 2`; `internal/model`'s
`ResponseMeasure`), the same block the pipeline's own parser reads. Two
consequences follow, and both are deliberate rather than incidental: raw
transport bytes are recorded (`raw_bytes`, `raw_sha256`) for audit but bound
nothing, so a response with interior whitespace or a comment before the fence
that pushes it over the limit as raw text can still pass on payload bytes
alone; and because only the *last* fenced block is measured, a response of
tens of thousands of raw bytes whose final fenced block compacts to a few
hundred bytes also passes — the budget bounds what is actually parsed, not
what the model emitted around it. A profile with no `response_contract_version`
predates this change and is read under its original semantics: for that
profile alone, the raw response was the bound.

Configure `[research.budgets.<role>]` with `input_bytes` and `response_bytes`;
roles are `triage`, `researcher`, `challenger`, `chief`. Environment variables are
`CFR_RESEARCH_<ROLE>_INPUT_BYTES` and `CFR_RESEARCH_<ROLE>_RESPONSE_BYTES`.
Inputs must be 4,096–2,097,152 bytes, responses 1,024–131,072 bytes, with response
smaller than input. Existing precedence applies. Provider output-token limits do
not increase. Byte limits and the recorded `ceil(bytes/3)` estimate do not promise
an exact token count or fit within an undocumented model context window.

Research, revision and challenge prompts size their evidence section adaptively
(`sizeEvidence`) for exactly the room left in that call, rather than a fixed
character count: the section is measured redacted, against what the rest of the
call's own sections and its one appended mandatory addition (`revision_issues`
or `previous_challenge`) already cost, and fit by bisection over
`promptDocuments`'s optional-text ceiling. An evidence-light call is granted
more room; a call sharing space with a large mandatory addition gets
correspondingly less. Plan review is the one exception and still caps evidence
at a fixed 24,000 characters, deliberately never trimmed further — a plan
review has nothing safe to drop, since idea, dossier and evidence are exactly
what it is reviewing. The Chief board budgets globally across the whole
shortlist rather than per company (`chiefBoard`): every cited claim's required
quotations are reserved first, across all companies at once, and only the
remainder funds optional case narrative (by rank, so the board argues every
company to the same depth or none) and then source context (proportional to
unmet need). Chief inputs use only cited evidence and compact outcome records,
retaining all selected companies, including failed/deferred ones. Accepted
quotations appear in source text once; Chief claim records retain source and
issuer-role references. Full dossiers, request ledgers and source documents
remain in research artifacts. Source `selected_spans` use Unicode-character
offsets into original stored text; capacity decisions themselves are made in
UTF-8 bytes, after redaction. `omitted_text` distinguishes prompt omission from
source download truncation. If required quotations cannot fit, `omitted_claim_ids` records the omission and
preflight stops the call. Future-dated publications retain an unavailable record
but their content is excluded from model inputs.

The Chief board is budgeted for the whole board at once, not per company. Its
budget is the Chief input limit less the measured bytes of the persona wrapper,
contract lines and every other assembled section, macro included whether or not
macro is droppable. Every company's record is projected to what the Chief decides
from. Retained: candidate, outcome and eligibility for every selected company;
dossier status, preferred direction, evidence quality, entry conditions,
monitoring and unresolved items; each claim's ID, kind, evidence IDs, numerical
comparison and per-passage issuer-role attribution; the challenge verdict,
material issues, requests, condition/compaction/target assessments and every
per-claim assessment and attribution; computed temporal facts; and each cited
source's identity, attribution, reporting period, publication date and accepted
quotations. Projected away, each recorded by name in the record's
`projected_away`: the researcher's working narrative (hypothesis, changed,
expectations, underappreciated, mechanism, priced in, counterargument); claim
text and event passages whose own quotation is present in source text; the
reason on a claim review that is both supported and attribution-confirmed, which
restates the two verdicts recorded beside it; evidence ages derivable
from `as_of` and `published_at`; and source scaffolding beyond identity and
attribution (URL, retrieval time, page title, links, parent). An absent field
whose name is not in `projected_away` was never written.

Allocation is two passes over the whole board. Every company's records and every
cited claim's required quotations are reserved first; if those alone exceed the
budget the call fails as an input-capacity failure naming each company and its
required bytes, never as a silent trim. The remainder funds, in order, each
dossier's case narrative (long, short and no-trade cases, catalyst window,
invalidation) whole field by whole field, and then optional source context,
divided across companies in proportion to each one's unmet need rather than
spent company by company. A company's optional appetite is capped at its
required quotations plus 9,000 characters. Allocation is independent of the
order companies appear in, and is persisted per run as `chief-board`.

Prompt profiles include component byte sizes, limits, heuristic token estimates,
SHA-256 and `visible_evidence`. Only profiles for attempted calls contribute to
model visibility counts. Usage stays separate and retains unknown/incomplete
attempts. Chief fallback preparation failure is persisted and finalizes degraded,
just like an attempted fallback failure; successful fallback does not erase the
primary failure.

Material target comparisons carry explicit source/destination securities,
currencies, major/minor quotation units, share/ADS bases, target publication date,
forecast horizon and reference-price date. The comparison destination must be the
researched listing. Go computes normalized target and upside from the verified
price on that date. Cross-listing conversion requires a known issuer relationship,
a quoted ordinary-shares-per-ADS ratio from an issuer/depositary source, and an
effective date evidenced by that quote or the document publication date. The
reader marks authority from configured issuer seed hosts, including the final
response host; third-party sources cannot assign themselves authority. Unknown
ratio wording remains unresolved rather than guessed.

Dated FX uses completed observations at/before the valuation anchor and rejects
rates older than five calendar days. Results are cached per currency/anchor.
London pence are explicitly distinguished from GBP major units. Same-currency
comparisons need no external FX. Existing legacy FX behavior is unchanged. Ratio
provenance travels in frozen documents and FX in frozen prices; missing historical
fields remain missing with no current-data fallback. `target_claim_ids` connects
an execution target rationale to validated comparisons. The plan challenger must
reject omitted references when an external numerical target is being relied on.
A normalized annual target is not a validated 10–15-session forecast.

Temporal event ordering and projected thesis session dates use the listing-local
civil date. Timestamp precision is retained; date-only same-day releases remain
uncertain. Future observations and uncompleted sessions are unavailable scenarios,
not fetchable historical gaps. Requests for dated observations can include
`observation_date`; the request loop refuses future/uncompleted dates explicitly.
Existing early-earnings and awaiting-prices watchlists remain in force.

Coverage regions use explicit listing suffixes, independent of close hours and
calendar completeness. US-listed ADRs count as US listings. Every current curated
listing maps to one region; unmapped future listings remain `other/unknown`.
Coverage distinguishes accessible, failed and model-visible documents. It also
counts `substantive_primary_documents` and `visible_primary_documents`. Recognized
index/navigation pages are marked `navigation_only` and remain discovery inputs;
their length cannot satisfy the substantive-document requirement. Document request
identity ignores URL fragments and scheme/host case, while preserving meaningful
paths, query parameters and observation-date checks. Initial reads prefer discovered
release links over alternate-language indexes, fragments and static assets; unresolved
HTML template links are excluded.
`source_hosts` counts delivery hosts, **not independent reporting origins**;
syndication/independence remains a source-review question. The fixed evaluation
panel is ASML.AS, STLAM.MI, NOKIA.HE, 2330.TW, 9988.HK and BHP.AX. Synthetic panel
regressions establish passage delivery; they do not establish live source access.

Research comparison diagnostics expose logical calls, attempted/deferred companies,
completed/failed research, truncations and attempts, capacity failures, request
outcomes/repeats, per-stage usage, and primary/fallback results. Historical
truncations inferred from error text are labeled separately. Zero plans must be
interpreted using those outcomes, not as evidence that no opportunities existed.
See the [bounded acceptance runbook](../plans/2026-09-12-reliability-acceptance.md).


Current execution plans must declare `target_method`: `external_comparison`
requires nonempty validated `target_claim_ids`; `thesis_scenario` requires no
comparison IDs and remains subject to substantive scenario review. Final reviews
also echo `plan_hash` and return `target_assessment`; support requires a matching
plan/dossier and supported target assessment. Publication-time checks cover
additional challenger and plan-review claims as well as dossier claims.

Evidence allocation reserves all required quotations across the complete text
budget before adding optional context. A cited source is not restricted to 5,000
characters of required quotations: that limit applies only to optional context.
Globally infeasible requirements produce explicit omissions and capacity failure.


## Conditional research and operational results

`unresolved` contains material gaps in the existing thesis evidence. Optional
`entry_conditions` and `monitoring` arrays distinguish observable prerequisites
for entering an already evidenced thesis from future developments to monitor.
Missing core evidence cannot be moved into either array to obtain support. The
challenger must explicitly set `conditions_reviewed: true` when supporting a
dossier carrying these fields. Go copies reviewed entry conditions to plan
prerequisites and monitoring to the plan's monitoring array. Historical artifacts
without the optional fields remain readable; schema version remains 2.

Continuation does not require a scheduled announcement, and a trailing price move
alone proves neither full incorporation nor delayed incorporation. Future observations
are scenarios, not missing historical documents. A retrievable material historical gap
should produce an available source/passage request within the existing research budget.

Research outcomes now separately record `contract` (`ok`, `compacted`, `failed`),
`repair_attempts` (logical schema/compaction calls actually dispatched) and
`compaction_attempts`. Transport success is preserved when a complete response
fails the byte budget. Historical absent fields mean unrecorded, not measured zero.
Call diagnostics retain recovery type, actual attempts, usage and original errors.

Go computes `research_summary` in ideas.json: shortlisted, researched (usable
dossier), reviewed, supported, actionable, conditional, rejected, failed and deferred.
The TUI and headless view display these counts, clearly label research failures,
and expose primary-Chief access failure and configured fallback provenance.
Historical results derive the summary from metadata when available.

When no eligible company produced usable research, Chief and fallback synthesis
are skipped. Go still writes empty ideas, per-company explanations and degraded
artifacts (headless exit 3 for research failure). A legitimate reviewed no-trade
result remains distinct. A known disabled-Claude-subscription error stops unchanged
retries; the configured Chief engine remains the primary and the separately
configured Claude-only fallback retains its authorization and provenance.


### Continuation contracts (September 22)

Prompt profile version 2 applies the actual input byte cap after credential
redaction and persona wrapping. Optional sections are omitted whole and named in
`omitted`; required macro context, dossier identity, quotations and compaction
originals remain mandatory. A refused prompt is saved for audit. Zero-attempt
preparation failures carry payload `not_attempted`, not invalid JSON.

Yahoo news first queries the local listing. Only a nonempty, recent but unresolved
feed can trigger one query through the existing NYSE/NASDAQ ADR mapping. Quiet,
stale-only, failed and unmapped feeds receive no alias retry. The returned facts
retain the local ticker and explicitly label the ADR query. Issuer news never
supplies a currency or price conversion. Link ranking prefers substantive releases
and the seed language; navigation indexes and subscription pages are context.
Uncited navigation text comes after substantive sources in prompt allocation.

`source_diagnostics` records stable IDs, provider, ticker, listing region, stage,
reason, disposition and redacted message. Dispositions distinguish failed fetches,
withheld evidence, expected coverage gaps and context. Unrelated ownership filings
are `filtered_unrelated`; unsupported instruments are `not_applicable`. Other
provider warnings remain explicitly generic until typed at their source. Cache
schema 5 and frozen snapshots preserve these records. Existing error lists and
severity gates remain in place. Headless text and TUI share the source summary.
The scoreboard deduplicates source reasons by ID, groups them by region, and adds
`stage_progress` from the same research summary used by the result views. Its
historical `completed_research` field still means a completed reviewed workflow.
Cohorts include recorded Chief selection/model and prompt/response versions;
absent historical versions remain unknown.

`cfr acceptance-manifest [--indices sp500,eu50] [--ticker AAPL]` uses normal config
precedence and runtime defaults without starting a run. It exports an allowlisted
projection, configuration/source/persona hashes, dirty file hashes and capture
time, with a final full-credential/prefix leakage check. The shell wrapper in
`docs/plans/` delegates to this command. Run-specific timing belongs to the later
run artifact, not the manifest's capture time.

The source-only collector `go run ./cmd/cfr-source-capture --out NEW.json` is an
explicit network operation: fixed six-company panel, at most eight documents per
company, no config credentials or models, and refusal to overwrite a capture.
Ordinary tests replay the committed September 21 corpus without network access.
See [the continuation record](../plans/2026-09-22-continuation.md) for limitations.
