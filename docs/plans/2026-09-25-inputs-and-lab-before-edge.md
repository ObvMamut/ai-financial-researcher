# CFR plan, 2026-09-25: fix the inputs, extend the lab, stop spending on thesis until its arm reports

**Goal (unchanged from v2):** ship ideas that beat their benchmark over 10–15 sessions, and *know*
whether they do.

**Headline.** Nothing measured so far beats zero once beta is removed. The composite's IC10 is 0.004
against beta-adjusted returns (v2 Status, C4). Its top-5 excess is +0.115% in the first half and
+1.513% in the second (`evidence/backtest/FULL_REPORT.txt:229–230`). Live closed ideas average
+0.01% excess (n=28, `.data/calibration.json`). This plan does not claim a new edge. It fixes four
input and record faults found in the latest runs. Each one corrupts either what ships or what the
scoreboard can learn. It also runs the three lab experiments that can answer the open questions without
spending on models. And it puts thesis mode on hold until its lean arm reports.

## 1. Findings, ranked by strength of evidence

| # | Finding | Evidence | Strength |
|---|---|---|---|
| F1 | **Every run on 2026-09-24 had no verified earnings calendar.** That is all 7 thesis acceptance runs and the legacy run. The AlphaVantage key's 25 daily requests went on per-ticker news before the calendar's one bulk request was made. No run between 09-07 and 09-23 lost the calendar. | `metadata.json` of all 8 runs on 09-24 contains "earnings calendar unavailable … daily budget of 25 requests is exhausted". A scan of `runs/2026-09-0*/1*/23*` finds 0 such runs. `avcalendar.go` explains that the calendar costs one bulk request and is cached per UTC day, but nothing reserves that request. | Direct, 8/8 runs |
| F2 | **The calendar gap confounds the v2 thesis verdict.** Thesis puts the verified date into the researcher's temporal facts (`thesis.go:877`, `pack.EventDates`). In Run 7, at least 4 of 12 final reviews block on unverified or undated earnings timing: CDNS ("no claim supplies a quantified earnings release date"), ACN ("Oct 1 time … secondary-only"), ON ("estimated Nov 2"), COST ("issuer-published release time missing"). v2's conclusion that "the remaining lever is research quality" came from runs missing this input. It does **not** explain the 0/20 before 09-24, because those runs had the calendar. | `runs/2026-09-24T12-53-48/ideas.json` `decisions[].review_reason` | Direct for the confound. The size of the effect is unknown. |
| F3 | **News relevance: a symbol tag is not a story about the company.** Alpaca marks an article as `Related` whenever the symbol appears anywhere in its `symbols` list (`alpacanews.go:130`). So market wraps count as company coverage. SAP.DE's four "tagged" items were AMD, NVIDIA and Micron premarket notes and a European market close. It got `Coverage=true` and a news score of 0, and that 0 is what carried it past the evidence floor (`riskgate.go:792`, which only checks whether a key exists). Across the run, a crude check finds 56 of 116 news facts don't name the company in the headline, summary or the first 300 characters of the body. | `runs/2026-09-24T12-58-48/data/news.json` `ByTicker["SAP.DE"]`; the check is a name/ticker substring match, so it is indicative only | Direct for SAP.DE; 56/116 is approximate |
| F4 | **`selection.json` does not record why names were left out.** `selection.go:505–520` sets `excluded` (sector_cap/below_cut) on every unshipped row. The artifact has no `excluded` key on any of the 7 unshipped rows, including ORCL, which was sector-capped. No test asserts it. `metadata.json` records no build commit, so it is not possible to tell which binary wrote the artifact. The local, untracked binary `./cfr` is dated 09-23 18:41, which is before `bc83a3c`. The sector-cap arm and the vetoed arm both depend on this field. | The artifact vs `selection.go`; `grep excludedSectorCap *_test.go` finds nothing | Direct. The root cause (a stale binary or a code path) is unconfirmed. |
| F5 | **Thesis spend has returned nothing.** 27 runs have shipped 0 ideas at 1.3–1.5M tokens each. The one stronger-model run (Run 7) produced 1 supported dossier and still shipped 0. v2's own rule decides thesis by the `thesis-lean` arm, and about 60 leans from 09-23/24 mature at 15 sessions around **2026-10-15**. | v2 "Live acceptance" and "Run 7" sections | Direct |

