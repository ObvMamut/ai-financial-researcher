// Package orchestrator drives the two-stage (or three-stage) analysis pipeline.
// It spawns CLI subprocesses via a bounded worker pool and emits progress events
// over a channel consumed by the TUI. It never imports tui types.
package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/agents"
	"github.com/mamut/claude-financial-researcher/internal/marketdata"
	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/parse"
	"github.com/mamut/claude-financial-researcher/internal/redact"
	"github.com/mamut/claude-financial-researcher/internal/scoreboard"
	"github.com/mamut/claude-financial-researcher/internal/store"
	"github.com/mamut/claude-financial-researcher/internal/universe"
)

// EventType classifies a progress event.
type EventType int

const (
	EventStatus   EventType = iota // one agent changed status
	EventLog                       // free-text log line
	EventComplete                  // run finished, Ideas populated
	EventError                     // unrecoverable run error
)

// Event is the unit emitted on the progress channel.
type Event struct {
	Type    EventType
	Agent   string             // role name, relevant for EventStatus
	Status  model.AgentStatus  // relevant for EventStatus
	Message string             // log text or error message
	Report  *model.Report      // set on EventStatus when done/failed
	Ideas   *model.IdeasResult // set on EventComplete
	Meta    *model.RunMeta     // set on EventComplete
}

// Config holds all run parameters.
type Config struct {
	// Frozen and EvaluationRun execute an explicitly reserved evaluation arm
	// against a common, closed evidence corpus. They must be supplied together.
	Frozen        *marketdata.ResearchSnapshot
	EvaluationRun *store.Run
	ResearchMode  string
	Research      model.ResearchConfig
	Mode          model.Mode
	Ticker        string   // single-stock mode only
	Indices       []string // independent mode: index keys to screen; empty = all
	AgentsDir     string   // path to agents/*.md
	RunsDir       string   // base directory for run artifacts (default "runs")
	DataDir       string   // path for cached market data
	Workers       int      // bounded pool size (default 4)

	Timeouts  model.StageTimeouts
	Retry     model.RetryPolicy
	Weights   model.DomainWeights
	Providers model.ProviderConfig
	Models    map[model.CLI]string // per-CLI model passed via --model; empty CLIClaude defaults to "opus"
	Binaries  map[model.CLI]string // per-CLI executable name; empty CLIGemini defaults to "agy", CLIClaude to "claude"

	// CheapEngine selects the engine for the cheap-research roles (scouts +
	// specialists): CLIGemini (the `agy` CLI, default), CLIApi (a remote
	// OpenAI-compatible endpoint, config in API), or CLILocal (a local
	// OpenAI-compatible server, config in Local). Synthesis always uses the
	// Claude CLI regardless.
	CheapEngine model.CLI
	// API configures the remote CLIApi engine; required when CheapEngine == CLIApi.
	API model.APIConfig
	// Local configures the local OpenAI-compatible server; required when
	// CheapEngine == CLILocal. The key is optional (local servers don't auth).
	Local model.APIConfig

	// GeminiConcurrency caps simultaneous Gemini (agy) subprocesses. Concurrent agy
	// processes contend on the OS keyring during auth and escalate to a browser login;
	// defaults to 1 when unset.
	GeminiConcurrency int
	// LocalConcurrency caps simultaneous local-model calls; one GPU can't run the
	// 5 specialists at once. Defaults to 1 when unset.
	LocalConcurrency int

	// KeepRuns is how many run directories CleanupOldRuns retains (default 100;
	// the scoreboard needs history).
	KeepRuns int

	// PriceTTL bounds how stale a cached daily price series may be before it is
	// refetched. Every level in every idea is computed off the last close, so a
	// day-scoped cache priced a Saturday run off Thursday. Zero means 4h.
	PriceTTL time.Duration
	// DataCacheDays is how long cached provider responses survive the run-start
	// janitor. Zero disables pruning.
	DataCacheDays int

	// PrescreenTopPerIndex is how many of each index's highest-composite names
	// the Stage 0.5 table puts in front of its scout (the weakest
	// prescreenBottomPerIndex are always appended as short candidates). Zero
	// means 15.
	PrescreenTopPerIndex int
	// PrescreenPullbackPerIndex and PrescreenBasePerIndex size the archetype
	// sections the scout table carries alongside the top-of-ranking one, **per
	// direction** — each is rendered as a long half and a short half. Zero
	// means 5 and 3, so ten and six rows.
	PrescreenPullbackPerIndex int
	PrescreenBasePerIndex     int
	// PrescreenDriftPerIndex sizes the earnings-drift section, also per
	// direction. Zero means 5, so up to ten rows.
	PrescreenDriftPerIndex int
	// MaxShortlist caps the merged shortlist that reaches the specialists. Zero
	// means 12.
	MaxShortlist int
	// MaxPerIndex caps how many of those names one index may contribute before
	// the merit backfill. Zero means 5.
	MaxPerIndex int
	// MaxThinlyCovered caps how many shortlisted names the run's sources can
	// ground less than thinCoverage of the domain weight for. Zero — the
	// default — admits none, because the risk gate's evidence floor deletes an
	// idea scored by quant alone and such a name can be scored by nothing else.
	// A negative value disables the cap.
	MaxThinlyCovered int
	// ShortlistReserve holds slots in the shortlist for names the pre-screen
	// classified as something other than "continuation" — a pullback or a base.
	// Zero means 3, of a twelve-name shortlist; a negative value disables the
	// reserve and restores the pure merit sort.
	//
	// The reserve exists because merit is the pre-screen composite, the
	// composite is built from trailing returns, and a name at its 52-week high
	// is by construction the name with the highest trailing returns. Without a
	// reserve the archetypes would be computed, displayed to the scout, and
	// then sorted straight back out of the shortlist.
	ShortlistReserve int
	// ShortlistReserveMinMerit is the merit a candidate needs to take a
	// reserved slot, in the composite's own z units. It is what keeps the
	// reserve from becoming a quota: below it, the slot goes unfilled and the
	// shortlist ships short. Zero means 0.5.
	ShortlistReserveMinMerit float64
	// Risk is the deterministic risk policy applied after synthesis: stop and
	// target bands, the reward:risk floor, expectancy, liquidity, book-level
	// correlation and beta, and position sizing. Zero fields take the defaults
	// in riskDefaults. Risk.ADVMinUSD also gates the Stage 0.5 pre-screen, so a
	// name too thin to trade never reaches a model either.
	Risk model.RiskConfig

	// ChiefAdjustBand is how far, in confidence points, the Chief Analyst may
	// move an idea from its computed base score. Confidence beyond the band is
	// clamped; far beyond it triggers one corrective re-prompt. Zero means 10.
	ChiefAdjustBand int

	// FillWindowDays is how many sessions a past idea's entry limit stays live
	// when the scoreboard replays it for the track record. Zero means 3.
	FillWindowDays int

	// SynthesisMaxAttempts overrides Retry.MaxAttempts for the primary Chief
	// Analyst call only. A synthesis timeout means "too slow," not "flaky," so
	// retrying identically just delays reaching the DeepSeek fallback below.
	// Zero means 1 (no retry).
	SynthesisMaxAttempts int

	// ChiefFallback configures an optional DeepSeek resilience call attempted
	// when the primary Chief Analyst (claude CLI) call fails or its JSON fails
	// to parse — sitting between "Claude failed" and the mechanical
	// buildDegradedIdeas fallback. Off by default, gated on APIKey != "" alone,
	// and never inherited from API/Local: turning on cheap_engine=api must never
	// silently also enable Chief Analyst fallback spend.
	ChiefFallback model.APIConfig
	// ChiefFallbackEnabled is a three-state override on top of the api_key
	// gate above: nil (the config key omitted) preserves the api_key-only
	// gate, true forces the fallback on (still requiring credentials), false
	// forces it off even with credentials present. Mirrors
	// config.Settings.ChiefFallbackEnabled — see chiefFallbackAllowed, which
	// additionally requires the primary Chief engine to be claude, matching
	// config.Settings.ChiefFallbackActive's engine-aware rule so the two
	// gates cannot disagree.
	ChiefFallbackEnabled *bool

	// ChiefEngine selects the Chief Analyst synthesis engine: "claude" (the
	// claude CLI, default — "" is equivalent) or "api" (ChiefAPI below).
	// Resolved once per run by resolveChiefEngine (chief.go), never inferred
	// per-call from the CLI value alone, since chief_engine="api" and
	// cheap_engine="api"/"local" can both resolve to model.CLIApi while
	// pointing at entirely different endpoints.
	ChiefEngine string
	// ChiefAPI is the Chief's own dedicated OpenAI-compatible credentials, read
	// only when ChiefEngine == "api". It deliberately never inherits from
	// API/Local/ChiefFallback — see Settings.ChiefAPI's doc comment.
	ChiefAPI model.APIConfig
}

