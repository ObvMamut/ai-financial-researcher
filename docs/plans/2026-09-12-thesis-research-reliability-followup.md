# Thesis research reliability follow-up plan

Date: 2026-09-12  
Status: implemented and locally verified on September 13; live reliability,
regional source gains and prospective evaluation remain open.  
Predecessor: [September 7 improvement plan](2026-09-07-thesis-research-improvements.md).

See the [implementation and verification record](2026-09-13-reliability-implementation.md)
for per-item states and the [bounded acceptance runbook](2026-09-12-reliability-acceptance.md)
for the remaining operator checks. Open checkboxes below retain live-dependent
acceptance requirements even where implementation and local verification are complete.

The September 10 full independent run demonstrated useful safeguards but failed
to produce reliable company research. This plan carries forward the unresolved
findings, prioritizes their causes, and defines observable acceptance criteria.
The first objective is completed, evidence-grounded research within bounded
resource usage; prospective trading-performance evaluation follows that work.

## Scope

- In: output-limit handling, prompt and response sizing, usable source passages,
  numerical comparability, temporal and expectations reasoning, regional evidence
  availability, diagnostics, regression coverage and staged evaluation.
- Out: changing trading-risk thresholds, forcing a minimum number of trades,
  changing the default legacy pipeline, replacing primary Claude synthesis,
  introducing a new paid provider, or claiming improved investment returns from
  this audit. This document records planned work; it starts no research run.

Preserve the separate, explicitly configured DeepSeek Chief fallback, bounded
concurrency and cancellation, defensive parsing, source provenance, redaction,
and failure/watchlist distinctions. Keep historical artifacts and `agents.v1/`
intact. Behavioral changes must update the relevant
[workflow specification](../workflow/thesis-research.md) and runtime personas.

## Evidence and interpretation

The completed run is `runs/2026-09-10T06-49-03`. The comparison baseline is
`runs/2026-09-07T05-53-00`, which motivated the predecessor plan. The detailed
[audit](../../.data/independent-validation-2026-09-10/review.md) and
[reproducible counts](../../.data/independent-validation-2026-09-10/comparison.json)
remain local generated artifacts; the findings below make this plan readable
without those directories.

| Measure | September 7 baseline | September 10 completed run |
| --- | ---: | ---: |
| Universe rows / eligible unique companies | 268 / 232 | 268 / 232 |
| Candidates / selected companies | 24 / 12 | 24 / 12 |
| Runtime | 10m 48s | 12m 7s |
| Plans / watchlist / rejected | 0 / 2 / 10 | 0 / 11 / 1 |
| Logical model calls / attempts | 65 / 65 | 44 / 65 |
| Reported completion tokens | 157,043 | 438,995 |
| Non-US companies with discovery news | 17 / 113 | 16 / 113 |
| Failed source-document records | 14 | 9 |
| Oversized source-document failures | 9 | 0 |

Baseline Claude usage is unavailable; new primary-Claude usage is incomplete.
The token comparison is not a dollar-cost comparison. Dates, selected companies,
provider responses and Chief models differ, so this is an operational audit,
not a controlled estimate of the implementation's effect. Fewer document
failures alone do not establish improved retrieval because documents differ.

### What worked and must remain protected

- DeepSeek `deepseek-v4-pro` completed final synthesis after primary Claude failed
  because of an organization access restriction. Fallback provenance was saved;
  the overall run correctly remained degraded.
- Nine failed company research paths stayed watchlist with
  `blocked=research_failure`. BSX's Chief watchlist was preserved, correcting the
  baseline's incorrect conversion to rejection. All ten companies that reached
  research retained the Chief's final reasoning.
- ORCL and ADBE were deferred before company model calls because the captured
  verified earnings dates triggered the event gate. Their outcomes were
  `not_run`, distinct from failures.
- Request accounting exposed 69 results: 27 fulfilled, 12 already attempted,
  23 unavailable and seven unsupported. All seven unsupported cases were URLs
  absent from supplied/discovered evidence; refusing them protected provenance.

### What failed or needs improvement

