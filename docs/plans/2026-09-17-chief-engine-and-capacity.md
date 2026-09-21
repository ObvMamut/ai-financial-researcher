# Chief engine and capacity implementation and verification record

Date: 2026-09-21. Implements the accepted
[chief engine and capacity plan](2026-09-17-chief-engine-and-capacity-plan.md), Increments
A and B (Tasks 0–15), against the failure this plan exists to close:
[the September 15 recovery record](2026-09-15-thesis-recovery.md), superseded, and the
live run `runs/2026-09-15T17-00-30` it could not explain — nine of twelve companies failed
for budgeting reasons a suite of twelve synthetic dossiers, sized to pass, could not catch.
Code, runtime personas, workflow contracts and the output schema are updated. No live
research run was started; this plan explicitly stops at the offline gate.

## Completion states

| Plan item | Implementation | Local verification | Live acceptance |
| --- | --- | --- | --- |
| `chief_engine` selector, dedicated `[chief_api]` credentials | Complete: migration matrix (`claude`/`api` × fallback enabled/omitted/false), never inherits from `[api]`/`[local]`/`DEEPSEEK_API_KEY`, redacted, preflight gated on the resolved engine | Passed migration-matrix, redaction and no-installed-Claude-binary regressions | Pending |
| Shared Chief execution helper, four routing sites | Complete: `resolveChiefEngine`/`chiefTarget`/`cheapTarget`; timeout, retry and stage now come from the resolved target, not from `cli == model.CLIClaude`; no `CLIClaude` remains at a Chief dispatch site | Passed the engine × pipeline × purpose routing matrix on two distinct httptest servers, and the synthesis-timeout/stage regression | Pending |
| Fallback gating, permanent-failure stop | Complete: `chiefFallbackAllowed`'s full truth table (primary × `enabled` tri-state × credentials); API primary never attempts Claude or a second identical call; 401/403 stop unchanged retries | Passed the gating and permanent-failure regressions; `TestThesisAllFailedSkipsChiefAndPersistsDegradedResults` passes unedited (see below) | Pending |
| Chief provenance in metadata/headless/TUI/scoreboard | Complete: `ChiefEngine`/`ChiefModel`/`ChiefAttempted`/`ChiefAccepted` on `RunMeta`, a compact two-field subset on `IdeasResult`/`ideas.json`; scoreboard cohorts separate by engine | Passed the provenance-distinguishes and JSON/TUI-agreement regressions | Pending |
| Compact-payload response contract | Complete: `responseCapacity` measures `json.Compact` of the last fenced JSON block, not raw stdout; `ResponseMeasure`/`response_contract_version` additive on `PromptProfile`; recovery order (repair XOR compaction, never chained) preserved | Passed the LLY boundary case (see below), losslessness, and recovery-order regressions | Pending |
| Measured compaction allowance | Complete: `measureCompaction` derives `protected_bytes`/`narrative_budget` from the actual payload, not a fixed 400-character hint; infeasible dispatch refused before any call; per-field allocation proportional with a floor | Passed feasibility, allocation and input-capacity-before-dispatch regressions; six real capacity cases given a measured verdict, not asserted to all recover | Pending |
| Researcher persona — material claims | Complete: guidance toward claim IDs over repeated narrative; no hard claim cutoff, no instruction to discard contradictory evidence | Passed the no-reinstated-cutoff regression | Pending |
| Section-measured prompt assembly | Complete: `assembleSections` — a flat, non-overlapping byte partition; mandatory sections fail the call by name rather than silently folding into a neighbour; `promptComponents`'s marker-substring scan deleted | Passed the exact-attribution, mandatory/optional-drop and mandatory-overflow-names-both-sections regressions | Pending |
| Global Chief board budget | Complete: `chiefBoard` — required quotations reserved globally before any optional content; two-tier optional pool (case narrative, funded by rank across the whole board; then source context, proportional to unmet need); order-independent by construction; every candidate keeps its outcome even when evidence is trimmed | Passed the twelve-realistic-dossier fit, fairness (non-first-come-first-served, order-independent), and outcome-retention regressions; a fix round closed two design defects (below) | Pending |
| Revision and challenge prompt fitting | Complete: `sizeEvidence` resizes the trimmable evidence section per call, by bisection, for exactly what that call's other sections and its one appended mandatory addition cost — not a flat 24,000-character allocation; quotation dedup; request-ledger compaction to identity+outcome with the durable ledger untouched | Passed against the **real captured SNOW/OKTA/ORCL prompts** (below), not only a committed reconstruction | Pending |
| Preparation-refused outcome, named omissions | Complete: `OutcomeNotAttempted`, additive and distinct from `OutcomeNotRun`; `DomainStatus.Omitted`/`Report.Omitted` name the mandatory requirement that did not fit; two further real defects fixed (below) | Passed separation, stage-progress, aggregation and unknown-usage-stays-a-lower-bound regressions | Pending |
| Documentation, implementation record, acceptance manifest | Complete: this record; `docs/workflow/thesis-research.md` and `output-schema.md` updated for the payload-based response contract, measured compaction, section-measured prompts and provenance fields; `docs/plans/acceptance-manifest.sh` | Manifest run against this repository's real configuration; self-check confirmed no key material in its output (below) | Pending |