func (c *Config) applyDefaults() {
	if c.ResearchMode == "" {
		c.ResearchMode = "legacy"
	}
	c.Research = c.Research.Defaults()
	if c.AgentsDir == "" {
		c.AgentsDir = "agents"
	}
	if c.RunsDir == "" {
		c.RunsDir = "runs"
	}
	if c.DataDir == "" {
		c.DataDir = ".data"
	}
	if c.Workers <= 0 {
		c.Workers = 4
	}

	// Default model selection. Claude does the single heavy synthesis call;
	// pin it so the run never depends on the machine's global CLI default.
	if c.Models == nil {
		c.Models = map[model.CLI]string{}
	}
	if c.Models[model.CLIClaude] == "" {
		c.Models[model.CLIClaude] = "opus"
	}

	// Default CLI binaries. Google discontinued the free `gemini` CLI tier, so the
	// cheap-research role now shells out to `agy` (the Antigravity CLI), which exposes
	// the same -p/--model interface. Override per role via CFR_GEMINI_BIN/CFR_CLAUDE_BIN.
	if c.Binaries == nil {
		c.Binaries = map[model.CLI]string{}
	}
	if c.Binaries[model.CLIGemini] == "" {
		c.Binaries[model.CLIGemini] = "agy"
	}
	if c.Binaries[model.CLIClaude] == "" {
		c.Binaries[model.CLIClaude] = "claude"
	}

	// Cheap-research role defaults to the `agy` CLI engine so a fresh checkout
	// (and the hermetic fakebin tests) need no API key. Set CheapEngine=CLIApi
	// (remote) or CLILocal (local server) to route scouts+specialists to the
	// OpenAI-compatible HTTP engine.
	if c.CheapEngine == "" {
		c.CheapEngine = model.CLIGemini
	}
	// Chief Analyst engine defaults to the claude CLI — the pipeline's existing
	// behavior — mirroring Settings.ValidateChiefEngine's own "" == "claude"
	// contract so a bare Config{} (every pre-Task-4 test, every call site that
	// predates chief_engine) still resolves the way it always did.
	if c.ChiefEngine == "" {
		c.ChiefEngine = "claude"
	}
	if c.API.Model == "" {
		c.API.Model = "deepseek-chat"
	}
	// Local defaults target a stock Ollama install; the model must be set by the
	// user (it depends on what they have pulled), so it has no default.
	if c.Local.BaseURL == "" {
		c.Local.BaseURL = "http://localhost:11434/v1"
	}

	// ChiefFallback defaults only take effect once its own APIKey is set (see
	// resolveChiefFallback) — harmless no-ops otherwise. deepseek-reasoner, not
	// the cheap role's deepseek-chat: this is a resilience call for the single
	// most important step in the pipeline. Its MaxTokens default is 4x the
	// cheap role's because a reasoning model spends tokens on chain-of-thought
	// before its answer, and apiengine.go already treats finish_reason=="length"
	// as an error rather than truncated success.
	if c.ChiefFallback.BaseURL == "" {
		c.ChiefFallback.BaseURL = "https://api.deepseek.com"
	}
	if c.ChiefFallback.Model == "" {
		c.ChiefFallback.Model = "deepseek-reasoner"
	}
	if c.ChiefFallback.MaxTokens <= 0 {
		c.ChiefFallback.MaxTokens = 32768
	}

	// Serialize agy auth by default to avoid keyring contention (see Config.GeminiConcurrency).
	if c.GeminiConcurrency <= 0 {
		c.GeminiConcurrency = 1
	}
	// Serialize local-model calls by default (one GPU, one generation at a time).
	if c.LocalConcurrency <= 0 {
		c.LocalConcurrency = 1
	}

	if c.KeepRuns <= 0 {
		c.KeepRuns = 100
	}
	if c.PriceTTL <= 0 {
		c.PriceTTL = marketdata.DefaultPriceTTL
	}
	if c.DataCacheDays < 0 {
		c.DataCacheDays = 0
	}

	// Funnel geometry. 15 names per index is roughly the top sixth of a curated
	// sample — wide enough that the scout is choosing rather than rubber-stamping
	// a ranking, narrow enough that the table stays readable in a prompt.
	if c.PrescreenTopPerIndex <= 0 {
		c.PrescreenTopPerIndex = 15
	}
	if c.MaxShortlist <= 0 {
		c.MaxShortlist = 12
	}
	if c.MaxPerIndex <= 0 {
		c.MaxPerIndex = 5
	}
	// None, and the reason is that such a name cannot reach the output at all.
	//
	// A name below thinCoverage is one no domain but quant can ground —
	// expectedCoverage counts exactly the quant weight for an unmapped foreign
	// listing — and riskgate's evidence floor deletes an idea scored by quant
	// alone. So the cap was not admitting weaker research, it was admitting
	// candidates guaranteed to be deleted, and paying for them in specialist
	// reports and shortlist slots. On 2026-09-05 it seated exactly its four:
	// 051910.KS, BAYN.DE, BMW.DE and DSFIR.AS took a third of the shortlist,
	// News, Fundamentals and Sentiment returned nothing for any of them, and
	// all four were dropped — leaving eight eligible names of which seven were
	// one sector, so the run shipped two ideas.
	//
	// A negative value disables the cap, for an experiment that wants them back.
	if c.MaxThinlyCovered < 0 {
		c.MaxThinlyCovered = -1
	}
	// A quarter of a twelve-name shortlist. Enough that a run cannot ship an
	// all-continuation book by default, few enough that the merit sort still
	// decides the majority of it.
	if c.ShortlistReserve == 0 {
		c.ShortlistReserve = 3
	}
	if c.ShortlistReserve < 0 {
		c.ShortlistReserve = 0
	}
	// Half a standard deviation of the within-index composite: comfortably
	// above average for its index, without demanding that a pullback outrank
	// the continuation names it is being reserved against — which it cannot,
	// since the composite is what ranks them and it rewards having already run.
	if c.ShortlistReserveMinMerit == 0 {
		c.ShortlistReserveMinMerit = 0.5
	}
	c.Risk = riskDefaults(c.Risk)
	// Ten points is roughly one confidence band in scoring.md's calibration
	// table: enough for the Chief to express a real cross-domain read the
	// arithmetic cannot see, not enough to overwrite it.
	if c.ChiefAdjustBand <= 0 {
		c.ChiefAdjustBand = 10
	}

	// Default Timeouts. Synthesis is 15 minutes — every real run to date has
	// been SIGKILLed mid-attempt at the old 5-minute default (601s ≈ 2 attempts
	// x 300s), so there is no empirical data yet on how long an uninterrupted
	// call actually takes.
	if c.Timeouts.Screening == 0 {
		c.Timeouts.Screening = 5 * time.Minute
	}
	if c.Timeouts.Analysis == 0 {
		c.Timeouts.Analysis = 5 * time.Minute
	}
	if c.Timeouts.Synthesis == 0 {
		c.Timeouts.Synthesis = 15 * time.Minute
	}
	if c.Timeouts.SynthesisFallback == 0 {
		c.Timeouts.SynthesisFallback = c.Timeouts.Synthesis
	}

	// Default Retry Policy
	if c.Retry.MaxAttempts <= 0 {
		c.Retry.MaxAttempts = 2 // 1 retry
	}
	if c.Retry.BaseDelay == 0 {
		c.Retry.BaseDelay = 1 * time.Second
	}
	// The primary Chief Analyst call gets its own attempt budget: a timeout
	// there means "too slow," not "flaky," so retrying identically just delays
	// reaching the DeepSeek fallback.
	if c.SynthesisMaxAttempts <= 0 {
		c.SynthesisMaxAttempts = 1
	}

	// Default weights (matches scoring.md), mapped to the 5–20 day horizon this
	// system actually trades. Fundamentals led at 0.30 for no reason connected
	// to the holding period: a rich multiple says little about the next three
	// weeks, and it is the worst-covered domain (US filers only, quarterly, and
	// often stale). Quant is the only domain covered for every name, computed
	// rather than recalled, and measured over exactly this horizon.
	if c.Weights.Quant == 0 && c.Weights.News == 0 && c.Weights.Fundamentals == 0 && c.Weights.Macro == 0 && c.Weights.Sentiment == 0 {
		c.Weights = model.DefaultDomainWeights()
	}
}

// resolveCheapEngine maps the user-facing CheapEngine selector onto the concrete
// runner engine, the APIConfig to hand the pool, and the throttle concurrency.
// It validates required fields and fails fast so a misconfig surfaces before the
// run rather than as a wave of per-call failures.
func resolveCheapEngine(cfg Config) (engine model.CLI, api model.APIConfig, conc int, err error) {
	switch cfg.CheapEngine {
	case model.CLIGemini:
		// CLI engine (agy by default, or a cheap binary override) — no extra config.
		return model.CLIGemini, model.APIConfig{}, cfg.GeminiConcurrency, nil
	case model.CLIApi:
		if cfg.API.BaseURL == "" || cfg.API.APIKey == "" || cfg.API.Model == "" {
			return "", model.APIConfig{}, 0, fmt.Errorf("cheap_engine=api requires api.base_url, api.model, and an API key (set CFR_API_KEY)")
		}
		// Remote APIs handle concurrency well; run the specialists in parallel.
		return model.CLIApi, cfg.API, 0, nil
	case model.CLILocal:
		if cfg.Local.BaseURL == "" || cfg.Local.Model == "" {
			return "", model.APIConfig{}, 0, fmt.Errorf("cheap_engine=local requires local.base_url and local.model (api_key optional)")
		}
		// Local runs on the same CLIApi HTTP path, throttled to one GPU generation.
		return model.CLIApi, cfg.Local, cfg.LocalConcurrency, nil
	default:
		return "", model.APIConfig{}, 0, fmt.Errorf("invalid cheap_engine %q (valid: gemini, api, local)", cfg.CheapEngine)
	}
}

// resolveChiefFallback validates the optional DeepSeek Chief Analyst fallback.
// It is off by default: an empty APIKey means "not configured" and is not an
// error, since ChiefFallback.BaseURL/Model are always filled by applyDefaults.
// A key set alongside an empty BaseURL/Model is only reachable via a
// deliberately broken config (e.g. explicitly blanking a default), and fails
// fast here — before Stage 0.5 runs, not after burning a full synthesis
// attempt on a call that was always going to fail.
func resolveChiefFallback(cfg Config) (api model.APIConfig, ok bool, err error) {
	if cfg.ChiefFallback.APIKey == "" {
		return model.APIConfig{}, false, nil
	}
	if cfg.ChiefFallback.BaseURL == "" || cfg.ChiefFallback.Model == "" {
		return model.APIConfig{}, false, fmt.Errorf("chief_fallback requires base_url and model alongside its api_key")
	}
	return cfg.ChiefFallback, true, nil
}

