package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/model"
)

func TestSeptember13DossiersReachResearchContract(t *testing.T) {
	files, err := filepath.Glob("testdata/sep13-dossiers/*.md")
	if err != nil {
		t.Fatal(err)
	}
	tested, compacted := 0, 0
	for _, file := range files {
		if filepath.Base(file) == "README.md" {
			continue
		}
		t.Run(filepath.Base(file), func(t *testing.T) {
			raw, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			var original model.CandidateDossier
			if err := decodeResearch(string(raw), &original); err != nil {
				t.Fatal(err)
			}
			calls := 0
			runner, _, done := thesisFixture(t, func(prompt string, _ int) string {
				calls++
				if strings.Contains(prompt, "Compact this complete dossier") {
					var value map[string]any
					if err := decodeResearch(string(raw), &value); err != nil {
						t.Fatal(err)
					}
					for _, field := range dossierNarrativeFields {
						value[field] = "Concise synthetic narrative retaining uncertainty."
					}
					return fenced(value)
				}
				return string(raw)
			})
			defer done()
			var out model.CandidateDossier
			reports, transport, parsing, err := researchCall(context.Background(), runner, "thesis-researcher", "captured", "Captured response regression", &out, dossierSchema)
			if err != nil || transport != model.OutcomeOK || parsing != model.OutcomeOK {
				t.Fatalf("dossier discarded: transport=%s parsing=%s err=%v", transport, parsing, err)
			}
			if !reflect.DeepEqual(original.Claims, out.Claims) || !reflect.DeepEqual(original.Requests, out.Requests) || original.Status != out.Status || out.PreferredDirection != "NONE" {
				t.Fatal("recovery changed findings or skipped research requests")
			}
			if original.Ticker == "SAN.PA" {
				compacted++
				if calls != 2 || len(reports) != 2 || reports[1].Contract != "compacted" || len(reports[1].OriginalNarratives) != 12 {
					t.Fatalf("missing bounded compaction provenance: %+v", reports)
				}
			} else if calls != 1 || len(reports[0].WritingDiagnostics) == 0 {
				t.Fatal("writing targets triggered a repair or were not measured")
			}
			tested++
		})
	}
	if tested != 12 || compacted != 1 {
		t.Fatalf("tested=%d compacted=%d", tested, compacted)
	}
}

func TestWritingTargetsDoNotEraseCompleteDossiers(t *testing.T) {
	for _, length := range []int{400, 401} {
		d := supportedResearch().Dossier
		d.LongCase = strings.Repeat("界", length)
		d.Claims[0].Passages[0].Quote = strings.Repeat("é", 301)
		if problems := dossierSchema(&d); len(problems) != 0 {
			t.Fatal(problems)
		}
		if len(dossierWritingDiagnostics(&d)) == 0 {
			t.Fatal("missing length diagnostics")
		}
	}
}

func TestCompactionProtectsAllNonNarrativeFields(t *testing.T) {
	d := supportedResearch().Dossier
	d.EntryConditions = []string{"Confirm executable quote"}
	d.Monitoring = []string{"Monitor future deliveries"}
	d.Unresolved = []string{"Missing filing"}
	d.Events = []model.ResearchEvent{{Kind: "earnings", OccurredAt: "2026-09-01T12:00:00Z", EvidenceID: "source", Passage: "Issuer date"}}
	var original map[string]any
	if err := json.Unmarshal([]byte(jsonText(d)), &original); err != nil {
		t.Fatal(err)
	}
	original["future_extension"] = map[string]any{"uncertainty": "must survive"}
	before := fenced(original)
	for _, field := range []string{"claims", "unresolved", "entry_conditions", "monitoring", "events", "status", "future_extension", "expectations_claim_ids"} {
		t.Run(field, func(t *testing.T) {
			var value map[string]any
			if err := decodeResearch(before, &value); err != nil {
				t.Fatal(err)
			}
			value[field] = "changed"
			if compactionPreservesEvidence(before, fenced(value)) == nil {
				t.Fatal("protected field changed")
			}
		})
	}
	original["long_case"] = "Shortened narrative; uncertainty remains."
	if err := compactionPreservesEvidence(before, fenced(original)); err != nil {
		t.Fatal(err)
	}
	original["counterargument"] = ""
	if compactionPreservesEvidence(before, fenced(original)) == nil {
		t.Fatal("erased counterargument")
	}
}

