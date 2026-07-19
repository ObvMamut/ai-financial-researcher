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

	GeminiConcurrency int
	Weights           model.DomainWeights
	Timeouts          model.StageTimeouts
	Retry             model.RetryPolicy
	Providers         model.ProviderConfig
	Models            map[model.CLI]string
	Binaries          map[model.CLI]string
}

// fileFormat is the TOML shape of cfr.toml. All fields optional.
type fileFormat struct {
	AgentsDir string   `toml:"agents_dir"`
	RunsDir   string   `toml:"runs_dir"`
	DataDir   string   `toml:"data_dir"`
	KeepRuns  int      `toml:"keep_runs"`
	Workers   int      `toml:"workers"`
	Indices   []string `toml:"indices"`

	GeminiConcurrency int `toml:"gemini_concurrency"`

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
}

// Load resolves the settings. Missing config files are fine; a malformed file
// is an error (silently ignoring a typo'd config is worse than failing).
func Load() (*Settings, error) {
	s := &Settings{
		AgentsDir: "agents",
		RunsDir:   "runs",
		DataDir:   ".data",
		KeepRuns:  100,
		Models:    map[model.CLI]string{},
		Binaries:  map[model.CLI]string{},
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
	setInt(&s.GeminiConcurrency, f.GeminiConcurrency)
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
}
