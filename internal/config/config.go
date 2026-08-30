// Package config resolves app settings from, in increasing precedence:
// built-in defaults → ~/.config/cfr/config.toml → ./cfr.toml → environment
// variables. Command-line flags (headless mode) override on top of the result.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/mamut/claude-financial-researcher/internal/model"
)

// Settings is the fully resolved configuration.
type Settings struct {
	AgentsDir string
	RunsDir   string
	DataDir   string
	KeepRuns  int
	Workers   int
	Indices   []string

	// PriceTTL is how long a cached daily price series is served before being
	// refetched. The cache used to be scoped to the UTC calendar day and nothing
	// finer, so a run started at 21:00 priced its ideas off an entry written at
	// 04:00. Zero means the marketdata default (4h).
	PriceTTL time.Duration
	// DataCacheDays is how long cached provider responses are kept on disk before
	// the run-start janitor removes them. Keys are date-scoped, so older entries
	// can never be read again — they merely accumulate. Zero disables pruning.
	DataCacheDays int

	// Funnel geometry. PrescreenTopPerIndex is how many of each index's
	// highest-composite names the Stage 0.5 table shows its scout;
	// MaxShortlist caps the merged shortlist and MaxPerIndex caps one index's
	// share of it. ADVMinUSD is the 20-day average dollar volume below which a
	// name is not tradeable in size and never reaches a model. Zero means the
	// orchestrator's defaults (15 / 12 / 5 / $20M).
	PrescreenTopPerIndex int
	MaxShortlist         int
	MaxPerIndex          int
	ADVMinUSD            float64

	GeminiConcurrency int
	Weights           model.DomainWeights
	Timeouts          model.StageTimeouts
	Retry             model.RetryPolicy
	Providers         model.ProviderConfig
	Models            map[model.CLI]string
	Binaries          map[model.CLI]string

	// CheapEngine selects the engine for scouts+specialists ("gemini" | "api" |
	// "local"); empty defaults to "gemini" (the agy CLI). API configures the remote
	// OpenAI-compatible engine ("api"); Local configures a local OpenAI-compatible
	// server such as Ollama ("local"), throttled by LocalConcurrency.
	CheapEngine      model.CLI
	API              model.APIConfig
	Local            model.APIConfig
	LocalConcurrency int
}

// fileFormat is the TOML shape of cfr.toml. All fields optional.
type fileFormat struct {
	AgentsDir string   `toml:"agents_dir"`
	RunsDir   string   `toml:"runs_dir"`
	DataDir   string   `toml:"data_dir"`
	KeepRuns  int      `toml:"keep_runs"`
	Workers   int      `toml:"workers"`
	Indices   []string `toml:"indices"`

	PriceTTL      string `toml:"price_ttl"`       // Go duration, e.g. "4h"
	DataCacheDays int    `toml:"data_cache_days"` // 0 disables pruning

	PrescreenTopPerIndex int `toml:"prescreen_top_per_index"`
	MaxShortlist         int `toml:"max_shortlist"`
	MaxPerIndex          int `toml:"max_per_index"`

	GeminiConcurrency int `toml:"gemini_concurrency"`

	// CheapEngine routes scouts+specialists: "gemini" (agy CLI, default), "api"
	// (remote OpenAI-compatible HTTP), or "local" (local OpenAI-compatible server).
	CheapEngine string `toml:"cheap_engine"`

	// LocalConcurrency caps simultaneous local-model calls (default 1).
	LocalConcurrency int `toml:"local_concurrency"`

	Weights struct {
		Fundamentals float64 `toml:"fundamentals"`
		Quant        float64 `toml:"quant"`
		News         float64 `toml:"news"`
		Macro        float64 `toml:"macro"`
		Sentiment    float64 `toml:"sentiment"`
	} `toml:"weights"`

	Timeouts struct {
		Screening string `toml:"screening"` // Go duration strings, e.g. "5m"
		Analysis  string `toml:"analysis"`
		Synthesis string `toml:"synthesis"`
	} `toml:"timeouts"`

	Retry struct {
		MaxAttempts int    `toml:"max_attempts"`
		BaseDelay   string `toml:"base_delay"`
		MaxDelay    string `toml:"max_delay"`
		Jitter      bool   `toml:"jitter"`
	} `toml:"retry"`

	Models struct {
		Claude string `toml:"claude"`
		Gemini string `toml:"gemini"`
	} `toml:"models"`

	Binaries struct {
		Claude string `toml:"claude"`
		Gemini string `toml:"gemini"`
	} `toml:"binaries"`

	Providers struct {
		ContactEmail    string `toml:"contact_email"`
		AlphaVantageKey string `toml:"alphavantage_key"`
		FredKey         string `toml:"fred_key"`
	} `toml:"providers"`

	// Risk holds the tradeability and sizing limits. Only the liquidity floor is
	// consumed today (the pre-screen drops anything below it); the rest of the
	// table arrives with the risk gate.
	Risk struct {
		ADVMinUSD float64 `toml:"adv_min_usd"`
	} `toml:"risk"`

	// API configures the remote OpenAI-compatible cheap-research engine. Prefer
	// setting api_key via the CFR_API_KEY env var rather than committing it to a file.
	API struct {
		BaseURL   string `toml:"base_url"`
		Model     string `toml:"model"`
		APIKey    string `toml:"api_key"`
		MaxTokens int    `toml:"max_tokens"`
	} `toml:"api"`

	// Local configures a local OpenAI-compatible server (Ollama/llama.cpp). The
	// key is optional — local servers don't authenticate.
	Local struct {
		BaseURL   string `toml:"base_url"`
		Model     string `toml:"model"`
		APIKey    string `toml:"api_key"`
		MaxTokens int    `toml:"max_tokens"`
	} `toml:"local"`
}

