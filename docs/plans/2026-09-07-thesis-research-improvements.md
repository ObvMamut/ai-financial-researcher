# Thesis research improvement plan

Follow-up: unresolved live-run findings and acceptance criteria are consolidated
in the [September 12 reliability plan](2026-09-12-thesis-research-reliability-followup.md).

Date: 2026-09-07  
Status: implementation and local regression coverage are present, but live
reliability acceptance is incomplete. The full independent run on 2026-09-10
completed with a working DeepSeek Chief fallback and zero trade plans; nine of
ten companies that reached model research failed at the output-token limit.
Failure containment and provenance improved, while usable evidence delivery and
regional coverage still need work. See the completed live assessment below.
Prospective paired evaluation remains open. Credential rotation remains an
operator action and is not confirmed.
Retained local artifacts under `runs/` and `.data/` have been sanitized in place.

## Purpose and scope

Improve the reliability and methodology of the opt-in thesis research pipeline
using the full independent run below as evidence. Preserve the Claude CLI
synthesis contract, graceful degradation, bounded concurrency, defensive
validation, and existing risk protections. Legacy remains the default.

This document records proposed work; it does not change the behavioral contracts
in [the thesis workflow specification](../workflow/thesis-research.md).
One degraded run cannot establish whether the methodology improves trading
performance or whether the market offered no opportunities.

## Run reviewed

```sh
go run ./cmd/cfr run --research-mode thesis --json
```

The initial sandbox attempt could not resolve market-data hosts and was stopped.
The subsequent network-enabled run completed and supplies the findings here.

| Measure | Result |
| --- | --- |
| Run directory | `runs/2026-09-07T05-53-00` |
| Research engine | API, `deepseek-chat` |
| Synthesis engine | Claude CLI, `opus` |
| Duration | 647,657 ms, approximately 10m 48s |
| Funnel | 268 universe rows → 232 eligible unique tickers → 24 candidates → 12 researched |
| Outcome | Degraded |
| Final decisions | 0 trades, 2 watchlist, 10 rejected |
| Watchlist | ORCL, 2330.TW |
| Model calls | 65, all recorded as `done` despite downstream parsing failures |
| Reported completion tokens | 157,043; Claude usage unavailable |
| Error entries | 167, including repeated errors; 144 distinct strings |

Local artifacts, which are generated and should remain outside commits:

- [Final output](../../runs/2026-09-07T05-53-00/ideas.json)
- [Run metadata](../../runs/2026-09-07T05-53-00/metadata.json)
- [Chief assessment](../../runs/2026-09-07T05-53-00/chief-analyst.md)
- [Discovery evidence](../../runs/2026-09-07T05-53-00/data/discovery.json)
- [Company research](../../runs/2026-09-07T05-53-00/data/research.json)

These links require the local run directory. Do not publish raw artifacts before
redacting provider diagnostics: some contain an echoed credential. No credential
value is included in this plan.

## Findings from the outputs

1. **JSON failures affected half the shortlist.** Eight parsing/type errors
   affected ORCL, MU, ADBE, ZS, 9988.HK, and BSX. ZS and BSX finished with empty
   dossiers. All model calls nevertheless remained `done` in metadata, obscuring
   the distinction between receiving a response and obtaining usable research.
2. **Finalization changed decision meaning.** Claude put BSX on the watchlist
   because its research failed; Go changed it to rejected. Nine Chief reasons
   were replaced by challenger reasons, including incorrect timing claims that
   Claude had corrected.
3. **Research requests often added no evidence.** The run recorded 20 unsupported
   request errors and nine failed passage searches. Models emitted unsupported
   operations or omitted the request kind. The supported `news` operation itself
   performs no additional retrieval.
4. **Global evidence coverage was uneven.** Discovery news reached 118 of 119
   eligible US names, versus 17 of 113 European and Asian names. All 715 full-body
   discovery articles came through Benzinga; the remaining 933 items were Yahoo
   headlines. Multiple articles do not necessarily represent independent events
   or independent reporting.
5. **Source reading and evidence presentation were fragile.** Fourteen document
   attempts failed: nine oversized responses, two 404s, one 403, one 503, and one
   invalid public URL. TSMC's bundled issuer page returned 404. The Chief's text
   allocation was approximately 173–500 characters per document, often too small
   to substantiate a detailed claim.
