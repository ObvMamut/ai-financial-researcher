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
	"github.com/mamut/claude-financial-researcher/internal/redact"
)

// Settings is the fully resolved configuration.
type Settings struct {
	ResearchMode string
	Research     model.ResearchConfig
	AgentsDir    string
	RunsDir      string
	DataDir      string
	KeepRuns     int
	Workers      int
	Indices      []string

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
	// MaxShortlist caps the merged shortlist, MaxPerIndex caps one index's
	// share of it, and MaxThinlyCovered caps the names the run's sources can
	// ground less than 60% of the domain weight for. Zero means the
	// orchestrator's defaults (15 / 12 / 5), and for MaxThinlyCovered zero is
	// itself the default — such a name cannot clear the risk gate's evidence
	// floor, so a slot spent on one is a slot spent on a guaranteed deletion.
	// A negative value re-admits them for an experiment.
	PrescreenTopPerIndex int
	// PrescreenPullbackPerIndex and PrescreenBasePerIndex size the archetype
	// sections the scout table carries alongside the top-of-ranking one, **per
	// direction**: each is rendered as a long half and a short half, so these
	// count rows per half. Zero means the orchestrator's defaults (5 / 3).
	PrescreenPullbackPerIndex int
	PrescreenBasePerIndex     int
	// PrescreenDriftPerIndex sizes the earnings-drift section, also per
	// direction. Zero means the orchestrator's default (5).
	PrescreenDriftPerIndex int
	MaxShortlist           int
	MaxPerIndex            int
	MaxThinlyCovered       int
	// ShortlistReserve holds slots in the shortlist for non-continuation
	// archetypes, and ShortlistReserveMinMerit is the composite z a candidate
	// needs to take one. Zero means the orchestrator's defaults (3 / 0.5);
	// a negative reserve disables it.
	ShortlistReserve         int
	ShortlistReserveMinMerit float64

	// Risk is the deterministic post-synthesis risk policy. Zero fields take the
	// orchestrator's defaults. Risk.ADVMinUSD also gates the Stage 0.5
	// pre-screen: a name too thin to trade should not reach a model either.
	Risk model.RiskConfig

	// ChiefAdjustBand is how far, in confidence points, the Chief Analyst may
	// move an idea from its computed base score before the orchestrator clamps
	// it. Zero means 10.
	ChiefAdjustBand int

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

	// FillWindowDays is how many sessions the scoreboard's replay keeps a limit
	// entry live before calling the idea unfilled. Zero means 3.
	FillWindowDays int

	// SynthesisMaxAttempts overrides Retry.MaxAttempts for the primary Chief
	// Analyst call only. Zero means 1 (no retry) — a synthesis timeout means
	// "too slow," not "flaky."
	SynthesisMaxAttempts int
	// ChiefFallback configures the optional DeepSeek resilience call attempted
	// when the primary Chief Analyst call fails or its JSON fails to parse. Off
	// by default (gated on its own api_key), and never shared with API/Local —
	// turning on cheap_engine=api must never silently also enable this spend.
	ChiefFallback model.APIConfig
	// ChiefFallbackEnabled is a three-state override on top of the api_key gate
	// above: nil (the key omitted) preserves today's behaviour — enabled once
	// ChiefFallback.APIKey is set — while a non-nil value wins outright, so an
	// operator can force the fallback off even with credentials present. It
	// must stay *bool rather than bool: a plain bool cannot distinguish
	// "omitted" from "false" and would silently disable every existing user's
	// fallback the moment this field shipped.
	ChiefFallbackEnabled *bool

	// ChiefEngine selects the Chief Analyst synthesis engine: "claude" (the
	// `claude` CLI; also the meaning of "") or "api" (the same stdlib
	// OpenAI-compatible engine as CheapEngine's "api"/"local", routed through
	// ChiefAPI instead). Omitting the key preserves the pipeline's existing
	// behaviour exactly — the Chief stays on the Claude CLI.
	ChiefEngine string
	// ChiefAPI is the Chief's own dedicated OpenAI-compatible credentials, read
	// only when ChiefEngine == "api". It deliberately never inherits from
	// API, Local, or DEEPSEEK_API_KEY: turning the cheap-research role onto the
	// API engine (cheap_engine = "api") must never silently also turn on
	// Chief-tier spend, which runs on a different cost tier for the single most
	// important step in the pipeline.
	ChiefAPI model.APIConfig
}

