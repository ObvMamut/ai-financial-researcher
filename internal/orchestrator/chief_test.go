package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/agents"
	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/store"
)

// routingBody is the one shape the routing matrix cares about out of an
// OpenAI-compatible chat completion request: which model and output cap the
// request actually carried, independent of whichever APIConfig produced it.
type routingBody struct {
	Model     string `json:"model"`
	MaxTokens int    `json:"max_tokens"`
}

// routingServer records every request it receives (decoded, not just
// counted) and replies with a fixed, valid completion. Recording the body
// rather than only a hit count is the point: a hit count cannot tell a Chief
// request that reused the cheap engine's model/max_tokens apart from one that
// used its own.
type routingServer struct {
	mu     sync.Mutex
	bodies []routingBody
}

func (s *routingServer) record(b routingBody) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bodies = append(s.bodies, b)
}
func (s *routingServer) snapshot() []routingBody {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]routingBody(nil), s.bodies...)
}
func (s *routingServer) hits() int { return len(s.snapshot()) }

func newRoutingServer(t *testing.T, reply string) (*httptest.Server, *routingServer) {
	t.Helper()
	rec := &routingServer{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body routingBody
		_ = json.NewDecoder(r.Body).Decode(&body)
		rec.record(body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": reply}}}})
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

// TestChiefRoutingMatrix is the regression for the bug Task 4 exists to fix:
// thesis.go used to read the Chief's output cap and dispatch target off the
// cheap-research pool (t.pool.api / pool.submit's per-CLI maps), which is
// silently wrong the moment chief_engine="api" — cheap_engine="api" (or
// "local", itself CLIApi under the hood) and chief_engine="api" both resolve
// to model.CLIApi while pointing at completely different endpoints. Two
// httptest servers with deliberately different models/max_tokens make that
// reuse visible instead of invisible: a hit count alone would not catch it.
//
// legacy's two Chief dispatch sites (orchestrator.go, post-Task-4) are
// `chiefTarget(...)` followed directly by `runAgent(...)` — no pool involved,
// before or after this task — so the legacy leg below calls that same
// sequence directly, exactly as orchestrator.go does. thesis's two sites are
// `t.call(..., chiefTarget(...))`, so the thesis leg drives that real method
// on a real thesisRunner, and also makes one genuine cheap call
// (t.cheapTarget()) in the same wiring to prove cheap traffic still lands on
// the cheap server, not the Chief's.
func TestChiefRoutingMatrix(t *testing.T) {
	for _, pipeline := range []string{"legacy", "thesis"} {
		for _, engine := range []string{"claude", "api"} {
			t.Run(pipeline+"_"+engine, func(t *testing.T) {
				testChiefRoutingCase(t, pipeline, engine)
			})
		}
	}
}

func testChiefRoutingCase(t *testing.T, pipeline, engine string) {
	t.Helper()
	cheapSrv, cheapRec := newRoutingServer(t, "cheap-probe-response")
	chiefSrv, chiefRec := newRoutingServer(t, "chief-probe-response")

	cfg := Config{
		AgentsDir:            "../../agents",
		Timeouts:             model.StageTimeouts{Analysis: 5 * time.Second, Synthesis: 30 * time.Second},
		Retry:                model.RetryPolicy{MaxAttempts: 1},
		SynthesisMaxAttempts: 1,
	}

	var claudeMarker string
	switch engine {
	case "claude":
		cfg.ChiefEngine = "claude"
		bin := filepath.Join(t.TempDir(), "claude")
		claudeMarker = filepath.Join(t.TempDir(), "marker")
		script := "#!/bin/sh\necho \"$2\" >> \"" + claudeMarker + "\"\necho 'chief response'\n"
		if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
		cfg.Binaries = map[model.CLI]string{model.CLIClaude: bin}
		cfg.Models = map[model.CLI]string{model.CLIClaude: "opus"}
	case "api":
		cfg.ChiefEngine = "api"
		cfg.ChiefAPI = model.APIConfig{BaseURL: chiefSrv.URL, Model: "chief-model", APIKey: "chief-key", MaxTokens: 24576}
	}

	chiefE, err := resolveChiefEngine(cfg)
	if err != nil {
		t.Fatalf("resolveChiefEngine: %v", err)
	}
	data := `{"probe":"routing-matrix"}`

	var initial, corrective model.Report
	initialTarget := chiefTarget(chiefE, cfg, chiefInitial)
	correctiveTarget := chiefTarget(chiefE, cfg, chiefCorrective)
	// Everything but the caller's intent (which carries no field on
	// callTarget) must be identical: same CLI, model, binary, API, stage,
	// timeout and retry policy for both purposes.
	if initialTarget != correctiveTarget {
		t.Fatalf("corrective target != initial target:\n%+v\n%+v", initialTarget, correctiveTarget)
	}

	switch pipeline {
	case "legacy":
		// orchestrator.go's own dispatch sites, character for character: no
		// pool, no wrapper — chiefTarget then runAgent directly.
		initial = runAgent(context.Background(), initialTarget.CLI, "chief-analyst", string(initialTarget.Stage), data, initialTarget.Timeout, initialTarget.Retry, initialTarget.Model, initialTarget.Binary, initialTarget.API)
		corrective = runAgent(context.Background(), correctiveTarget.CLI, "chief-analyst", string(correctiveTarget.Stage), data, correctiveTarget.Timeout, correctiveTarget.Retry, correctiveTarget.Model, correctiveTarget.Binary, correctiveTarget.API)
	case "thesis":
		run, err := store.New(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		reg, err := agents.Load(cfg.AgentsDir)
		if err != nil {
			t.Fatal(err)
		}
		pool := newPool(1, cfg.Models, cfg.Binaries, model.APIConfig{BaseURL: cheapSrv.URL, Model: "cheap-model", APIKey: "cheap-key", MaxTokens: 4096}, model.CLIApi, 0)
		ctx, cancel := context.WithCancel(context.Background())
		pool.start(ctx)
		defer func() { cancel(); pool.stop() }()
		runner := &thesisRunner{cfg: cfg, ch: make(chan Event, 10), run: run, reg: reg, pool: pool, cheap: model.CLIApi}

		initial, err = runner.call(context.Background(), "thesis-chief", "chief-analyst", data, initialTarget)
		if err != nil && initial.Status != model.StatusDone {
			t.Fatalf("initial Chief call: %v (%+v)", err, initial)
		}
		corrective, err = runner.call(context.Background(), "thesis-chief", "chief-analyst-corrective", data, correctiveTarget)
		if err != nil && corrective.Status != model.StatusDone {
			t.Fatalf("corrective Chief call: %v (%+v)", err, corrective)
		}
		// Prove the cheap call in this same wiring really does land on the
		// cheap server — the routing matrix is meaningless if only the Chief
		// side is ever exercised.
		if cheapReport, err := runner.call(context.Background(), "macro", "macro", data, runner.cheapTarget()); err != nil || cheapReport.Status != model.StatusDone {
			t.Fatalf("cheap call: %v (%+v)", err, cheapReport)
		}
	}

	switch engine {
	case "claude":
		// The Chief itself must never touch either HTTP server. The thesis
		// leg also makes one genuine *cheap* call in this same wiring (to
		// prove cheap routing independently of the Chief's engine), which
		// legitimately hits cheapSrv — that is not a routing failure.
		if got := chiefRec.hits(); got != 0 {
			t.Fatalf("claude-engine Chief call reached chiefSrv: %d requests", got)
		}
		if pipeline == "legacy" && cheapRec.hits() != 0 {
			t.Fatalf("legacy pipeline made no cheap call in this test, but cheapSrv saw %d requests", cheapRec.hits())
		}
		marker, err := os.ReadFile(claudeMarker)
		if err != nil || !strings.Contains(string(marker), "routing-matrix") {
			t.Fatalf("fake claude binary did not receive the prompt: err=%v marker=%q", err, marker)
		}
		if initial.Status != model.StatusDone || corrective.Status != model.StatusDone {
			t.Fatalf("claude Chief calls did not complete: initial=%+v corrective=%+v", initial, corrective)
		}
	case "api":
		if initial.Status != model.StatusDone || corrective.Status != model.StatusDone {
			t.Fatalf("api Chief calls did not complete: initial=%+v corrective=%+v", initial, corrective)
		}
		if got := chiefRec.hits(); got != 2 {
			t.Fatalf("chiefSrv hits = %d, want 2 (initial + corrective)", got)
		}
		for _, body := range chiefRec.snapshot() {
			if body.Model != "chief-model" || body.MaxTokens != 24576 {
				t.Errorf("Chief request carried model=%q max_tokens=%d, want chief-model/24576 — the cheap config leaked in", body.Model, body.MaxTokens)
			}
		}
		if cheapRec.hits() != 0 {
			for _, body := range cheapRec.snapshot() {
				if body.Model == "chief-model" {
					t.Fatal("Chief traffic reached the cheap server")
				}
			}
		}
		if pipeline == "thesis" {
			if got := cheapRec.hits(); got != 1 {
				t.Fatalf("cheapSrv hits = %d, want 1 (the probe cheap call)", got)
			}
			for _, body := range cheapRec.snapshot() {
				if body.Model != "cheap-model" || body.MaxTokens != 4096 {
					t.Errorf("cheap request carried model=%q max_tokens=%d, want cheap-model/4096", body.Model, body.MaxTokens)
				}
			}
		}
	}
}

// TestAPIChiefKeepsSynthesisTimeoutAndStage guards against thesis.go deciding
// timeout, retry and stage from the CLI value alone (Diagnosis §5): before
// Task 4, `cli == model.CLIClaude` picked both, so a Chief routed to CLIApi
// silently fell back to the analysis timeout/retry/stage meant for
// specialists. chiefTarget must keep every purpose and both engines on the
// synthesis budget regardless of which CLI they resolve to.
func TestAPIChiefKeepsSynthesisTimeoutAndStage(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		// A transient (5xx, non-429/408) failure buys every configured retry
		// attempt without the test waiting anywhere near a real timeout.
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	cfg := Config{
		AgentsDir:            "../../agents",
		Timeouts:             model.StageTimeouts{Analysis: 5 * time.Second, Synthesis: 90 * time.Second},
		Retry:                model.RetryPolicy{MaxAttempts: 4, BaseDelay: time.Millisecond},
		SynthesisMaxAttempts: 2,
		ChiefEngine:          "api",
		ChiefAPI:             model.APIConfig{BaseURL: srv.URL, Model: "chief-model", APIKey: "chief-key"},
	}
	chiefE, err := resolveChiefEngine(cfg)
	if err != nil {
		t.Fatalf("resolveChiefEngine: %v", err)
	}
	target := chiefTarget(chiefE, cfg, chiefInitial)
	if target.Timeout != 90*time.Second {
		t.Errorf("Timeout = %v, want 90s (Synthesis, not the 5s Analysis timeout)", target.Timeout)
	}
	if target.Retry.MaxAttempts != 2 {
		t.Errorf("Retry.MaxAttempts = %d, want 2 (SynthesisMaxAttempts, not Retry.MaxAttempts=4)", target.Retry.MaxAttempts)
	}
	if target.Stage != model.StageSynthesis {
		t.Errorf("Stage = %q, want %q", target.Stage, model.StageSynthesis)
	}

	run, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reg, err := agents.Load(cfg.AgentsDir)
	if err != nil {
		t.Fatal(err)
	}
	runner := &thesisRunner{cfg: cfg, ch: make(chan Event, 10), run: run, reg: reg, cheap: model.CLIApi}
	report, _ := runner.call(context.Background(), "thesis-chief", "chief-analyst", "{}", target)
	if report.Stage != model.StageSynthesis {
		t.Errorf("report.Stage = %q, want synthesis (not analysis)", report.Stage)
	}
	if report.Attempts != 2 {
		t.Errorf("report.Attempts = %d, want 2 — SynthesisMaxAttempts must govern, not Retry.MaxAttempts=4", report.Attempts)
	}
	if attempts != 2 {
		t.Errorf("server saw %d requests, want 2", attempts)
	}
}

// TestOutputTokenLimitIsRecordedOnlyForAPICalls guards ruling R10: a CLI
// subprocess (gemini/claude) has no max_tokens parameter, so nothing sends
// one anywhere, and PromptProfile.OutputTokenLimit must record no cap at
// all for such a call rather than defaultMaxTokens — an absent/zero value
// means "unrecorded," never a measured one. Only a CLIApi call's own
// resolved cap belongs there, and a Chief-on-API call's cap must be its own,
// never leaked from the cheap engine's config.
func TestOutputTokenLimitIsRecordedOnlyForAPICalls(t *testing.T) {
	apiSrv, _ := newRoutingServer(t, "probe response")

	run, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reg, err := agents.Load("../../agents")
	if err != nil {
		t.Fatal(err)
	}
	runner := &thesisRunner{cfg: Config{AgentsDir: "../../agents"}, ch: make(chan Event, 10), run: run, reg: reg, cheap: model.CLIGemini}

	// A cheap CLI target (gemini): no max_tokens parameter exists on a
	// subprocess call, so the profile must record none.
	cliBin := filepath.Join(t.TempDir(), "gemini")
	if err := os.WriteFile(cliBin, []byte("#!/bin/sh\necho 'cli response'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	cliTarget := callTarget{CLI: model.CLIGemini, Binary: cliBin, Stage: model.StageAnalysis, Timeout: 5 * time.Second, Retry: model.RetryPolicy{MaxAttempts: 1}}
	cliReport, err := runner.call(context.Background(), "macro", "cli-probe", "{}", cliTarget)
	if err != nil || cliReport.Prompt == nil {
		t.Fatalf("cli call: %v (%+v)", err, cliReport)
	}
	if cliReport.Prompt.OutputTokenLimit != 0 {
		t.Errorf("CLI OutputTokenLimit = %d, want 0 — a subprocess call has no max_tokens", cliReport.Prompt.OutputTokenLimit)
	}

	// A cheap API target: its own resolved max_tokens is recorded.
	cheapAPITarget := callTarget{CLI: model.CLIApi, API: model.APIConfig{BaseURL: apiSrv.URL, Model: "cheap-model", APIKey: "k", MaxTokens: 4096}, Stage: model.StageAnalysis, Timeout: 5 * time.Second, Retry: model.RetryPolicy{MaxAttempts: 1}}
	cheapReport, err := runner.call(context.Background(), "macro", "cheap-api-probe", "{}", cheapAPITarget)
	if err != nil || cheapReport.Prompt == nil {
		t.Fatalf("cheap api call: %v (%+v)", err, cheapReport)
	}
	if cheapReport.Prompt.OutputTokenLimit != 4096 {
		t.Errorf("cheap API OutputTokenLimit = %d, want 4096", cheapReport.Prompt.OutputTokenLimit)
	}

	// A Chief API target: its cap is its own (defaultMaxTokens, since
	// ChiefAPI.MaxTokens is left unset here), never the cheap value above.
	cfg := Config{AgentsDir: "../../agents", ChiefEngine: "api", ChiefAPI: model.APIConfig{BaseURL: apiSrv.URL, Model: "chief-model", APIKey: "chief-key"},
		Timeouts: model.StageTimeouts{Synthesis: 5 * time.Second}, Retry: model.RetryPolicy{MaxAttempts: 1}, SynthesisMaxAttempts: 1}
	chiefE, err := resolveChiefEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	chiefReport, err := runner.call(context.Background(), "thesis-chief", "chief-probe", "{}", chiefTarget(chiefE, cfg, chiefInitial))
	if err != nil || chiefReport.Prompt == nil {
		t.Fatalf("chief api call: %v (%+v)", err, chiefReport)
	}
	if chiefReport.Prompt.OutputTokenLimit != defaultMaxTokens {
		t.Errorf("Chief OutputTokenLimit = %d, want %d (its own cap, not the cheap engine's 4096)", chiefReport.Prompt.OutputTokenLimit, defaultMaxTokens)
	}
}

// ---------------------------------------------------------------------------
// Task 5: fallback gating.
//
// resolveChiefFallback (orchestrator.go) gates the optional DeepSeek Chief
// fallback on its own api_key alone — it knows nothing about which engine the
// primary Chief call actually ran on. Once chief_engine="api" existed (Task
// 4), that became a live bug: an API-primary run whose Chief call fails would
// still reach the fallback through the key-only gate, dispatching a second,
// near-identical, billable DeepSeek call for a failure that has no Claude
// primary to be a resilience measure for.
//
// chiefFallbackAllowed (fallback.go) closes this by adding the primary-engine
// and tri-state ChiefFallbackEnabled gates on top of resolveChiefFallback's
// own credential validation, matching config.Settings.ChiefFallbackActive's
// engine-aware rule so the two can no longer disagree.
// ---------------------------------------------------------------------------

// countingChiefServer is like fakeDeepSeekServer but exposes its own request
// count, so a test can assert "at most one call reached this endpoint" by
// counting rather than by inferring it from the run's outcome.
func countingChiefServer(t *testing.T, content string) (*httptest.Server, *int32) {
	t.Helper()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{
			"choices": []map[string]any{{
				"finish_reason": "stop",
				"message":       map[string]string{"role": "assistant", "content": content},
			}},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// failingServer always answers with the given status, counting requests.
func failingServer(t *testing.T, status int) (*httptest.Server, *int32) {
	t.Helper()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// TestAPIPrimaryNeverAttemptsClaudeOrASecondIdenticalCall is the assertion the
// controller flagged as the worst bug this task can leave unfixed: with
// chief_engine=api and [chief_fallback] credentials still configured (enabled
// omitted, the common shape — this repo's own cfr.toml is exactly that), a
// failing API-primary Chief call must reach the Chief endpoint exactly once —
// never fall back to a second, near-identical DeepSeek call, and never touch
// the claude binary (there is no Claude primary to fail over from).
func TestAPIPrimaryNeverAttemptsClaudeOrASecondIdenticalCall(t *testing.T) {
	t.Setenv("CFR_FAKE_MODE", "ok") // scouts/specialists (gemini fakebin) succeed normally
	chiefSrv, chiefHits := failingServer(t, http.StatusInternalServerError)
	fallbackSrv, fallbackHits := countingChiefServer(t, fallbackIdeasContent)

	cfg := testConfig(t, model.ModeIndependent)
	// The claude binary must never be invoked: replace it with one that
	// records if it ever is, rather than trusting "it wasn't configured".
	claudeMarker := filepath.Join(t.TempDir(), "claude-invoked")
	claudeBin := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(claudeBin, []byte("#!/bin/sh\necho invoked >> \""+claudeMarker+"\"\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	cfg.Binaries[model.CLIClaude] = claudeBin

	cfg.ChiefEngine = "api"
	cfg.ChiefAPI = model.APIConfig{BaseURL: chiefSrv.URL, Model: "chief-model", APIKey: "chief-key"}
	cfg.ChiefFallback = model.APIConfig{BaseURL: fallbackSrv.URL, Model: "deepseek-reasoner", APIKey: "sk-test"}
	// enabled deliberately left nil (omitted).

	complete, runErr, _ := drain(t, Run(context.Background(), cfg))
	if runErr != nil {
		t.Fatalf("unexpected EventError: %s", runErr.Message)
	}
	if complete == nil {
		t.Fatal("no EventComplete received")
	}

	if got := atomic.LoadInt32(chiefHits); got != 1 {
		t.Errorf("chief endpoint saw %d requests, want exactly 1", got)
	}
	if got := atomic.LoadInt32(fallbackHits); got != 0 {
		t.Errorf("fallback endpoint saw %d requests, want 0 — chief_engine=api has no Claude primary to fall back from", got)
	}
	if _, err := os.Stat(claudeMarker); err == nil {
		t.Error("the claude binary was invoked, but chief_engine=api should never dispatch to it")
	}
	if complete.Meta.Outcome != "degraded" {
		t.Errorf("outcome = %q, want degraded", complete.Meta.Outcome)
	}
	for _, d := range complete.Meta.Domains {
		if d.Domain == "chief-analyst-fallback" {
			t.Errorf("a chief-analyst-fallback domain status was recorded, but the fallback must never have run: %+v", d)
		}
	}
}

// TestFallbackDisabledExplicitlyWithCredentialsPresent: chief_engine=claude
// (the default), [chief_fallback].enabled=false, credentials present. An
// explicit false must win over api_key-present alone.
func TestFallbackDisabledExplicitlyWithCredentialsPresent(t *testing.T) {
	t.Setenv("CFR_FAKE_MODE", "chief-fail")
	fallbackSrv, fallbackHits := countingChiefServer(t, fallbackIdeasContent)

	cfg := testConfig(t, model.ModeIndependent)
	cfg.ChiefFallback = model.APIConfig{BaseURL: fallbackSrv.URL, Model: "deepseek-reasoner", APIKey: "sk-test"}
	disabled := false
	cfg.ChiefFallbackEnabled = &disabled

	complete, runErr, _ := drain(t, Run(context.Background(), cfg))
	if runErr != nil {
		t.Fatalf("unexpected EventError: %s", runErr.Message)
	}
	if complete == nil {
		t.Fatal("no EventComplete received")
	}

	if got := atomic.LoadInt32(fallbackHits); got != 0 {
		t.Errorf("fallback endpoint saw %d requests, want 0 — enabled=false must win over api_key alone", got)
	}
	if complete.Meta.Outcome != "degraded" {
		t.Errorf("outcome = %q, want degraded", complete.Meta.Outcome)
	}
	assertDegradedIdeas(t, complete.Ideas)
}

// TestHistoricalClaudeToAPIFallbackStillWorks pins today's documented
// behaviour unchanged: chief_engine omitted (defaults to claude),
// [chief_fallback] credentials present, enabled omitted. A failing primary
// must produce exactly one fallback request, a parsed result, and must NOT
// erase the primary failure: the run stays degraded and the "chief-analyst"
// domain keeps its failed status.
func TestHistoricalClaudeToAPIFallbackStillWorks(t *testing.T) {
	t.Setenv("CFR_FAKE_MODE", "chief-fail")
	fallbackSrv, fallbackHits := countingChiefServer(t, fallbackIdeasContent)

	cfg := testConfig(t, model.ModeIndependent)
	cfg.ChiefFallback = model.APIConfig{BaseURL: fallbackSrv.URL, Model: "deepseek-reasoner", APIKey: "sk-test"}
	// ChiefEngine and ChiefFallbackEnabled both left zero-value (omitted).

	complete, runErr, _ := drain(t, Run(context.Background(), cfg))
	if runErr != nil {
		t.Fatalf("unexpected EventError: %s", runErr.Message)
	}
	assertFallbackFired(t, complete)

	if got := atomic.LoadInt32(fallbackHits); got != 1 {
		t.Errorf("fallback endpoint saw %d requests, want exactly 1", got)
	}
	foundFailedPrimary := false
	for _, d := range complete.Meta.Domains {
		if d.Domain == "chief-analyst" && d.Status == model.StatusFailed {
			foundFailedPrimary = true
		}
	}
	if !foundFailedPrimary {
		t.Errorf("the primary chief-analyst failure must still be recorded alongside a successful fallback: %+v", complete.Meta.Domains)
	}
}

// TestChiefFallbackFiresWhenExplicitlyEnabledOnClaudePrimary closes ruling
// R28's Gate B gap: the chief_fallback truth table's sixth row —
// chief_engine="claude" (the default), [chief_fallback] enabled EXPLICITLY
// true, credentials present — was tested at neither the config layer nor
// here. ChiefFallbackActive (config.go) takes the identical code path for
// enabled=true and enabled omitted (fallback.go:49's gate is `!= nil &&
// !*enabled`, so only an explicit false differs), so a regression that
// special-cased "omitted" instead of testing the boolean's value would flip
// this exact configuration from "fallback fires" to "run degrades" while
// every other existing test kept passing.
//
// TestHistoricalClaudeToAPIFallbackStillWorks is the template; the only
// difference is ChiefFallbackEnabled set to &trueVal instead of left nil.
func TestChiefFallbackFiresWhenExplicitlyEnabledOnClaudePrimary(t *testing.T) {
	t.Setenv("CFR_FAKE_MODE", "chief-fail")
	fallbackSrv, fallbackHits := countingChiefServer(t, fallbackIdeasContent)

	cfg := testConfig(t, model.ModeIndependent)
	cfg.ChiefFallback = model.APIConfig{BaseURL: fallbackSrv.URL, Model: "deepseek-reasoner", APIKey: "sk-test"}
	trueVal := true
	cfg.ChiefFallbackEnabled = &trueVal

	complete, runErr, _ := drain(t, Run(context.Background(), cfg))
	if runErr != nil {
		t.Fatalf("unexpected EventError: %s", runErr.Message)
	}
	assertFallbackFired(t, complete)

	if got := atomic.LoadInt32(fallbackHits); got != 1 {
		t.Errorf("fallback endpoint saw %d requests, want exactly 1", got)
	}
	foundFailedPrimary := false
	for _, d := range complete.Meta.Domains {
		if d.Domain == "chief-analyst" && d.Status == model.StatusFailed {
			foundFailedPrimary = true
		}
	}
	if !foundFailedPrimary {
		t.Errorf("the primary chief-analyst failure must still be recorded alongside a successful fallback: %+v", complete.Meta.Domains)
	}
}

// ---------------------------------------------------------------------------
// Task 5: permanent-failure stop.
//
// apiengine.go already classifies any 4xx other than 429/408 as permanent
// (isPermanentStatus) and wraps it in permanentError; runner.go already stops
// on a permanentError or outputLimitError without a further attempt. Neither
// had Chief-specific coverage, and neither gave a 401/403 a FailureKind a
// caller could distinguish from an ordinary failed call the way the Claude
// disabled-subscription case already gets "authentication".
// ---------------------------------------------------------------------------

// TestPermanentAuthFailureStopsUnchangedRetries proves a 401 or 403 from the
// Chief's own API endpoint buys exactly one dispatched attempt — never the
// unchanged-retry behaviour a bare transient/permanent distinction would
// otherwise miss — and that the resulting report carries a FailureKind that
// tells a 401/403 apart from every other failure.
func TestPermanentAuthFailureStopsUnchangedRetries(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(fmt.Sprintf("%d", code), func(t *testing.T) {
			srv, hits := failingServer(t, code)
			api := model.APIConfig{BaseURL: srv.URL, Model: "chief-model", APIKey: "bad-key"}
			// A generous retry budget: if unchanged-retry ever regressed back
			// in, this would spend all 3 rather than stopping at 1.
			retry := model.RetryPolicy{MaxAttempts: 3, BaseDelay: time.Millisecond}

			r := runAgent(context.Background(), model.CLIApi, "chief-analyst", string(model.StageSynthesis),
				"prompt", 5*time.Second, retry, "", "", api)

			if got := atomic.LoadInt32(hits); got != 1 {
				t.Fatalf("server saw %d requests, want exactly 1 (no unchanged retry on a permanent auth failure)", got)
			}
			if r.Attempts != 1 {
				t.Errorf("report.Attempts = %d, want 1", r.Attempts)
			}
			if r.Status != model.StatusFailed {
				t.Fatalf("status = %q, want failed", r.Status)
			}
			if r.FailureKind != "permanent_auth" {
				t.Errorf("FailureKind = %q, want permanent_auth", r.FailureKind)
			}
		})
	}
}

// TestOutputLimitGetsNoUnchangedRetryOrSchemaRepair proves a
// finish_reason=="length" Chief response stops after exactly one attempt,
// preserves the usage the provider reported even though the call failed, and
// carries FailureKind "output_limit". It also stands as the proof that an
// output-limit Chief failure cannot buy a schema-repair call: the Chief's own
// dispatch (thesis.go's t.call for "chief-analyst"/"thesis-chief") never
// routes through researchCall/decodeOrRepair — that machinery (thesis_schema.go)
// is wired only to the research roles (triage/researcher/challenger) — so
// there is no repair call for this test to observe not happening; the single
// request this test counts is the only one that could ever be dispatched.
func TestOutputLimitGetsNoUnchangedRetryOrSchemaRepair(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		fmt.Fprint(w, `{"choices":[{"finish_reason":"length","message":{"role":"assistant","content":"half a board"}}],"usage":{"prompt_tokens":100,"completion_tokens":50}}`)
	}))
	defer srv.Close()

	api := model.APIConfig{BaseURL: srv.URL, Model: "chief-model", APIKey: "k"}
	retry := model.RetryPolicy{MaxAttempts: 3, BaseDelay: time.Millisecond}

	r := runAgent(context.Background(), model.CLIApi, "chief-analyst", string(model.StageSynthesis),
		"prompt", 5*time.Second, retry, "", "", api)

	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("server saw %d requests, want exactly 1 (no unchanged retry and no repair call)", got)
	}
	if r.Status != model.StatusFailed {
		t.Fatalf("status = %q, want failed", r.Status)
	}
	if r.FailureKind != "output_limit" {
		t.Errorf("FailureKind = %q, want output_limit", r.FailureKind)
	}
	if len(r.Usage) == 0 || r.Usage[0].CompletionTokens == nil || *r.Usage[0].CompletionTokens != 50 {
		t.Errorf("usage not preserved on a truncated failure: %+v", r.Usage)
	}
}

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

// Compaction takes the Chief's engine and long timeout but NOT its
// synthesis_max_attempts: that budget (default 1) exists because the Chief's
// one synthesis call has its own fallback, and compaction has none. A
// transient reset on the only attempt failed 2330.TW live
// (runs/2026-09-23T15-25-44), where the cheap path would have retried.
func TestCompactionTargetFollowsTheResolvedChiefEngine(t *testing.T) {
	cfg := Config{SynthesisMaxAttempts: 1}
	cfg.Retry.MaxAttempts = 2
	cfg.Timeouts.Synthesis = 90 * time.Second
	r := &thesisRunner{cfg: cfg, chief: chiefEngine{CLI: model.CLIClaude, Model: "opus", Binary: "claude"}}
	got := r.compactionTarget()
	if got.CLI != model.CLIClaude || got.throttled || got.Stage != model.StageSynthesis || got.Timeout != 90*time.Second || got.Retry.MaxAttempts != 2 {
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
	runner.cfg.Retry.MaxAttempts = 3
	var out model.CandidateDossier
	reports, _, _, err := researchCall(context.Background(), runner, "thesis-researcher", "trunc", "data", &out, dossierSchema)
	if err == nil || calls != 1 || reports[1].FailureKind != "output_limit" {
		t.Fatalf("calls=%d err=%v recovery=%+v", calls, err, reports[1])
	}
}

func TestCompactionRetriesATransientChiefFailure(t *testing.T) {
	calls := 0
	chief := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			http.Error(w, "upstream reset", http.StatusBadGateway)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
			"message": map[string]string{"content": fenced(map[string]any{"long_case": "short"})}}}})
	}))
	defer chief.Close()
	d := supportedResearch().Dossier
	d.ContractVersion = 2
	d.LongCase = strings.Repeat("long narrative ", 2000)
	runner, _, done := thesisFixture(t, func(string, int) string { return fenced(d) })
	defer done()
	runner.chief = chiefEngine{CLI: model.CLIApi, Model: "chief-model", API: model.APIConfig{BaseURL: chief.URL, Model: "chief-model", APIKey: "k2"}}
	runner.cfg.SynthesisMaxAttempts = 1
	runner.cfg.Retry.MaxAttempts = 2
	runner.cfg.Retry.BaseDelay = time.Millisecond
	var out model.CandidateDossier
	reports, _, _, err := researchCall(context.Background(), runner, "thesis-researcher", "transient", "data", &out, dossierSchema)
	if err != nil || calls != 2 || reports[1].Contract != "compacted" || reports[1].Attempts != 2 {
		t.Fatalf("calls=%d err=%v recovery=%+v", calls, err, reports[1])
	}
}

