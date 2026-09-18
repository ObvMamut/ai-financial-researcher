# Chief Engine Selection and Research Capacity — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make thesis-mode research complete within its declared byte budgets and route Chief synthesis through a configurable engine, so the 9-of-12 company failure in `runs/2026-09-15T17-00-30` cannot recur for formatting, budgeting, or engine-availability reasons.

**Architecture:** Two increments over the existing thesis pipeline. **A** introduces a `chief_engine` selector with dedicated `[chief_api]` credentials and one shared Chief execution helper, replacing four hard-coded `model.CLIClaude` routing sites. **B** redefines the response limit as *compact JSON payload bytes* instead of raw stdout bytes, gives narrative compaction a *measured* allowance instead of a fixed 400-character hint, and rebuilds prompt assembly from named self-measuring sections with a global Chief-board budget.

**Tech Stack:** Go, standard library only for new code (`encoding/json`, `crypto/sha256`). Existing `charmbracelet` TUI. Tests use inline `httptest` servers in the `thesis_test.go` style — **not** `testdata/fakebin`, which only knows legacy personas.

**Spec:** `docs/plans/2026-09-15-deepseek-chief-and-research-reliability.md`, WP1–WP6 plus the provenance bullets of WP8. Behavioural contracts live in `docs/workflow/thesis-research.md` §"Reliability contracts and diagnostics" and `docs/workflow/output-schema.md`; those are the source of truth and move with the code.

---

## Context

`docs/plans/2026-09-15-thesis-recovery.md` — the document this plan was asked to build on — records fixes verified against **twelve synthetic dossiers**. Eleven passed without repair; one took a compaction. On that evidence the record reads as closed.

The live run performed later the same day, `runs/2026-09-15T17-00-30`, refutes it. Of twelve shortlisted companies, **nine failed**:

| Companies | Blocking event |
| --- | --- |
| LLY, BAC, NOKIA.HE, TTD, REGN, 9988.HK | Complete response still exceeded 20,480 bytes after compaction |
| SNOW, OKTA | Revision prompt exceeded 98,304 bytes — call never dispatched |
| ORCL | Final challenger prompt exceeded 98,304 bytes — review never dispatched |

Zero plans shipped. All 55 HTTP completions with a recorded finish reason reported `stop` — nothing was truncated by the provider. These are **our own budgeting failures**, and the synthetic fixtures could not have caught them because they were sized to pass.

Two root causes are verified in the code (Diagnosis below): the response budget is measured against raw stdout rather than the JSON payload it is meant to bound, and the compactor is told to aim for a fixed character count with no knowledge of how much room remains. A third: the Chief prompt already sat at 158,528 of 196,608 bytes with only *three* usable dossiers — a full twelve-company board cannot fit under the current per-company allocation.

Separately, the primary Chief call failed on disabled Claude subscription access. The settled decision is `chief_engine=api` with DeepSeek primary and Claude retained as a one-setting switch back.

**Intended outcome:** every company that reaches model research produces a readable dossier and a substantive review, within unchanged budgets and unchanged provider caps, with the engine that produced each answer recorded. Trade count is explicitly *not* an acceptance criterion. This plan stops at a green offline gate; live acceptance stays an operator decision.

---

## Global Constraints

Copied from the spec and `CLAUDE.md`. Every task's requirements implicitly include this section.

- **No new dependency, provider, credential, or paid service.** Go stdlib plus the existing `charmbracelet` TUI.
- **Provider output caps are never raised to make anything pass.** A cap is a ceiling, not a generation target.
- **The cheap-research engine stays independent of the Chief.** Selecting `api` for both roles must never put the Chief on the cheap research model, and must never source the Chief key from `[api]`, `[local]`, or `DEEPSEEK_API_KEY`.
- **Config precedence is fixed:** defaults → `~/.config/cfr/config.toml` → `./cfr.toml` → `CFR_*` env → flags.
- **Credentials never appear** in errors, logs, metadata, prompt snapshots, or pair manifests. Every new key joins global redaction (`internal/config/config.go:306`).
- **Normalization is lossless.** `json.Compact` on the raw payload — never decode into a typed dossier and re-emit. Unknown fields, exact strings, numeric spellings, and quotation contents survive byte-identical.
- **One repair allowance per response, never chained.** A complete malformed payload may buy one schema repair; a complete schema-valid oversized dossier may buy one narrative compaction. One cannot buy the other. Local whitespace normalization consumes no model call.
- **Protected wire fields are byte-identical after compaction** — including unknown extensions, claims, exact quotations, numerical values, events, requests, uncertainties, entry conditions, monitoring, and status.
- **Historical artifacts stay readable and are never reinterpreted.** New fields are additive and versioned; an absent old field means *unrecorded*, never *measured zero*. Never recompute a past run's acceptance under a new size rule.
- **Failure states stay distinct:** transport ≠ parsing ≠ contract/capacity ≠ preparation-refused-before-dispatch. Transport and parsing must not claim to have failed when neither occurred.
- **All-failed behaviour is preserved:** no usable dossiers skips *every* Chief engine and still writes degraded decisions, per-company explanations, and metadata (headless exit 3).
- **Research failure is not thesis rejection.** A failed investigation stays a watchlist entry.
- **Writing targets stay diagnostics.** 12 claims, 400-char narratives, 2 × 300-char quotations are recorded in `writing_diagnostics` and never invalidate a complete dossier. The three-requests-per-round limit and the byte budgets remain hard.
- **Agent personas are runtime data.** Editing `agents/*.md` must not require recompiling.
- **Default byte budgets are unchanged by this plan:** triage 98,304/12,288 · researcher 98,304/20,480 · challenger 98,304/12,288 · chief 196,608/24,576 (UTF-8 bytes, input/response).

---

## Diagnosis — verified in the code, not inferred from the run log

**1. The response budget measures the wrong bytes.** `internal/orchestrator/thesis_budget.go:122-129` (`responseCapacity`) compares `len(r.Stdout)` — raw stdout including the ```json fence and every space the model emitted — against `profile.ResponseLimit`. LLY's round-2 *compaction result* was **20,493 raw bytes against a 20,480 limit, but 20,326 compact** — a compaction that had done its job and was thrown away over 155 bytes of interior whitespace and a 12-byte fence. (LLY's original response measured 20,883 compact and did need that compaction; what was lost was the recovery, not the need for one.) The same check then runs *again* on the compaction result (`thesis.go:114`, reached via `compactDossier` → `t.call`), so a compaction that genuinely fits the payload budget can still fail on formatting.

**2. The compactor is not told how much room it has.** `internal/orchestrator/thesis_compaction.go:71` builds a fixed prompt: *"Aim for under 400 characters each."* Nothing computes protected-field bytes, JSON structural overhead, or the remaining narrative allowance. With 12–17 claims and 23–29 passages, six dossiers had almost no narrative room left and the model was handed a target that could not help. There is no check that the compaction *input* (which embeds the entire original response) fits, and no refusal path when protected content alone already exceeds the budget — a doomed call is dispatched anyway.

**3. The Chief board allocates per-company, not globally.** `internal/orchestrator/thesis_budget.go:104` calls `promptDocumentsAt(v.Documents, 9000, …)` inside a per-company loop. Three usable dossiers already occupied 142,756 of 196,608 input bytes. Twelve companies at a fixed 9,000-character allocation cannot fit.

**4. Prompt components are reverse-engineered, not measured.** `internal/orchestrator/thesis_budget.go:187-215` (`promptComponents`) finds sections by searching for marker substrings in the already-assembled string and attributes the bytes between markers. A section without a marker is silently folded into its predecessor — which is why the audit could not attribute the SNOW/OKTA/ORCL overflow (+92, +4,773, +4,109 bytes) to any component.

**5. Engine type decides call semantics.** `internal/orchestrator/thesis.go:104-112` uses `cli == model.CLIClaude` to pick *both* the timeout/retry policy (synthesis vs analysis) *and* the stage label. Routing the Chief through `CLIApi` would silently demote it to research timeouts. Worse, `thesis.go:90-95` reads the output-token cap from `t.pool.api.MaxTokens` — the **cheap** engine's API config.

**6. Four hard-coded routing sites.** `orchestrator.go:1213` (legacy initial), `orchestrator.go:1305` (legacy corrective), `thesis.go:239` (thesis initial), `thesis.go:284` (thesis corrective). The fallback block is `thesis.go:252-275`; the corrective path deliberately has none. Metadata at `thesis.go:327` and `orchestrator.go:1476` records `SynthesisModel: cfg.Models[model.CLIClaude]`, which is simply wrong under an API Chief.

**7. The whole feature is uncommitted.** `git log -- 'internal/orchestrator/thesis*' 'agents/thesis-*' 'docs/workflow/thesis-research.md'` returns **zero commits across all branches**. 127 dirty paths (61 modified, +2958/−359; 66 untracked) sit on `fix/run-2026-09-01-audit`, whose HEAD `1159f7d` predates all of it by eleven days. The suite is nonetheless green today: 13 packages pass, 662 tests, ~31s; `go build` and `go vet` clean.

---

## What changes in `docs/plans/2026-09-15-thesis-recovery.md`

The record is accurate about what was built and honest that its fixtures are synthetic. Its defects are standing and one unsourced claim:

1. **It reads as closed.** The run two days later shows the same failure class at six times the scale. It needs a *Superseded* banner naming the live evidence and this plan.
2. **It does not say why the fixtures could not catch this.** Twelve dossiers sized to pass cannot detect a budget measured against the wrong bytes. Say so.
3. **One claim has no citable source.** "Earlier bounded document-reader probes exposed access limitations for Tesla (403), Regeneron (timeout) and thin Trade Desk extraction" appears nowhere in the run artifacts, test fixtures, or the `.data/independent-validation-2026-09-10` audit. What `runs/2026-09-13T11-30-16` *does* contain is an HTTP 403 on an investing.com TTD article and a long MarketScreener navigation capture for TTD. The sentence must be rewritten to cite what exists or marked as an unpersisted manual probe.
4. **Its acceptance criterion is not falsifiable.** Replace "assess readable dossiers, completed reviews … together" with: *every company that reaches model research produces a readable dossier and a substantive review*, with trade count explicitly excluded.
5. **The Claude failure is no longer incidental.** It is now a settled engine policy change; link it to the DeepSeek plan.

Task 1 carries the exact replacement text.

---

## Execution and model assignment

Dispatch with `superpowers:subagent-driven-development`: one implementer per task, a task review after each, a whole-branch review at the end. **Always name the model explicitly** — an omitted model inherits the session's.

| Role | Model | Applies to |
| --- | --- | --- |
| Implementer, mechanical transcription | **Haiku** | Tasks 1, 7 — the plan carries the exact text; the work is transcription plus a build/test run. |
| Implementer, standard | **Sonnet** | Tasks 2–6, 8–11, 13–15. Multi-file integration against a complete brief. |
| Implementer, design judgment | **Opus** | Task 12 only — a global allocation algorithm with no obviously correct shape. |
| Task reviewer | **Sonnet**, except **Opus** for Tasks 4, 8, 9, 12 | Scale to diff risk. |
| Scoped re-review of a fix diff | **Haiku** for one-file fixes, else **Sonnet** | Verdicts findings ADDRESSED / NOT ADDRESSED only. |
| Fix-loop rounds 4–5 | One tier above the stuck implementer | Per the skill. |
| Final whole-branch review | **Opus** | Once, over `merge-base..HEAD`. |

**Pre-flight (controller, Opus):** before dispatching Task 1, run the plan conflict scan the skill requires — one ledger row per task pair sharing a file (Tasks 8/9/11/13 all touch `thesis_budget.go` and `thesis_compaction.go`; Tasks 4/5/6 all touch `thesis.go` and `orchestrator.go`), one row per task on internal self-consistency. Rule on anything it surfaces before Task 1.

**Guardrail for every task:** the project's real `./cfr.toml` holds live credentials, and no `CFR_*` variable can clear a key already set in it. Tests must never resolve config from the repo root — they run in their own package directory or an explicit `t.TempDir()`. Never run `cfr run` from the repo root during development.

---

## File structure

| File | Responsibility | Task |
| --- | --- | --- |
| `internal/orchestrator/chief.go` *(new)* | Resolve the Chief engine once per run; execute a Chief call with an explicit purpose. | 4 |
| `internal/orchestrator/chief_test.go` *(new)* | Routing matrix: engine × pipeline × purpose. | 4, 5 |
| `internal/orchestrator/thesis_response.go` *(new)* | Payload extraction, `json.Compact` normalization, byte measurement. | 8 |
| `internal/orchestrator/thesis_sections.go` *(new)* | Named prompt sections with self-reported byte sizes; mandatory/optional allocation. | 11 |
| `internal/config/config.go` | `chief_engine`, `[chief_api]`, `[chief_fallback].enabled` tri-state, env, redaction. | 3 |
| `internal/orchestrator/thesis_budget.go` | `responseCapacity` → payload bytes; `chiefContext` → global board; drop `promptComponents`. | 8, 11, 12 |
| `internal/orchestrator/thesis_compaction.go` | Measured allowance, infeasibility refusal. | 9 |
| `internal/orchestrator/thesis.go` | `call` takes a `callTarget`; Chief sites use the helper; section-built prompts. | 4, 11, 13 |
| `internal/orchestrator/orchestrator.go` | Legacy Chief sites use the helper. | 4 |
| `internal/model/research_reliability.go` | `PromptProfile` gains measured section sizes + response measurement. | 8, 11, 14 |
| `internal/model/types.go` | `RunMeta` Chief provenance; `DomainStatus` preparation outcome. | 6, 14 |
| `agents/thesis-researcher.md` | Guidance toward material claims over repeated narrative. | 10 |
| `docs/workflow/thesis-research.md`, `docs/workflow/output-schema.md`, `cfr.toml.example`, `CLAUDE.md`, `README.md` | Contracts move with the code. | 7, 15 |

---

## Task 0: Baseline commit and branch

**Controller-executed. No subagent.** This is SDD Setup, and the user has approved it.

- [ ] **Step 1: Confirm the tree is green before freezing it**

```bash
go build ./... && go vet ./... && go test ./... -count=1
```
Expected: all 13 packages `ok`.

- [ ] **Step 2: Commit the whole working tree as one baseline**

```bash
git add -A
git commit -m "$(cat <<'EOF'
Land thesis research mode as a working baseline