## 2. The three leads

**Lead 1: confidence=0 ideas (SAP.DE, TTD).** Do **not** add a confidence floor. Under `merit_veto`,
confidence is the base score, which is informational. Its measured predictive value is nil:
- shipped-confidence IC was +0.033 (n=76, H10) and −0.079 (n=62, H15);
- the top base-score tercile did worst (+0.19% vs +0.97/+1.08%);
- specialist *disagreement* buckets did no worse than agreement (quant disagree +0.83 vs agree +0.32
  at H10).

Source: `evidence/legacy/detail.md:31,75–80,143`. A zero means the specialists net-disagree with the
scout, and that is not evidence against the trade.

The real defect is F3. SAP.DE passed the evidence floor only on news that was not about SAP. Once
relevance is fixed, the existing floor drops it (quant alone). TTD's missing sentiment is the designed
abstention (a non-directional computed verdict), and TTD had fundamentals and news, so it ships
correctly under current rules.

**Lead 2: the sector cap cost ORCL.** This is confirmed mechanically. The default `max_per_sector` is 2
(`riskgate.go:42`), and MSFT and SAP.DE already held the two IT slots (`pickBook`, `selection.go:211`).
The decisive merit gap was SAP.DE 1.4924 vs ORCL 1.4847, a 0.5% difference. Two things **cannot** be
claimed:
- that ORCL was the better trade, because the Chief's shadow rank has no measured value (Chief picks
  +0.55% vs +0.58% for the names it left out);
- that its base of 66 means anything, per lead 1.

One run cannot size the cap. Experiment E2 does.

**Lead 3: the other regime.** This is partly answered already. The lab's first half (2022-10 → 2024-09)
has composite IC10 −0.005 and top-5 excess +0.115%. The edge is second-half only
(`FULL_REPORT.txt:79,97,229–230`). `.data/calibration.json` covers only live ideas since 2026-06
(n=28), so it cannot help. The lab already accepts `cfr backtest --years 10` (`cmd/cfr/backtest.go:31`,
`yahooRange` gives "10y"/"max"). That covers 2018 Q4, 2020 and 2022. This is E1, and it needs no new
data or keys. Caveat: current constituents only, so survivorship flatters longs more the further back
the replay goes.

## 3. Wave A: input and record fixes (small code, root cause, both modes)

A1. **Reserve the calendar request.** In every run, fetch the AlphaVantage `EARNINGS_CALENDAR` before
any per-ticker AlphaVantage call, or hold one request of the limiter's daily budget for it. Files:
`internal/marketdata/avcalendar.go`, `alphavantage.go`, and the limiter. Add a test: with a budget of
N already spent by news, the calendar still loads. Acceptance: two runs back to back on one UTC day
both carry `EventDates`.

A2. **News relevance.** An item counts as coverage only if the company is its subject. That means the
headline or summary names the ticker root, the ADR symbol or the company name, *or* the item's
`symbols` list holds 3 or fewer symbols. Everything else keeps its existing "surfaced, not tagged"
label, and a name whose items are all non-subject becomes uncovered (the path
`headlineFacts` already uses). Files: `internal/marketdata/alpacanews.go:130`, `newsfilter.go`
(`relatesTo`), and the same rule for `yahoonews.go`. Test with a fixture of the SAP.DE wrap items.
Acceptance: re-derive coverage from saved `news.json` files (no models) and report how many shipped
ideas across legacy runs would have failed the floor.

A3. **Persist and test the selection record.**
- Add a test that `data/selection.json` carries `excluded` for sector_cap, below_cut and vetoed.
- Find out why the 12-58-48 artifact lacks it.
- Record the build commit (`runtime/debug.ReadBuildInfo`, `vcs.revision`, plus a dirty flag) in
  `metadata.json`.

Files: `internal/orchestrator/selection.go`, `selection_test.go`, `internal/store`, `internal/model`.