// Load resolves the settings. Missing config files are fine; a malformed file
// is an error (silently ignoring a typo'd config is worse than failing).
func Load() (*Settings, error) {
	s := &Settings{
		AgentsDir: "agents",
		RunsDir:   "runs",
		DataDir:   ".data",
		KeepRuns:  100,
		PriceTTL:  4 * time.Hour,

		// A week keeps a few days of runs re-runnable offline without letting a
		// universe-wide pre-screen's few hundred files a day pile up forever.
		DataCacheDays: 7,
		Models:        map[model.CLI]string{},
		Binaries:      map[model.CLI]string{},
	}

	var paths []string
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".config", "cfr", "config.toml"))
	}
	paths = append(paths, "cfr.toml")

	for _, p := range paths {
		if err := s.applyFile(p); err != nil {
			return nil, err
		}
	}
	s.applyEnv()
	return s, nil
}

func (s *Settings) applyFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var f fileFormat
	if err := toml.Unmarshal(data, &f); err != nil {
		return fmt.Errorf("config %s: %w", path, err)
	}

	setStr := func(dst *string, v string) {
		if v != "" {
			*dst = v
		}
	}
	setInt := func(dst *int, v int) {
		if v > 0 {
			*dst = v
		}
	}
	setStr(&s.AgentsDir, f.AgentsDir)
	setStr(&s.RunsDir, f.RunsDir)
	setStr(&s.DataDir, f.DataDir)
	setInt(&s.KeepRuns, f.KeepRuns)
	setInt(&s.Workers, f.Workers)
	setInt(&s.DataCacheDays, f.DataCacheDays)
	setInt(&s.GeminiConcurrency, f.GeminiConcurrency)
	setInt(&s.PrescreenTopPerIndex, f.PrescreenTopPerIndex)
	setInt(&s.MaxShortlist, f.MaxShortlist)
	setInt(&s.MaxPerIndex, f.MaxPerIndex)
	if f.Risk.ADVMinUSD > 0 {
		s.ADVMinUSD = f.Risk.ADVMinUSD
	}
	if len(f.Indices) > 0 {
		s.Indices = f.Indices
	}

	if f.Weights.Fundamentals+f.Weights.Quant+f.Weights.News+f.Weights.Macro+f.Weights.Sentiment > 0 {
		s.Weights = model.DomainWeights{
			Fundamentals: f.Weights.Fundamentals,
			Quant:        f.Weights.Quant,
			News:         f.Weights.News,
			Macro:        f.Weights.Macro,
			Sentiment:    f.Weights.Sentiment,
		}
	}

	parseDur := func(dst *time.Duration, v, field string) error {
		if v == "" {
			return nil
		}
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("config %s: %s: %w", path, field, err)
		}
		*dst = d
		return nil
	}
	if err := parseDur(&s.Timeouts.Screening, f.Timeouts.Screening, "timeouts.screening"); err != nil {
		return err
	}
	if err := parseDur(&s.Timeouts.Analysis, f.Timeouts.Analysis, "timeouts.analysis"); err != nil {
		return err
	}
	if err := parseDur(&s.Timeouts.Synthesis, f.Timeouts.Synthesis, "timeouts.synthesis"); err != nil {
		return err
	}
	setInt(&s.Retry.MaxAttempts, f.Retry.MaxAttempts)
	if err := parseDur(&s.Retry.BaseDelay, f.Retry.BaseDelay, "retry.base_delay"); err != nil {
		return err
	}
	if err := parseDur(&s.Retry.MaxDelay, f.Retry.MaxDelay, "retry.max_delay"); err != nil {
		return err
	}
	if err := parseDur(&s.PriceTTL, f.PriceTTL, "price_ttl"); err != nil {
		return err
	}
	if f.Retry.Jitter {
		s.Retry.Jitter = true
	}

	if f.Models.Claude != "" {
		s.Models[model.CLIClaude] = f.Models.Claude
	}
	if f.Models.Gemini != "" {
		s.Models[model.CLIGemini] = f.Models.Gemini
	}
	if f.Binaries.Claude != "" {
		s.Binaries[model.CLIClaude] = f.Binaries.Claude
	}
	if f.Binaries.Gemini != "" {
		s.Binaries[model.CLIGemini] = f.Binaries.Gemini
	}
	setStr(&s.Providers.ContactEmail, f.Providers.ContactEmail)
	setStr(&s.Providers.AlphaVantageKey, f.Providers.AlphaVantageKey)
	setStr(&s.Providers.FredKey, f.Providers.FredKey)

	if f.CheapEngine != "" {
		s.CheapEngine = model.CLI(f.CheapEngine)
	}
	setInt(&s.LocalConcurrency, f.LocalConcurrency)
	setStr(&s.API.BaseURL, f.API.BaseURL)
	setStr(&s.API.Model, f.API.Model)
	setStr(&s.API.APIKey, f.API.APIKey)
	setInt(&s.API.MaxTokens, f.API.MaxTokens)
	setStr(&s.Local.BaseURL, f.Local.BaseURL)
	setStr(&s.Local.Model, f.Local.Model)
	setStr(&s.Local.APIKey, f.Local.APIKey)
	setInt(&s.Local.MaxTokens, f.Local.MaxTokens)
	return nil
}