## Shipped contracts

The response budget bounds `json.Compact` of the **last fenced JSON block** a call
produces (`response_contract_version: 2`), not the raw response. Raw bytes are recorded
(`raw_bytes`, `raw_sha256`) for audit and bind nothing. A profile with no
`response_contract_version` predates this and is read under its original semantics — its
`bytes` field was the raw response, full stop. The recovery order stays fixed: one
schema repair OR one narrative compaction per response, never chained; local
normalization (`json.Compact`) consumes no model call.

Compaction allowance is measured, not assumed: `protected_bytes` (the payload with every
narrative field blanked) plus a declared 256-byte structural headroom fix the
`narrative_budget`, allocated per field in proportion to that field's own length with a
floor for any non-empty field. A company whose protected content alone exceeds the
response limit is refused before dispatch (`Feasible: false`, zero model calls,
`input_capacity`) rather than sent a doomed call.

Every thesis prompt is now assembled from named, self-measuring sections
(`assembleSections`): a flat, non-overlapping byte partition, so a September-15-style
overflow can always be attributed to a name. Mandatory sections are never silently
trimmed; a mandatory overflow is a named capacity error, not a component-less one. The
Chief board budgets globally across the whole shortlist rather than per company: required
quotations for every cited claim, across every company, are reserved before any optional
content; the remainder funds case narrative first (by rank across the whole board, so
every company is argued to the same depth or none — an indivisible field cannot be
usefully split proportionally) and then source context (proportional to each company's
still-unmet need). Research, revision and challenge prompts size their own trimmable
evidence section per call, by bisection, for exactly what the rest of that call already
costs, rather than a flat character allocation that could not account for a corrective
call's extra mandatory sections.

`chief_engine` (`claude` | `api`, omitted means `claude`) selects the Chief Analyst's
engine through one resolved `chiefEngine`/`callTarget` used at every Chief dispatch site;
timeout, retry policy and stage now come from the resolved target rather than from
`cli == model.CLIClaude`, so an API Chief can never be silently demoted to research
timeouts or misreport its stage. `[chief_api]` is dedicated and never inherits from
`[api]`, `[local]` or `DEEPSEEK_API_KEY`. The existing `chief_fallback` resilience path is
unchanged and remains Claude-primary-only; `[chief_fallback].enabled` distinguishes an
omitted key (fallback active when credentials are present, as before) from an explicit
`false`, and an explicit `true` together with a non-`claude` primary is a configuration
error caught at load. `ChiefEngine`/`ChiefModel`/`ChiefAttempted`/`ChiefAccepted` on
`RunMeta` record what was configured, what was actually called, and whose output shipped,
as three distinct facts; `SynthesisModel` keeps its prior meaning and place.

A call refused before dispatch on input capacity now says so: `OutcomeNotAttempted`
(`"not_attempted"`) is additive and distinct from `OutcomeNotRun` — a capacity refusal and
a policy deferral are different facts and are never aliased. `DomainStatus.Omitted` /
`Report.Omitted` name the mandatory requirement that did not fit. Two further defects,
found while fixing the above and confirmed against the real September 15 metadata rather
than hypothesized, are also fixed: a challenge call's own transport/parsing outcome no
longer overwrites its dossier's already-completed outcome (ORCL's real record showed a
completed dossier reset to an apparent failure when only its review afterward could not be
attempted); and a single call failure no longer double-counts in a run's `data_errors`
once as raw company text and again as the ticker-tagged domain error.

The default legacy pipeline, independent research mode, provider output caps, risk
thresholds and byte budgets (triage 98,304/12,288 · researcher 98,304/20,480 · challenger
98,304/12,288 · chief 196,608/24,576) are all unchanged by this plan. No new dependency,
provider, credential or paid service was introduced.

## Verification evidence

Executed with the repository Go toolchain:

- `go build ./...`, `go vet ./...`: passed, at every task boundary and at the final HEAD.
- `go test ./... -count=1`: passed, 13/13 packages, at every task boundary and at the
  final HEAD.
- `gofmt -l .`: empty at HEAD. Through Task 15 this reported
  `internal/orchestrator/riskgate_test.go` only — a pre-existing toolchain divergence
  (`go.mod` declared Go 1.26.3; the development machine runs go1.27.1), dated to before
  this plan's baseline commit (`7c126f3`), not a regression introduced by any task here.
  Put to the user as an operator decision rather than resolved silently: bump `go.mod`'s
  Go directive to 1.27.1 (chosen) or pin the toolchain to 1.26.3. `go.mod` now declares
  1.27.1 and the file is reformatted to match.
- `git diff --check`: clean at every commit.

**LLY recovers by normalization alone.** The response-contract boundary case: a captured
response of 20,493 raw bytes against a 20,480-byte limit, whose fenced JSON block compacts
to 20,326 bytes, now passes on payload bytes with **zero recovery calls dispatched** —
the exact regression this increment exists to fix. A comparably-sized case whose payload
also exceeds the limit (not merely its raw bytes) still correctly fails.

**The Chief board fits twelve companies globally**, measured against realistic-scale
dossiers (15–25 KiB each, not synthetic fixtures sized to pass): board 179,824–180,944 of
a 180,944-byte budget; full prompt 195,488–195,655 of 196,608. The corrective re-prompt,
which appends two further mandatory sections nothing in the original board budget
reserved for, is now sized per call (`chiefPromptBuilder`) rather than reusing the
initial call's board — before the fix round below, the corrective call could never be
assembled at all once eight or more dossiers were usable.

A Task 12 fix round closed two findings from its own task review, verified by mutation
(seven mutations, each caught by a named test; the mutation that had been silently
`ALL GREEN` — disabling the required-floor capacity check entirely — now fails three
tests): the corrective call's extra mandatory sections were unreserved for, and the
board's entire optional pool was being granted to an indivisible per-company narrative
share that could fund no field at all, leaving source context unfunded in every real run.
Both are fixed; the production optional pool now measured 6,747 narrative / 4,495 context
bytes at the calibrated budget, against 11,248 / 0 before.

**The real captured SNOW, OKTA and ORCL prompts now fit** — not only a committed
reconstruction of the failure shape, but the actual September 15 artifacts
(`runs/2026-09-15T17-00-30/data/research-*.json`, gitignored, read for measurement only,
never committed). Rebuilt through the current prompt builder: SNOW revision
98,396→92,999 bytes, OKTA revision 103,077→94,229, ORCL challenge-final 102,413→92,660,
all under the 98,304-byte limit with 4.1–5.6 KB of margin, every required quotation and
every unresolved review issue retained. (A committed test necessarily uses a
reconstruction rather than the real prompts — committed fixtures may carry no prompts,
configuration, credentials, source text or provider article bodies — but this record
states what was verified against the real artifacts, not only what the committed test
demonstrates.)

**The response contract's recovery order held under mutation** throughout: a schema
repair and a compaction never chain in either direction; explicit `finish_reason=length`
is an `output_limit` failure with one attempt and no repair; local normalization
dispatches zero model calls.

`TestThesisAllFailedSkipsChiefAndPersistsDegradedResults` (`internal/orchestrator/thesis_test.go:209`)
is a two-line wrapper around `testThesisResultFixture`
(`internal/orchestrator/thesis_test.go:213`) and passes **unedited**, as the plan
requires — but the wrapper itself asserts almost nothing by construction, so that
sentence alone is nearly vacuous. The behaviour lives in the helper, which grew across
this branch from 193 to 252 lines (+60/−1) with its assertion count rising from 6 to 19;
the single deletion is a forced one-line signature update (`runThesis` gained the
resolved-engine parameter in Task 4), not a removed check. **Restated as a falisfiable
claim:** all-failed behaviour is unchanged — with zero usable dossiers the run skips
**every** Chief engine and still persists degraded decisions, per-company explanations and
metadata (headless exit 3) — and that behaviour is strictly better covered at this HEAD
than at the merge base, not merely re-asserted by an unchanged wrapper.

**The acceptance manifest self-checks clean.** Run against this repository's actual
resolved configuration (`sh docs/plans/acceptance-manifest.sh`), it exits 0 and prints
git revision/dirty state, persona hashes, a redacted config hash, provider base
URLs/models/caps (no keys), role byte budgets, retry/round/document limits, the operator
deadlines from the acceptance runbook, and the worst-case per-company call ceiling
`2*(rounds+3) = 12` at this repository's configured `rounds: 3`. Its own self-check —
gathering every actually-configured secret and grepping its own assembled output for the
full value and, per `internal/redact.go`'s own 8-character threshold, its prefix — found
no leak. It never invokes `cfr` and never opens a network connection; it only reads local
configuration to prove none of it reached its own output.

