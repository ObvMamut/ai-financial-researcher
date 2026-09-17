package orchestrator

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