// applyEnv lets the established CFR_* environment variables win over files.
func (s *Settings) applyEnv() {
	setStr := func(dst *string, key string) {
		if v := os.Getenv(key); v != "" {
			*dst = v
		}
	}
	setStr(&s.RunsDir, "CFR_RUNS_DIR")
	setStr(&s.AgentsDir, "CFR_AGENTS_DIR")
	setStr(&s.Providers.AlphaVantageKey, "ALPHAVANTAGE_API_KEY")
	setStr(&s.Providers.FredKey, "FRED_API_KEY")
	setStr(&s.Providers.ContactEmail, "CFR_CONTACT_EMAIL")

	// Cheap-research API engine. CFR_API_KEY is preferred; DEEPSEEK_API_KEY is
	// accepted as an alias so a DeepSeek key already in the environment just works.
	setStr(&s.API.BaseURL, "CFR_API_BASE_URL")
	setStr(&s.API.Model, "CFR_API_MODEL")
	setStr(&s.API.APIKey, "DEEPSEEK_API_KEY")
	setStr(&s.API.APIKey, "CFR_API_KEY")
	if v := os.Getenv("CFR_CHEAP_ENGINE"); v != "" {
		s.CheapEngine = model.CLI(v)
	}

	// Local OpenAI-compatible server (Ollama/llama.cpp). Key is optional.
	setStr(&s.Local.BaseURL, "CFR_LOCAL_BASE_URL")
	setStr(&s.Local.Model, "CFR_LOCAL_MODEL")
	setStr(&s.Local.APIKey, "CFR_LOCAL_KEY")
	if v := os.Getenv("CFR_API_MAX_TOKENS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			s.API.MaxTokens = n
		}
	}
	if v := os.Getenv("CFR_LOCAL_CONCURRENCY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			s.LocalConcurrency = n
		}
	}

	if v := os.Getenv("CFR_CLAUDE_MODEL"); v != "" {
		s.Models[model.CLIClaude] = v
	}
	if v := os.Getenv("CFR_GEMINI_MODEL"); v != "" {
		s.Models[model.CLIGemini] = v
	}
	if v := os.Getenv("CFR_CLAUDE_BIN"); v != "" {
		s.Binaries[model.CLIClaude] = v
	}
	if v := os.Getenv("CFR_GEMINI_BIN"); v != "" {
		s.Binaries[model.CLIGemini] = v
	}
	if v := os.Getenv("CFR_GEMINI_CONCURRENCY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			s.GeminiConcurrency = n
		}
	}
	if v := os.Getenv("CFR_KEEP_RUNS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			s.KeepRuns = n
		}
	}
	if v := os.Getenv("CFR_PRICE_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			s.PriceTTL = d
		}
	}
	if v := os.Getenv("CFR_DATA_CACHE_DAYS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			s.DataCacheDays = n
		}
	}
	setPosInt := func(dst *int, key string) {
		if v := os.Getenv(key); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				*dst = n
			}
		}
	}
	setPosInt(&s.PrescreenTopPerIndex, "CFR_PRESCREEN_TOP_PER_INDEX")
	setPosInt(&s.MaxShortlist, "CFR_MAX_SHORTLIST")
	setPosInt(&s.MaxPerIndex, "CFR_MAX_PER_INDEX")
	if v := os.Getenv("CFR_ADV_MIN_USD"); v != "" {
		if x, err := strconv.ParseFloat(v, 64); err == nil && x > 0 {
			s.ADVMinUSD = x
		}
	}
}