// Run executes the full pipeline and streams Events. It closes the returned
// channel when the run completes (success or error). The caller must drain it.
func Run(ctx context.Context, cfg Config) <-chan Event {
	ch := make(chan Event, 64)
	go func() {
		defer close(ch)
		cfg.applyDefaults()
		if err := run(ctx, cfg, ch); err != nil {
			ch <- Event{Type: EventError, Message: redact.String(err.Error())}
		}
	}()
	return ch
}

func emit(ch chan<- Event, e Event) {
	e.Message = redact.String(e.Message)
	select {
	case ch <- e:
	default: // drop if the TUI is too slow; never block pipeline
	}
}

// Every event message passes through emit, which redacts: run-log lines are
// built from provider diagnostics, and a provider that echoes its own query
// string would otherwise print a credential to the TUI and to the headless
// stream.
func log(ch chan<- Event, msg string) {
	emit(ch, Event{Type: EventLog, Message: msg})
}

func agentStatus(ch chan<- Event, role string, status model.AgentStatus, r *model.Report) {
	emit(ch, Event{Type: EventStatus, Agent: role, Status: status, Report: r})
}

func run(ctx context.Context, cfg Config, ch chan<- Event) error {
	start := time.Now()
	if err := validateFrozenRun(cfg); err != nil {
		return err
	}
	if cfg.Frozen != nil {
		ctx = withResearchTime(ctx, cfg.Frozen.AsOf)
		ctx = withFrozenModelTools(ctx)
	}
	if cfg.ResearchMode != "legacy" && cfg.ResearchMode != "thesis" {
		return fmt.Errorf("research_mode must be legacy or thesis")
	}
	if cfg.ResearchMode == "thesis" {
		if err := validateResearchBudgets(cfg.Research); err != nil {
			return err
		}
	}

	// Resolve + validate the cheap-research engine up front. Fail fast on a
	// misconfigured API/local engine rather than letting every cheap call fail
	// one-by-one (which would look like a silent provider outage). Note CLIClaude
	// is intentionally not a cheap engine: it shares model config with the synthesis
	// role, so routing the cheap roles to it would silently run them on Opus. To use
	// Claude cheaply, keep cheap_engine=gemini and override binaries.gemini=claude +
	// models.gemini=haiku.
	cheapCLI, cheapAPI, cheapConc, err := resolveCheapEngine(cfg)
	if err != nil {
		return err
	}
	// And the risk policy, for the same reason again: an unsatisfiable σ band
	// drops every idea the Chief writes, which is indistinguishable from the
	// Chief writing nothing sound. Checked here rather than in config.Load
	// because riskDefaults has filled the unset side by now — see
	// validateRiskPolicy.
	if err := validateRiskPolicy(cfg.Risk); err != nil {
		return err
	}
	// Resolve + validate the Chief Analyst's own engine up front too, for the
	// same fail-fast reason as the cheap engine above: a misconfigured
	// chief_engine=api should surface before Stage 0.5's universe-wide fetch
	// (and, in thesis mode, before any research spend), not after it. Resolved
	// before the fallback below because chiefFallbackAllowed needs to know
	// which engine the primary call actually runs on.
	chiefE, err := resolveChiefEngine(cfg)
	if err != nil {
		return err
	}
	// Validate the optional Chief Analyst DeepSeek fallback up front too, for
	// the same reason: a broken [chief_fallback] should surface now, not after
	// a full 15-minute synthesis attempt has already failed.
	fallbackAPI, fallbackOK, err := chiefFallbackAllowed(cfg, chiefE)
	if err != nil {
		return err
	}
	// What the cheap engine can actually do. Every scout/specialist prompt states
	// this, and reports are checked against it afterwards — the personas assume a
	// search-capable CLI, which is false on the HTTP engine.
	cheapCaps := agents.Capabilities{WebSearch: engineWebSearch(cheapCLI)}
	if cfg.Frozen != nil {
		cheapCaps.WebSearch = false
	}
	if !cheapCaps.WebSearch {
		log(ch, "Cheap engine has no web search: agents are told not to cite sources, and any [source:] tag they emit anyway will be stripped.")
	}

	// Load agent personas
	reg, err := agents.Load(cfg.AgentsDir)
	if err != nil {
		return fmt.Errorf("load agents: %w", err)
	}

	// Load universe
	uni, err := universe.Load()
	if err != nil {
		return fmt.Errorf("load universe: %w", err)
	}

	// Create run store
	run := cfg.EvaluationRun
	if run == nil {
		run, err = store.New(cfg.RunsDir)
		if err != nil {
			return fmt.Errorf("create run dir: %w", err)
		}
	}
	log(ch, fmt.Sprintf("Run directory: %s", run.Dir))
	if cfg.Frozen == nil {
		_ = store.CleanupOldRuns(cfg.RunsDir, cfg.KeepRuns)
	}

	// Start worker pool
	p := newPool(cfg.Workers, cfg.Models, cfg.Binaries, cheapAPI, cheapCLI, cheapConc)
	p.start(ctx)
	defer p.stop()

	// Initialize market data service
	cache := marketdata.NewCache(cfg.DataDir)
	// Cache keys are date-scoped, so yesterday's entries can never be read
	// again — but nothing removed them, and a universe-wide pre-screen writes a
	// few hundred files a day.
	if cfg.Frozen == nil && cfg.DataCacheDays > 0 {
		if n, err := cache.Prune(time.Duration(cfg.DataCacheDays) * 24 * time.Hour); err != nil {
			log(ch, fmt.Sprintf("warn: prune data cache: %v", err))
		} else if n > 0 {
			log(ch, fmt.Sprintf("Pruned %d cached data file(s) older than %d days", n, cfg.DataCacheDays))
		}
	}
	// Yahoo News comes before AlphaVantage on the news domain deliberately. It is
	// keyless and global, so it is the source that covers the whole shortlist;
	// AlphaVantage adds its scored articles and the earnings calendar on top when
	// its 25-a-day free budget allows. The order is what a reader sees first, and
	// it should be the evidence that is actually there for every name.
	avProvider := marketdata.NewAlphaVantageProvider(cfg.Providers.AlphaVantageKey, cfg.DataDir)
	dataSvc := marketdata.NewService(
		cache,
		marketdata.NewEdgarProvider(cfg.Providers.ContactEmail, cache),
		// Alpaca before Yahoo on the news domain: it is keyed and answering,
		// where Yahoo's endpoints are currently 429 to this host. Yahoo stays
		// second because it is the only source that reaches a foreign listing
		// under its own symbol. Both merge, and pack.go drops an exact repeat
		// of a headline the other already supplied.
		marketdata.NewAlpacaNewsProvider(cfg.Providers.AlpacaKeyID, cfg.Providers.AlpacaSecret),
		marketdata.NewYahooNewsProvider(),
		avProvider,
		marketdata.NewYahooOptionsProvider(),
		marketdata.NewFredProvider(cfg.Providers.FredKey),
	)
	if cfg.Frozen != nil {
		dataSvc = marketdata.NewFrozenService(cfg.Frozen)
	}
	// AlphaVantage's free tier is 25 requests a day against the key, not against
	// the run, so a run can start with the budget already gone. Say so up front:
	// on 2026-09-01 the counter stood at 24 of 25 and the run discovered it one
	// ticker at a time, five minutes in, as a scatter of unrelated-looking
	// failures. A run with no key configured is not degraded and says nothing.
	avBudget := dailyBudgetOf(avProvider)
	if cfg.Frozen != nil {
		avBudget = nil
	}
	if avBudget != nil {
		used, limit := avBudget.DailyBudget()
		log(ch, fmt.Sprintf("AlphaVantage daily budget: %d of %d requests spent, %d left for this run",
			used, limit, limit-used))
	}

	var shortlist []model.Candidate
	var domainStatuses []model.DomainStatus

	// Per-stage wall clock and every provider failure, for metadata.json. Only
	// per-agent durations were kept before, so the in-process stages — which are
	// most of a run's wall time — were entirely unattributed, and provider
	// errors lived only in data/<domain>.json.
	stageMS := map[string]int64{}
	var dataErrors []string
	stage := func(name string, t time.Time) { stageMS[name] = time.Since(t).Milliseconds() }

	// Prices route per symbol: Alpaca for US equities when a key is configured,
	// Yahoo for foreign listings, index benchmarks and FX. With no Alpaca key
	// this is Yahoo alone and the run behaves exactly as it did before.
	yahoo := marketdata.NewYahooClient(cache)
	yahoo.SetPriceTTL(cfg.PriceTTL)
	livePrices := marketdata.NewPrices(cfg.Providers.AlpacaKeyID, cfg.Providers.AlpacaSecret, cache)
	livePrices.SetPriceTTL(cfg.PriceTTL)
	var prices marketdata.PriceSource = livePrices
	// One FX table for the whole run: every liquidity floor and every position
	// size is stated in USD, and half this universe does not trade in it.
	//
	// FX deliberately holds the *Yahoo* client, not the router. Its symbols are
	// synthetic pairs like EURUSD=X, which carry no exchange suffix and so read
	// as US listings — routing them would send a currency pair to an equities
	// API that has never heard of it.
	fx := marketdata.NewFXRates(yahoo)
	if cfg.Frozen != nil {
		prices = cfg.Frozen
		fx = marketdata.NewFXRates(cfg.Frozen)
	}

	var indices []string
	if cfg.Mode == model.ModeIndependent {
		indices = selectIndices(cfg.Indices)
		if len(indices) == 0 {
			return fmt.Errorf("no valid indices selected (known: %s)", strings.Join(universe.AllIndices(), ", "))
		}
	}

	// ── Stage 0.5: universe-wide quant pre-screen (in-process, no model) ──────
	//
	// Rank every constituent before any model sees the index, so the scouts
	// filter a computed shortlist instead of inventing one from familiarity.
	prescreenParams := defaultPrescreenParams()
	prescreenParams.TopPerIndex = cfg.PrescreenTopPerIndex
	if cfg.PrescreenPullbackPerIndex > 0 {
		prescreenParams.PullbackPerIndex = cfg.PrescreenPullbackPerIndex
	}
	if cfg.PrescreenBasePerIndex > 0 {
		prescreenParams.BasePerIndex = cfg.PrescreenBasePerIndex
	}
	if cfg.PrescreenDriftPerIndex > 0 {
		prescreenParams.DriftPerIndex = cfg.PrescreenDriftPerIndex
	}
	prescreenParams.ADVMinUSD = cfg.Risk.ADVMinUSD
	var prescreen *Prescreen
	if cfg.Mode == model.ModeIndependent {
		prescreenStart := time.Now()
		total := 0
		for _, idx := range indices {
			total += len(uni.Constituents(idx))
		}
		log(ch, fmt.Sprintf("Stage 0.5: pre-screening %d names across %s (cold fetch, cached ~0s)…",
			total, strings.Join(indices, ", ")))
		// EDGAR's report-date index feeds the drift leg. It shares the SEC
		// contact and disk cache with every other EDGAR provider, and is
		// additive: without a contact email it returns nothing and the
		// pre-screen ranks exactly as it did before.
		var reportDates marketdata.ReportDateSource = marketdata.NewEdgarReportDates(cfg.Providers.ContactEmail, cache)
		if cfg.Frozen != nil {
			reportDates = cfg.Frozen
		}
		prescreen = runPrescreen(ctx, ch, prices, fx, reportDates, uni, indices, prescreenParams)
		logPackErrors(ch, "prescreen", prescreen.Errors)
		dataErrors = append(dataErrors, prefixed("prescreen", prescreen.Errors)...)
		if err := run.WritePrescreen(prescreen); err != nil {
			log(ch, fmt.Sprintf("warn: write prescreen.json: %v", err))
		}
		ranked := 0
		for _, idx := range indices {
			ranked += len(prescreen.Ranked(idx))
		}
		log(ch, fmt.Sprintf("Pre-screen ranked %d of %d names (%d excluded: illiquid, too little history, or unavailable)",
			ranked, total, total-ranked))
		stage("prescreen", prescreenStart)
	}

	if cfg.ResearchMode == "thesis" {
		return runThesis(ctx, cfg, ch, run, reg, uni, p, cheapCLI, chiefE, dataSvc, prices, fx, prescreen, indices, start, stageMS)
	}

	// ── Stage 1: Scouts (independent research only) ────────────────────────────
	scoutStart := time.Now()
	if cfg.Mode == model.ModeIndependent {
		log(ch, fmt.Sprintf("Stage 1: screening %s with scouts…", strings.Join(indices, ", ")))
		resultChans := make([]<-chan model.Report, len(indices))

		for i, idx := range indices {
			cs := uni.Constituents(idx)
			// Say how many names are really on the table. The universe files
			// are curated samples, not full index memberships, and logging
			// "screening sp500" invited the belief that all 500 were screened.
			log(ch, fmt.Sprintf("screening %d of %s", len(cs), idx))
			prompt, err := reg.AssemblePrompt(agents.PromptParams{
				Role:                 "scout",
				Mode:                 cfg.Mode,
				RunTS:                run.TS,
				IndexKey:             idx,
				IndexConstituentList: universe.ConstituentList(cs),
				PrescreenTable:       prescreen.Table(idx, prescreenParams),
				Caps:                 cheapCaps,
			})
			if err != nil {
				log(ch, fmt.Sprintf("warn: assemble scout-%s prompt: %v", idx, err))
				continue
			}
			role := "scout-" + idx
			agentStatus(ch, role, model.StatusRunning, nil)
			resultChans[i] = p.submit(cheapCLI, role, string(model.StageScreening), prompt, cfg.Timeouts.Screening, cfg.Retry)
		}

		for i, idx := range indices {
			if resultChans[i] == nil {
				continue
			}
			role := "scout-" + idx
			r := <-resultChans[i]
			// A scout gets no data pack, so on a search-less engine every
			// [source:] tag it emits is invented — and the shortlist itself
			// would otherwise be selected on that invented evidence.
			if !cheapCaps.WebSearch {
				cleaned, fabricated := scrubCitations(r.Stdout, nil)
				r.Stdout = cleaned
				if len(fabricated) > 0 {
					log(ch, fmt.Sprintf("warn: %s cited %d fabricated source(s) on a search-less engine (%s) — tags stripped",
						role, len(fabricated), strings.Join(fabricated, ", ")))
				}
			}
			r.Path = fmt.Sprintf("%s/%s.md", run.Dir, role)
			if err := run.WriteReport(role, r.Stdout); err != nil {
				log(ch, fmt.Sprintf("warn: write %s report: %v", role, err))
			}
			// Scouts had no status row of their own, so the whole screening
			// stage was missing from the run's own accounting.
			domainStatuses = append(domainStatuses, model.DomainStatus{
				Domain: role, Status: r.Status, Err: r.Err,
				Duration: r.Duration, Attempts: r.Attempts, Tokens: r.Tokens, Usage: r.Usage,
			})
			if r.Status == model.StatusFailed {
				agentStatus(ch, role, model.StatusFailed, &r)
				log(ch, fmt.Sprintf("Scout %s failed (%s) — continuing", idx, r.Err))
				continue
			}
			agentStatus(ch, role, model.StatusDone, &r)
			candidates, rejected := parseScoutResult(r.Stdout, idx, uni.Constituents(idx))
			if len(rejected) > 0 {
				log(ch, fmt.Sprintf("warn: %s nominated %d symbol(s) not in %s (%s) — dropped",
					role, len(rejected), idx, strings.Join(rejected, ", ")))
			}
			shortlist = append(shortlist, candidates...)
		}

		shortlist = universe.Dedupe(shortlist)
		log(ch, fmt.Sprintf("Shortlist after dedup: %d names", len(shortlist)))
		for _, c := range collapseOverlappingNominations(uni, shortlist) {
			log(ch, fmt.Sprintf("%s was nominated by %s, which share the listing — counting that as one reading, not agreement",
				c.Ticker, strings.Join(c.NominatedBy, " and ")))
		}
		// Agreement and disagreement between scouts are the two things the merge
		// used to swallow. Both are worth saying out loud: the run log is where
		// a reader finds out that two independent tables produced the same name,
		// or that which direction a name shipped in was decided by collection
		// order rather than by evidence.
		for _, c := range shortlist {
			if len(c.Contested) > 0 {
				log(ch, fmt.Sprintf("warn: %s was nominated %s here and the opposite way by %s — keeping the first reading and charging its merit for the contradiction",
					c.Ticker, c.Bias, strings.Join(c.Contested, ", ")))
			}
			if c.Nominations > 1 {
				log(ch, fmt.Sprintf("%s was nominated by %d scouts in the same direction", c.Ticker, c.Nominations))
			}
		}
		// Label each nomination with the archetype its own pre-screen row was
		// classified under, before the merge reads it. The scout picks which
		// table to nominate from; the label is the classifier's to assign.
		for i := range shortlist {
			if r, ok := prescreen.Row(shortlist[i].Index, shortlist[i].Ticker); ok {
				shortlist[i].Setup = r.Setup
			}
		}
		before := len(shortlist)
		// One coverage answer, read by both the merit sort and the hard cap, so
		// the two cannot disagree about which names the run can research.
		coverage := func(ticker string) float64 { return expectedCoverage(cfg.Weights, ticker) }
		// Merit, not round-robin: keep the nominations the pre-screen composite
		// agrees with, in the direction they were nominated in — but hold a few
		// slots for the setups that are not "this has already run", because the
		// composite is built from trailing returns and left alone it fills the
		// whole shortlist with names at their highs.
		shortlist = universe.CapMerit(shortlist, universe.MeritCaps{
			Max:              cfg.MaxShortlist,
			PerIndex:         cfg.MaxPerIndex,
			PerSector:        shortlistPerSector(cfg.Risk.MaxPerSector),
			ThinlyCovered:    cfg.MaxThinlyCovered,
			CoverageFloor:    thinCoverage,
			Reserve:          cfg.ShortlistReserve,
			ReservePredicate: func(c model.Candidate) bool { return c.Setup != "" && c.Setup != SetupContinuation },
			ReserveMinMerit:  cfg.ShortlistReserveMinMerit,
			Score:            func(c model.Candidate) float64 { return meritScore(prescreen, coverage, c) },
			Coverage:         func(c model.Candidate) float64 { return coverage(c.Ticker) },
		})
		if len(shortlist) < before {
			coverageRule := fmt.Sprintf("max %d under %.0f%% coverage", cfg.MaxThinlyCovered, thinCoverage*100)
			switch {
			case cfg.MaxThinlyCovered < 0:
				coverageRule = "no coverage cap"
			case cfg.MaxThinlyCovered == 0:
				coverageRule = fmt.Sprintf("none under %.0f%% coverage — the evidence floor would delete them", thinCoverage*100)
			}
			log(ch, fmt.Sprintf("Shortlist trimmed %d → %d by pre-screen merit (max %d per index, %s)",
				before, len(shortlist), cfg.MaxPerIndex, coverageRule))
		}
		if cfg.ShortlistReserve > 0 {
			var held int
			for _, c := range shortlist {
				if c.Setup != "" && c.Setup != SetupContinuation {
					held++
				}
			}
			log(ch, fmt.Sprintf("shortlist setups: %d of %d are drift, pullback or base (reserve %d at merit ≥ %+.2f)",
				held, len(shortlist), cfg.ShortlistReserve, cfg.ShortlistReserveMinMerit))
		}
		for _, c := range shortlist {
			log(ch, fmt.Sprintf("shortlist: %s (%s, %s) — scout %s, %s, merit %+.2f",
				c.Ticker, c.Index, c.Sector, c.Bias, orUnknown(c.Setup), meritScore(prescreen, coverage, c)))
		}
		if err := run.WriteShortlist(shortlist); err != nil {
			log(ch, fmt.Sprintf("warn: write shortlist: %v", err))
		}

		if len(shortlist) == 0 {
			return fmt.Errorf("all scouts failed or returned no candidates — cannot continue")
		}
	} else {
		// Single-stock: build shortlist from user ticker
		c, _ := uni.Lookup(cfg.Ticker)
		shortlist = []model.Candidate{
			{Ticker: c.Ticker, Name: c.Name, Sector: c.Sector, Bias: model.BiasNeutral, Reason: "user-specified", Index: c.Index},
		}
		log(ch, fmt.Sprintf("Single-stock mode: analysing %s", c.Ticker))
	}

	// SEC filings and listed option chains are US instruments, so the non-US half
	// of a balanced shortlist reaches fewer domains than the rest of it. Say so
	// once, plainly: nothing in the run said it, and two domains reporting gaps
	// for those names read as two domains failing.
	thinlyCovered := thinlyCoveredNames(shortlist, cfg.Weights, thinCoverage)
	if len(thinlyCovered) > 0 {
		log(ch, fmt.Sprintf("%d of %d shortlisted names reach under %.0f%% of the domain weight (%s): US filings and option chains do not cover them, so fundamentals and sentiment must stand down.",
			len(thinlyCovered), len(shortlist), thinCoverage*100, strings.Join(thinlyCovered, ", ")))
	}

	stage("screening", scoutStart)

	// ── Stage 1.5: price history + quant metrics (in-process, no model) ───────
	quantStart := time.Now()
	log(ch, "Stage 1.5: fetching price history and computing quant metrics…")
	agentStatus(ch, "quant-data", model.StatusRunning, nil)
	// The pre-screen already fetched every one of these series; the shared cache
	// serves them back here without a second round trip.
	quantPack, quantSeries := buildQuantPack(ctx, ch, run, prices, fx, shortlist)
	logPackErrors(ch, "quant", quantPack.Errors)
	dataErrors = append(dataErrors, prefixed("quant", quantPack.Errors)...)
	if trimmed, dropped := dropStalePriced(cfg.Mode, shortlist, quantPack); len(dropped) > 0 {
		shortlist = trimmed
		msg := fmt.Sprintf("dropped %s from the shortlist: priced off a superseded close, so no level computed for %s could be acted on",
			strings.Join(dropped, ", "), pluralNames(len(dropped)))
		log(ch, "warn: "+msg)
		dataErrors = append(dataErrors, "quant: "+msg)
		if err := run.WriteShortlist(shortlist); err != nil {
			log(ch, fmt.Sprintf("warn: rewrite shortlist: %v", err))
		}
	}
	if len(quantPack.ByTicker) > 0 {
		agentStatus(ch, "quant-data", model.StatusDone, nil)
		log(ch, fmt.Sprintf("Quant metrics computed for %d/%d tickers", len(quantPack.ByTicker), len(shortlist)))
	} else {
		agentStatus(ch, "quant-data", model.StatusFailed, nil)
		log(ch, "warn: no price data available — specialists will run ungrounded")
	}

	stage("quant", quantStart)

	// ── Stage 2: Specialists (parallel) ────────────────────────────────────────
	analysisStart := time.Now()
	log(ch, "Stage 2: running 5 specialist agents…")
	specialists := []struct {
		role string
		cli  model.CLI
	}{
		{"news", cheapCLI},
		{"fundamentals", cheapCLI},
		{"quant", cheapCLI},
		{"sentiment", cheapCLI},
		{"macro", cheapCLI},
	}

	eventDates := map[string]time.Time{}
	verifiedDates := map[string]bool{}
	collectQuantDates(verifiedDates, quantPack)
	// The filing dates the drift block renders. They come from the pre-screen
	// rather than from a data pack, so nothing else registers them, and an
	// unregistered date the app itself printed reads to checkFabricatedDates as
	// an invention.
	collectDriftDates(verifiedDates, prescreen, shortlist)
	specChans := make([]<-chan model.Report, len(specialists))
	grounded := make([]bool, len(specialists))
	ungrounded := make([][]string, len(specialists))
	abstained := make([][]string, len(specialists))
	citable := make([]map[string]bool, len(specialists))
	tickers := make([]string, len(shortlist))
	for j, c := range shortlist {
		tickers[j] = c.Ticker
	}
	for i, sp := range specialists {
		pack := dataSvc.BuildPack(ctx, sp.role, tickers)
		if cfg.Frozen != nil && sp.role == "fundamentals" {
			cfg.Frozen.AddLegacySources(ctx, pack, tickers)
		}
		// The verified earnings dates ride in on the news pack; the validator and
		// the risk checks read the same map the news prompt renders.
		for t, d := range pack.EventDates {
			eventDates[t] = d
		}
		collectVerifiedDates(verifiedDates, pack)
		// Multiples are computed here rather than asked for: the filings give
		// shares and EPS, the quant pack gives the price, and dividing them is
		// arithmetic. Done before WriteDataPack so the artifact is what the
		// agent saw.
		if sp.role == "fundamentals" {
			enrichFundamentals(pack, quantPack)
		}
		logPackErrors(ch, sp.role, pack.Errors)
		dataErrors = append(dataErrors, prefixed(sp.role, pack.Errors)...)
		// No provider serves the quant domain — its evidence is the computed
		// metrics pack — so data/quant.json was written byte-identical to
		// data/macro.json but for the label, with an empty ByTicker. That
		// artifact misrepresented what the quant agent actually saw.
		if sp.role != "quant" {
			if err := run.WriteDataPack(sp.role, pack); err != nil {
				log(ch, fmt.Sprintf("warn: write %s data pack: %v", sp.role, err))
			}
		}
		citable[i] = pack.Citable

		dataBlock := specialistDataBlock(sp.role, pack, quantPack, prescreen, shortlist)

		// Grounded means we injected *per-ticker* verified data for this domain.
		// Judging it from dataBlock != "" was misleading: macro facts and the
		// shared price context are appended to every role, so a sentiment pack
		// with an empty ByTicker still counted as grounded.
		grounded[i] = groundedFor(sp.role, pack, quantPack)
		ungrounded[i] = ungroundedFor(sp.role, pack, quantPack, tickers)
		abstained[i] = abstainedFor(sp.role, pack, tickers)
		if len(ungrounded[i]) > 0 {
			log(ch, fmt.Sprintf("data[%s]: no verified data for %d/%d tickers: %s",
				sp.role, len(ungrounded[i]), len(tickers), strings.Join(ungrounded[i], ", ")))
		}
		if len(abstained[i]) > 0 {
			log(ch, fmt.Sprintf("data[%s]: %d/%d tickers have data but no directional signal, so the domain stands down on them: %s",
				sp.role, len(abstained[i]), len(tickers), strings.Join(abstained[i], ", ")))
		}

		prompt, err := reg.AssemblePrompt(agents.PromptParams{
			Role:      sp.role,
			Mode:      cfg.Mode,
			RunTS:     run.TS,
			Shortlist: shortlist,
			Ticker:    cfg.Ticker,
			DataBlock: dataBlock,
			Caps:      cheapCaps,
		})
		if err != nil {
			log(ch, fmt.Sprintf("warn: assemble %s prompt: %v", sp.role, err))
			continue
		}
		agentStatus(ch, sp.role, model.StatusRunning, nil)
		specChans[i] = p.submit(sp.cli, sp.role, string(model.StageAnalysis), prompt, cfg.Timeouts.Analysis, cfg.Retry)
	}

	var specReports []agents.ReportContext
	var missingDomains []string

	for i, sp := range specialists {
		if specChans[i] == nil {
			missingDomains = append(missingDomains, sp.role)
			domainStatuses = append(domainStatuses, model.DomainStatus{Domain: sp.role, Status: model.StatusFailed, Err: "prompt assembly failed"})
			continue
		}
		r := <-specChans[i]

		// Enforce the capability rule the prompt declared: on a search-less
		// engine, strip any [source:] tag whose domain the run's own data does
		// not vouch for. Do it before the report is written or handed on, so
		// neither the artifact nor the Chief Analyst ever sees a fake citation.
		var fabricated []string
		if !cheapCaps.WebSearch {
			r.Stdout, fabricated = scrubCitations(r.Stdout, citable[i])
			if len(fabricated) > 0 {
				log(ch, fmt.Sprintf("warn: %s cited %d fabricated source(s) on a search-less engine (%s) — tags stripped",
					sp.role, len(fabricated), strings.Join(fabricated, ", ")))
			}
		}

		// Computed coverage is the authority: delete scores for names this domain
		// had no data for, or that were never on the shortlist, and say so in the
		// tail. The agent's own `missing` array is not the authority but it is
		// binding on itself — a name in both arrays loses its score too — and the
		// removals are named in a notice prepended to the prose, so a stripped
		// score cannot be read back out of the paragraphs. Done before the report
		// is written so the artifact is exactly what the Chief read.
		var enf enforcement
		if r.Status != model.StatusFailed {
			corrected, e, err := enforceSpecialistTail(sp.role, r.Stdout, ungrounded[i], abstained[i], shortlist)
			switch {
			case err != nil:
				// A report with no usable tail is a refusal or a truncation. It
				// used to pass as "done" on non-empty stdout alone and flow
				// into synthesis unnoticed; it fails the domain now.
				r.Status = model.StatusFailed
				r.Err = err.Error()
				log(ch, fmt.Sprintf("warn: %s report unusable: %v", sp.role, err))
			default:
				r.Stdout, enf = corrected, e
				if len(enf.Renamed) > 0 {
					log(ch, fmt.Sprintf("%s named %d company(ies) instead of their symbols (%s) — resolved, scores kept",
						sp.role, len(enf.Renamed), strings.Join(enf.Renamed, ", ")))
				}
				if len(enf.Corrected) > 0 {
					log(ch, fmt.Sprintf("warn: %s scored %d ticker(s) it had no verified data for (%s) — scores removed, names moved to `missing`",
						sp.role, len(enf.Corrected), strings.Join(enf.Corrected, ", ")))
				}
				if len(enf.OffShortlist) > 0 {
					log(ch, fmt.Sprintf("warn: %s scored %d ticker(s) not on the shortlist (%s) — scores removed",
						sp.role, len(enf.OffShortlist), strings.Join(enf.OffShortlist, ", ")))
				}
				if len(enf.SelfContradicted) > 0 {
					log(ch, fmt.Sprintf("warn: %s both scored and declared missing %d ticker(s) (%s) — scores removed",
						sp.role, len(enf.SelfContradicted), strings.Join(enf.SelfContradicted, ", ")))
				}
			}
		}

		r.Path = fmt.Sprintf("%s/%s.md", run.Dir, sp.role)
		if err := run.WriteReport(sp.role, r.Stdout); err != nil {
			log(ch, fmt.Sprintf("warn: write %s report: %v", sp.role, err))
		}

		// Grounded means we actually injected verified data into the prompt —
		// judged from the data we assembled, never from the model's own output.
		status := model.DomainStatus{
			Domain:                 sp.role,
			Status:                 r.Status,
			Err:                    r.Err,
			Duration:               r.Duration,
			Attempts:               r.Attempts,
			Tokens:                 r.Tokens,
			Usage:                  r.Usage,
			Grounded:               grounded[i],
			Ungrounded:             ungrounded[i],
			Abstained:              abstained[i],
			CorrectedScores:        enf.Corrected,
			OffShortlistScores:     enf.OffShortlist,
			SelfContradictedScores: enf.SelfContradicted,
			NeutralScores:          enf.Neutral,
			FabricatedCitations:    fabricated,
			ScoredNames:            enf.Scored,
		}

		if r.Status == model.StatusFailed {
			agentStatus(ch, sp.role, model.StatusFailed, &r)
			log(ch, fmt.Sprintf("Specialist %s failed — noting for chief analyst", sp.role))
			missingDomains = append(missingDomains, sp.role)
		} else {
			agentStatus(ch, sp.role, model.StatusDone, &r)
			specReports = append(specReports, agents.ReportContext{
				Domain:  sp.role,
				Content: r.Stdout,
			})
		}
		domainStatuses = append(domainStatuses, status)
	}

	// A specialist that missed a name it could have grounded wrote that part of
	// its report from recollection alone. That is a degraded run whatever the
	// agent's exit code said, so carry it through to the outcome below.
	covGaps := coverageGaps(domainStatuses)
	for _, g := range covGaps {
		log(ch, fmt.Sprintf("warn: %s has no verified data for %s — names it could have covered", g.Domain, strings.Join(g.Missing, ", ")))
	}

	// What each domain asserted that its evidence did not support. The tail
	// enforcement above already deleted these scores and logged them; nothing
	// carried them into the run's own verdict, so a run in which half of macro's
	// output was invented still reported `warnings: []` and `outcome: complete`.
	confab := confabulations(domainStatuses)
	for _, c := range confab {
		if c.severe() {
			log(ch, fmt.Sprintf("warn: %s invented %d of its %d score(s) — over the %.0f%% mark, so this run is degraded",
				c.Domain, c.invented(), c.Scored, confabulationThreshold*100))
		}
	}

	// Minimum check
	if len(specReports) < 2 {
		return fmt.Errorf("fewer than 2 specialist reports succeeded — cannot synthesise")
	}

	stage("analysis", analysisStart)

	// ── Stage 3: Chief Analyst (Claude) ───────────────────────────────────────
	synthStart := time.Now()
	log(ch, "Stage 3: Chief Analyst synthesising results…")

	// The weighting the Chief was told to apply is done here first, in
	// arithmetic the run can reproduce. It is shown to the model as the level to
	// start from, and enforced afterwards as the level it may only move by the
	// configured band.
	// Which domains stood down on which names, so a measured "nothing to read
	// here" stops being priced as a fetch that failed. Computed once and shared
	// with the degraded path below, so the fallback ranking cannot disagree with
	// the numbers the Chief was shown.
	stoodDown := standDowns(domainStatuses)
	bases := computeBaseScores(cfg.Weights, specReports, shortlist, stoodDown)

	// What this pipeline has actually achieved, measured by replaying its own
	// past ideas. It reaches the Chief as context for how hard to lean on
	// today's evidence, and the risk gate as the edge its expectancy check
	// assumes — replacing a prior with a measurement.
	var cal *scoreboard.Calibration
	if cfg.Frozen == nil {
		cal = trackRecord(ctx, ch, cfg, prices)
	}

	// And what that record *shows*, which the arithmetic cannot say on its own:
	// the attribution counts outcomes, and only the reasoning recorded with each
	// past idea says what the winners had in common. One cheap-engine call,
	// enforced against the counted cells, and skipped entirely below the closed-
	// trade threshold.
	var pmRes postMortemResult
	if cfg.Frozen == nil {
		pmRes = postMortem(ctx, ch, cfg, reg, p, cheapCLI, cheapCaps, prices)
	}
	if pmRes.Status.Domain != "" {
		domainStatuses = append(domainStatuses, pmRes.Status)
	}
	if pmRes.Report != "" {
		if err := run.WriteReport("post-mortem", pmRes.Report); err != nil {
			log(ch, fmt.Sprintf("warn: write post-mortem report: %v", err))
		}
	}
	if pmRes.PM != nil {
		if err := pmRes.PM.Save(run.Dir); err != nil {
			log(ch, fmt.Sprintf("warn: copy the post-mortem into the run: %v", err))
		}
	}

	verifiedCtx := verified{
		AsOf:      frozenAsOf(cfg),
		Universe:  uni,
		Quant:     quantPack,
		Shortlist: shortlist,
		Bases:     bases,
		Events:    eventDates,
		Dates:     verifiedDates,
		Series:    quantSeries,
	}
	if cal != nil && cal.NClosed > 0 {
		// Calibration.Block() returns "" below MinClosedForFeedback, so the log
		// used to announce a track record the Chief was never shown. Say which
		// it is: a run log that reports context the model did not receive is a
		// worse artifact than one that reports nothing.
		if cal.NClosed < scoreboard.MinClosedForFeedback {
			log(ch, fmt.Sprintf("track record: %d closed idea(s), %.0f%% profitable, avg %+.2fR — withheld from the Chief (%d of %d closed)",
				cal.NClosed, cal.WinRate*100, cal.AvgR, cal.NClosed, scoreboard.MinClosedForFeedback))
		} else {
			log(ch, fmt.Sprintf("track record: %d closed idea(s), %.0f%% profitable, avg %+.2fR",
				cal.NClosed, cal.WinRate*100, cal.AvgR))
		}
		if err := cal.Save(run.Dir); err != nil {
			log(ch, fmt.Sprintf("warn: copy the track record into the run: %v", err))
		}
	}
	if r, ok := cal.RealizedEdge(); ok {
		verifiedCtx.RealizedR = &r
		verifiedCtx.RealizedN = cal.NClosed
		log(ch, fmt.Sprintf("risk: expectancy blends the simulation with the measured %+.2fR over %d closed idea(s)",
			r, cal.NClosed))
	}
	for _, b := range bases {
		if b.Direction == "" {
			log(ch, fmt.Sprintf("base: %s — no domain scored it", b.Ticker))
			continue
		}
		capNote := ""
		if b.Cap > 0 {
			capNote = fmt.Sprintf(", capped at %d", b.Cap)
		}
		log(ch, fmt.Sprintf("base: %s %s %d (%.0f%% of domain weight%s)",
			b.Ticker, b.Direction, b.Confidence, b.CoveredWeight*100, capNote))
	}

	prompt, err := reg.AssemblePrompt(agents.PromptParams{
		Role:             "chief-analyst",
		Mode:             cfg.Mode,
		RunTS:            run.TS,
		Shortlist:        shortlist,
		Ticker:           cfg.Ticker,
		Reports:          specReports,
		Missing:          missingDomains,
		Weights:          cfg.Weights,
		QuantBlock:       quantPack.CompactBlock() + driftBlock(prescreen, shortlist) + regimeSuffix(quantPack),
		BaseScoreBlock:   baseScoreBlock(bases, cfg.ChiefAdjustBand),
		TrackRecordBlock: cal.Block(),
		PostMortemBlock:  pmRes.PM.Block(),
	})
	if err != nil {
		return fmt.Errorf("assemble chief-analyst prompt: %w", err)
	}

	// The primary Chief Analyst call gets its own attempt budget: a timeout
	// means "too slow," not "flaky," so retrying identically just delays
	// reaching the DeepSeek fallback below. cfg.Retry (still governing
	// screening/analysis, and the fallback's own transient-error retries) is
	// left untouched. chiefTarget bakes this into every purpose and both
	// engines, so it is computed once here and reused for the corrective call.
	chiefT := chiefTarget(chiefE, cfg, chiefInitial)

	agentStatus(ch, "chief-analyst", model.StatusRunning, nil)
	r := runAgent(ctx, chiefT.CLI, "chief-analyst", string(chiefT.Stage), prompt, chiefT.Timeout, chiefT.Retry, chiefT.Model, chiefT.Binary, chiefT.API)
	r.Path = fmt.Sprintf("%s/chief-analyst.md", run.Dir)
	if err := run.WriteReport("chief-analyst", r.Stdout); err != nil {
		log(ch, fmt.Sprintf("warn: write chief-analyst report: %v", err))
	}
	// The synthesis call is the single most expensive step in the run and had
	// no row of its own; only the five specialists were accounted for.
	//
	// The index is kept because a corrective re-prompt is a second call on this
	// same row: recording only the first left 273s of the 2026-09-01 run — 36% of
	// its most expensive stage — visible in `stages.synthesis` and accounted for
	// nowhere, under `attempts: 1`.
	chiefStatusIdx := len(domainStatuses)
	domainStatuses = append(domainStatuses, model.DomainStatus{
		Domain: "chief-analyst", Status: r.Status, Err: r.Err,
		Duration: r.Duration, Attempts: r.Attempts, Tokens: r.Tokens, Usage: r.Usage,
	})

	var ideas *model.IdeasResult
	var warnings []warning
	outcome := "complete"
	if len(covGaps) > 0 {
		outcome = "degraded"
	}
	for _, c := range confab {
		if c.severe() {
			outcome = "degraded"
		}
	}

	var synthesisFallbackEngine string
	if r.Status == model.StatusFailed {
		agentStatus(ch, "chief-analyst", model.StatusFailed, &r)
		fellBack := false
		if fallbackOK {
			fbIdeas, fbWarnings, fbStatus, ok := attemptChiefFallback(ctx, ch, run, cfg, fallbackAPI, prompt, r.Err, verifiedCtx)
			domainStatuses = append(domainStatuses, fbStatus)
			if ok {
				ideas = fbIdeas
				warnings = fbWarnings
				synthesisFallbackEngine = fallbackAPI.Model
				fellBack = true
			}
		}
		if !fellBack {
			log(ch, "Chief analyst failed — building degraded fallback from specialist scores…")
			ideas = buildDegradedIdeas(cfg, specReports, shortlist, stoodDown)
		}
		outcome = "degraded"
	} else {
		agentStatus(ch, "chief-analyst", model.StatusDone, &r)
		// Parse and validate ideas
		var parseErr error
		ideas, parseErr = parseIdeas(r.Stdout)
		if parseErr == nil {
			warnings = validateIdeas(ideas, cfg, verifiedCtx)
			findings := applyRiskGate(ideas, verifiedCtx, cfg.Risk)

			// Corrective re-prompt for violations worth a second model call:
			// concentration, inverted stop/target levels, a confidence scored
			// against a different thesis than the domains reported, and every
			// actionable risk-gate violation.
			//
			// Observational findings are logged and warned about but never
			// re-prompted: there is one corrective call, and asking the Chief to
			// fix the account's equity or the run's missing price history spends
			// it on something no re-emission can change.
			var repromptReasons []string
			// Whether the one corrective call actually landed. The drop note
			// below used to claim it had, unconditionally.
			correctiveApplied := false
			for _, f := range findings {
				log(ch, "risk: "+f.Message)
				if f.actionable() {
					repromptReasons = append(repromptReasons, f.Message)
				}
			}
			for _, w := range warnings {
				if strings.Contains(w.Message, "level ordering") {
					repromptReasons = append(repromptReasons, fmt.Sprintf("fix %s: stop/entry/target must be ordered for the trade direction (BUY: stop < entry < target; SELL: target < entry < stop)", w.Ticker))
				}
				if strings.Contains(w.Message, "far confidence") {
					repromptReasons = append(repromptReasons, fmt.Sprintf(
						"%s: your confidence was more than %d points outside its computed base score — re-score it from the base in the \"Computed base scores\" table and name each adjustment",
						w.Ticker, 2*cfg.ChiefAdjustBand))
				}
			}

			if len(repromptReasons) > 0 {
				log(ch, "Validation failed — attempting corrective re-prompt…")
				first := r
				reprompt := correctivePrompt(prompt, first.Stdout, repromptReasons, bases)
				correctiveT := chiefTarget(chiefE, cfg, chiefCorrective)
				r = runAgent(ctx, correctiveT.CLI, "chief-analyst", string(correctiveT.Stage), reprompt, correctiveT.Timeout, correctiveT.Retry, correctiveT.Model, correctiveT.Binary, correctiveT.API)
				r.Path = first.Path // same artifact; the corrected pass is written over it

				// The second call is part of the same synthesis step, so it lands
				// on the same status row rather than vanishing from the accounting.
				st := &domainStatuses[chiefStatusIdx]
				st.Duration += r.Duration
				st.Attempts += r.Attempts
				st.Tokens += r.Tokens
				st.Usage = append(st.Usage, r.Usage...)
				if r.Status != model.StatusFailed {
					st.Status, st.Err = r.Status, r.Err
				}

				// Every outcome of the corrective call is recorded. A silent
				// discard here is worse than no re-prompt at all: the run spends
				// a full synthesis call, keeps the uncorrected book, and then
				// describes itself as having been corrected.
				switch newIdeas, err := parseIdeas(r.Stdout); {
				case r.Status == model.StatusFailed:
					st.Corrective = "failed"
					log(ch, fmt.Sprintf("warn: the corrective re-prompt failed (%v) — shipping the first pass unchanged", r.Err))
					warnings = append(warnings, warning{Message: fmt.Sprintf(
						"the corrective re-prompt failed (%v); the ideas below are the uncorrected first pass", r.Err)})
				case err != nil:
					st.Corrective = "unparseable"
					log(ch, fmt.Sprintf("warn: the corrective re-prompt returned unparseable JSON (%v) — shipping the first pass unchanged", err))
					warnings = append(warnings, warning{Message: fmt.Sprintf(
						"the corrective re-prompt returned unparseable JSON (%v); the ideas below are the uncorrected first pass", err)})
				default:
					st.Corrective = "applied"
					correctiveApplied = true
					ideas = newIdeas
					warnings = validateIdeas(ideas, cfg, verifiedCtx)
					findings = applyRiskGate(ideas, verifiedCtx, cfg.Risk)
					// Persist the reasoning that produced the ideas actually
					// shipped. Only the first response used to be written, so
					// chief-analyst.md documented ranks, levels and
					// confidences that contradicted the ideas.json beside it.
					if err := run.WriteReport("chief-analyst", correctedReport(first.Stdout, r.Stdout, repromptReasons)); err != nil {
						log(ch, fmt.Sprintf("warn: write corrected chief-analyst report: %v", err))
					}
				}
			}

			// One corrective call is the budget. An idea whose construction is
			// still negative-expectancy or noise-stopped after that is dropped:
			// shipping four ideas is a success, and an unsound one is worse than
			// none because it looks like the others.
			for _, f := range findings {
				msg := f.Message
				if f.Ticker != "" {
					msg = strings.TrimPrefix(msg, f.Ticker+": ")
				}
				warnings = append(warnings, warning{Ticker: f.Ticker, Message: "risk gate: " + msg})
			}
			if dropped := dropViolating(ideas, findings); len(dropped) > 0 {
				// Say which it was. "After one corrective re-prompt" was printed
				// whether or not the corrective pass had produced anything, so
				// the artifact could not distinguish a book the Chief had been
				// given a chance to fix from one it had not.
				after := "after one corrective re-prompt"
				switch {
				case len(repromptReasons) == 0:
					after = "with no corrective re-prompt spent"
				case !correctiveApplied:
					after = "after a corrective re-prompt that did not land"
				}
				log(ch, fmt.Sprintf("Risk gate dropped %d idea(s) %s", len(dropped), after))
				for _, d := range dropped {
					log(ch, "dropped: "+d)
				}
				ideas.Notes = strings.TrimSpace(ideas.Notes + fmt.Sprintf(
					" Risk gate dropped %d idea(s) %s: %s. Fewer ideas is the intended outcome — an unsound construction is worse than none.",
					len(dropped), after, strings.Join(dropped, "; ")))
			}
		} else {
			log(ch, fmt.Sprintf("warn: parse ideas JSON: %v", parseErr))
			fellBack := false
			if fallbackOK {
				fbIdeas, fbWarnings, fbStatus, ok := attemptChiefFallback(ctx, ch, run, cfg, fallbackAPI, prompt, fmt.Sprintf("unparseable JSON: %v", parseErr), verifiedCtx)
				domainStatuses = append(domainStatuses, fbStatus)
				if ok {
					ideas = fbIdeas
					warnings = fbWarnings
					synthesisFallbackEngine = fallbackAPI.Model
					fellBack = true
				}
			}
			if !fellBack {
				ideas = buildDegradedIdeas(cfg, specReports, shortlist, stoodDown)
				ideas.Notes = fmt.Sprintf("JSON parse error: %v — %s", parseErr, ideas.Notes)
			}
			outcome = "degraded"
		}
	}

	// Record the verified price each idea was generated at (scoreboard baseline).
	// Rounded to the cent: Yahoo's closes arrive as float32, so writing them raw
	// put "49.13999938964844" in the artifact for a €49.14 close.
	for i := range ideas.Ideas {
		if m, ok := quantPack.ByTicker[strings.ToUpper(ideas.Ideas[i].Ticker)]; ok && m.LastClose > 0 {
			ideas.Ideas[i].PriceAtGeneration = math.Round(m.LastClose*100) / 100
		}
	}

	// Final persistence and metadata
	if cfg.Frozen != nil {
		ideas.GeneratedAt = cfg.Frozen.AsOf.UTC().Format(time.RFC3339)
	}
	if err := run.WriteIdeas(ideas); err != nil {
		log(ch, fmt.Sprintf("warn: write ideas.json: %v", err))
	}

	for _, g := range covGaps {
		warnings = append(warnings, warning{
			Message: fmt.Sprintf("%s: no verified data for %s — names it could have covered", g.Domain, strings.Join(g.Missing, ", ")),
		})
	}
	// One warning for one spent key. The per-ticker refusals stay in
	// data_errors, where the detail belongs; what was missing was the single
	// line that says they are all the same fact.
	if avBudget != nil {
		if used, limit := avBudget.DailyBudget(); used >= limit {
			warnings = append(warnings, warning{Message: fmt.Sprintf(
				"AlphaVantage daily budget exhausted (%d of %d requests spent) — every per-ticker AlphaVantage failure in data_errors is this one spent key, not a separate problem per name; its scored news and earnings dates are missing from this run and the budget resets at 00:00 UTC",
				used, limit)})
		}
	}
	for _, c := range confab {
		for _, m := range c.messages() {
			warnings = append(warnings, warning{Message: m})
		}
	}
	// Stale prices reached data_errors and stopped there. Every entry, stop and
	// target is computed to the cent off the last close, so a stale name that
	// survives into the shipped ideas is priced off a session that has already
	// been superseded — which is a fact about the output, not about the fetch.
	if stale := staleIdeas(quantPack, ideas); len(stale) > 0 {
		warnings = append(warnings, warning{Message: fmt.Sprintf(
			"%s shipped with levels computed from a last bar that trails its own market's last completed session — re-price before acting",
			strings.Join(stale, ", "))})
	}

	warnMsgs := []string{} // serialize as [] rather than null in metadata.json
	for _, w := range warnings {
		msg := w.Message
		if w.Ticker != "" {
			msg = fmt.Sprintf("[%s] %s", w.Ticker, w.Message)
		}
		log(ch, "validation: "+msg)
		warnMsgs = append(warnMsgs, msg)
	}

	stage("synthesis", synthStart)

	meta := model.RunMeta{
		Mode:          string(cfg.Mode),
		Ticker:        cfg.Ticker,
		Indices:       indices,
		GeneratedAt:   ideas.GeneratedAt,
		Shortlist:     shortlist,
		Domains:       domainStatuses,
		Weights:       cfg.Weights,
		ThinlyCovered: thinlyCovered,
		Warnings:      warnMsgs,
		Outcome:       outcome,
		Duration:      time.Since(start).Milliseconds(),

		Engine:                  string(cfg.CheapEngine),
		EngineModel:             cheapModelName(cfg),
		SynthesisModel:          cfg.Models[model.CLIClaude],
		SynthesisFallbackEngine: synthesisFallbackEngine,
		Stages:                  stageMS,
		DataErrors:              dataErrors,
		PersonaSHA:              reg.PersonaSHA(),
		PersonaSet:              filepath.Base(cfg.AgentsDir),
	}
	if err := run.WriteMeta(meta); err != nil {
		log(ch, fmt.Sprintf("warn: write metadata.json: %v", err))
	}

	log(ch, fmt.Sprintf("Done. %d trade idea(s) produced. Outcome: %s. Artifacts: %s", len(ideas.Ideas), outcome, run.Dir))
	// The terminal event must never be dropped: send blocking (the Run
	// contract requires the caller to drain the channel).
	ch <- Event{Type: EventComplete, Ideas: ideas, Meta: &meta, Message: run.Dir}
	return nil
}

