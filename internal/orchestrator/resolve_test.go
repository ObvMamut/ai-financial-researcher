package orchestrator

import (
	"testing"

	"github.com/mamut/claude-financial-researcher/internal/model"
)

func TestResolveCheapEngineGemini(t *testing.T) {
	engine, api, conc, err := resolveCheapEngine(Config{CheapEngine: model.CLIGemini, GeminiConcurrency: 1})
	if err != nil {
		t.Fatalf("gemini: %v", err)
	}
	if engine != model.CLIGemini || conc != 1 || api != (model.APIConfig{}) {
		t.Errorf("gemini => (%q, %+v, %d)", engine, api, conc)
	}
}

func TestResolveCheapEngineAPI(t *testing.T) {
	cfg := Config{
		CheapEngine: model.CLIApi,
		API:         model.APIConfig{BaseURL: "https://api.deepseek.com", Model: "deepseek-chat", APIKey: "sk-x"},
	}
	engine, api, conc, err := resolveCheapEngine(cfg)
	if err != nil {
		t.Fatalf("api: %v", err)
	}
	// Remote API resolves to the CLIApi engine, its own config, unthrottled (0).
	if engine != model.CLIApi || api != cfg.API || conc != 0 {
		t.Errorf("api => (%q, %+v, %d)", engine, api, conc)
	}
}

func TestResolveCheapEngineAPIMissingKey(t *testing.T) {
	cfg := Config{CheapEngine: model.CLIApi, API: model.APIConfig{BaseURL: "https://x", Model: "m"}}
	if _, _, _, err := resolveCheapEngine(cfg); err == nil {
		t.Fatal("expected error when remote api key is missing")
	}
}

func TestResolveCheapEngineLocal(t *testing.T) {
	cfg := Config{
		CheapEngine:      model.CLILocal,
		Local:            model.APIConfig{BaseURL: "http://localhost:11434/v1", Model: "qwen2.5:7b-instruct"},
		LocalConcurrency: 1,
	}
	engine, api, conc, err := resolveCheapEngine(cfg)
	if err != nil {
		t.Fatalf("local: %v", err)
	}
	// Local shares the CLIApi runner engine, uses the [local] config, throttled.
	if engine != model.CLIApi || api != cfg.Local || conc != 1 {
		t.Errorf("local => (%q, %+v, %d)", engine, api, conc)
	}
}

func TestResolveCheapEngineLocalNoKeyNeeded(t *testing.T) {
	// Local requires base_url + model but NOT a key.
	cfg := Config{
		CheapEngine:      model.CLILocal,
		Local:            model.APIConfig{BaseURL: "http://localhost:11434/v1", Model: "llama3.1:8b"},
		LocalConcurrency: 2,
	}
	if _, _, conc, err := resolveCheapEngine(cfg); err != nil || conc != 2 {
		t.Fatalf("local without key should succeed with conc=2, got conc=%d err=%v", conc, err)
	}
}

func TestResolveCheapEngineLocalMissingModel(t *testing.T) {
	cfg := Config{CheapEngine: model.CLILocal, Local: model.APIConfig{BaseURL: "http://localhost:11434/v1"}}
	if _, _, _, err := resolveCheapEngine(cfg); err == nil {
		t.Fatal("expected error when local model is missing")
	}
}

func TestResolveCheapEngineUnknown(t *testing.T) {
	if _, _, _, err := resolveCheapEngine(Config{CheapEngine: "bogus"}); err == nil {
		t.Fatal("expected error for unknown cheap_engine")
	}
}