func TestCompactionFailureDoesNotBuyAnotherRepair(t *testing.T) {
	for _, failure := range []string{"oversize", "malformed", "changed_status"} {
		t.Run(failure, func(t *testing.T) {
			d := supportedResearch().Dossier
			d.ContractVersion = 2
			d.LongCase = strings.Repeat("long narrative ", 2000)
			calls := 0
			runner, _, done := thesisFixture(t, func(prompt string, _ int) string {
				calls++
				if !strings.Contains(prompt, "Compact this complete dossier") || failure == "oversize" {
					return fenced(d)
				}
				if failure == "malformed" {
					return "incomplete response"
				}
				next := d
				next.LongCase = "short"
				next.Status = "rejected"
				return fenced(next)
			})
			defer done()
			var out model.CandidateDossier
			reports, transport, parsing, err := researchCall(context.Background(), runner, "thesis-researcher", "failed", "data", &out, dossierSchema)
			if err == nil || calls != 2 || transport != model.OutcomeOK || parsing != model.OutcomeOK || out.Ticker != "" {
				t.Fatalf("failure hidden or retried: calls=%d transport=%s parsing=%s err=%v", calls, transport, parsing, err)
			}
			r := thesisResearch{}
			r.addResearchReports(reports)
			if r.Outcome.Contract != model.OutcomeFailed || r.Outcome.CompactionAttempts != 1 || !r.researchFailed() {
				t.Fatalf("failure provenance lost: %+v", r.Outcome)
			}
		})
	}
}

// TestCompactionCancellationAndInputBudgetPreventDispatch pins two
// independent reasons compactDossier must never dispatch: an
// already-cancelled context (t.call's own ctx.Err() guard, thesis.go) and an
// assembled prompt that exceeds the researcher role's input budget
// (preparePrompt's own guard, thesis_budget.go, reached through
// compactDossier — TestCompactionInputCapacityIsCheckedBeforeDispatch pins
// that path further). Each case uses a raw shaped so ONLY the mechanism
// under test can be the reason for zero dispatch: an ordinary small dossier
// for cancellation (so the input-budget guard would never fire on its own),
// and a huge one for the budget case (with an uncancelled context, so
// cancellation cannot be why it did not dispatch). Sharing one huge raw
// across both cases — as an earlier version of this test did — let the
// input-budget guard fire in both branches regardless of context state,
// which meant the "cancelled" branch never actually exercised cancellation;
// a mutation that disabled t.call's ctx.Err() check confirmed this: the test
// passed regardless (see the task report's mutation matrix).
func TestCompactionCancellationAndInputBudgetPreventDispatch(t *testing.T) {
	small := fenced(supportedResearch().Dossier)
	huge := supportedResearch().Dossier
	huge.LongCase = strings.Repeat("界", 100000)
	// A huge NARRATIVE field (long_case) alone must not trip
	// measureCompaction's feasibility gate — the point of compaction is to
	// shrink exactly this kind of field, and the protected floor
	// (everything except the twelve narrative fields) stays small.
	hugeRaw := fenced(huge)

	cases := []struct {
		name      string
		cancelled bool
		raw       string
	}{
		{"cancelled", true, small},
		{"over_input_budget", false, hugeRaw},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			calls := 0
			runner, _, done := thesisFixture(t, func(string, int) string { calls++; return "unexpected" })
			defer done()
			ctx, cancel := context.WithCancel(context.Background())
			if c.cancelled {
				cancel()
			}
			var out model.CandidateDossier
			r, err := compactDossier(ctx, runner, "thesis-researcher", "bounded", c.raw, 20480, &out)
			cancel()
			if err == nil || calls != 0 || r.Attempts != 0 {
				t.Fatal("cancelled/over-budget compaction dispatched")
			}
		})
	}
}