// selectIndices validates the requested index keys against the universe,
// preserving canonical order. Empty input means all indices.
// dailyBudgeted is the narrow seam for a provider whose API key carries a daily
// quota. Only AlphaVantage has one, so this is a type assertion rather than a
// method on marketdata.Provider — widening Provider would put a meaningless
// DailyBudget on five other implementations.
type dailyBudgeted interface {
	DailyBudget() (used, limit int)
}

// dailyBudgetOf returns the provider's budget accessor, or nil when the
// provider has no quota to report or no key to spend. An unconfigured key is a
// choice, not a failure, and must produce neither a log line nor a warning.
func dailyBudgetOf(p marketdata.Provider) dailyBudgeted {
	if p == nil || !p.Available() {
		return nil
	}
	b, ok := p.(dailyBudgeted)
	if !ok {
		return nil
	}
	return b
}

func selectIndices(requested []string) []string {
	all := universe.AllIndices()
	if len(requested) == 0 {
		return all
	}
	want := make(map[string]bool, len(requested))
	for _, k := range requested {
		want[strings.ToLower(strings.TrimSpace(k))] = true
	}
	var out []string
	for _, k := range all {
		if want[k] {
			out = append(out, k)
		}
	}
	return out
}

// logPackErrors surfaces market-data provider failures instead of silently
// running ungrounded. Long lists are summarized to keep the log readable.
func logPackErrors(ch chan<- Event, domain string, errs []string) {
	const show = 3
	for i, e := range errs {
		if i == show {
			log(ch, fmt.Sprintf("data[%s]: … and %d more provider errors", domain, len(errs)-show))
			break
		}
		log(ch, fmt.Sprintf("data[%s]: %s", domain, e))
	}
}