6. **Temporal reasoning contained clear errors.** Alibaba's challenger called
   September 24 outside the holding window, although it is 13 weekdays after
   September 7 under the application's estimated calendar. Stellantis's final
   rejection said September 8 had already passed. Both errors survived into the
   final output.
7. **Diagnostics need deterministic accounting and redaction.** The Chief's
   aggregate error counts did not match the artifacts. Metadata contains 118
   Yahoo errors, six AlphaVantage errors, eight JSON errors, 20 request-contract
   errors, nine passage-search errors, and six other provider errors. An
   AlphaVantage diagnostic echoed a credential into persisted evidence/errors.

## Methodology assessment

Useful foundations include persisted source evidence, researcher direction
blinding, explicit uncertainty, a separate challenger, deterministic risk checks,
and permission to return no trades. The BHP challenger caught a concrete causal
error: a copper thesis had relied on a lithium supply disruption.

The pipeline should distinguish rejecting a particular hypothesis from rejecting
the company in both directions. TTD's review established bearish deterioration
while concentrating on disproving a bullish rebound. That should prompt a bounded
assessment of a short thesis or an explicit explanation of why neither direction
is supported.

Several reviews treated public information as necessarily fully priced in, or
required a scheduled catalyst despite the specification allowing continuation
mechanisms. A stronger dossier should identify expectations, the remaining
discrepancy, and its plausible transmission to price within the holding window.
Publication alone proves neither full incorporation nor delayed incorporation.

Valid evidence IDs are necessary but do not establish that claims follow from
their sources. In discovery, Sony's lawsuit against Anthropic was described as
a liability for Sony. Sony did not reach the shortlist, but the error illustrates
the need for issuer-role and causal-attribution checks.

