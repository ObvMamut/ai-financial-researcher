# Selectable Chief Analyst and thesis research reliability

- Date: 2026-09-15.
- Status: detailed implementation plan; implementation and new live acceptance pending.
- Baseline: `runs/2026-09-15T17-00-30` on the existing dirty working tree.
- User decision: DeepSeek is the primary Chief for now; Claude remains selectable.

## Objective and settled decisions

Make the existing Chief Analyst role work directly through DeepSeek during the
current Claude subscription pause, while retaining a one-setting switch back to
the Claude CLI. Complete company research reliably within declared resource
budgets, preserve evidence through review, and make operational failures easy to
diagnose. Evaluate investment performance after those reliability requirements pass.

The following decisions are settled:

- Activate API Chief selection in this project's local configuration. Reuse the
  existing dedicated DeepSeek Chief endpoint, credentials, model and output cap.
  The audited fallback model was `deepseek-v4-pro`; this identifies the observed
  configuration, not a claim about the provider's current model catalogue.
- Keep the cheap researcher configuration independent of the Chief. Selecting API
  for both roles must not accidentally put the Chief on the cheap research model.
- Preserve both Chief personas: `chief-analyst` for legacy research and
  `thesis-chief` for thesis research. Engine selection does not change their jobs.
- Keep Claude available through explicit selection. Do not schedule a switch next
  month or attempt Claude automatically while DeepSeek is primary.
- Preserve existing configurations that omit the new selector. This project's
  explicit API selection changes its active default without changing all users'
  historical behavior.
- Retain declared provider caps, bounded retries, cancellation and the existing
  one-repair allowance. Improve representation and allocation before proposing
  larger limits or additional calls.
- Judge completion by usable research and substantive review. A completed,
  evidence-grounded no-trade result is valid; a failed investigation remains failed.

The user's engine decision supersedes the earlier Claude-only primary constraint
for this planned change. Update the shared development guidance when implementing
the engine change so future work follows the same policy.

### Scope

- **In:** Chief configuration/routing in legacy and thesis, headless/TUI/frozen
  pairs, response and prompt representation, recovery, evidence delivery, source
  coverage, diagnostics, compatibility, tests and bounded acceptance.
- **Out:** changing scoring weights or risk thresholds, expanding index samples,
  introducing new paid providers or credentials, forcing trade counts, automatic
  calibration changes, recurring research or unrelated working-tree cleanup.
- The post-mortem remains a cheap-research role; changing Chief selection must not
  move that work to the Chief model.

## Evidence from the completed run

The live command was `go run ./cmd/cfr run --research-mode thesis --json`, using all
four index samples. Artifacts are local generated files and may be absent from a
fresh checkout:

- [Run metadata](../../runs/2026-09-15T17-00-30/metadata.json)
- [Published results](../../runs/2026-09-15T17-00-30/ideas.json)
- [Company research](../../runs/2026-09-15T17-00-30/data/research.json)
- [Discovery coverage](../../runs/2026-09-15T17-00-30/data/discovery-coverage.json)
- [Chief input profile](../../runs/2026-09-15T17-00-30/data/input-chief-analyst.json)

| Measurement | Observed value |
| --- | ---: |
| Runtime | 427,744 ms, approximately 7m 08s |
| Universe rows / eligible unique companies | 268 / 232 |
| Ranked rows | 267; one excluded |
| Candidate budget / shortlist | 24 / 12 |
| Usable dossiers / completed reviews | 3 / 3 |
| Failed company workflows / deferred companies | 9 / 0 |
| Supported dossiers / published plans | 0 / 0 |
| Final decisions | 11 watchlist, one rejected |
| Logical calls / dispatched attempts | 59 / 56 |
| Compaction calls | 13: seven succeeded, six failed |
| Schema repair calls | Two |
| Input-capacity failures before dispatch | Three |
| Reported prompt / completion tokens | 938,326 / 259,216 |
| Application outcome / exit | Degraded / 3 |

All 55 HTTP completions with a recorded finish reason reported `stop`. The Claude
attempt had incomplete usage. This run's capacity failures were local byte-budget
failures, not observed provider token truncation. Usage totals are reported counts,
not a dollar-cost estimate. `go run` returned wrapper exit 1 while reporting the
application's `exit status 3`; direct binary execution is preferable for exit-code
acceptance.

