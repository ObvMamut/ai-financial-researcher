package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/model"
)

// clearEnv blanks every env var Load reads so the host environment can't leak
// into assertions.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"CFR_RUNS_DIR", "CFR_AGENTS_DIR", "ALPHAVANTAGE_API_KEY", "FRED_API_KEY",
		"CFR_CONTACT_EMAIL", "CFR_CLAUDE_MODEL", "CFR_GEMINI_MODEL",
		"CFR_CLAUDE_BIN", "CFR_GEMINI_BIN", "CFR_GEMINI_CONCURRENCY", "CFR_KEEP_RUNS",
		"CFR_CHEAP_ENGINE", "CFR_API_BASE_URL", "CFR_API_MODEL", "CFR_API_KEY", "DEEPSEEK_API_KEY",
		"CFR_LOCAL_BASE_URL", "CFR_LOCAL_MODEL", "CFR_LOCAL_KEY", "CFR_LOCAL_CONCURRENCY",
	} {
		t.Setenv(k, "")
	}
}

// isolate points HOME at an empty temp dir and chdirs into another, so only
// files the test writes are visible to Load.
func isolate(t *testing.T) (home, cwd string) {
	t.Helper()
	home = t.TempDir()
	cwd = t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(cwd)
	clearEnv(t)
	return home, cwd
}

func TestLoadDefaults(t *testing.T) {
	isolate(t)
	s, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.AgentsDir != "agents" || s.RunsDir != "runs" || s.DataDir != ".data" {
		t.Errorf("default dirs wrong: %+v", s)
	}
	if s.KeepRuns != 100 {
		t.Errorf("KeepRuns = %d, want 100", s.KeepRuns)
	}
}

