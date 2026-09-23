package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/redact"
)

// clearEnv blanks every env var Load reads so the host environment can't leak
// into assertions.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, role := range []string{"TRIAGE", "RESEARCHER", "CHALLENGER", "CHIEF"} {
		for _, field := range []string{"INPUT_BYTES", "RESPONSE_BYTES"} {
			t.Setenv("CFR_RESEARCH_"+role+"_"+field, "")
		}
	}
	for _, k := range []string{
		"CFR_RESEARCH_MODE", "CFR_RESEARCH_ROUNDS", "CFR_RESEARCH_DOCUMENTS", "CFR_RESEARCH_CANDIDATES", "CFR_RESEARCH_SHORTLIST", "CFR_RESEARCH_SOURCES_FILE", "CFR_RESEARCH_HOLIDAYS_FILE",
		"CFR_RUNS_DIR", "CFR_AGENTS_DIR", "ALPHAVANTAGE_API_KEY", "FRED_API_KEY",
		"CFR_CONTACT_EMAIL", "CFR_CLAUDE_MODEL", "CFR_GEMINI_MODEL",
		"CFR_CLAUDE_BIN", "CFR_GEMINI_BIN", "CFR_GEMINI_CONCURRENCY", "CFR_KEEP_RUNS",
		"CFR_CHEAP_ENGINE", "CFR_API_BASE_URL", "CFR_API_MODEL", "CFR_API_KEY", "DEEPSEEK_API_KEY",
		"CFR_LOCAL_BASE_URL", "CFR_LOCAL_MODEL", "CFR_LOCAL_KEY", "CFR_LOCAL_CONCURRENCY",
		"CFR_API_MAX_TOKENS", "CFR_LOCAL_MAX_TOKENS",
		"APCA_API_KEY_ID", "APCA_API_SECRET_KEY", "CFR_ALPACA_KEY_ID", "CFR_ALPACA_SECRET_KEY",
		"CFR_CHIEF_ENGINE", "CFR_CHIEF_API_BASE_URL", "CFR_CHIEF_API_MODEL", "CFR_CHIEF_API_KEY",
		"CFR_CHIEF_API_MAX_TOKENS", "CFR_CHIEF_FALLBACK_ENABLED", "CFR_CHIEF_FALLBACK_BASE_URL",
		"CFR_CHIEF_FALLBACK_MODEL", "CFR_CHIEF_FALLBACK_API_KEY", "CFR_CHIEF_FALLBACK_MAX_TOKENS",
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

// TestJitterCanBeTurnedOffByAHigherPrecedenceFile pins a one-way flag.
//
// The apply was `if f.Retry.Jitter { s.Retry.Jitter = true }`, which reads a
// value and can only ever set it. Once the global config turned jitter on, the
// project file could not turn it off — silently inverting the documented
// precedence for that one key, exactly the defect data_cache_days already
// carries a presence check for.
func TestJitterCanBeTurnedOffByAHigherPrecedenceFile(t *testing.T) {
	home, cwd := isolate(t)
	if err := os.MkdirAll(filepath.Join(home, ".config", "cfr"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(path, body string) {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(home, ".config", "cfr", "config.toml"), "[retry]\njitter = true\n")
	write(filepath.Join(cwd, "cfr.toml"), "[retry]\njitter = false\n")

	s, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if s.Retry.Jitter {
		t.Error("./cfr.toml set jitter = false and was ignored — precedence runs the wrong way")
	}
}

func TestJitterStillInheritsWhenTheLocalFileIsSilent(t *testing.T) {
	home, cwd := isolate(t)
	if err := os.MkdirAll(filepath.Join(home, ".config", "cfr"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".config", "cfr", "config.toml"),
		[]byte("[retry]\njitter = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "cfr.toml"),
		[]byte("[retry]\nmax_attempts = 3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !s.Retry.Jitter {
		t.Error("a file that says nothing about jitter must not clear it")
	}
	if s.Retry.MaxAttempts != 3 {
		t.Errorf("max_attempts = %d, want 3", s.Retry.MaxAttempts)
	}
}

// Every other [local] key is settable from the environment; max_tokens was the
// one that was not, so a local model's context had to live in a file.
func TestLocalMaxTokensFromEnv(t *testing.T) {
	isolate(t)
	t.Setenv("CFR_LOCAL_MAX_TOKENS", "16384")
	t.Setenv("CFR_API_MAX_TOKENS", "4096")

	s, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if s.Local.MaxTokens != 16384 {
		t.Errorf("Local.MaxTokens = %d, want 16384", s.Local.MaxTokens)
	}
	if s.API.MaxTokens != 4096 {
		t.Errorf("API.MaxTokens = %d, want 4096 — the two must not share a variable", s.API.MaxTokens)
	}
}

// --- Phase 1.5: an explicit zero in the [risk] block must survive ---
//
// The risk block used zero as a sentinel twice over: the loader only assigned a
// value when it was > 0, and riskgate.riskDefaults then replaced anything <= 0.
// So `cost_bps = 0` — "price this book frictionless" — came back as 30, and an
// operator had no way at all to say it. These tests pin the presence rule: a key
// the operator wrote is honoured at whatever value they wrote, and a key they
// omitted still takes the default.

func TestExplicitZeroCostBpsSurvivesTheLoader(t *testing.T) {
	_, cwd := isolate(t)
	if err := os.WriteFile(filepath.Join(cwd, "cfr.toml"), []byte("[risk]\ncost_bps = 0.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.Risk.CostBps != 0 {
		t.Errorf("CostBps = %v, want 0 (explicitly frictionless)", s.Risk.CostBps)
	}
	if !s.Risk.Set("cost_bps") {
		t.Error("cost_bps was written in the file but is not marked explicit, so riskDefaults will overwrite it")
	}
}

func TestOmittedRiskKeyIsNotMarkedExplicit(t *testing.T) {
	_, cwd := isolate(t)
	if err := os.WriteFile(filepath.Join(cwd, "cfr.toml"), []byte("[risk]\nrr_min = 2.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.Risk.Set("cost_bps") {
		t.Error("cost_bps is absent from the file but marked explicit")
	}
	if !s.Risk.Set("rr_min") || s.Risk.RRMin != 2.0 {
		t.Errorf("rr_min = %v explicit=%v, want 2.0 explicit", s.Risk.RRMin, s.Risk.Set("rr_min"))
	}
}

func TestExplicitZeroFloorFromEnvSurvivesTheLoader(t *testing.T) {
	isolate(t)
	t.Setenv("CFR_ADV_MIN_USD", "0")
	s, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.Risk.ADVMinUSD != 0 || !s.Risk.Set("adv_min_usd") {
		t.Errorf("ADVMinUSD = %v explicit=%v, want 0 explicit (liquidity floor disabled)",
			s.Risk.ADVMinUSD, s.Risk.Set("adv_min_usd"))
	}
}

func TestAZeroCeilingIsAConfigError(t *testing.T) {
	// Zero is the identity for a floor and the annihilator for a ceiling: a stop
	// band whose maximum is 0 rejects every idea ever written. That is a typo,
	// not a policy, so it is refused at load rather than honoured or silently
	// replaced.
	for _, key := range []string{"stop_sigma_max", "target_sigma_max", "max_pair_corr", "max_portfolio_beta", "account_equity", "risk_per_trade_pct"} {
		t.Run(key, func(t *testing.T) {
			_, cwd := isolate(t)
			body := "[risk]\n" + key + " = 0.0\n"
			if err := os.WriteFile(filepath.Join(cwd, "cfr.toml"), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(); err == nil {
				t.Errorf("Load accepted %s = 0, which forbids every idea", key)
			}
		})
	}
}

func TestANegativeFloorIsAConfigError(t *testing.T) {
	for _, key := range []string{"cost_bps", "rr_min", "stop_sigma_min", "adv_min_usd"} {
		t.Run(key, func(t *testing.T) {
			_, cwd := isolate(t)
			body := "[risk]\n" + key + " = -1.0\n"
			if err := os.WriteFile(filepath.Join(cwd, "cfr.toml"), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(); err == nil {
				t.Errorf("Load accepted %s = -1", key)
			}
		})
	}
}

func TestANegativeMeasuredEdgeIsAcceptedAndMarked(t *testing.T) {
	// edge_sigma_daily is a prior, not a limit: it is negative exactly when the
	// system is losing money, and refusing to express that is refusing to model
	// the case the gate exists for.
	_, cwd := isolate(t)
	if err := os.WriteFile(filepath.Join(cwd, "cfr.toml"), []byte("[risk]\nedge_sigma_daily = -0.02\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.Risk.EdgeSigmaDaily != -0.02 || !s.Risk.Set("edge_sigma_daily") {
		t.Errorf("EdgeSigmaDaily = %v explicit=%v, want -0.02 explicit",
			s.Risk.EdgeSigmaDaily, s.Risk.Set("edge_sigma_daily"))
	}
}

func TestAHigherPrecedenceFileCanZeroAFloorTheGlobalSet(t *testing.T) {
	home, cwd := isolate(t)
	global := filepath.Join(home, ".config", "cfr")
	if err := os.MkdirAll(global, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(global, "config.toml"), []byte("[risk]\ncost_bps = 45.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "cfr.toml"), []byte("[risk]\ncost_bps = 0.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.Risk.CostBps != 0 {
		t.Errorf("CostBps = %v, want 0 — the local file's explicit zero must beat the global 45", s.Risk.CostBps)
	}
}

// Alpaca is the US price and news source added after Yahoo's chart endpoint
// began answering 429. Both halves of the credential must arrive by the same
// three routes every other key does, and the CFR_-prefixed alias must win over
// Alpaca's own env convention when both are set.
func TestLoadAlpacaCredentials(t *testing.T) {
	t.Run("from file", func(t *testing.T) {
		_, cwd := isolate(t)
		toml := `
[providers]
alpaca_key_id     = "file-id"
alpaca_secret_key = "file-secret"
`
		if err := os.WriteFile(filepath.Join(cwd, "cfr.toml"), []byte(toml), 0o644); err != nil {
			t.Fatal(err)
		}
		s, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if s.Providers.AlpacaKeyID != "file-id" || s.Providers.AlpacaSecret != "file-secret" {
			t.Errorf("file keys not loaded: %+v", s.Providers)
		}
	})

	t.Run("alpaca's own env convention", func(t *testing.T) {
		isolate(t)
		t.Setenv("APCA_API_KEY_ID", "apca-id")
		t.Setenv("APCA_API_SECRET_KEY", "apca-secret")
		s, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if s.Providers.AlpacaKeyID != "apca-id" || s.Providers.AlpacaSecret != "apca-secret" {
			t.Errorf("APCA_* not read: %+v", s.Providers)
		}
	})

	t.Run("CFR_ alias wins", func(t *testing.T) {
		isolate(t)
		t.Setenv("APCA_API_KEY_ID", "apca-id")
		t.Setenv("APCA_API_SECRET_KEY", "apca-secret")
		t.Setenv("CFR_ALPACA_KEY_ID", "cfr-id")
		t.Setenv("CFR_ALPACA_SECRET_KEY", "cfr-secret")
		s, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if s.Providers.AlpacaKeyID != "cfr-id" || s.Providers.AlpacaSecret != "cfr-secret" {
			t.Errorf("CFR_ALPACA_* did not take precedence: %+v", s.Providers)
		}
	})

	t.Run("absent is not an error", func(t *testing.T) {
		isolate(t)
		s, err := Load()
		if err != nil {
			t.Fatalf("no Alpaca key must not fail the load: %v", err)
		}
		if s.Providers.AlpacaKeyID != "" || s.Providers.AlpacaSecret != "" {
			t.Errorf("keys invented from nowhere: %+v", s.Providers)
		}
	})
}

// The archetype and entry-band keys are the levers for "stop buying at the
// high", so a silent parse failure would be indistinguishable from the feature
// not working.
func TestLoadFileAppliesArchetypeAndEntryBandKeys(t *testing.T) {
	_, cwd := isolate(t)
	toml := `
prescreen_pullback_per_index = 7
prescreen_base_per_index = 4
shortlist_reserve = -1
shortlist_reserve_min_merit = 0.9

[risk]
entry_patience_sigma = 2.0
entry_chase_sigma = 0.25
`
	if err := os.WriteFile(filepath.Join(cwd, "cfr.toml"), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.PrescreenPullbackPerIndex != 7 || s.PrescreenBasePerIndex != 4 {
		t.Errorf("archetype section sizes = %d/%d, want 7/4",
			s.PrescreenPullbackPerIndex, s.PrescreenBasePerIndex)
	}
	// Negative is the documented way to turn the reserve off, so this key
	// cannot be presence-detected by sign the way the other counts are.
	if s.ShortlistReserve != -1 {
		t.Errorf("shortlist_reserve = %d, want -1 to survive as the off switch", s.ShortlistReserve)
	}
	if s.ShortlistReserveMinMerit != 0.9 {
		t.Errorf("shortlist_reserve_min_merit = %v, want 0.9", s.ShortlistReserveMinMerit)
	}
	if s.Risk.EntryPatienceSigma != 2.0 || s.Risk.EntryChaseSigma != 0.25 {
		t.Errorf("entry bands = %v/%v, want 2.0/0.25",
			s.Risk.EntryPatienceSigma, s.Risk.EntryChaseSigma)
	}
	for _, key := range []string{"entry_patience_sigma", "entry_chase_sigma"} {
		if !s.Risk.Set(key) {
			t.Errorf("%s not recorded as explicitly set", key)
		}
	}
}

func TestThesisConfigPrecedenceAndBudgetValidation(t *testing.T) {
	_, cwd := isolate(t)
	if err := os.WriteFile(filepath.Join(cwd, "cfr.toml"), []byte("research_mode = \"thesis\"\n[research]\nrounds=2\ndocuments=6\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CFR_RESEARCH_ROUNDS", "4")
	s, e := Load()
	if e != nil {
		t.Fatal(e)
	}
	if s.ResearchMode != "thesis" || s.Research.Rounds != 4 || s.Research.Documents != 6 || s.Research.Shortlist != 12 {
		t.Fatalf("wrong resolution: %+v", s.Research)
	}
	t.Setenv("CFR_RESEARCH_ROUNDS", "99")
	if _, e = Load(); e == nil {
		t.Fatal("accepted unbounded research")
	}
}

func TestResearchRoleBudgetPrecedenceAndValidation(t *testing.T) {
	_, cwd := isolate(t)
	if err := os.WriteFile(filepath.Join(cwd, "cfr.toml"), []byte("[research.budgets.researcher]\ninput_bytes=65536\nresponse_bytes=16384\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if s.Research.Budgets.Researcher.InputBytes != 65536 || s.Research.Budgets.Chief.InputBytes != 192<<10 {
		t.Fatal("file/default budget merge failed")
	}
	t.Setenv("CFR_RESEARCH_RESEARCHER_INPUT_BYTES", "73728")
	s, err = Load()
	if err != nil || s.Research.Budgets.Researcher.InputBytes != 73728 {
		t.Fatalf("env override: %v", err)
	}
	t.Setenv("CFR_RESEARCH_CHIEF_RESPONSE_BYTES", "2097152")
	if _, err = Load(); err == nil {
		t.Fatal("invalid response budget accepted")
	}
}

// TestChiefEngineMigrationMatrix pins the chief_engine selector's compatibility
// matrix: the default stays "claude" with today's fallback-gating behaviour
// when the key is omitted, "api" requires its own dedicated [chief_api]
// credentials, and the two are mutually exclusive with an explicitly-enabled
// [chief_fallback] (that resilience call only makes sense behind a Claude
// primary). Every case writes its TOML into t.TempDir() — the repo's own
// ./cfr.toml carries live credentials and must never be reachable here.
func TestChiefEngineMigrationMatrix(t *testing.T) {
	cases := []struct {
		name, toml   string
		env          map[string]string
		wantEngine   string
		wantFallback bool
		wantErr      string
	}{
		{name: "no selector, no fallback", toml: ``, wantEngine: "claude"},
		{name: "no selector, fallback key, enabled omitted",
			toml:       "[chief_fallback]\nbase_url=\"https://x/v1\"\nmodel=\"m\"\napi_key=\"k\"\n",
			wantEngine: "claude", wantFallback: true},
		{name: "claude with fallback explicitly false",
			toml:       "chief_engine=\"claude\"\n[chief_fallback]\nenabled=false\nbase_url=\"https://x/v1\"\nmodel=\"m\"\napi_key=\"k\"\n",
			wantEngine: "claude", wantFallback: false},
		// R28: the sixth truth-table row, tested at neither layer before this.
		// enabled=false (above) and enabled omitted (further above) both take
		// the disabled/nil-defaults-active paths; this is the third row,
		// enabled=true explicit, and it must take the SAME active path as
		// omitted rather than a special-cased one that could silently diverge.
		{name: "claude with fallback explicitly true",
			toml:       "chief_engine=\"claude\"\n[chief_fallback]\nenabled=true\nbase_url=\"https://x/v1\"\nmodel=\"m\"\napi_key=\"k\"\n",
			wantEngine: "claude", wantFallback: true},
		{name: "api with valid dedicated settings",
			toml:       "chief_engine=\"api\"\n[chief_api]\nbase_url=\"https://y/v1\"\nmodel=\"n\"\napi_key=\"k2\"\n",
			wantEngine: "api", wantFallback: false},
		{name: "api missing model",
			toml:    "chief_engine=\"api\"\n[chief_api]\nbase_url=\"https://y/v1\"\napi_key=\"k2\"\n",
			wantErr: "chief_api.model"},
		{name: "api with fallback explicitly true",
			toml:    "chief_engine=\"api\"\n[chief_api]\nbase_url=\"https://y/v1\"\nmodel=\"n\"\napi_key=\"k2\"\n[chief_fallback]\nenabled=true\nbase_url=\"https://x/v1\"\nmodel=\"m\"\napi_key=\"k\"\n",
			wantErr: "fallback is supported only after a Claude primary",
		},
		// The exact shape of this repo's own ./cfr.toml: a [chief_fallback]
		// block left over from chief_engine="claude" use, with base_url, model
		// and api_key set but no `enabled` key at all. Compatibility matrix row
		// 4 (docs/plans/2026-09-15-deepseek-chief-and-research-reliability.md:
		// 157-165): the run PROCEEDS as "Configured API Chief, fallback
		// Disabled" — leftover credentials with `enabled` omitted must not
		// block adding chief_engine="api" to an existing config. Whether those
		// retained credentials could ever fire is a runtime guarantee, not a
		// config-time one.
		{name: "api with leftover fallback credentials, enabled omitted",
			toml:       "chief_engine=\"api\"\n[chief_api]\nbase_url=\"https://y/v1\"\nmodel=\"n\"\napi_key=\"k2\"\n[chief_fallback]\nbase_url=\"https://x/v1\"\nmodel=\"m\"\napi_key=\"k\"\nmax_tokens=32768\n",
			wantEngine: "api", wantFallback: false},
		{name: "invalid enum", toml: "chief_engine=\"gpt\"\n", wantErr: "chief_engine"},
		{name: "env overrides file",
			toml: "chief_engine=\"claude\"\n",
			env: map[string]string{"CFR_CHIEF_ENGINE": "api",
				"CFR_CHIEF_API_BASE_URL": "https://z/v1", "CFR_CHIEF_API_MODEL": "q",
				"CFR_CHIEF_API_KEY": "k3"},
			wantEngine: "api"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, cwd := isolate(t)
			if err := os.WriteFile(filepath.Join(cwd, "cfr.toml"), []byte(tc.toml), 0o644); err != nil {
				t.Fatal(err)
			}
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			s, err := Load()
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("Load: got no error, want one containing %q", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("Load error = %q, want it to contain %q", err.Error(), tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if s.ChiefEngine != tc.wantEngine {
				t.Errorf("ChiefEngine = %q, want %q", s.ChiefEngine, tc.wantEngine)
			}
			if got := s.ChiefFallbackActive(); got != tc.wantFallback {
				t.Errorf("ChiefFallbackActive() = %v, want %v", got, tc.wantFallback)
			}
		})
	}
}

// TestChiefEngineEmptyStringPreservesExistingBehaviour pins the "omitting the
// selector changes nothing" constraint explicitly, independent of the fallback
// axis the matrix above already covers.
func TestChiefEngineEmptyStringPreservesExistingBehaviour(t *testing.T) {
	_, cwd := isolate(t)
	if err := os.WriteFile(filepath.Join(cwd, "cfr.toml"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.ChiefEngine != "claude" {
		t.Errorf("ChiefEngine = %q, want the default claude", s.ChiefEngine)
	}
	if s.ChiefAPI != (model.APIConfig{}) {
		t.Errorf("ChiefAPI = %+v, want zero value when unconfigured", s.ChiefAPI)
	}
}

// TestLoadAllowsAPIChiefAlongsideAPICheapEngine confirms the two engine
// selectors are independent: turning the cheap-research role onto the API
// engine must not interact with, or be required by, the Chief's own selector.
func TestLoadAllowsAPIChiefAlongsideAPICheapEngine(t *testing.T) {
	_, cwd := isolate(t)
	toml := `
cheap_engine = "api"
chief_engine = "api"

[api]
base_url = "https://cheap.invalid/v1"
model = "cheap-model"
api_key = "cheap-key"

[chief_api]
base_url = "https://chief.invalid/v1"
model = "chief-model"
api_key = "chief-key"
`
	if err := os.WriteFile(filepath.Join(cwd, "cfr.toml"), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.API.APIKey != "cheap-key" || s.ChiefAPI.APIKey != "chief-key" {
		t.Errorf("API/ChiefAPI keys crossed: API=%+v ChiefAPI=%+v", s.API, s.ChiefAPI)
	}
}

func TestManifestConfigHonorsChiefEndpointEnvironmentOverrides(t *testing.T) {
	home, cwd := isolate(t)
	dir := filepath.Join(home, ".config", "cfr")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("[chief_api]\nbase_url='https://user.example.test'\nmodel='user-model'\napi_key='user-manifest-credential'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "cfr.toml"), []byte("chief_engine='api'\n[chief_api]\nbase_url='https://project.example.test'\nmodel='project-model'\napi_key='project-manifest-credential'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CFR_CHIEF_API_BASE_URL", "https://chief.example.test")
	t.Setenv("CFR_CHIEF_API_MODEL", "fixture-chief-model")
	t.Setenv("CFR_CHIEF_API_KEY", "environment-manifest-credential")
	s, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if s.ChiefAPI.BaseURL != "https://chief.example.test" || s.ChiefAPI.Model != "fixture-chief-model" {
		t.Fatal("environment did not win")
	}
	for _, secret := range []string{"user-manifest-credential", "project-manifest-credential", "environment-manifest-credential"} {
		if !redact.ContainsCredential(secret) {
			t.Fatal("overridden credential not registered")
		}
	}
}

func TestCompactionReasoningEffortIsReadAndValidated(t *testing.T) {
	for _, c := range []struct{ value, wantErr string }{
		{"adaptive", ""}, {"low", ""}, {"high", ""}, {"max", ""}, {"", ""}, {"medium", "compaction_reasoning_effort"},
	} {
		s := &Settings{ChiefEngine: "api", ChiefAPI: model.APIConfig{BaseURL: "https://x/v1", Model: "m", APIKey: "k", CompactionEffort: c.value}}
		err := s.ValidateChiefEngine()
		if c.wantErr == "" && err != nil {
			t.Errorf("%q rejected: %v", c.value, err)
		}
		if c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)) {
			t.Errorf("%q: err %v, want mention of %s", c.value, err, c.wantErr)
		}
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "cfr.toml")
	if err := os.WriteFile(path, []byte("[chief_api]\ncompaction_reasoning_effort = \"adaptive\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s := &Settings{}
	if err := s.applyFile(path); err != nil {
		t.Fatal(err)
	}
	if s.ChiefAPI.CompactionEffort != "adaptive" {
		t.Fatalf("file value not read: %q", s.ChiefAPI.CompactionEffort)
	}
}