### Failure attribution

| Companies | Last blocking event | Interpretation |
| --- | --- | --- |
| LLY, BAC, NOKIA.HE, TTD, REGN, 9988.HK | Complete response still exceeded 20,480 bytes after compaction | Six failed research workflows |
| SNOW, OKTA | Revision prompt exceeded 98,304 bytes | Two calls never dispatched |
| ORCL | Final challenger prompt exceeded 98,304 bytes | Final review never dispatched |
| JNJ, TTE.PA | Completed final challenge requested revision | Completed watchlist research with unresolved issues |
| ASML.AS | Completed final challenge rejected the thesis | Substantive rejection |

Primary Claude failed with disabled subscription access. The configured DeepSeek
fallback completed. Fixing that engine choice removes an expected primary failure;
it does not repair the nine earlier company failures.

### Capacity evidence and limits of the diagnosis

| Failed compaction | Raw response bytes | Compact JSON estimate |
| --- | ---: | ---: |
| LLY | 20,493 | 20,326 |
| BAC | 21,289 | 20,894 |
| NOKIA.HE | 20,795 | 20,735 |
| TTD | 23,583 | 21,766 |
| REGN | 21,990 | 21,071 |
| 9988.HK | 21,328 | 21,224 |

The compact column was measured by parsing and serializing with `jq`; exact Go
whitespace-compaction results need regression assertions. This is evidence that
formatting overhead can matter, especially for LLY, not proof that all six
dossiers can be recovered through formatting. These responses had 12–17 claims
and 23–29 passages. Protected content was substantial, but the audit did not prove
that their minimum valid representations exceeded the total budget.

Input failures were SNOW 98,396 bytes (+92), OKTA 103,077 (+4,773), and ORCL
102,413 (+4,109). The builder includes evidence text, metadata, prior dossier,
original compaction narratives, temporal facts, request history and review issues.
Some profile components currently group multiple sections together. Component
counts must be improved before attributing all excess to one section.

The Chief prompt was already **158,528 of 196,608 bytes** with only three usable
dossiers; its company board alone occupied 142,756 bytes. A realistic twelve-company
board is a separate capacity risk that must be tested. The current measurement does
not establish its exact size or that it is impossible to represent within budget.

Discovery news reached 118/119 US companies, 7/47 European companies and 9/66
Asia-Pacific companies. The run recorded 93 Yahoo unresolved-symbol warnings,
six document-source failures, and one stale shortlisted price (`9988.HK`). These
are retrieval/freshness concerns independent of dossier serialization.

## Target configuration and execution behavior

### Proposed public configuration

These names are proposed for implementation and do not exist yet:

| Setting | Contract |
| --- | --- |
| `chief_engine` | `api` or `claude`; omitted retains historical Claude selection |
| `[chief_api]` | Dedicated `base_url`, `model`, `api_key`, `max_tokens` for primary API Chief |
| `CFR_CHIEF_ENGINE` | Overrides file selection |
| `CFR_CHIEF_API_BASE_URL`, `CFR_CHIEF_API_MODEL`, `CFR_CHIEF_API_KEY`, `CFR_CHIEF_API_MAX_TOKENS` | Override the dedicated API Chief fields |
| `--chief-engine` | Final override for `run` and model-running `research-pair` |
| `[chief_fallback].enabled` / `CFR_CHIEF_FALLBACK_ENABLED` | Optional explicit enable/disable for the existing Claude-to-API fallback |

Use the established precedence: built-in defaults, user config, project config,
environment, command flags. An invalid enum or explicitly invalid numeric value
must produce a configuration error before data acquisition.

### Compatibility and migration matrix

| Resolved configuration | Primary | Fallback behavior |
| --- | --- | --- |
| No new selector; no fallback key | Claude CLI | Disabled, as before |
| No new selector; existing fallback key; enabled omitted | Claude CLI | Existing API fallback remains enabled |
| `chief_engine=claude`; fallback enabled explicitly false | Claude CLI | Disabled even when old credentials remain |
| `chief_engine=api`; valid dedicated Chief API settings; fallback enabled omitted/false | Configured API Chief | Disabled |
| `chief_engine=api`; missing required Chief API settings | No run | Configuration error before provider/model calls |
| `chief_engine=api`; fallback explicitly enabled true | No run | Explain that this first increment supports fallback only after Claude primary |

