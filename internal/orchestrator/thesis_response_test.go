package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/mamut/claude-financial-researcher/internal/agents"
	"github.com/mamut/claude-financial-researcher/internal/marketdata"
	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/store"
)

// syntheticDossierOfCompactSize builds a fenced ```json response whose
// extracted payload compacts (via json.Compact) to exactly compactWant bytes,
// and whose complete raw response (fence markers included) is exactly
// rawWant bytes. It pads a "filler" string field on a schema-valid dossier to
// hit the compact target, then inserts interior whitespace immediately after
// the payload's opening brace — insignificant to json.Compact, so it widens
// only the raw measurement — to hit the raw target.
//
// Both counts are asserted here, against measureResponse itself, so a
// miscalibrated fixture fails loudly in the helper rather than making a
// caller's test vacuously pass or fail on the wrong axis.
func syntheticDossierOfCompactSize(t *testing.T, compactWant, rawWant int) string {
	t.Helper()
	const fence = "```json\n"
	const closeFence = "\n```"
	fenceOverhead := len(fence) + len(closeFence)

	d := supportedResearch().Dossier
	base, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal base dossier: %v", err)
	}
	var obj map[string]any
	if err := json.Unmarshal(base, &obj); err != nil {
		t.Fatalf("unmarshal base dossier: %v", err)
	}

	obj["filler"] = ""
	empty, err := json.Marshal(obj)
	if err != nil {
		t.Fatalf("marshal with empty filler: %v", err)
	}
	need := compactWant - len(empty)
	if need < 0 {
		t.Fatalf("compact target %d is smaller than the base dossier's %d bytes (with empty filler)", compactWant, len(empty))
	}
	obj["filler"] = strings.Repeat("x", need)
	compact, err := json.Marshal(obj)
	if err != nil {
		t.Fatalf("marshal with sized filler: %v", err)
	}
	if len(compact) != compactWant {
		t.Fatalf("compact calibration: got %d bytes, want %d", len(compact), compactWant)
	}

	innerPad := rawWant - fenceOverhead - compactWant
	if innerPad < 0 {
		t.Fatalf("raw target %d cannot fit compact target %d plus %d bytes of fence overhead", rawWant, compactWant, fenceOverhead)
	}
	// Insert the padding immediately after the opening brace. Whitespace there
	// is insignificant to json.Compact but counts toward the raw response.
	padded := string(compact[:1]) + strings.Repeat(" ", innerPad) + string(compact[1:])

	stdout := fence + padded + closeFence
	if len(stdout) != rawWant {
		t.Fatalf("raw calibration: got %d bytes, want %d", len(stdout), rawWant)
	}

	m := measureResponse(stdout)
	if m.PayloadBytes != compactWant || m.RawBytes != rawWant {
		t.Fatalf("measureResponse calibration mismatch: payload=%d raw=%d, want %d/%d", m.PayloadBytes, m.RawBytes, compactWant, rawWant)
	}
	if !m.Normalized {
		t.Fatalf("synthetic dossier failed to normalize: %+v", m)
	}
	return stdout
}

// Step 1: the boundary test from the brief. LLY is the September 15 run's
// LLY round-2-compaction artifact (raw 20,493 / compact 20,326 against the
// 20,480 limit) — a compaction result that already fit the budget and was
// rejected anyway because the old gate measured raw bytes including 155
// bytes of formatting whitespace plus the fence. BAC (raw 21,289 / compact
// 20,894) is genuinely oversized on payload bytes too, and must still fail.
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

// Step 2: json.Compact is byte-preserving for string contents and numeric
// spellings; decoding into model.CandidateDossier and re-emitting is not.
// This is what stops a future refactor from reaching for the typed
// round-trip that the losslessness constraint forbids.
func TestNormalizationPreservesUnknownFieldsUnicodeEscapesAndBigNumbers(t *testing.T) {
	in := `{"ticker":"9988.HK","unknown_future_field":{"a":[1,2]},
	        "quote":"He said \"no\" — 阿里巴巴 集团",
	        "big":123456789012345678901234567890,"small":1.000,"neg":-0.0}`
	stdout := "```json\n" + in + "\n```"

	m := measureResponse(stdout)
	if !m.Normalized {
		t.Fatalf("expected normalization to succeed on well-formed JSON: %+v", m)
	}

	extracted, ok := extractLastJSON(stdout)
	if !ok {
		t.Fatal("fence extraction failed")
	}
	var compacted bytes.Buffer
	if err := json.Compact(&compacted, []byte(extracted)); err != nil {
		t.Fatalf("json.Compact: %v", err)
	}
	if m.PayloadBytes != compacted.Len() {
		t.Fatalf("PayloadBytes = %d, want %d", m.PayloadBytes, compacted.Len())
	}

	decodeUseNumber := func(s string) any {
		t.Helper()
		dec := json.NewDecoder(strings.NewReader(s))
		dec.UseNumber()
		var v any
		if err := dec.Decode(&v); err != nil {
			t.Fatalf("decode %q: %v", s, err)
		}
		return v
	}
	original := decodeUseNumber(extracted)
	roundTripped := decodeUseNumber(compacted.String())
	if !reflect.DeepEqual(original, roundTripped) {
		t.Fatalf("normalization altered the decoded value:\noriginal:  %#v\ncompacted: %#v", original, roundTripped)
	}

	origMap, ok1 := original.(map[string]any)
	rtMap, ok2 := roundTripped.(map[string]any)
	if !ok1 || !ok2 {
		t.Fatalf("expected both decodes to be objects: %#v / %#v", original, roundTripped)
	}
	const wantQuote = "He said \"no\" — 阿里巴巴 集团"
	if origMap["quote"] != wantQuote {
		t.Fatalf("original quote decoded incorrectly: %q", origMap["quote"])
	}
	if rtMap["quote"] != wantQuote {
		t.Fatalf("compaction changed the quote's bytes: %q", rtMap["quote"])
	}
}