// ── Parsing helpers ─────────────────────────────────────────────────────────

// extractLastJSON pulls the last ```json ... ``` block from text.
func extractLastJSON(text string) (string, bool) {
	return parse.LastJSONBlock(text)
}

func parseIdeas(stdout string) (*model.IdeasResult, error) {
	raw, ok := extractLastJSON(stdout)
	if !ok {
		return nil, fmt.Errorf("no fenced ```json block found in chief-analyst output")
	}
	var result model.IdeasResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	return &result, nil
}

// parseScoutResult extracts a scout's candidates and validates every one of
// them against the constituent list that scout was actually handed. It returns
// the accepted candidates and the symbols it rejected.
//
// Nothing checked this before, so a symbol the model invented — or borrowed
// from another index — went straight onto the shortlist and consumed a
// data-provider slot and an analysis slot on a company the run had never
// screened. Identity (ticker casing, name, sector, index) is taken from the
// universe row rather than from the model: the scout's job is to choose, not to
// describe.
func parseScoutResult(stdout, index string, cs []model.Constituent) ([]model.Candidate, []string) {
	raw, ok := extractLastJSON(stdout)
	if !ok {
		return nil, nil
	}
	var sr model.ScoutResult
	if err := json.Unmarshal([]byte(raw), &sr); err != nil {
		return nil, nil
	}

	known := make(map[string]model.Constituent, len(cs))
	for _, c := range cs {
		known[strings.ToUpper(c.Ticker)] = c
	}

	out := make([]model.Candidate, 0, len(sr.Candidates))
	var rejected []string
	seen := map[string]bool{}
	for _, c := range sr.Candidates {
		t := strings.ToUpper(strings.TrimSpace(c.Ticker))
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		con, ok := known[t]
		if !ok {
			rejected = append(rejected, t)
			continue
		}
		out = append(out, model.Candidate{
			Ticker: con.Ticker,
			Name:   con.Name,
			Sector: con.Sector,
			Index:  index,
			Bias:   normalizeBias(c.Bias),
			Reason: strings.TrimSpace(c.Reason),
		})
	}
	return out, rejected
}