The first increment needs no generalized provider chain. Keep the existing
Claude-to-API fallback, and use primary API execution with no fallback for the
current project. Retained fallback credentials alone must never dispatch a second
identical DeepSeek call after primary API failure.

During implementation, explicitly copy this project's existing Chief endpoint,
model and output cap into the new dedicated fields and reuse its existing Chief
credential through the corresponding local setting or environment variable. Do
not source the Chief key implicitly from `[api]`, `[local]` or `DEEPSEEK_API_KEY`.
Keep local secrets out of documentation, snapshots and commits. A missing selector
must never implicitly migrate credentials or rewrite user files.

Switching back is `chief_engine=claude` or its environment/command override. The
existing Claude binary and model settings remain available. Re-enabling its API
fallback is a separate explicit choice when the project has disabled it.

### Chief execution contract

- Resolve the primary engine once per run into its transport, model, dedicated
  API settings or CLI binary, and synthesis policy.
- Route both initial Chief synthesis and the one corrective Chief call through
  that resolved configuration in both research modes.
- Preserve synthesis timeouts and `synthesis_max_attempts` for primary calls.
  The engine type must not determine whether a call is research or synthesis.
- Give Chief calls their own API settings. The existing worker pool has one API
  configuration for cheap calls; changing only `CLIClaude` to `CLIApi` is unsafe.
  Prefer a small shared Chief execution helper over restructuring the whole pool.
- Keep format parsing and trading validation specific to the pipeline. Legacy
  still uses base scores and its existing degraded path; thesis still requires
  reviewed evidence and has no mechanical trade fallback.
- Keep fallback attempt/time budgets and corrective behavior explicit. This
  change preserves the existing fallback's lack of an extra corrective call.
- Preserve frozen-run restrictions on model tools and evidence acquisition.

## Implementation work packages

Each package has three separate completion states: implementation, local
verification, and live acceptance where applicable. Check off a package only when
its stated acceptance is satisfied; local tests alone do not close a live gate.

### WP1 — P0: preserve the failure baseline

**Primary files:** orchestrator and scoreboard test fixtures, research diagnostics
tests, and the acceptance record.

- [ ] Add a sanitized operational fixture reproducing the run counts above.
- [ ] Add representative payload/input fixtures for all six failed compactions,
  all three input failures, successful recovery and schema repair.
- [ ] Keep fixture provenance explicit: captured, sanitized, or synthetic. If
  text changes during sanitization, record new byte sizes and stop calling it an
  exact replay of the original payload.
- [ ] Keep generated runs as local evidence; commit only bounded fixtures needed
  to reproduce behavior. Avoid copying entire provider articles into tests.
- [ ] Record baseline hashes of affected files and runtime personas before code
  edits; preserve the pre-existing dirty worktree.

**Acceptance:** diagnostics reproduce 59 logical calls, 56 attempts, 13
compactions, two schema repairs, three zero-attempt input failures, nine failed
company workflows and the primary/fallback outcomes. Baseline tests describe the
old failure; new behavior tests separately assert the intended correction.

### WP2 — P0: configuration, migration and selected-engine preflight

**Primary files:** `internal/config/config.go`, `internal/config/config_test.go`,
`cmd/cfr/main.go`, `cmd/cfr/headless.go`, `cmd/cfr/research_pair.go`,
`cfr.toml.example`, configuration/redaction tests.

- [ ] Implement the configuration and compatibility matrix above, including the
  distinction between omitted and explicitly false fallback enablement.
- [ ] Resolve required settings before pre-screening or frozen-corpus collection.
- [ ] Check the Claude executable only when Claude is selected. Do not require
  an installed Claude CLI for an API-only research run or pair.
- [ ] Restrict preflight to local configuration and selected executable checks.
  Subscription validity is established by an actual selected-engine call; a
  binary version check cannot prove account access. Avoid extra paid probe calls.
- [ ] Add the new Chief credential to global redaction and every public config
  projection, particularly frozen-pair registration.
- [ ] Apply this project's explicit DeepSeek selection after the routing tests
  pass; leave the existing credentials and Claude configuration recoverable.

