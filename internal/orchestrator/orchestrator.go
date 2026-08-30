// Package orchestrator drives the two-stage (or three-stage) analysis pipeline.
// It spawns CLI subprocesses via a bounded worker pool and emits progress events
// over a channel consumed by the TUI. It never imports tui types.
package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/agents"
	"github.com/mamut/claude-financial-researcher/internal/marketdata"
	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/parse"
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
	Mode      model.Mode
	Ticker    string   // single-stock mode only
	Indices   []string // independent mode: index keys to screen; empty = all
	AgentsDir string   // path to agents/*.md
	RunsDir   string   // base directory for run artifacts (default "runs")
	DataDir   string   // path for cached market data
	Workers   int      // bounded pool size (default 4)

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
	// MaxShortlist caps the merged shortlist that reaches the specialists. Zero
	// means 12.
	MaxShortlist int
	// MaxPerIndex caps how many of those names one index may contribute before
	// the merit backfill. Zero means 5.
	MaxPerIndex int
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
}

func (c *Config) applyDefaults() {
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
	if c.API.Model == "" {
		c.API.Model = "deepseek-chat"
	}
	// Local defaults target a stock Ollama install; the model must be set by the
	// user (it depends on what they have pulled), so it has no default.
	if c.Local.BaseURL == "" {
		c.Local.BaseURL = "http://localhost:11434/v1"
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
	c.Risk = riskDefaults(c.Risk)
	// Ten points is roughly one confidence band in scoring.md's calibration
	// table: enough for the Chief to express a real cross-domain read the
	// arithmetic cannot see, not enough to overwrite it.
	if c.ChiefAdjustBand <= 0 {
		c.ChiefAdjustBand = 10
	}

	// Default Timeouts
	if c.Timeouts.Screening == 0 {
		c.Timeouts.Screening = 5 * time.Minute
	}
	if c.Timeouts.Analysis == 0 {
		c.Timeouts.Analysis = 5 * time.Minute
	}
	if c.Timeouts.Synthesis == 0 {
		c.Timeouts.Synthesis = 5 * time.Minute
	}

	// Default Retry Policy
	if c.Retry.MaxAttempts <= 0 {
		c.Retry.MaxAttempts = 2 // 1 retry
	}
	if c.Retry.BaseDelay == 0 {
		c.Retry.BaseDelay = 1 * time.Second
	}

	// Default weights (matches scoring.md), mapped to the 5–20 day horizon this
	// system actually trades. Fundamentals led at 0.30 for no reason connected
	// to the holding period: a rich multiple says little about the next three
	// weeks, and it is the worst-covered domain (US filers only, quarterly, and
	// often stale). Quant is the only domain covered for every name, computed
	// rather than recalled, and measured over exactly this horizon.
	if c.Weights.Quant == 0 && c.Weights.News == 0 && c.Weights.Fundamentals == 0 && c.Weights.Macro == 0 && c.Weights.Sentiment == 0 {
		c.Weights = model.DomainWeights{
			Quant:        0.35,
			News:         0.25,
			Fundamentals: 0.15,
			Sentiment:    0.15,
			Macro:        0.10,
		}
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

// Run executes the full pipeline and streams Events. It closes the returned
// channel when the run completes (success or error). The caller must drain it.
func Run(ctx context.Context, cfg Config) <-chan Event {
	ch := make(chan Event, 64)
	go func() {
		defer close(ch)
		cfg.applyDefaults()
		if err := run(ctx, cfg, ch); err != nil {
			ch <- Event{Type: EventError, Message: err.Error()}
		}
	}()
	return ch
}

func emit(ch chan<- Event, e Event) {
	select {
	case ch <- e:
	default: // drop if the TUI is too slow; never block pipeline
	}
}

func log(ch chan<- Event, msg string) {
	emit(ch, Event{Type: EventLog, Message: msg})
}

func agentStatus(ch chan<- Event, role string, status model.AgentStatus, r *model.Report) {
	emit(ch, Event{Type: EventStatus, Agent: role, Status: status, Report: r})
}

func run(ctx context.Context, cfg Config, ch chan<- Event) error {
	start := time.Now()

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
	// What the cheap engine can actually do. Every scout/specialist prompt states
	// this, and reports are checked against it afterwards — the personas assume a
	// search-capable CLI, which is false on the HTTP engine.
	cheapCaps := agents.Capabilities{WebSearch: engineWebSearch(cheapCLI)}
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
	run, err := store.New(cfg.RunsDir)
	if err != nil {
		return fmt.Errorf("create run dir: %w", err)
	}
	log(ch, fmt.Sprintf("Run directory: %s", run.Dir))
	_ = store.CleanupOldRuns(cfg.RunsDir, cfg.KeepRuns)

	// Start worker pool
	p := newPool(cfg.Workers, cfg.Models, cfg.Binaries, cheapAPI, cheapCLI, cheapConc)
	p.start(ctx)
	defer p.stop()

	// Initialize market data service
	cache := marketdata.NewCache(cfg.DataDir)
	// Cache keys are date-scoped, so yesterday's entries can never be read
	// again — but nothing removed them, and a universe-wide pre-screen writes a
	// few hundred files a day.
	if cfg.DataCacheDays > 0 {
		if n, err := cache.Prune(time.Duration(cfg.DataCacheDays) * 24 * time.Hour); err != nil {
			log(ch, fmt.Sprintf("warn: prune data cache: %v", err))
		} else if n > 0 {
			log(ch, fmt.Sprintf("Pruned %d cached data file(s) older than %d days", n, cfg.DataCacheDays))
		}
	}
	dataSvc := marketdata.NewService(
		cache,
		marketdata.NewEdgarProvider(cfg.Providers.ContactEmail, cache),
		marketdata.NewAlphaVantageProvider(cfg.Providers.AlphaVantageKey, cfg.DataDir),
		marketdata.NewYahooOptionsProvider(),
		marketdata.NewFredProvider(cfg.Providers.FredKey),
	)

	var shortlist []model.Candidate
	var domainStatuses []model.DomainStatus

	// Per-stage wall clock and every provider failure, for metadata.json. Only
	// per-agent durations were kept before, so the in-process stages — which are
	// most of a run's wall time — were entirely unattributed, and provider
	// errors lived only in data/<domain>.json.
	stageMS := map[string]int64{}
	var dataErrors []string
	stage := func(name string, t time.Time) { stageMS[name] = time.Since(t).Milliseconds() }

	yc := marketdata.NewYahooClient(cache)
	yc.SetPriceTTL(cfg.PriceTTL)

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
	prescreenParams.ADVMinUSD = cfg.Risk.ADVMinUSD
	var prescreen *Prescreen
	if cfg.Mode == model.ModeIndependent {
		prescreenStart := time.Now()
		total := 0
		for _, idx := range indices {
			total += len(uni.Constituents(idx))
		}
		log(ch, fmt.Sprintf("Stage 0.5: pre-screening %d names across %s (cold fetch ~%ds, cached ~0s)…",
			total, strings.Join(indices, ", "), total/4))
		prescreen = runPrescreen(ctx, ch, yc, uni, indices, prescreenParams)
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
				PrescreenTable:       prescreen.Table(idx, prescreenParams.TopPerIndex, prescreenParams.BottomPerIndex),
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
				Duration: r.Duration, Attempts: r.Attempts, Tokens: r.Tokens,
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
		before := len(shortlist)
		// Merit, not round-robin: keep the nominations the pre-screen composite
		// agrees with, in the direction they were nominated in.
		shortlist = universe.CapMerit(shortlist, cfg.MaxShortlist, cfg.MaxPerIndex,
			func(c model.Candidate) float64 { return meritScore(prescreen, c) })
		if len(shortlist) < before {
			log(ch, fmt.Sprintf("Shortlist trimmed %d → %d by pre-screen merit (max %d per index)",
				before, len(shortlist), cfg.MaxPerIndex))
		}
		for _, c := range shortlist {
			log(ch, fmt.Sprintf("shortlist: %s (%s, %s) — scout %s, merit %+.2f",
				c.Ticker, c.Index, c.Sector, c.Bias, meritScore(prescreen, c)))
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

	// SEC and AlphaVantage are both US-only, so the non-US half of a balanced
	// shortlist can only ever be graded on quant. Say so once, plainly: nothing
	// in the run said it, and three domains reporting gaps for those names read
	// as three domains failing.
	quantOnly := quantOnlyNames(shortlist)
	if len(quantOnly) > 0 {
		log(ch, fmt.Sprintf("%d of %d shortlisted names are non-US listings (%s): US filings, news and sentiment do not cover them, so they are graded on quant alone.",
			len(quantOnly), len(shortlist), strings.Join(quantOnly, ", ")))
	}

	stage("screening", scoutStart)

	// ── Stage 1.5: price history + quant metrics (in-process, no model) ───────
	quantStart := time.Now()
	log(ch, "Stage 1.5: fetching price history and computing quant metrics…")
	agentStatus(ch, "quant-data", model.StatusRunning, nil)
	// The pre-screen already fetched every one of these series; the shared cache
	// serves them back here without a second round trip.
	quantPack, quantSeries := buildQuantPack(ctx, ch, run, yc, shortlist)
	logPackErrors(ch, "quant", quantPack.Errors)
	dataErrors = append(dataErrors, prefixed("quant", quantPack.Errors)...)
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
	if quantPack.AsOf != "" {
		verifiedDates[quantPack.AsOf] = true
	}
	specChans := make([]<-chan model.Report, len(specialists))
	grounded := make([]bool, len(specialists))
	ungrounded := make([][]string, len(specialists))
	citable := make([]map[string]bool, len(specialists))
	tickers := make([]string, len(shortlist))
	for j, c := range shortlist {
		tickers[j] = c.Ticker
	}
	for i, sp := range specialists {
		pack := dataSvc.BuildPack(ctx, sp.role, tickers)
		// The verified earnings dates ride in on the news pack; the validator and
		// the risk checks read the same map the news prompt renders.
		for t, d := range pack.EventDates {
			eventDates[t] = d
		}
		// Every date this run actually collected, so a date in the final output
		// that appears nowhere here can be recognised for what it is.
		for _, td := range pack.ByTicker {
			for _, f := range td.Facts {
				if !f.AsOf.IsZero() {
					verifiedDates[f.AsOf.UTC().Format("2006-01-02")] = true
				}
			}
		}
		for _, f := range pack.MacroFacts {
			if !f.AsOf.IsZero() {
				verifiedDates[f.AsOf.UTC().Format("2006-01-02")] = true
			}
		}
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

		// Assemble the verified-data block per role: the quant specialist gets
		// the full computed pack; news/sentiment get compact price context so
		// their narratives stay anchored to real closes.
		dataBlock := pack.Markdown()
		switch sp.role {
		case "quant":
			if qmd := quantPack.Markdown(); qmd != "" {
				// Append rather than replace: replacing made quant the only
				// specialist that lost the macro backdrop every other role got.
				dataBlock = qmd
				if mm := pack.MacroMarkdown(); mm != "" {
					dataBlock += "\n### Verified macro backdrop\n\n" + mm
				}
			}
		case "news", "sentiment", "fundamentals":
			// Fundamentals needs the price to say anything about a multiple:
			// without it the domain could report a revenue figure but never a
			// P/E, and "expensive" was an assertion about a number it had not
			// been shown.
			if cb := quantPack.CompactBlock(); cb != "" {
				dataBlock += "\n### Verified price context (computed from daily OHLCV)\n\n" + cb
			}
		case "macro":
			// The macro domain's question at this horizon is the market regime,
			// and the benchmark prices answer it. FRED alone never could.
			if rb := quantPack.RegimeBlock(); rb != "" {
				dataBlock += "\n" + rb
			}
		}

		// Grounded means we injected *per-ticker* verified data for this domain.
		// Judging it from dataBlock != "" was misleading: macro facts and the
		// shared price context are appended to every role, so a sentiment pack
		// with an empty ByTicker still counted as grounded.
		grounded[i] = groundedFor(sp.role, pack, quantPack)
		ungrounded[i] = ungroundedFor(sp.role, pack, quantPack, tickers)
		if len(ungrounded[i]) > 0 {
			log(ch, fmt.Sprintf("data[%s]: no verified data for %d/%d tickers: %s",
				sp.role, len(ungrounded[i]), len(tickers), strings.Join(ungrounded[i], ", ")))
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

		// Computed coverage is the authority, not the agent's own `missing`
		// array: delete scores for names this domain had no data for, or that
		// were never on the shortlist, and say so in the tail. Done before the
		// report is written so the artifact is exactly what the Chief read.
		var enf enforcement
		if r.Status != model.StatusFailed {
			corrected, e, err := enforceSpecialistTail(sp.role, r.Stdout, ungrounded[i], tickers)
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
				if len(enf.Corrected) > 0 {
					log(ch, fmt.Sprintf("warn: %s scored %d ticker(s) it had no verified data for (%s) — scores removed, names moved to `missing`",
						sp.role, len(enf.Corrected), strings.Join(enf.Corrected, ", ")))
				}
				if len(enf.OffShortlist) > 0 {
					log(ch, fmt.Sprintf("warn: %s scored %d ticker(s) not on the shortlist (%s) — scores removed",
						sp.role, len(enf.OffShortlist), strings.Join(enf.OffShortlist, ", ")))
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
			Domain:              sp.role,
			Status:              r.Status,
			Err:                 r.Err,
			Duration:            r.Duration,
			Attempts:            r.Attempts,
			Tokens:              r.Tokens,
			Grounded:            grounded[i],
			Ungrounded:          ungrounded[i],
			CorrectedScores:     enf.Corrected,
			OffShortlistScores:  enf.OffShortlist,
			FabricatedCitations: fabricated,
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
	bases := computeBaseScores(cfg.Weights, specReports, shortlist)

	// What this pipeline has actually achieved, measured by replaying its own
	// past ideas. It reaches the Chief as context for how hard to lean on
	// today's evidence, and the risk gate as the edge its expectancy check
	// assumes — replacing a prior with a measurement.
	cal := trackRecord(ctx, ch, cfg, yc)
	verifiedCtx := verified{
		Universe:  uni,
		Quant:     quantPack,
		Shortlist: shortlist,
		Bases:     bases,
		Events:    eventDates,
		Dates:     verifiedDates,
		Series:    quantSeries,
	}
	if cal != nil && cal.NClosed > 0 {
		log(ch, fmt.Sprintf("track record: %d closed idea(s), %.0f%% profitable, avg %+.2fR",
			cal.NClosed, cal.WinRate*100, cal.AvgR))
		if err := cal.Save(run.Dir); err != nil {
			log(ch, fmt.Sprintf("warn: copy the track record into the run: %v", err))
		}
	}
	if r, ok := cal.RealizedEdge(); ok {
		verifiedCtx.RealizedR = &r
		log(ch, fmt.Sprintf("risk: expectancy assumes the measured %+.2fR edge, not the %.2fσ prior",
			r, riskDefaults(cfg.Risk).EdgeSigmaDaily))
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
		QuantBlock:       quantPack.CompactBlock() + regimeSuffix(quantPack),
		BaseScoreBlock:   baseScoreBlock(bases, cfg.ChiefAdjustBand),
		TrackRecordBlock: cal.Block(),
	})
	if err != nil {
		return fmt.Errorf("assemble chief-analyst prompt: %w", err)
	}

	agentStatus(ch, "chief-analyst", model.StatusRunning, nil)
	r := runAgent(ctx, model.CLIClaude, "chief-analyst", string(model.StageSynthesis), prompt, cfg.Timeouts.Synthesis, cfg.Retry, cfg.Models[model.CLIClaude], cfg.Binaries[model.CLIClaude], model.APIConfig{})
	r.Path = fmt.Sprintf("%s/chief-analyst.md", run.Dir)
	if err := run.WriteReport("chief-analyst", r.Stdout); err != nil {
		log(ch, fmt.Sprintf("warn: write chief-analyst report: %v", err))
	}
	// The synthesis call is the single most expensive step in the run and had
	// no row of its own; only the five specialists were accounted for.
	domainStatuses = append(domainStatuses, model.DomainStatus{
		Domain: "chief-analyst", Status: r.Status, Err: r.Err,
		Duration: r.Duration, Attempts: r.Attempts, Tokens: r.Tokens,
	})

	var ideas *model.IdeasResult
	var warnings []warning
	outcome := "complete"
	if len(covGaps) > 0 {
		outcome = "degraded"
	}

	if r.Status == model.StatusFailed {
		agentStatus(ch, "chief-analyst", model.StatusFailed, &r)
		log(ch, "Chief analyst failed — building degraded fallback from specialist scores…")
		ideas = buildDegradedIdeas(cfg, specReports, shortlist)
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
			// risk-gate violation.
			var repromptReasons []string
			for _, f := range findings {
				log(ch, "risk: "+f.Message)
				repromptReasons = append(repromptReasons, f.Message)
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
				reprompt := prompt + "\n\nCRITICAL: Your previous output failed validation. " + strings.Join(repromptReasons, "; ") + ". Re-emit the full JSON block with these problems fixed."
				r = runAgent(ctx, model.CLIClaude, "chief-analyst", string(model.StageSynthesis), reprompt, cfg.Timeouts.Synthesis, cfg.Retry, cfg.Models[model.CLIClaude], cfg.Binaries[model.CLIClaude], model.APIConfig{})
				if r.Status != model.StatusFailed {
					if newIdeas, err := parseIdeas(r.Stdout); err == nil {
						ideas = newIdeas
						warnings = validateIdeas(ideas, cfg, verifiedCtx)
						findings = applyRiskGate(ideas, verifiedCtx, cfg.Risk)
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
				log(ch, fmt.Sprintf("Risk gate dropped %d idea(s) that a corrective re-prompt did not fix", len(dropped)))
				for _, d := range dropped {
					log(ch, "dropped: "+d)
				}
				ideas.Notes = strings.TrimSpace(ideas.Notes + fmt.Sprintf(
					" Risk gate dropped %d idea(s) after one corrective re-prompt: %s. Fewer ideas is the intended outcome — an unsound construction is worse than none.",
					len(dropped), strings.Join(dropped, "; ")))
			}
		} else {
			log(ch, fmt.Sprintf("warn: parse ideas JSON: %v", parseErr))
			ideas = buildDegradedIdeas(cfg, specReports, shortlist)
			ideas.Notes = fmt.Sprintf("JSON parse error: %v — %s", parseErr, ideas.Notes)
			outcome = "degraded"
		}
	}

	// Record the verified price each idea was generated at (scoreboard baseline).
	for i := range ideas.Ideas {
		if m, ok := quantPack.ByTicker[strings.ToUpper(ideas.Ideas[i].Ticker)]; ok && m.LastClose > 0 {
			ideas.Ideas[i].PriceAtGeneration = m.LastClose
		}
	}

	// Final persistence and metadata
	if err := run.WriteIdeas(ideas); err != nil {
		log(ch, fmt.Sprintf("warn: write ideas.json: %v", err))
	}

	for _, g := range covGaps {
		warnings = append(warnings, warning{
			Message: fmt.Sprintf("%s: no verified data for %s — names it could have covered", g.Domain, strings.Join(g.Missing, ", ")),
		})
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
		Mode:        string(cfg.Mode),
		Ticker:      cfg.Ticker,
		Indices:     indices,
		GeneratedAt: ideas.GeneratedAt,
		Shortlist:   shortlist,
		Domains:     domainStatuses,
		Weights:     cfg.Weights,
		QuantOnly:   quantOnly,
		Warnings:    warnMsgs,
		Outcome:     outcome,
		Duration:    time.Since(start).Milliseconds(),

		Engine:         string(cfg.CheapEngine),
		EngineModel:    cheapModelName(cfg),
		SynthesisModel: cfg.Models[model.CLIClaude],
		Stages:         stageMS,
		DataErrors:     dataErrors,
		PersonaSHA:     reg.PersonaSHA(),
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