The entire thesis pipeline, its four personas, its workflow spec and the
paired-evaluation subcommands have never been committed. This freezes them
as one reviewable baseline so later work has a diff to stand against.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
)"
```

- [ ] **Step 3: Branch for the new work**

```bash
git switch -c feat/chief-engine-and-capacity
git rev-parse HEAD   # record as MERGE_BASE in the ledger
```

- [ ] **Step 4: Copy this plan into the project's own convention**

The repo keeps its planning record in `docs/plans/`, and the SDD workspace is keyed on the plan's path. Copy this file to `docs/plans/2026-09-17-chief-engine-and-capacity-plan.md` and commit it on the new branch, so the plan travels with the work it describes.

- [ ] **Step 5: Record the ledger header**

`# SDD ledger — plan: docs/plans/2026-09-17-chief-engine-and-capacity-plan.md`, then `MERGE_BASE: <sha>`.

---

## Task 1: Correct the recovery record

**Model: Haiku.** Pure transcription — the replacement text is below.

**Files:** Modify `docs/plans/2026-09-15-thesis-recovery.md`.

**Interfaces:** Consumes nothing. Produces nothing code depends on.

- [ ] **Step 1: Insert a Superseded banner directly under the H1**

```markdown
> **Superseded.** The fixes below are real and still in the tree, but this
> record's standing is not. The live run `runs/2026-09-15T17-00-30`, executed
> the same evening, failed nine of twelve companies in the same failure class:
> six complete responses still exceeded 20,480 bytes after compaction, and
> three prompts exceeded 98,304 bytes and were never dispatched. Remaining work
> is tracked in
> [the DeepSeek Chief and reliability plan](2026-09-15-deepseek-chief-and-research-reliability.md).
```

- [ ] **Step 2: Add a new section immediately after "Implemented behavior"**

```markdown
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
```

- [ ] **Step 3: Replace the first paragraph of "Verification"**

Insert before the existing bullet list:

```markdown
The checks below all passed, and none of them could have caught the failures
above. The twelve synthetic dossiers were sized to pass the budget, so a budget
measured against raw stdout rather than the JSON payload it bounds looks
correct against them. Synthetic coverage establishes that the recovery path
executes; it establishes nothing about whether real dossiers fit.
```

- [ ] **Step 4: Replace the second paragraph of "Limits and next acceptance"**

The paragraph beginning "Earlier bounded document-reader probes exposed access limitations for Tesla (403)…" becomes:

```markdown
An earlier manual document-reader probe was described as exposing access
limitations for Tesla, Regeneron and The Trade Desk. That probe was not
persisted as a run artifact or a test fixture and cannot be cited here. What
the September 13 run does record is an HTTP 403 on an investing.com article for
TTD and a MarketScreener capture that was mostly navigation chrome. Updated
ranking tests replay captured URLs locally and do not establish live access to
any issuer. No new paid search service, model credential or recurring work was
introduced.
```

- [ ] **Step 5: Replace the final paragraph's last sentence**

"Assess readable dossiers, completed reviews, evidence visibility, conditional/actionable plans and failures together; trade count alone is not an acceptance criterion." becomes:

```markdown
The acceptance criterion is falsifiable and singular: every company that
reaches model research produces a readable dossier and a substantive review,
with no unrecovered capacity, transport, parsing or contract failure. Trade
count is not an acceptance criterion at any value, including zero.
```

- [ ] **Step 6: Verify and commit**

```bash
git diff --check && git add docs/plans/2026-09-15-thesis-recovery.md
git commit -m "Correct the standing of the September 15 recovery record"
```

---

## Task 2: Operational baseline fixtures from the September 15 run

**Model: Sonnet.**

**Files:**
- Create: `internal/scoreboard/testdata/research-sep15.json` (sanitized, follow `research-sep10.json` exactly)
- Create: `internal/orchestrator/testdata/sep15-capacity/manifest.json`
- Create: `internal/orchestrator/thesis_capacity_baseline_test.go`
- Read: `runs/2026-09-15T17-00-30/metadata.json`, `data/research.json`, `data/input-chief-analyst.json`

**Interfaces:**
- Produces: `sep15CapacityCase{Ticker string; RawBytes, CompactBytes, Limit int; Kind string}` loaded from the manifest by later tasks' tests.

- [ ] **Step 1: Write the failing baseline test**

Follow the existing `research-sep10.json` consumer exactly — find it with `grep -rn "research-sep10" internal/scoreboard` and reuse its loader and its diagnostics type rather than inventing a second one.

```go
func TestSeptember15BaselineReproducesTheFailureCounts(t *testing.T) {
	d := <the same loader the sep10 fixture test uses>(t, "testdata/research-sep15.json")
	for _, c := range []struct {
		name string
		got  int
		want int
	}{
		{"logical calls", d.LogicalCalls, 59},
		{"dispatched attempts", d.DispatchedAttempts, 56},
		{"compaction calls", d.CompactionCalls, 13},
		{"schema repairs", d.SchemaRepairs, 2},
		{"zero-attempt input failures", d.InputCapacityFailures, 3},
		{"failed company workflows", d.FailedCompanies, 9},
		{"usable dossiers", d.UsableDossiers, 3},
		{"completed reviews", d.CompletedReviews, 3},
	} {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}
}
```
If the sep10 diagnostics type lacks a field above, add it there (additively) rather than creating a parallel type.

- [ ] **Step 2: Run it and confirm it fails on the missing fixture**

`go test ./internal/scoreboard -run September15 -count=1` → FAIL, no such file.

- [ ] **Step 3: Build the sanitized fixture**

Copy only the operational fields the `research-sep10.json` fixture carries — counts, statuses, failure kinds, byte sizes, usage. **No prompts, no configuration, no credentials, no source text, no provider article bodies.** Record provenance in the file: `"provenance": "sanitized from runs/2026-09-15T17-00-30; operational fields only"`.

- [ ] **Step 4: Record the capacity manifest with exact Go byte counts**