// fileFormat is the TOML shape of cfr.toml. All fields optional.
type fileFormat struct {
	ResearchMode string               `toml:"research_mode"`
	Research     model.ResearchConfig `toml:"research"`
	AgentsDir    string               `toml:"agents_dir"`
	RunsDir      string               `toml:"runs_dir"`
	DataDir      string               `toml:"data_dir"`
	KeepRuns     int                  `toml:"keep_runs"`
	Workers      int                  `toml:"workers"`
	Indices      []string             `toml:"indices"`

	PriceTTL      string `toml:"price_ttl"`       // Go duration, e.g. "4h"
	DataCacheDays int    `toml:"data_cache_days"` // 0 disables pruning

	PrescreenTopPerIndex      int     `toml:"prescreen_top_per_index"`
	PrescreenPullbackPerIndex int     `toml:"prescreen_pullback_per_index"`
	PrescreenBasePerIndex     int     `toml:"prescreen_base_per_index"`
	PrescreenDriftPerIndex    int     `toml:"prescreen_drift_per_index"`
	MaxShortlist              int     `toml:"max_shortlist"`
	MaxPerIndex               int     `toml:"max_per_index"`
	MaxThinlyCovered          int     `toml:"max_thinly_covered"`
	ShortlistReserve          int     `toml:"shortlist_reserve"`
	ShortlistReserveMinMerit  float64 `toml:"shortlist_reserve_min_merit"`
	ChiefAdjustBand           int     `toml:"chief_adjust_band"`

	GeminiConcurrency int `toml:"gemini_concurrency"`

	// CheapEngine routes scouts+specialists: "gemini" (agy CLI, default), "api"
	// (remote OpenAI-compatible HTTP), or "local" (local OpenAI-compatible server).
	CheapEngine string `toml:"cheap_engine"`

	// ChiefEngine routes the Chief Analyst synthesis step: "claude" (default,
	// the `claude` CLI) or "api" (the dedicated [chief_api] HTTP engine below).
	ChiefEngine string `toml:"chief_engine"`

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
		Screening         string `toml:"screening"` // Go duration strings, e.g. "5m"
		Analysis          string `toml:"analysis"`
		Synthesis         string `toml:"synthesis"`
		SynthesisFallback string `toml:"synthesis_fallback"` // empty = same as synthesis
	} `toml:"timeouts"`

	Retry struct {
		MaxAttempts int    `toml:"max_attempts"`
		BaseDelay   string `toml:"base_delay"`
		MaxDelay    string `toml:"max_delay"`
		Jitter      bool   `toml:"jitter"`
		// SynthesisMaxAttempts overrides MaxAttempts for the primary Chief
		// Analyst call only (see Settings.SynthesisMaxAttempts).
		SynthesisMaxAttempts int `toml:"synthesis_max_attempts"`
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
		AlpacaKeyID     string `toml:"alpaca_key_id"`
		AlpacaSecretKey string `toml:"alpaca_secret_key"`
	} `toml:"providers"`

	// Risk holds the tradeability, geometry and sizing limits enforced after
	// synthesis (docs/workflow/scoring.md). Every one of these was a preference
	// in a persona before it was a number here.
	Risk struct {
		AccountEquity      float64 `toml:"account_equity"`
		RiskPerTradePct    float64 `toml:"risk_per_trade_pct"`
		CostBps            float64 `toml:"cost_bps"`
		RRMin              float64 `toml:"rr_min"`
		StopSigmaMin       float64 `toml:"stop_sigma_min"`
		StopSigmaMax       float64 `toml:"stop_sigma_max"`
		TargetSigmaMax     float64 `toml:"target_sigma_max"`
		EntryPatienceSigma float64 `toml:"entry_patience_sigma"`
		EntryChaseSigma    float64 `toml:"entry_chase_sigma"`
		ADVMinUSD          float64 `toml:"adv_min_usd"`
		MaxPairCorr        float64 `toml:"max_pair_corr"`
		MaxPortfolioBeta   float64 `toml:"max_portfolio_beta"`
		MaxPerSector       int     `toml:"max_per_sector"`
		EdgeSigmaDaily     float64 `toml:"edge_sigma_daily"`
		MinExpectancyR     float64 `toml:"min_expectancy_r"`
		MinExpectancyBps   float64 `toml:"min_expectancy_bps"`
	} `toml:"risk"`

	// API configures the remote OpenAI-compatible cheap-research engine. Prefer
	// setting api_key via the CFR_API_KEY env var rather than committing it to a file.
	API struct {
		BaseURL   string `toml:"base_url"`
		Model     string `toml:"model"`
		APIKey    string `toml:"api_key"`
		MaxTokens int    `toml:"max_tokens"`
	} `toml:"api"`

	// Scoreboard tunes how past ideas are replayed.
	Scoreboard struct {
		FillWindowDays int `toml:"fill_window_days"`
	} `toml:"scoreboard"`

	// ChiefFallback configures the optional DeepSeek resilience call for the
	// Chief Analyst synthesis step (off by default; see Settings.ChiefFallback).
	// Deliberately its own dedicated credentials, never shared with [api]/[local]:
	// prefer CFR_CHIEF_FALLBACK_API_KEY over committing api_key to this file.
	ChiefFallback struct {
		// Enabled is *bool, not bool: omitted (nil) keeps today's behaviour
		// (enabled once APIKey is set), while an explicit true/false overrides
		// it either way. See Settings.ChiefFallbackEnabled.
		Enabled   *bool  `toml:"enabled"`
		BaseURL   string `toml:"base_url"`
		Model     string `toml:"model"`
		APIKey    string `toml:"api_key"`
		MaxTokens int    `toml:"max_tokens"`
	} `toml:"chief_fallback"`

	// ChiefAPI is the Chief Analyst's dedicated OpenAI-compatible engine (used
	// only when chief_engine = "api"). Deliberately its own block, never
	// merged with [api]/[local]: see Settings.ChiefAPI.
	ChiefAPI struct {
		BaseURL   string `toml:"base_url"`
		Model     string `toml:"model"`
		APIKey    string `toml:"api_key"`
		MaxTokens int    `toml:"max_tokens"`
		// CompactionReasoningEffort sets reasoning_effort on dossier
		// compaction calls: "adaptive" (low for shallow cuts, high for deep
		// ones), a fixed "low" / "high" / "max", or "" to send none.
		CompactionReasoningEffort string `toml:"compaction_reasoning_effort"`
	} `toml:"chief_api"`

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
		// Register after every file, not only once at the end: a later file
		// that fails to parse makes Load return here, before the final call
		// below ever runs, and any secret an earlier file already populated
		// into s must still be redacted in whatever the caller does with that
		// returned error (e.g. logging it further up the stack).
		s.registerSecrets()
	}
	s.applyEnv()
	s.registerSecrets()
	if s.ResearchMode == "" {
		s.ResearchMode = "legacy"
	}
	if s.ResearchMode != "legacy" && s.ResearchMode != "thesis" {
		return nil, fmt.Errorf("research_mode must be legacy or thesis")
	}
	s.Research = s.Research.Defaults()
	if err := s.Research.Budgets.Validate(); err != nil {
		return nil, err
	}
	if s.Research.Rounds < 1 || s.Research.Rounds > 6 || s.Research.Documents < 1 || s.Research.Documents > 32 || s.Research.Candidates < 1 || s.Research.Candidates > 48 || s.Research.Shortlist < 1 || s.Research.Shortlist > s.Research.Candidates {
		return nil, fmt.Errorf("invalid research budgets")
	}
	if err := s.validateRisk(); err != nil {
		return nil, err
	}
	if err := s.ValidateChiefEngine(); err != nil {
		return nil, err
	}
	return s, nil
}

