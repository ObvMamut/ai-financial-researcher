# Chief engine and research reliability: continuation

Implementation of the follow-up to the September 17 plan, audited September 21
and completed September 22. This record supplements the original implementation
record; it does not claim live model acceptance or investment performance.

## Changes

- Production prompt assembly now uses the real byte budget after redaction and
  wrapping. Optional diagnostics/history can be omitted whole; profile version 2
  names omissions. Required compaction originals, quotations, dossier identity,
  macro and company floors survive or cause an explicit preparation refusal.
- Evidence allocation reserves mandatory sections first. Optional history cannot
  cause a required-passage refusal simply by occupying its space.
- Zero-attempt failures report payload `not_attempted`. Infeasible compaction
  reports failed preparation, its measured allowance and zero dispatched calls.
- Native `cfr acceptance-manifest` uses the config loader, runtime defaults,
  engine gates and actual persona directory. Its explicit projection excludes
  credentials; hashes describe resolved config, issuer sources, optional holiday
  overrides, personas and dirty files. File and environment credentials are
  registered before precedence can hide them. A decoded JSON leakage check catches
  full credentials and their prefixes before output. The shell is only a wrapper.
- Yahoo retains local-symbol-first relevance checks and permits one existing
  major-exchange ADR query only for an unresolved recent local feed. The local
  ticker and ADR origin are explicit. No price conversion is inferred.
- Issuer discovery ranks releases ahead of indexes and subscription pages and
  prefers the seed language. Prompt allocation prioritizes uncited substantive
  documents over navigation. URL safety, bounded requests and access failures remain.
- Typed source diagnostics travel through provider caches, packs, frozen copies,
  company artifacts and metadata. Unique-ID counts distinguish failed, withheld,
  expected and context-only observations, with scoreboard region/reason counts.
  Source summaries share one renderer in headless text and TUI. Existing severity
  gates and legacy error arrays remain. Generic warnings are labelled generic;
  their text is not heuristically reclassified.
- Scoreboard `stage_progress` uses the result summary's separate researched and
  reviewed counts; legacy `completed_research` keeps its reviewed-workflow meaning.
  Cohorts include Chief selection/model and recorded prompt/response versions.
  Unknown historical versions remain unknown.

## Captured reasoning audit

Source: local `runs/2026-09-15T17-00-30/data/`. These are historical observations;
none is a new model evaluation. The runtime personas already require short-horizon
mechanisms, source attribution, dates/units and neutral-positioning abstention.
No persona rewrite or new scalar-reference schema was justified by this sample.

| Case / artifact | Findings and retained treatment |
| --- | --- |
| JNJ / `research-4a4e4a.json` | Watchlist/revise, 13 claims. Missing issuer corroboration and post-anchor expectations/returns remain gaps. Future conditions are not historical evidence. The next scheduled event lies outside the window. Overlapping sales inference does not establish the claimed mechanism. |
| TTE.PA / `research-5454452e5041.json` | Watchlist/revise, 14 claims. USD 88.50 ADR target versus EUR 80.49 local quote lacks a ratio; stale crude and missing excerpts/expectations do not support a short-horizon move. Numerical-basis and source checks remain binding. |
| ASML.AS / `research-41534d4c2e4153.json` | Rejected/reject, 13 claims. A 2028 EUV order was overextended into issuer backlog; annual US-line IV was compared with daily local volatility; attribution and short-horizon bridge were missing. Neutral positioning was improperly treated as negative, but independent rejection grounds remain. |
| TTD / `research-545444.json` | Issuer homepage text (333 characters) and filings-index text (377 characters) were navigation. The filings index was not recognized by the previous URL classifier; it is now context-only. |
| NOKIA.HE / `research-4e4f4b49412e4845.json` | Historical issuer seed failed. The separate capture below returned accessible issuer material; this does not retroactively repair the historical run. |

The inspected trusted computed quotations were at least 30 characters; no short
scalar was demonstrated to fail solely because of the document quotation floor.
Existing temporal, attribution, numerical conversion, stale-price, continuation
and condition-review regressions remain the enforcement boundary. No scheduled
catalyst requirement was added. No chart-based thesis rule was relaxed.

**LLY correction:** 20,493 raw / 20,326 compact bytes describe the captured
*compaction response*. The original dossier's compact payload was 20,883 bytes,
above the 20,480 limit. Normalization accepts the already-produced compaction
response; it does not eliminate the original recovery call.

## One bounded source capture

Public no-key document reads only, no model calls and no app credentials.
Capture: 2026-09-21 12:23:08.720–12:23:34.869 UTC. Maximum eight attempts per
company; 34 total attempts. Raw file: `.data/source-capture-2026-09-21.json`.
The committed replay fixture is
`internal/marketdata/testdata/regional-capture-2026-09-21.json`: the same full
extracted texts and SHA-256 hashes, with discovery link arrays removed to reduce
fixture size. Embedded coverage in that fixture is the capture-time measurement;
tests recompute coverage under the current classifier.

| Company | Attempts | Accessible | Substantive primary, current classifier | Distinct substantive hosts |
| --- | ---: | ---: | ---: | ---: |
| ASML.AS | 8 | 8 | 5 | 1 |
| STLAM.MI | 1 | 0 | 0 | 0 |
| NOKIA.HE | 8 | 8 | 4 | 1 |
| 2330.TW | 8 | 8 | 7 | 1 |
| 9988.HK | 8 | 8 | 6 | 1 |
| BHP.AX | 1 | 0 | 0 | 0 |