For each of the six failed compactions and three input failures, record raw bytes and the byte count produced by **Go's `json.Compact`**, not by `jq`. The audit's compact column was a jq estimate; the plan's whole boundary case turns on 13 bytes, so the fixture's authority must be Go.

```json
{"provenance":"captured byte sizes from runs/2026-09-15T17-00-30; payloads not retained",
 "response_limit":20480,"input_limit":98304,
 "cases":[
  {"ticker":"LLY","kind":"response","raw_bytes":20493,"compact_bytes":20326},
  {"ticker":"BAC","kind":"response","raw_bytes":21289,"compact_bytes":20894},
  {"ticker":"NOKIA.HE","kind":"response","raw_bytes":20795,"compact_bytes":20735},
  {"ticker":"TTD","kind":"response","raw_bytes":23583,"compact_bytes":21766},
  {"ticker":"REGN","kind":"response","raw_bytes":21990,"compact_bytes":21071},
  {"ticker":"9988.HK","kind":"response","raw_bytes":21328,"compact_bytes":21224},
  {"ticker":"SNOW","kind":"input","raw_bytes":98396},
  {"ticker":"OKTA","kind":"input","raw_bytes":103077},
  {"ticker":"ORCL","kind":"input","raw_bytes":102413}]}
```

If a recomputed Go `json.Compact` size differs from the jq figure for any retained payload, **record the Go figure and note the divergence in the provenance string.** Do not silently keep the jq number.

- [ ] **Step 5: Add the standing assertion that LLY currently fails**

```go
// Baseline: describes the defect, not the fix. Task 8 inverts this.
func TestBaselineRawByteBudgetRejectsLLY(t *testing.T) {
	c := capacityCase(t, "LLY")
	if c.RawBytes <= c.Limit {
		t.Fatalf("fixture no longer reproduces the raw-byte overflow")
	}
	if c.CompactBytes > c.Limit {
		t.Fatalf("LLY must be recoverable by normalization alone: %d > %d", c.CompactBytes, c.Limit)
	}
}
```

- [ ] **Step 6: Run the package and commit**

```bash
go test ./internal/scoreboard ./internal/orchestrator -count=1
git add internal/scoreboard/testdata internal/orchestrator/testdata internal/orchestrator/thesis_capacity_baseline_test.go
git commit -m "Preserve the September 15 failure baseline as fixtures"
```

---

## Task 3: `chief_engine` configuration, migration matrix, preflight, redaction

**Model: Sonnet.**

**Files:**
- Modify: `internal/config/config.go` (Settings ~:20, file struct ~:112, redaction :306, file merge :569-572, env :642-647, validation :273-283)
- Modify: `internal/config/config_test.go`, `internal/config/redaction_test.go`
- Modify: `cmd/cfr/headless.go` (flag beside `--research-mode` at :26), `cmd/cfr/research_pair.go` (:59-67, :133)
- Modify: `cfr.toml.example`

**Interfaces:**
- Produces:
```go
// Settings
ChiefEngine string          // "claude" | "api"; "" means claude
ChiefAPI    model.APIConfig // dedicated; never inherits from [api]/[local]
// Settings.ChiefFallback gains:
ChiefFallbackEnabled *bool  // nil = omitted, distinct from explicit false
```

- [ ] **Step 1: Write the failing migration-matrix test**

```go
func TestChiefEngineMigrationMatrix(t *testing.T) {
	cases := []struct {
		name, toml string
		env        map[string]string
		wantEngine string
		wantFallback bool
		wantErr    string
	}{
		{name: "no selector, no fallback", toml: ``, wantEngine: "claude"},
		{name: "no selector, fallback key, enabled omitted",
			toml: "[chief_fallback]\nbase_url=\"https://x/v1\"\nmodel=\"m\"\napi_key=\"k\"\n",
			wantEngine: "claude", wantFallback: true},
		{name: "claude with fallback explicitly false",
			toml: "chief_engine=\"claude\"\n[chief_fallback]\nenabled=false\nbase_url=\"https://x/v1\"\nmodel=\"m\"\napi_key=\"k\"\n",
			wantEngine: "claude", wantFallback: false},
		{name: "api with valid dedicated settings",
			toml: "chief_engine=\"api\"\n[chief_api]\nbase_url=\"https://y/v1\"\nmodel=\"n\"\napi_key=\"k2\"\n",
			wantEngine: "api", wantFallback: false},
		{name: "api missing model",
			toml: "chief_engine=\"api\"\n[chief_api]\nbase_url=\"https://y/v1\"\napi_key=\"k2\"\n",
			wantErr: "chief_api.model"},
		{name: "api with fallback explicitly true",
			toml: "chief_engine=\"api\"\n[chief_api]\nbase_url=\"https://y/v1\"\nmodel=\"n\"\napi_key=\"k2\"\n[chief_fallback]\nenabled=true\nbase_url=\"https://x/v1\"\nmodel=\"m\"\napi_key=\"k\"\n",
			wantErr: "fallback is supported only after a Claude primary"},
		{name: "invalid enum", toml: "chief_engine=\"gpt\"\n", wantErr: "chief_engine"},
		{name: "env overrides file",
			toml: "chief_engine=\"claude\"\n",
			env: map[string]string{"CFR_CHIEF_ENGINE": "api",
				"CFR_CHIEF_API_BASE_URL": "https://z/v1", "CFR_CHIEF_API_MODEL": "q",
				"CFR_CHIEF_API_KEY": "k3"},
			wantEngine: "api"},
	}
	// each case: write toml into t.TempDir(), set env, Load, assert
}
```

**Critical:** each case writes into `t.TempDir()` and loads from there. The repo's own `./cfr.toml` carries live credentials and must never be reachable from a test.

- [ ] **Step 2: Assert the Chief key never leaks and never inherits**

```go
func TestChiefAPIKeyIsRedactedAndNeverInherited(t *testing.T) {
	// [api].api_key set, [chief_api].api_key absent, chief_engine="api"
	// -> must be a configuration error naming chief_api.api_key,
	//    not a silent inheritance of the cheap key.
	// And: an error string containing the key is a test failure.
}
```
Add `s.ChiefAPI.APIKey` to the redaction list at `internal/config/config.go:306`.

- [ ] **Step 3: Run the tests to confirm they fail**

`go test ./internal/config -run Chief -count=1` → FAIL (unknown field / no such behaviour).

- [ ] **Step 4: Implement**

Add the fields, the TOML tags (`chief_engine`, `[chief_api]`, `[chief_fallback].enabled`), env reads (`CFR_CHIEF_ENGINE`, `CFR_CHIEF_API_BASE_URL`, `CFR_CHIEF_API_MODEL`, `CFR_CHIEF_API_KEY`, `CFR_CHIEF_API_MAX_TOKENS`, `CFR_CHIEF_FALLBACK_ENABLED`), and the validation beside the existing `research_mode` enum check. Use `*bool` for `enabled` so omitted and false differ. Default `ChiefAPI.MaxTokens` to the existing `defaultMaxTokens` when unset.

- [ ] **Step 5: Add the `--chief-engine` flag**

`cmd/cfr/headless.go` beside `--research-mode`; `cmd/cfr/research_pair.go` beside its existing flags. Flags win over env, per the fixed precedence.

- [ ] **Step 6: Preflight — check the Claude binary only when Claude is selected**

`cmd/cfr/research_pair.go:133` currently reads `settings.Binaries[model.CLIClaude]` unconditionally. Gate it on the resolved engine. Resolve Chief settings **before** pre-screening or frozen-corpus collection, so an invalid config fails before any data acquisition.

```go
func TestAPIChiefRunsWithNoClaudeBinaryInstalled(t *testing.T) {
	// chief_engine="api", cheap_engine="api", Binaries[CLIClaude] =
	// "/nonexistent/claude". Assert Load and the run's preflight both succeed:
	// an API-only run or research-pair must not require an installed Claude.
}

func TestSelectedClaudeWithMissingBinaryFailsBeforeAcquisition(t *testing.T) {
	// chief_engine="claude", Binaries[CLIClaude] = "/nonexistent/claude".
	// Assert the error surfaces before any market-data request: count HTTP
	// hits on a test price server and require zero.
}
```

Do **not** add a probe call to validate subscription access — a binary version check cannot prove account access, and a probe costs money. Access is established by the actual call.

- [ ] **Step 7: Document the keys in `cfr.toml.example`**

Commented-out block beside `[chief_fallback]` (~:305), stating that `[chief_api]` is dedicated and never inherits from `[api]`, `[local]`, or `DEEPSEEK_API_KEY`.

- [ ] **Step 8: Verify and commit**

```bash
go test ./internal/config ./cmd/cfr -count=1 && gofmt -l internal/config cmd/cfr
git commit -am "Add a chief_engine selector with dedicated Chief API credentials"
```

---

## Task 4: Shared Chief execution helper and the four routing sites

**Model: Sonnet. Reviewer: Opus.** The highest-integration task in Increment A.

**Files:**
- Create: `internal/orchestrator/chief.go`, `internal/orchestrator/chief_test.go`
- Modify: `internal/orchestrator/thesis.go` (:85-123 `call`, :239, :284), `internal/orchestrator/orchestrator.go` (:1213, :1305)

**Interfaces:**
- Consumes: `Settings.ChiefEngine`, `Settings.ChiefAPI` from Task 3.
- Produces:
```go
type chiefPurpose string
const (
	chiefInitial    chiefPurpose = "synthesis"
	chiefCorrective chiefPurpose = "corrective"
	chiefFallback   chiefPurpose = "fallback"
)

// Resolved once per run, before any data acquisition.
type chiefEngine struct {
	CLI     model.CLI       // CLIClaude or CLIApi
	Model   string          // the actual model name, for provenance
	Binary  string          // CLI only
	API     model.APIConfig // API only; never the cheap pool's config
}
func resolveChiefEngine(cfg Config) (chiefEngine, error)
func (e chiefEngine) outputTokens() int

// Everything a call needs, so engine type no longer implies call semantics.
// The field order mirrors runAgent's parameters (runner.go:38) so the mapping
// is obvious: runAgent(ctx, t.CLI, role, string(t.Stage), prompt, t.Timeout,
//                      t.Retry, t.Model, t.Binary, t.API)
type callTarget struct {
	CLI     model.CLI
	Model   string
	Binary  string
	API     model.APIConfig
	Stage   model.Stage       // model.StageAnalysis | model.StageSynthesis
	Timeout time.Duration
	Retry   model.RetryPolicy
}
func (t *thesisRunner) cheapTarget() callTarget
func chiefTarget(e chiefEngine, cfg Config, p chiefPurpose) callTarget
```
`chiefTarget` always sets `Stage: model.StageSynthesis`, `Timeout: cfg.Timeouts.Synthesis` and `Retry.MaxAttempts: cfg.SynthesisMaxAttempts`, for every purpose and both engines. `cheapTarget` always sets `model.StageAnalysis`, `cfg.Timeouts.Analysis` and `cfg.Retry`.

