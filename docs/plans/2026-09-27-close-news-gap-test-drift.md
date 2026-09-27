# Merge, push, and plan v4: close the news-coverage gap, test the one documented effect

## Context

The 2026-09-25 plan is implemented on `inputs-and-lab-before-edge` (25 commits over `main`, all
reviewed, `go test ./...` green). The user asked to merge and push it, to critique the analysis
behind it, and to propose the next improvement plan.

### Critique of my own analysis (what the last round got right, wrong, or overstated)

1. **A2 was reported as "honest news coverage", but it fixed the minority path.** 39 of the 45
   news-scored shipped ideas rest on AlphaVantage coverage. `alphavantage.go:328-368`
   (`articlesFor`) keeps *any* item AV tags with the ticker, with no relevance threshold, and never
   calls `isSubjectRelevant`. A market wrap counts as coverage there exactly as it did on Alpaca.
   The status line in the plan overstates the fix.
2. **The E1 verdict rests on a rule I pinned after seeing the result.** Reading net of the lab's
   30bp is the defensible reading, and it is disclosed. But a "majority of years" count was always
   weak evidence. The decisive facts were already there: beta-adjusted IC10 0.006 (t 0.66), and
   survivorship that flatters every early year. Conclusion that survives any reading: **the
   price-only composite has no measurable edge on a 10–15-session horizon.**
3. **The one effect with a documented mechanism on this horizon has never been tested.**
   Post-earnings drift is the live pipeline's first-tested archetype. The lab has no drift
   archetype (`docs/workflow/backtest.md` "Known departures"). C2 was marked untestable because
   `evaluate` (`analyze.go:365-397`) demands every region. But SEC submissions JSON is keyless and
   point-in-time for US filers: 8-K Item 2.02 gives the earnings-release dates, and 10-Q/10-K give
   the filing dates. So drift and the earnings-announcement premium are testable for US names.
4. **A cache the docs call permanent is not.** `edgarreports.go:33-36,116-118` and CLAUDE.md say the
   SEC daily index "caches forever". In fact `Cache.Get/Set` hashes today's UTC date into the key
   (`cache.go:72-77`), so every new day re-walks about 40 daily index files. It costs latency, not
   money, but it is a docs/code mismatch.
5. **Parked:** a default 4-year `cfr backtest` prints "E1 → not triggered", because
   `run.go:279` runs `e1Decision` on every window.
6. **Process:** parallel worktrees roughly tripled throughput, but session limits killed 6 agents
   twice mid-task, and five merges needed two merge agents. Next time, fewer, larger lanes and
   earlier merges.

### Where that leaves the system

Neither the screen nor the model stages has shown an edge. Live data cannot adjudicate: about 550
independent calls are needed, and the scoreboard has 28 closed. What has not been tested is
PEAD, US-only. The next plan fixes the remaining input fault, repairs the cache, and runs that one
test in the lab before any further live or model spend is argued for.

## Step 0 — Merge and push (on approval)

- `git checkout main && git merge --ff-only inputs-and-lab-before-edge`. `main` is still at
  390a119, the branch's base, so this fast-forwards.
- Push over HTTPS through gh's credential helper, because SSH is denied in this session:
  `git -c credential.helper='!gh auth git-credential' push https://github.com/ObvMamut/ai-financial-researcher.git main`
- **Note:** local `main` is already **48 commits ahead of `origin/main`**, whose last push was
  2026-09-22. The push publishes those 48 plus these 25. Nothing in them is secret: `cfr.toml`,
  `runs/` and `.data/` are untracked. Check with `git diff --stat origin/main..main -- '*.toml' '.env*'`
  before pushing.
- Then delete the merged branch locally with `git branch -d`.

## Wave A — input and cache honesty (small, no model calls)

**A1. AlphaVantage news relevance** (`internal/marketdata/alphavantage.go` `articlesFor`, `:157`, `:274-289`)
- Apply the same subject rule as Alpaca/Yahoo: reuse `isSubjectRelevant` (`newsfilter.go`) on AV's
  title + summary, with the same ctx-carried name and alias lookup.
- Also keep AV's own `relevance_score`, at a documented floor, as a second route. **Ruling:**
  choose the floor by the saved data rather than guess it. Report the relevance-score
  distribution of items that pass versus fail the text rule across `runs/*/data/news.json`, then
  pin the floor.
- Non-subject AV items become the same "context, not about this company" facts. They are not
  coverage, and they are not weighted into sentiment.
- Acceptance: re-run the env-gated `TestNewsRelevanceAcceptanceAudit` over all 45 news-scored
  shipped ideas, AV now included. Report how many flip and how many then fail the evidence floor
  (`riskgate.go` `checkPriceOnlyEvidence`). Update `docs/research/2026-09-25-news-relevance.md`,
  or add a 09-27 addendum.

**A2. SEC daily index: really permanent** (`edgarreports.go`, `cache.go`)
- Add a cache path with no date in its key (for example `cache.GetPermanent/SetPermanent`, keyed
  on the URL only) for immutable published documents.
- Exempt that path from `Prune`.
- Use it for daily-index files older than today.
- Test: a second fetch on a different "day" makes zero HTTP hits (httptest server).
- Fix the comments and CLAUDE.md.

**A3. Gate the E1 decision** (`internal/backtest/run.go:279`)
- Make `e1Decision` a decision only when `Years >= 10`.
- Otherwise record it as a comparison look labelled so, and do not count it in `TestsRun`.
- Update `backtest_test.go`.

## Wave B — test post-earnings drift in the lab (US-only, keyless, pre-registered)