Independent review across the branch identified and closed: mandatory-flag handling on
duplicate/conflicting prompt sections (Task 11 fix round); the two Task 12 board-budget
findings above; and, in Task 14, two real defects beyond its own brief (the challenge
stage-progress overwrite and the `data_errors` double-count), both confirmed against real
September 15 data before being fixed rather than fixed on suspicion.

## Known gaps, carried forward deliberately

**The Chief-board macro-drop hazard remains latent, by design, not by oversight.** The
guarantee that a board sized with macro already paid for is a board macro is never
dropped from is genuinely true today, but only because production always calls
`assembleSections` with `noSectionLimit` (`math.MaxInt`) — no task in this plan introduces
a real limit there, and none should without also closing this hazard, because a real limit
would let the board's own capacity-error path exceed its budget before a mandatory-overflow
error could fire, silently dropping macro rather than naming the overflow. Flagged for the
final whole-branch review, not resolved here.

**Not every oversized dossier is asserted to recover.** Task 9's measured
feasible/infeasible verdict is per company, and Gate B does not require all six of the
originally oversized responses to now fit — only that the verdict is measured rather than
assumed, and that LLY (the boundary case) recovers by normalization alone with zero model
spend.

**`DomainStatus.Payload == "invalid"` on a zero-attempt capacity refusal** is the same
class of inaccuracy `OutcomeNotAttempted` fixes elsewhere (no response was ever received,
so "invalid" implies one arrived and did not decode), and was left untouched rather than
changed without an explicit requirement to do so. It feeds `InvalidPayloads`
(`research_diagnostics.go`) directly; `PrimaryChiefFailed` is gated on
`Domain == "chief-analyst"` and is not reachable from this path (a capacity-refused
compaction domain is always named `<company>-compaction`), so the practical effect is
narrower than a first reading of "feeds `InvalidPayloads`/`PrimaryChiefFailed`" would
suggest — only the former is affected.

**The final whole-branch review** (Opus, over the full branch) found no correctness or
security defect in production code, confirmed every mutation-testing claim it re-ran
independently, and confirmed the real SNOW/OKTA/ORCL recovery against the actual
September 15 artifacts. It found three items worth a short fix round before merge: a
doc/code contradiction in `synthesis_model`'s documented meaning (now corrected above),
the two stale G-2 statements this record originally carried (now corrected above), and
a pre-existing gap in the engine-boundary redaction test — `TestPromptsAndReportsCannotCarryACredential`
asserted on the CLI's captured stdout (the inbound scrub) rather than on the outbound
prompt the model provider actually receives, so removing the line that redacts the
outbound prompt (`runner.go`, the legacy pipeline's only redaction, since it builds
prompts outside `preparePrompt`) shipped the suite green. That review also
independently reproduced a long-parked gap of the same shape: removing thesis's
`preparePrompt`-side redaction also ships green, though a probe showed two independent
downstream layers (the outbound scrub and the persisted-artifact scrub) still catch the
credential before it reaches the wire or disk — so its real cost was a *measurement*
defect (byte counts computed on pre-redaction text while the wire and artifact carry
post-redaction text) rather than a leak. Both gaps were closed together in one
dedicated fix round; see the branch history after this record's commit for that diff.

## Remaining acceptance

No live run was performed at any point in this plan's execution. Gate B is not a
reliability claim: it says the pipeline can no longer fail for the September 15 reasons —
a response budget measured against the wrong bytes, a compactor with no idea how much room
it had, a per-company board allocation that could not hold twelve companies, and revision/
challenge prompts sized without regard for what else a call carried. Whether real dossiers
fit under live model output, and whether any of this changes returns, is unestablished
until an operator runs the [bounded acceptance runbook](2026-09-12-reliability-acceptance.md)
with this plan's amendments, using `docs/plans/acceptance-manifest.sh` to capture the
preflight manifest first. Switching `chief_engine` to `api` in `./cfr.toml` remains an
operator step outside this plan's scope, performed after Gate B is green.

Prospective matched frozen-pair evaluation at ten and fifteen sessions remains later work
after both offline gate and live acceptance. Improved investment returns are unproven by
anything in this record. The existing operator item to confirm provider-side rotation of
the previously exposed Alpha Vantage credential remains unconfirmed and is unrelated to
this plan.