- [ ] **Step 1: Write the failing routing matrix test**

```go
func TestChiefRoutingMatrix(t *testing.T) {
	// engine ∈ {claude, api} × pipeline ∈ {legacy, thesis} × purpose ∈ {initial, corrective}
	// Two httptest servers with DIFFERENT base URLs, models and max_tokens:
	//   cheapSrv  -> model "cheap-model",  max_tokens 4096
	//   chiefSrv  -> model "chief-model",  max_tokens 24576
	// Assert for every api case:
	//   - the Chief request landed on chiefSrv, never cheapSrv
	//   - its body carries model "chief-model" and max_tokens 24576
	//   - the corrective call used the SAME target as the initial call
	// Assert for every claude case:
	//   - zero requests reached either HTTP server for the Chief
	//   - the fake claude binary received the prompt
}
```
The deliberately different endpoints are the point: accidental reuse of the cheap config is otherwise invisible.

- [ ] **Step 2: Write the failing call-semantics test**

```go
func TestAPIChiefKeepsSynthesisTimeoutAndStage(t *testing.T) {
	// cfg.Timeouts.Synthesis = 90s, cfg.Timeouts.Analysis = 5s,
	// cfg.SynthesisMaxAttempts = 2, cfg.Retry.MaxAttempts = 4.
	// With chief_engine=api the Chief call must still use 90s and 2 attempts,
	// and its report Stage must be model.StageSynthesis, not StageAnalysis.
}
```
This is the regression guarding Diagnosis §5.

- [ ] **Step 3: Run both; confirm they fail**

`go test ./internal/orchestrator -run 'ChiefRouting|APIChiefKeeps' -count=1`

- [ ] **Step 4: Implement `chief.go`**

`resolveChiefEngine` reads `cfg.ChiefEngine`; `""` and `"claude"` both yield `{CLI: CLIClaude, Model: cfg.Models[CLIClaude], Binary: cfg.Binaries[CLIClaude]}`; `"api"` yields `{CLI: CLIApi, Model: cfg.ChiefAPI.Model, API: cfg.ChiefAPI}`. Return an error, never a partial engine.

- [ ] **Step 5: Thread `callTarget` through `thesis.go`**

Change `func (t *thesisRunner) call(ctx, role, name, data string, cli model.CLI)` to take a `callTarget`. Delete both `if cli == model.CLIClaude` branches at `thesis.go:104-112` — timeout, retry and stage now come from the target. Replace `outputTokens` derivation at `thesis.go:90-95` with `target.API.MaxTokens` (falling back to `defaultMaxTokens`). Every existing cheap call site passes `t.cheapTarget()`; Chief sites at `:239` and `:284` pass `chiefTarget(e, cfg, chiefInitial)` / `chiefTarget(e, cfg, chiefCorrective)`.

- [ ] **Step 6: Replace the legacy sites**

`orchestrator.go:1213` and `:1305` currently call `runAgent(ctx, model.CLIClaude, "chief-analyst", …, cfg.Models[CLIClaude], cfg.Binaries[CLIClaude], model.APIConfig{})`. Route both through the resolved engine. **Keep the report name `chief-analyst`** — history compatibility depends on it. The actual engine and model are recorded separately (Task 6).

- [ ] **Step 7: Confirm nothing else pins Claude**

```bash
grep -rn "CLIClaude" internal/orchestrator/*.go | grep -v _test.go
```
Expected remaining: `orchestrator.go:66,67,197-198,210-211` (defaults), `pool.go:33` (comment), `runner.go:62,103,120,134` (Claude CLI transport specifics), `research_pair.go:133` (gated in Task 3). **No `CLIClaude` may remain at a Chief dispatch site.**

- [ ] **Step 8: Verify and commit**

```bash
go test ./internal/orchestrator ./cmd/cfr -count=1 && go vet ./... && gofmt -l internal/orchestrator
git commit -am "Route every Chief call through one resolved engine"
```

---

## Task 5: Fallback gating and permanent-failure stop

**Model: Sonnet.**

**Files:** Modify `internal/orchestrator/fallback.go`, `internal/orchestrator/thesis.go` (:252-275), `internal/orchestrator/runner.go` (:134), `internal/orchestrator/apiengine.go`; extend `internal/orchestrator/chief_test.go`.

**Interfaces:**
- Consumes: `chiefEngine`, `chiefPurpose` (Task 4); `ChiefFallbackEnabled *bool` (Task 3).
- Produces: `func chiefFallbackAllowed(cfg Config, primary chiefEngine) (model.APIConfig, bool, error)`.

- [ ] **Step 1: Write the failing gating tests**

```go
func TestAPIPrimaryNeverAttemptsClaudeOrASecondIdenticalCall(t *testing.T) {
	// chief_engine=api, [chief_fallback] credentials still present, enabled omitted.
	// Make the primary Chief call fail (500 from chiefSrv).
	// Assert: exactly ONE request reached chiefSrv, zero reached the claude binary,
	// zero reached the fallback endpoint. Run finalizes degraded.
}

func TestFallbackDisabledExplicitlyWithCredentialsPresent(t *testing.T) {
	// chief_engine=claude, [chief_fallback].enabled=false, credentials present.
	// Primary claude fails -> zero fallback requests; degraded.
}

func TestHistoricalClaudeToAPIFallbackStillWorks(t *testing.T) {
	// chief_engine omitted, [chief_fallback] credentials present, enabled omitted.
	// Primary claude fails -> exactly one fallback request; result parsed;
	// the primary failure is STILL recorded and the run stays degraded.
}
```

- [ ] **Step 2: Write the failing permanent-failure tests**

```go
func TestPermanentAuthFailureStopsUnchangedRetries(t *testing.T) {
	// API Chief returns 401 three times in a row would be the old behaviour.
	// Assert exactly ONE dispatched attempt, FailureKind "permanent_auth".
	// Same assertion for 403.
}

func TestOutputLimitGetsNoUnchangedRetryOrSchemaRepair(t *testing.T) {
	// finish_reason=length -> FailureKind "output_limit", one attempt,
	// usage preserved, zero repair calls.
}
```
`runner.go:134` already stops unchanged retries on the disabled-Claude-subscription string. Extend the same policy to the API engine's 401/403, keeping bounded transient retries for 429/5xx untouched.

- [ ] **Step 3: Run; confirm failures. Step 4: Implement `chiefFallbackAllowed`**

Truth table — `enabled` is `nil` (omitted) / `true` / `false`:

| primary | enabled | credentials | result |
| --- | --- | --- | --- |
| claude | nil | present | allowed |
| claude | nil | absent | not allowed |
| claude | true | present | allowed |
| claude | false | present | not allowed |
| api | nil / false | present | not allowed |
| api | true | present | configuration error at load (Task 3) |

- [ ] **Step 5: Preserve the all-failed skip**

`thesis.go:238` (`usableDossiers > 0`) must still gate *both* the primary and the fallback. Re-run `TestThesisAllFailedSkipsChiefAndPersistsDegradedResults` unchanged — it must pass without edits. If it needs editing, the change is wrong.

- [ ] **Step 6: Verify and commit**

```bash
go test ./internal/orchestrator -count=1
git commit -am "Gate the Chief fallback on its own enablement and stop permanent failures"
```

---

## Task 6: Chief provenance in metadata, headless, TUI and scoreboard cohorts

**Model: Sonnet.**

**Files:** Modify `internal/model/types.go` (`RunMeta`), `internal/orchestrator/thesis.go:327`, `internal/orchestrator/orchestrator.go:1476`, `cmd/cfr/headless.go`, `internal/tui/results.go`, `internal/scoreboard/research*.go`; tests in each.

**Interfaces:**
- Produces, additive on `RunMeta`:
```go
ChiefEngine    string `json:"chief_engine,omitempty"`    // configured primary
ChiefModel     string `json:"chief_model,omitempty"`     // model actually addressed
ChiefAttempted string `json:"chief_attempted,omitempty"` // engine that was called
ChiefAccepted  string `json:"chief_accepted,omitempty"`  // engine whose output was used
```
`SynthesisModel` keeps its meaning and its place; it is no longer the only source.

- [ ] **Step 1: Write the failing provenance test**

```go
func TestChiefProvenanceDistinguishesConfiguredAttemptedAccepted(t *testing.T) {
	// claude primary fails, api fallback succeeds:
	//   ChiefEngine "claude", ChiefAttempted "claude,api", ChiefAccepted "api"
	// api primary succeeds:
	//   ChiefEngine "api", ChiefAttempted "api", ChiefAccepted "api",
	//   ChiefModel "chief-model"  (NOT cfg.Models[CLIClaude], NOT "cheap-model")
}
```
Today both call sites write `SynthesisModel: cfg.Models[model.CLIClaude]`, which under an API Chief reports `"opus"` for a DeepSeek call. That is the bug this test pins.

- [ ] **Step 2: Write the failing agreement test**

```go
func TestJSONTextAndTUIAgreeOnChiefAndResearchCounts(t *testing.T) {
	// One fixture run; assert headless --json, the text renderer and the TUI
	// results model report the same primary engine, recovered failures,
	// completed research, completed reviews, failed research and decisions.
}
```

- [ ] **Step 3: Write the failing cohort test**

```go
func TestScoreboardCohortSeparatesChiefEngines(t *testing.T) {
	// Two runs, identical but for chief engine. --research-compare must not
	// pool them; a run with no chief_engine field is "claude (unrecorded)",
	// never silently merged into either cohort.
}
```