**B1. Point-in-time US earnings dates** (new `internal/marketdata/edgarhistory.go`)
- Per US constituent (the unique names in sp500 + nq100 samples, about 120), fetch SEC submissions
  JSON through the existing `edgarProvider` (UA with contact_email, 10 requests/s limiter,
  `edgar.go:50,112`).
- Decode `filings.recent` **and** the older `filings.files` pages, adding `items` to the decoded
  fields (`edgarform4.go:120-133` today reads only name, accession, date, form and doc).
- Return, per ticker, the dates of 8-K filings with Item 2.02 (earnings release) and of
  10-Q/10-K filings.
- Cache with A2's permanent path. Past filings never change; re-fetch only the current page.
- No key is needed. EDGAR requires `contact_email`, which the user config holds.

**B2. Drift signal in the panel** (`internal/backtest/panel.go`, `orchestrator/drift.go`)
- Export `computeDrift` (pure, `drift.go:107-162`) or add a thin exported wrapper. `backtest`
  already imports `orchestrator`, so there is no cycle.
- Add a `Filings map[string][]time.Time` field to `Data`, filled by B1 in `backtest.Run`.
- In `observe` (`panel.go:262-334`), add `SigDrift`: the latest Item-2.02 date on or before
  `date`, then `computeDrift(cut, bcut, filing, SigmaDaily)`, then its decayed `Score()`. NaN when
  there is no event inside 25 sessions.
- Add a second signal, `SigEarnWindow`: an indicator that an Item-2.02 date falls inside the next
  10 sessions. This is C2. Its date comes from past filing cadence, so it is not look-ahead. **Ruling:**
  predict the next release as the last Item-2.02 date plus about 91 days, ±7 days. Record it as an
  approximation in the doc. If it cannot be defined point-in-time, record it as untestable again.
- Keep `row.ReportDate` unset in the `PrescreenRow`, so `classifySetups` and the composite are
  unchanged and the existing tests stay comparable (`panel.go:109-111`, `run.go:50`).

**B3. Region-scoped test machinery** (`internal/backtest/analyze.go` `evaluate`, `slices`)
- Add an `evaluateScoped(region)` variant: cells filtered to one region, Newey-West t over those,
  and same sign required in H1 and H2 within the region.
- **Ruling:** a US-only test is registered as US-scoped. If it passes, it may be adopted **for US
  names only**. v2's every-region bar is unreachable here by construction, because there is no
  point-in-time EU/Asia source. The bar is otherwise unchanged: |t| > 2.5, both halves, beta-adjusted.

**B4. Pre-register and run once** (`docs/workflow/backtest.md` Wave B table, new "Wave D" rows)
- **D1 (drift IC):** US-scoped Newey-West t of the beta-adjusted IC10 of `SigDrift` > 2.5, with
  the same sign in both halves.
- **D2 (drift book):** a US top-5-by-|drift| weekly book (at the drift sign), beta-adjusted 15-session
  excess **net of 30bp** > 0 with NW t > 2.5, same sign in both halves.
- **D3 (C2, earnings premium):** US-scoped IC10 of `SigEarnWindow` > 0, t > 2.5, both halves, or
  recorded untestable.
- Each adds to the tests-run count, 11 decision tests after this round. Run `cfr backtest --years 10`
  once, with the same keyless run discipline as 2026-09-26.
- Write up in `docs/research/2026-09-2x-lab-drift.md`, every number from its artifact.
- **Decisions fixed now:**
  - D1 or D2 passes → a follow-up plan makes drift the primary US idea source. This means merit
    ordering for US names by drift first, plus a `drift` live arm. That is a separate plan, not
    this one.
  - Both fail → the docs, CLAUDE.md and the TUI state that no signal tested on this horizon has
    shown an edge (screen, model stages, PEAD). The plan then recommends the owner run live only as
    measurement, at reduced cadence. Changing that cadence stays the owner's call.

## Wave C — unchanged checkpoints (no code)

- **About 2026-10-15:** the `thesis-lean` arm's 15-session read (v2 / 09-25 Wave C).
- **Same date:** a first read of `sector-capped` and `shipped-merit_veto`. Report n and interval
  only; decide nothing below n = 30.

## Critical files

- `internal/marketdata/alphavantage.go`, `newsfilter.go`, `cache.go`, `edgarreports.go`,
  new `edgarhistory.go`, `edgarform4.go` (decode `items`, `filings.files`)
- `internal/orchestrator/drift.go` (export)
- `internal/backtest/panel.go`, `analyze.go`, `run.go`, `backtest_test.go`
- `docs/workflow/backtest.md`, `docs/workflow/output-schema.md` (AV coverage rule), CLAUDE.md
  (cache and drift wording)

## Verification

- Every change: `gofmt -l .`, `go vet ./...`, `go test ./...`.
- A1: fixture tests from real AV items in `runs/*/data/news.json` (a wrap tagged at low relevance
  must not count). The acceptance audit reports its numbers, with nothing asserted.
- A2: an httptest test showing a second-day fetch makes zero requests.
- B1: an httptest submissions server with `recent` plus a `files` page, and Item 2.02 parsing.
  Then one live fetch per ticker (keyless, UA with contact_email).
- B2/B3: synthetic panels. Drift NaN outside 25 sessions. Point-in-time (no filing after `date`
  is used). Scoped `evaluate` ignores other regions.
- B4: `cfr backtest --years 10 --json` run once from the repo, with keys unset
  (`env -u ALPHAVANTAGE_API_KEY -u FRED_API_KEY -u DEEPSEEK_API_KEY -u CFR_API_KEY`), 0 unavailable
  and no shortfall WARNING. Numbers are quoted with artifact line citations.
- Execution: the same subagent-driven process. Three lanes, since A1/A2 (marketdata) and B2/B3
  (backtest) are independent, B1 depends on A2's permanent cache, and B4 runs last. Merge early.
