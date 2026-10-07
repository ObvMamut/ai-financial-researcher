# v8: next steps after the survivorship-free result (2026-10-07)

## Where things stand

- **The register.** Eighteen tests have run on this screen family:
  - fifteen on today's constituents;
  - three on a survivorship-free US universe.

  None passed. Removing survivorship cut the closest miss (H1-63, US) from t 2.24 to 1.30.
- **Live runs.** The pipeline works, and its data paths are now guarded:
  - `cfr canary` probes every source;
  - foreign news covers 68 of 114 names;
  - the options chain abstains before the open, rather than reporting blind values.

  But a live run is a measurement of a screen the lab has repeatedly failed to separate
  from noise.
- **What is pending.** OOS-H1-63 is registered and held out in code. It cannot be
  evaluated before about 2027-12.

The plan below runs in order. Each phase ends in a decision or a commit.

## Phase 0: the owner's decision. Do this first; everything else depends on it.

**0.1 Update the keep/cut/stop memo** (`docs/research/2026-10-01-keep-cut-stop.md`).
- Add the point-in-time result.
- Add the canary and the coverage work.
- Add the true cost of a live run, measured from `metadata.json` usage: about
  165K tokens on DeepSeek per run.
- Restate the three options:
  1. **Keep** as a research tool, with no recurring live runs.
  2. **Cut** live runs to zero and keep the code.
  3. **Stop** and archive.

**0.2 Recommendation to carry in the memo.** Option 1, narrowed to one more round of
genuinely new hypotheses (Phase 2) on the point-in-time lab:
- If that round fails too, move to option 2 and leave OOS-H1-63 as the only live
  question until 2027-12.
- A fourth round of screen tuning is not on the table: eighteen null results say the
  price-only screen at these horizons is exhausted.

## Phase 1: small open items (independent of the decision)

1. **Canary outside the session.** Run `cfr canary` after 20:00 UTC. The options probe
   should report the expected abstention. Paste the output into the canary commit's
   follow-up note.
2. **`DOW@2019-04-02`.** This was the one unavailable interval: Alpaca had no bars for Dow
   Inc as of today. Find out whether Dow changed ticker or Alpaca's `asof` mapping
   fails. If it is a rename, add it to `sp500_renames.csv`, rebuild, and re-run the
   history tests. Do **not** re-run the registered point-in-time tests: one interval
   out of 708 cannot change them, and re-running would spend the registration.
3. **Post-mortem cache is not versioned.** `LoadPostMortem` reuses a stored file for
   24 hours, even when the validator that judged its lessons has changed (that
   happened on 2026-10-06). Add a validator version to the stored file and redraw on
   a mismatch. Write a test for it.
4. **VOW3.DE share-class sibling.** Yahoo tags Volkswagen's preference-share stories
   to VOW.DE. Add a small sibling table (local listing → share-class siblings), read
   only by the tag check in `isSubjectRelevant`, with a regression test. Do this only
   if other dual-listed names are affected too: check HEN3.DE and BMW.DE first.
5. **nq100 sample hygiene.** `internal/universe/data/nq100.csv` holds 11 names that are
   not Nasdaq-100 members (NET, SNOW, OKTA, TTD, …). The point-in-time lab no longer
   depends on the sample, but the live scouts do. Either relabel the file "US large-cap
   growth sample", or replace those 11 with current members. Ask the owner which.

## Phase 2: one last round of genuinely new hypotheses, on the point-in-time lab (only if Phase 0 says keep)

**Why this round is different.** Every earlier test drew on signals the lab had already
computed and printed. These three are not in the lab's signal list. Each is a published,
price-only anomaly that the current composite does not contain. The point-in-time
universe has about 500 US names per date, against the sample's 98, so the tests have far
more power.

| Test | Hypothesis | Signal (oriented so higher is bullish) |
|---|---|---|
| **N1: MAX** (Bali, Cakici & Whitelaw 2011) | Stocks with an extreme single-day return last month underperform next month | −(max daily return over the last 21 sessions) |
| **N2: IVOL** (Ang, Hodrick, Xing & Zhang 2006) | High idiosyncratic volatility underperforms | −(σ of residuals from a 63-session market-model regression on the benchmark) |
| **N3: FIP** (Da, Gurun & Warachka 2014) | Momentum built from many small moves persists more than momentum from a few jumps | sign(mom12-1) × (%down days − %up days), interacted with mom12-1 as in the paper |

**Protocol, fixed before any code runs on real data.**
- Register in `docs/workflow/backtest.md`.
- **Statistic:** per-date β-adjusted rank IC at 21 sessions (these are monthly-horizon
  effects), averaged across the two indices, with the Newey-West t (`nwLags(21)`).
- **Sample:** US point-in-time, 2017-09-29..2026-09-25. One run.
- **Bar:** t > +2.5, positive in both halves.
- **Holm family:** m = 21 (the 18 already run plus these 3).
- **Decision:**
  - If a test passes, its Holm p is reported and an OOS registration follows on weeks
    after 2026-09-25. Nothing goes live.
  - If all three fail, the memo moves to option 2.

**Implementation** (in `internal/backtest/panel.go` `observe`):
- Three new signal constants and their computations, from the series cut at the
  rebalance bar (no look-ahead).
- Unit tests on synthetic series with known answers.
- One test in the style of `TestSignalsIgnoreBarsAfterDate`.

**Honest prior.** All three are monthly-horizon effects that are documented to have
decayed after publication. Most likely all three fail. The point of the round is to
close the question with a clean test, not to find a trade.

## Phase 3: only if a Phase 2 test passes

1. Register the passing signal's OOS test, on weeks after 2026-09-25, held out in code the
   same way OOS-H1-63 is (`OOSHoldoutAfter`).
2. Add the signal to the pre-screen as an **inactive** column. It is computed and
   persisted to `prescreen.json`, but carries no weight. The live record then
   accumulates alongside the OOS weeks.
3. Do not change ranking, selection or exits until the OOS test passes.

## Phase 4: if the decision is cut or stop

1. **Cut:** set the local config so `cfr run` is not part of any routine. Keep `cfr canary`,
   `cfr backtest` and `cfr scoreboard` working. Add a CLAUDE.md note that live runs are
   suspended by owner decision, with the date.
2. **Stop:** tag the repository `final-2026-10`. Write a one-page `docs/research/final.md`
   summarising the register, what was built, and the OOS-H1-63 evaluation date in case
   anyone wants to come back for it. Archive.

## Not planned, deliberately

- More live runs "to see". The dedupe rule means runs closer than a week apart add
  nothing, and 550 independent calls would be needed to detect a 50bp edge.
- Re-tuning the composite, the weights, the stops or the model stages. Each has a
  measured null.
- EU/Asia point-in-time membership. There is no free source for the membership history
  of STOXX 50 or the Asia sample, and Yahoo has no prices for delisted foreign names.
  Revisit only if a source appears.
- New paid data or model providers. The CLAUDE.md boundary stands.
