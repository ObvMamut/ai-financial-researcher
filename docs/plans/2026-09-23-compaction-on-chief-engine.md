# Narrative-Only Compaction on the Chief Engine — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the one-shot dossier compaction actually recover oversized thesis dossiers. The model returns only the twelve narrative fields; Go splices them into the original payload; and the call runs on the Chief engine instead of the cheap engine.

**Architecture:** Compaction stops asking a model to re-type roughly 15 KB of protected claims byte-for-byte. `spliceNarratives` copies every non-narrative field from the original raw payload, so protected content is identical by construction. It takes only the narrative strings from the reply. The spliced result is measured against the budget in Go. The compaction call is dispatched through `chiefTarget` on the run's resolved `chiefEngine`, as a documented exception to the cost split.

**Tech Stack:** Go standard library (`encoding/json`, `bytes`). Tests use inline `httptest` servers, following `thesisFixture` (`internal/orchestrator/thesis_schema_test.go:23`).

**Spec / evidence:** Live acceptance on 2026-09-23 (runbook `docs/plans/2026-09-12-reliability-acceptance.md`):
- STLAM.MI stopped twice on `response_capacity`: runs `runs/2026-09-23T14-25-54` and `runs/2026-09-23T14-42-49`.
- Across 15 recorded compactions, deepseek-chat shrank narratives to 0.61–0.99 of their size, whatever was requested. Byte limits and word limits both failed; a word-limit probe returned 0.69 where 0.47 was needed.
- The identical production prompt on deepseek-v4-pro met **12/12** per-field byte budgets (ratio 0.45 where 0.47 was needed). But it spent its whole 32,768-token cap, mostly on reasoning while re-typing claims, and was truncated before finishing. Probe output: `.data/acceptance/probe-stlam-v4pro-compaction.md`.

## Global Constraints

- No new dependency, provider, credential or paid service. Compaction reuses the already-configured Chief engine and its own credentials: `[chief_api]`, or the `claude` CLI.
- One repair allowance per response, never chained. A compaction that still fails to fit stops. There's no second compaction and no schema repair after it.
- Protected wire fields stay byte-identical. That includes unknown extensions, claims, quotations, numerical spellings, events, requests, uncertainties, conditions and status. `compactionPreservesEvidence` stays exactly as it is and still runs on the spliced result.
- No typed round-trip. The splice works on `map[string]json.RawMessage` and never decodes into `model.CandidateDossier` and re-emits.
- `measureCompaction`, `compactionHeadroom` (256) and `compactionPerFieldFloor` (40) stay unchanged.
- Default byte budgets stay unchanged: researcher 98,304/20,480. Provider output caps are never raised.
- `TestThesisAllFailedSkipsChiefAndPersistsDegradedResults` must pass without edits.
- Tests never resolve config from the repo root; the repo `./cfr.toml` holds live keys.

## Review Focus