**Acceptance:** every matrix row and precedence layer has a meaningful test.
Errors contain no credentials. An API run works with a nonexistent Claude binary
configured; a selected missing Claude binary fails before acquisition. Existing
Claude/fallback configurations continue to behave as documented.

### WP3 — P0: consistent Chief routing and finalization

**Primary files:** `internal/orchestrator/orchestrator.go`, `thesis.go`,
`fallback.go`, `runner.go`, `pool.go` only as needed, and integration tests.

- [ ] Add one shared helper for resolving/executing Chief calls with an explicit
  call purpose: initial synthesis, corrective synthesis, or existing fallback.
- [ ] Replace hard-coded Claude routing in both initial and corrective paths.
- [ ] Preserve report names for history compatibility; record the actual engine
  and model separately from `chief-analyst` and its corrective/fallback names.
- [ ] Keep transport, parse, contract, validation and risk results distinct. A
  syntactically readable Chief response must still pass normal enforcement.
- [ ] Stop unchanged retries on permanent authentication/configuration errors
  and provider output exhaustion. Preserve existing bounded transient retries.
- [ ] Preserve all-failed thesis behavior: no usable dossiers skips every Chief
  engine and still writes degraded decisions and metadata.
- [ ] Test an API primary corrective call with materially different cheap/Chief
  endpoints, models and caps so accidental configuration reuse is detectable.

**Acceptance:** legacy and thesis both complete through DeepSeek primary and a
fake Claude primary. The selected engine handles corrections using synthesis
timeouts. Successful API primary execution creates no artificial Claude failure;
a real primary failure followed by permitted fallback remains visible/degraded.

### WP4 — P0: explicit response-size semantics and lossless normalization

**Primary files:** `thesis_budget.go`, `thesis_schema.go`,
`internal/model/research_reliability.go`, `apiengine.go` if needed for size
telemetry, and workflow/output-schema documentation.

- [ ] Define the role response limit as the compact complete JSON payload size,
  with an explicit response-contract version. Keep raw transport limits separate.
  This changes the current raw-stdout byte contract and must be documented.
- [ ] Preserve original redacted stdout, selected raw JSON, raw byte count,
  compact payload byte count, normalization method and associated hashes.
- [ ] Use lossless JSON whitespace compaction, such as Go `json.Compact`, rather
  than decoding into a typed dossier and re-emitting it. Unknown fields, exact
  strings, numeric spellings and quotation contents must survive unchanged.
- [ ] Retain the bounded HTTP response read. Normalization cannot make a
  provider-truncated completion usable or bypass the transport-size bound.
- [ ] Make the recovery order explicit: complete valid payload under budget
  proceeds; complete malformed payload may use one schema repair; complete
  schema-valid oversized dossier may use one narrative compaction. A model repair
  cannot then buy another model repair. Local whitespace normalization consumes
  no model call.
- [ ] Treat historical profiles without the new contract version as measured
  under their original semantics; never reinterpret past failures as successes.

**Acceptance:** LLY's captured formatting boundary is tested using exact Go byte
counts. Unicode, escaped quotes, unknown fields and large numbers survive.
Malformed and truncated responses cannot enter substantive research as valid
objects. The five still-oversized examples proceed only through permitted recovery.

### WP5 — P0: compaction with measured remaining space

**Primary files:** `thesis_compaction.go`, `thesis_contract.go`,
`thesis_schema.go`, `agents/thesis-researcher.md`, recovery tests.

- [ ] Calculate protected-field bytes and JSON structural overhead before a
  compaction call. Record the remaining narrative allowance and the original
  excess. Check compaction-input capacity as well as output capacity.
- [ ] Allocate that allowance across the twelve narrative fields with declared
  headroom. Account for UTF-8 and JSON escaping; character counts are advisory.
- [ ] Give the compactor the measured limits and require compact JSON output.
  Preserve material qualifications and counterarguments, not just nonempty text.
- [ ] Keep the existing protected-field equality checks, including unknown
  extensions, status, requests, uncertainty, entry conditions and numerical facts.
- [ ] If protected content alone leaves insufficient room, record an explicit
  capacity failure before a doomed model call. If content cannot be shortened
  faithfully, retain failure rather than manufacture a compact supported thesis.
