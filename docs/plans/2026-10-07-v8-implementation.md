# v8 Implementation Plan: the owner's decision, the small fixes, and the last round of hypotheses (N1–N3)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task by task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Execute `docs/plans/2026-10-07-v8-next-steps.md` under the owner's decisions of 2026-10-07:
- update the keep/cut/stop memo;
- land the Phase 1 fixes;
- pre-register N1 MAX, N2 IVOL and N3 FIP, then run them once on the point-in-time (PIT) US lab;
- apply the registered decision. If all three fail, Cut follows. If any passes, Phase 3 follows.

**Architecture:**
- **New signals.** The three signals are lab-local helpers in a new `internal/backtest/anomalies.go`. They are added to `observe` as three new `Sig` constants. The existing cross-section and IC machinery then scores them at every horizon with no further wiring.
- **The tests.** A new `anomalies` report block evaluates them at 21 sessions with the halves-only bar (`applyScopedBar`, `nwLags(21)`). It also carries its own m = 21 Holm family, built from a sourced table of the 18 earlier p-values.
- **Existing semantics are kept.** `tests_run` and the in-report Holm family stay as they are.
- **Phase 1** is five independent small changes: data, scoreboard, news and the canary.

**Tech stack:**
- Go: the standard library plus the existing charmbracelet TUI.
- The existing Python 3 history build scripts.
- Keyless Yahoo and SEC, plus the Alpaca key already in `./cfr.toml`.
- No models, no new dependencies.

**Spec:** `docs/plans/2026-10-07-v8-next-steps.md`. Read it alongside this plan.

## Owner decisions (asked and answered 2026-10-07)

1. **Keep, narrowed.** One last pre-registered round, N1–N3, on the PIT lab.
   - If all three fail, the memo moves to option 2 (Cut) and Phase 4.1 runs with no further question.
   - A pass earns only an OOS registration and an inactive pre-screen column (Phase 3). Nothing goes live.
2. **nq100: replace** the 11 non-members with current Nasdaq-100 members. Do not relabel the file.

## Global Constraints

Every task includes these.
- **The protocol, verbatim from the spec:**
  - Statistic: "per-date β-adjusted rank IC at 21 sessions (these are monthly-horizon effects), averaged across the two indices, with the Newey-West t (`nwLags(21)`)".
  - Sample: "US point-in-time, 2017-09-29..2026-09-25. One run."
  - Bar: "t > +2.5, positive in both halves".
  - Holm family: "m = 21 (the 18 already run plus these 3)".
- **Registration first.** The N1–N3 registration is committed before any N1–N3 Go code exists.
- **No real-data look before the run.** Once the signals exist, any real-data `cfr backtest` prints their IC21. So from Task 8's merge until the one registered run (Task 11), nothing runs a real-data backtest, sample or PIT. Tests use synthetic data only.
- **One run.** A run that crashes before printing any statistic may be repeated. Any other failure is recorded as failed evidence and is not re-run.
- **The earlier PIT tests are not re-decided.** "Do not re-run the registered point-in-time tests" (spec 1.2). The N1–N3 run recomputes the in-report C, E and H figures. Any difference from `docs/research/2026-10-07-evidence/pit-9y.json` is recorded with its cause and is never re-decided.
- **Nothing goes live.** No change to ranking, weights, selection, stops or exits ("Do not change ranking, selection or exits until the OOS test passes"). No fourth round of screen tuning. No live `cfr run` "to see". No EU or Asia PIT work.
- **No paid or metered APIs.** No new credentials, no model calls, no new dependencies. Run the lab with `env -u ALPHAVANTAGE_API_KEY -u FRED_API_KEY -u DEEPSEEK_API_KEY -u CFR_API_KEY`. Never print or commit anything from `./cfr.toml`, `.env*` or credentials.
- **Do not push.** Local `main` is 34 commits ahead of the real `origin/main` (`e4a3565`); the tracking ref is stale. Push only when the owner asks.
- **Use a fresh binary.** Build to the scratch dir or use `go run ./cmd/cfr`. The `./cfr` binary at the repo root is stale (2026-09-23) and has no `canary`.
- **Checks.** Each task ends with `gofmt -l .` empty, `go vet ./...` and `go test ./...` clean, with the output shown.
- **Write-ups.** Every number quoted in a write-up or the memo is traceable to `json:<line>` in an evidence file.

## Rulings (recorded; no further questions)

- **R0, the memo.** Updated in place, `docs/research/2026-10-01-keep-cut-stop.md`, with the title suffix "updated 2026-10-07". Task 2 gives the content. Measured cost: 139,033–165,286 tokens per legacy run on DeepSeek (mean 152,715) over the four runs 2026-09-24..2026-10-07:
  - 112–128K on `deepseek-chat` (scouts, specialists, post-mortem);
  - 27–41K on `deepseek-v4-pro` (Chief);
  - 165K when the run redraws the 24-hour post-mortem, about 140K when it reuses it.
- **R1, the canary.** The spec's premise is wrong. After 20:00 UTC (16:00 ET under daylight time) Yahoo reports `POST`, and the options probe abstains only in `PRE`/`PREPRE` (`yahoooptions.go:57,290`). So:
  - Run the canary once after 20:00 UTC, as the spec asks; it records post-close behaviour.
  - Run it once in a weekday pre-open window, 05:00–13:00 UTC, where the abstention was observed (05:13Z, 06:11Z, 06:49Z). That run is the one expected to show `before the US session: the legs abstain as designed`.
  - Raw output goes in `docs/research/<date>-evidence/canary-<HHMM>Z.txt`, with a section in `docs/research/2026-10-07-acceptance.md`. The repo has no git-notes habit, so the "follow-up note" is that section plus a commit citing `c634b0c`.
  - If the POST run *fails* on an options warning, that is a canary false alarm. Debug it with superpowers:systematic-debugging: capture `marketState` and the diagnostics. Do not widen the abstention without a captured fixture.
  - If no execution session is live in the pre-open window, the status table records it as the owner's one-liner, `go run ./cmd/cfr canary`.
- **R2, the canary guard.** `probeAlpacaBars` indexes `s.Bars[len(s.Bars)-1]` with no empty check (`internal/marketdata/canary.go:190`). An empty answer with no error would panic the probe that exists to catch silent failures. Guard it and fail with "no bars", with a regression test. This is in scope as part of Phase 1.1.
- **R3, Cut (Phase 4.1).** "Set the local config so `cfr run` is not part of any routine" means:
  - Remove `Bash(cfr run)` from the gitignored `.claude/settings.local.json` and add `ask` rules for `cfr run`, `./cfr run` and `go run ./cmd/cfr run`, using the update-config skill.
  - Nothing schedules `cfr run`: there is no cron, runit service or timer, which was checked. `~/.claude/settings.json` is left alone, because it is global, not local; the final report mentions it.
  - Add a dated CLAUDE.md note.
  - `cfr canary`, `cfr backtest` and `cfr scoreboard` stay working, verified by tests and one canary run.
- **R4, E3.** E3's rule fires on both 9-year US replays (`pit-9y.json`, `sample-9y.json`). It is not a registered decision on those runs, which were decided on the 10-year run where it did not trigger. The N-run write-up and the memo disclose it. No test is added, because the round is closed to new hypotheses beyond N1–N3.

- **R5, DOW.** It is not a rename. Yahoo shows Dow Inc. trading as `DOW` on NYSE from 2019-04-01 to today, with `corporateActions: []`. So the spec's "add it to `sp500_renames.csv`, rebuild" branch does not apply.
  - The working hypothesis is that Alpaca's `asof` mapping fails for a reused symbol. Dow Chemical held `DOW` until 2017-08-31, and the failing request was asof 2026-10-07 from 2016-06-29.
  - Other reused tickers in the same batch priced fine: DD, FOX, FOXA, IR, CTVA.
  - A code change is made **only** if the keyed probe shows the mapping works from the interval's own start (Task 5, branch C). Disabling the mapping (`asof=-` or no `asof`) is never used as a fallback, because that is what would splice Dow Chemical onto Dow Inc.