1. **The reply echoes the whole dossier with a changed protected field** (e.g. `status: "rejected"`). The change is ignored: the spliced result keeps the original value, and compaction succeeds if it fits. → Task 2, `changed_status` case.
2. **The reply omits a narrative field.** The original text is kept for that field, and the Go size check still decides the outcome. → Task 1.
3. **Protected text contains `<`, `>`, `&` or non-ASCII.** The splice must not rewrite them as six-byte JSON unicode escapes (backslash-u-003c and so on), which would inflate the payload and break byte-identity. → Task 1.
4. **`chief_engine = "claude"`.** Compaction targets the Claude CLI through `chiefTarget`, never the cheap pool. → Task 3, `compactionTarget` unit test.
5. **The Chief API reply hits `finish_reason=length`** (v4-pro's reasoning uses up the cap). That's `output_limit` with one attempt, no retry and no further repair. → Task 3.

---

### Task 1: `spliceNarratives`

**Files:**
- Modify: `internal/orchestrator/thesis_compaction.go` (add the function after `compactionPreservesEvidence`)
- Test: `internal/orchestrator/thesis_recovery_test.go`

**Interfaces:**
- Produces: `func spliceNarratives(raw, reply string) (string, error)`. It returns a fenced (```` ```json ````) payload made of `raw`'s fields, with only the narrative fields listed in `dossierNarrativeFields` taken from `reply`.

- [ ] **Step 1: Write the failing test**

```go
func TestSpliceNarrativesKeepsProtectedBytesAndTakesOnlyNarratives(t *testing.T) {
	raw := "```json\n" + `{"ticker":"9988.HK","status":"watchlist","long_case":"old long","short_case":"old short",` +
		`"claims":[{"id":"c1","quote":"R&D <up> 阿里巴巴 \"q\""}],"big":123456789012345678901234567890,"small":1.000,` +
		`"future_extension":{"a":[1,2]}}` + "\n```"
	reply := fenced(map[string]any{"long_case": "new long", "status": "rejected",
		"claims": []any{}, "hypothesis": "not in original"})

	got, err := spliceNarratives(raw, reply)
	if err != nil {
		t.Fatal(err)
	}
	if err := compactionPreservesEvidence(raw, got); err != nil {
		t.Fatalf("protected content changed: %v", err)
	}
	var m map[string]json.RawMessage
	if err := decodeResearch(got, &m); err != nil {
		t.Fatal(err)
	}
	for field, want := range map[string]string{
		"long_case":  `"new long"`,   // taken from the reply
		"short_case": `"old short"`,  // omitted by the reply: original kept
		"status":     `"watchlist"`,  // protected: the reply's change is ignored
		"small":      `1.000`,        // numeric spelling survives
		"big":        `123456789012345678901234567890`,
	} {
		if string(m[field]) != want {
			t.Errorf("%s = %s, want %s", field, m[field], want)
		}
	}
	if _, ok := m["hypothesis"]; ok {
		t.Error("a narrative field absent from the original must not be added")
	}
	if !strings.Contains(got, `R&D <up> 阿里巴巴`) {
		t.Errorf("protected text was re-escaped: %s", got)
	}
}

func TestSpliceNarrativesRejectsUnusableReplies(t *testing.T) {
	raw := fenced(map[string]any{"ticker": "AAA", "long_case": "x"})
	for name, reply := range map[string]string{
		"unparseable":   "no fenced json here",
		"non-string":    fenced(map[string]any{"long_case": 42}),
	} {
		if _, err := spliceNarratives(raw, reply); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
```

- [ ] **Step 2: Run to confirm it fails**

Run: `go test ./internal/orchestrator -run TestSpliceNarratives -count=1`
Expected: FAIL, `undefined: spliceNarratives`.

- [ ] **Step 3: Implement**

```go
// spliceNarratives builds the compacted payload from the original's own bytes:
// every non-narrative field is copied from raw as its json.RawMessage, and only
// the narrative strings present in both raw and reply are taken from reply. A
// reply that also re-emits claims or status is harmless — those keys are never
// read — so protected content is identical by construction rather than by
// trusting the model to re-type it. SetEscapeHTML(false) keeps '<', '>' and '&'
// in protected strings as written (see measureCompaction's floor comment).
func spliceNarratives(raw, reply string) (string, error) {
	var original map[string]json.RawMessage
	if err := decodeResearch(raw, &original); err != nil {
		return "", fmt.Errorf("original payload: %w", err)
	}
	var next map[string]json.RawMessage
	if err := decodeResearch(reply, &next); err != nil {
		return "", fmt.Errorf("compaction reply: %w", err)
	}
	for _, field := range dossierNarrativeFields {
		v, inReply := next[field]
		if _, inOriginal := original[field]; !inReply || !inOriginal {
			continue
		}
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			return "", fmt.Errorf("compaction reply %s is not a string", field)
		}
		original[field] = v
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(original); err != nil {
		return "", err
	}
	return "```json\n" + strings.TrimSpace(buf.String()) + "\n```", nil
}
```

Erasure (an empty reply field where the original wasn't empty) is deliberately left to `compactionPreservesEvidence`, which already rejects it. Task 2 runs it on the spliced output.

- [ ] **Step 4: Run to confirm it passes**

Run: `go test ./internal/orchestrator -run TestSpliceNarratives -count=1` → PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/orchestrator/thesis_compaction.go internal/orchestrator/thesis_recovery_test.go
git commit -m "Splice compacted narratives into the original payload"
```

---

### Task 2: `compactDossier` asks for narratives only and measures the splice

**Files:**
- Modify: `internal/orchestrator/thesis_compaction.go` (`compactDossier`, prompt at ~:304 and result handling at ~:319-341)
- Test: `internal/orchestrator/thesis_recovery_test.go` (`TestCompactionFailureDoesNotBuyAnotherRepair`, `TestCompactionPromptCarriesTheMeasuredAllowance`)

**Interfaces:**
- Consumes: `spliceNarratives` (Task 1), plus the existing `measureResponse`, `decodeThesis`, `compactionPreservesEvidence` and `dossierWritingDiagnostics`.
- Produces: no new names. On a compaction that's still over budget, the `DomainStatus` has `FailureKind == "response_capacity"`.

- [ ] **Step 1: Update the tests to the new contract (failing)**

In `TestCompactionFailureDoesNotBuyAnotherRepair`, `changed_status` no longer fails. A reply that changes protected fields is ignored, not trusted. Split that case out of the failure loop:

```go
	for _, failure := range []string{"oversize", "malformed"} {
		// ... loop body unchanged ...
	}
```

Add this test beside it:

```go
func TestCompactionIgnoresProtectedChangesInTheReply(t *testing.T) {
	d := supportedResearch().Dossier
	d.ContractVersion = 2
	d.LongCase = strings.Repeat("long narrative ", 2000)
	calls := 0
	runner, _, done := thesisFixture(t, func(prompt string, _ int) string {
		calls++
		if !strings.Contains(prompt, "Compact this complete dossier") {
			return fenced(d)
		}
		next := d
		next.LongCase = "short"
		next.Status = "rejected"
		return fenced(next)
	})
	defer done()
	var out model.CandidateDossier
	reports, _, _, err := researchCall(context.Background(), runner, "thesis-researcher", "ignored", "data", &out, dossierSchema)
	if err != nil || calls != 2 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
	if out.Status != d.Status || out.LongCase != "short" {
		t.Fatalf("status=%q long_case=%q: protected change leaked or narrative lost", out.Status, out.LongCase)
	}
	if reports[1].Contract != "compacted" {
		t.Fatalf("recovery = %+v", reports[1])
	}
}
```

In `TestCompactionPromptCarriesTheMeasuredAllowance`, add these strings to the `for _, warn := range` list:

```go
		"containing only these narrative fields",
		"Go keeps every other field exactly as written",
```

- [ ] **Step 2: Run to confirm they fail**

Run: `go test ./internal/orchestrator -run 'TestCompaction' -count=1`
Expected: `TestCompactionIgnoresProtectedChangesInTheReply` fails (protected change rejected), and the prompt test fails on the new wording.

- [ ] **Step 3: Implement**

Replace the prompt in `compactDossier`. Keep the leading "Compact this complete dossier" — test fixtures route on it:

```go
	prompt := fmt.Sprintf(
		"Compact this complete dossier to fit a %d-byte response budget. "+
			"This consumes the one repair allowance; there is no further repair. "+
			"Rewrite only these narrative fields, each to at most the UTF-8 byte "+
			"budget given for it:\n%s\n"+
			"Preserve every material qualification and counterargument; a shorter "+
			"field that drops a caveat is a failure, not a success. Do not add "+
			"findings or upgrade the verdict. Return one fenced JSON object "+
			"containing only these narrative fields, as strings. Do not repeat "+
			"claims, quotations, evidence IDs or any other field: Go keeps every "+
			"other field exactly as written in the original.\nOriginal response:\n%s",
		allowance.Limit, perFieldLines(allowance.PerField, current), raw)
```

Replace the result handling after `if err != nil { ... return s, err }`:

```go
	spliced, err := spliceNarratives(raw, r.Stdout)
	if err == nil {
		err = compactionPreservesEvidence(raw, spliced)
	}
	if err == nil {
		if m := measureResponse(spliced); m.PayloadBytes > allowance.Limit {
			s.FailureKind = "response_capacity"
			err = fmt.Errorf("response capacity exceeded after compaction: payload %d > %d bytes; complete response retained for audit", m.PayloadBytes, allowance.Limit)
		}
	}
	var next model.CandidateDossier
	if err == nil {
		err = decodeThesis(spliced, &next, currentResearchSchema(dossierSchema))
	}
	if werr := t.run.WriteReport(name+"-compacted", spliced); werr != nil && err == nil {
		err = werr
	}
	if err != nil {
		s.Err = err.Error()
		return s, err
	}
```

(The success tail — `OriginalNarratives`, `WritingDiagnostics`, `Contract`/`Payload`, `*out = next` — stays unchanged.) When `spliceNarratives` fails, `spliced` is `""`, and writing an empty `-compacted` report is harmless. The raw reply is already on disk as `name-compaction`.

- [ ] **Step 4: Run the package**

Run: `go test ./internal/orchestrator -count=1` → PASS. If a test outside this task fails because its compaction fixture replies with a full dossier, that's expected to keep working (the splice ignores extra keys). Investigate any failure before changing a test.

- [ ] **Step 5: Commit**

```bash
git add internal/orchestrator/thesis_compaction.go internal/orchestrator/thesis_recovery_test.go
git commit -m "Ask compaction for narratives only and measure the spliced result"
```

---

### Task 3: Route compaction to the Chief engine

**Files:**
- Modify: `internal/orchestrator/chief.go` (a purpose constant and `compactionTarget`), `internal/orchestrator/thesis.go:43-55` (`chief` field), `internal/orchestrator/thesis.go:178` (set it), `internal/orchestrator/thesis_compaction.go:319` (use it)
- Modify test runners that can reach compaction: `thesis_schema_test.go:55` (`thesisFixture`), `thesis_response_test.go:404`, `thesis_test.go:157`
- Test: `internal/orchestrator/chief_test.go`

**Interfaces:**
- Produces: `const chiefCompaction chiefPurpose = "compaction"`; the field `thesisRunner.chief chiefEngine`; `func (t *thesisRunner) compactionTarget() callTarget`, which returns `chiefTarget(t.chief, t.cfg, chiefCompaction)`.

- [ ] **Step 1: Write the failing tests**

In `chief_test.go`:

```go
func TestCompactionRunsOnTheChiefEngineNotTheCheapPool(t *testing.T) {
	var chiefModels []string
	var chiefMaxTokens []int
	chief := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model     string `json:"model"`
			MaxTokens int    `json:"max_tokens"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		chiefModels = append(chiefModels, req.Model)
		chiefMaxTokens = append(chiefMaxTokens, req.MaxTokens)
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
			"message": map[string]string{"content": fenced(map[string]any{"long_case": "short"})}}}})
	}))
	defer chief.Close()

	d := supportedResearch().Dossier
	d.ContractVersion = 2
	d.LongCase = strings.Repeat("long narrative ", 2000)
	cheapSawCompaction := 0
	// The fixture server is the cheap engine: it answers research with the
	// oversized dossier and must never be asked to compact it.
	runner, _, done := thesisFixture(t, func(prompt string, _ int) string {
		if strings.Contains(prompt, "Compact this complete dossier") {
			cheapSawCompaction++
		}
		return fenced(d)
	})
	defer done()
	runner.chief = chiefEngine{CLI: model.CLIApi, Model: "chief-model", API: model.APIConfig{BaseURL: chief.URL, Model: "chief-model", APIKey: "k2", MaxTokens: 32768}}

	var out model.CandidateDossier
	if _, _, _, err := researchCall(context.Background(), runner, "thesis-researcher", "routed", "data", &out, dossierSchema); err != nil {
		t.Fatal(err)
	}
	if cheapSawCompaction != 0 {
		t.Fatalf("the cheap engine received %d compaction prompts", cheapSawCompaction)
	}
	if len(chiefModels) != 1 || chiefModels[0] != "chief-model" || chiefMaxTokens[0] != 32768 {
		t.Fatalf("compaction did not reach the Chief engine with its own model and cap: models=%v max_tokens=%v", chiefModels, chiefMaxTokens)
	}
	if out.LongCase != "short" {
		t.Fatalf("the Chief's narrative was not spliced in: %q", out.LongCase)
	}
}

