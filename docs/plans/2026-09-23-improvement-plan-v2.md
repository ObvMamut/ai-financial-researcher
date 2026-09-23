# CFR improvement plan (final, 2026-09-23)

**Goal:** ship trade ideas that beat their benchmark over 10–15 sessions, and *know*
whether they do. That means three things, in priority order:
1. Stop the parts that measurably lose money.
2. Make thesis mode able to produce ideas at all.
3. Raise the rate at which the system learns what works, from ~5 scored calls a run to hundreds.

Evidence: `docs/research/2026-09-23-evidence/`. It holds four studies: a point-in-time
backtest (268 names, 207 weekly rebalances, 4,080 trades), thesis forensics (119 challenges),
legacy stage attribution (26 runs), and a literature review (23 sources).

---

## 1. What the evidence says

| Finding | Number | Implication |
|---|---|---|
| Pre-screen composite barely predicts returns | IC10 0.012 (t 0.75). Top-5 excess +0.81% per 15 sessions (t 1.6), almost all in 2024-09 → 2026-09. | The screen is the best thing we have, but it may be a regime effect. Keep it and keep testing it. |
| No free price signal is robust | Only 12-1 momentum has a consistent sign (IC ≈0.03). Reversal ≈0. An out-of-sample blend is *worse*. | No quick signal fix exists. New signals must pass a pre-registered bar. |
| Legacy specialists and Chief add nothing measurable | Domain ICs −0.08 to +0.07. Top base-score third did worst. Chief picks +0.55% vs +0.58% for the names it left out. | Stop letting LLMs *rank*. Measure them in shadow instead. |
| Thesis mode ships nothing, for three specific reasons | 0 ideas, 9.5M tokens. 58% of names never produce a readable dossier. 92 of 92 final dossiers say NONE. 65 of 67 final challenges carry ≥1 objection that free data can never answer. | It is fixable. The binding causes are named, with file and line (§3). |
| Barriers subtract value | 15-session hold, no barriers: +0.71% net per trade. Current 9%/15% stop/target: +0.30%. Every target lowers the return. 86% of live trades exited on time; 2 of 69 hit the target. | Use a catastrophe stop and a time exit. Drop take-profit targets. |
| Patient limit entries are adversely selected | Unfilled ideas' calls +3.09%; filled ideas −0.90%. | Enter at the next open. |
| Live samples cannot decide anything soon | A 50bp / 2-week edge needs ~550 independent calls. | Decide mechanics and signals in the backtest. Score *every* researched name live, not only the five that ship. |

The literature agrees on all of this:
- Big-cap short-term reversal is dead unless conditioned on news (Blitz 2023; Da, Liu & Schaumburg 2014; Dai et al. 2024).
- Post-earnings drift is gone in large caps (Martineau 2022).
- Free-form LLM stock picking has no durable edge (FINSABER, KDD 2026; "Profit Mirage", 2025).
- LLMs do add value when they turn text into structured labels (Lopez-Lira & Tang; Chen, Kelly & Xiu 2023).

## 2. Options considered, with decisions

| Move | Decision | Why |
|---|---|---|
| Kill thesis mode | **Rejected** (owner decision, and the evidence shows it is fixable) | It is switched off only by the rule in §3.5, never removed. |
| Relax only thesis mode's claim gate | Rejected as insufficient | The simulation shipped 0: every passing dossier says NONE. |
| More compaction / budget engineering | Stopped | 12% of tokens go to compaction, and it still fails. Shrink the output instead. |
| Calibrate stop and target in σ | Rejected | Every target lowered returns in 4,080 trades. The walk-forward pick lost about 1 point to plain holding. |
| New blended composite from IC | Rejected for now | The out-of-sample blend is worse. Signals are tested one at a time against a pre-registered bar (§5). |
| LLMs as free-form pickers | Demoted to shadow | No measured value, and the literature agrees. |
| LLMs as structured labellers | **Adopted** | The only literature-backed role, and the thesis dossier already gathers the needed evidence. |
| Score only shipped ideas | Rejected | Far too slow. Score the whole shortlist, every thesis lean, and whole-universe IC on every run. |
| Paid data, or a scheduled daily screener | Out of scope | Hard boundary on paid services. Scheduled jobs are not requested. |

## 3. Wave 1: make thesis mode able to ship (owner priority)

These are the three binding causes, in order, followed by scoring changes. Personas are runtime data, so no recompile is needed for persona edits.

### 3.1 Require a direction
- **Researcher schema** (`agents/thesis-researcher.md`):
  - Add `lean: BUY|SELL` and `conviction: 1–5` to every dossier.
  - `preferred_direction: NONE` is only allowed with `none_reason` ∈ {`event_inside_window`, `evidence_conflict`, `no_mechanism`}.
