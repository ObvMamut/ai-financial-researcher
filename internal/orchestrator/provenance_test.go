package orchestrator

import (
	"context"
	"testing"

	"github.com/mamut/claude-financial-researcher/internal/model"
)

// TestChiefProvenanceDistinguishesConfiguredAttemptedAccepted is Task 6's
// headline regression: today both Chief call sites (thesis.go, orchestrator.go)
// write SynthesisModel: cfg.Models[model.CLIClaude] unconditionally, so a run
// whose Chief actually answered on chief_engine="api" (or on the DeepSeek
// resilience fallback) still reports "opus" in metadata.json. That is
// indistinguishable from a real Claude-Chief run downstream (scoreboard
// cohorts, the TUI, the text renderer) and is the exact bug the sep15 fixture
// documents as unfixed.
//
// This test pins three genuinely different facts: the CONFIGURED primary
// engine (never changes because a fallback happened to rescue the run), the
// engine(s) ATTEMPTED (comma-joined, in call order), and the engine whose
// output was ACCEPTED (empty when neither produced usable ideas).
func TestChiefProvenanceDistinguishesConfiguredAttemptedAccepted(t *testing.T) {
	t.Run("claude_primary_fails_api_fallback_succeeds", func(t *testing.T) {
		t.Setenv("CFR_FAKE_MODE", "chief-fail") // fake claude binary fails the Chief call
		fallbackSrv, fallbackHits := countingChiefServer(t, fallbackIdeasContent)

		cfg := testConfig(t, model.ModeIndependent)
		cfg.ChiefFallback = model.APIConfig{BaseURL: fallbackSrv.URL, Model: "deepseek-reasoner", APIKey: "sk-test"}
		// ChiefEngine left at its zero value: "claude" is the configured primary.

		complete, runErr, _ := drain(t, Run(context.Background(), cfg))
		if runErr != nil {
			t.Fatalf("unexpected EventError: %s", runErr.Message)
		}
		if complete == nil {
			t.Fatal("no EventComplete received")
		}
		meta := complete.Meta
		if got := *fallbackHits; got != 1 {
			t.Fatalf("fallback endpoint saw %d requests, want exactly 1", got)
		}

		if meta.ChiefEngine != "claude" {
			t.Errorf("ChiefEngine = %q, want %q (the configured primary, unchanged by a successful fallback)", meta.ChiefEngine, "claude")
		}
		if meta.ChiefAttempted != "claude,api" {
			t.Errorf("ChiefAttempted = %q, want %q", meta.ChiefAttempted, "claude,api")
		}
		if meta.ChiefAccepted != "api" {
			t.Errorf("ChiefAccepted = %q, want %q (the fallback's output shipped)", meta.ChiefAccepted, "api")
		}
		if meta.ChiefModel != "deepseek-reasoner" {
			t.Errorf("ChiefModel = %q, want %q", meta.ChiefModel, "deepseek-reasoner")
		}
		if meta.SynthesisModel != "deepseek-reasoner" {
			t.Errorf("SynthesisModel = %q, want %q — it must track the model that actually answered, not cfg.Models[CLIClaude]", meta.SynthesisModel, "deepseek-reasoner")
		}

		// The primary failure must still be recorded: a successful fallback
		// must not erase it.
		foundFailedPrimary := false
		for _, d := range meta.Domains {
			if d.Domain == "chief-analyst" && d.Status == model.StatusFailed {
				foundFailedPrimary = true
			}
		}
		if !foundFailedPrimary {
			t.Errorf("primary chief-analyst failure not recorded alongside the successful fallback: %+v", meta.Domains)
		}
	})

	t.Run("api_primary_succeeds", func(t *testing.T) {
		chiefSrv, chiefHits := countingChiefServer(t, fallbackIdeasContent)

		cfg := testConfig(t, model.ModeIndependent)
		cfg.ChiefEngine = "api"
		cfg.ChiefAPI = model.APIConfig{BaseURL: chiefSrv.URL, Model: "chief-model", APIKey: "chief-key"}
		// cfg.Models[model.CLIClaude] defaults to "opus" via applyDefaults; the
		// cheap engine (gemini/fakebin) reports some other model entirely. Both
		// must be absent from ChiefModel/SynthesisModel.

		complete, runErr, _ := drain(t, Run(context.Background(), cfg))
		if runErr != nil {
			t.Fatalf("unexpected EventError: %s", runErr.Message)
		}
		if complete == nil {
			t.Fatal("no EventComplete received")
		}
		meta := complete.Meta
		if got := *chiefHits; got == 0 {
			t.Fatal("chief endpoint saw no requests")
		}

		if meta.ChiefEngine != "api" {
			t.Errorf("ChiefEngine = %q, want %q", meta.ChiefEngine, "api")
		}
		if meta.ChiefAttempted != "api" {
			t.Errorf("ChiefAttempted = %q, want %q", meta.ChiefAttempted, "api")
		}
		if meta.ChiefAccepted != "api" {
			t.Errorf("ChiefAccepted = %q, want %q", meta.ChiefAccepted, "api")
		}
		if meta.ChiefModel != "chief-model" {
			t.Errorf("ChiefModel = %q, want %q (NOT cfg.Models[CLIClaude]=opus, NOT the cheap engine's model)", meta.ChiefModel, "chief-model")
		}
		if meta.SynthesisModel != "chief-model" {
			t.Errorf("SynthesisModel = %q, want %q", meta.SynthesisModel, "chief-model")
		}
	})
}