func TestCompactionTargetFollowsTheResolvedChiefEngine(t *testing.T) {
	cfg := Config{SynthesisMaxAttempts: 1}
	cfg.Timeouts.Synthesis = 90 * time.Second
	r := &thesisRunner{cfg: cfg, chief: chiefEngine{CLI: model.CLIClaude, Model: "opus", Binary: "claude"}}
	got := r.compactionTarget()
	if got.CLI != model.CLIClaude || got.throttled || got.Stage != model.StageSynthesis || got.Timeout != 90*time.Second || got.Retry.MaxAttempts != 1 {
		t.Fatalf("claude chief: %+v", got)
	}
}

func TestCompactionOutputLimitIsNotRetried(t *testing.T) {
	calls := 0
	chief := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
			"message": map[string]string{"content": "```json\n{\"long_case\":\"trunc"}, "finish_reason": "length"}}})
	}))
	defer chief.Close()
	d := supportedResearch().Dossier
	d.ContractVersion = 2
	d.LongCase = strings.Repeat("long narrative ", 2000)
	runner, _, done := thesisFixture(t, func(string, int) string { return fenced(d) })
	defer done()
	runner.chief = chiefEngine{CLI: model.CLIApi, Model: "chief-model", API: model.APIConfig{BaseURL: chief.URL, Model: "chief-model", APIKey: "k2"}}
	runner.cfg.SynthesisMaxAttempts = 3
	var out model.CandidateDossier
	reports, _, _, err := researchCall(context.Background(), runner, "thesis-researcher", "trunc", "data", &out, dossierSchema)
	if err == nil || calls != 1 || reports[1].FailureKind != "output_limit" {
		t.Fatalf("calls=%d err=%v recovery=%+v", calls, err, reports[1])
	}
}
```

Add any imports `chief_test.go` lacks (`net/http`, `net/http/httptest`, `strings`, `time`).

- [ ] **Step 2: Run to confirm they fail**

Run: `go test ./internal/orchestrator -run 'TestCompaction(RunsOn|Target|OutputLimit)' -count=1`
Expected: compile failure, `runner.chief undefined` / `compactionTarget undefined`.

- [ ] **Step 3: Implement**

`chief.go`, in the `chiefPurpose` block:

```go
	// chiefCompaction is the one repair of an oversized research dossier. It
	// runs on the Chief engine because the cheap model measurably cannot make
	// the cut: 15 recorded deepseek-chat compactions landed at 0.61–0.99 of
	// their narrative size regardless of the requested budget, while the same
	// prompt on the Chief model met 12/12 per-field budgets (2026-09-23,
	// docs/plans/2026-09-23-compaction-on-chief-engine.md).
	chiefCompaction chiefPurpose = "compaction"