- **R6, the share-class siblings.** The spec's condition is met by other names, not by the two it named.
  - HEN3.DE is covered (13/20 tagged to the sample line) and BMW.DE is tagged correctly (20/20), so neither needs a sibling.
  - VOW3.DE (0/20; VOW.DE 19/20), 3988.HK (A-share 601988.SS), 2318.HK (601318.SS, 82318.HK) and GOOGL on the Yahoo path (all 20 tagged GOOG) lose tagged stories to a sibling. The probe was run on 2026-10-07 at 19:45–20:00Z.
  - The table holds only those evidence-backed pairs. It is one-directional, and only `isSubjectRelevant`'s tag check reads it.
  - Two alias rows from the same probe ride along: `2318.HK,Ping An`, without which the 2318.HK sibling row cannot fire, since headlines say "Ping An", and `BMW.DE,BMW`, because only 1 of 20 headlines spells out "Bayerische Motoren Werke".
- **R7, freezing the nq100 sample for OOS.** The registered OOS-H1-63 universe is "the `internal/universe/data/*.csv` files as of this commit" (`backtest.md:360-364`). Before nq100.csv changes, `--evaluate-oos` gets byte-identical embedded copies of the four index files.
  - The files were last changed in `3b38520`; `5cf834e` touched only `aliases.csv`.
  - Their SHA-256 values are pinned in a test.
  - This is a procedural guarantee of the registered sample, not a protocol change. It is written into the OOS section as such.
- **R8, the nq100 replacement rule.** Use data from the repo only. Applied 2026-10-07 to `history/nq100_changes.csv`, whose newest row is 2026-09-14, and `history_sectors.csv`, it gives an exact list.
  - Each of the 11 non-members (BIIB, CHTR, ILMN, JD, NET, OKTA, ON, SNOW, TEAM, TTD, ZS) is replaced by a current member missing from the sample, as follows:
    - same sector first;
    - longest current membership;
    - not in `sp500.csv`, so the nq100 scout gains names no other scout screens;
    - ties broken alphabetically;
    - a slot whose sector runs dry takes the longest-tenured remaining candidate from any sector.
  - Twelve current members have no sector anywhere in the repo (ALAB, ALNY, ARM, CCEP, CRWV, FER, MSTR, NBIS, RKLB, SHOP, SPCX, TRI) and are ineligible, because a row needs a sector.
  - The result:
    - **IT:** ADI, ADSK, MCHP, WDAY, NXPI, FTNT.
    - **Health Care:** GEHC. The only HC candidate, so the second HC slot goes to **ADP**.
    - **Communication Services:** CMCSA, WBD.
    - **Consumer Discretionary:** MAR.
  - The sp500 overlap goes from 35 to 33 of 56: BIIB and CHTR leave, and no replacement is in `sp500.csv`.

## Review Focus

Each line names an input that no task's happy-path test reaches, and the behaviour expected. Each is pinned by a test in the task named.
1. **Yahoo throttles during the one run.** This host got HTTP 429 from every Yahoo endpoint from about 19:37Z on 2026-10-07. The two PIT benchmarks come from Yahoo `HistoryRange`, and a failed fetch is only logged as unavailable. Expected: the run stops before computing any statistic, so it counts as a crash and may be repeated. It must not print NaN β-adjusted statistics that spend the registration (Task 11).
2. **Look-ahead through slice capacity.** `cut.Bars` shares its backing array with the future. Expected: N1–N3 are finite at the probe date and identical under a truncated or perturbed future (Task 9).
3. **A missing, short or flat benchmark for IVOL.** Expected: NaN, never 0 and never a panic; N1 and N3 unaffected (Task 9).
4. **A sample-universe run after N1–N3 exist.** Expected: the N tests read "comparison", not "run". `tests_run` and the in-report Holm family are unchanged: 15 at 10 years on the sample, 11 on PIT (Task 10).
5. **A stored post-mortem written before the version field existed** (today's `.data/postmortem.json`). Expected: version 0, so it is redrawn and not reused for 24 hours (Task 4).

## Execution topology (subagent-driven; the controller is the main session)

**Wave 0: controller, sequential, on branch `v8` from `main`.**
- **Task 0, setup.**
  - `git switch -c v8`.
  - Copy this file verbatim to `docs/plans/2026-10-07-v8-implementation.md`.
  - Update the memory note `cfr-v8-plan-pending` with the owner decisions.
  - Commit.
- **Task 1, the N1–N3 registration.** One commit, before any N code.

**Wave 1: parallel.** Each implementer is a fresh subagent with `isolation: "worktree"`, branched from `v8` after Task 1. Each is followed by a spec-compliance review subagent and a code-quality review subagent; docs-only tasks get one review. After both reviews pass, the controller rebases the branch onto `v8` and fast-forwards, one branch at a time, then re-runs `go test ./...`.
- Tasks 2, 3, 4, 5, 6, 8, 9 and 11.
- The controller runs the canary passes (Task 3R) when the clock allows.

**Wave 2.**
- Task 7 needs Task 6.
- Task 10 needs Task 9.

**Wave 3: controller.**
- Task 12: the full checks, then the one run. Everything must be merged first: Task 5 can add DOW to the panel, and Task 11 protects the run.

**Wave 4.**
- Task 13: the write-up.
- Then the registered branch:
  - all fail: Task 14, Cut;
  - any pass: Tasks 15–16, Phase 3.

**Wave 5.**
- Task 17: whole-branch review, verification, fast-forward of `main`, memory notes, status table. No push.

---

## Phase 0

### Task 2: the keep/cut/stop memo, with the decision recorded (Phase 0.1, 0.2)

**Files:** Modify `docs/research/2026-10-01-keep-cut-stop.md` (61 lines).

**Interfaces:** Produces the memo that Task 14 appends the outcome to.

- [ ] **Title:** `# Keep, cut or stop: a memo for the owner (2026-10-01, updated 2026-10-07)`.
- [ ] **"The question."** Replace it with: "Eighteen pre-registered lab tests have now run: fifteen on today's constituents and three on a survivorship-free US universe. None has found an edge that survives cost, on the live 15-session hold or on 21- and 63-session holds. This memo sets out three options. The owner decided on 2026-10-07; the decision is recorded at the end."
- [ ] **Evidence table:**
  - Add the row `| The screen on a survivorship-free US universe, 2017-09-29..2026-09-25 (PIT-IC10, PIT-H1-63, PIT-E1) | β-adj. IC10 t −0.30; top-5 at 63 sessions +2.30% net per hold, t 1.30 (same-window sample +3.56%, t 2.24); positive in 3 of 10 years | 2026-10-07-pit-lab.md |`.
  - Change the last row to `| The 15 sample tests, Holm-adjusted; the 3 point-in-time tests | smallest Holm p 0.159 (H1-63); PIT one-sided p 0.617 / 0.097 / 0.945. None below 0.05 | 2026-10-01-lab-horizon.md; 2026-10-07-evidence/pit-9y.json |`.
- [ ] **The H1-63 paragraph** becomes: "The closest result was H1-63, the screen's top names held for about three months. On the survivorship-free universe the same statistic falls to t 1.30. Survivorship had inflated it by 1.26 points per hold, about a third. It is no longer a lead. Its out-of-sample test, OOS-H1-63, uses weeks after 2026-09-25 and is registered and held out in code. It is the one open question, and it cannot be evaluated before about 2027-12."
- [ ] **New section `## What changed since 2026-10-01`**, one bullet each:
  - the point-in-time US lab (`cfr backtest --universe pit`);
  - OOS-H1-63 registered (`09a35a6`) and held out in code (`b7709b8`);
  - `cfr canary`: nine probes, exit 1 on a silent failure (`c634b0c`);
  - foreign news coverage from 25/115 to 68/114 (mapped 27/27, unmapped 41/87; Korea, Taiwan, India and Thailand still dark);
  - the options chain abstains before the US open rather than reporting blind values, and is not cached (`8e42983`);
  - live-system risk and data fixes since this memo, none of them a signal change: the merit_veto pair cap (`ce22704`), silent catastrophe-stop placement (`78e0b60`), earnings-in-hold stamping (`e9f9276`);
  - E3's rule fires on both 9-year US replays, but it is not a registered decision there (R4).
- [ ] **Option 1:**
  - *Cost* becomes: "139–165K DeepSeek tokens per legacy live run (mean 152,715), measured from `metadata.json` usage over the four runs 2026-09-24..2026-10-07. That is 112–128K on deepseek-chat (scouts, specialists, post-mortem) and 27–41K on deepseek-v4-pro (Chief). It is 165K when a run redraws the 24-hour post-mortem and about 140K when it reuses it. The lab is keyless and makes no model calls."
  - *What it buys* becomes: "OOS-H1-63's single evaluation (~2027-12), the reusable lab, and one last registered round of genuinely new hypotheses."
- [ ] **Option 2:**
  - "The last live run was 2026-10-07 (14:40Z)."
  - *Risk* gains: "`cfr canary` now measures that repair in one command."
- [ ] **Option 3:** "*What it loses:* OOS-H1-63's evaluation and the reusable lab."
- [ ] **`## Recommendation`** is replaced with: "**Option 1, narrowed to one more round of genuinely new hypotheses** (N1 MAX, N2 IVOL, N3 FIP), run once on the point-in-time lab at a 21-session horizon. If that round fails too, move to option 2 and leave OOS-H1-63 as the only live question until 2027-12. A fourth round of screen tuning is not on the table: eighteen null results say the price-only screen at these horizons is exhausted."
  - Delete the two "next" items. Both are done: the OOS registration and the PIT source.
- [ ] **New section `## Owner decision (2026-10-07)`:** "Option 1, narrowed, as recommended. If N1–N3 all fail, option 2 follows with no further question. Separately, nq100.csv's 11 non-members are replaced by current members."
- [ ] **The closing line** becomes: "No setting or default changed with this memo."
- [ ] **Check:** every number above matches its source: `pit-lab.md:35-37,48-53`; `acceptance.md:73-79`; `lab-horizon.md:103-119`; the four runs' `metadata.json` (`runs/2026-09-24T12-58-48`, `2026-10-06T18-07-24`, `2026-10-07T05-13-11`, `2026-10-07T14-40-51`).
- [ ] **Commit:** `v8 Phase 0: memo gains the PIT result, canary, coverage and measured run cost; owner keeps, narrowed`.

## Phase 1

### Task 3: the canary's Alpaca probe fails on an empty answer instead of panicking (Phase 1.1)

**Files:**
- Modify: `internal/marketdata/canary.go:178-197`.
- Test: `internal/marketdata/canary_test.go`.

**Interfaces:** None.

- [ ] **Step 1: write the failing test.** Copy the healthy `fakeYahoo` bodies from `TestCanaryPassesOnHealthySources`.

```go
func TestCanaryFailsOnAnEmptyAlpacaAnswer(t *testing.T) {
	fakeYahoo(t, healthyOptions, healthySearch) // the bodies TestCanaryPassesOnHealthySources uses
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"bars":{},"next_page_token":null}`)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("CFR_ALPACA_BASE", srv.URL)
	res := RunCanary(context.Background(), CanaryConfig{AlpacaKeyID: "k", AlpacaSecret: "s", Cache: NewCache(t.TempDir()), Now: canaryNow})
	if r := byName(res)["alpaca-bars AAPL"]; r.Status != CanaryFail || !strings.Contains(r.Detail, "no bars") {
		t.Errorf("an empty Alpaca answer = %+v, want a failure saying no bars", r)
	}
}
```

  If `healthyOptions`/`healthySearch` are inline literals in that test, hoist them to package-level vars in the same commit.
- [ ] **Step 2:** `go test ./internal/marketdata -run TestCanaryFailsOnAnEmptyAlpacaAnswer -v`. Expected: a panic (index out of range or nil dereference at `canary.go:190`).
- [ ] **Step 3:** insert the same guard `probeChart` uses (`canary.go:92-95`), right after the `err` check in `probeAlpacaBars`:

```go
	if s == nil || len(s.Bars) == 0 {
		r.Status, r.Detail = CanaryFail, "no bars returned"
		return r
	}