// syntheticCompactionRaw builds a fenced dossier whose PROTECTED content
// (everything but the twelve narrative fields) compacts to exactly
// wantProtected bytes, and whose twelve narrative fields together add
// exactly wantNarrative more bytes (plain ASCII "a", so no escaping makes
// the JSON-quoted delta diverge from the raw byte count) — matching how
// task-9-context.md defines protected/narrative/compact for the real Sept
// 15 originals ("protected + narrative = compact exactly"). It calibrates by
// measurement (marshal, measure, pad a "filler" protected field to the
// target) rather than by formula, so a change to the base dossier shape
// cannot silently drift the fixture off its target.
func syntheticCompactionRaw(t *testing.T, wantProtected, wantNarrative int) string {
	t.Helper()
	d := supportedResearch().Dossier
	d.ContractVersion = 2
	ids := []string{d.Claims[0].ID}
	d.ExpectationsClaimIDs, d.PricedInClaimIDs = ids, ids
	base, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]any
	if err := json.Unmarshal(base, &obj); err != nil {
		t.Fatal(err)
	}

	n := len(dossierNarrativeFields)
	per := wantNarrative / n
	rem := wantNarrative - per*n
	for i, f := range dossierNarrativeFields {
		sz := per
		if i == 0 {
			sz += rem
		}
		if sz < 0 {
			t.Fatalf("narrative target %d cannot be spread across %d fields", wantNarrative, n)
		}
		obj[f] = strings.Repeat("a", sz)
	}
	obj["filler"] = ""

	// Measure the protected floor with the real narrative content already in
	// place but filler still empty, then pad filler (itself a protected,
	// unknown field) by exactly the shortfall. Padding a field OTHER than
	// the twelve narrative ones keeps wantNarrative exact regardless of how
	// much padding wantProtected needs.
	zeroed := map[string]any{}
	for k, v := range obj {
		zeroed[k] = v
	}
	for _, f := range dossierNarrativeFields {
		zeroed[f] = ""
	}
	baseline, err := json.Marshal(zeroed)
	if err != nil {
		t.Fatal(err)
	}
	need := wantProtected - len(baseline)
	if need < 0 {
		t.Fatalf("base dossier structure (%d bytes) already exceeds the target protected size %d", len(baseline), wantProtected)
	}
	obj["filler"] = strings.Repeat("f", need)

	full, err := json.Marshal(obj)
	if err != nil {
		t.Fatal(err)
	}
	return "```json\n" + string(full) + "\n```"
}

// TestCompactionRefusesADoomedCallWhenProtectedContentAlreadyExceeds pins
// Step 1 / controller amendment behavior: when the PROTECTED content alone
// (claims, quotations — everything a compaction call may never touch)
// already compacts past the limit, no rewrite of the twelve narrative
// fields can rescue the response, and the call must never be dispatched.
func TestCompactionRefusesADoomedCallWhenProtectedContentAlreadyExceeds(t *testing.T) {
	const limit = 20480
	// Protected content alone (25,000 bytes) already exceeds the limit;
	// narrative content is zero, so there is nothing a rewrite could do.
	raw := syntheticCompactionRaw(t, 25000, 0)

	calls := 0
	runner, _, done := thesisFixture(t, func(string, int) string { calls++; return "unexpected" })
	defer done()

	var out model.CandidateDossier
	status, err := compactDossier(context.Background(), runner, "thesis-researcher", "doomed", raw, limit, &out)
	if err == nil {
		t.Fatal("a doomed compaction must fail")
	}
	if calls != 0 {
		t.Fatalf("a doomed compaction dispatched %d model call(s), want 0", calls)
	}
	if status.Attempts != 0 {
		t.Fatalf("Attempts = %d, want 0", status.Attempts)
	}
	if status.Contract != model.OutcomeFailed {
		t.Fatalf("Contract = %q, want failed", status.Contract)
	}
	if status.Recovery != "compaction" {
		t.Fatalf("Recovery = %q, want compaction", status.Recovery)
	}
	if status.Allowance == nil || status.Allowance.Feasible {
		t.Fatalf("Allowance not recorded as infeasible: %+v", status.Allowance)
	}
	if !strings.Contains(status.Err, "protected_bytes") || !strings.Contains(status.Err, fmt.Sprint(limit)) {
		t.Fatalf("error does not name protected_bytes and the limit: %q", status.Err)
	}
}