// ValidateChiefEngine normalizes an omitted ChiefEngine to "claude" and
// enforces its enum plus the [chief_api]/[chief_fallback] rules that only
// make sense once the selector is known. It is exported so cmd/cfr can
// re-check a run after a --chief-engine flag override changes the resolved
// value post-Load — the same "fail before any data acquisition" guarantee
// Load itself gives a file/env-only configuration.
//
// Validation errors name the field rather than echoing an untrusted value, so
// none of its error messages may interpolate a credential value; they name
// the offending field instead (see the registerSecrets doc comment).
func (s *Settings) ValidateChiefEngine() error {
	if s.ChiefEngine == "" {
		s.ChiefEngine = "claude"
	}
	switch s.ChiefAPI.CompactionEffort {
	case "", "adaptive", "low", "high", "max":
	default:
		return fmt.Errorf(`chief_api.compaction_reasoning_effort must be "", "adaptive", "low", "high" or "max"`)
	}
	switch s.ChiefEngine {
	case "claude":
		return nil
	case "api":
		if s.ChiefAPI.BaseURL == "" {
			return fmt.Errorf(`chief_api.base_url is required when chief_engine = "api"`)
		}
		if s.ChiefAPI.Model == "" {
			return fmt.Errorf(`chief_api.model is required when chief_engine = "api"`)
		}
		if s.ChiefAPI.APIKey == "" {
			return fmt.Errorf(`chief_api.api_key is required when chief_engine = "api" — it is never inherited from [api], [local], or DEEPSEEK_API_KEY`)
		}
		// The DeepSeek fallback is a resilience measure for when the Claude
		// primary call fails; with chief_engine = "api" there is no Claude
		// primary to fall back from. Reject only an EXPLICIT `enabled = true`
		// here, not the broader "would be active" state (ChiefFallbackActive):
		// leftover [chief_fallback] credentials with `enabled` omitted are a
		// common, harmless shape — this repo's own cfr.toml is exactly that —
		// and must load as "configured API Chief, fallback disabled" rather than
		// fail config-time. Retained-but-inert credentials never dispatching a
		// second DeepSeek call is a runtime guarantee (Task 5's job), not a
		// config-time one; see docs/plans/2026-09-15-deepseek-chief-and-
		// research-reliability.md's compatibility matrix, row 4 vs row 6.
		if s.ChiefFallbackEnabled != nil && *s.ChiefFallbackEnabled {
			return fmt.Errorf(`chief_fallback is supported only after a Claude primary (chief_engine = "claude") — it cannot be explicitly enabled when chief_engine = "api"; leave [chief_fallback] enabled unset (or false) to keep its credentials configured but inert`)
		}
		return nil
	default:
		return fmt.Errorf(`chief_engine must be "claude" or "api" (got %q)`, s.ChiefEngine)
	}
}