```

- [ ] **Step 4:** `go test ./internal/marketdata -run 'TestCanary' -v` passes.
- [ ] **Step 5: commit.** `canary: an empty Alpaca answer fails the probe instead of panicking`.

### Task 3R: the canary outside the session (controller, R1)

- [ ] **Build:** after Task 3 merges, `go build -o $SCRATCH/cfr ./cmd/cfr`, where `$SCRATCH` is the session scratch dir.
- [ ] **Health check:** one request, `curl -s -o /dev/null -w '%{http_code}' -A 'Mozilla/5.0' 'https://query1.finance.yahoo.com/v8/finance/chart/AAPL?range=5d&interval=1d'`. If it is 429, wait at least an hour before the canary.
- [ ] **Post-close run.** At ≥ 20:00 UTC, from the repo root (`./cfr.toml` supplies the Alpaca key and `contact_email`; the canary probes no AlphaVantage, FRED or DeepSeek):
  - `{ date -u; $SCRATCH/cfr canary; echo "exit $?"; } > docs/research/2026-10-07-evidence/canary-$(date -u +%H%M)Z.txt 2>&1`.
  - Expected: `yahoo-options AAPL` passes with "N facts", since Yahoo is in `POST`.
  - If it fails on a chain warning, use superpowers:systematic-debugging. Capture the `marketState` and the diagnostics, and make no code change without a captured fixture.
  - If Yahoo is still 429, record it as a true positive of the canary.
- [ ] **Pre-open run.** On a weekday at 05:00–13:00 UTC, the same command, writing to `docs/research/<that date>-evidence/canary-<HHMM>Z.txt`. Expected: `pass  yahoo-options AAPL  before the US session: the legs abstain as designed (…)`. If no session is live in that window, the status table records the owner's one-liner `go run ./cmd/cfr canary`.
- [ ] **Write-up.** Add `## Canary outside the session (follow-up to c634b0c)` to `docs/research/2026-10-07-acceptance.md`: each run's UTC time, the implied `marketState`, all nine status lines, and whether the abstention appeared and why.
- [ ] **Commit:** `Canary outside the session: post-close and pre-open runs (follow-up to c634b0c)`.

### Task 4: the stored post-mortem records its validator version and is redrawn on a mismatch (Phase 1.3)

**Files:**
- Modify:
  - `internal/scoreboard/postmortem.go`: the struct at :51-63, `ParsePostMortem` at :84-100;
  - `internal/orchestrator/postmortem.go:56-58`.
- Test:
  - `internal/scoreboard/postmortem_test.go`;
  - `internal/orchestrator/postmortem_test.go` (create it if absent; package `orchestrator`).

**Interfaces:**
- Produces `scoreboard.PostMortemValidatorVersion` (const int = 3).
- Produces the `PostMortem.ValidatorVersion` field (JSON `validator_version`).
- Produces `orchestrator.reusablePostMortem(*scoreboard.PostMortem) bool`.

- [ ] **Step 1: write the failing tests.**

```go
// internal/scoreboard/postmortem_test.go
func TestPostMortemRecordsTheValidatorVersion(t *testing.T) {
	a := attributionWith(/* one cell with n >= MinCellN, as TestPostMortemRefusesAThinCell builds it */)
	pm, err := ParsePostMortem(`{"n_closed":10,"lessons":[]}`, a)
	if err != nil {
		t.Fatal(err)
	}
	if pm.ValidatorVersion != PostMortemValidatorVersion {
		t.Fatalf("parsed version %d, want %d", pm.ValidatorVersion, PostMortemValidatorVersion)
	}
	dir := t.TempDir()
	if err := pm.Save(dir); err != nil {
		t.Fatal(err)
	}
	if got := LoadPostMortem(dir); got == nil || got.ValidatorVersion != PostMortemValidatorVersion {
		t.Fatalf("round trip lost the version: %+v", got)
	}
	// A file from before the field existed decodes as 0: unrecorded.
	legacy := `{"computed_at":"2026-10-06T18:10:08Z","n_closed":12,"lessons":[],"rejected":["x"]}`
	if err := os.WriteFile(filepath.Join(dir, PostMortemFile), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := LoadPostMortem(dir); got == nil || got.ValidatorVersion != 0 {
		t.Fatalf("legacy file = %+v, want version 0", got)
	}
}

// internal/orchestrator/postmortem_test.go
func TestStoredPostMortemIsReusedOnlyUnderTheSameValidator(t *testing.T) {
	at := func(age time.Duration) string { return time.Now().Add(-age).UTC().Format(time.RFC3339) }
	cur := scoreboard.PostMortemValidatorVersion
	for _, c := range []struct {
		name string
		pm   *scoreboard.PostMortem
		want bool
	}{
		{"none stored", nil, false},
		{"fresh, same validator", &scoreboard.PostMortem{ComputedAt: at(time.Hour), ValidatorVersion: cur}, true},
		{"fresh, unrecorded validator", &scoreboard.PostMortem{ComputedAt: at(time.Hour)}, false},
		{"fresh, older validator", &scoreboard.PostMortem{ComputedAt: at(time.Hour), ValidatorVersion: cur - 1}, false},
		{"stale, same validator", &scoreboard.PostMortem{ComputedAt: at(25 * time.Hour), ValidatorVersion: cur}, false},
	} {
		if got := reusablePostMortem(c.pm); got != c.want {
			t.Errorf("%s: reusable = %v, want %v", c.name, got, c.want)
		}
	}
}
```

  Also add `TestPostMortemValidatorVersionPinsTheCellFamilies` in the scoreboard package:
  - Build an `Attribution` that has a cell in every family, using the helpers `attributionOf`/`cellAttribution`.
  - Collect the family prefixes that `cellList()` (`attribution.go:355`) renders.
  - Compare them to the pinned set `{"agreement","coverage","earnings","entry","sector","setup"}`, together with `PostMortemValidatorVersion == 3`.
  - Failure message: "the cells a lesson may name changed: bump PostMortemValidatorVersion and update this pin".