// Step 3a. Modeled on runs/2026-09-15T17-00-30/research-4a4e4a-round-1.md
// (JNJ), which failed json.Compact with "invalid character '\n' in string
// literal" — a raw, unescaped newline inside a JSON string value, in an
// otherwise complete response. runs/ is gitignored and not committed; this is
// a minimal synthetic reproduction of the same shape (cited here rather than
// checked in).
//
// Normalization must not rescue a payload like this into a capacity pass:
// PayloadBytes falls back to RawBytes, and — because the malformed text here
// is far under any response budget — the response proceeds exactly as it did
// before Task 8, into the schema-repair path, never the capacity path.
func TestMalformedPayloadCannotBeNormalizedIntoValidity(t *testing.T) {
	malformed := "```json\n{\"ticker\":\"AAA\",\"note\":\"unterminated\nline\"}\n```"

	m := measureResponse(malformed)
	if m.Normalized {
		t.Fatalf("expected normalization to fail on a raw newline inside a string: %+v", m)
	}
	if m.PayloadBytes != m.RawBytes {
		t.Fatalf("failed normalization must fall back to raw bytes: payload=%d raw=%d", m.PayloadBytes, m.RawBytes)
	}
	if m.RawBytes != len(malformed) {
		t.Fatalf("RawBytes = %d, want %d", m.RawBytes, len(malformed))
	}

	calls, repairs := 0, 0
	runner, _, done := thesisFixture(t, func(prompt string, _ int) string {
		calls++
		if strings.Contains(prompt, "formatting correction only") {
			repairs++
			return fenced(supportedResearch().Dossier)
		}
		return malformed
	})
	defer done()

	var out model.CandidateDossier
	reports, transport, parsing, err := researchCall(context.Background(), runner, "thesis-researcher", "malformed", "data", &out, dossierSchema)
	if err != nil {
		t.Fatalf("schema repair should have recovered the malformed payload: %v", err)
	}
	if calls != 2 || repairs != 1 {
		t.Fatalf("expected exactly one schema-repair call, got calls=%d repairs=%d", calls, repairs)
	}
	if transport != model.OutcomeOK || parsing != model.OutcomeRepaired {
		t.Fatalf("transport=%s parsing=%s, want ok/repaired", transport, parsing)
	}
	for _, rep := range reports {
		if rep.FailureKind == "response_capacity" {
			t.Fatalf("malformed payload was wrongly treated as a capacity failure: %+v", reports)
		}
	}
}

// Step 3b. A completion cut off at the provider's own token limit
// (finish_reason=="length") fails transport in runner.go before
// responseCapacity ever runs — regardless of how small its (necessarily
// incomplete) JSON prefix would compact to, it must never be read as a
// capacity verdict, and it must not buy a repair call: it is not a complete
// response.
func TestTruncatedCompletionGetsNoNormalizationBenefit(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{
			map[string]any{
				// Deliberately tiny and incomplete: even a payload that would
				// compact far under the limit must not be treated as fitting.
				"message":       map[string]string{"content": `{"ticker":"AAA"`},
				"finish_reason": "length",
			},
		}})
	}))
	defer srv.Close()

	cfg := Config{ResearchMode: "thesis", CheapEngine: model.CLIApi, AgentsDir: "../../agents",
		API:     model.APIConfig{BaseURL: srv.URL, Model: "fixture", APIKey: "fixture"},
		DataDir: t.TempDir(), RunsDir: t.TempDir()}
	cfg.applyDefaults()
	reg, err := agents.Load(cfg.AgentsDir)
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.New(cfg.RunsDir)
	if err != nil {
		t.Fatal(err)
	}
	pool := newPool(1, nil, nil, cfg.API, model.CLIApi, 0)
	ctx, cancel := context.WithCancel(context.Background())
	pool.start(ctx)
	defer func() { cancel(); pool.stop() }()
	runner := &thesisRunner{cfg: cfg, ch: make(chan Event, 200), run: run, reg: reg, pool: pool,
		cheap: model.CLIApi, svc: marketdata.NewService(nil, researchFixtureProvider{}), sources: map[string][]string{}}

	var out model.CandidateDossier
	reports, transport, _, callErr := researchCall(context.Background(), runner, "thesis-researcher", "truncated", "data", &out, dossierSchema)
	if calls != 1 {
		t.Fatalf("expected exactly one attempt, got %d", calls)
	}
	if callErr == nil {
		t.Fatal("a truncated completion must fail")
	}
	if transport != model.OutcomeFailed {
		t.Fatalf("transport = %s, want failed", transport)
	}
	if len(reports) != 1 || reports[0].FailureKind != "output_limit" {
		t.Fatalf("reports = %+v, want a single output_limit failure", reports)
	}
	if reports[0].Recovery != "" {
		t.Fatalf("a truncated completion bought a recovery call: %+v", reports[0])
	}
}

