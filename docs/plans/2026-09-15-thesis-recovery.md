# September 13 independent-run recovery

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

## Verification

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

Earlier bounded document-reader probes exposed access limitations for Tesla
(403), Regeneron (timeout) and thin Trade Desk extraction. Updated ranking tests
do not establish reliable live access to every issuer. No new paid search service,
model credential or recurring work was introduced.

A new full live model run was not performed during implementation. Use the
[bounded acceptance runbook](2026-09-12-reliability-acceptance.md) for that separate
operator check. Assess readable dossiers, completed reviews, evidence visibility,
conditional/actionable plans and failures together; trade count alone is not an
acceptance criterion.