// Measured on deepseek-v4-pro (2026-09-23), delivered narrative bytes against
// the per-field budgets it was given: effort=high landed at -19%..+2% across
// four live compactions; effort=low at +1%..+12% across three, independent of
// how deep the cut was. So a low-effort call is told budgets scaled by
// compactionLowEffortTarget, while measurement and feasibility still use the
// real allowance; high/max are told the real budgets.
func TestCompactionReasoningEffortPolicy(t *testing.T) {
	for _, c := range []struct {
		policy, want string
		scaled       bool
	}{
		{"low", "low", true},
		{"high", "high", false},
		{"max", "max", false},
		{"", "", false},
	} {
		t.Run("policy="+c.policy, func(t *testing.T) {
			var efforts []any
			var prompts []string
			chief := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var b struct {
					Effort   any `json:"reasoning_effort"`
					Messages []struct {
						Content string `json:"content"`
					} `json:"messages"`
				}
				json.NewDecoder(r.Body).Decode(&b)
				efforts = append(efforts, b.Effort)
				prompts = append(prompts, b.Messages[0].Content)
				json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
					"message": map[string]string{"content": fenced(map[string]any{"long_case": "short"})}}}})
			}))
			defer chief.Close()
			d := supportedResearch().Dossier
			d.ContractVersion = 2
			d.LongCase = strings.Repeat("n", 21000)
			runner, _, done := thesisFixture(t, func(string, int) string { return fenced(d) })
			defer done()
			runner.chief = chiefEngine{CLI: model.CLIApi, Model: "chief-model", API: model.APIConfig{BaseURL: chief.URL, Model: "chief-model", APIKey: "k2", CompactionEffort: c.policy}}
			var out model.CandidateDossier
			reports, _, _, err := researchCall(context.Background(), runner, "thesis-researcher", "effort", "data", &out, dossierSchema)
			if err != nil {
				t.Fatal(err)
			}
			got := ""
			if len(efforts) == 1 && efforts[0] != nil {
				got = efforts[0].(string)
			}
			if len(efforts) != 1 || got != c.want {
				t.Fatalf("sent reasoning_effort %v, want %q", efforts, c.want)
			}
			a := reports[1].Allowance
			if a == nil || a.ReasoningEffort != c.want {
				t.Fatalf("effort not recorded on the allowance: %+v", a)
			}
			real := fmt.Sprintf("long_case: %d bytes (currently", a.PerField["long_case"])
			stated := fmt.Sprintf("long_case: %d bytes (currently", int(float64(a.PerField["long_case"])*compactionLowEffortTarget))
			if c.scaled && (!strings.Contains(prompts[0], stated) || strings.Contains(prompts[0], real)) {
				t.Fatalf("low effort must be told the scaled budget %q", stated)
			}
			if !c.scaled && !strings.Contains(prompts[0], real) {
				t.Fatalf("effort %q must be told the real budget %q", c.policy, real)
			}
		})
	}
}