Stellantis and BHP returned HTTP 403, preserved as failed retrievals. Each region
has three eligible companies, two with substantive documents, sixteen accessible
documents and one failed document. Asia-Pacific has thirteen substantive primary
documents across two issuer hosts; Europe has nine across two hosts. Hosts are
not independent reporting origins. TSMC's translated releases are still separate
captured documents, not seven independent events.

On this exact corpus, stricter navigation classification reduces Europe's naive
substantive count from fourteen to nine (two ASML results indexes and three Nokia
navigation/subscription pages). Asia-Pacific remains thirteen. This is a correction
to evidence accounting, not lost access. With a 1,600-character text budget per
company, replay verifies substantive passages in actual researcher prompts and
profiles for all four accessible companies. Capture itself has zero model-visible
spans; visibility is measured in offline prompt preparation. No second capture was
performed after ranking changes. Broad regional coverage improvement and live
reasoning improvement remain unproven; the fixed-corpus tests establish selection,
classification and visibility, not a claim of additional live source access.

## Standing regression matrix

All model calls below use fake CLIs or loopback HTTP fixtures. The source corpus
is replayed locally. Full repository tests also retain existing calendar, risk,
coverage, response-contract and frozen-pair checks.

| Area | Regression evidence |
| --- | --- |
| Resolved config / secrets | `cmd/cfr/acceptance_test.go`; `TestManifestConfigHonorsChiefEndpointEnvironmentOverrides`; config Chief migration/precedence tests; no dispatch from manifest |
| Chief engine × mode × pipeline | `TestThesisChiefPipelineRoutingMatrix`: Claude/API × single/independent × direct/corrective; `TestLegacyAPIChiefCorrectiveRepromptEndToEnd`: both engines and both modes, actual initial/corrective driver calls; `TestChiefRoutingMatrix`: dedicated credentials/model/caps |
| Fallback and provenance | `TestHistoricalClaudeToAPIFallbackStillWorks`, `TestFallbackDisabledExplicitlyWithCredentialsPresent`, `TestThesisChiefFailureUsesConfiguredDeepSeekFallback`, provenance and all-failed tests |
| Failure/cancellation | `TestPermanentAuthFailureStopsUnchangedRetries`, `TestRunAgentCLITimeoutKillsSubprocess`, `TestCancelledThesisCallDoesNotDispatch`, `TestAPIChiefKeepsSynthesisTimeoutAndStage`, existing malformed-output tests |
| Response bytes / recovery | `thesis_response_test.go`; `TestCompactionFeasibilityAcrossTheSixRealOversizedOriginals`; `TestCompactionProtectsAllNonNarrativeFields`; `TestSchemaRepairAndCompactionNeverChain`; `TestCompactionRefusesADoomedCallWhenProtectedContentAlreadyExceeds` |
| Real prompt fitting | `TestProductionPromptFitsOptionalsAndPersistsRequiredRefusal`, `TestSuccessfulCompactionRetainsOriginalReviewEvidence`, `TestCapturedRevisionAndChallengePromptsFit` |
| Chief board / corrective | `TestTwelveRealisticDossiersFitTheChiefInputBudget`, `TestCorrectiveChiefPromptFitsOnATwelveDossierBoard`, `TestRequiredFloorOverflowNamesItsCompaniesAndKeepsEveryCandidate` |
| Redaction before measurement/dispatch | Existing user changes preserved: `TestPreparePromptMeasuresSectionsAfterRedaction`, `TestWrapPromptRedactsEvenUnredactedData`, `TestOutboundPromptToEngineCannotCarryACredential` |
| Attribution/time/units | `TestSonyAttributionRequiresQuotedEvidenceAndConsistentReview`, `TestNumericalComparisonNormalizesAndRejectsInvalidBasis`, `TestFutureEvidenceAndTimestampPrecision`, stale-price tests |
| Source capture / visibility | `TestCapturedRegionalSources`, `TestCapturedIssuerReleaseReachesResearchPrompt`, `TestYahooNewsMappedFallbackIsBoundedAndKeepsLocalIdentity` |
| Filtered versus lost | `TestOwnershipDiagnosticsDistinguishUnrelatedFromFetchFailure`; FPI and unavailable-provider tests |
| Cache / frozen / history | `TestSourceDiagnosticsSurviveCacheAndFrozenCopies`, `TestResearchDiagnosticsKeepDossierAndReviewProgressSeparate`, existing cohort/history and research-pair tests |

## Verification and remaining operator work

Final checks on the completed code passed:

- `go test ./... -count=1` (all packages; loopback enabled for local fixtures).
- `GOCACHE=/tmp/cfr-go-cache go build ./...`.
- `GOCACHE=/tmp/cfr-go-cache go vet ./...`.
- `gofmt -l .` (no output) and `git diff --check` (clean).

The native manifest command also succeeded against resolved local settings and
wrote valid version-2 JSON to `/tmp/cfr-acceptance-manifest-2026-09-22.json`,
without provider or model dispatch.

The temporary Go cache is used because the sandbox cannot write the normal cache.
The source-only collector was executed once during the capture above; tests never
rerun it. The new corrective-routing and ownership-classification checks also
passed independently before the final suite.
No live model run, local credential change, provider switch, commit or push is part
of this continuation. WP10's bounded live acceptance and matched frozen-pair
10/15-session evaluation remain pending. The previously noted credential rotation
is an external operator item; local redaction tests do not establish revocation.