- [ ] Guide initial dossiers toward material claims and references instead of
  repeating the same facts across twelve narratives. Do not reinstate a hard
  twelve-claim cutoff or silently discard contradictory claims.
- [ ] Preserve original narratives for the independent compaction assessment.
  Budget their later delivery explicitly; successful compaction must not create
  an oversized revision prompt by adding an unmeasured history section.

**Acceptance:** captured payload shapes have measured feasible/infeasible cases,
exact protected-field preservation, at most one recovery, and independently
reviewed narrative changes. No test substitutes arbitrary short strings and
claims that this establishes live semantic preservation.

**Provider-cap decision:** retain the configured output cap initially. It is a
ceiling, not a generation target, and there is no fixed token-to-byte conversion.
Before changing request parameters, verify current primary provider documentation
through Context7 where available, otherwise official documentation. Account for
reasoning-token semantics. Any later cap adjustment requires observed completion
data and a separate regression against increased truncation.

### WP6 — P0: fit research, review and full-board Chief prompts

**Primary files:** `thesis.go`, `thesis_budget.go`, `thesis_passages.go`,
`thesis_time.go`, prompt-profile types, passage/reliability tests.

- [ ] Build prompts from identifiable sections and measure their actual serialized
  byte sizes. Report original compaction narratives separately from the dossier.
- [ ] Reserve instructions, identities, current hashes, mandatory reasoning,
  unresolved review issues, required quotations and essential request answers.
  Allocate remaining bytes to context and navigation information.
- [ ] Store each required quotation once in the prompt, with an unambiguous
  evidence/claim reference from the dossier and review. A reference is valid only
  when its exact source passage is present in the same model input.
- [ ] Preserve every relevant opposing passage, issuer attribution, publication
  date, reporting period, unresolved issue and review outcome. Keep full dossiers,
  request ledgers and source documents in the run artifacts.
- [ ] Use bytes for the assembled capacity decision and Unicode character offsets
  for source span locations. Test the conversion boundary with multilingual text.
- [ ] Bind review hashes to the full canonical dossier/plan. Record projection
  hashes separately so prompt shortening cannot authorize a different dossier.
- [ ] Compact repeated request results into stable IDs/outcomes while preserving
  every answer the model still needs and the full durable ledger. Do not re-fetch
  or forget previously unavailable/unsupported requests.
- [ ] Use a global Chief-board budget across all twelve candidates. Retain each
  candidate's outcome; allocate evidence according to required support, not a
  first-come loop that silently excludes later companies.
- [ ] Reserve space for corrective findings and prior proposed plans. Exercise a
  full board with realistic successful dossier sizes, not twelve tiny fixtures.
- [ ] If all mandatory content remains too large, stop with named omitted
  requirements and a capacity outcome. Do not introduce extra model summarizers,
  smaller shortlists or larger caps as an unrecorded workaround.

**Acceptance:** exact captured SNOW/OKTA/ORCL cases fit whenever mandatory content
is feasible. Initial, revision, challenge, final challenge, twelve-company Chief
and corrective prompts all stay within declared limits in realistic fixtures.
Every required source passage can be located in the persisted input. Infeasible
cases dispatch zero calls and explain which requirements exceeded capacity.

### WP7 — P1: research quality and usable regional evidence

**Primary files:** `agents/thesis-researcher.md`, `agents/thesis-challenger.md`,
`agents/thesis-triage.md`, `internal/marketdata/research*.go`,
`yahoonews.go`, ADR routing and request-loop tests as indicated by reproductions.

- [ ] Audit a declared sample from this run: JNJ, TTE.PA and ASML.AS for completed
  reasoning/reviews; TTD and NOKIA.HE for bulky evidence and source limitations.
- [ ] Trace material expectations, timing and target claims to source text and
  reviewer treatment. Verify that missing core evidence remains unresolved and
  that genuine continuation does not require a scheduled announcement.
- [ ] Review leakage of moving-average, support/resistance and chart-pattern
  arguments from supplied articles into triage or theses. Adjust the relevant
  prompt and targeted validation only where the captured examples justify it.