- **Persona text to rewrite:**
  - `:72–73`: "a final supported dossier has no unresolved material questions and no pending requests".
  - `:105`: "Use NONE … when neither direction qualifies".
  - The symmetric burden on full and partial incorporation (`:94–96`, `:125–127`), which neither can meet.
  - Replacement rule: disclosed uncertainty is allowed, and hidden uncertainty is not.
- **Revision prompt:** in `internal/orchestrator/thesis.go:1184`, change "resolve these issues or stand down" to "resolve what can be resolved and disclose the rest as risks".
- **Go:** add `Lean`, `Conviction` and `NoneReason` to the dossier model. `thesis_validate.go` enforces the enum. A missing lean is a schema error, fixable by the existing single repair.

### 3.2 Split objections that can be answered from ones that can't
- **Challenger schema** (`agents/thesis-challenger.md`): each material issue carries a `category` from a closed enum.
  - Blocking: `grounding`, `attribution`, `positioning_misread`, `stale_or_inaccessible_source`, `direction_unexamined`, `form`.
  - Disclosed risk: `priced_in_unprovable`, `forecast_mechanism`, `future_prices`, `annual_target_horizon`, `issuer_time_unpublished`.
- **New meaning of *supported*:**
  - No blocking-category issue.
  - Every *core* claim supported and attribution-confirmed. The dossier marks ≤3 claims `core: true`; default is the expectations and priced-in claims.
  - No claim disputed.
  - Non-core `unresolved` claims and disclosed risks are allowed. They set `evidence_quality: mixed` and are copied into the plan's `risks` array.
- **Code:**
  - `thesis_contract.go:80–82` becomes a check on core claims only; the every-claim coverage check in `:84–88` stays.
  - `thesis_validate.go:89–103`: stop pushing tool and validator strings into `Unresolved` wholesale. Blocking problems still force watchlist; the others become disclosed risks.
  - `thesis.go:1164–1169` and `:1209–1215`: consistency problems are categorised, not treated as automatic material issues.
  - `thesis_passages.go:33,40`: tool diagnostics no longer land in `unresolved`.
- **Chief persona** (`agents/thesis-chief.md:8–10`, `:65–66`): select on the new *supported* definition and may use `evidence_quality: mixed`.
- **Docs:** `docs/workflow/thesis-research.md` defines the categories.

### 3.3 Shrink the dossier instead of compacting it
- Change the contract to 6 material claims (from 12), 250-character narratives (from 400) and 1 quotation per claim (from 2).
- Raise the default researcher `response_bytes` to 32,768.
- Compaction stays as a fallback. Its narrative-field list stays as it is, because it measures fields and doesn't assume a count.
- Targets: `contract: failed` under 10% of researched names, and a full run under 1M tokens (the latest run used 1.89M).

### 3.4 Structured labels, reused by §6
- Every dossier also emits the fixed-schema event labels: `move_driver` (news / earnings / none / unknown), `pending_binary_event` with a date, and `corporate_action`.
- Cost is one schema field. They feed test C3 and the shadow veto analysis.

### 3.5 Head-to-head scoring, and the switch-off rule
- `cfr scoreboard --control` gains arms `thesis` (shipped) and `thesis-lean` (every researched name at its lean, conviction recorded).
- A backfill script scores the 34 historical leans in `leans.csv` as the first observations.
- **Switch-off rule:** thesis stays on while its interval overlaps or beats the best other arm. It is switched off (code kept) only if, after at least 8 market weeks, `thesis-lean − shortlist` has its whole 95% interval below zero *and* the other arm is available as the replacement.

**Wave 1 acceptance:**
- Two full thesis runs.
- At least one supported dossier with a direction.
- A lean on 100% of researched names.
- `contract: failed` under 10%.
- Tokens under 1M per run.
- All tests green.

## 4. Wave 2: trade mechanics (both modes)

1. **Config first, zero code:** set the entry bands `entry_patience_sigma` and `entry_chase_sigma` (`riskgate.go:310–311`) to ~0.1. The limit then sits at the last close and fills at the next open in the replay (the replay already fills gap-throughs at the open).
2. **Code:**
   - Add an `entry_type: "market_on_open"` idea field.
   - Make `target` optional and informational. The risk gate stops requiring reward:risk and target bands.
   - The stop becomes a catastrophe stop, floored at ≥2σ·√H.
   - Keep the time exit at `timeframe_days` (10–15).
   - Keep risk-budget sizing off the stop distance.
   - Files: `riskgate.go`, `enforce.go`, `agents/chief-analyst.md`, `agents/thesis-chief.md`, `internal/scoreboard/replay.go` (new entries fill at the next open; historical ideas keep the old semantics), `docs/workflow/scoring.md`.
3. **Beta-hedged P&L column in the scoreboard,** using `quant.Metrics` beta against the benchmark. Backtest excess was longs +1.32% and shorts +0.11%, so the scoreboard must show the book's market exposure separately from selection.

**Acceptance:**
- Replay tests cover both entry types.
- A fakebin headless run in each mode emits market-on-open ideas with no binding target.

## 5. Wave 3: the backtest lab, and the few signal tests worth running