| Finding | Evidence and consequence | Certainty |
| --- | --- | --- |
| Output truncation dominates research failures | Nine of ten attempted companies failed at 8,192 output tokens. Ten logical calls exhausted two attempts each, consuming 163,840 completion tokens on those failed calls. Only BHP.AX completed research/review and received a substantive rejection. | Observed in completed run |
| Retry handling repeats an exhausted request | `apiengine.go` returns a generic error for finish reason `length`; `runner.go` routes it through ordinary retries without changing the prompt or output cap. | Confirmed in code and run artifacts |
| Context and output growth need explicit control | The run reported 1,748,299 input and 438,995 output tokens. Chief fallback alone received 257,339 input tokens. The current document-text budget does not bound all serialized dossiers, claims, requests and instructions. | Size observed; contribution of each prompt component still needs measurement |
| Retrieved evidence does not always reach the model | The supplemental TSMC audit found a material passage at character offset 5,339 in a retrieved primary release, while saved inputs retained a short navigation-heavy prefix. | Observed in completed company records from the interrupted September 9 attempt |
| Exact quotes do not establish numerical comparability | A rejected Alibaba dossier compared a 9988.HK local price with a USD BABA target without currency/ADS normalization. Quote and issuer-role checks did not catch it. | Supplemental September 9 finding; no shipped trade |
| Temporal inputs and methodological prompts remain insufficient | Supplemental reviews treated missing future outcomes as research gaps and inferred expectations from trailing price strength. Explicit long/short/no-trade cases existed but did not prevent these reasoning errors. | Supplemental qualitative evidence, not a measured failure rate |
| Regional coverage remains poor | US news reached 118/119 companies; Europe 8/47; Asia-Pacific 8/66. Sixteen Asia-universe names were labeled other/unknown by new telemetry. | Observed in completed run; comparison uses consistent universe membership |

The interrupted run `2026-09-09T19-34-00` never completed Chief synthesis. Its
completed TSMC, Alibaba and Stellantis company records inform supplemental
regressions only; canceled companies are excluded from research-quality counts.
Zero plans in September 10 principally reflects failed research and does not
establish that the market offered no opportunities.

## Action items and acceptance criteria

- [x] **P0 — Handle output-limit failures separately from transient failures.**
  Update `internal/orchestrator/apiengine.go`, `runner.go` and the thesis call
  path with a distinguishable truncation outcome. Stop unchanged automatic
  retries after an explicit output-limit response. If bounded recovery is used,
  make the changed request strategy and its shared attempt/token budget explicit;
  do not treat partial JSON as successful research or increase spend silently.
  **Acceptance:** a local HTTP fixture returning `finish_reason=length` produces
  one unchanged request, accurate usage and a failed outcome. Any recovery is
  separately identifiable and bounded. Transient failures still retry according
  to policy, and cancellation interrupts both paths.

- [ ] **P0 — Fit complete research inputs and outputs to role budgets.**
  Profile serialized prompts by component before choosing limits. Review
  `internal/orchestrator/thesis.go`, `thesis_schema.go`,
  `internal/agents/agents.go`, `internal/model/research.go`, configuration and
  `agents/thesis-*.md`. Bound claim counts, quoted material, narrative fields,
  request history and repeated dossier content while retaining opposing cases,
  material uncertainty and evidence lineage. Reserve room for complete output;
  record input estimates distinctly from actual provider token usage. Compact
  Chief context without hiding failed companies or counterevidence.
  **Acceptance:** representative multi-round, revision, challenge and twelve-name
  Chief fixtures stay within declared role budgets and retain required evidence
  and outcomes. Oversized inputs fail or compact explicitly. Existing saved
  schemas remain readable. Local size checks alone do not close live truncation
  acceptance; the staged run in the final item must confirm it.

- [x] **P1 — Deliver relevant passages through every review stage.**
  Fix selection in `internal/orchestrator/thesis_passages.go` and its call sites;
  inspect extraction in `internal/marketdata/research.go` and source helpers.
  Initial research must find substantive text before claims exist; later reviews
  must retain quoted passages, context, issuer, event and reporting period.
  Preserve omissions and truncation explicitly within the smaller total budget.
  **Acceptance:** a sanitized navigation-heavy TSMC fixture with relevant text
  beyond character 5,339 supplies that text to initial research, challenger and
  Chief. Assert against persisted inputs, not just the selector helper. Include
  conflicting passages, multilingual text and a genuinely unavailable passage.

- [x] **P1 — Validate numerical comparisons across listings and currencies.**
  Extend research claim validation and structured provenance where needed in
  `internal/model/research.go`, `internal/orchestrator/thesis_validate.go` and
  `thesis_passages.go`; reuse existing exchange, FX and ADR mappings. A material
  target/upside comparison needs security identity, currency, per-share/ADS
  basis, valuation date, conversion ratio and dated FX where applicable.
  Missing or incompatible inputs remain unresolved rather than supporting a
  computed upside claim. Broader semantic entailment still requires review.
  **Acceptance:** the Alibaba mismatch is blocked; same-listing and correctly
  normalized synthetic comparisons pass; unknown ADS ratio, stale/missing FX,
  reversed ratio and differently dated targets are handled explicitly. Preserve
  compatibility with historical dossiers that lack these optional fields.