- [ ] Verify publication time versus retrieval time, future observations,
  local/ADR target units, source attribution and stale-price handling. A stale
  price must not disappear from final enforcement because a dossier recovered.
- [ ] Audit citations to short computed facts separately from document quotations.
  If a trusted scalar cannot satisfy a document-length rule, define an explicit
  typed fact reference; do not weaken quotation checks globally.
- [ ] Investigate Yahoo foreign-symbol resolution using captured provider replies.
  Preserve company relevance checks when mapping through an existing ADR.
- [ ] Improve issuer release/link selection with the same public providers and
  bounded document requests; preserve 401/403, timeouts and unavailable records.
- [ ] Evaluate the fixed panel: ASML.AS, STLAM.MI, NOKIA.HE, 2330.TW, 9988.HK,
  BHP.AX. Record accessible documents, substantive primary text, model-visible
  primary evidence, regional denominators and distinct source hosts separately.

**Acceptance:** review findings are attached to source/input examples; corrected
reasoning does not obtain support by suppressing uncertainty. On the fixed source
corpus, each region shows additional substantive primary evidence reaching model
inputs. New live source access is reported separately from replay improvements.
Source-host counts are not treated as independent reporting origins.

### WP8 — P1: trustworthy diagnostics, usage and historical compatibility

**Primary files:** `internal/model/types.go`, `research_reliability.go`,
`research_summary.go`, `internal/store/`, `internal/scoreboard/research*.go`,
`cmd/cfr/headless.go`, `internal/tui/` and redaction tests.

- [ ] Add explicit actual engine/model and call-purpose provenance while keeping
  existing report names and older metadata readable. Separate configured primary,
  attempted engine and the engine whose output was accepted.
- [ ] Version new budget/profile semantics. Add raw/payload bytes, protected-field
  bytes, context allocation, recovery identity and recovered/final failure status.
- [ ] Introduce an explicit preparation outcome for zero-attempt capacity refusal.
  Transport and parsing must not claim to have failed when neither occurred.
- [ ] Preserve stage progress: a usable dossier followed by unavailable review
  should remain visible as research completed/review failed, while staying blocked
  for selection. Avoid resetting earlier successful work to an apparent zero.
- [ ] Aggregate failures by stable identity instead of counting the same error
  again as a company note and a domain error. Keep detailed evidence accessible.
- [ ] Report provider/source warnings by type and region. Correctly filtered
  unrelated filings and expected uncovered instruments need distinct labels from
  lost material evidence; changing run severity requires explicit tests/specs.
- [ ] Preserve unknown historical values as unknown. Incomplete usage is a lower
  bound, never a measured zero or an invented dollar amount.
- [ ] Make scoreboard cohort identity include actual Chief selection/model and
  relevant contract/profile versions. Historical Claude-plus-fallback runs must
  remain distinguishable from new DeepSeek-primary runs.
- [ ] Save a redacted resolved-configuration hash and source/persona hashes for
  new acceptance runs. Exclude all keys from snapshots, logs and pair manifests.

**Acceptance:** JSON, text and TUI agree on primary engine, recovered failures,
completed research, completed reviews, failed research and final decisions. An
all-failed run, reviewed no-trade run, and supported conditional run remain distinct.

### WP9 — P1: regression matrix and integrated validation

Use repository-native fake CLIs and local HTTP fixtures. Avoid metered model calls
for ordinary tests. Add behavior assertions alongside each implementation package.

| Area | Required cases |
| --- | --- |
| Configuration | Every migration row; all precedence levels; invalid selector/cap; secret redaction |
| Chief routing | API/Claude × legacy/thesis × single/independent; initial and corrective; distinct cheap/Chief settings |
| Fallback | Historical Claude-to-API success/failure; disabled fallback with credentials present; API-primary no Claude attempt |
| Failures | Permanent auth; bounded transient retry; timeout; cancellation; no usable dossiers; malformed Chief result |
| Response sizing | Raw formatting overhead; exact boundary; Unicode/escaping; unknown fields; complete oversize; truncated output |
| Recovery | Protected mutations rejected; narrative qualifications retained; infeasible floor; failed recovery gets no extra call |
| Prompt sizing | Captured three failures; full twelve-company board; corrective headroom; infeasible required evidence |
| Research meaning | Long/short/NONE; reviewed conditional plan; future observation; unsupported target; stale price; wrong issuer |
| Artifacts/UI | Accurate stage progress, actual engine, additive versions, missing old fields, no secret exposure |
| Frozen pairs | API-only operation without Claude installed; selected-engine preflight before capture; shared evidence and no model tools |