```

`chief.go`, after `chiefTarget`:

```go
// compactionTarget is the dossier-compaction call's target: the run's own
// resolved Chief engine, with the Chief's stage, timeout and retry budget —
// never the cheap pool, whose model cannot perform the cut.
func (t *thesisRunner) compactionTarget() callTarget {
	return chiefTarget(t.chief, t.cfg, chiefCompaction)
}
```

`thesis.go`: add `chief chiefEngine` to `thesisRunner`, and `chief: chiefE,` to the literal at `:178`.

`thesis_compaction.go:319`: replace `t.cheapTarget()` with `t.compactionTarget()`.

Test runners: give `thesisFixture` (`thesis_schema_test.go:55`) a Chief on the same fixture server, so every existing compaction test keeps its single reply function:

```go
		chief: chiefEngine{CLI: model.CLIApi, Model: "fixture-chief", API: model.APIConfig{BaseURL: srv.URL, Model: "fixture-chief", APIKey: "fixture"}},
```

Do the same at `thesis_response_test.go:404` and `thesis_test.go:157`, using that test's own server URL. The `chief_test.go` literals at `:157`, `:273` and `:304` don't reach compaction; leave them alone.

- [ ] **Step 4: Run the whole suite**

Run: `go test ./... -count=1` → all 13 packages `ok`. `TestThesisAllFailedSkipsChiefAndPersistsDegradedResults` must pass without being edited.

- [ ] **Step 5: Commit**

```bash
git add internal/orchestrator/chief.go internal/orchestrator/thesis.go internal/orchestrator/thesis_compaction.go internal/orchestrator/*_test.go
git commit -m "Run dossier compaction on the Chief engine"
```

---

### Task 4: Documentation

**Files:** `CLAUDE.md` (the Cost-split bullet at ~:72-81), `docs/workflow/thesis-research.md` (the compaction paragraph at ~:471-500), `cfr.toml.example` (the comment above `#[research.budgets.researcher]`).

- [ ] **Step 1: CLAUDE.md** — append to the Cost-split bullet:

```markdown
  The second reviewed exception is dossier compaction: the one repair of an
  oversized thesis dossier runs on the selected Chief engine, because the cheap
  model measurably cannot make the cut (15 recorded compactions at 0.61–0.99 of
  narrative size whatever was asked; the Chief model met 12/12 per-field
  budgets). It fires at most once per dossier and only when one is over budget,
  and the model returns only the twelve narrative fields — Go splices them into
  the original payload.
```

- [ ] **Step 2: thesis-research.md** — in the compaction paragraph, after "The model is given the resulting per-field byte allocations directly — never a fixed character count", insert:

```markdown
The compaction call runs on the configured Chief engine, not the cheap engine,
and returns only the twelve narrative fields. Go splices them into the original
payload's own bytes, so protected fields are identical by construction; fields
the reply omits keep their original text, and anything else the reply contains
is ignored. The spliced payload is then measured against the limit in Go and
still passes the protected-field comparison.
```

Then replace "Go compares them before accepting the compacted response." with "Go compares them on the spliced payload before accepting it."

- [ ] **Step 3: cfr.toml.example** — add above `#[research.budgets.researcher]`:

```toml
# An oversized researcher dossier gets one compaction, run on the Chief engine
# ([chief_api] or the claude CLI), never the cheap engine.
```

- [ ] **Step 4: Verify and commit**

```bash
go build ./... && go vet ./... && gofmt -l . && git diff --check && go test ./... -count=1
git add CLAUDE.md docs/workflow/thesis-research.md cfr.toml.example
git commit -m "Document compaction on the Chief engine"
```

---

## Live verification (after Task 4, operator-approved; uses metered calls)

1. `go build -o cfr ./cmd/cfr` (the `/cfr` binary path is gitignored).
2. Re-run the stopped case: `timeout -s INT 10m ./cfr run --research-mode thesis --ticker STLAM.MI --json`. Inspect it with the same stop rules. Also check that the compaction's `usage` shows `finish_reason: stop`, and that `-compacted` holds the spliced dossier.
3. If it passes, continue the runbook's fixed order: NOKIA.HE, 2330.TW, 9988.HK, BHP.AX. Then run the full independent thesis run with a 20-minute deadline, and analyse it.
4. If v4-pro still exhausts its token cap on a narratives-only reply, stop. That becomes a new diagnosis, not another tweak.

## Known, separate finding (not in this plan)

Expected coverage notes, such as a foreign private issuer having no Form 4 leg, are counted as run errors, so every foreign listing ends `degraded` even when every call succeeds (ASML.AS, `runs/2026-09-23T14-22-22`). This is tracked for after acceptance.