- [x] **P1 — Distinguish future information from missing historical evidence.**
  Tighten `thesis_time.go`, `thesis_events.go`, validation and researcher/
  challenger prompts around the run anchor. Future results and unelapsed price
  sessions are not fetchable evidence; use conditional scenarios or the existing
  event/price watchlist rules. Require cited expectations or a clearly labeled
  inference for priced-in claims; publication and trailing strength alone do
  not establish full incorporation. Retain legitimate continuation theses.
  **Acceptance:** fixtures cover future releases, before/after-market events,
  holidays, date-only and estimated calendars, truly missing past prices, neutral
  positioning and unsupported priced-in assertions. No fixture gains a supported
  thesis by replacing uncertainty with a guessed fact.

- [ ] **P1 — Improve usable non-US evidence and request effectiveness.**
  Review `internal/marketdata/research_sources.go`, `research_coverage.go`,
  issuer/link discovery and the request loop in `thesis.go`. Fix region accounting
  using a documented consistent mapping. Extend accessible primary issuer and
  exchange sources using existing providers/public endpoints; distinguish source
  access, useful extracted text, model-visible passages and independent origin.
  Give models actionable known links and prior request outcomes so they do not
  repeatedly ask for inaccessible URLs, cached news or future observations.
  Preserve URL checks and shared provider/document budgets.
  **Acceptance:** every eligible ticker belongs to exactly one denominator;
  repeats incur no provider call; blocked/401/403 sources remain explicit.
  On a fixed, declared European/Asian evaluation panel, demonstrate additional
  usable primary evidence in each region and trace it into model inputs. Report
  absolute counts, unchanged denominators and failures; more telemetry or
  relabeling alone does not satisfy coverage acceptance.

- [x] **P2 — Turn the audit into reusable regression and run diagnostics.**
  Add sanitized fixtures for the issues above using existing fake CLIs and HTTP
  fixtures. Extend `internal/scoreboard/research_diagnostics.go` and saved usage
  where necessary to report attempted/not-run companies, completed research,
  truncations, recovery attempts, evidence visibility and per-stage usage. Keep
  unknown historical usage distinct from zero and separate primary failure from
  successful fallback. Report zero-trade causes without conflating abstention,
  failed research and substantive rejection.
  **Acceptance:** reproducible diagnostics recover the September 10 totals and
  distinguish two deferrals, nine failures and one completed rejection. Existing
  BSX preservation, fallback, redaction, risk gates and supported-plan tests
  continue to pass; include cancellation and exhausted-budget regressions.

- [ ] **P2 — Verify implementation, then demonstrate live reliability and evaluate.**
  Run focused package tests, then `go test ./...`, `go build ./...`,
  `go vet ./...`, formatting and `git diff --check` with the required Go toolchain.
  Use an independent review of changes and acceptance evidence. Once local
  checks pass, perform bounded validation with the existing configured engines:
  start with representative company cases, then a full independent run. Record
  the exact revision, persona/configuration hashes without secrets, budgets,
  dates and all failed attempts. Do not start recurring or unbounded work.
  **Acceptance:** all companies that actually reach model research complete a
  readable dossier and substantive challenge in the designated acceptance run;
  explicit early deferrals are counted separately. No unchanged truncation
  retries occur. Failure to meet this gate keeps live acceptance open, even if
  tests pass or some ideas ship. Report usage and latency alongside evidence
  quality. Then use the existing registered frozen-pair tooling for matched
  legacy/thesis evaluation at 10 and 15 sessions, retaining empty/degraded arms,
  mature-outcome requirements, benchmark comparisons and execution-cost
  assumptions. Better returns remain unproven until sufficient prospective
  evidence exists; a positive trade count is not an acceptance criterion.

## Execution order and completion tracking

Complete the two P0 items first. Passage delivery depends on the resulting
context budget. Numerical validation and temporal reasoning can be developed
independently; coverage work can proceed alongside them with separate file
ownership. Add regression evidence with each change. Integrate before the final
independent review and bounded live acceptance; performance evaluation comes last.

For each item, record three separate states in continuation notes:
**implementation**, **local verification**, and **live acceptance where
applicable**. Attach commands/results or run identifiers. A checkbox closes only
when its stated acceptance criteria are met, not when code merely exists.

## Decisions and remaining operator questions

- Complete prompt profiles now record component bytes and heuristic token
  estimates separately from actual provider usage. Declared prompt/response
  budgets are triage 96/12 KiB, researcher 96/20 KiB, challenger 96/12 KiB and
  Chief 192/24 KiB. Provider output caps are unchanged; explicit truncation has
  no recovery call. Live fit remains to be measured.
- The fixed panel is ASML.AS, STLAM.MI, NOKIA.HE, 2330.TW, 9988.HK and BHP.AX.
  Seed corrections and synthetic passage/coverage checks are implemented;
  reproducible primary-source gains through the application reader in both
  regions remain to be demonstrated on a captured corpus.
- Provider-side rotation of the previously exposed Alpha Vantage credential is
  still unconfirmed in the predecessor plan. Keep that operator item visible;
  a local redaction check does not establish revocation.