// normalizeBias maps a scout's direction onto the three values the rest of the
// pipeline understands. The merit merge reads it to decide which end of the
// pre-screen composite a nomination is strong at, so an unrecognised string has
// to become neutral rather than travel on as a fourth, meaningless direction.
func normalizeBias(b model.Bias) model.Bias {
	switch model.Bias(strings.ToLower(strings.TrimSpace(string(b)))) {
	case model.BiasBullish:
		return model.BiasBullish
	case model.BiasBearish:
		return model.BiasBearish
	default:
		return model.BiasNeutral
	}
}

// prefixed labels a domain's provider errors so they stay attributable once
// merged into the run-level list.
func prefixed(domain string, errs []string) []string {
	if len(errs) == 0 {
		return nil
	}
	out := make([]string, 0, len(errs))
	for _, e := range errs {
		out = append(out, domain+": "+e)
	}
	return out
}

// cheapModelName is the model the cheap-research roles actually ran on, which
// differs by engine: the CLI engines take it from Models, the HTTP engines from
// whichever APIConfig the selector resolved to.
func cheapModelName(cfg Config) string {
	switch cfg.CheapEngine {
	case model.CLIApi:
		return cfg.API.Model
	case model.CLILocal:
		return cfg.Local.Model
	default:
		return cfg.Models[model.CLIGemini]
	}
}

