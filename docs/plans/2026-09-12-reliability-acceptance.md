# Bounded thesis reliability acceptance

This is an operator runbook, not authorization for automatic live execution.
Implementation and local fixture checks are separate from live acceptance. No
recurring runs, model switches, extra credentials or automatic reruns are needed.

## Preflight and immutable manifest

Use the already configured engines and credentials. Record the Git revision and
whether the tree is dirty; for a dirty tree also record hashes of changed source
files. Save persona hashes, redacted resolved-configuration hash, run anchor,
selected tickers/indices, configured provider output caps, role byte budgets,
retry/round/document limits and deadline. Never put credentials in the manifest.
Do not raise output caps to make an acceptance run pass. Ensure the separate Chief
fallback remains explicitly configured rather than inheriting cheap-engine keys.

Use defaults: 3 rounds, 8 document attempts per company, 24 candidates, 12-company
shortlist; the declared role budgets are in `cfr.toml.example`. No provider-truncation recovery is enabled. The one repair allowance permits
either formatting a complete malformed response or compacting only narrative
prose in a complete schema-valid oversized dossier. Protected evidence remains
unchanged, and compaction must receive explicit independent review.

A company has at most `2 * (rounds + 3)` logical research/review calls including
format repairs: research rounds, initial challenge, revision and final challenge.
Each uses at most the existing retry-attempt limit; truncations stop after one
attempt. Add discovery/triage (each with one possible format repair), one Macro,
up to five plan reviews before and after correction (each with one repair), the
configured primary Chief attempts and the optional fallback attempt budget.
Record this worst-case bound from the actual configuration. Provider token caps
bound metered HTTP attempts; CLI usage may remain incomplete and cannot be
converted into an invented dollar estimate. All calls remain subject to existing
per-call timeouts and the operator deadlines below.

## Fixed evidence panel

Freeze these six names before inspecting results:

| Europe | Asia-Pacific |
| --- | --- |
| ASML.AS | 2330.TW |
| STLAM.MI | 9988.HK |
| NOKIA.HE | BHP.AX |

Capture issuer seed pages and ranked discovered release links, with at most eight
reads per name. Preserve accessible responses, unavailable/401/403 results and
extraction failures. Do not substitute successful companies for failed ones.
Replay both the previous presentation and new passage selection against the same
captured source corpus. Count accessible documents, usable primary text and
model-visible primary evidence for all six names, with unchanged denominators.
A source-access or telemetry improvement alone is insufficient: require additional
usable primary evidence delivered to model inputs in each region.

The corrected/additional seeds were checked against official pages during
implementation: [TSMC latest news](https://pr.tsmc.com/english/latest-news),
[Nokia stock-exchange releases](https://www.nokia.com/newsroom/stock-exchange-releases/),
and [BHP financial results](https://www.bhp.com/financial-results). Search/web
availability does not prove accessibility through the application's document
reader. Actual six-name source acceptance therefore remains open until captured.

## Model stages and stopping rules

1. Run one thesis single-stock case for each fixed-panel company using the
   existing `cfr run --research-mode thesis --ticker TICKER --json` entry point.
   Enforce a ten-minute deadline per case; cancel when it expires. Preserve all
   failures. A correctly deferred event case counts as not-run and supplies no
   evidence about company-model reliability.
2. Inspect artifacts and diagnostics. Stop the acceptance sequence on truncation,
   unreadable dossier, unavailable substantive challenge, missing material
   evidence or unrecovered exhausted capacity. Diagnose locally before scheduling a new
   designated acceptance attempt; do not repeat automatically.
3. Once representative cases pass, run one full independent thesis run with the
   existing selected indices and a twenty-minute deadline. Do not require trades.
   Every company that reaches model research must produce a readable dossier and
   substantive challenge. Count event deferrals separately. A fallback success
   must retain primary-failure provenance and the degraded run status.
4. Report exact run IDs, budgets, input profiles, actual versus unknown usage,
   latency, request outcomes, evidence visibility and every failure. Mark live
   reliability accepted only if the designated full run meets every criterion.

## Prospective evaluation and remaining operator item

Only after reliability acceptance, register existing frozen legacy/thesis pairs
with matched captured evidence. Evaluate 10- and 15-session outcomes once mature;
retain empty/degraded arms, benchmarks, execution-cost assumptions and unavailable
outcomes. Do not re-run models while refreshing outcome prices or automatically
alter calibration. No claim of improved returns follows from local tests or a
positive trade count.

Provider-side rotation of the previously exposed Alpha Vantage credential remains
unconfirmed. Redaction tests demonstrate local handling, not provider revocation.
