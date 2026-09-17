# Reliability implementation and verification record

Date: 2026-09-13. Implements the accepted
[September 12 follow-up plan](2026-09-12-thesis-research-reliability-followup.md).
Code, runtime personas, configuration examples and workflow contracts are updated.
Existing working-tree changes were preserved. No live research run was started.

## Completion states

| Plan item | Implementation | Local verification | Live acceptance |
| --- | --- | --- | --- |
| Explicit output exhaustion | Complete: typed failure, one attempt, retained usage, no partial JSON or recovery calls | Passed HTTP/runner fixtures and cancellation regressions | Full-run absence of repeated truncation still to be observed |
| Role budgets and compact contracts | Complete: assembled UTF-8 byte limits, prompt snapshots/profiles, bounded dossiers, hash-bound challenger reviews, compact Chief context | Passed multi-round/revision fixtures, twelve-company board, input/output capacity and degraded-artifact regressions | Open: every attempted company must complete in designated acceptance run |
| Relevant passages | Complete: substantive initial selection, global reservation of required quotations before optional context, explicit omissions | Passed deep navigation-heavy TSMC persisted-input regression, conflicting evidence, Unicode and unavailable-material cases | Real-source evidence gains covered by regional gate below |
| Numerical comparability | Complete: listing/currency/share-basis provenance, dated FX and ratio checks, Go-computed normalization, explicit target method, hash-bound execution review | Passed mismatch, normalized comparison, unavailable/stale/future FX and ratio/provenance regressions | No investment-performance inference from synthetic comparisons |
| Temporal and expectations reasoning | Complete: listing-local anchors, unavailable future observations, publication-time validation through review, explicit expectations references | Passed event/calendar/price cases and future challenger-evidence regression | Quality of model reasoning remains subject to live review |
| Regional evidence and requests | Complete: exhaustive curated-listing regions, corrected issuer seeds, bounded link discovery, cached request outcomes, actual prompt visibility | Passed denominator, request-repeat, authority and synthetic fixed-panel checks | Open: additional usable primary evidence in both regions on same captured corpus |
| Reusable diagnostics | Complete: attempted/deferred/completed/failed research, explicit/inferred truncations, request repeats, per-stage usage and fallback provenance | Passed sanitized September 10 artifact reconstruction and existing integration suite | New live measurements pending |
| Verification and evaluation | Implementation checks and independent review complete; bounded operator runbook provided | Full tests, build, vet, formatting and diff whitespace checks passed | Live reliability and subsequent frozen-pair evaluation open |

## Shipped contracts

Prompt and complete-response limits are measured in UTF-8 bytes, including the
assembled persona/instructions. Defaults are triage 96/12 KiB, researcher
96/20 KiB, challenger 96/12 KiB and Chief 192/24 KiB. Profiles save component sizes,
the exact redacted prompt hash, compaction choices and visible evidence. Token
estimates are heuristic and remain separate from reported provider usage.

Current dossiers use contract version 2, at most twelve claims and three research
requests, 400-character narrative fields, and up to two 300-character quotations
per claim. Challenger reviews refer to claim IDs and the dossier hash. Fresh
outputs must follow the current contract; historical artifacts remain readable.
Execution reviews bind to the exact plan hash and explicitly assess its target.
External target comparisons require validated claim references; thesis scenarios
are labeled separately. Numerical conversion results supplied by models are
cleared and computed again in Go.

Unavailable evidence, future evidence, extraction truncation and prompt omissions
remain distinguishable. Required quotations are reserved across all documents
before optional context; an impossible total budget fails before dispatch.
Failed and deferred companies remain visible to Chief synthesis. Capacity failure
in either primary or fallback preparation still produces degraded artifacts.

The default legacy pipeline, primary Claude CLI, separately configured Chief
fallback, provider output caps, risk thresholds and frozen control personas retain
their existing contracts. No new service, credential requirement or dependency
was introduced.

## Verification evidence

Executed with the repository Go toolchain (Go 1.26.5):

- Focused orchestrator, marketdata, scoreboard and configuration tests: passed.
- `go test ./...`: passed across all packages.
- `go build ./...`: passed.
- `go vet ./...`: passed.
- `gofmt` checks on changed Go files and `git diff --check`: passed.

Tests using local HTTP fixtures required loopback permission in this sandbox;
the full suite passed with that permission. No model credentials were needed.

The sanitized `internal/scoreboard/testdata/research-sep10.json` fixture contains
operational fields from the completed September 10 artifacts, without prompts,
configuration, credentials or source text. Diagnostics reproduce 44 logical
calls, 65 attempts, ten attempted companies, two deferrals, nine failures, one
completed rejection, ten truncated calls/twenty truncated attempts and 69 request
results. Reported usage is 1,748,299 prompt and 438,995 completion tokens, while
incomplete historical usage remains explicit. Malformed fallback payloads cannot
be counted as successful fallback research.

Independent review identified and verified fixes for current-contract bypasses,
issuer/ratio provenance, prompt visibility accounting, fallback preflight
finalization, listing-local dates, optional target provenance, premature
per-document quotation limits and future evidence in additional review claims.
The final focused re-review reported no remaining actionable findings in its
three latest corrections; its four focused regressions passed. The full suite
above supplies the broader validation that its restricted sandbox could not run.

## Remaining acceptance

Follow the [bounded acceptance runbook](2026-09-12-reliability-acceptance.md) for
the fixed six-company Europe/Asia-Pacific panel, representative single-company
runs and one full independent run. Capture all failures, exact source/persona/
configuration hashes, budgets, usage and latency. Synthetic coverage and official
website availability do not demonstrate gains through the application reader.
No live acceptance criterion has been closed by local tests.

Prospective matched frozen-pair evaluation at ten and fifteen sessions remains
later work after reliability acceptance. Improved investment returns are unproven.
The existing operator item to confirm provider-side rotation of the previously
exposed Alpha Vantage credential remains open; local redaction tests cannot
establish revocation.