// ChiefFallbackActive reports whether the configured DeepSeek Chief Analyst
// fallback would actually fire: it requires a Claude primary to fall back
// from (chief_engine == "claude" — under "api" there is no primary-failure
// event to trigger it, so leftover credentials there are inert by
// construction, not merely by policy), its own api_key present, and
// ChiefFallbackEnabled not explicitly false. nil (the `enabled` key omitted)
// is what preserves today's api_key-only gate under chief_engine == "claude".
func (s *Settings) ChiefFallbackActive() bool {
	if s.ChiefEngine != "claude" {
		return false
	}
	if s.ChiefFallback.APIKey == "" {
		return false
	}
	return s.ChiefFallbackEnabled == nil || *s.ChiefFallbackEnabled
}

// registerSecrets is the one place that knows every configured credential, so
// it is the one place that can tell the sinks what to keep out of artifacts,
// prompts and logs. Providers echo their own request back on failure; without
// this a key travelled into metadata.json and into a researcher's retrieval
// diagnostics block. See internal/redact.
func (s *Settings) registerSecrets() {
	redact.Register(
		s.Providers.AlphaVantageKey,
		s.Providers.FredKey,
		s.Providers.AlpacaKeyID,
		s.Providers.AlpacaSecret,
		s.API.APIKey,
		s.Local.APIKey,
		s.ChiefFallback.APIKey,
		s.ChiefAPI.APIKey,
	)
}