- [ ] **Step 2:** `go test ./internal/scoreboard ./internal/orchestrator -run 'ValidatorVersion|SameValidator' -v`. Expected: compile failure, because the symbols are undefined.
- [ ] **Step 3: implement.** In `internal/scoreboard/postmortem.go`:

```go
// PostMortemValidatorVersion names the rules ParsePostMortem enforces. A stored
// post-mortem judged under other rules is redrawn, not reused
// (internal/orchestrator/postmortem.go). Bump it whenever the cells a lesson
// may name, or how a lesson is matched, counted or capped, changes.
//   0: unrecorded — every file written before this field existed.
//   1: cells matched by their bare bucket key, as first shipped.
//   2: a24f23e (2026-10-06) — a cell may be named as the table renders it.
//   3: e9f9276 (2026-10-07) — the earnings-in-hold cells.
const PostMortemValidatorVersion = 3
```

  Then:
  - Add `ValidatorVersion int `json:"validator_version"`` to `PostMortem`, after `ComputedAt`.
  - Set `ValidatorVersion: PostMortemValidatorVersion` in `ParsePostMortem`'s literal (:89-92).

  In `internal/orchestrator/postmortem.go`, replace :56-58:

```go
	stored := scoreboard.LoadPostMortem(cfg.DataDir)
	if reusablePostMortem(stored) {
		return postMortemResult{PM: stored}
	}
	if stored != nil && stored.Age() < postMortemMaxAge {
		log(ch, fmt.Sprintf("post-mortem: the stored lessons were judged by validator v%d and this build enforces v%d, so they are redrawn",
			stored.ValidatorVersion, scoreboard.PostMortemValidatorVersion))
	}
```

  and add:

```go
// reusablePostMortem reports whether stored lessons may stand in for a fresh
// draw: judged by this build's validator, and younger than postMortemMaxAge.
// The check lives here, not in LoadPostMortem, so `cfr postmortem` still shows
// whatever file is stored.
func reusablePostMortem(stored *scoreboard.PostMortem) bool {
	return stored != nil && stored.ValidatorVersion == scoreboard.PostMortemValidatorVersion &&
		stored.Age() < postMortemMaxAge
}
```

- [ ] **Step 4:** `go test ./internal/scoreboard ./internal/orchestrator` passes, including `TestPostMortemLessonsAreEnforcedAndReachTheChief` (`integration_test.go:1523`).
- [ ] **Step 5: docs.** One sentence in `docs/workflow/output-schema.md`'s `postmortem.json` entry: `validator_version`, and redraw on a mismatch.
- [ ] **Step 6: commit.** `Post-mortem: record the validator version; a stored file judged by other rules is redrawn`.

### Task 5: `DOW@2019-04-02` (Phase 1.2, superpowers:systematic-debugging)

**Files:**
- Possibly modify: `internal/backtest/pit.go:104-117`, and test `internal/backtest/pit_test.go` (branch C only).
- Modify: `docs/research/2026-10-07-pit-lab.md:26`, `docs/research/2026-10-07-survivorship-sources.md:45-48` (it wrongly lists DOW among "renamed tickers").

**Interfaces:** None.

- [ ] **Step 1: reproduce with keyed requests**, without exposing the key.
  - Write a throwaway `main` package inside the module, at `$REPO/.dowprobe/main.go`. Never commit it, and delete it afterwards.
  - It loads the config the way `cmd/cfr` does, from the repo root, and calls `GET https://data.alpaca.markets/v2/stocks/bars` for `symbols=DOW` with `timeframe=1Day`, `adjustment=all`, `feed=sip`, `end=2026-10-07`.
  - It prints only HTTP status, bar count and first and last bar dates.
  - Variants:
    - (A) `asof=2026-10-07`, `start=2016-06-29`: the failing request.
    - (B) `asof=2026-10-07`, `start=2019-04-01`.
    - (C) no `asof`, `start=2016-06-29`.
    - (D) `asof=-`, `start=2016-06-29`.
    - (E) `asof=2019-04-02`, `start=2016-06-29`.
- [ ] **Step 2: decide by the evidence.** Record the five lines in `docs/research/2026-10-07-pit-lab.md` under the Unavailable row.
  - **(A) returns bars:** transient. Record it and change no code.
  - **(A) empty, (B) returns Dow Inc. bars from 2019-04:** the mapping breaks when the window reaches the previous holder's years. This is branch C: implement Step 3.
  - **Only (C) or (D) return bars:** the mapping itself fails for DOW. Record it. Make no fallback, per R5.
  - **Nothing returns bars:** Alpaca has no Dow Inc. bars. Record it.
- [ ] **Step 3 (branch C only): one retry from the interval's own start.**
  - Failing test in `pit_test.go`. A fake loader returns no bars for `DOW` when `start` is before 2019-04-02, and `fakeLoader` bars otherwise. Use a member whose `Interval.From` is 2019-04-02.
  - Assert:
    - `data.Series["DOW@2019-04-02"]` is non-empty, and DOW is not in `unavailable`;
    - a symbol that stays empty after the retry is still listed with the exact message `"<key>: no Alpaca bars as of <asof>"`;
    - a member priced by its batch makes no second call (count the calls).
  - Then implement this in `loadPIT`'s member loop:

```go
		for _, m := range g {
			s := got[m.Constituent.Ticker]
			if (s == nil || len(s.Bars) == 0) && m.Interval.From.After(start) {
				// A reused ticker can come back empty when the window reaches the
				// previous holder's years (DOW: Dow Chemical to 2017-08, Dow Inc.
				// from 2019-04). Ask once more from the interval's own start, under
				// the same asof, so the mapping is never switched off.
				again, err := loader.HistoryAsOf(ctx, []string{m.Constituent.Ticker}, m.Interval.From, now, a)
				if err != nil {
					return unavailable, fmt.Errorf("point-in-time prices for %s as of %s: %w", m.SeriesKey(), a.Format("2006-01-02"), err)
				}
				s = again[m.Constituent.Ticker]
			}
			if s == nil || len(s.Bars) == 0 {
				unavailable = append(unavailable, fmt.Sprintf("%s: no Alpaca bars as of %s", m.SeriesKey(), a.Format("2006-01-02")))
				continue
			}
			data.Series[m.SeriesKey()] = s
		}
```

- [ ] **Step 4:** `go test ./internal/backtest ./internal/universe`. The second package is the history tests; nothing in history changes.
- [ ] **Step 5: docs.**
  - `survivorship-sources.md:45-48`: DOW is not a renamed ticker; state the finding.
  - `pit-lab.md:26`: the cause, and either "priced from 2019-04 since <commit>" or "remains unavailable".
  - **Do not re-run the registered PIT tests.**
- [ ] **Step 6: commit.**
  - Branch C: `PIT: retry an empty reused ticker from its interval start; DOW was never renamed`.
  - Otherwise: `PIT: DOW@2019-04-02 is <cause>, not a rename`.

### Task 6: freeze OOS-H1-63's universe in code (R7)