func TestLoadFileOverridesDefaults(t *testing.T) {
	_, cwd := isolate(t)
	toml := `
runs_dir = "myruns"
keep_runs = 7
indices = ["eu50", "sp500"]
gemini_concurrency = 3

[weights]
fundamentals = 0.4
quant = 0.3
news = 0.1
macro = 0.1
sentiment = 0.1

[timeouts]
screening = "90s"

[retry]
max_attempts = 5
base_delay = "1s"

[models]
claude = "sonnet"

[binaries]
gemini = "/opt/agy"

[providers]
contact_email = "a@b.c"
`
	if err := os.WriteFile(filepath.Join(cwd, "cfr.toml"), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.RunsDir != "myruns" || s.KeepRuns != 7 || s.GeminiConcurrency != 3 {
		t.Errorf("scalars not applied: %+v", s)
	}
	if len(s.Indices) != 2 || s.Indices[0] != "eu50" {
		t.Errorf("Indices = %v", s.Indices)
	}
	if s.Weights.Fundamentals != 0.4 || s.Weights.Quant != 0.3 {
		t.Errorf("Weights = %+v", s.Weights)
	}
	if s.Timeouts.Screening != 90*time.Second {
		t.Errorf("Timeouts.Screening = %v", s.Timeouts.Screening)
	}
	if s.Retry.MaxAttempts != 5 || s.Retry.BaseDelay != time.Second {
		t.Errorf("Retry = %+v", s.Retry)
	}
	if s.Models[model.CLIClaude] != "sonnet" {
		t.Errorf("Models = %v", s.Models)
	}
	if s.Binaries[model.CLIGemini] != "/opt/agy" {
		t.Errorf("Binaries = %v", s.Binaries)
	}
	if s.Providers.ContactEmail != "a@b.c" {
		t.Errorf("Providers = %+v", s.Providers)
	}
	// AgentsDir untouched by the file → still the default.
	if s.AgentsDir != "agents" {
		t.Errorf("AgentsDir = %q, want default", s.AgentsDir)
	}
}

func TestLoadFileCanExplicitlyDisableCachePruning(t *testing.T) {
	_, cwd := isolate(t)
	if err := os.WriteFile(filepath.Join(cwd, "cfr.toml"), []byte("data_cache_days = 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// An explicit 0 in the file means "disable pruning" and must not be
	// mistaken for "key absent" and silently overridden by the default (7).
	if s.DataCacheDays != 0 {
		t.Errorf("DataCacheDays = %d, want 0 (explicit disable)", s.DataCacheDays)
	}
}

func TestLoadFileOmittingCacheDaysKeepsDefault(t *testing.T) {
	_, cwd := isolate(t)
	if err := os.WriteFile(filepath.Join(cwd, "cfr.toml"), []byte("runs_dir = \"myruns\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.DataCacheDays != 7 {
		t.Errorf("DataCacheDays = %d, want default 7 when the key is absent", s.DataCacheDays)
	}
}

func TestLoadLocalFileBeatsGlobal(t *testing.T) {
	home, cwd := isolate(t)
	global := filepath.Join(home, ".config", "cfr")
	if err := os.MkdirAll(global, 0o755); err != nil {
		t.Fatal(err)
	}
	// Global sets two keys; local overrides one of them.
	os.WriteFile(filepath.Join(global, "config.toml"), []byte("runs_dir = \"global-runs\"\nkeep_runs = 5\n"), 0o644)
	os.WriteFile(filepath.Join(cwd, "cfr.toml"), []byte("runs_dir = \"local-runs\"\n"), 0o644)

	s, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.RunsDir != "local-runs" {
		t.Errorf("RunsDir = %q, want local-runs (local file wins)", s.RunsDir)
	}
	if s.KeepRuns != 5 {
		t.Errorf("KeepRuns = %d, want 5 (global key not overridden locally)", s.KeepRuns)
	}
}

func TestLoadEnvBeatsFiles(t *testing.T) {
	_, cwd := isolate(t)
	os.WriteFile(filepath.Join(cwd, "cfr.toml"),
		[]byte("runs_dir = \"file-runs\"\nkeep_runs = 5\n\n[binaries]\ngemini = \"file-agy\"\n"), 0o644)
	t.Setenv("CFR_RUNS_DIR", "env-runs")
	t.Setenv("CFR_KEEP_RUNS", "9")
	t.Setenv("CFR_GEMINI_BIN", "env-agy")

	s, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.RunsDir != "env-runs" || s.KeepRuns != 9 || s.Binaries[model.CLIGemini] != "env-agy" {
		t.Errorf("env did not win: RunsDir=%q KeepRuns=%d Binaries=%v", s.RunsDir, s.KeepRuns, s.Binaries)
	}
}

func TestLoadCheapEngineAPIFromFile(t *testing.T) {
	_, cwd := isolate(t)
	toml := `
cheap_engine = "api"

[api]
base_url = "https://api.deepseek.com"
model = "deepseek-chat"
`
	if err := os.WriteFile(filepath.Join(cwd, "cfr.toml"), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.CheapEngine != model.CLIApi {
		t.Errorf("CheapEngine = %q, want api", s.CheapEngine)
	}
	if s.API.BaseURL != "https://api.deepseek.com" || s.API.Model != "deepseek-chat" {
		t.Errorf("API = %+v", s.API)
	}
}

func TestLoadAPIKeyFromEnv(t *testing.T) {
	isolate(t)
	// CFR_API_KEY takes precedence over the DEEPSEEK_API_KEY alias.
	t.Setenv("DEEPSEEK_API_KEY", "sk-alias")
	t.Setenv("CFR_API_KEY", "sk-primary")
	t.Setenv("CFR_CHEAP_ENGINE", "api")
	s, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.API.APIKey != "sk-primary" {
		t.Errorf("APIKey = %q, want CFR_API_KEY to win over DEEPSEEK_API_KEY", s.API.APIKey)
	}
	if s.CheapEngine != model.CLIApi {
		t.Errorf("CheapEngine = %q, want api", s.CheapEngine)
	}
}

func TestLoadDeepSeekKeyAliasFromEnv(t *testing.T) {
	isolate(t)
	// With only DEEPSEEK_API_KEY set, it fills the key.
	t.Setenv("DEEPSEEK_API_KEY", "sk-alias")
	s, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.API.APIKey != "sk-alias" {
		t.Errorf("APIKey = %q, want the DEEPSEEK_API_KEY alias", s.API.APIKey)
	}
}

func TestLoadLocalEngineFromFile(t *testing.T) {
	_, cwd := isolate(t)
	toml := `
cheap_engine = "local"
local_concurrency = 2

[local]
base_url = "http://localhost:11434/v1"
model = "qwen2.5:7b-instruct"
`
	if err := os.WriteFile(filepath.Join(cwd, "cfr.toml"), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.CheapEngine != model.CLILocal {
		t.Errorf("CheapEngine = %q, want local", s.CheapEngine)
	}
	if s.Local.BaseURL != "http://localhost:11434/v1" || s.Local.Model != "qwen2.5:7b-instruct" {
		t.Errorf("Local = %+v", s.Local)
	}
	if s.LocalConcurrency != 2 {
		t.Errorf("LocalConcurrency = %d, want 2", s.LocalConcurrency)
	}
}

func TestLoadLocalKeyFromEnv(t *testing.T) {
	isolate(t)
	t.Setenv("CFR_LOCAL_BASE_URL", "http://127.0.0.1:8000/v1")
	t.Setenv("CFR_LOCAL_MODEL", "llama3.1:8b")
	s, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.Local.BaseURL != "http://127.0.0.1:8000/v1" || s.Local.Model != "llama3.1:8b" {
		t.Errorf("Local from env = %+v", s.Local)
	}
}

func TestLoadMalformedFileIsError(t *testing.T) {
	_, cwd := isolate(t)
	os.WriteFile(filepath.Join(cwd, "cfr.toml"), []byte("runs_dir = [not toml"), 0o644)
	if _, err := Load(); err == nil {
		t.Fatal("Load succeeded on malformed TOML, want error")
	}
}