- [ ] **Step 4: Run all three; confirm failures. Step 5: Implement.**

Additive fields only. An absent field on a historical run means *unrecorded* — never substitute a default and never rewrite a stored artifact.

- [ ] **Step 6: Verify and commit**

```bash
go test ./internal/model ./internal/orchestrator ./internal/scoreboard ./internal/tui ./cmd/cfr -count=1
git commit -am "Record which engine produced each Chief answer"
```

---

## Task 7: Increment A documentation

**Model: Haiku.** Transcription; the replacement text is given.

**Files:** Modify `CLAUDE.md`, `AGENTS.md`, `README.md`, `cfr.toml.example` (:438-439), `docs/workflow/thesis-research.md` (§"Running and configuration").

- [ ] **Step 1: Replace the CLAUDE.md hard-constraint bullet**

The bullet beginning "**Model access: CLI subprocess by default; a keyed API is allowed for the *cheap-research role*…**" becomes:

```markdown
- **Model access: the Chief Analyst engine is selected by `chief_engine`; the
  cheap-research role is selected by `cheap_engine`.** Heavy synthesis (Chief
  Analyst) runs either as a `claude` CLI shell-out in headless/print mode (`-p`)
  or through the OpenAI-compatible HTTP engine in `internal/orchestrator/apiengine.go`,
  chosen by `chief_engine` (`claude` | `api`; omitted means `claude`). This
  project currently selects `api` with DeepSeek, because Claude subscription
  access is paused; `chief_engine=claude` switches back in one setting. An API
  Chief needs its own dedicated credentials (`[chief_api]` / `CFR_CHIEF_API_*`),
  **never** inherited from `[api]`, `[local]` or `DEEPSEEK_API_KEY`, so turning
  on `cheap_engine=api` can never silently also spend on synthesis. Do not add
  an SDK — the API Chief reuses the existing stdlib HTTP engine.
  The separate off-by-default `chief_fallback` engine
  (`internal/orchestrator/fallback.go`'s `attemptChiefFallback`) is unchanged and
  remains a Claude-primary-only resilience call: it fires after the primary
  `claude` call exhausts `synthesis_max_attempts` or its JSON fails to parse,
  strictly before the mechanical `buildDegradedIdeas` fallback, and it is never
  attempted when `chief_engine=api`. `[chief_fallback].enabled` distinguishes
  an omitted key (fallback stays enabled when credentials are present, as
  before) from an explicit `false`.
```

The rest of that bullet — the `cheap_engine` sub-list for `gemini`/`api`/`local`, and the market-data parenthetical — is unchanged. Keep it.

- [ ] **Step 2: Amend the cost-split bullet**

Append to "**Cost split:**":

```markdown
  The split is about *which model does which work*, not about which transport
  carries it: an API Chief is still one heavy synthesis call on a strong model,
  addressed through its own credentials and its own cap, never the cheap
  research model.
```

- [ ] **Step 3: Fix `cfr.toml.example:438-439`**

"Claude CLI remains the final selector" → "the Chief Analyst remains the final selector, on the engine `chief_engine` names".

- [ ] **Step 4: Mirror into `AGENTS.md` and `README.md`**

Same substance, matching each file's existing voice and length.

- [ ] **Step 5: Add the selector to `docs/workflow/thesis-research.md` §"Running and configuration"**

One paragraph: the selector, its two values, the dedicated-credential rule, and that engine selection changes neither persona (`chief-analyst` for legacy, `thesis-chief` for thesis).

- [ ] **Step 6: Verify and commit**

```bash
go build ./... && go test ./internal/agents -count=1 && git diff --check
git commit -am "Document the Chief engine selector"
```

---

### ▸ Gate A

Before Task 8: `go build ./... && go vet ./... && go test ./... -count=1` all green; `gofmt -l .` empty; `git diff --check` clean. Ledger line: `Gate A: green at <sha>`.

---

## Task 8: Compact-payload response contract and the recovery order

**Model: Sonnet. Reviewer: Opus.** The single highest-value change in the plan — it alone recovers LLY.

**Files:**
- Create: `internal/orchestrator/thesis_response.go`, `internal/orchestrator/thesis_response_test.go`
- Modify: `internal/orchestrator/thesis_budget.go:122-129`, `internal/model/research_reliability.go`
- Read: `internal/orchestrator/thesis_schema.go:221-242` (the recovery branch)