// collapseOverlappingNominations rewrites Nominations to count *independent*
// readings, and returns the candidates it changed so the run log can say so.
//
// The merit sort pays meritAgreementBonus per extra nomination, and the reason
// it gives is that "two scouts reaching the same name from different index
// tables is independent evidence". That holds only where the tables are drawn
// from different pools. 35 of the 56 names in nq100 are also in sp500 — MU,
// PANW, QCOM, SNPS, NVDA, META and thirty more — so for those names the two
// scouts are choosing the same ticker out of two overlapping candidate pools
// built from one price history. The composite differs between the two rows only
// because it is standardised within each index; the underlying evidence is one
// series, read twice.
//
// So a name every nominating index holds is one reading however many scouts
// mention it. On 2026-09-05 all four dual-nominated names were in that overlap,
// each collected a bonus for it, and the shortlist came out seven-twelfths
// Information Technology against a book limit of two ideas per sector.
//
// A nomination from an index that does *not* hold the ticker would be genuinely
// separate, and still counts — parseScoutResult drops off-list nominations, so
// today that cannot arise, but the rule is about independence rather than about
// which validation happens to run upstream.
func collapseOverlappingNominations(uni *universe.Universe, shortlist []model.Candidate) []model.Candidate {
	if uni == nil {
		return nil
	}
	var changed []model.Candidate
	for i := range shortlist {
		c := &shortlist[i]
		if len(c.NominatedBy) < 2 {
			continue
		}
		// One reading for the pool they share, plus one for each index that
		// could not have been reading it.
		n := 1
		for _, idx := range c.NominatedBy {
			if !uni.Contains(idx, c.Ticker) {
				n++
			}
		}
		if n >= c.Nominations {
			continue
		}
		c.Nominations = n
		changed = append(changed, *c)
	}
	return changed
}

// shortlistPerSector is how many names of one sector may reach the specialists,
// derived from the number the risk gate will let into the book.
//
// One more than the gate allows, deliberately. Matching the gate exactly would
// hand it a shortlist with no slack: every sector would arrive at its limit and
// the gate could only accept or shrink the book, never choose between two names
// of the same sector on the research it just paid for. One spare per sector is
// what makes the gate's cap a selection rather than a truncation.
//
// It is derived rather than configured separately because the two numbers answer
// one question, and on 2026-09-05 they disagreed: the funnel had no sector notion
// at all, returned a shortlist seven-twelfths Information Technology, and the
// gate's limit of two then cut the book to two ideas.
func shortlistPerSector(gate int) int {
	if gate <= 0 {
		return 0 // the gate is disabled; so is the funnel's cap
	}
	return gate + 1
}