### 5.1 Port the scratch backtest to Go
- New `internal/backtest` and `cfr backtest`.
- Point-in-time replay using the *shipping* `quant.Compute` and pre-screen scoring functions. Export them from `prescreen.go` without changing their behaviour.
- Cache-only: no network, no models. Outputs JSON under `.data/backtest/`.
- **Parity:** match the Python spec (`evidence/backtest/*.py`): composite IC10 0.012 ± 0.005, and the no-barrier hold at +0.71% ± 0.1%.
- **Look-ahead test:** signals at date d are identical with or without the bars after d.

### 5.2 Whole-universe IC on every live run
- For each run, rank-correlate every `prescreen.json` row's score with its realised 10- and 15-session excess return.
- One run yields ~268 observations instead of 5. This is the fastest honest read on whether the screen works out of sample, going forward.
- Add it to `cfr scoreboard --control` as `universe_ic`, with a week-clustered interval.

### 5.3 Pre-registered tests
The list below is fixed before any test is run. Each test runs once. It is adopted only if |t| > 2.5, with the same sign in both halves and every region. The report records how many tests were run.

- **C1 — momentum weighting:** raise mom12-1 relative to ret63 (ret63's IC is ≈0).
- **C2 — earnings-announcement premium:** tilt long into names with verified earnings inside the window.
- **C3 — news-conditioned residual reversal:** fade residual moves that had no news, follow those that did. Proxy for news: abnormal volume plus earnings dates. Live, use the labels from §3.4.
- **C4 — beta-adjusted targets:** re-run every IC against beta-adjusted excess, to separate bull-market beta from signal.
- **C5 — breadth:** measure the composite's t-stat on a wider liquid US universe (Alpaca batches, so it is cheap). Widen the universe only if the IC holds.

Anything adopted ships behind `prescreen_version = 2`, with v1 kept as the control arm. Null results are written into the docs.

**Survivorship:** the universe is current constituents only, which flatters momentum and longs. Every lab report says so.

## 6. Wave 4: LLMs as labellers and vetoes (legacy mode)

1. The legacy specialists stop scoring direction and strength. They emit the §3.4 labels plus `veto_reason` from a closed enum.
2. Go ships the top of the shortlist by merit, after vetoes.
3. The Chief writes the plan text and may veto, but may not change direction or rank.
4. The sentiment verdict stays. It is computed in Go, and it is the only domain that was never negative.
5. **Shadow arms for about 6 weeks, then keep or retire:**
   - The old Chief ranking.
   - Vetoed vs kept names.
   - Each label value.

   A stage is retired if its interval does not exclude zero in its favour.
6. Macro: drop the LLM call (weight 0, IC ≈0) and give the Chief a computed regime line instead.

## 7. Wave 0: housekeeping (first, about an hour)

- Commit `CLAUDE.md` (Chief engine drift), this plan and `docs/research/2026-09-23-evidence/`.
- Correct the success criterion in `docs/workflow/scoreboard.md`: n ≥ 60 is a monitoring floor, not a decision threshold. Decisions on mechanics and signals come from the lab. Decisions on model stages come from shadow arms over the full shortlist.
- Moratorium: no more compaction, budget or contract commits unless Wave 1 acceptance fails *because of* them.

## 8. Order and effort

| Wave | Effort | Depends on | Delivers |
|---|---|---|---|
| 0 | ~1 h | — | Honest docs and a durable evidence record |
| 1 (thesis) | ~1 week | 0 | Thesis mode ships ideas, and every lean is scored |
| 2 (mechanics) | 1 day for config + 3 days for code | 0 | Entries and exits stop losing value |
| 5.2 (universe IC) | ~1 day | 0 | Hundreds of scored observations per run |
| 3 (lab + tests) | ~1–2 weeks | 0 | Adopt/kill decisions with real statistical power |
| 4 (labels/veto) | ~1 week + 6 weeks of shadow | 1, 3 | LLMs used where they measurably help |

Waves 1, 2 and 5.2 are independent and can run in parallel on separate branches.

## 9. Risks

- **Regime dependence:** the only positive result comes from one bull market (2024-09 → 2026-09). Every report shows the two halves separately.
- **Thesis fixes could let weak ideas through.** The mitigations: blocking categories stay blocking, the plan-review challenge stays, and the `thesis-lean` arm exposes quality within weeks.
- **Overfitting in the lab:** the test list is fixed before running, the bar is |t| > 2.5 with a cross-half and cross-region sign check, and every test run is counted.
- **Look-ahead bias in the LLMs:** only forward-dated live scoring counts for model stages. The lab never uses models.

## 10. Verification (every change)

- `gofmt -l`, `go vet ./...`, `go test ./...` (repo-native).
- Fakebin hermetic runs for any pipeline change.
- Acceptance numbers per wave, taken from the actual run artifacts, never asserted.
- `cfr scoreboard --control` (with intervals) is the standing report after each live run.