A4. **Scoreboard arms** (the v2 open follow-ups plus one new arm):
- split `shipped` by `ideas.json.selection`;
- add a `sector-capped` arm (names excluded by sector_cap, at the scout's direction), which is the live
  counterpart of E2.

Files: `internal/scoreboard/control.go`.

Wave A adds no model calls and does not touch the cost split.

## 4. Wave B: lab experiments (no models, no keys; pre-registered, run once each)

The rule is v2's: adopt a change only if |t| > 2.5 with the same sign in both halves and every
region, and record every test that is run.

- **E1: long history.** Run `cfr backtest --years 10`. Add per-calendar-year slices to
  `internal/backtest/analyze.go`, alongside the halves. Report the composite IC10/IC15 (plain and
  beta-adjusted) and the top-5 excess per year. Decision: if the composite's beta-adjusted top-5
  excess is not positive in a majority of years, the docs stop describing the screen as having an
  edge, and the TUI says so.
- **E2: live-book replay with a cap grid.** Add to the lab a book builder that mirrors live
  construction:
  - per index, the top-k by |composite|, with k set to the scouts' typical nomination count;
  - then `max_per_index`;
  - then `max_per_sector` ∈ {1, 2, 3, off};
  - then the top 5.

  Measure the mean beta-adjusted excess, the weekly book sd and the worst 4-week drawdown for each cap.
  This answers lead 2 with about 500 weekly books instead of one anecdote. Reuse the sector field
  (`panel.go:410`) and `picksPerIndex`/`barrier.go`.
- **E3: which side carries the result.** Report top-5 beta-adjusted excess separately for longs and
  shorts. Plain excess is long +1.316% and short +0.111%. If shorts are ≤0 beta-adjusted in both
  halves, pre-register "long-only merit_veto" as a config test for the live `shipped` arm.

## 5. Wave C: thesis on hold (no code)

Make no further thesis runs or persona and gate iterations until the `thesis-lean` arm has its
15-session read of the 09-23/24 leans (≈2026-10-15). On that date:
- if the lean interval is ≤0 or overlaps zero widely, keep thesis off, as v2's switch rule already
  implies;
- if it is positive, one thesis run with A1 in place (calendar present) tests the F2 confound before
  any cost-split decision.

The cost-split question stays with the owner, as v2 left it. Nothing in this plan requires breaching
the split.

## 6. Order and effort

| Wave | Effort | Depends on | Delivers |
|---|---|---|---|
| A1 calendar | ½ day | — | Verified event dates in every run |
| A2 relevance | 1 day | — | Honest news coverage and evidence floor |
| A3 record | ½ day | — | Attributable artifacts; working exclusion arms |
| A4 arms | ½ day | A3 | merit_veto measured on its own; sector-cap arm |
| B E1–E3 | 2–3 days | — | Regime answer, cap sizing, side attribution |
| C thesis | 0 | the arm's maturity date | A decision from data, not from another run |

## 7. Risks

- **A2 may shrink coverage** and push more names under the floor, so fewer ideas ship. That is the
  intended result: shipping fewer is allowed.
- **Survivorship in E1 grows with history.** Weight the IC and beta-adjusted results over raw long
  returns, and say so in the report.
- **E2 models the scouts as a composite cut.** The LLM scouts' nominations differ. The live
  `sector-capped` arm is the check.
- **Multiple testing:** 3 new tests are added to v2's count. The bar stays unchanged.

## 8. Verification (every change)

- `gofmt -l`, `go vet ./...`, `go test ./...`.
- Fakebin headless runs in legacy mode for A1–A3, asserting `EventDates`, `excluded` and the commit
  field in the artifacts.
- A2 is re-scored from saved `news.json`. E1–E3 numbers come from `cfr backtest --json`. The
  scoreboard numbers come from `cfr scoreboard --control`. Report every number from its artifact,
  never asserted.

## Status (2026-09-25)

| Wave | State |
|---|---|
| A1–A4 | proposed |
| B E1–E3 | proposed, pre-registered above |
| C | on hold until ≈2026-10-15 |