// TestCompactionAllowanceIsMeasuredNotAssumed pins Step 1: the narrative
// budget is computed from a measured protected floor, not from a fixed
// 12*400-character assumption. It uses BAC's real controller-measured
// numbers from task-9-context.md's first table (compact 22,076 / protected
// 14,756 / narrative 7,320), NOT the brief's own illustrative "protected
// 19,900" figure — that number could not be reproduced by direct
// remeasurement of the real artifact (see the task report) and is not used
// here, so as not to assert something as measured that was not.
func TestCompactionAllowanceIsMeasuredNotAssumed(t *testing.T) {
	const limit = 20480
	const wantProtected = 14756
	const wantNarrative = 7320
	raw := syntheticCompactionRaw(t, wantProtected, wantNarrative)

	a, err := measureCompaction(raw, limit)
	if err != nil {
		t.Fatal(err)
	}
	if a.ProtectedBytes != wantProtected {
		t.Fatalf("ProtectedBytes = %d, want %d", a.ProtectedBytes, wantProtected)
	}
	if a.PayloadBytes != wantProtected+wantNarrative {
		t.Fatalf("PayloadBytes = %d, want %d", a.PayloadBytes, wantProtected+wantNarrative)
	}
	wantBudget := limit - wantProtected - compactionHeadroom
	if a.NarrativeBudget != wantBudget {
		t.Fatalf("NarrativeBudget = %d, want %d (limit - protected - headroom)", a.NarrativeBudget, wantBudget)
	}
	// task-9-context.md's real BAC row: budget 5,468. Assert the real number,
	// not the brief's own illustrative "~324" derived from a protected floor
	// that could not be reproduced.
	if wantBudget != 5468 {
		t.Fatalf("sanity: expected the real controller-measured BAC budget 5468, computed %d", wantBudget)
	}
	if a.NarrativeBudget == 12*400 {
		t.Fatal("narrative budget must be measured, not the old fixed 12*400-character assumption")
	}
	if !a.Feasible {
		t.Fatalf("BAC's real numbers are feasible (budget %d > 0); got infeasible: %+v", wantBudget, a)
	}
	sum := 0
	for _, v := range a.PerField {
		sum += v
	}
	if sum > a.NarrativeBudget {
		t.Fatalf("sum(PerField) = %d exceeds NarrativeBudget %d", sum, a.NarrativeBudget)
	}
}

