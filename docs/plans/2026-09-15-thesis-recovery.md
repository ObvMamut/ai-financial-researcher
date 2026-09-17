# September 13 independent-run recovery

> **Superseded.** The fixes below are real and still in the tree, but this
> record's standing is not. The live run `runs/2026-09-15T17-00-30`, executed
> the same evening, failed nine of twelve companies in the same failure class:
> six complete responses still exceeded 20,480 bytes after compaction, and
> three prompts exceeded 98,304 bytes and were never dispatched. Remaining work
> is tracked in
> [the DeepSeek Chief and reliability plan](2026-09-15-deepseek-chief-and-research-reliability.md).

Implemented against the failures in `2026-09-13T11-30-16`. The run returned
twelve complete researcher objects, all initially watchlist/NONE. Eleven were
discarded for writing-target violations; SAN.PA exceeded the total response byte
budget. This prevented requested retrieval and substantive challenge. The primary
Chief also encountered disabled Claude subscription access. These failures did
not establish that the shortlist contained no trade opportunities.

## Implemented behavior

- Narrative/quotation lengths and claim counts are measured writing targets.
  Complete dossiers within the response byte budget continue through research.
  The three-request limit and all input, output and provider budgets still apply.
- One complete, schema-valid oversized researcher response can spend its existing
  repair allowance on narrative compaction. Go requires every other wire field,
  including unknown fields, to remain unchanged. Original narratives reach the
  challenger, which must explicitly assess whether qualifications were preserved.
  Failed compaction and provider truncation cannot trigger further recovery.
- Dossiers distinguish material unresolved evidence from entry conditions and
  future monitoring. Independent review must verify that classification before
  support. Reviewed conditions reach plan prerequisites; monitoring reaches the
  final plan. Citation, direction, target-review and risk checks still apply.
- Research prompts assess long, short and no-trade cases with the same evidence
  burden, permit evidenced continuation without a scheduled announcement, and
  distinguish future observations from retrievable historical gaps.
- Document identity ignores fragments and host/scheme case while retaining paths,
  queries and observation-date restrictions. Issuer seeds and discovered-link
  ranking favor release documents over language indexes, resource pages, templates
  and assets. Navigation text cannot satisfy the substantive-source requirement.
- Go publishes research/review/failure counts in results and displays operational
  failures in the TUI and headless output. Outcomes retain contract failures and
  actual repair counts. No usable dossiers means Chief/fallback calls are skipped.
  Disabled Claude subscription access stops unchanged retries; the configured
  primary/fallback engine contracts remain intact.

## What the live run refuted

`runs/2026-09-15T17-00-30` shortlisted twelve companies and produced zero plans.

| Companies | Last blocking event |
| --- | --- |
| LLY, BAC, NOKIA.HE, TTD, REGN, 9988.HK | Complete response still exceeded 20,480 bytes after compaction |
| SNOW, OKTA | Revision prompt exceeded 98,304 bytes; call never dispatched |
| ORCL | Final challenger prompt exceeded 98,304 bytes; review never dispatched |
| JNJ, TTE.PA | Completed final challenge requested revision |
| ASML.AS | Completed final challenge rejected the thesis |

All 55 HTTP completions with a recorded finish reason reported `stop`. No
provider truncated anything; the budgets were ours. All six spent their one
compaction allowance and were rejected again afterwards. LLY's is the clearest
case: its compaction had already done the job, landing at 20,326 payload bytes
against a 20,480-byte limit, and was discarded anyway because the raw response
measured 20,493 — 155 bytes of interior whitespace and a 12-byte fence, none of
which the budget was ever meant to bound. The remaining five exceeded the limit
on payload bytes as well, having been given a fixed "under 400 characters each"
target instead of the allowance that was actually left to them, which nothing
computed.

## Verification

The checks below all passed, and none of them could have caught the failures
above. The twelve synthetic dossiers were sized to pass the budget, so a budget
measured against raw stdout rather than the JSON payload it bounds looks
correct against them. Synthetic coverage establishes that the recovery path
executes; it establishes nothing about whether real dossiers fit.

- `go test ./... -count=1`: passed across all packages.
- `go build ./...` and `go vet ./...`: passed.
- Go formatting and `git diff --check`: clean.
- Twelve synthetic dossiers preserve the failed run's field shapes, Unicode
  lengths, claim/request counts and original directions. Eleven pass without
  repair; SAN.PA takes one compaction. Findings and request lists survive.
- Regression tests cover subsequent retrieval and independent challenge, original
  narrative delivery, protected-field mutation, failed/oversized compaction,
  cancellation, input-budget refusal and permanent Claude access failure.
- An end-to-end fixture produces a reviewed, sized conditional plan with its
  prerequisite and monitoring intact. The all-failed fixture skips synthesis and
  persists degraded results. Existing successful no-trade cases remain distinct.
- Captured link lists and source-coverage tests exercise TSMC, Micron, Sanofi,
  Oracle and Alibaba navigation failures. These tests replay URLs locally.

## Limits and next acceptance

The fixtures are synthetic regression inputs, not investment evidence. Their
successful conditional plan proves the publication path works; it does not prove
that the September 13 companies warranted trades or that returns will improve.

An earlier manual document-reader probe was described as exposing access
limitations for Tesla, Regeneron and The Trade Desk. That probe was not
persisted as a run artifact or a test fixture and cannot be cited here. What
the September 13 run does record is an HTTP 403 on an investing.com article for
TTD and a MarketScreener capture that was mostly navigation chrome. Updated
ranking tests replay captured URLs locally and do not establish live access to
any issuer. No new paid search service, model credential or recurring work was
introduced.

A new full live model run was not performed during implementation. Use the
[bounded acceptance runbook](2026-09-12-reliability-acceptance.md) for that separate
operator check. The acceptance criterion is falsifiable and singular: every company that
reaches model research produces a readable dossier and a substantive review,
with no unrecovered capacity, transport, parsing or contract failure. Trade
count is not an acceptance criterion at any value, including zero.