Operational gaps also need different outcomes from investment judgments. Boeing
lacked post-event prices after a reported weekend event. September 7 was a US
market holiday according to the [NYSE calendar](https://www.nyse.com/trade/hours-calendars).
An awaiting-repricing watchlist condition would better describe that evidence
limitation, while attribution and trade feasibility still require investigation.

## Prioritized implementation checklist

- [x] **P0 — Redact provider diagnostics before storage or model prompting.**
  Sanitize provider response bodies and error paths, including AlphaVantage.
  Sanitize retained artifacts without destroying the audit trail.
  Add regression coverage ensuring configured
  credentials cannot appear in reports, metadata, prompts, or logs.
- [ ] **P0 — Confirm credential rotation.** Revoke and replace the exposed
  Alpha Vantage credential through the provider account. Local redaction is
  complete, but provider-side revocation has not been confirmed.
- [x] **P0 — Establish an explicit thesis output schema.** Update
  `internal/agents/agents.go`, `agents/thesis-*.md`, and research decoding in
  `internal/orchestrator/thesis.go`. Remove inherited legacy `missing`/strength
  instructions; specify array types and request enums. Add one bounded schema
  repair attempt before substantive review. Never fabricate missing claims or
  silently promote repaired output to supported status.
- [x] **P0 — Separate research failure from thesis rejection.** Track transport,
  parsing, evidence, and review outcomes independently in shared types and run
  metadata. Update `internal/orchestrator/thesis_validate.go` so a failed review
  cannot turn a Chief watchlist decision into a substantive rejection or replace
  corrected reasoning. Preserve both review provenance and final decision reasons.
- [x] **P0 — Implement an explicit research request/result protocol.** In
  `internal/model/research.go` and `internal/orchestrator/thesis.go`, validate
  operations and return a result for each request: fulfilled, unsupported,
  unavailable, already attempted, or budget exhausted. Make cached-news behavior
  explicit. Stop repeated requests that cannot add evidence and keep all work
  within shared document, concurrency, and cancellation budgets.
- [x] **P1 — Compute temporal facts in Go.** Supply session boundaries, event
  ordering, evidence age, and comparable volatility units. Extend
  `internal/marketdata/research_calendar.go` and
  `internal/orchestrator/thesis_events.go` with verified exchange-calendar
  handling while retaining estimated status when coverage is incomplete. Check
  earnings eligibility before full company research; retain blocked names as
  post-event watchlist candidates where appropriate.
- [ ] **P1 — Improve retrieval reliability and regional coverage.** Implementation
  is present; live acceptance reopened on 2026-09-10 because non-US discovery
  coverage did not improve (16/113 versus 17/113 eligible names with news). Repair issuer
  seeds in `internal/marketdata/research_sources.go`, prioritize primary releases
  and recent quarterly evidence, reuse supplied full article bodies, rank
  discovered links, and handle oversized HTML safely while preserving bounded
  reads and public-destination checks. Measure usable coverage by region and
  manage provider quotas across runs rather than repeatedly spending on failures.
- [ ] **P1 — Supply claim-relevant evidence passages.** Implementation is present;
  live acceptance reopened after the September 9 artifact audit found a retrieved
  primary passage omitted from actual model inputs and a currency/ADS comparison
  error surviving exact-quote checks in a rejected dossier. Replace equal-length
  document prefixes in `promptDocuments` with claim-linked excerpts. Preserve
  issuer identity, reporting periods, event identity, and source lineage.
  Extend claim validation beyond citation-ID existence, including attribution
  and consistency checks. Persist the evidence selection used for each review.
- [x] **P1 — Review both directions under consistent methodological rules.**
  Require explicit long, short, and no-trade reasoning within a bounded research
  budget. Challenge unsupported priced-in assertions, allow evidenced
  continuation mechanisms, distinguish neutral positioning from negative
  evidence, and enforce the prohibition on chart-pattern reasoning. Keep
  runtime personas and workflow specifications synchronized.
- [x] **P2 — Convert this run's failures into sanitized regression fixtures.**
  Cover malformed JSON, wrong array types, empty dossiers, unsupported requests,
  BSX status preservation, Alibaba/Stellantis dates, Sony attribution, failed
  retrieval, and exhausted budgets. Include supported-plan and risk-gate paths
  because this live run produced no plans. Run focused package tests followed
  by `go build ./...`, `go test ./...`, and `go vet ./...`.
- [ ] **P2 — Evaluate with prospective paired runs after reliability fixes.**
  Compare legacy and thesis using matched timestamps and evidence snapshots at
  10 and 15 sessions. Track technical failures, regional coverage, empty runs,
  latency, complete token accounting, benchmark-relative directional calls, and
  execution costs separately. Retain empty/degraded runs and account for
  overlapping exposures. Do not change defaults or tune risk parameters from
  this single run.

## Implementation continuation — 2026-09-07

The worktree already contained the P0 implementation and an unfinished US
calendar. Continuation preserved those changes and completed:

- US 2026–2027 closure coverage and published early closes, with explicit
  estimated status elsewhere; computed date ordering, fifteen-session windows,
  evidence age, comparable volatility units, and exact benchmark boundary checks.
  Earnings checks precede full company research. Event/price deferrals remain
  watchlist conditions rather than company rejections.
- The repaired TSMC press-center seed, ranked issuer/results links, reuse of
  supplied full bodies, safe bounded extraction from oversized HTML, regional
  coverage artifacts, and persistent provider-confirmed daily quota exhaustion.
- Quoted claim passages, issuer roles, source/parent lineage, claim-focused prompt
  excerpts, persisted inputs for every review, and review coverage/attribution
  consistency checks. Exact quotations and consistent roles do not prove semantic
  entailment; the independent source review remains responsible for that judgment.
- Explicit long, short and no-trade cases, with the selected direction required
  to match the reviewed dossier. Runtime prompts apply consistent expectations,
  continuation, positioning and chart-pattern rules.
- Synthetic, sanitized regressions for the named temporal and attribution errors,
  missing reaction prices, truncated documents, repeated full-body requests,
  daily quota refusal and passage budgets, alongside the existing P0 parsing,
  request, failed-research and supported-plan/risk-gate fixtures.

The bundled schedule and seed were checked against the
[NYSE calendar](https://www.nyse.com/trade/hours-calendars) and
[TSMC press center](https://pr.tsmc.com/english). Global calendar coverage remains
partial; retrieval remains limited to accessible source text. Link ranking uses
URL relevance and date-like suffixes, not inferred publication timestamps.
Regional coverage is now measurable; improved live coverage has not been established.
Prospective paired research and credential rotation remain open. No live model
research, provider-key changes, or default/risk-parameter changes were performed
in this continuation.

## Validation already performed

During the review, `go build ./...`, `go test ./...`, and `go vet ./...` passed;
test results were cached. No source code was changed as part of the review.
The live run produced no execution plans, so it did not exercise final
trade-construction checks. Existing user changes were preserved.

Continuation validation completed successfully:

- Focused new regressions, then affected package suites with `-count=1`.
- `go build ./...`, `go test ./... -count=1`, and `go vet ./...`.
- `gofmt` on changed Go files and `git diff --check`.

Checks used `GOCACHE=/tmp/cfr-go-build` because the normal build cache is
read-only in the sandbox. Tests using local HTTP fixtures ran with loopback
access; the sandbox-only socket error was environmental, not an assertion
failure. The initial baseline also exposed the unfinished calendar's stale
US-estimate expectation; it now tests an uncovered foreign market separately
from published US coverage. No external model accounts or live market research
were used for validation.

## Evaluation implementation continuation — 2026-09-08

The remaining P2 evaluation item now has additional local tooling:

- Explicit pair manifests audit run identities, generation-time tolerance,
  declared registration time, matching selection universes and SHA-256 hashes
  of declared common input artifacts. Mismatches and missing runs remain visible.
- Matched declared inputs can report per-pair differences for each control arm
  at 10 and 15 sessions; empty, pending and unavailable arms do not become zero
  returns. Same-ticker overlapping observations are identified beyond weekly
  deduplication, including opposite-direction exposure.
- Every run directory is retained in the comparison, including missing/malformed
  outputs. Diagnostics include failed calls, invalid/repaired payloads, source
  error counts, regional coverage availability and latency.
- API usage is retained across every attempt, including truncated/failed calls.
  Prompt/completion/total and cache/reasoning subsets remain separate; unknown
  CLI usage is explicit. Compatibility completion totals include all attempts.
- Offline comparison reads saved prices without changing calibration. An
  optional round-trip cost assumption produces a separate closed-trade replay
  scenario. Exact benchmark boundaries and saved quant anchors prevent holiday
  or intraday comparisons from silently using different price dates.

This does not complete prospective evaluation. Snapshot hashes verify only
the declared file scope, and registration time is an operator declaration.
The tooling does not freeze all provider inputs or reconstruct missing evidence.
Deliberately collected common snapshots and live paired runs, followed by
10/15-session outcomes, are still required. No live model runs, scheduled
collection, credential rotation, or parameter tuning were performed.

Evaluation continuation validation passed: affected CLI, scoreboard and
orchestrator suites, followed by `go build ./...`, `go test ./... -count=1`
and `go vet ./...`. Regression fixtures cover failed/empty results, missing and
zero token counts, retry usage, snapshot mismatches, pending/empty paired arms,
overlapping exposures, benchmark anchors, cancellation, and execution cost/fill
windows. Changed Go files were formatted and `git diff --check` passed.
Checks used `GOCACHE=/tmp/cfr-go-build`; local HTTP fixtures ran with loopback
access. The build emitted a read-only module stat-cache warning and exited
successfully. No live model research was used for validation.

## Prospective workflow continuation — 2026-09-09

Implemented the collection and execution workflow behind `cfr research-pair`:

- Register the selection, settings, persona/source hashes, randomized arm order,
  10/15-session horizons and optional execution-cost assumption before collection.
- Freeze one bounded evidence corpus and give both arms identical hashed inputs,
  a common generation clock, private caches, disabled model tools, and no
  historical calibration or postmortem feedback.
- Preserve failed and not-started arms, cancellation and input-tampering errors.
  Refresh outcome prices into separate artifacts and save evaluation provenance.
- Record Claude structured token usage across attempts, including cache tokens,
  while marking incomplete/absent telemetry explicitly.
- Require exact completed session windows for outcome measurement. Future
  endpoints remain pending; missing matured bars and uncovered exchange
  calendars remain unavailable.

See [the workflow](../workflow/thesis-research.md#collecting-a-prospective-frozen-pair)
for commands, artifact layout, limitations and the distinction between frozen
evaluation and ordinary research.

The P2 evaluation item remains open until actual paired research and its future
10/15-session outcomes have been collected and assessed. Implementing this
workflow cannot supply future observations or establish a performance edge.
Credential rotation also remains unconfirmed. No default or risk parameters
were tuned.


The live trial is prepared as:

```sh
/tmp/cfr-research-pair research-pair --out .data/pair-2026-09-09-aapl --ticker AAPL --omit-alphavantage --timeout 15m
```

The initial automatic approval review rejected execution pending explicit
authorization for live service usage. The user subsequently approved the
proposed run, and it launched using the existing configured engines, with
Alpha Vantage excluded and a 15-minute total deadline. Its artifacts are kept
under `.data/pair-2026-09-09-aapl`; failed or empty outcomes remain part of
the evaluation. Later price collection uses
`cfr research-pair --evaluate .data/pair-2026-09-09-aapl --refresh`.

Final continuation validation passed: `go build ./...`,
`go test ./... -count=1`, `go vet ./...`, formatting checks and
`git diff --check`, with `GOCACHE=/tmp/cfr-go-build`. Local HTTP fixtures
used approved loopback access; live model research was not used by the tests.
The build's read-only module stat-cache warning did not prevent success.

A read-only subagent review found and then verified fixes for stale intraday
outcome promotion, fatal-arm telemetry loss, and filing notices being promoted
to full documents. New regressions cover those failures, registration ordering,
snapshot immutability, canceled/tampered experiments, failure retention and
outcome refresh isolation. The reviewer reported no remaining material defects
within that bounded review.


## Approved live pair — 2026-09-09

The user approved the previously proposed live action. The AAPL pair completed
with exit code 3: both arms degraded because the normal Claude Chief CLI reported
that its organization has disabled Claude subscription access. DeepSeek research
completed: six thesis calls and five legacy specialist calls. Thesis retained
zero plans and one rejection decision; legacy produced one mechanical fallback
idea. This is retained as a failed/degraded synthesis trial, not a successful
comparison of the two complete research pipelines.

Artifacts are under `.data/pair-2026-09-09-aapl`, with a readable `review.md`.
The pair audit verified identical corpus hashes and generation timestamps
(`2026-09-09T13:53:09Z`), prior registration and all copied input hashes.
A separate outcome-price snapshot and initial evaluation were saved. The
legacy directional windows remain pending until September 23 and September 30,
after the conservative 22:00 UTC publication cutoff. Thesis has no shipped plan,
so this pair cannot yield a shipped-arm performance difference even afterward.

This run exposed and reproduced two additional issues now corrected: frozen
Quant needed the common macro backdrop, and regime/computed roles should not
gain artificial company-provider errors; comparison text must show unavailable
returns when there are no measured observations. Original run artifacts and
their diagnostics remain unchanged.

A future full pair requires restored authorized Claude CLI access. Official
[Claude Code error documentation](https://code.claude.com/docs/en/errors)
identifies this as a server-side organization restriction, which cannot be
overridden locally. Alpha Vantage remained excluded; revocation/replacement
of the exposed credential is still unconfirmed. The prospective evaluation
item remains open; no research defaults or risk parameters were tuned.

Validation after the live-run fixes passed: focused regressions followed by
`go test ./... -count=1`, `go build ./...`, `go vet ./...`, formatting and
`git diff --check`. Price-only refresh succeeded, and a final read-only audit
rechecked both generation snapshot hashes, common timestamps and all registered
input hashes. No original generation artifacts were rewritten.


## Chief backup correction — 2026-09-09

The user correctly identified that the existing DeepSeek Chief API fallback
should cover the Claude organization failure. Inspection found that both
pipeline branches already implemented it, but local `cfr.toml` had only the
cheap-research API credential: no `[chief_fallback]` section or fallback
environment credential was configured. The prior statement that restored
Claude access was the only route to a full synthesis was incomplete.

At the user's request, the local ignored configuration now explicitly enables
the Chief fallback using the existing DeepSeek account credential. Claude
remains primary; no automatic credential inheritance or repository-wide default
was introduced. The configured fallback model is `deepseek-v4-pro`, verified
against the account's model list, with a 32,768-token output limit. Current
[DeepSeek documentation](https://api-docs.deepseek.com/quick_start/pricing)
lists the old `deepseek-reasoner` name as deprecated.

A new thesis integration regression reproduces the Claude subscription-access
failure and verifies that the configured DeepSeek fallback completes, its
provenance is saved, and its valid no-trade result survives. Existing legacy
tests cover both failed-Claude and malformed-Claude-output fallback triggers.
A successful backup still leaves the run degraded to preserve primary-failure
provenance.

Automatic approval review rejected a second full paired experiment over
additional inference cost. A smaller verification was approved and completed:
one synthetic DeepSeek request, no research documents, capped at 1,024 output
tokens. It returned the expected empty ideas/decisions JSON with finish reason
`stop`; reported usage was 103 prompt and 52 completion tokens (155 total).
This verifies the live backup endpoint together with the hermetic pipeline
tests; it does not represent a second full research experiment. The original
pair and all of its observations remain intact.

Backup continuation validation passed: focused legacy/thesis fallback tests,
`go test ./... -count=1`, `go build ./...`, `go vet ./...`, formatting and
`git diff --check`. The local configuration remains Git-ignored and its
credential was neither printed nor added to tracked files.


## Completed full independent validation — 2026-09-10

At the user's explicit request, a full independent thesis run across all four
curated universes completed as `runs/2026-09-10T06-49-03`. It took 727,117 ms
(12m 7s) and exited 3, degraded. The funnel was 268 universe rows → 232 eligible
unique tickers → 24 candidates → 12 selected companies. ORCL and ADBE were
correctly deferred before model research for verified same-day earnings.

DeepSeek Chief fallback completed after primary Claude's organization-access
failure: `deepseek-v4-pro`, 107,800 ms, 257,339 input and 4,954 output tokens.
The final output contained zero plans, eleven watchlist decisions and one
substantive rejection (BHP.AX). Nine of the ten companies that reached research
failed at the 8,192-token output limit. Ten logical calls exhausted both attempts;
those twenty capped attempts consumed 163,840 reported completion tokens.
This is not evidence that all companies lacked opportunities.

The status/provenance fixes worked: all nine research failures stayed watchlist
with `blocked=research_failure`; BSX's Chief watchlist was preserved. All ten
researched companies retained the Chief's final reason. The two event deferrals
received the deterministic earnings reason. Sixty-nine structured request
results exposed 27 fulfilled, 12 already attempted, 23 unavailable and seven
unsupported URLs. All seven URL refusals enforced supplied-source provenance.

Reported completion tokens increased from 157,043 to 438,995 (2.80×; baseline
Claude usage unavailable). Non-US discovery news coverage was 16/113 versus
17/113, so instrumentation has improved without demonstrated coverage gains.
Source-document failures were nine versus fourteen, with zero versus nine
oversized failures, but different retrieved documents prevent a causal estimate.

The requested run and assessment are complete. The reliability objective is not:

- [ ] **P0 — Resolve output truncation under realistic structured research loads.**
  Fit dossiers and accumulated context to configured budgets and avoid identical
  retries at an exhausted output cap. Keep failure and watchlist protections.
- [ ] **P1 — Verify relevant passages survive actual model-input selection.**
  Exercise navigation-heavy primary sources with material statements beyond the
  leading prefix, through research, challenger and Chief inputs.
- [ ] **P1 — Validate numerical listing/currency/ADS comparability.** Exact quotes
  and issuer roles are insufficient for valuation comparisons. An independent
  audit found an HKD-local-price/USD-ADS-target mismatch in a rejected Alibaba
  dossier from the interrupted September 9 attempt.

The existing regional coverage and passage checklist items above are reopened
for live acceptance; their implementations and regression history remain intact.
The interrupted `2026-09-09T19-34-00` attempt is retained separately and is not
counted as a completed run. Neither this unpaired before/after audit nor the
previous degraded AAPL pair establishes improved investment performance.

The full assessment, comparison script and JSON counts are saved under
[the September 10 validation directory](../../.data/independent-validation-2026-09-10/review.md).
Generated artifacts remain local and ignored. Validation checked the actual
exit code, metadata, outcomes, Chief/final decision preservation and reproducible
counts. Four configured secret values had no exact matches across 136 generated
run/validation files. This assessment changed documentation only; no new runtime
code, provider configuration or risk defaults were introduced, and no additional
live experiment was started.