Run focused packages first, then the full checks with the required Go toolchain:

```sh
go test ./internal/config ./internal/agents ./internal/orchestrator ./internal/model ./internal/marketdata ./internal/store ./internal/scoreboard ./internal/tui ./cmd/cfr -count=1
go test ./... -count=1
go build ./...
go vet ./...
git diff --check
```

Format edited Go files with `gofmt` and verify the result. If local HTTP fixtures
are blocked by sandbox loopback restrictions, rerun with the required permission
and distinguish that from assertion failures. Update workflow contracts, README
requirements, examples, `AGENTS.md` and `CLAUDE.md` in the same change as behavior.

**Acceptance:** all required checks pass, captured regressions exercise actual
failure shapes, and an implementation review verifies the credential separation,
call bounds, preserved evidence and config migration matrix.

### WP10 — P2: bounded live acceptance and later evaluation

Use the [existing acceptance runbook](2026-09-12-reliability-acceptance.md) with
these amendments: DeepSeek is primary for the current project, Claude account
access is not a prerequisite for API acceptance, and response-contract versions
are recorded. Writing this plan does not launch any new live calls.

1. **Offline gate:** WP1–WP9 local checks pass. Save the manifest before looking
   at new live results, including source/persona/config hashes, engines, caps,
   bytes, budgets, run anchor, indices, deadline and experiment purpose.
2. **Source gate:** capture the fixed six-name regional panel once with at most
   eight document attempts per name. Preserve failures and unchanged denominators.
3. **Model gate:** use the same six-name panel for bounded single-stock research,
   at most ten minutes per case. Add LLY, TTD, SNOW, OKTA and ORCL as a declared
   capacity panel, each at most once per designated acceptance attempt. These
   cases cover formatting, protected content, revision and final-review pressure.
4. **Stop rule:** stop the sequence on unrecovered capacity, truncated/unreadable
   output or an unavailable substantive review. Diagnose locally before declaring
   a new acceptance attempt. Do not replace a failed company with an easier one.
5. **Full-run gate:** after representative cases pass, execute one independent
   thesis run on all four indices, with a twenty-minute deadline and the default
   3 rounds, 8 documents, 24 candidates and 12 shortlisted companies.
6. **Review gate:** audit the evidence behind every proposed plan, plus the
   completed no-trade outcomes. A high completion count with unsupported claims
   is insufficient. Distinguish missing evidence acknowledged by a valid review
   from a review that never ran.
7. **Publish record:** save exact run IDs, actual engines, call/attempt counts,
   recovery rate, bytes, source visibility, tokens, durations, failures and exit
   codes. Use direct binary execution for the application exit-code check.

Compute the maximum logical/model attempts from resolved settings before live
execution. Per company, `2 * (rounds + 3)` bounds rounds, initial challenge,
revision, final challenge and their one possible repair each: at defaults this
is at most 12 logical calls per company, or 144 for twelve companies. Add actual
discovery/triage, Macro, plan-review and Chief/corrective allowances, and apply
the configured retry bounds. This is a ceiling, not a target or permission to
repeat an acceptance sequence. Provider exhaustion still stops unchanged retries.

#### Full-run acceptance checklist

- [ ] Every non-deferred company has a usable dossier and substantive review;
  no unrecovered capacity, transport, parsing or contract failure remains.
- [ ] At least one company reaches research. An all-deferred run is inconclusive
  for model reliability; it cannot pass by satisfying an empty denominator.
- [ ] Every input/payload satisfies its recorded contract; all required passages,
  review hashes and counterevidence remain available.
- [ ] Initial and corrective Chief calls use the configured DeepSeek Chief model;
  no Claude call is attempted for the current configuration.
- [ ] A supported or conditional plan passes target, evidence, date, construction
  and risk enforcement. Zero supported plans can still satisfy research reliability.
- [ ] Any remaining source limitations are named and their material effect is
  reviewed. No material gap becomes an actionable result through a severity change.