**Interfaces:**
- Produces, in **`internal/model/research_reliability.go`** (the type must live in `model` because `PromptProfile` does — an orchestrator-local type cannot be a field of it):
```go
type ResponseMeasure struct {
	RawBytes      int    `json:"raw_bytes"`
	PayloadBytes  int    `json:"payload_bytes"`
	Method        string `json:"normalization"` // "json.Compact of the fenced payload"
	RawSHA256     string `json:"raw_sha256"`
	PayloadSHA256 string `json:"payload_sha256"`
	Normalized    bool   `json:"normalized"`    // false when no payload could be extracted
}
```
and in `internal/orchestrator/thesis_response.go`:
```go
// measureResponse never decodes into a typed value. It locates the fenced JSON
// object and runs json.Compact over those exact bytes.
func measureResponse(stdout string) model.ResponseMeasure
```
`model.PromptProfile` gains two additive fields:
```go
ResponseContractVersion int                   `json:"response_contract_version,omitempty"`
Response                *ResponseMeasure      `json:"response,omitempty"`
```
`ResponseContractVersion` is **not** the existing `Version` field (which is the profile's own version, currently `1`, and stays `1`). Consumed by Tasks 11 and 14.

- [ ] **Step 1: Write the failing boundary test**

```go
func TestCompactPayloadBudgetRecoversLLYAndStillRejectsRealOversize(t *testing.T) {
	const limit = 20480
	lly := syntheticDossierOfCompactSize(t, 20326, 20493) // compact, raw
	m := measureResponse(lly)
	if m.PayloadBytes != 20326 || m.RawBytes != 20493 {
		t.Fatalf("measure = %d/%d", m.PayloadBytes, m.RawBytes)
	}
	r := model.Report{Status: model.StatusDone, Stdout: lly}
	responseCapacity(&r, &model.PromptProfile{ResponseLimit: limit})
	if r.Status != model.StatusDone {
		t.Fatalf("LLY must pass on payload bytes: %s", r.Err)
	}

	bac := syntheticDossierOfCompactSize(t, 20894, 21289)
	r = model.Report{Status: model.StatusDone, Stdout: bac}
	responseCapacity(&r, &model.PromptProfile{ResponseLimit: limit})
	if r.FailureKind != "response_capacity" {
		t.Fatalf("BAC exceeds the budget on payload bytes too; got %q", r.FailureKind)
	}
}
```

- [ ] **Step 2: Write the failing losslessness test**

```go
func TestNormalizationPreservesUnknownFieldsUnicodeEscapesAndBigNumbers(t *testing.T) {
	in := `{"ticker":"9988.HK","unknown_future_field":{"a":[1,2]},
	        "quote":"He said \"no\" — 阿里巴巴 集团",
	        "big":123456789012345678901234567890,"small":1.000,"neg":-0.0}`
	// After json.Compact, re-decoding with UseNumber must yield a value
	// DeepEqual to the original decoded with UseNumber, and every byte of the
	// quote string must be identical.
}
```
`json.Compact` is byte-preserving for string contents and numeric spellings; decoding into `model.CandidateDossier` and re-emitting is not. The test is what stops a future refactor from reaching for the typed round-trip.

- [ ] **Step 3: Write the failing recovery-order tests**

```go
func TestMalformedPayloadCannotBeNormalizedIntoValidity(t *testing.T)
// Unparseable JSON -> Normalized=false, RawBytes used, schema-repair path,
// never a capacity pass.

func TestTruncatedCompletionGetsNoNormalizationBenefit(t *testing.T)
// finish_reason=length -> "output_limit", one attempt, zero repair calls,
// regardless of how small the compacted prefix is.

func TestSchemaRepairAndCompactionNeverChain(t *testing.T)
// A response that took a schema repair and is then oversized gets NO
// compaction; a compacted response that is malformed gets NO schema repair.

func TestLocalNormalizationConsumesNoModelCall(t *testing.T)
// An LLY-shaped response dispatches exactly zero recovery calls.
```

- [ ] **Step 4: Run all four groups; confirm failures**

`go test ./internal/orchestrator -run 'CompactPayload|Normalization|Malformed|Truncated|NeverChain|NoModelCall' -count=1`

- [ ] **Step 5: Implement `measureResponse`**

Locate the fenced JSON exactly as the existing parse path does (reuse `internal/parse` if it already exposes the extraction; do not write a second fence scanner). Run `json.Compact` into a `bytes.Buffer` over those bytes. On any error set `Normalized: false` and leave `PayloadBytes == RawBytes`.

- [ ] **Step 6: Rewrite `responseCapacity`**

```go
func responseCapacity(r *model.Report, profile *model.PromptProfile) {
	r.Prompt = profile
	if profile == nil || r.Status != model.StatusDone {
		return
	}
	m := measureResponse(r.Stdout)
	profile.Response = &m
	profile.ResponseContractVersion = 2
	if m.PayloadBytes > profile.ResponseLimit {
		r.Status = model.StatusFailed
		r.FailureKind = "response_capacity"
		r.Err = fmt.Sprintf(
			"response capacity exceeded: payload %d > %d bytes (raw %d); complete response retained for audit",
			m.PayloadBytes, profile.ResponseLimit, m.RawBytes)
	}
}
```

- [ ] **Step 7: Invert the Task 2 baseline assertion**

`TestBaselineRawByteBudgetRejectsLLY` documented the defect. Replace it with the fixed behaviour and keep the comment explaining what it used to assert, so the history is legible.

- [ ] **Step 8: Verify and commit**

```bash
go test ./internal/orchestrator ./internal/model -count=1
git commit -am "Measure the response budget against the payload it bounds"
```

---

## Task 9: Measured compaction allowance

**Model: Sonnet. Reviewer: Opus.**

**Files:** Modify `internal/orchestrator/thesis_compaction.go`, `internal/orchestrator/thesis_recovery_test.go`; read `internal/orchestrator/thesis_schema.go:221-242`.

**Interfaces:**
- Consumes: `measureResponse`, `model.ResponseMeasure` (Task 8).
- Produces, in **`internal/model/research_reliability.go`** (same reason as Task 8 — it becomes a field of `model.DomainStatus`):
```go
type CompactionAllowance struct {
	PayloadBytes    int            `json:"payload_bytes"`
	Limit           int            `json:"limit"`
	ProtectedBytes  int            `json:"protected_bytes"`  // payload with all 12 narratives emptied
	Excess          int            `json:"original_excess"`  // PayloadBytes - Limit
	NarrativeBudget int            `json:"narrative_budget"` // Limit - ProtectedBytes - headroom
	PerField        map[string]int `json:"per_field"`        // bytes, not characters
	Feasible        bool           `json:"feasible"`
}
```
and in `internal/orchestrator/thesis_compaction.go`:
```go
func measureCompaction(raw string, limit int) (model.CompactionAllowance, error)
```
`model.DomainStatus` gains `Allowance *CompactionAllowance` alongside the existing `OriginalNarratives`.

- [ ] **Step 1: Write the failing feasibility tests**

```go
func TestCompactionRefusesADoomedCallWhenProtectedContentAlreadyExceeds(t *testing.T) {
	// A dossier whose claims/quotations alone compact to > limit.
	// Assert: Feasible=false, ZERO model calls dispatched,
	//   Attempts==0, Contract=="failed", Recovery=="compaction",
	//   and the recorded error names protected_bytes and limit.
}

func TestCompactionAllowanceIsMeasuredNotAssumed(t *testing.T) {
	// SYNTHETIC, not BAC: payload 20894, limit 20480, protected 19900.
	// Corrected (ruling R21): 19,900 is not BAC's protected floor -- BAC's real
	// floor is 14,756 both before and after compaction, giving a ~5,468-byte
	// budget. These numbers are a synthetic exercise of the FORMULA only.
	// Note also that at protected 19900 the twelve 40-byte floors (480) exceed
	// the 324-byte budget, so this case is Feasible=FALSE and PerField sums to
	// 480 -- the "Sum(PerField) <= NarrativeBudget" line below cannot hold for
	// these inputs. Use feasible numbers to assert that, and keep an infeasible
	// case to exercise the refusal branch.
	// NarrativeBudget must be limit - protected - headroom, i.e. ~324 bytes,
	// NOT 12 * 400 characters. Sum(PerField) <= NarrativeBudget.
}

func TestPerFieldAllocationIsProportionalWithAFloorForNonEmptyFields(t *testing.T) {
	// Every field that is non-empty before compaction gets a non-zero
	// allocation; empty fields get zero; the sum never exceeds the budget.
}
```

- [ ] **Step 2: Write the failing input-capacity test**

```go
func TestCompactionInputCapacityIsCheckedBeforeDispatch(t *testing.T) {
	// The compaction prompt embeds the whole original response. With a
	// researcher input budget of 98,304 and a large original plus
	// (Corrected, ruling R23: the 23,583 figure previously here is the
	// manifest's TTD COMPACTION-RESULT raw_bytes, not an original -- the same
	// result-vs-original category error the Task 9 amendment corrects for
	// Step 7. A 23,551-byte raw response assembles to a 36,277-byte prompt,
	// which does NOT overflow 98,304; size the synthetic original accordingly.)
	// instructions, assert the input check runs and, when it fails, the
	// outcome is FailureKind "input_capacity" with zero attempts — not a
	// generic compaction failure.
}
```

- [ ] **Step 3: Run; confirm failures. Step 4: Implement `measureCompaction`**

Protected bytes: decode the raw payload into `map[string]json.RawMessage`, set each of the twelve `dossierNarrativeFields` to `""`, re-marshal, and `json.Compact`. That is the floor. Headroom: a declared constant `compactionHeadroom = 256` bytes covering JSON escaping and UTF-8 expansion the model cannot be expected to predict — name it and comment why.

Allocate `NarrativeBudget` across the fields in proportion to each field's current compacted byte length, with a floor of 40 bytes for any field that is currently non-empty. If the floors alone exceed `NarrativeBudget`, `Feasible = false`.

- [ ] **Step 5: Replace the compaction prompt**

Drop "Aim for under 400 characters each." Give the model the measured numbers:

```go
prompt := fmt.Sprintf(
	"Compact this complete dossier to fit a %d-byte response budget. "+
		"This consumes the one repair allowance; there is no further repair. "+
		"Shorten only these narrative fields, to at most the UTF-8 byte budget "+
		"given for each — everything else in the payload is already accounted "+
		"for and must not change:\n%s\n"+
		"Preserve every material qualification and counterargument; a shorter "+
		"field that drops a caveat is a failure, not a success. Do not add "+
		"findings or upgrade the verdict. Every other field, including unknown "+
		"fields, claims, exact quotations, evidence IDs, numerical values, "+
		"requests, unresolved questions, conditions, events and status must "+
		"remain byte-identical. Return one complete fenced JSON object with no "+
		"surrounding prose.\nOriginal response:\n%s",
	a.Limit, perFieldLines(a.PerField), raw)
```
`perFieldLines` renders one `field: N bytes (currently M)` line per field, ordered as `dossierNarrativeFields`.

- [ ] **Step 6: Keep the protected-field check exactly as it is**

`compactionPreservesEvidence` (`thesis_compaction.go:21-68`) is correct: a raw-map deep-equal minus the narrative fields, with `UseNumber`. **Do not touch it.** Re-run `TestCompactionProtectsAllNonNarrativeFields` and `TestCompactionFailureDoesNotBuyAnotherRepair` unedited.

- [ ] **Step 7: Verify against the Task 2 manifest**

Add a table test over all six capacity cases asserting which are feasible under the measured allowance and which are not. **Record the answer the code gives; do not assert that all six recover.** The audit was explicit that formatting overhead mattering for LLY is not proof the other five can be recovered.

- [ ] **Step 8: Verify and commit**

```bash
go test ./internal/orchestrator -count=1
git commit -am "Give compaction the space it actually has"
```

---

## Task 10: Researcher persona — material claims over repeated narrative

**Model: Sonnet.** Prose, but load-bearing prose.

**Files:** Modify `agents/thesis-researcher.md`; add one assertion to `internal/orchestrator/thesis_recovery_test.go`.

- [ ] **Step 1: Read the whole persona first**

154 lines. The twelve narrative fields are `long_case`, `short_case`, `no_trade_case`, `hypothesis`, `changed`, `expectations`, `underappreciated`, `mechanism`, `priced_in`, `counterargument`, `invalidation`, `catalyst_window`.

> **Corrected (ruling R20).** This step originally asserted "the observed failure shape is the same fact restated across several of them." That was never measured, and it is false. Across all six oversized dossiers, verbatim 8-gram overlap between any two narrative fields is 0-4 shingles out of ~1,000; loose 4-gram overlap with stopwords removed is 0.5%-3.9%; narrative text appearing anywhere in `claims` is 0.0%-0.5%. There is no repetition to remove.
>
> The measured failure is uniform overshoot of the existing advisory: **66 of 72 narrative fields exceed 400 characters**, by 25%-52%, while every other writing target is honoured comfortably (quotations average 85-155 chars against a 300-char allowance). Truncating every narrative to exactly 400 characters would have fitted **four of the six** under the limit (LLY, BAC, REGN, NOKIA.HE); TTD and 9988.HK would not, because their protected content alone is 16,476 and 16,341 bytes.
>
> A second defect, found by Task 9's review: `agents/thesis-researcher.md:110-111`'s fixed "400 characters per narrative field" is prepended to **every compaction prompt** (`compactDossier` -> `preparePrompt` -> `AssemblePrompt`), so after Task 9 it contradicts the measured per-field budgets sent below it. Reconciling the two is now this task's main work. See `task-10-context.md`.

- [ ] **Step 2: Add guidance, not a limit**

Each narrative field answers its own question and cites claim IDs rather than restating the claim's content. A fact already carried by a claim belongs in the claim. **Do not reinstate a hard twelve-claim cutoff** and **do not instruct the model to discard contradictory claims** — a dropped counterargument is a worse outcome than an oversized dossier, because the oversized one has a measured recovery path and the incomplete one does not.

- [ ] **Step 3: Add the regression assertion**

```go
func TestResearcherPersonaDoesNotReinstateAHardClaimCutoff(t *testing.T) {
	b := readPersona(t, "thesis-researcher")
	for _, banned := range []string{"at most 12 claims", "no more than twelve claims",
		"discard", "omit contradictory"} {
		if strings.Contains(strings.ToLower(b), banned) {
			t.Errorf("persona reinstates a cutoff or invites dropping evidence: %q", banned)
		}
	}
}
```

- [ ] **Step 4: Verify the persona still loads and commit**

```bash
go test ./internal/agents ./internal/orchestrator -count=1
git commit -am "Point the researcher at material claims rather than repeated narrative"
```

---

## Task 11: Section-measured prompt assembly

**Model: Sonnet.**

**Files:**
- Create: `internal/orchestrator/thesis_sections.go`, `internal/orchestrator/thesis_sections_test.go`
- Modify: `internal/orchestrator/thesis_budget.go` (delete `promptComponents` at :187-215; rewrite `preparePrompt` at :22-60), `internal/orchestrator/thesis.go` (every prompt-building site), `internal/model/research_reliability.go`

**Interfaces:**
- Produces:
```go
type promptSection struct {
	Name      string // stable identifier, e.g. "evidence", "request_results"
	Body      string
	Mandatory bool   // mandatory sections are never trimmed; they fail the call instead
}
// assemble concatenates in order, measuring each section's exact byte size.
// Optional sections are dropped from the tail until the total fits; every drop
// is named in omitted. A mandatory overflow returns promptCapacityError.
func assembleSections(s []promptSection, limit int) (text string, sizes map[string]int, omitted []string, err error)
```
`PromptProfile.Components` is now populated from `sizes` directly. Consumed by Tasks 12, 13, 14.

- [ ] **Step 1: Write the failing attribution test**

```go
func TestSectionSizesAreExactAndNoSectionIsFoldedIntoItsNeighbour(t *testing.T) {
	s := []promptSection{
		{Name: "identity", Body: "abc", Mandatory: true},
		{Name: "temporal_facts", Body: "defgh", Mandatory: true},
		{Name: "evidence", Body: strings.Repeat("x", 100)},
	}
	_, sizes, _, err := assembleSections(s, 1000)
	// sizes must be exactly {identity:3, temporal_facts:5, evidence:100}
	// and sum(sizes) + separators must equal len(text).
}
```
Today a section without a marker string silently inherits its predecessor's bytes — which is why the SNOW/OKTA/ORCL overflows could not be attributed.

- [ ] **Step 2: Write the failing mandatory/optional tests**

```go
func TestOptionalSectionsAreDroppedFromTheTailAndNamed(t *testing.T) {
	// Three mandatory sections of 100 bytes each and two optional of 500,
	// limit 900. Assert: text contains all three mandatory bodies, contains
	// neither optional body, omitted == []string{"optional_b", "optional_a"}
	// (tail first), and len(text) <= 900.
}

func TestMandatoryOverflowIsACapacityErrorNamingTheRequirements(t *testing.T) {
	// Two mandatory sections of 600 bytes, limit 900.
	// Assert: err is a promptCapacityError, text is "", and err.Error()
	// contains both section names — the caller must be able to say WHICH
	// requirement did not fit, which is what the September 15 audit could not.
}
```

- [ ] **Step 3: Run; confirm failures. Step 4: Implement `assembleSections`.**

- [ ] **Step 5: Convert every thesis prompt site to sections**

The current marker map at `thesis_budget.go:193-200` is the inventory of section names — reuse those exact names so existing artifacts stay comparable: `identity`, `temporal_facts`, `evidence`, `source_urls`, `request_results`, `dossier_hash`, `previous_dossier`, `retrieval_errors`, `previous_challenge`, `revision_issues`, `company_board`, `quant`, `macro`, `risk_policy`. Mandatory: `identity`, `temporal_facts`, `dossier_hash`, `revision_issues`, and required quotations within `evidence`. Optional: `retrieval_errors`, `source_urls`, and optional context within `evidence`.

- [ ] **Step 6: Delete `promptComponents` and its marker map**

`grep -rn promptComponents internal/` must return nothing.

- [ ] **Step 7: Verify and commit**

```bash
go test ./internal/orchestrator ./internal/model -count=1 && go vet ./...
git commit -am "Build prompts from sections that measure themselves"
```

---

## Task 12: Global Chief board budget

**Model: Opus.** No obviously correct allocation shape; this is the design task.

**Files:** Modify `internal/orchestrator/thesis_budget.go:74-120` (`chiefContext`), `internal/orchestrator/thesis.go:231`; extend `internal/orchestrator/thesis_reliability_test.go`.

**Interfaces:**
- Consumes: `assembleSections` (Task 11), `promptDocumentsAt` (existing, `thesis_passages.go`).
- Produces: `func chiefBoard(research []thesisResearch, textBudget int) ([]chiefCompany, []string /*omitted*/)`.

- [ ] **Step 1: Write the failing full-board test**

```go
func TestTwelveRealisticDossiersFitTheChiefInputBudget(t *testing.T) {
	// Twelve dossiers at realistic successful size — NOT twelve tiny fixtures.
	// Size them from internal/orchestrator/testdata/sep13-dossiers (15k-25k each).
	// Assert the assembled Chief prompt <= 196,608 bytes and that all twelve
	// candidates retain their outcome record.
}
```
The observed board occupied 142,756 bytes with **three** usable dossiers. Twelve tiny fixtures would pass and prove nothing; the audit says so explicitly.

- [ ] **Step 2: Write the failing fairness test**

```go
func TestBoardAllocationIsNotFirstComeFirstServed(t *testing.T) {
	// Twelve companies, the first three carrying far more evidence than the
	// rest. Assert the twelfth still receives a non-zero document allocation
	// whenever its claims require supporting passages, and that reordering the
	// slice does not change any company's allocation.
}
```
Order-independence is the property that distinguishes a global budget from a loop that runs out.

- [ ] **Step 3: Write the failing retention test**

```go
func TestEveryCandidateKeepsItsOutcomeEvenWhenEvidenceIsTrimmed(t *testing.T) {
	// Failed, deferred and ineligible companies must still appear on the board
	// with their outcome. Trimming evidence must never delete a candidate.
}
```

- [ ] **Step 4: Run; confirm failures. Step 5: Design and implement `chiefBoard`.**

Replace the per-company `promptDocumentsAt(v.Documents, 9000, …)` at `:104` with two passes over the whole board:

1. **Required pass.** Every cited claim's required quotations, for every company, reserved globally before any optional context — the same rule `thesis_passages.go` already applies within one company, lifted to the board. If the required total alone exceeds `textBudget`, that is a capacity failure naming the companies involved, not a silent trim.
2. **Optional pass.** Distribute the remainder proportionally to each company's *unmet* need, not to its total evidence, so an evidence-heavy company cannot starve a thin one that still needs its passages.

Candidate records (outcome, eligibility, temporal facts) are cheap and always retained; only document text is allocated.

- [ ] **Step 6: Derive `textBudget` from the measured sections**

`textBudget = chief.InputBytes − sizeof(instructions + quant + macro + risk_policy + per-company outcome records)`, taken from Task 11's `sizes` map, not from a constant.

- [ ] **Step 7: Verify and commit**

```bash
go test ./internal/orchestrator -count=1
git commit -am "Budget the Chief board globally instead of per company"
```

---

## Task 13: Revision and challenge prompt fitting

**Model: Sonnet.** This is the SNOW/OKTA/ORCL task.

**Files:** Modify `internal/orchestrator/thesis.go` (the revision and challenge prompt builders), `internal/orchestrator/thesis_passages.go`, `internal/orchestrator/thesis_compaction.go`; extend `internal/orchestrator/thesis_reliability_test.go`.

**Interfaces:** Consumes `assembleSections` (Task 11) and `compactionAllowance.OriginalNarratives` (Task 9).

- [ ] **Step 1: Write the failing captured-case tests**

```go
func TestCapturedRevisionAndChallengePromptsFit(t *testing.T) {
	// SNOW 98,396 (+92), OKTA 103,077 (+4,773), ORCL 102,413 (+4,109)
	// against a 98,304-byte input limit. Rebuild each prompt from the same
	// inputs and assert it now fits, with every required quotation present and
	// every unresolved review issue retained.
}
```

- [ ] **Step 2: Write the failing quotation-dedup test**

```go
func TestEachRequiredQuotationIsStoredOnceAndStillResolvable(t *testing.T) {
	// A quotation cited by three claims appears in the prompt ONCE.
	// Every claim's evidence reference must still locate it in the same input:
	// a reference is valid only when its exact source passage is present.
}
```

- [ ] **Step 3: Write the failing compaction-history test**

```go
func TestSuccessfulCompactionDoesNotOverflowTheRevisionPrompt(t *testing.T) {
	// After a compaction, the original narratives must reach the challenger,
	// but as an explicitly budgeted "compaction_originals" section. Assert the
	// section is measured, appears in Components, and is dropped-and-named
	// rather than silently overflowing when the budget is tight.
}
```
SNOW overflowed by 92 bytes — the size of an unmeasured section appended after the fact.

- [ ] **Step 4: Write the failing request-ledger test**

```go
func TestRepeatedRequestResultsCompactToIDsWithoutForgettingOutcomes(t *testing.T) {
	// Requests already answered unavailable/unsupported compact to
	// id+outcome in the prompt. Assert the model is never invited to re-issue
	// one, and that the FULL durable ledger survives in the run artifacts.
}
```

- [ ] **Step 5: Write the failing hash-binding test**

```go
func TestShorteningAProjectionCannotAuthorizeADifferentDossier(t *testing.T) {
	// The dossier_hash the challenger echoes must bind to the FULL canonical
	// dossier, not to the shortened projection it was shown.
	// Build a dossier; project it twice with different text budgets.
	// Assert: dossierHash(full) is identical in both prompts, and the
	// per-prompt projection hash is recorded SEPARATELY in the profile and
	// differs between them. A review echoing the projection hash instead of
	// the dossier hash must be rejected by compactReviewProblems.
}
```
This is the one way prompt shortening could silently change what a review authorized. `dossierHash` (`thesis_budget.go:17-20`) already hashes the full struct — the test pins that it keeps doing so as the projections change under Tasks 12 and 13.

- [ ] **Step 6: Write the failing offset-boundary test**

```go
func TestCapacityUsesBytesWhileSpanOffsetsStayUnicodeCharacters(t *testing.T) {
	// A document mixing ASCII, "阿里巴巴集团", "—" and an emoji.
	// selected_spans offsets must index RUNES into the stored text and still
	// locate the exact quotation after assembly.
	// The capacity decision must use len([]byte(...)), not RuneCountInString:
	// assert a prompt whose rune count fits but whose byte count does not is
	// refused, naming input_capacity.
}
```

- [ ] **Step 7: Run all six; confirm failures. Step 8: Implement.**

Store each required quotation once in a `quotations` section keyed by evidence ID; claims reference the key. Compact answered request results to `id + outcome` while preserving any answer the model still needs, and keep the full durable ledger in the run artifacts. Budget `compaction_originals` as its own optional section.

- [ ] **Step 9: Verify and commit**

```bash
go test ./internal/orchestrator -count=1
git commit -am "Fit the revision and challenge prompts the September 15 run could not build"
```

---

## Task 14: Preparation-refused outcome and named omissions

**Model: Sonnet.**

**Files:** Modify `internal/model/research.go` (outcome constants), `internal/model/types.go` (`DomainStatus`), `internal/orchestrator/thesis_budget.go` (`promptCapacityError`, `promptFailureKind`), `internal/orchestrator/thesis_schema.go:221-246`, `cmd/cfr/headless.go`, `internal/tui/`; tests in each.

**Interfaces:**
- Produces: `model.OutcomeNotAttempted = "not_attempted"`; `DomainStatus.Omitted []string`.

- [ ] **Step 1: Write the failing separation test**

```go
func TestZeroAttemptCapacityRefusalDoesNotClaimTransportOrParsingFailure(t *testing.T) {
	// An input-capacity refusal must record:
	//   Attempts == 0
	//   Transport == model.OutcomeNotAttempted
	//   Parsing   == model.OutcomeNotAttempted
	//   FailureKind == "input_capacity"
	//   Omitted names the mandatory sections that exceeded capacity
	// Today both transport and parsing read "failed" though neither occurred.
}
```

- [ ] **Step 2: Write the failing stage-progress test**

```go
func TestUsableDossierFollowedByUnavailableReviewStaysVisibleAsResearchCompleted(t *testing.T) {
	// research completed / review failed, still blocked for selection.
	// Earlier successful work must not reset to an apparent zero.
}
```

- [ ] **Step 3: Write the failing aggregation test**

```go
func TestOneFailureIsCountedOnceNotAsBothCompanyNoteAndDomainError(t *testing.T) {
	// Aggregate by stable identity. The detailed evidence stays reachable in
	// the artifacts; the count is one.
}
```

- [ ] **Step 4: Write the failing unknown-usage test**

```go
func TestIncompleteUsageStaysALowerBoundNeverAMeasuredZero(t *testing.T) {
	// A Claude CLI report whose usage could not be parsed, plus one API
	// report with real usage. Assert the aggregate marks the total as a
	// lower bound (a separate boolean or an explicit "incomplete" marker),
	// that the unparsed call contributes 0 WITHOUT the total claiming to be
	// exact, and that no dollar figure is derived anywhere.
}
```

- [ ] **Step 5: Run; confirm failures. Step 6: Implement additively.**

`OutcomeNotAttempted` is a new value, not a renaming. Historical artifacts carrying `failed` in these positions keep it and are not reinterpreted.

- [ ] **Step 7: Verify and commit**

```bash
go test ./internal/model ./internal/orchestrator ./internal/tui ./cmd/cfr -count=1
git commit -am "Stop reporting transport and parsing failures that never happened"
```

---

## Task 15: Increment B documentation, implementation record, acceptance manifest

**Model: Sonnet.** The record needs judgment, not transcription.

**Files:**
- Modify: `docs/workflow/thesis-research.md` (§"Reliability contracts and diagnostics", lines ~467-497), `docs/workflow/output-schema.md`, `cfr.toml.example`
- Create: `docs/plans/2026-09-17-chief-engine-and-capacity.md`
- Create: `docs/plans/acceptance-manifest.sh`

- [ ] **Step 1: Rewrite the response-limit paragraphs in the workflow spec**

The spec currently says the response budget bounds the complete response. It now bounds the **compact JSON payload**; raw transport bytes are recorded separately and bound nothing. State the contract version (2), the recovery order, that local normalization consumes no model call, and that historical profiles without a contract version are read under their original semantics and never reinterpreted.

- [ ] **Step 2: Rewrite the compaction paragraph**

The allowance is measured from protected-field bytes plus structural overhead, allocated per field in bytes with declared headroom, and refuses to dispatch when protected content alone exceeds the budget.

- [ ] **Step 3: Document the new profile fields in `output-schema.md`**

`response_contract_version`, `raw_bytes`, `payload_bytes`, `normalization`, `raw_sha256`, `payload_sha256`, the per-section `components` map, `compaction.allowance`, `omitted`, `not_attempted`, and the four `chief_*` provenance fields. Mark every one additive; absent means unrecorded.

- [ ] **Step 4: Write the implementation record**

`docs/plans/2026-09-17-chief-engine-and-capacity.md`, in the house style of `2026-09-13-reliability-implementation.md`: a completion table with **three separate columns** — implementation, local verification, live acceptance — and live acceptance **Pending** on every row. State plainly that no live run was performed and that the offline gate is not a reliability claim.

- [ ] **Step 5: Write the acceptance manifest script**

`docs/plans/acceptance-manifest.sh` captures, before any live run: git revision and dirty state (with per-file hashes when dirty), persona hashes, a **redacted** resolved-config hash, run anchor, indices, provider caps, role byte budgets, retry/round/document limits, deadline, and the computed worst-case call ceiling `2 * (rounds + 3)` per company. It must **refuse to emit any key material** — assert that by grepping its own output for the configured key prefixes and failing if found. The script captures a manifest; it never launches a run.

- [ ] **Step 6: Cross-link the plan records**

Add this plan's record to the "Related records" list in `2026-09-15-deepseek-chief-and-research-reliability.md`, and mark WP1–WP6 and the WP8 provenance bullets as implemented there — leaving WP7, the rest of WP8, WP9 and WP10 Pending.

- [ ] **Step 7: Verify and commit**

```bash
go build ./... && go test ./... -count=1 && git diff --check
git commit -am "Record the capacity work and prepare the acceptance manifest"
```

---

### ▸ Gate B — the offline gate

This is where the plan stops. Nothing below is authorized by it.

```bash
go test ./internal/config ./internal/agents ./internal/orchestrator ./internal/model \
        ./internal/marketdata ./internal/store ./internal/scoreboard ./internal/tui \
        ./cmd/cfr -count=1
go test ./... -count=1
go build ./...
go vet ./...
gofmt -l .            # must be empty
git diff --check      # must be clean
```

Then the whole-branch review (Opus) over `merge-base..HEAD`, pointed at the ledger's deferred-minor and parked lines.

**Gate B is green when, and only when:**

- [ ] All 13 packages pass; `gofmt -l .` empty; `git diff --check` clean.
- [ ] LLY recovers: its compaction result (raw 20,493 / payload 20,326) is accepted on
      payload bytes. Note this is the compaction RESULT, not LLY's original response —
      the original measured 20,883 compact and legitimately needed its one compaction.
      Separately: a response whose payload already fits dispatches zero recovery calls.
- [ ] The remaining five capacity cases produce a *recorded, measured* feasible/infeasible verdict — whatever it is. Recovering all six is not required and must not be asserted.
- [ ] The captured SNOW, OKTA and ORCL prompts fit, with every required quotation and unresolved review issue retained.
- [ ] Twelve realistically-sized dossiers fit the Chief input budget, order-independently.
- [ ] Every migration-matrix row has a passing test; no error string contains a credential.
- [ ] An API-primary run attempts no Claude call and no second identical DeepSeek call.
- [ ] `TestThesisAllFailedSkipsChiefAndPersistsDegradedResults` passes **unedited**.
- [ ] JSON, text and TUI agree on engine and research counts.

**Gate B is not a reliability claim.** It says the pipeline can no longer fail for the reasons it failed on September 15. Whether real dossiers fit, and whether any of this improves returns, is unestablished until the operator runs `docs/plans/2026-09-12-reliability-acceptance.md` with the Task 15 amendments.

---

## Operator step — switching this project to the API Chief

**Not a subagent task.** `./cfr.toml` holds live credentials; no implementer touches it. Do this yourself, after Gate B is green, with the routing tests passing:

```toml
chief_engine = "api"

[chief_api]
base_url   = "<the existing chief_fallback base_url>"
model      = "<the existing chief_fallback model>"
max_tokens = <the existing chief_fallback max_tokens>
api_key    = "<the existing chief_fallback api_key>"
```

Copy the values from the existing `[chief_fallback]` block — the spec is explicit that the dedicated Chief endpoint, model and cap are reused, and equally explicit that the key is **not** sourced implicitly from `[api]`, `[local]` or `DEEPSEEK_API_KEY`. Leave `[chief_fallback]` in place; with `chief_engine="api"` it is inert (Task 5), and it is what you switch back to alongside `chief_engine="claude"` when Claude access returns.

Keep the prior values recoverable outside the repository. Never commit this file.

---

## Deferred — planned, not executed here

Tracked in the spec; a follow-up plan picks them up after Gate B and the live acceptance.

- **WP7 — research and regional evidence quality.** Audit JNJ, TTE.PA, ASML.AS (completed reasoning) and TTD, NOKIA.HE (bulky evidence). Yahoo foreign-symbol resolution (93 unresolved-symbol warnings in the Sept 15 run; discovery news reached 118/119 US but 7/47 European and 9/66 Asia-Pacific companies). Issuer release/link selection. The fixed six-name panel: ASML.AS, STLAM.MI, NOKIA.HE, 2330.TW, 9988.HK, BHP.AX.
- **WP8 remainder** — warning typing by region, filtered-vs-lost evidence labels.
- **WP9** — the full regression matrix as a standing document.
- **WP10** — bounded live acceptance, then matched frozen-pair evaluation at 10 and 15 sessions.
- **Open operator item, unchanged:** provider-side rotation of the previously exposed Alpha Vantage credential remains unconfirmed. Local redaction tests demonstrate handling, not revocation.

---

## Verification

**Per task:** each carries its own failing-test-first cycle and its own commit. No task is complete until its tests pass and the task review returns clean (or its findings are parked with a ruling at the round-5 cap).

**End to end, after Gate B — offline, no model spend:**

```bash
# 1. Hermetic thesis pipeline, both engines, via inline httptest fixtures.
go test ./internal/orchestrator -run 'Thesis|Chief' -count=1 -v

# 2. Headless legacy run against the fake CLIs (legacy personas only).
CFR_CLAUDE_BIN=$PWD/testdata/fakebin/claude \
CFR_GEMINI_BIN=$PWD/testdata/fakebin/agy \
  go run ./cmd/cfr run --indices sp500 --json ; echo "exit=$?"
# Expect exit 0 (complete) or 3 (degraded) — never a panic, never a hang.

# 3. Config matrix from a scratch directory, so the repo's real cfr.toml
#    with its live keys is unreachable.
cd "$(mktemp -d)" && printf 'chief_engine="api"\n' > cfr.toml && \
  go run <repo>/cmd/cfr run --json ; echo "exit=$?"
# Expect a configuration error naming chief_api.base_url — before any
# data acquisition, and with no key in the message.

# 4. Manifest capture, no run launched.
sh docs/plans/acceptance-manifest.sh > /tmp/manifest.json
grep -iE 'sk-|api_key|secret' /tmp/manifest.json && echo "LEAK" || echo "clean"
```

**Never during development:** `cfr run` from the repo root without `--json` against real engines. The project's `./cfr.toml` holds live DeepSeek, Alpaca, FRED and Alpha Vantage credentials, and no `CFR_*` variable can clear a key already set there — a stray run spends money. Use a scratch working directory.