**Files:**
- Create: `internal/universe/data/frozen/oos-h1-63/{sp500,nq100,eu50,asia100}.csv`, from `git show 3b38520:internal/universe/data/<key>.csv`.
- Modify:
  - `internal/universe/universe.go:16-50` (embed, loader);
  - `cmd/cfr/backtest.go:51`;
  - `internal/backtest/run.go` (`Run`'s `EvaluateOOS` guard);
  - `docs/workflow/backtest.md`, the OOS section.
- Test: `internal/universe/universe_test.go`, `internal/backtest/earnings_test.go` (next to the OOS test at :254-289).

**Interfaces:**
- Produces `universe.LoadFrozen(name string) (*Universe, error)`.
- Produces `(*Universe).Frozen() string`.
- Produces `backtest.OOSUniverse = "oos-h1-63"`.

- [ ] **Step 1: write the failing tests.**

```go
func TestOOSUniverseIsFrozenByteForByte(t *testing.T) {
	// SHA-256 of internal/universe/data/<key>.csv at the OOS-H1-63 registration
	// (identical from 3b38520 through 06cab72).
	want := map[string]string{
		"sp500":   "98402233cac4e488f57e98226a24a2e89005e753ca741835a0b0c3cb5c6ef2cb",
		"nq100":   "804a5678ccb114935af510c0214f977db8e7d5c32f288e43fb38201d47cde95c",
		"eu50":    "ff4888cda2dc3d77c452c2a915b569ee5ba5132c11de4239799669fa70f93b1e",
		"asia100": "91e4c827b9ff468064f8fec1a1544a1f2bebfab098cd20a8b6bfe7cb30de9261",
	}
	for key, sum := range want {
		b, err := frozenFS.ReadFile("data/frozen/oos-h1-63/" + key + ".csv")
		if err != nil {
			t.Fatal(err)
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(b)); got != sum {
			t.Errorf("frozen %s.csv sha256 %s, want %s: OOS-H1-63's registered universe must not change", key, got, sum)
		}
	}
	u, err := LoadFrozen("oos-h1-63")
	if err != nil || u.Frozen() != "oos-h1-63" || len(u.Constituents("nq100")) != 56 {
		t.Fatalf("LoadFrozen = %v, %v", u, err)
	}
}
```

  Also, in the backtest package, `TestEvaluateOOSRefusesTheLiveSamples`:
  - `Run` with `EvaluateOOS: true` and `universe.Load()` returns an error naming `OOSUniverse`;
  - with `universe.LoadFrozen(OOSUniverse)` it gets past that check, to the existing maturity refusal. Reuse the fixture of the test at `earnings_test.go:254-289`.
- [ ] **Step 2:** run both. Expected: undefined `frozenFS`, `LoadFrozen` and `OOSUniverse`.
- [ ] **Step 3: implement.**
  - `//go:embed data/frozen/oos-h1-63/*.csv` into `var frozenFS embed.FS`.
  - Refactor `Load()` into `load(fsys fs.FS, dir, frozen string)`:
    - `Load()` is `load(dataFS, "data", "")`;
    - `LoadFrozen(name)` is `load(frozenFS, "data/frozen/"+name, name)`, and errors on an unknown name;
    - add a `frozen string` field and its `Frozen()` getter.
  - `cmd/cfr/backtest.go:51`: when the `--evaluate-oos` flag is set, load `universe.LoadFrozen(backtest.OOSUniverse)`.
  - In `Run`, before the dates are computed:

```go
	if cfg.EvaluateOOS && uni.Frozen() != OOSUniverse {
		return nil, fmt.Errorf("OOS-H1-63 is evaluated on the universe frozen at its registration (universe.LoadFrozen(%q)), not the live samples", OOSUniverse)
	}
```

- [ ] **Step 4:** `go test ./internal/universe ./internal/backtest` passes.
- [ ] **Step 5: docs.** Append to the OOS section's "Universe frozen at registration" bullet: "Enforced in code since <this commit>: `--evaluate-oos` reads byte-identical embedded copies of the four index files (`internal/universe/data/frozen/oos-h1-63/`, SHA-256 pinned in `TestOOSUniverseIsFrozenByteForByte`), so later edits to the live samples, such as v8's nq100 replacement, cannot reach it. This changes no statistic and no sample."
- [ ] **Step 6: commit.** `Freeze OOS-H1-63's registered universe in code before the samples change`.

### Task 7: replace nq100's 11 non-members (Phase 1.5, R8; needs Task 6)

**Files:**
- Modify:
  - `internal/universe/data/nq100.csv`;
  - `internal/universe/data/history/README.md:64-66`;
  - `docs/workflow/universe.md`;
  - the "35 of 56" sites: `CLAUDE.md`, `internal/backtest/scoped.go:13`, `internal/backtest/book.go:80`, `internal/orchestrator/orchestrator.go:1895`, `internal/model/types.go:116`, `internal/orchestrator/prescreen_test.go:914`, `internal/backtest/book_test.go:129`, `docs/workflow/independent-research.md:302`.
- Modify `internal/orchestrator/prescreen_test.go:928-937`, the OKTA fixture.
- Leave dated results text as it was, e.g. `docs/workflow/backtest.md:729` if it sits in a dated section: it described the sample of its day.

**Interfaces:** None.

- [ ] **Step 1: write the failing test** in `internal/universe/history_test.go`:

```go
// Every row of the nq100 sample is a Nasdaq-100 member on the history's last
// date — the sample may be partial, never wrong.
func TestNQ100SampleHoldsOnlyCurrentMembers(t *testing.T) {
	h, err := LoadHistory("nq100")
	if err != nil {
		t.Fatal(err)
	}
	u, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	last := h.LastDate() // the newest change row; add this accessor if History lacks one
	for _, c := range u.Constituents("nq100") {
		if !h.MemberOn(c.Ticker, last) { // or the History API's equivalent
			t.Errorf("%s is in nq100.csv but not a Nasdaq-100 member on %s", c.Ticker, last.Format("2006-01-02"))
		}
	}
}
```

  Use the existing `History` API. If it has no last-date or membership query, add the smallest accessor `TestSP500HistoryContainsTodaysSample` would also be written with.
- [ ] **Step 2:** `go test ./internal/universe -run TestNQ100SampleHoldsOnlyCurrentMembers -v`. Expected: it fails, listing the 11.
- [ ] **Step 3: edit `nq100.csv`.**
  - Delete the 11 rows: BIIB, CHTR, ILMN, JD, NET, OKTA, ON, SNOW, TEAM, TTD, ZS.
  - Append the replacements:

```
ADI,Analog Devices Inc.,NASDAQ,US,Information Technology
ADSK,Autodesk Inc.,NASDAQ,US,Information Technology
MCHP,Microchip Technology Inc.,NASDAQ,US,Information Technology
WDAY,Workday Inc.,NASDAQ,US,Information Technology
NXPI,NXP Semiconductors N.V.,NASDAQ,NL,Information Technology
FTNT,Fortinet Inc.,NASDAQ,US,Information Technology
GEHC,GE HealthCare Technologies Inc.,NASDAQ,US,Health Care
ADP,Automatic Data Processing Inc.,NASDAQ,US,Industrials
CMCSA,Comcast Corporation,NASDAQ,US,Communication Services
WBD,Warner Bros. Discovery Inc.,NASDAQ,US,Communication Services
MAR,Marriott International Inc.,NASDAQ,US,Consumer Discretionary
```

  - Line 1 becomes `# source: Nasdaq-100 representative constituents as of 2026-06-01; on 2026-10-07 the 11 non-members were replaced by current members (history/nq100_changes.csv: same sector, longest tenure, not in sp500.csv)`.
- [ ] **Step 4: fix the fixture and the counts.**
  - `prescreen_test.go:928-937`: OKTA becomes ADSK, nq100-only after this change, in the skip guard and in the shortlist.
  - Recompute the overlap:

```sh
comm -12 <(grep -v '^#' internal/universe/data/sp500.csv | cut -d, -f1 | sort) <(grep -v '^#' internal/universe/data/nq100.csv | cut -d, -f1 | sort) | grep -vc '^ticker$'
```

    Expected: 33. Then edit every present-tense "35 of … 56" to "33 of … 56".
  - `history/README.md:64-66`: the sample now holds only current members (rule above).
  - `docs/workflow/universe.md`: the same, one line.
- [ ] **Step 5:** `go test ./...` passes. These read the CSVs too: `adr_test.go:52`, `newsfilter_universe_test.go:28`, `TestOOSUniverseIsFrozenByteForByte`, which must still pass because the frozen copy is untouched.
- [ ] **Step 6 (only if Yahoo answers 200):** `CFR_UNIVERSE_CHECK=1 go test ./internal/universe -run TestEveryConstituentPrices -v`. Record the result in the commit message; if Yahoo throttles, record "not run".
- [ ] **Step 7: commit.** `nq100: replace the 11 non-members with current Nasdaq-100 members`.

### Task 8: share-class siblings for the news tag check, and two aliases (Phase 1.4, R6)

**Files:**
- Create: `internal/marketdata/data/share_siblings.csv`, `internal/marketdata/siblings.go`.
- Modify: `internal/marketdata/newsfilter.go:279-289`, `internal/universe/data/aliases.csv`.
- Test: `internal/marketdata/newsfilter_test.go`, `internal/marketdata/siblings_test.go`, `internal/marketdata/yahoonews_test.go`.

**Interfaces:** Produces `marketdata.Siblings(ticker string) []string`.

- [ ] **Step 1: write the failing tests.**

```go
func TestIsSubjectRelevantAcceptsAShareClassSiblingTag(t *testing.T) {
	ctx := context.Background()
	tags := []string{"VOW.DE", "BMW.DE", "MBG.DE", "PAH3.DE"}
	if !isSubjectRelevant(ctx, tags, "Volkswagen cuts its outlook as China sales slide", "", "VOW3.DE") {
		t.Error("a Volkswagen story tagged to the ordinary line was not counted for the preference line")
	}
	// The sibling widens only the tag check: a four-tag story that never names
	// the company still does not count.
	if isSubjectRelevant(ctx, tags, "German carmakers slip on tariff fears", "", "VOW3.DE") {
		t.Error("a sector story was counted on a sibling tag alone")
	}
	if Siblings("VOW.DE") != nil || Siblings("GOOG") != nil {
		t.Error("siblings are one-directional: only the listed listing gains them")
	}
}

// Every listing in the sibling table is a universe constituent, and no row maps
// a listing to itself or repeats.
func TestShareSiblingsMatchTheUniverse(t *testing.T) { /* the loop of adr_test.go:52-75, over shareSiblings' keys */ }
```

  Also add a provider-level test in `yahoonews_test.go`, `TestYahooNewsCountsAStoryTaggedToAShareClassSibling`:
  - A fake search server answers a dotted local-symbol query with `{"news":[]}`, as `fakeYahoo` in `canary_test.go` does.
  - It answers the name query `Volkswagen` with two items tagged only `VOW.DE` whose titles name Volkswagen.
  - `NewYahooNewsProvider().Fetch(ctx, "news", "VOW3.DE")` returns facts marked related, and no "not one of them is about this company" warning.
- [ ] **Step 2:** `go test ./internal/marketdata -run 'Sibling' -v`. Expected: undefined `Siblings`.
- [ ] **Step 3: implement.**

  `internal/marketdata/data/share_siblings.csv`:

```
ticker,sibling,note
VOW3.DE,VOW.DE,Volkswagen preference vs ordinary: Yahoo tagged 19 of 20 Volkswagen stories VOW.DE and none VOW3.DE (probe 2026-10-07)
3988.HK,601988.SS,Bank of China H vs A shares: 9 of 20 tagged 601988.SS against 1 tagged 3988.HK (probe 2026-10-07)
2318.HK,601318.SS,Ping An Insurance H vs A shares: 15 of 20 tagged 601318.SS (probe 2026-10-07)
2318.HK,82318.HK,Ping An Insurance RMB counter: 9 of 20 tagged 82318.HK (probe 2026-10-07)
GOOGL,GOOG,Alphabet class A vs class C: Yahoo tagged all 20 stories for the GOOGL query GOOG (probe 2026-10-07)
```

  `internal/marketdata/siblings.go`, following `adr.go`'s embed and CSV pattern (`adr.go:9-10,64-90`):

```go
//go:embed data/share_siblings.csv
var shareSiblingsRaw string

// shareSiblings maps a listing to the other share classes or lines of the same
// company that a feed may tag instead (VOW3.DE's stories are tagged VOW.DE).
// One-directional, and read only by isSubjectRelevant's tag check: a sibling
// tag still needs the story to be about the company.
var shareSiblings = loadShareSiblings()

func loadShareSiblings() map[string][]string {
	out := map[string][]string{}
	records, err := csv.NewReader(strings.NewReader(shareSiblingsRaw)).ReadAll()
	if err != nil {
		return out
	}
	for i, rec := range records {
		if i == 0 || len(rec) < 2 {
			continue
		}
		t, s := strings.ToUpper(strings.TrimSpace(rec[0])), strings.ToUpper(strings.TrimSpace(rec[1]))
		if t != "" && s != "" && t != s {
			out[t] = append(out[t], s)
		}
	}
	return out
}

// Siblings returns a listing's share-class siblings, or nil.
func Siblings(ticker string) []string {
	return shareSiblings[strings.ToUpper(strings.TrimSpace(ticker))]
}
```

  In `isSubjectRelevant`, the tag check becomes:

```go
	if !relatesTo(symbols, ticker) && !relatesTo(symbols, root) && !relatesTo(symbols, adr) && !relatesToAny(symbols, Siblings(ticker)) {
		return false
	}
```

  with:

```go
// relatesToAny is relatesTo over several candidate tags.
func relatesToAny(related, tickers []string) bool {
	for _, t := range tickers {
		if relatesTo(related, t) {
			return true
		}
	}
	return false
}
```

  Extend the doc comment on `isSubjectRelevant` with one sentence on siblings. Append `2318.HK,Ping An` and `BMW.DE,BMW` to `aliases.csv`.
- [ ] **Step 4:** `go test ./internal/marketdata ./internal/universe ./internal/orchestrator` passes, including `TestAliasTickersExistInTheUniverse` and the relevance audit test (`news_relevance_audit_test.go:90`).
- [ ] **Step 5: docs.** One clause in CLAUDE.md's `marketdata/` map entry: "share_siblings.csv widens only newsfilter's tag check".
- [ ] **Step 6: commit.** `News: share-class siblings for the tag check (VOW3.DE, 3988.HK, 2318.HK, GOOGL); BMW and Ping An aliases`.

## Phase 2 (owner: keep)

**Phase 2 rulings.**
- **N1 MAX:** the maximum simple daily return `C[i]/C[i-1]−1` over the last 21 sessions of `cut`. The signal is `−MAX`. NaN if there are fewer than 22 bars or a non-positive close.
- **N2 IVOL:** the sample σ (n−2 degrees of freedom) of the OLS residuals over the last 63 date-aligned log-return pairs from `quant.AlignedReturns(cut, bcut)`, the member's own benchmark, as β uses. The signal is `−IVOL`. NaN if there is no benchmark, fewer than 63 pairs, or zero benchmark variance. Disclosed: `lowvol` (total σ over 63 sessions) has already been printed at IC21 in `pit-9y.json`.
- **N3 FIP:** over the 231 daily close changes inside mom12-1's window (bars t−252..t−21), `ID = sign(mom12_1)·(%down − %up)`. Flat days count only in the denominator. The signal is `−mom12_1·ID`, the continuous analogue of Da–Gurun–Warachka's PRET×ID sort.
- **Statistic:** the per-(date, index) Spearman of the signal against `BX[21]` (`cell.bic[sig][h21]`), averaged per date across the two indices (`dateSeries`). The test is `applyScopedBar(…, nwLags(21)=5, +1)` with Scope US: t > +2.5 and both halves positive, split at `midDate`.
- **Gating and families:**
  - The N tests have status "run" only on `--universe pit` with US indices only. Elsewhere they are "comparison", with p NaN.
  - They stay out of `tests_run` and out of the in-report Holm family. Their own `anomalies.register_family` is Holm over a sourced table of the 18 prior p-values plus these 3, so m = 21.
  - The prior p-values are taken from the 10-year Holm table (`lab-horizon.md:103-119`) and `pit-9y.json`, with PIT-E1 = binomialSignP(3,10) = 0.945.

### Task 1: register N1–N3 (controller, before any N code)
- [ ] Append a section `## Pre-registered new anomalies (v8, registered 2026-10-08, before any code)` to `docs/workflow/backtest.md`, after the PIT section. It gives:
  - the hypotheses and their citations: Bali, Cakici & Whitelaw 2011 (JFE); Ang, Hodrick, Xing & Zhang 2006 (JF); Da, Gurun & Warachka 2014 (RFS);
  - the exact formulas above and the `lowvol` disclosure;
  - the statistic, the window 2017-09-29..2026-09-25, and the command `cfr backtest --universe pit --indices sp500,nq100 --years 9 --json`;
  - the one-run rule, the bar, and the 18-row prior p table;
  - m = 21: Holm at 5% needs p < 0.00238 (t > 2.82);
  - the decisions: any pass → write-up with its register Holm p, an OOS registration on weeks after 2026-09-25, an inactive column, nothing live; all fail → the memo moves to option 2 and live runs are suspended;
  - the honest prior.
- [ ] Commit: `v8: pre-register N1 MAX, N2 IVOL, N3 FIP on the PIT lab (m = 21)`.

### Task 9: the N1–N3 signals (TDD, synthetic data only)
**Files:**
- Create: `internal/backtest/anomalies.go`, `internal/backtest/anomalies_test.go`.
- Modify: `internal/backtest/panel.go` (constants before `NumSignals`, names `max21`, `ivol63`, `fip`; three lines in `observe` after `SigIntraday21`), `backtest_test.go` (look-ahead finiteness).

**Produces:** `SigMax21`, `SigIVol63`, `SigFIP`; `maxDailyReturn(bars []quant.Bar, n int) float64`, `residualVol(s, bench *quant.Series, n int) float64`, `fipSignal(bars []quant.Bar, mom float64) float64`.

- [ ] **Failing tests on hand-built bars:**
  - **MAX:** a known spike, and flat closes give 0.
  - **IVOL:** a stock built as `r = 0.0002 + 1.3·r_b + e`, with known residuals, matches the σ of `e` with n−2 dof to 1e-12. A nil, short or flat benchmark gives NaN.
  - **FIP:** a hand count of up, down and flat days in the window, with the sign flip for negative momentum. Fewer than 253 bars or NaN mom gives NaN.
  - **Look-ahead:** `TestSignalsIgnoreBarsAfterDate` additionally asserts N1–N3 are finite for every row at the probe date.
- [ ] **Implement.** No helper may reslice past `len`. Add `rec.Sig[SigMax21] = -maxDailyReturn(cut.Bars, 21)`, `rec.Sig[SigIVol63] = -residualVol(cut, bcut, 63)` and `rec.Sig[SigFIP] = fipSignal(cut.Bars, mx.Mom12_1)`.
- [ ] **Fixtures:** where an RNG fixture filled every `r.Sig`, keep the existing stream: draw only for the signals before `SigMax21`, then set the new ones separately.
- [ ] **Checks:** `go test ./internal/backtest`. Real-data backtests are forbidden from here until Task 12.
- [ ] **Commit.**

### Task 10: anomaly tests and the m = 21 register family (TDD)
**Files:** `internal/backtest/anomalies.go`, `run.go` (`Result.Anomalies` with JSON key `anomalies`, the `Analyze` call, gating in `Run`, a text section), `holm.go` (`sidedness`: PIT-E1 is the binomial sign test).

**Produces:**
- `AnomalyReport{Tests []TestResult; RegisterFamily MultipleTesting}`;
- `anomalyTests(cells []cell, mid string) []TestResult`;
- `priorRegister []TestResult`, with 18 rows, each with ID, P and a source in Note;
- `registerFamily(tests []TestResult) MultipleTesting`, which reuses `buildHolm` over pointers to copies of the prior rows plus `&tests[i]`.

- [ ] **Failing tests:**
  - The bar on synthetic cells: a high t with one negative half fails; a high t with both halves positive passes; there are 5 lags.
  - `priorRegister` has 18 unique IDs, each p in (0,1].
  - The register family has `FamilySize == 21`; a NaN-p N test counts as 1; a planted p of 1e-4 gets a Holm p of 21e-4.
  - On a sample-universe result the N tests are "comparison" and `tests_run` is unchanged. The existing 15/14 pins still pass.
- [ ] **Implement and commit.**

### Task 12: full checks and the one run (controller)
- [ ] **Pre-flight:**
  - all tasks are merged; `gofmt -l .`, `go vet ./...` and `go test ./...` are clean;
  - the Yahoo check returns 200;
  - `go build -o $SCRATCH/cfr ./cmd/cfr`.
- [ ] **The run**, from the repo root:

```sh
env -u ALPHAVANTAGE_API_KEY -u FRED_API_KEY -u DEEPSEEK_API_KEY -u CFR_API_KEY \
  $SCRATCH/cfr backtest --universe pit --indices sp500,nq100 --years 9 --json \
  > docs/research/2026-10-08-evidence/anomalies-pit-9y.json 2> docs/research/2026-10-08-evidence/anomalies-pit-9y.log
```

  A crash before any statistic may be repeated; nothing else may.
- [ ] **Checks with `jq`:**
  - the window and the universe;
  - `unavailable`;
  - `anomalies.register_family.family_size == 21`;
  - the three N tests' status is `run`;
  - the 11 in-report figures, diffed against `pit-9y.json`.

### Task 13: the write-up
- [ ] `docs/research/2026-10-08-lab-anomalies.md` gives:
  - the run checks;
  - an N1–N3 table: mean IC21, t (5), n, halves, p and register Holm p;
  - the 21-row register;
  - the differences in the recomputed figures, with their causes;
  - the E3 disclosure.
  - Every number is cited as `json:<line>`.
- [ ] `backtest.md` gets a results subsection and the register count of 21.
- [ ] Then Task 14 (all fail) or Tasks 15–16 (any pass).

### Tasks 15–16: Phase 3 (only if a test passes)
- **Task 15.** Register OOS-<S> in `backtest.md`:
  - S's statistic on weeks after 2026-09-25, on a rebuilt PIT history;
  - evaluated in the same single `--evaluate-oos` look as OOS-H1-63 (~2027-12);
  - the same bar.
- **Task 16.** Promote S's helper to `internal/quant`, so the lab and live share one function. Add an inactive `PrescreenRow` field persisted to `prescreen.json`. IVOL needs the `benchmark` closure in `runPrescreen`. Tests prove the following are byte-identical with the field set to extreme values:
  - `Table()`;
  - `ScorePrescreen`;
  - `classifySetups`;
  - `meritComposite`.
- Nothing in ranking, selection or exits changes.

### Task 11: a missing benchmark stops the run before any statistic (Review Focus 1)

**Files:**
- Modify: `internal/backtest/run.go:274-278`.
- Test: `internal/backtest/earnings_test.go`, next to `fakeLoader` at :193.

**Interfaces:** None. It changes `Run`'s error behaviour only.

- [ ] **Step 1: write the failing test.**

```go
// failingBench is fakeLoader except that one symbol, a benchmark, fails the
// way Yahoo did on 2026-10-07 (HTTP 429 from every endpoint).
type failingBench struct{ symbol string }

func (f failingBench) HistoryRange(ctx context.Context, symbol, rng string, age time.Duration) (*quant.Series, error) {
	if symbol == f.symbol {
		return nil, errors.New("yahoo: HTTP 429")
	}
	return fakeLoader{}.HistoryRange(ctx, symbol, rng, age)
}

func TestRunStopsWhenABenchmarkIsUnavailable(t *testing.T) {
	uni, err := universe.Load()
	if err != nil {
		t.Fatal(err)
	}
	bench := universe.BenchmarkFor("sp500", "AAPL")
	res, err := Run(context.Background(), failingBench{symbol: bench}, uni,
		Config{Indices: []string{"sp500"}, Years: 1, Now: day("2021-12-31")})
	if err == nil || !strings.Contains(err.Error(), bench) {
		t.Fatalf("Run = %v, %v; want an error naming %s and no report", res, err, bench)
	}
	if res != nil {
		t.Error("a report was produced without its benchmark")
	}
}
```

  Use whatever `Config` an existing `Run` test in the package uses to reach the price-loading stage, and copy it.
- [ ] **Step 2:** `go test ./internal/backtest -run TestRunStopsWhenABenchmarkIsUnavailable -v`. Expected: FAIL, because a report comes back with NaN β-adjusted figures.
- [ ] **Step 3: implement**, right after the benchmark fetch loop:

```go
	// Every registered statistic is beta-adjusted against these series. A
	// benchmark that failed to load turns each into NaN while a report still
	// prints, and a printed report spends a registration; stop instead (the
	// one-run rule lets a crash before any statistic be repeated).
	for b, s := range data.Bench {
		if s == nil || len(s.Bars) == 0 {
			return nil, fmt.Errorf("benchmark %s is unavailable, so nothing is computed: %s", b, strings.Join(unavailable, "; "))
		}
	}
```

- [ ] **Step 4:** `go test ./internal/backtest` passes. If an existing test relied on a silently missing benchmark, give that test a benchmark rather than weakening the guard.
- [ ] **Step 5: docs.** One bullet in `docs/workflow/backtest.md`'s method section: a missing benchmark is fatal.
- [ ] **Step 6: commit.** `Lab: a missing benchmark stops the run instead of printing NaN beta-adjusted statistics`.

## Phase 4.1: if all three fail (the registered all-fail decision)

### Task 14: Cut, applied

**Files:**
- Modify:
  - `docs/research/2026-10-01-keep-cut-stop.md`;
  - `CLAUDE.md`: the lab paragraph; the sentence "Live runs are measurement only, at a cadence the owner sets; the keep/cut/stop memo is docs/research/2026-10-01-keep-cut-stop.md"; the `cfr run` line under Commands;
  - `internal/tui/results.go:99` (`screenNoEdgeLine`) and `internal/tui/results_test.go:97`.
- Local, gitignored: `.claude/settings.local.json`, via the update-config skill.

**Interfaces:** Consumes Task 13's numbers.

- [ ] **Step 1: TUI, test first.** In `results_test.go:97`, add the substring assertion `"MAX, IVOL or FIP"`, then run `go test ./internal/tui -run NoEdge -v`. Expected: FAIL. Then change the line's ending to:

```go
const screenNoEdgeLine = "  No signal tested has shown an edge net of cost: not the pre-screen\n  that ranked these ideas, held 15, 21 or 63 sessions, not 12-1\n  momentum alone, not the model stages, not post-earnings drift. A\n  survivorship-free US replay (2017-2026) confirms it for the screen\n  and finds none in MAX, IVOL or FIP either."
```

  Then update the comment above it with the v8 sentence. `go test ./internal/tui` passes.
- [ ] **Step 2: memo.** Append `## Outcome (<run date>)`: "N1–N3 all fail (N1 t …, N2 t …, N3 t …; smallest register Holm p …, m = 21). Option 2 is in force from <date>: live runs are suspended by owner decision. `cfr canary`, `cfr backtest` and `cfr scoreboard` remain supported. OOS-H1-63 is the only open question; its single evaluation is `cfr backtest --evaluate-oos` once 52 held-out weeks have matured (~2027-12)." Every number comes from Task 13.
- [ ] **Step 3: CLAUDE.md.**
  - The quoted sentence becomes: "Live runs are suspended by owner decision (<date>: the keep/cut/stop memo's option 2, after N1–N3 failed). `cfr canary`, `cfr backtest` and `cfr scoreboard` stay supported. The memo is docs/research/2026-10-01-keep-cut-stop.md".
  - The lab paragraph gains one sentence: "v8 (<date>): three published price-only anomalies the composite lacks — N1 MAX, N2 IVOL, N3 FIP — fail on the PIT lab at 21 sessions (t …); the register holds 21 tests, none passed".
  - The `cfr run` command line gains `# suspended by owner decision, <date>`.
- [ ] **Step 4: local config (controller).** Using the update-config skill on `.claude/settings.local.json`:
  - remove the allow entry `Bash(cfr run)`;
  - add `ask` entries for `Bash(cfr run:*)`, `Bash(./cfr run:*)` and `Bash(go run ./cmd/cfr run:*)`.
  - Check that the file parses as JSON and that `git status` shows nothing for it, since it is gitignored.
  - `~/.claude/settings.json:187` (the auto-mode "routine … go build/test/run" line) is global, not local. Leave it and name it in the final report.
- [ ] **Step 5: verify the kept tools.**
  - `go test ./...`.
  - `go run ./cmd/cfr scoreboard`: no model calls; output shown.
  - The Task 3R canary output already covers `cfr canary`.
- [ ] **Step 6: commit.** `v8: N1–N3 fail; live runs suspended by owner decision (option 2)`.

## Wrap-up

### Task 17: final review, merge, memory

- [ ] **Whole-branch review.** A code-review subagent on `opus` reviews `main..v8`, against this plan and the spec. Fix its findings with superpowers:receiving-code-review. Run each fix's own tests, then `go test ./...`.
- [ ] **Verify** with superpowers:verification-before-completion: `gofmt -l .` is empty, and `go vet ./...` and `go test ./...` are clean, with the output shown.
- [ ] **Merge:** `git switch main && git merge --ff-only v8`. **Do not push.**
- [ ] **Memory:**
  - Update `cfr-v8-plan-pending`: v8 executed, the outcome, what remains open (OOS-H1-63 ~2027-12; the pre-open canary, if still pending).
  - Append the N1–N3 result and the register's 21 tests to `cfr-edge-evidence-2026-09`.
  - Keep the `MEMORY.md` lines in sync.
- [ ] **Status.** Fill in the status table at the end of `docs/plans/2026-10-07-v8-implementation.md` and commit it: `Plan v8: status`.

## Verification (end to end)

- **Checks:** `gofmt -l .` is empty, and `go vet ./...` and `go test ./...` are clean, with the output shown after every merge and at the end.
- **The one run's artifact must show:**
  - `first_date` 2017-09-29 and `last_date` 2026-09-25;
  - `universe` `pit`;
  - no `shortfall`;
  - `anomalies.register_family.family_size` 21;
  - an `unavailable` list that is either empty or exactly what Task 5 predicts;
  - no benchmark missing. Task 11 makes that fatal, so a printed report proves it.
- **The 11 in-report figures** are compared with `docs/research/2026-10-07-evidence/pit-9y.json`, and every difference is recorded with its cause (Alpaca refetch, DOW, sectors from Task 7).
- **Traceability:** every number in the write-up, memo and CLAUDE.md traces to `json:<line>`.
- **Live code paths changed in Phase 1** (post-mortem gate, news siblings, nq100 rows) are covered by their unit tests and by `go test ./internal/orchestrator`. Its integration tests reroute Yahoo through `httptest`, so they touch no network. No live or smoke `cfr run` is made: it is not needed, and Yahoo is throttling this host.
- **The canary:** a post-close run is saved, and a pre-open run is saved or recorded as the owner's to run.

## Status

| Step | State |
|---|---|
| 0 setup | done: 427ccf2 |
| 1 registration | done: 4032e95 (2026-10-08, before any N code) |
| 2 memo | done: b5968df |
| 3 / 3R canary guard, outside-session runs | guard b5bc959 (a backstop: alpaca.go already errors on empty); pre-open run 09:53Z abstains as designed, 9/9; post-close run pending |
| 4 post-mortem version | done: f46ba55 (v3) |
| 5 DOW | done: b4a417f — not a rename; Alpaca drops a reused ticker from a large batch reaching the old holder's years; retry from interval start |
| 6 OOS universe frozen | done: 333fbdd |
| 7 nq100 replaced | done: 513ddde (overlap 33 of 56) |
| 8 share-class siblings | done: 334b41d |
| 9 N1–N3 signals | done: 3f2ba7f |
| 10 anomaly tests, register family | done: eaea574; gate tightened in 93a3c4b |
| 11 missing benchmark fatal | done: 19449dc |
| 12 the one run | done: 2026-10-08 10:11Z from 513ddde, 0 unavailable — N1 t 1.66, N2 t 2.13, N3 t 0.45; all fail |
| 13 write-up | done: d169876 |
| 14 Cut, or 15–16 Phase 3 | Cut done: 68cb0af; Phase 3 not triggered |
| 17 review, merge, memory | review: no blocking defect, fixes 93a3c4b; merged to main, not pushed |