// validateRisk refuses the risk settings that cannot mean anything, so that
// every value which survives means exactly what it says.
//
// Zero is the identity for a floor and the annihilator for a ceiling: a
// `cost_bps` or `rr_min` of 0 turns a check off, which is a policy an operator
// may want, while a `stop_sigma_max` of 0 says no stop may sit any distance
// from entry and so rejects every idea ever written. The second is a typo every
// time. Both used to be quietly replaced by the default, which is the worst of
// the three options: the run neither honoured the setting nor reported that it
// had ignored it.
//
// Only keys the operator actually wrote are checked. An absent key is zero here
// and is filled by riskgate.riskDefaults later, which is not an error.
func (s *Settings) validateRisk() error {
	ceilings := []struct {
		key string
		v   float64
	}{
		{"account_equity", s.Risk.AccountEquity},
		{"risk_per_trade_pct", s.Risk.RiskPerTradePct},
		{"stop_sigma_max", s.Risk.StopSigmaMax},
		{"target_sigma_max", s.Risk.TargetSigmaMax},
		{"entry_patience_sigma", s.Risk.EntryPatienceSigma},
		{"entry_chase_sigma", s.Risk.EntryChaseSigma},
		{"max_pair_corr", s.Risk.MaxPairCorr},
		{"max_portfolio_beta", s.Risk.MaxPortfolioBeta},
		{"max_per_sector", float64(s.Risk.MaxPerSector)},
	}
	for _, c := range ceilings {
		if s.Risk.Set(c.key) && c.v <= 0 {
			return fmt.Errorf("config risk.%s = %v: must be > 0 — it is a ceiling, and a ceiling of zero rejects every idea rather than disabling the check", c.key, c.v)
		}
	}
	floors := []struct {
		key string
		v   float64
	}{
		{"cost_bps", s.Risk.CostBps},
		{"rr_min", s.Risk.RRMin},
		{"stop_sigma_min", s.Risk.StopSigmaMin},
		{"adv_min_usd", s.Risk.ADVMinUSD},
	}
	for _, c := range floors {
		if s.Risk.Set(c.key) && c.v < 0 {
			return fmt.Errorf("config risk.%s = %v: must be >= 0 — set it to 0 to disable the check", c.key, c.v)
		}
	}
	// edge_sigma_daily, min_expectancy_r and min_expectancy_bps are unconstrained
	// in sign on purpose: a negative edge is what a losing system has, and a
	// negative floor is a weaker floor.
	return nil
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
	md, err := toml.Decode(string(data), &f)
	if err != nil {
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
	setStr(&s.ResearchMode, f.ResearchMode)
	setInt(&s.Research.Rounds, f.Research.Rounds)
	setInt(&s.Research.Documents, f.Research.Documents)
	setInt(&s.Research.Candidates, f.Research.Candidates)
	setInt(&s.Research.Shortlist, f.Research.Shortlist)
	setStr(&s.Research.SourcesFile, f.Research.SourcesFile)
	setStr(&s.Research.HolidaysFile, f.Research.HolidaysFile)
	setInt(&s.Research.Budgets.Triage.InputBytes, f.Research.Budgets.Triage.InputBytes)
	setInt(&s.Research.Budgets.Triage.ResponseBytes, f.Research.Budgets.Triage.ResponseBytes)
	setInt(&s.Research.Budgets.Researcher.InputBytes, f.Research.Budgets.Researcher.InputBytes)
	setInt(&s.Research.Budgets.Researcher.ResponseBytes, f.Research.Budgets.Researcher.ResponseBytes)
	setInt(&s.Research.Budgets.Challenger.InputBytes, f.Research.Budgets.Challenger.InputBytes)
	setInt(&s.Research.Budgets.Challenger.ResponseBytes, f.Research.Budgets.Challenger.ResponseBytes)
	setInt(&s.Research.Budgets.Chief.InputBytes, f.Research.Budgets.Chief.InputBytes)
	setInt(&s.Research.Budgets.Chief.ResponseBytes, f.Research.Budgets.Chief.ResponseBytes)
	setStr(&s.AgentsDir, f.AgentsDir)
	setStr(&s.RunsDir, f.RunsDir)
	setStr(&s.DataDir, f.DataDir)
	setInt(&s.KeepRuns, f.KeepRuns)
	setInt(&s.Workers, f.Workers)
	// data_cache_days=0 is a valid, meaningful setting ("disable pruning"), so
	// it needs its own presence check instead of the "v > 0" setInt guard,
	// which would otherwise treat an explicit 0 as "not set".
	if md.IsDefined("data_cache_days") && f.DataCacheDays >= 0 {
		s.DataCacheDays = f.DataCacheDays
	}
	setInt(&s.GeminiConcurrency, f.GeminiConcurrency)
	setInt(&s.PrescreenTopPerIndex, f.PrescreenTopPerIndex)
	setInt(&s.PrescreenPullbackPerIndex, f.PrescreenPullbackPerIndex)
	setInt(&s.PrescreenBasePerIndex, f.PrescreenBasePerIndex)
	setInt(&s.PrescreenDriftPerIndex, f.PrescreenDriftPerIndex)
	setInt(&s.MaxShortlist, f.MaxShortlist)
	setInt(&s.MaxPerIndex, f.MaxPerIndex)
	// Not setInt: zero is the default and means "admit no thinly-covered name",
	// so a negative value is the documented way to turn the cap off and sign
	// cannot decide whether the key was set.
	if md.IsDefined("max_thinly_covered") {
		s.MaxThinlyCovered = f.MaxThinlyCovered
	}
	// Not setInt: a negative shortlist_reserve is the documented way to turn the
	// archetype reserve off, so sign cannot decide whether the key was set.
	if f.ShortlistReserve != 0 {
		s.ShortlistReserve = f.ShortlistReserve
	}
	if f.ShortlistReserveMinMerit != 0 {
		s.ShortlistReserveMinMerit = f.ShortlistReserveMinMerit
	}
	setInt(&s.ChiefAdjustBand, f.ChiefAdjustBand)
	// The whole [risk] block is presence-detected, the way data_cache_days above
	// is. Every key in it is a float64 whose zero value is also a legal setting
	// — `cost_bps = 0` prices a book frictionless, `rr_min = 0` disables the
	// reward:risk floor — so a "v > 0" guard did not mean "unset", it meant
	// "unsayable": the value was discarded here and then defaulted again by
	// riskgate.riskDefaults, and an operator asking for no cost assumption got
	// 30 bps with no diagnostic. Marking the key explicit is what carries the
	// operator's intent past both layers.
	setRisk := func(dst *float64, key string, v float64) {
		if !md.IsDefined("risk", key) {
			return
		}
		*dst = v
		if s.Risk.Explicit == nil {
			s.Risk.Explicit = map[string]bool{}
		}
		s.Risk.Explicit[key] = true
	}
	// A count, not a ratio, so it needs its own setter: everything else under
	// [risk] is a float64.
	setRiskInt := func(dst *int, key string, v int) {
		if !md.IsDefined("risk", key) {
			return
		}
		*dst = v
		if s.Risk.Explicit == nil {
			s.Risk.Explicit = map[string]bool{}
		}
		s.Risk.Explicit[key] = true
	}
	setRiskInt(&s.Risk.MaxPerSector, "max_per_sector", f.Risk.MaxPerSector)
	setRisk(&s.Risk.AccountEquity, "account_equity", f.Risk.AccountEquity)
	setRisk(&s.Risk.RiskPerTradePct, "risk_per_trade_pct", f.Risk.RiskPerTradePct)
	setRisk(&s.Risk.CostBps, "cost_bps", f.Risk.CostBps)
	setRisk(&s.Risk.RRMin, "rr_min", f.Risk.RRMin)
	setRisk(&s.Risk.StopSigmaMin, "stop_sigma_min", f.Risk.StopSigmaMin)
	setRisk(&s.Risk.StopSigmaMax, "stop_sigma_max", f.Risk.StopSigmaMax)
	setRisk(&s.Risk.TargetSigmaMax, "target_sigma_max", f.Risk.TargetSigmaMax)
	setRisk(&s.Risk.EntryPatienceSigma, "entry_patience_sigma", f.Risk.EntryPatienceSigma)
	setRisk(&s.Risk.EntryChaseSigma, "entry_chase_sigma", f.Risk.EntryChaseSigma)
	setRisk(&s.Risk.ADVMinUSD, "adv_min_usd", f.Risk.ADVMinUSD)
	setRisk(&s.Risk.MaxPairCorr, "max_pair_corr", f.Risk.MaxPairCorr)
	setRisk(&s.Risk.MaxPortfolioBeta, "max_portfolio_beta", f.Risk.MaxPortfolioBeta)
	setRisk(&s.Risk.EdgeSigmaDaily, "edge_sigma_daily", f.Risk.EdgeSigmaDaily)
	setRisk(&s.Risk.MinExpectancyR, "min_expectancy_r", f.Risk.MinExpectancyR)
	setRisk(&s.Risk.MinExpectancyBps, "min_expectancy_bps", f.Risk.MinExpectancyBps)
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
	if err := parseDur(&s.Timeouts.SynthesisFallback, f.Timeouts.SynthesisFallback, "timeouts.synthesis_fallback"); err != nil {
		return err
	}
	setInt(&s.Retry.MaxAttempts, f.Retry.MaxAttempts)
	setInt(&s.SynthesisMaxAttempts, f.Retry.SynthesisMaxAttempts)
	if err := parseDur(&s.Retry.BaseDelay, f.Retry.BaseDelay, "retry.base_delay"); err != nil {
		return err
	}
	if err := parseDur(&s.Retry.MaxDelay, f.Retry.MaxDelay, "retry.max_delay"); err != nil {
		return err
	}
	if err := parseDur(&s.PriceTTL, f.PriceTTL, "price_ttl"); err != nil {
		return err
	}
	// jitter=false is a meaningful setting, not an absent one, so it needs the
	// same presence check data_cache_days already has. Testing the value alone
	// made the flag one-way: once ~/.config/cfr/config.toml turned jitter on,
	// no ./cfr.toml and no flag could turn it off again, which inverts the
	// documented precedence for the one key where it is silent.
	if md.IsDefined("retry", "jitter") {
		s.Retry.Jitter = f.Retry.Jitter
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
	setStr(&s.Providers.AlpacaKeyID, f.Providers.AlpacaKeyID)
	setStr(&s.Providers.AlpacaSecret, f.Providers.AlpacaSecretKey)

	if f.CheapEngine != "" {
		s.CheapEngine = model.CLI(f.CheapEngine)
	}
	setStr(&s.ChiefEngine, f.ChiefEngine)
	setInt(&s.LocalConcurrency, f.LocalConcurrency)
	setInt(&s.FillWindowDays, f.Scoreboard.FillWindowDays)
	setStr(&s.API.BaseURL, f.API.BaseURL)
	setStr(&s.API.Model, f.API.Model)
	setStr(&s.API.APIKey, f.API.APIKey)
	setInt(&s.API.MaxTokens, f.API.MaxTokens)
	setStr(&s.Local.BaseURL, f.Local.BaseURL)
	setStr(&s.Local.Model, f.Local.Model)
	setStr(&s.Local.APIKey, f.Local.APIKey)
	setInt(&s.Local.MaxTokens, f.Local.MaxTokens)
	setStr(&s.ChiefFallback.BaseURL, f.ChiefFallback.BaseURL)
	setStr(&s.ChiefFallback.Model, f.ChiefFallback.Model)
	setStr(&s.ChiefFallback.APIKey, f.ChiefFallback.APIKey)
	setInt(&s.ChiefFallback.MaxTokens, f.ChiefFallback.MaxTokens)
	// *bool, not setStr/setInt's "nonzero wins": omitted (nil) must stay distinct
	// from an explicit false, so presence is read straight off the decoded
	// pointer rather than off an IsDefined lookup keyed by string path.
	if f.ChiefFallback.Enabled != nil {
		v := *f.ChiefFallback.Enabled
		s.ChiefFallbackEnabled = &v
	}
	setStr(&s.ChiefAPI.BaseURL, f.ChiefAPI.BaseURL)
	setStr(&s.ChiefAPI.Model, f.ChiefAPI.Model)
	setStr(&s.ChiefAPI.APIKey, f.ChiefAPI.APIKey)
	setInt(&s.ChiefAPI.MaxTokens, f.ChiefAPI.MaxTokens)
	setStr(&s.ChiefAPI.CompactionEffort, f.ChiefAPI.CompactionReasoningEffort)
	return nil
}

// applyEnv lets the established CFR_* environment variables win over files.
func (s *Settings) applyEnv() {
	setStr := func(dst *string, key string) {
		if v := os.Getenv(key); v != "" {
			*dst = v
		}
	}
	setSecret := func(dst *string, key string) {
		if v := os.Getenv(key); v != "" {
			redact.Register(v)
			*dst = v
		}
	}
	setPosInt := func(dst *int, key string) {
		if v := os.Getenv(key); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				*dst = n
			}
		}
	}
	setStr(&s.ResearchMode, "CFR_RESEARCH_MODE")
	setStr(&s.Research.SourcesFile, "CFR_RESEARCH_SOURCES_FILE")
	setStr(&s.Research.HolidaysFile, "CFR_RESEARCH_HOLIDAYS_FILE")
	setPosInt(&s.Research.Rounds, "CFR_RESEARCH_ROUNDS")
	setPosInt(&s.Research.Documents, "CFR_RESEARCH_DOCUMENTS")
	setPosInt(&s.Research.Candidates, "CFR_RESEARCH_CANDIDATES")
	setPosInt(&s.Research.Shortlist, "CFR_RESEARCH_SHORTLIST")
	setPosInt(&s.Research.Budgets.Triage.InputBytes, "CFR_RESEARCH_TRIAGE_INPUT_BYTES")
	setPosInt(&s.Research.Budgets.Triage.ResponseBytes, "CFR_RESEARCH_TRIAGE_RESPONSE_BYTES")
	setPosInt(&s.Research.Budgets.Researcher.InputBytes, "CFR_RESEARCH_RESEARCHER_INPUT_BYTES")
	setPosInt(&s.Research.Budgets.Researcher.ResponseBytes, "CFR_RESEARCH_RESEARCHER_RESPONSE_BYTES")
	setPosInt(&s.Research.Budgets.Challenger.InputBytes, "CFR_RESEARCH_CHALLENGER_INPUT_BYTES")
	setPosInt(&s.Research.Budgets.Challenger.ResponseBytes, "CFR_RESEARCH_CHALLENGER_RESPONSE_BYTES")
	setPosInt(&s.Research.Budgets.Chief.InputBytes, "CFR_RESEARCH_CHIEF_INPUT_BYTES")
	setPosInt(&s.Research.Budgets.Chief.ResponseBytes, "CFR_RESEARCH_CHIEF_RESPONSE_BYTES")
	setStr(&s.RunsDir, "CFR_RUNS_DIR")
	setStr(&s.AgentsDir, "CFR_AGENTS_DIR")
	setSecret(&s.Providers.AlphaVantageKey, "ALPHAVANTAGE_API_KEY")
	setSecret(&s.Providers.FredKey, "FRED_API_KEY")
	setStr(&s.Providers.ContactEmail, "CFR_CONTACT_EMAIL")
	// Alpaca's own env names first, so a key already exported for the alpaca
	// SDKs just works; the CFR_-prefixed alias is read second and therefore
	// wins, matching how CFR_API_KEY overrides DEEPSEEK_API_KEY above.
	setSecret(&s.Providers.AlpacaKeyID, "APCA_API_KEY_ID")
	setSecret(&s.Providers.AlpacaSecret, "APCA_API_SECRET_KEY")
	setSecret(&s.Providers.AlpacaKeyID, "CFR_ALPACA_KEY_ID")
	setSecret(&s.Providers.AlpacaSecret, "CFR_ALPACA_SECRET_KEY")

	// Cheap-research API engine. CFR_API_KEY is preferred; DEEPSEEK_API_KEY is
	// accepted as an alias so a DeepSeek key already in the environment just works.
	setStr(&s.API.BaseURL, "CFR_API_BASE_URL")
	setStr(&s.API.Model, "CFR_API_MODEL")
	setSecret(&s.API.APIKey, "DEEPSEEK_API_KEY")
	setSecret(&s.API.APIKey, "CFR_API_KEY")
	if v := os.Getenv("CFR_CHEAP_ENGINE"); v != "" {
		s.CheapEngine = model.CLI(v)
	}
	setStr(&s.ChiefEngine, "CFR_CHIEF_ENGINE")

	// Chief Analyst's own dedicated API engine (chief_engine = "api"). No
	// DEEPSEEK_API_KEY alias here either, for the same reason [chief_fallback]
	// has none below: a dedicated key must be set explicitly, never inherited
	// from the cheap-research role's config.
	setStr(&s.ChiefAPI.BaseURL, "CFR_CHIEF_API_BASE_URL")
	setStr(&s.ChiefAPI.Model, "CFR_CHIEF_API_MODEL")
	setSecret(&s.ChiefAPI.APIKey, "CFR_CHIEF_API_KEY")
	setPosInt(&s.ChiefAPI.MaxTokens, "CFR_CHIEF_API_MAX_TOKENS")

	// Local OpenAI-compatible server (Ollama/llama.cpp). Key is optional.
	setStr(&s.Local.BaseURL, "CFR_LOCAL_BASE_URL")
	setStr(&s.Local.Model, "CFR_LOCAL_MODEL")
	setSecret(&s.Local.APIKey, "CFR_LOCAL_KEY")
	setPosInt(&s.API.MaxTokens, "CFR_API_MAX_TOKENS")
	// [local] is configurable by env on every other key; max_tokens was the one
	// omission, so a local model's context had to be set in a file even when
	// everything else about it came from the environment.
	setPosInt(&s.Local.MaxTokens, "CFR_LOCAL_MAX_TOKENS")

	// Optional DeepSeek resilience fallback for the Chief Analyst synthesis
	// step. Deliberately no DEEPSEEK_API_KEY alias here (unlike [api]'s): a
	// dedicated key must be set explicitly, never inherited from the
	// cheap-research role's config.
	setStr(&s.ChiefFallback.BaseURL, "CFR_CHIEF_FALLBACK_BASE_URL")
	setStr(&s.ChiefFallback.Model, "CFR_CHIEF_FALLBACK_MODEL")
	setSecret(&s.ChiefFallback.APIKey, "CFR_CHIEF_FALLBACK_API_KEY")
	if v := os.Getenv("CFR_CHIEF_FALLBACK_MAX_TOKENS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			s.ChiefFallback.MaxTokens = n
		}
	}
	// *bool: presence decides, not truthiness — CFR_CHIEF_FALLBACK_ENABLED=false
	// must be as expressible as leaving it unset means "inherit today's
	// api_key-gated default". LookupEnv (not Getenv) is what makes an explicit
	// empty string distinguishable from unset, matching the file-side check.
	if v, ok := os.LookupEnv("CFR_CHIEF_FALLBACK_ENABLED"); ok && v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			s.ChiefFallbackEnabled = &b
		}
	}
	if v := os.Getenv("CFR_SYNTHESIS_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			s.Timeouts.Synthesis = d
		}
	}
	if v := os.Getenv("CFR_SYNTHESIS_FALLBACK_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			s.Timeouts.SynthesisFallback = d
		}
	}
	if v := os.Getenv("CFR_SYNTHESIS_MAX_ATTEMPTS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			s.SynthesisMaxAttempts = n
		}
	}
	if v := os.Getenv("CFR_LOCAL_CONCURRENCY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			s.LocalConcurrency = n
		}
	}
	if v := os.Getenv("CFR_FILL_WINDOW_DAYS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			s.FillWindowDays = n
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
	setPosInt(&s.PrescreenTopPerIndex, "CFR_PRESCREEN_TOP_PER_INDEX")
	setPosInt(&s.PrescreenPullbackPerIndex, "CFR_PRESCREEN_PULLBACK_PER_INDEX")
	setPosInt(&s.PrescreenBasePerIndex, "CFR_PRESCREEN_BASE_PER_INDEX")
	setPosInt(&s.PrescreenDriftPerIndex, "CFR_PRESCREEN_DRIFT_PER_INDEX")
	// Any parsed integer wins here, negative included: a negative reserve is the
	// documented way to disable the archetype reserve, so setPosInt would make
	// the switch unreachable from the environment.
	if v := os.Getenv("CFR_SHORTLIST_RESERVE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			s.ShortlistReserve = n
		}
	}
	if v := os.Getenv("CFR_SHORTLIST_RESERVE_MIN_MERIT"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			s.ShortlistReserveMinMerit = f
		}
	}
	setPosInt(&s.MaxShortlist, "CFR_MAX_SHORTLIST")
	setPosInt(&s.MaxPerIndex, "CFR_MAX_PER_INDEX")
	// Signed, not setPosInt: 0 admits no thinly-covered name and a negative
	// value turns the cap off, so both have to survive the parse.
	if v := os.Getenv("CFR_MAX_THINLY_COVERED"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			s.MaxThinlyCovered = n
		}
	}
	setPosInt(&s.ChiefAdjustBand, "CFR_CHIEF_ADJUST_BAND")
	// Presence, not sign, decides an override here too — see setRisk in
	// applyFile. A var that is set and parses wins at whatever value it holds,
	// so CFR_COST_BPS=0 is as expressible as cost_bps = 0 in the file.
	envRisk := func(dst *float64, key, env string) {
		v, ok := os.LookupEnv(env)
		if !ok || v == "" {
			return
		}
		x, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return
		}
		*dst = x
		if s.Risk.Explicit == nil {
			s.Risk.Explicit = map[string]bool{}
		}
		s.Risk.Explicit[key] = true
	}
	envRisk(&s.Risk.ADVMinUSD, "adv_min_usd", "CFR_ADV_MIN_USD")
	envRisk(&s.Risk.AccountEquity, "account_equity", "CFR_ACCOUNT_EQUITY")
	envRisk(&s.Risk.RiskPerTradePct, "risk_per_trade_pct", "CFR_RISK_PER_TRADE_PCT")
	envRisk(&s.Risk.RRMin, "rr_min", "CFR_RR_MIN")
	envRisk(&s.Risk.CostBps, "cost_bps", "CFR_COST_BPS")
	envRisk(&s.Risk.StopSigmaMin, "stop_sigma_min", "CFR_STOP_SIGMA_MIN")
	envRisk(&s.Risk.StopSigmaMax, "stop_sigma_max", "CFR_STOP_SIGMA_MAX")
	envRisk(&s.Risk.TargetSigmaMax, "target_sigma_max", "CFR_TARGET_SIGMA_MAX")
	envRisk(&s.Risk.EntryPatienceSigma, "entry_patience_sigma", "CFR_ENTRY_PATIENCE_SIGMA")
	envRisk(&s.Risk.EntryChaseSigma, "entry_chase_sigma", "CFR_ENTRY_CHASE_SIGMA")
	envRisk(&s.Risk.MaxPairCorr, "max_pair_corr", "CFR_MAX_PAIR_CORR")
	envRisk(&s.Risk.MaxPortfolioBeta, "max_portfolio_beta", "CFR_MAX_PORTFOLIO_BETA")
	if v, ok := os.LookupEnv("CFR_MAX_PER_SECTOR"); ok && v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			s.Risk.MaxPerSector = n
			if s.Risk.Explicit == nil {
				s.Risk.Explicit = map[string]bool{}
			}
			s.Risk.Explicit["max_per_sector"] = true
		}
	}
	envRisk(&s.Risk.EdgeSigmaDaily, "edge_sigma_daily", "CFR_EDGE_SIGMA_DAILY")
	envRisk(&s.Risk.MinExpectancyR, "min_expectancy_r", "CFR_MIN_EXPECTANCY_R")
	envRisk(&s.Risk.MinExpectancyBps, "min_expectancy_bps", "CFR_MIN_EXPECTANCY_BPS")
}