- [ ] Usage, time, call limits and every failed/recovered attempt are recorded.
- [ ] The application outcome and exit code match its completed/degraded state.

Operational reliability, regional evidence gains and investment performance are
separate gates. Record their status separately. Regional failures remain open even
if the model pipeline completes; model completion does not establish better returns.

After these gates, use existing registered frozen legacy/thesis pairs for future
10- and 15-session evaluation. Include the actual Chief in cohort identity and
keep empty/degraded arms, benchmarks, execution-cost assumptions and unavailable
outcomes. When Claude access resumes, first verify selection with a bounded case;
later engine comparisons should use matched frozen evidence and declared cohorts.

## Delivery order and completion tracking

| Increment | Packages | Reviewable result |
| --- | --- | --- |
| A | WP1, WP2, WP3, essential WP8 provenance/tests | DeepSeek primary and optional Claude work across supported entry points |
| B | WP4, WP5, WP6, related WP8/tests | Measured response recovery and complete-context budgeting |
| C | WP7, remaining WP8, WP9 | Evidence/research improvements, compatible diagnostics and full local verification |
| D | WP10 | Recorded live acceptance; subsequent prospective evaluation remains separate |

Implement with small cohesive changes and tests for each increment. Do not mix
unrelated existing work into commits. Engine routing can be reviewed independently
from response semantics, and both can be reviewed before live model spending.

| Package | Implementation | Local verification | Live acceptance |
| --- | --- | --- | --- |
| WP1 baseline | Pending | Pending | Existing run supplies baseline only |
| WP2 config/migration | Pending | Pending | Pending |
| WP3 Chief routing | Pending | Pending | Pending |
| WP4 response normalization | Pending | Pending | Pending |
| WP5 bounded compaction | Pending | Pending | Pending |
| WP6 complete prompt budgets | Pending | Pending | Pending |
| WP7 research/source quality | Pending | Pending | Pending |
| WP8 diagnostics | Pending | Pending | Pending |
| WP9 integrated verification | Pending | Pending | Not a live gate by itself |
| WP10 acceptance/evaluation | Pending | Prerequisite packages | Pending |

The earlier audit ran `go test ./... -count=1`, `go build ./...`, `go vet ./...`
and `git diff --check` successfully. Those results describe the pre-implementation
tree and do not count as verification of any proposed change in this document.

### Rollback and compatibility

- Engine selection is reversible through configuration. Returning to Claude
  requires working access; switching a setting cannot restore a subscription.
- Preserve the prior local config values during explicit migration, without
  checking secret-bearing backups into the repository.
- Keep raw reports and old metadata readable. New budget and provenance fields
  are additive and versioned; old absent fields remain unrecorded.
- A rollback of response representation must retain each run's recorded contract
  and measurements. Never recompute historical acceptance using a new size rule.
- Do not rewrite historical cohorts, personas in `agents.v1/`, or prior research
  decisions when changing the active Chief.

## Remaining decisions during implementation

No user choice blocks the plan. Resolve these engineering details from evidence:

1. Confirm exact normalized byte measurements and the protected-content floor
   before finalizing narrative allowances. Preserve a recorded explanation for
   any case that still cannot fit.
2. Measure realistic twelve-company Chief and corrective representations before
   finalizing the global allocation. If mandatory content remains infeasible,
   record the blocker and propose a separate protocol/budget decision rather than
   silently widening this implementation.
3. Verify the existing configured provider's current request/usage behavior before
   changing provider parameters. A reported model name and token-to-byte heuristic
   are insufficient documentation.

## Related records

- [September 7 improvement plan](2026-09-07-thesis-research-improvements.md)
- [September 12 reliability follow-up](2026-09-12-thesis-research-reliability-followup.md)
- [September 13 implementation record](2026-09-13-reliability-implementation.md)
- [September 15 recovery implementation](2026-09-15-thesis-recovery.md)
- [Bounded acceptance runbook](2026-09-12-reliability-acceptance.md)
- [Thesis workflow](../workflow/thesis-research.md)
- [Output schema](../workflow/output-schema.md)

This plan extends the earlier reliability work and records the revised Chief
policy. Existing implementation records remain historical evidence; pending work
above must not be described as completed merely because its predecessor passed
synthetic tests.