// Step 3c. The one-repair-allowance rule must never chain: a response that
// took a schema repair and whose repair came back oversized gets no
// compaction (compaction is only ever attempted against the ORIGINAL call's
// response_capacity failure, never against a repair's own report); and a
// response that took a compaction and whose compaction came back malformed
// gets no schema repair (compactDossier decodes its own result directly and
// never calls into decodeOrRepair).
func TestSchemaRepairAndCompactionNeverChain(t *testing.T) {
	t.Run("repaired_then_oversized_buys_no_compaction", func(t *testing.T) {
		calls := 0
		runner, _, done := thesisFixture(t, func(prompt string, _ int) string {
			calls++
			if strings.Contains(prompt, "formatting correction only") {
				// The repair "fixes" the shape but comes back oversized.
				return fenced(map[string]any{"ticker": "AAA", "filler": strings.Repeat("x", 25000)})
			}
			// The original response is malformed (small), forcing a repair.
			return "not a json object at all"
		})
		defer done()

		var out model.CandidateDossier
		reports, _, _, err := researchCall(context.Background(), runner, "thesis-researcher", "chain-a", "data", &out, dossierSchema)
		if err == nil {
			t.Fatal("an oversized repair must still fail the call")
		}
		if calls != 2 {
			t.Fatalf("expected exactly one repair attempt (no chained compaction), got %d calls", calls)
		}
		for _, rep := range reports {
			if rep.Recovery == "compaction" {
				t.Fatalf("schema repair chained into compaction: %+v", reports)
			}
		}
	})

	t.Run("compacted_then_malformed_buys_no_repair", func(t *testing.T) {
		d := supportedResearch().Dossier
		d.LongCase = strings.Repeat("long narrative ", 2000) // forces response_capacity
		calls := 0
		runner, _, done := thesisFixture(t, func(prompt string, _ int) string {
			calls++
			if strings.Contains(prompt, "Compact this complete dossier") {
				return "not a json object at all"
			}
			return fenced(d)
		})
		defer done()

		var out model.CandidateDossier
		reports, _, _, err := researchCall(context.Background(), runner, "thesis-researcher", "chain-b", "data", &out, dossierSchema)
		if err == nil {
			t.Fatal("a malformed compaction must still fail the call")
		}
		if calls != 2 {
			t.Fatalf("expected exactly one compaction attempt (no chained repair), got %d calls", calls)
		}
		for _, rep := range reports {
			if rep.Recovery == "schema_repair" {
				t.Fatalf("compaction chained into schema repair: %+v", reports)
			}
		}
	})
}

// Step 3d. Once a response's payload bytes fit the budget, recognizing that
// costs zero model calls: local normalization (json.Compact) never dispatches
// anything. Shaped like the LLY compaction-round artifact from the September
// 15 run (compact 20,326 / raw 20,493 against the 20,480 limit) — that
// response had already survived one upstream compaction call by the time it
// reached this shape; what this test pins is that THIS call, seeing a
// response whose payload already fits, does not spend a further one.
func TestLocalNormalizationConsumesNoModelCall(t *testing.T) {
	lly := syntheticDossierOfCompactSize(t, 20326, 20493)
	calls := 0
	runner, _, done := thesisFixture(t, func(prompt string, _ int) string {
		calls++
		return lly
	})
	defer done()

	var out model.CandidateDossier
	reports, transport, parsing, err := researchCall(context.Background(), runner, "thesis-researcher", "lly-shaped", "data", &out, dossierSchema)
	if err != nil {
		t.Fatalf("a payload-fitting response should not fail: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected zero recovery calls (one dispatch total), got %d", calls)
	}
	if len(reports) != 1 || reports[0].Recovery != "" {
		t.Fatalf("unexpected recovery attempt: %+v", reports)
	}
	if transport != model.OutcomeOK || parsing != model.OutcomeOK {
		t.Fatalf("transport=%s parsing=%s, want ok/ok", transport, parsing)
	}
}