// TestPerFieldAllocationIsProportionalWithAFloorForNonEmptyFields pins Step
// 1/4: every currently non-empty narrative field gets a non-zero (at least
// compactionPerFieldFloor) allocation, every empty field gets zero, larger
// fields get proportionally more, and the total never exceeds the budget.
func TestPerFieldAllocationIsProportionalWithAFloorForNonEmptyFields(t *testing.T) {
	d := supportedResearch().Dossier
	d.ContractVersion = 2
	ids := []string{d.Claims[0].ID}
	d.ExpectationsClaimIDs, d.PricedInClaimIDs = ids, ids
	d.LongCase = strings.Repeat("a", 2000)
	d.ShortCase = strings.Repeat("b", 1000)
	d.Hypothesis = strings.Repeat("c", 200)
	d.NoTradeCase, d.Changed, d.Expectations, d.Underappreciated = "", "", "", ""
	d.Mechanism, d.PricedIn, d.Counterargument, d.Invalidation, d.CatalystWindow = "", "", "", "", ""

	raw := fenced(d)
	a, err := measureCompaction(raw, 20480)
	if err != nil {
		t.Fatal(err)
	}
	if !a.Feasible {
		t.Fatalf("expected a feasible allowance: %+v", a)
	}

	nonEmpty := map[string]bool{"long_case": true, "short_case": true, "hypothesis": true}
	sum := 0
	for _, f := range dossierNarrativeFields {
		v := a.PerField[f]
		sum += v
		switch {
		case nonEmpty[f] && v < compactionPerFieldFloor:
			t.Errorf("%s: allocation = %d, want at least the floor %d", f, v, compactionPerFieldFloor)
		case !nonEmpty[f] && v != 0:
			t.Errorf("%s: allocation = %d, want 0 for a field that was already empty", f, v)
		}
	}
	if sum > a.NarrativeBudget {
		t.Fatalf("sum(PerField) = %d exceeds NarrativeBudget %d", sum, a.NarrativeBudget)
	}
	if !(a.PerField["long_case"] > a.PerField["short_case"] && a.PerField["short_case"] > a.PerField["hypothesis"]) {
		t.Fatalf("allocation is not proportional to current field size: %+v", a.PerField)
	}

	// The floor only has anything to prove when the budget is tight enough
	// that a pure proportional split would starve a tiny non-empty field
	// below it. Build one: long_case dominates (5000 bytes) over a 1-byte
	// short_case, under a budget of exactly 600 — a pure proportional split
	// would give short_case 600*1/5001 = 0 (rounds down), so a non-zero
	// floor result here is the floor mechanism, not proportionality alone
	// (verified by mutation: removing the floor drops this to 1 byte, see
	// the task report's mutation matrix).
	tight := supportedResearch().Dossier
	tight.ContractVersion = 2
	tight.ExpectationsClaimIDs, tight.PricedInClaimIDs = ids, ids
	tight.LongCase = strings.Repeat("a", 5000)
	tight.ShortCase = "b"
	tight.NoTradeCase, tight.Hypothesis, tight.Changed, tight.Expectations, tight.Underappreciated = "", "", "", "", ""
	tight.Mechanism, tight.PricedIn, tight.Counterargument, tight.Invalidation, tight.CatalystWindow = "", "", "", "", ""
	tightRaw := fenced(tight)
	probe, err := measureCompaction(tightRaw, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	const wantBudget = 600
	limit := probe.ProtectedBytes + compactionHeadroom + wantBudget
	b, err := measureCompaction(tightRaw, limit)
	if err != nil {
		t.Fatal(err)
	}
	if b.NarrativeBudget != wantBudget {
		t.Fatalf("calibration: NarrativeBudget = %d, want %d", b.NarrativeBudget, wantBudget)
	}
	if b.PerField["short_case"] < compactionPerFieldFloor {
		t.Fatalf("short_case (1 byte, dwarfed by a 5000-byte long_case) got %d under a tight budget, below the floor %d — the floor is not being applied", b.PerField["short_case"], compactionPerFieldFloor)
	}
	tightSum := 0
	for _, v := range b.PerField {
		tightSum += v
	}
	if tightSum > b.NarrativeBudget {
		t.Fatalf("sum(PerField) = %d exceeds NarrativeBudget %d", tightSum, b.NarrativeBudget)
	}
}

// TestMeasureCompactionProtectedFloorIgnoresHTMLEscaping pins controller
// amendment 1: the protected floor must be measured with HTML escaping OFF.
// All twelve narrative fields are already empty here, so zeroing them is a
// no-op and the ONLY way ProtectedBytes could exceed PayloadBytes is the
// escaping bug — decoding then re-marshaling '<', '&' and '>' (one byte
// each in the original, raw and unescaped, exactly as a model would write
// them) into six-byte \u00XX escapes with the default marshaller.
func TestMeasureCompactionProtectedFloorIgnoresHTMLEscaping(t *testing.T) {
	raw := "```json\n" + `{"contract_version":2,"ticker":"AAA","status":"watchlist","evidence_quality":"mixed",` +
		`"preferred_direction":"NONE","long_case":"","short_case":"","no_trade_case":"","hypothesis":"",` +
		`"changed":"","expectations":"","underappreciated":"","mechanism":"","priced_in":"",` +
		`"counterargument":"","invalidation":"","catalyst_window":"","claims":[],` +
		`"unresolved":["A<B & C>D discussed in filing"],"expectations_claim_ids":[],"priced_in_claim_ids":[]}` +
		"\n```"

	a, err := measureCompaction(raw, 20480)
	if err != nil {
		t.Fatal(err)
	}
	if a.ProtectedBytes > a.PayloadBytes {
		t.Fatalf("protected floor %d exceeds the payload's own compact size %d — HTML escaping inflated it", a.ProtectedBytes, a.PayloadBytes)
	}
}

// TestCompactionInputCapacityIsCheckedBeforeDispatch pins Step 2: the
// compaction prompt embeds the whole original response, and preparePrompt's
// existing len(prompt) > budget.InputBytes check (thesis_budget.go), reached
// through compactDossier, must refuse to dispatch and report
// FailureKind=="input_capacity" with zero attempts — never a generic
// compaction failure. This is a regression pin on an existing mechanism
// (TestCompactionCancellationAndInputBudgetPreventDispatch above already
// exercises it), not a second one; see the task report for confirmation
// that no new capacity-checking code was needed here.
//
// The brief's own illustrative numbers for this case ("a researcher input
// budget of 98,304 and a 23,583-byte original") do not actually overflow:
// measured directly, assembling a 23,583-byte original through the real
// thesis-researcher persona (9,867 bytes) plus this compaction prompt's own
// instructions produces a 35,812-byte prompt, well under 98,304. This test
// therefore uses a realistically larger original to actually exercise the
// check (see the task report for the measurement and the discrepancy).
func TestCompactionInputCapacityIsCheckedBeforeDispatch(t *testing.T) {
	calls := 0
	runner, _, done := thesisFixture(t, func(string, int) string { calls++; return "unexpected" })
	defer done()

	// A huge NARRATIVE field: the protected floor stays small (Feasible must
	// stay true), so this exercises the INPUT check specifically, not the
	// feasibility gate above.
	d := supportedResearch().Dossier
	d.LongCase = strings.Repeat("x", 95000)
	raw := fenced(d)

	var out model.CandidateDossier
	status, err := compactDossier(context.Background(), runner, "thesis-researcher", "oversized-original", raw, 20480, &out)
	if err == nil {
		t.Fatal("an over-input-budget compaction must fail")
	}
	if calls != 0 {
		t.Fatalf("the input-capacity check did not run before dispatch: calls=%d", calls)
	}
	if status.FailureKind != "input_capacity" {
		t.Fatalf("FailureKind = %q, want input_capacity", status.FailureKind)
	}
	if status.Attempts != 0 {
		t.Fatalf("Attempts = %d, want 0", status.Attempts)
	}
}

// TestCompactionFeasibilityAcrossTheSixRealOversizedOriginals pins Step 7:
// the feasibility verdict measureCompaction computes for each of the six
// original (pre-compaction) oversized responses from runs/2026-09-15T17-00-30
// (task-9-context.md's first table, corrected under ruling R17 — NOT
// testdata/sep15-capacity/manifest.json, which records the compaction
// RESULTS per the controller amendment, and would answer a different
// question). It asserts only the arithmetic the controller measured
// directly (every one of the six has a positive narrative budget, so
// Feasible=true for all six) — it does NOT assert that a model would
// actually produce a faithful reduction that fits; that is a semantic
// question the plan explicitly forbids asserting here.
func TestCompactionFeasibilityAcrossTheSixRealOversizedOriginals(t *testing.T) {
	const limit = 20480
	cases := []struct {
		ticker               string
		protected, narrative int
	}{
		{"LLY", 14883, 6000},
		{"BAC", 14756, 7320},
		{"REGN", 15343, 7101},
		{"NOKIA.HE", 15545, 7283},
		{"TTD", 16476, 6388},
		{"9988.HK", 16341, 6808},
	}
	for _, c := range cases {
		t.Run(c.ticker, func(t *testing.T) {
			raw := syntheticCompactionRaw(t, c.protected, c.narrative)
			a, err := measureCompaction(raw, limit)
			if err != nil {
				t.Fatal(err)
			}
			if a.ProtectedBytes != c.protected {
				t.Fatalf("ProtectedBytes = %d, want %d", a.ProtectedBytes, c.protected)
			}
			if a.PayloadBytes != c.protected+c.narrative {
				t.Fatalf("PayloadBytes = %d, want %d", a.PayloadBytes, c.protected+c.narrative)
			}
			wantBudget := limit - c.protected - compactionHeadroom
			if a.NarrativeBudget != wantBudget {
				t.Fatalf("NarrativeBudget = %d, want %d", a.NarrativeBudget, wantBudget)
			}
			t.Logf("%s: protected=%d narrative=%d budget=%d feasible=%v", c.ticker, a.ProtectedBytes, c.narrative, a.NarrativeBudget, a.Feasible)
			if !a.Feasible {
				t.Errorf("%s: computed infeasible; the controller's own measurement is that every one of the six has a positive budget (%d)", c.ticker, wantBudget)
			}
		})
	}
}

func TestConditionsRequireReviewAndDoNotHideCoreEvidenceGaps(t *testing.T) {
	r := supportedResearch()
	r.Dossier.ContractVersion = 2
	r.Dossier.ExpectationsClaimIDs, r.Dossier.PricedInClaimIDs = []string{"c1"}, []string{"c1"}
	r.Dossier.EntryConditions = []string{"Confirm the executable quote before entry"}
	r.Dossier.Monitoring = []string{"Monitor next month's delivery announcement"}
	validateDossier(&r)
	if r.Dossier.Status != "supported" {
		t.Fatalf("future monitoring became missing evidence: %+v", r.Dossier)
	}
	c := model.ThesisChallenge{ContractVersion: 2, DossierHash: dossierHash(r.Dossier), Verdict: "supported", ClaimReviews: []model.ClaimReview{{ClaimID: "c1", Assessment: "supported", Attribution: "confirmed", Reason: "issuer source"}}}
	if len(compactReviewProblems(r.Dossier, c)) == 0 {
		t.Fatal("unreviewed conditions accepted")
	}
	c.ConditionsReviewed = true
	if problems := compactReviewProblems(r.Dossier, c); len(problems) != 0 {
		t.Fatal(problems)
	}
	r.Dossier.Unresolved = []string{"Missing issuer attribution"}
	validateDossier(&r)
	if r.Dossier.Status == "supported" {
		t.Fatal("entry conditions bypassed missing core evidence")
	}
}

func TestLongNarrativeStillRetrievesAndReceivesIndependentReview(t *testing.T) {
	id := fixtureEvidenceID(t)
	d := supportedResearch().Dossier
	d.LongCase = strings.Repeat("Evidence and counterargument remain explicit. ", 12)
	d.Claims[0].EvidenceIDs = []string{id}
	d.Claims[0].Passages[0].EvidenceID = id
	calls := 0
	runner, pack, done := thesisFixture(t, func(prompt string, _ int) string {
		calls++
		if strings.Contains(prompt, "# Independent thesis challenge") {
			return fenced(model.ThesisChallenge{Ticker: "AAA", Verdict: "supported", Reason: "Source supports the mechanism", Claims: d.Claims})
		}
		next := d
		if strings.Contains(prompt, "Round 1/3") {
			next.Requests = []model.ResearchRequest{{Kind: "passage", EvidenceID: id, Query: "Issuer raised guidance", Question: "Verify the delivery statement"}}
		} else if !strings.Contains(prompt, "Passage extracted from "+id) {
			t.Error("retrieval answer missing from next research round")
		}
		return fenced(next)
	})
	defer done()
	out := runner.investigate(context.Background(), model.Candidate{Ticker: "AAA"}, nil, pack, nil)
	if calls != 3 || out.Dossier.Status != "supported" || out.Outcome.Review != "supported" || out.Outcome.RepairAttempts != 0 {
		t.Fatalf("research interrupted: calls=%d outcome=%+v", calls, out.Outcome)
	}
}

func TestCompactionOriginalsReachIndependentChallenge(t *testing.T) {
	id := fixtureEvidenceID(t)
	d := supportedResearch().Dossier
	d.LongCase = "Material qualification: delivery timing remains uncertain. " + strings.Repeat("Repeated explanation. ", 1400)
	d.Claims[0].EvidenceIDs = []string{id}
	d.Claims[0].Passages[0].EvidenceID = id
	reviewed := false
	runner, pack, done := thesisFixture(t, func(prompt string, _ int) string {
		if strings.Contains(prompt, "# Independent thesis challenge") {
			reviewed = true
			if !strings.Contains(prompt, d.LongCase) {
				t.Error("original qualification omitted from independent review")
			}
			return fenced(model.ThesisChallenge{Ticker: "AAA", Verdict: "supported", Reason: "Material qualifications retained", CompactionAssessment: "preserved", Claims: d.Claims})
		}
		if strings.Contains(prompt, "Compact this complete dossier") {
			next := d
			next.LongCase = "Delivery opportunity depends on uncertain timing."
			return fenced(next)
		}
		return fenced(d)
	})
	defer done()
	out := runner.investigate(context.Background(), model.Candidate{Ticker: "AAA"}, nil, pack, nil)
	if !reviewed || out.Dossier.Status != "supported" || out.Outcome.Contract != "compacted" || out.Outcome.CompactionAttempts != 1 {
		t.Fatalf("compaction did not reach review: %+v", out.Outcome)
	}
}

func TestEquivalentDocumentRequestsReuseEvidenceButFutureDatesStayUnavailable(t *testing.T) {
	id := fixtureEvidenceID(t)
	d := supportedResearch().Dossier
	d.Claims[0].EvidenceIDs = []string{id}
	d.Claims[0].Passages[0].EvidenceID = id
	runner, pack, done := thesisFixture(t, func(prompt string, _ int) string {
		if strings.Contains(prompt, "# Independent thesis challenge") {
			return fenced(model.ThesisChallenge{Ticker: "AAA", Verdict: "supported", Reason: "source supports thesis", Claims: d.Claims})
		}
		next := d
		if strings.Contains(prompt, "Round 1/3") {
			next.Requests = []model.ResearchRequest{
				{Kind: "document", URL: "https://ISSUER.example/release#main", Question: "Read existing release"},
				{Kind: "document", URL: "https://issuer.example/release", ObservationDate: "2099-01-01", Question: "Unavailable future release"},
			}
		}
		return fenced(next)
	})
	defer done()
	out := runner.investigate(context.Background(), model.Candidate{Ticker: "AAA"}, nil, pack, nil)
	reused, future := false, false
	for _, r := range out.Results {
		if r.Outcome == model.RequestAlreadyDone && len(r.EvidenceIDs) == 1 && r.EvidenceIDs[0] == id {
			reused = true
		}
		if r.Request.ObservationDate == "2099-01-01" && r.Outcome == model.RequestUnavailable {
			future = true
		}
	}
	if !reused || !future || out.Dossier.Status != "supported" {
		t.Fatalf("request semantics lost: %+v", out.Results)
	}
}

func TestClaudeDisabledSubscriptionStopsWithoutUnchangedRetry(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho 'Your organization has disabled Claude subscription access for Claude Code' >&2\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	r := runAgent(context.Background(), model.CLIClaude, "chief-analyst", "synthesis", "fixture", time.Second, model.RetryPolicy{MaxAttempts: 3}, "", bin, model.APIConfig{})
	if r.Status != model.StatusFailed || r.FailureKind != "authentication" || r.Attempts != 1 || !strings.Contains(r.Err, "disabled Claude subscription access") {
		t.Fatalf("permanent access failure lost or retried: %+v", r)
	}
}
